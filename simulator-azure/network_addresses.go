package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// A virtual network is one engine network over the first IPv4 prefix of its
// address space (azure_workload_networks.go), created when the first workload
// attaches to it and removed with the network. A subnet is an address ledger over its own prefix: every
// network interface, platform-owned or not, holds a lease on it, and the
// stored interfaces rebuild the ledger after a restart.

// azureSubnetReservation is what Azure Virtual Network keeps in every subnet:
// the network address, the default gateway, the two addresses Azure DNS maps
// into the virtual network, and the broadcast address.
var azureSubnetReservation = ipam.HostReservation{First: 4, Last: 1}

const azureMACPrefix = "02:15:5d"

func azureNICMAC(nicID string) string {
	return ipam.DeriveMAC(azureMACPrefix, nicID)
}

func formatAzureMAC(mac string) string {
	return strings.ToUpper(strings.ReplaceAll(mac, ":", "-"))
}

func azureVNetIDOfSubnet(subnetID string) string {
	return strings.Split(subnetID, "/subnets/")[0]
}

// dockerNetForVNet names the engine network realizing a virtual network: its
// name for the operator, and a digest of its resource id so two virtual
// networks of one name in different resource groups or subscriptions are
// two networks.
func dockerNetForVNet(vnetID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(vnetID)))
	return "sim-vnet-" + strings.ToLower(vnetID[strings.LastIndex(vnetID, "/")+1:]) + "-" + hex.EncodeToString(sum[:4])
}

// azureVNetIPv4Prefix is the prefix the virtual network's engine network
// spans: the first IPv4 member of addressSpace.addressPrefixes.
func azureVNetIPv4Prefix(vnet VirtualNetwork) (netip.Prefix, error) {
	for _, raw := range vnet.Properties.AddressSpace.AddressPrefixes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("virtual network %s: address prefix %q: %w", vnet.ID, raw, err)
		}
		if prefix.Addr().Is4() {
			return prefix, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("virtual network %s has no IPv4 address prefix", vnet.ID)
}

// azureSubnetIPv4CIDR is the prefix a subnet leases addresses from: its
// addressPrefix, or the first IPv4 member of addressPrefixes.
func azureSubnetIPv4CIDR(properties SubnetProperties) (string, error) {
	prefixes := make([]string, 0, len(properties.AddressPrefixes)+1)
	if properties.AddressPrefix != "" {
		prefixes = append(prefixes, properties.AddressPrefix)
	}
	prefixes = append(prefixes, properties.AddressPrefixes...)
	selected := ""
	for _, prefix := range prefixes {
		ip, _, err := net.ParseCIDR(prefix)
		if err != nil {
			return "", fmt.Errorf("invalid subnet address prefix %q: %w", prefix, err)
		}
		if selected == "" && ip.To4() != nil {
			selected = prefix
		}
	}
	if selected != "" {
		return selected, nil
	}
	return "", fmt.Errorf("subnet requires an IPv4 addressPrefix or addressPrefixes member")
}

// azureSubnetLedger leases one subnet's addresses and remembers which owner
// holds each, so an owner re-requesting its own address keeps it and a
// release by another owner leaves it alone.
type azureSubnetLedger struct {
	cidr   string
	ipam   *ipam.IPAM
	owners map[string]string
}

var azureSubnetLedgers = struct {
	sync.Mutex
	bySubnet map[string]*azureSubnetLedger
}{bySubnet: map[string]*azureSubnetLedger{}}

// azureSubnetLedgerFor returns the subnet's ledger, building it from the
// stored subnet and seeding it with every stored address in the subnet when
// this process has not used the subnet yet. Caller holds azureSubnetLedgers.
func azureSubnetLedgerFor(subnetID string) (*azureSubnetLedger, error) {
	subnet, ok := azureSubnetByID(subnetID)
	if !ok {
		return nil, &azureAddressFault{
			code:    "ResourceNotFound",
			status:  http.StatusNotFound,
			message: fmt.Sprintf("The Resource %q was not found.", subnetID),
		}
	}
	cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(subnet.ID)
	if ledger := azureSubnetLedgers.bySubnet[key]; ledger != nil && ledger.cidr == cidr {
		return ledger, nil
	}
	allocator, err := ipam.NewIPAMWithReserved(cidr, ipam.FirstHostGateway(cidr), azureSubnetReservation)
	if err != nil {
		return nil, fmt.Errorf("subnet %s: %w", subnet.ID, err)
	}
	ledger := &azureSubnetLedger{cidr: cidr, ipam: allocator, owners: map[string]string{}}
	for owner, address := range azureStoredSubnetAddresses(subnet.ID) {
		if _, err := ledger.lease(owner, address); err != nil {
			return nil, fmt.Errorf("rebuild the address ledger of subnet %s from %s: %w", subnet.ID, owner, err)
		}
	}
	azureSubnetLedgers.bySubnet[key] = ledger
	return ledger, nil
}

// azureStoredSubnetAddresses lists every address the store records in the
// subnet, by the IP configuration or frontend that holds it.
func azureStoredSubnetAddresses(subnetID string) map[string]net.IP {
	held := map[string]net.IP{}
	if azureNICs != nil {
		for _, nic := range azureNICs.List() {
			for _, ipcfg := range nic.Properties.IPConfigurations {
				if ipcfg.Properties.Subnet == nil || !strings.EqualFold(ipcfg.Properties.Subnet.ID, subnetID) {
					continue
				}
				if ip := net.ParseIP(ipcfg.Properties.PrivateIPAddress); ip != nil {
					held[ipcfg.ID] = ip
				}
			}
		}
	}
	if azureApplicationGateways != nil {
		for _, gw := range azureApplicationGateways.List() {
			for _, fe := range gw.Properties.FrontendIPConfigurations {
				if fe.Properties.Subnet == nil || !strings.EqualFold(fe.Properties.Subnet.ID, subnetID) {
					continue
				}
				if ip := net.ParseIP(fe.Properties.PrivateIPAddress); ip != nil {
					held[applicationGatewayFrontendNICID(gw.ID, fe.Name)] = ip
				}
			}
		}
	}
	return held
}

func (l *azureSubnetLedger) lease(owner string, requested net.IP) (net.IP, error) {
	if requested != nil {
		if current, held := l.owners[requested.String()]; held && current == owner {
			return requested, nil
		}
	}
	ip, err := l.ipam.Reserve(owner, requested)
	if err != nil {
		return nil, err
	}
	l.owners[ip.String()] = owner
	return ip, nil
}

func (l *azureSubnetLedger) release(owner string, ip net.IP) {
	if ip == nil || l.owners[ip.String()] != owner {
		return
	}
	l.ipam.Release(ip)
	delete(l.owners, ip.String())
}

// azureAddressFault is an address lease the Network resource provider refuses
// with a request error of its own.
type azureAddressFault struct {
	code    string
	status  int
	message string
}

func (f *azureAddressFault) Error() string { return f.message }

// azureLeaseSubnetAddress leases an address in the subnet to owner: the
// requested one, or the next free one. A requested address another owner holds
// fails with ipam.ErrAddressInUse; a subnet with none left with
// ipam.ErrNoAvailableIP.
func azureLeaseSubnetAddress(subnetID, owner string, requested net.IP) (net.IP, error) {
	azureSubnetLedgers.Lock()
	defer azureSubnetLedgers.Unlock()
	ledger, err := azureSubnetLedgerFor(subnetID)
	if err != nil {
		return nil, err
	}
	return ledger.lease(owner, requested)
}

// azureReleaseSubnetAddress returns owner's lease on ip to the subnet. An
// address owner does not hold, or a subnet no longer stored, is left alone.
func azureReleaseSubnetAddress(subnetID, owner string, ip net.IP) {
	azureSubnetLedgers.Lock()
	defer azureSubnetLedgers.Unlock()
	ledger, err := azureSubnetLedgerFor(subnetID)
	if err != nil {
		return
	}
	ledger.release(owner, ip)
}

// azureForgetSubnetLedger drops a subnet's ledger, for a subnet that is
// deleted or whose prefix changed; the next lease rebuilds it from the store.
func azureForgetSubnetLedger(subnetID string) {
	azureSubnetLedgers.Lock()
	defer azureSubnetLedgers.Unlock()
	delete(azureSubnetLedgers.bySubnet, strings.ToLower(subnetID))
}

// azureWriteAddressLeaseError answers a network interface write whose address
// lease the subnet refused, with the Network resource provider's own codes.
func azureWriteAddressLeaseError(w http.ResponseWriter, nicID string, err error) {
	var fault *azureAddressFault
	switch {
	case errors.As(err, &fault):
		AzureError(w, fault.code, fault.message, fault.status)
	case errors.Is(err, ipam.ErrAddressInUse):
		AzureErrorf(w, "PrivateIPAddressInUse", http.StatusBadRequest,
			"An IP configuration of network interface %s requests a private IP address that is already allocated to another resource: %v", nicID, err)
	case errors.Is(err, ipam.ErrNoAvailableIP):
		AzureErrorf(w, "SubnetIsFull", http.StatusBadRequest,
			"The subnet of network interface %s does not have enough capacity for another IP address: %v", nicID, err)
	default:
		AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
			"An IP configuration of network interface %s cannot take its address: %v", nicID, err)
	}
}

// azurePrimaryIPConfigIndex is the IP configuration Azure treats as the
// interface's primary: the one marked primary, or the first when none is.
func azurePrimaryIPConfigIndex(nic NetworkInterface) int {
	for i, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Primary {
			return i
		}
	}
	return 0
}

func azurePrimaryIPConfig(nic NetworkInterface) NetworkInterfaceIPConfiguration {
	return nic.Properties.IPConfigurations[azurePrimaryIPConfigIndex(nic)]
}

// azureNICSecondaryAddresses lists the private addresses of the interface's
// secondary IP configurations.
func azureNICSecondaryAddresses(nic NetworkInterface) []net.IP {
	primary := azurePrimaryIPConfigIndex(nic)
	var out []net.IP
	for i, ipcfg := range nic.Properties.IPConfigurations {
		if i == primary {
			continue
		}
		if ip := net.ParseIP(ipcfg.Properties.PrivateIPAddress); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// azureLeaseNICAddresses gives every IP configuration of nic an address in its
// subnet and writes it into the configuration. A configuration keeps the
// address it held in prev, the interface as stored before this write, unless
// it asks for another static one; the addresses of configurations nic no
// longer has go back to the subnet first, so a new configuration may take
// one of them. A write that fails leaves prev's leases as they were.
func azureLeaseNICAddresses(nic *NetworkInterface, prev *NetworkInterface) error {
	type lease struct {
		subnetID, owner string
		ip              net.IP
	}
	held := map[string]lease{}
	if prev != nil {
		for _, ipcfg := range prev.Properties.IPConfigurations {
			ip := net.ParseIP(ipcfg.Properties.PrivateIPAddress)
			if ipcfg.Properties.Subnet == nil || ip == nil {
				continue
			}
			held[strings.ToLower(ipcfg.Name)] = lease{subnetID: ipcfg.Properties.Subnet.ID, owner: ipcfg.ID, ip: ip}
		}
	}
	kept := map[string]bool{}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		name := strings.ToLower(ipcfg.Name)
		before, was := held[name]
		if !was || !strings.EqualFold(before.subnetID, ipcfg.Properties.Subnet.ID) {
			continue
		}
		if strings.EqualFold(ipcfg.Properties.PrivateIPAllocationMethod, "Static") && ipcfg.Properties.PrivateIPAddress != before.ip.String() {
			continue
		}
		kept[name] = true
	}

	azureSubnetLedgers.Lock()
	defer azureSubnetLedgers.Unlock()
	ledgers := map[string]*azureSubnetLedger{}
	ledgerFor := func(subnetID string) (*azureSubnetLedger, error) {
		key := strings.ToLower(subnetID)
		if ledger := ledgers[key]; ledger != nil {
			return ledger, nil
		}
		ledger, err := azureSubnetLedgerFor(subnetID)
		if err != nil {
			return nil, err
		}
		ledgers[key] = ledger
		return ledger, nil
	}
	var released, leased []lease
	for name, before := range held {
		if kept[name] {
			continue
		}
		if ledger, err := ledgerFor(before.subnetID); err == nil {
			ledger.release(before.owner, before.ip)
			released = append(released, before)
		}
	}
	// Nothing runs between a release and its re-lease under the lock, so a
	// restore of what this write released cannot be refused.
	restore := func(err error) error {
		for _, l := range leased {
			ledgers[strings.ToLower(l.subnetID)].release(l.owner, l.ip)
		}
		for _, l := range released {
			if _, rerr := ledgers[strings.ToLower(l.subnetID)].lease(l.owner, l.ip); rerr != nil {
				err = errors.Join(err, fmt.Errorf("restore %s to %s: %w", l.ip, l.owner, rerr))
			}
		}
		return err
	}
	for i := range nic.Properties.IPConfigurations {
		ipcfg := &nic.Properties.IPConfigurations[i]
		name := strings.ToLower(ipcfg.Name)
		var requested net.IP
		if strings.EqualFold(ipcfg.Properties.PrivateIPAllocationMethod, "Static") {
			if requested = net.ParseIP(ipcfg.Properties.PrivateIPAddress); requested == nil {
				return restore(&azureAddressFault{
					code:   "InvalidParameter",
					status: http.StatusBadRequest,
					message: fmt.Sprintf("IP configuration %s requests the invalid static private IP address %q.",
						ipcfg.ID, ipcfg.Properties.PrivateIPAddress),
				})
			}
		} else if kept[name] {
			requested = held[name].ip
		}
		ledger, err := ledgerFor(ipcfg.Properties.Subnet.ID)
		if err != nil {
			return restore(err)
		}
		ip, err := ledger.lease(ipcfg.ID, requested)
		if err != nil {
			return restore(fmt.Errorf("lease an address for IP configuration %s: %w", ipcfg.Name, err))
		}
		if !kept[name] {
			leased = append(leased, lease{subnetID: ipcfg.Properties.Subnet.ID, owner: ipcfg.ID, ip: ip})
		}
		ipcfg.Properties.PrivateIPAddress = ip.String()
	}
	return nil
}

// azureReleaseNICAddresses returns every address the interface holds.
func azureReleaseNICAddresses(nic NetworkInterface) {
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil {
			continue
		}
		azureReleaseSubnetAddress(ipcfg.Properties.Subnet.ID, ipcfg.ID, net.ParseIP(ipcfg.Properties.PrivateIPAddress))
	}
}
