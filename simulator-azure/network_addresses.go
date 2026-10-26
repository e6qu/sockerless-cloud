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

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// A virtual network is one engine network per IPv4 prefix of its address space
// (azure_workload_networks.go), created when the first workload attaches. A
// subnet is an address ledger over its prefix: every network interface holds a
// lease on it, and the stored interfaces rebuild the ledger after a restart.

// Azure keeps the network address, the default gateway, the two addresses
// Azure DNS maps into the virtual network, and the broadcast address in every
// subnet.
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

// The name for the operator and a digest of the resource ID and workload scope,
// so same-named virtual networks in different groups or subscriptions, or in
// two simulators on one engine, are distinct networks.
func dockerNetForVNet(vnetID string) string {
	sum := sha256.Sum256([]byte(sim.WorkloadScope() + "\x00" + strings.ToLower(vnetID)))
	return "sim-vnet-" + strings.ToLower(vnetID[strings.LastIndex(vnetID, "/")+1:]) + "-" + hex.EncodeToString(sum[:4])
}

// The network's own name for its first prefix, plus a digest for each later
// one: the bridge driver spans one range per network.
func dockerNetForVNetPrefix(vnetID string, prefix netip.Prefix, first bool) string {
	name := dockerNetForVNet(vnetID)
	if first {
		return name
	}
	sum := sha256.Sum256([]byte(prefix.Masked().String()))
	return name + "-" + hex.EncodeToString(sum[:3])
}

func azureVNetPrefixOfSubnet(vnet VirtualNetwork, subnet Subnet) (netip.Prefix, bool, error) {
	cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
	if err != nil {
		return netip.Prefix{}, false, err
	}
	subnetPrefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return netip.Prefix{}, false, fmt.Errorf("subnet %s: address prefix %q: %w", subnet.ID, cidr, err)
	}
	prefixes, err := azureVNetIPv4Prefixes(vnet)
	if err != nil {
		return netip.Prefix{}, false, err
	}
	for i, prefix := range prefixes {
		if prefix.Bits() <= subnetPrefix.Bits() && prefix.Contains(subnetPrefix.Masked().Addr()) {
			return prefix, i == 0, nil
		}
	}
	return netip.Prefix{}, false, fmt.Errorf("subnet %s (%s) lies outside the address space of virtual network %s (%s)",
		subnet.ID, cidr, vnet.ID, strings.Join(vnet.Properties.AddressSpace.AddressPrefixes, ", "))
}

// In their order; the engine network spans them.
func azureVNetIPv4Prefixes(vnet VirtualNetwork) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, raw := range vnet.Properties.AddressSpace.AddressPrefixes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("virtual network %s: address prefix %q: %w", vnet.ID, raw, err)
		}
		if prefix.Addr().Is4() {
			prefixes = append(prefixes, prefix)
		}
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("virtual network %s has no IPv4 address prefix", vnet.ID)
	}
	return prefixes, nil
}

// The first IPv4 prefix's reserved addresses go to the resolver's sidecar and
// the node agent.
func azureVNetIPv4Prefix(vnet VirtualNetwork) (netip.Prefix, error) {
	prefixes, err := azureVNetIPv4Prefixes(vnet)
	if err != nil {
		return netip.Prefix{}, err
	}
	return prefixes[0], nil
}

// Names the first subnet prefix outside the virtual network.
func azureSubnetInsideVNet(sn Subnet, vnet VirtualNetwork) (string, bool) {
	vnetPrefixes, err := azureVNetIPv4Prefixes(vnet)
	if err != nil {
		return "", true
	}
	for _, raw := range append([]string{sn.Properties.AddressPrefix}, sn.Properties.AddressPrefixes...) {
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		inside := false
		for _, vnetPrefix := range vnetPrefixes {
			if vnetPrefix.Bits() <= prefix.Bits() && vnetPrefix.Contains(prefix.Masked().Addr()) {
				inside = true
				break
			}
		}
		if !inside {
			return raw, false
		}
	}
	return "", true
}

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

// An owner re-requesting its address keeps it, and another owner's release
// leaves it alone.
type azureSubnetLedger struct {
	cidr   string
	ipam   *ipam.IPAM
	owners map[string]string
}

var azureSubnetLedgers = struct {
	sync.Mutex
	bySubnet map[string]*azureSubnetLedger
}{bySubnet: map[string]*azureSubnetLedger{}}

// Seed a ledger new to this process from every stored address in the subnet.
// The caller holds azureSubnetLedgers.
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

// Keyed by the IP configuration or frontend holding each address.
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

type azureAddressFault struct {
	code    string
	status  int
	message string
}

func (f *azureAddressFault) Error() string { return f.message }

// The requested address or the next free one. Another owner's address fails
// with ipam.ErrAddressInUse; a full subnet with ipam.ErrNoAvailableIP.
func azureLeaseSubnetAddress(subnetID, owner string, requested net.IP) (net.IP, error) {
	azureSubnetLedgers.Lock()
	defer azureSubnetLedgers.Unlock()
	ledger, err := azureSubnetLedgerFor(subnetID)
	if err != nil {
		return nil, err
	}
	return ledger.lease(owner, requested)
}

// An address owner does not hold, or a subnet no longer stored, is left alone.
func azureReleaseSubnetAddress(subnetID, owner string, ip net.IP) {
	azureSubnetLedgers.Lock()
	defer azureSubnetLedgers.Unlock()
	ledger, err := azureSubnetLedgerFor(subnetID)
	if err != nil {
		return
	}
	ledger.release(owner, ip)
}

// The next lease rebuilds the ledger from the store.
func azureForgetSubnetLedger(subnetID string) {
	azureSubnetLedgers.Lock()
	defer azureSubnetLedgers.Unlock()
	delete(azureSubnetLedgers.bySubnet, strings.ToLower(subnetID))
}

// With the Network resource provider's own codes.
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

// The one marked primary, or the first.
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

// A configuration keeps the address it held in prev unless it asks for another
// static one; addresses of dropped configurations return to the subnet first,
// so a new configuration may take one. A failed write leaves prev's leases as
// they were.
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
	// Nothing runs between a release and its re-lease under the lock, so restoring
	// what this write released cannot be refused.
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

func azureReleaseNICAddresses(nic NetworkInterface) {
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil {
			continue
		}
		azureReleaseSubnetAddress(ipcfg.Properties.Subnet.ID, ipcfg.ID, net.ParseIP(ipcfg.Properties.PrivateIPAddress))
	}
}
