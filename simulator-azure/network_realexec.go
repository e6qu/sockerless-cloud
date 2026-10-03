package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/realexec/fabric"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// azureFabric realizes virtual networks, subnets, the namespaces and taps
// behind network interfaces, and the virtual machines' Firecracker guests.
var azureFabric = fabric.New[string](fabric.Options{
	NetworkPrefix: "zn",
	SubnetPrefix:  "zs",
	Reserved:      azureSubnetReservation,
	OnTapClosed:   func(tap *realexec.TapNIC) { azureMetadataVMsByIP.Delete(tap.PrivateIP.String()) },
})

// azureSubnetReservation is what Azure Virtual Network keeps in every subnet:
// the network address, the default gateway, the two addresses Azure DNS maps
// into the virtual network, and the broadcast address.
var azureSubnetReservation = realexec.HostReservation{First: 4, Last: 1}

const azureMACPrefix = "02:15:5d"

func azureRequireNetworkHost(w http.ResponseWriter) bool {
	if err := azureNetworkHostError(); err != nil {
		AzureError(w, "OperationNotAllowed", err.Error(), http.StatusServiceUnavailable)
		return false
	}
	return true
}

// azureNetworkHostError reports why this host cannot realize the netns fabric,
// or nil when it can — for callers that write the refusal themselves.
func azureNetworkHostError() error {
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		return fmt.Errorf("real Azure networking requires Linux network namespace, bridge, veth, route, and nftables host capabilities: %w", err)
	}
	return nil
}

func azureNICMAC(nicID string) string {
	return fabric.DeriveMAC(azureMACPrefix, nicID)
}

func azureVNetIDOfSubnet(subnetID string) string {
	return strings.Split(subnetID, "/subnets/")[0]
}

// azureCreateRealSubnet realizes the subnet's bridge, and its virtual
// network's namespace when that is not realized yet, then brings its NAT
// gateway association into the fabric.
func azureCreateRealSubnet(ctx context.Context, subnet Subnet) error {
	vnetID := azureVNetIDOfSubnet(subnet.ID)
	if azureFabric.Subnet(subnet.ID) == nil {
		if _, ok := azureVnets.Get(vnetID); !ok {
			return fmt.Errorf("virtual network %s not found", vnetID)
		}
		cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
		if err != nil {
			return err
		}
		if _, err := azureFabric.EnsureSubnet(ctx, vnetID, subnet.ID, cidr, fabric.FirstHostGateway(cidr)); err != nil {
			return err
		}
	}
	return azureConfigureRealNATGatewayForSubnet(ctx, subnet)
}

func azureDeleteRealSubnet(ctx context.Context, subnetID string) error {
	return errors.Join(
		azureFabric.ReleaseOwned(ctx, azureSubnetNATOwner(subnetID)),
		azureFabric.DeleteSubnet(ctx, subnetID),
	)
}

// azureCreateRealNIC realizes a network interface in a namespace of its own
// and returns its private address. An interface a running machine already
// carries as a tap keeps the address it has there.
func azureCreateRealNIC(ctx context.Context, nicID, subnetID, requestedIP, mac string) (string, string, error) {
	if tap := azureFabric.Tap(nicID); tap != nil {
		return tap.PrivateIP.String(), formatAzureMAC(mac), nil
	}
	if nic := azureFabric.NIC(nicID); nic != nil {
		return nic.PrivateIP.String(), formatAzureMAC(mac), nil
	}
	if azureFabric.Subnet(subnetID) == nil {
		sn, ok := azureSubnets.Get(subnetID)
		if !ok {
			return "", "", fmt.Errorf("subnet %s not found", subnetID)
		}
		if err := azureCreateRealSubnet(ctx, sn); err != nil {
			return "", "", err
		}
	}
	var privateIP net.IP
	if requestedIP != "" {
		privateIP = net.ParseIP(requestedIP)
	}
	nic, err := azureFabric.AttachNamespaceNIC(ctx, subnetID, nicID, realexec.NamespaceNICSpec{
		NamespaceName: fabric.LinuxName("zi", nicID),
		HostVethName:  fabric.LinuxName("zh", nicID),
		GuestVethName: fabric.LinuxName("zg", nicID),
		MAC:           mac,
		PrivateIP:     privateIP,
	})
	if err != nil {
		return "", "", err
	}
	return nic.PrivateIP.String(), formatAzureMAC(mac), nil
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

// azureRealizeNICAddresses realizes the interface with its primary IP
// configuration's address and gives each secondary configuration its own.
func azureRealizeNICAddresses(ctx context.Context, nic *NetworkInterface, primary int, prev *NetworkInterface) error {
	ipcfg := &nic.Properties.IPConfigurations[primary]
	privateIP, mac, err := azureCreateRealNIC(ctx, nic.ID, ipcfg.Properties.Subnet.ID, ipcfg.Properties.PrivateIPAddress, azureNICMAC(nic.ID))
	if err != nil {
		return err
	}
	ipcfg.Properties.PrivateIPAddress = privateIP
	nic.Properties.MacAddress = mac
	return azureRealizeNICSecondaries(ctx, nic, prev)
}

// azureRealizeNICSecondaries gives every secondary IP configuration of nic an
// address of its own on the realized interface and writes it into the
// configuration. A configuration keeps the address it held in prev, the
// interface as stored before this write, unless it asks for another static
// one; the addresses of configurations nic no longer has go back to the
// subnet.
func azureRealizeNICSecondaries(ctx context.Context, nic *NetworkInterface, prev *NetworkInterface) error {
	held := map[string]string{}
	if prev != nil {
		primary := azurePrimaryIPConfigIndex(*prev)
		for i, ipcfg := range prev.Properties.IPConfigurations {
			if i != primary && ipcfg.Properties.PrivateIPAddress != "" {
				held[strings.ToLower(ipcfg.Name)] = ipcfg.Properties.PrivateIPAddress
			}
		}
	}
	primary := azurePrimaryIPConfigIndex(*nic)
	wanted := map[string]string{}
	for i, ipcfg := range nic.Properties.IPConfigurations {
		if i == primary {
			continue
		}
		name := strings.ToLower(ipcfg.Name)
		if strings.EqualFold(ipcfg.Properties.PrivateIPAllocationMethod, "Static") {
			wanted[name] = ipcfg.Properties.PrivateIPAddress
		} else {
			wanted[name] = held[name]
		}
	}
	realized := azureFabric.NICAddresses(nic.ID)
	for name, address := range held {
		ip := net.ParseIP(address)
		if wanted[name] == address || ip == nil || !slices.ContainsFunc(realized, ip.Equal) {
			continue
		}
		if err := azureFabric.RemoveNICAddress(ctx, nic.ID, ip); err != nil {
			return fmt.Errorf("release address %s of IP configuration %s: %w", address, name, err)
		}
	}
	for i := range nic.Properties.IPConfigurations {
		if i == primary {
			continue
		}
		ipcfg := &nic.Properties.IPConfigurations[i]
		var requested net.IP
		if address := wanted[strings.ToLower(ipcfg.Name)]; address != "" {
			if requested = net.ParseIP(address); requested == nil {
				return fmt.Errorf("IP configuration %s requests the invalid address %q", ipcfg.Name, address)
			}
		}
		ip, err := azureFabric.AddNICAddress(ctx, nic.ID, requested)
		if err != nil {
			return fmt.Errorf("lease an address for IP configuration %s: %w", ipcfg.Name, err)
		}
		ipcfg.Properties.PrivateIPAddress = ip.String()
	}
	return nil
}

func azureDeleteRealNIC(ctx context.Context, nicID string) error {
	var errs []error
	for _, vm := range azureVMs.List() {
		for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
			if strings.EqualFold(ref.ID, nicID) {
				errs = append(errs, azureFabric.StopVM(ctx, vm.ID, nil))
			}
		}
	}
	errs = append(errs, azureFabric.DeleteNIC(ctx, nicID))
	return errors.Join(errs...)
}

func azureReapplyRealNSGs(ctx context.Context) error {
	if azureNICs == nil {
		return nil
	}
	for _, nic := range azureNICs.List() {
		if err := azureApplyRealNSGsToNIC(ctx, nic); err != nil {
			return err
		}
	}
	return nil
}

func azureApplyRealNSGsToNIC(ctx context.Context, armNIC NetworkInterface) error {
	if !azureFabric.Realized(armNIC.ID) {
		return nil
	}
	stages, err := azureIngressPacketStages(armNIC)
	if err != nil {
		return fmt.Errorf("compile NSG for %s: %w", armNIC.ID, err)
	}
	return azureFabric.ApplyIngress(ctx, armNIC.ID, stages)
}

// azureVMRequestFault is a virtual-machine write the client cannot fix by
// retrying: the request's networkProfile names no usable network interface, so
// no amount of host health makes it succeed. Azure rejects these during request
// validation — 400 InvalidParameter for a networkProfile the Compute resource
// provider cannot accept, 404 ResourceNotFound for a referenced interface that
// does not exist. Reporting them as the 503 OperationNotAllowed that a genuine
// host failure earns would make an SDK retry policy retry forever.
type azureVMRequestFault struct {
	code    string
	status  int
	message string
}

func (f *azureVMRequestFault) Error() string { return f.message }

// azureValidateVMNetworkProfile applies those request-validation rules. It
// reads only ARM state, never the host, so a create with an unusable
// networkProfile is rejected identically on every host.
func azureValidateVMNetworkProfile(vm VirtualMachine) *azureVMRequestFault {
	nics := vm.Properties.NetworkProfile.NetworkInterfaces
	if len(nics) != 1 {
		return &azureVMRequestFault{
			code:   "InvalidParameter",
			status: http.StatusBadRequest,
			message: fmt.Sprintf("The value of parameter properties.networkProfile.networkInterfaces is invalid: exactly one network interface is required, got %d.",
				len(nics)),
		}
	}
	armNIC, ok := azureNICs.Get(nics[0].ID)
	if !ok {
		return &azureVMRequestFault{
			code:    "ResourceNotFound",
			status:  http.StatusNotFound,
			message: fmt.Sprintf("The Resource %q referenced by properties.networkProfile.networkInterfaces was not found.", nics[0].ID),
		}
	}
	if len(armNIC.Properties.IPConfigurations) == 0 || azurePrimaryIPConfig(armNIC).Properties.Subnet == nil {
		return &azureVMRequestFault{
			code:   "InvalidParameter",
			status: http.StatusBadRequest,
			message: fmt.Sprintf("The value of parameter properties.networkProfile.networkInterfaces is invalid: network interface %s requires a primary IP configuration with a subnet.",
				nics[0].ID),
		}
	}
	return nil
}

// azureVMMachineShape sizes the guest from the size catalogue the vmSizes
// reads serve. A size the catalogue does not carry boots at the substrate's
// smallest shape.
func azureVMMachineShape(vm VirtualMachine) (vcpus, memMiB int) {
	size, _ := vm.Properties.HardwareProfile["vmSize"].(string)
	for _, known := range azureVMSizeCatalogue() {
		if name, _ := known["name"].(string); strings.EqualFold(name, size) {
			cores, _ := known["numberOfCores"].(int)
			mem, _ := known["memoryInMB"].(int)
			if cores > 0 && mem > 0 {
				return cores, mem
			}
		}
	}
	return 1, 512
}

func azureStartRealVM(ctx context.Context, vm VirtualMachine) error {
	if fault := azureValidateVMNetworkProfile(vm); fault != nil {
		return fault
	}
	nicID := vm.Properties.NetworkProfile.NetworkInterfaces[0].ID
	armNIC, _ := azureNICs.Get(nicID)
	primary := azurePrimaryIPConfigIndex(armNIC)
	ipconf := armNIC.Properties.IPConfigurations[primary]
	subnetID := ipconf.Properties.Subnet.ID
	var requestedIP net.IP
	if ipconf.Properties.PrivateIPAddress != "" {
		requestedIP = net.ParseIP(ipconf.Properties.PrivateIPAddress)
	}
	if azureFabric.VMAlive(vm.ID) {
		return nil
	}
	// The interface's own namespace hands its address to the machine's tap.
	if err := azureFabric.CloseNamespaceNIC(ctx, nicID); err != nil {
		return err
	}
	metadataPort, err := workloadhost.ListenPort(simListenAddr)
	if err != nil {
		return err
	}
	vcpus, memMiB := azureVMMachineShape(vm)
	_, _, started, err := azureFabric.StartVM(ctx, fabric.VMSpec[string]{
		Key:     vm.ID,
		Network: azureVNetIDOfSubnet(subnetID),
		Subnet:  subnetID,
		NIC:     nicID,
		EnsureSubnet: func(ctx context.Context) error {
			sn, ok := azureSubnets.Get(subnetID)
			if !ok {
				return fmt.Errorf("subnet %s not found", subnetID)
			}
			return azureCreateRealSubnet(ctx, sn)
		},
		Tap: realexec.TapNICSpec{
			TapName:   fabric.LinuxName("zt", nicID),
			PrivateIP: requestedIP,
			MAC:       azureNICMAC(nicID),
		},
		MetadataPort:  metadataPort,
		MetadataTable: fabric.LinuxName("zmd", subnetID),
		Machine: realexec.FirecrackerVMConfig{
			ID:        "azure-" + vm.ID,
			VCPUCount: vcpus,
			MemoryMiB: memMiB,
		},
		BeforeBoot: func(tap *realexec.TapNIC, _ *realexec.FirecrackerVMConfig) error {
			armNIC.Properties.IPConfigurations[primary].Properties.PrivateIPAddress = tap.PrivateIP.String()
			for _, address := range azureNICSecondaryAddresses(armNIC) {
				if _, err := tap.AddAddress(address); err != nil {
					return fmt.Errorf("lease secondary address %s to %s: %w", address, nicID, err)
				}
			}
			armNIC.Properties.MacAddress = formatAzureMAC(azureNICMAC(nicID))
			azureNICs.Put(nicID, armNIC)
			azureMetadataVMsByIP.Store(tap.PrivateIP.String(), azureMetadataVM{
				VM:       vm,
				NIC:      armNIC,
				SubnetID: subnetID,
			})
			return nil
		},
	})
	if err != nil || !started {
		return err
	}
	return azureApplyRealNSGsToNIC(ctx, armNIC)
}

// azureStopRealVM stops the guest. Stopping removes the guest's working
// directory, and the machine's root filesystem lives inside it; Azure's
// managed disk outlives the machine, so the disk is copied out first.
func azureStopRealVM(ctx context.Context, vmID string) error {
	return azureFabric.StopVM(ctx, vmID, func(vm *realexec.FirecrackerVM) error {
		return azurePreserveVMDisk(vmID, vm.WorkDir)
	})
}

// azureDeleteRealVM stops the machine and discards its disk, which only a
// stopped machine keeps. Its network interfaces outlive it, as Azure's do:
// each goes back to a namespace of its own and keeps its private address.
func azureDeleteRealVM(ctx context.Context, vm VirtualMachine) error {
	errs := []error{azureStopRealVM(ctx, vm.ID)}
	azureDiscardVMDisk(vm.ID)
	for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
		tap := azureFabric.Tap(ref.ID)
		if tap == nil {
			continue
		}
		address := tap.PrivateIP.String()
		errs = append(errs, azureFabric.DeleteNIC(ctx, ref.ID))
		armNIC, ok := azureNICs.Get(ref.ID)
		if !ok || len(armNIC.Properties.IPConfigurations) == 0 || azurePrimaryIPConfig(armNIC).Properties.Subnet == nil {
			continue
		}
		if _, _, err := azureCreateRealNIC(ctx, ref.ID, azurePrimaryIPConfig(armNIC).Properties.Subnet.ID, address, azureNICMAC(ref.ID)); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, secondary := range azureNICSecondaryAddresses(armNIC) {
			if _, err := azureFabric.AddNICAddress(ctx, ref.ID, secondary); err != nil {
				errs = append(errs, fmt.Errorf("restore secondary address %s of %s: %w", secondary, ref.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// azureReconcileVMPowerState reports a machine whose record says running but
// whose guest is gone as stopped, which is what Azure reports for it.
func azureReconcileVMPowerState(ids ...string) {
	var claimed []string
	for _, id := range ids {
		if state, _ := azureVMStates.Get(id); state == "PowerState/running" {
			claimed = append(claimed, id)
		}
	}
	fabric.ReconcileLiveness(claimed, azureFabric.VMAlive, func(id string) {
		azureVMStates.Put(id, "PowerState/stopped")
	})
}

// azureIngressPacketStages compiles the network security groups an interface
// sits behind into one filter stage each, the subnet's first and the
// interface's second: Azure delivers an inbound packet only when every group
// on its path allows it. nil means no group applies and the interface is open.
func azureIngressPacketStages(nic NetworkInterface) ([][]realexec.PacketRule, error) {
	nsgs := azureAttachedNSGs(nic)
	if len(nsgs) == 0 {
		return nil, nil
	}
	stages := make([][]realexec.PacketRule, 0, len(nsgs))
	for _, nsg := range nsgs {
		rules, err := azureNSGIngressRules(nsg, nic)
		if err != nil {
			return nil, fmt.Errorf("network security group %s: %w", nsg.ID, err)
		}
		stages = append(stages, rules)
	}
	return stages, nil
}

// azureNSGIngressRules is one group's inbound rules in priority order, then
// the default rules every group carries below them: AllowVnetInBound and
// AllowAzureLoadBalancerInBound, with DenyAllInBound as the stage's final drop.
func azureNSGIngressRules(nsg NetworkSecurityGroup, nic NetworkInterface) ([]realexec.PacketRule, error) {
	securityRules := append([]SecurityRule(nil), nsg.Properties.SecurityRules...)
	sort.SliceStable(securityRules, func(i, j int) bool {
		if securityRules[i].Properties.Priority == securityRules[j].Properties.Priority {
			return securityRules[i].Name < securityRules[j].Name
		}
		return securityRules[i].Properties.Priority < securityRules[j].Properties.Priority
	})
	var rules []realexec.PacketRule
	for _, rule := range securityRules {
		props := rule.Properties
		if !strings.EqualFold(defaultString(props.Direction, "Inbound"), "Inbound") {
			continue
		}
		// A rule scoped to destination application security groups governs
		// only the interfaces in those groups; on every other interface the
		// rule is simply not part of the filter.
		if len(props.DestinationApplicationSecurityGroups) > 0 &&
			!azureNICInApplicationSecurityGroups(nic, props.DestinationApplicationSecurityGroups) {
			continue
		}
		verdict := "drop"
		if strings.EqualFold(props.Access, "Allow") {
			verdict = "accept"
		}
		expanded, err := azurePacketRulesForSecurityRule(props, verdict)
		if err != nil {
			return nil, fmt.Errorf("security rule %s: %w", rule.Name, err)
		}
		rules = append(rules, expanded...)
	}
	for _, cidr := range azureNICVNetCIDRs(nic) {
		rules = append(rules, realexec.PacketRule{Protocol: "*", SourceCIDR: cidr, Action: "accept"})
	}
	rules = append(rules, realexec.PacketRule{Protocol: "*", SourceCIDR: azureLoadBalancerProbeCIDR, Action: "accept"})
	return rules, nil
}

const azureLoadBalancerProbeCIDR = "168.63.129.16/32"

// azureAttachedNSGs lists the groups on an interface's inbound path, the
// subnet's before the interface's own.
func azureAttachedNSGs(nic NetworkInterface) []NetworkSecurityGroup {
	seen := map[string]bool{}
	var out []NetworkSecurityGroup
	add := func(id string) {
		if id == "" || seen[id] || azureNSGs == nil {
			return
		}
		seen[id] = true
		if nsg, ok := azureNSGs.Get(id); ok {
			out = append(out, nsg)
		}
	}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil || azureSubnets == nil {
			continue
		}
		if subnet, ok := azureSubnets.Get(ipcfg.Properties.Subnet.ID); ok && subnet.Properties.NetworkSecurityGroup != nil {
			add(subnet.Properties.NetworkSecurityGroup.ID)
		}
	}
	if nic.Properties.NetworkSecurityGroup != nil {
		add(nic.Properties.NetworkSecurityGroup.ID)
	}
	return out
}

func azurePacketRulesForSecurityRule(props SecurityRuleProperties, verdict string) ([]realexec.PacketRule, error) {
	// A rule written against source application security groups matches the
	// members of those groups and nothing else — an empty group therefore
	// matches no traffic, rather than falling back to the "any address" default
	// an absent address prefix carries.
	var sources []string
	if len(props.SourceApplicationSecurityGroups) > 0 {
		sources = azureApplicationSecurityGroupMemberIPs(props.SourceApplicationSecurityGroups)
	} else {
		sources = azureAddressPrefixes(props.SourceAddressPrefix, props.SourceAddressPrefixes)
	}
	return realexec.ExpandRules(props.Protocol, sources, azurePortRanges(props.DestinationPortRange, props.DestinationPortRanges), verdict)
}

// azureInvalidSecurityRulePort returns the first destination port range of a
// rule Azure would reject, or "".
func azureInvalidSecurityRulePort(props SecurityRuleProperties) string {
	for _, port := range azurePortRanges(props.DestinationPortRange, props.DestinationPortRanges) {
		if _, _, err := realexec.PortRange(port); err != nil {
			return port
		}
	}
	return ""
}

func azureWriteInvalidPortRange(w http.ResponseWriter, port string) {
	AzureErrorf(w, "SecurityRuleInvalidPortRange", http.StatusBadRequest,
		"Security rule has invalid Port range. Value provided: %s. Value should be an integer OR integer range with '-' delimiter. Valid range 0-65535.", port)
}

func azureAddressPrefixes(single string, many []string) []string {
	var values []string
	if single != "" {
		values = append(values, single)
	}
	values = append(values, many...)
	if len(values) == 0 {
		return []string{"0.0.0.0/0"}
	}
	var out []string
	for _, value := range values {
		switch {
		case value == "" || value == "*" || strings.EqualFold(value, "Internet"):
			out = append(out, "0.0.0.0/0")
		case strings.EqualFold(value, "VirtualNetwork"):
			out = append(out, azureAllVNetCIDRs()...)
		case strings.EqualFold(value, "AzureLoadBalancer"):
			out = append(out, azureLoadBalancerProbeCIDR)
		default:
			out = append(out, value)
		}
	}
	return out
}

func azurePortRanges(single string, many []string) []string {
	var values []string
	if single != "" {
		values = append(values, single)
	}
	values = append(values, many...)
	if len(values) == 0 {
		return []string{""}
	}
	return values
}

func azureNICVNetCIDRs(nic NetworkInterface) []string {
	seen := map[string]bool{}
	var out []string
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil || azureSubnets == nil {
			continue
		}
		subnet, ok := azureSubnets.Get(ipcfg.Properties.Subnet.ID)
		if !ok {
			continue
		}
		if azureVnets == nil {
			continue
		}
		vnet, ok := azureVnets.Get(azureVNetIDOfSubnet(subnet.ID))
		if !ok {
			continue
		}
		for _, cidr := range vnet.Properties.AddressSpace.AddressPrefixes {
			if !seen[cidr] {
				seen[cidr] = true
				out = append(out, cidr)
			}
		}
	}
	return out
}

func azureAllVNetCIDRs() []string {
	if azureVnets == nil {
		return []string{"0.0.0.0/0"}
	}
	var out []string
	for _, vnet := range azureVnets.List() {
		out = append(out, vnet.Properties.AddressSpace.AddressPrefixes...)
	}
	if len(out) == 0 {
		return []string{"0.0.0.0/0"}
	}
	return out
}

func azureSubnetNATOwner(subnetID string) string {
	return subnetID + "/natGateway"
}

// azureConfigureRealNATGatewayForSubnet makes the subnet's outbound traffic
// match its NAT gateway association: translated to the gateway's public
// address when it has one, untranslated otherwise.
func azureConfigureRealNATGatewayForSubnet(ctx context.Context, subnet Subnet) error {
	owner := azureSubnetNATOwner(subnet.ID)
	if subnet.Properties.NatGateway == nil {
		return azureFabric.ReleaseOwned(ctx, owner)
	}
	gw, ok := azureNatGateways.Get(subnet.Properties.NatGateway.ID)
	if !ok {
		return fmt.Errorf("NAT gateway %s not found", subnet.Properties.NatGateway.ID)
	}
	// Microsoft Azure permits a NAT gateway to be created and associated with
	// a subnet before a public IP address or public IP prefix is associated.
	// That intermediate control-plane state has no outbound data plane yet.
	if len(gw.Properties.PublicIPAddresses) == 0 && len(gw.Properties.PublicIPPrefixes) == 0 {
		return azureFabric.ReleaseOwned(ctx, owner)
	}
	var publicIP net.IP
	if len(gw.Properties.PublicIPAddresses) > 0 {
		pip, ok := azurePublicIPs.Get(gw.Properties.PublicIPAddresses[0].ID)
		if !ok {
			return fmt.Errorf("public IP address %s not found", gw.Properties.PublicIPAddresses[0].ID)
		}
		publicIP = net.ParseIP(pip.Properties.PublicIPAddress)
		if publicIP == nil {
			return fmt.Errorf("public IP address %s has no IPv4 lease", pip.ID)
		}
		if azureFabric.OwnedPublicIP(gw.ID) != nil {
			if err := azureFabric.ReleaseOwned(ctx, gw.ID); err != nil {
				return err
			}
		}
	} else {
		ip, err := azureFabric.ReservePublicIP(gw.ID, realexec.ReserveAzurePublicIPv4)
		if err != nil {
			return err
		}
		publicIP = ip
	}
	vnetID := azureVNetIDOfSubnet(subnet.ID)
	if _, ok := azureVnets.Get(vnetID); !ok {
		return fmt.Errorf("virtual network %s not found", vnetID)
	}
	if _, err := azureFabric.EnsureNetwork(ctx, vnetID); err != nil {
		return err
	}
	cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
	if err != nil {
		return err
	}
	return azureFabric.ConfigureSNAT(ctx, vnetID, owner, []string{cidr}, publicIP)
}

// azureDeleteRealNATGateway withdraws the translation of every subnet behind
// the gateway and returns the address it reserved.
func azureDeleteRealNATGateway(ctx context.Context, natID string) error {
	var errs []error
	if azureSubnets != nil {
		for _, sn := range azureSubnets.List() {
			if sn.Properties.NatGateway != nil && strings.EqualFold(sn.Properties.NatGateway.ID, natID) {
				errs = append(errs, azureFabric.ReleaseOwned(ctx, azureSubnetNATOwner(sn.ID)))
			}
		}
	}
	errs = append(errs, azureFabric.ReleaseOwned(ctx, natID))
	return errors.Join(errs...)
}

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

func formatAzureMAC(mac string) string {
	return strings.ToUpper(strings.ReplaceAll(mac, ":", "-"))
}
