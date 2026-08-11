package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

var (
	azureRealHost    = realexec.NewHost()
	azureRealMu      sync.Mutex
	azureRealVnets   = map[string]*realexec.Network{}
	azureRealSubnets = map[string]*realexec.Subnet{}
	azureRealNICs    = map[string]*realexec.NamespaceNIC{}
	azureRealVMNICs  = map[string]*realexec.TapNIC{}
	azureRealVMs     = map[string]*realexec.FirecrackerVM{}
	azureRealNatIPs  = map[string]net.IP{}
)

func azureRequireNetworkHost(w http.ResponseWriter) bool {
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		sim.AzureErrorf(w, "OperationNotAllowed", http.StatusServiceUnavailable,
			"real Azure networking requires Linux network namespace, bridge, veth, route, and nftables host capabilities: %v", err)
		return false
	}
	return true
}

// azureRealName derives the host-side name of a namespace, bridge, veth or tap
// from the ARM resource it realizes. Linux caps an interface name at 15
// characters, far shorter than a resource id, so the name is the prefix plus a
// hash of the WHOLE id: two resources that differ anywhere — a different
// resource group, a different parent, a different child name — get different
// host objects, which a positional truncation of the id could not guarantee.
func azureRealName(prefix, id string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(id)))
	name := prefix + strconv.FormatUint(h.Sum64(), 36)
	if len(name) > 15 {
		return name[:15]
	}
	return name
}

func azureCreateRealVnet(ctx context.Context, vnet VirtualNetwork) error {
	azureRealMu.Lock()
	if _, ok := azureRealVnets[vnet.ID]; ok {
		azureRealMu.Unlock()
		return nil
	}
	azureRealMu.Unlock()
	network, err := azureRealHost.CreateNetworkNamespace(ctx, azureRealName("zn", vnet.ID))
	if err != nil {
		return err
	}
	azureRealMu.Lock()
	azureRealVnets[vnet.ID] = network
	azureRealMu.Unlock()
	return nil
}

func azureDeleteRealVnet(ctx context.Context, vnetID string) error {
	azureRealMu.Lock()
	network := azureRealVnets[vnetID]
	delete(azureRealVnets, vnetID)
	for subnetID, subnet := range azureRealSubnets {
		if strings.HasPrefix(subnetID, vnetID+"/subnets/") {
			_ = subnet.Close(ctx)
			delete(azureRealSubnets, subnetID)
		}
	}
	for nicID, nic := range azureRealVMNICs {
		if armNIC, ok := azureNICs.Get(nicID); ok {
			for _, ipconf := range armNIC.Properties.IPConfigurations {
				if ipconf.Properties.Subnet != nil && strings.HasPrefix(ipconf.Properties.Subnet.ID, vnetID+"/subnets/") {
					azureMetadataVMsByIP.Delete(nic.PrivateIP.String())
					_ = nic.Close(ctx)
					delete(azureRealVMNICs, nicID)
					break
				}
			}
		}
	}
	for vmID, vm := range azureRealVMs {
		if armVM, ok := azureVMs.Get(vmID); ok {
			for _, ref := range armVM.Properties.NetworkProfile.NetworkInterfaces {
				if armNIC, ok := azureNICs.Get(ref.ID); ok {
					for _, ipconf := range armNIC.Properties.IPConfigurations {
						if ipconf.Properties.Subnet != nil && strings.HasPrefix(ipconf.Properties.Subnet.ID, vnetID+"/subnets/") {
							_ = vm.Stop(ctx)
							delete(azureRealVMs, vmID)
							break
						}
					}
				}
			}
		}
	}
	azureRealMu.Unlock()
	if network == nil {
		return nil
	}
	return network.Close(ctx)
}

func azureCreateRealSubnet(ctx context.Context, subnet Subnet) error {
	vnetID := strings.Split(subnet.ID, "/subnets/")[0]
	azureRealMu.Lock()
	if _, ok := azureRealSubnets[subnet.ID]; ok {
		azureRealMu.Unlock()
		return nil
	}
	network := azureRealVnets[vnetID]
	azureRealMu.Unlock()
	if network == nil {
		vnet, ok := azureVnets.Get(vnetID)
		if !ok {
			return fmt.Errorf("virtual network %s not found", vnetID)
		}
		if err := azureCreateRealVnet(ctx, vnet); err != nil {
			return err
		}
		azureRealMu.Lock()
		network = azureRealVnets[vnetID]
		azureRealMu.Unlock()
	}
	cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
	if err != nil {
		return err
	}
	realSubnet, err := network.CreateSubnet(ctx, realexec.SubnetSpec{
		Name:       subnet.ID,
		BridgeName: azureRealName("zs", subnet.ID),
		CIDR:       cidr,
		Gateway:    azureSubnetGateway(cidr),
	})
	if err != nil {
		return err
	}
	azureRealMu.Lock()
	azureRealSubnets[subnet.ID] = realSubnet
	azureRealMu.Unlock()
	if subnet.Properties.NatGateway != nil {
		if err := azureConfigureRealNATGatewayForSubnet(ctx, subnet); err != nil {
			return err
		}
	}
	return nil
}

func azureDeleteRealSubnet(ctx context.Context, subnetID string) error {
	azureRealMu.Lock()
	subnet := azureRealSubnets[subnetID]
	delete(azureRealSubnets, subnetID)
	azureRealMu.Unlock()
	if subnet == nil {
		return nil
	}
	return subnet.Close(ctx)
}

func azureCreateRealNIC(ctx context.Context, nicID, subnetID, requestedIP, mac string) (string, string, error) {
	azureRealMu.Lock()
	if existing, ok := azureRealNICs[nicID]; ok {
		azureRealMu.Unlock()
		return existing.PrivateIP.String(), mac, nil
	}
	subnet := azureRealSubnets[subnetID]
	azureRealMu.Unlock()
	if subnet == nil {
		sn, ok := azureSubnets.Get(subnetID)
		if !ok {
			return "", "", fmt.Errorf("subnet %s not found", subnetID)
		}
		if err := azureCreateRealSubnet(ctx, sn); err != nil {
			return "", "", err
		}
		azureRealMu.Lock()
		subnet = azureRealSubnets[subnetID]
		azureRealMu.Unlock()
	}
	privateIP := net.ParseIP(requestedIP)
	if requestedIP == "" {
		privateIP = nil
	}
	nic, err := subnet.AttachNamespaceNIC(ctx, realexec.NamespaceNICSpec{
		NamespaceName: azureRealName("zi", nicID),
		HostVethName:  azureRealName("zh", nicID),
		GuestVethName: azureRealName("zg", nicID),
		MAC:           mac,
		PrivateIP:     privateIP,
	})
	if err != nil {
		return "", "", err
	}
	azureRealMu.Lock()
	azureRealNICs[nicID] = nic
	azureRealMu.Unlock()
	return nic.PrivateIP.String(), formatAzureMAC(mac), nil
}

func azureDeleteRealNIC(ctx context.Context, nicID string) error {
	vmIDForNIC := ""
	for _, vm := range azureVMs.List() {
		for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
			if strings.EqualFold(ref.ID, nicID) {
				vmIDForNIC = vm.ID
				break
			}
		}
	}
	azureRealMu.Lock()
	nic := azureRealNICs[nicID]
	delete(azureRealNICs, nicID)
	tap := azureRealVMNICs[nicID]
	delete(azureRealVMNICs, nicID)
	var vm *realexec.FirecrackerVM
	if vmIDForNIC != "" {
		vm = azureRealVMs[vmIDForNIC]
		delete(azureRealVMs, vmIDForNIC)
	}
	azureRealMu.Unlock()
	var errs []error
	if vm != nil {
		errs = append(errs, vm.Stop(ctx))
	}
	if nic != nil {
		errs = append(errs, nic.Close(ctx))
	}
	if tap != nil {
		azureMetadataVMsByIP.Delete(tap.PrivateIP.String())
		errs = append(errs, tap.Close(ctx))
	}
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
	azureRealMu.Lock()
	nic := azureRealNICs[armNIC.ID]
	tap := azureRealVMNICs[armNIC.ID]
	azureRealMu.Unlock()
	if nic == nil && tap == nil {
		return nil
	}
	rules, filtered := azureIngressPacketRules(armNIC)
	if !filtered {
		var errs []error
		if nic != nil {
			errs = append(errs, nic.ClearIngressFilter(ctx))
		}
		if tap != nil {
			errs = append(errs, tap.ClearIngressFilter(ctx))
		}
		return errors.Join(errs...)
	}
	if nic != nil {
		if err := nic.ConfigureIngressFilter(ctx, rules); err != nil {
			return fmt.Errorf("configure NSG on %s: %w", armNIC.ID, err)
		}
	}
	if tap != nil {
		if err := tap.ConfigureIngressFilter(ctx, rules); err != nil {
			return fmt.Errorf("configure NSG on %s: %w", armNIC.ID, err)
		}
	}
	return nil
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
	if len(armNIC.Properties.IPConfigurations) == 0 || armNIC.Properties.IPConfigurations[0].Properties.Subnet == nil {
		return &azureVMRequestFault{
			code:   "InvalidParameter",
			status: http.StatusBadRequest,
			message: fmt.Sprintf("The value of parameter properties.networkProfile.networkInterfaces is invalid: network interface %s requires a primary IP configuration with a subnet.",
				nics[0].ID),
		}
	}
	return nil
}

func azureStartRealVM(ctx context.Context, vm VirtualMachine) error {
	if fault := azureValidateVMNetworkProfile(vm); fault != nil {
		return fault
	}
	nicID := vm.Properties.NetworkProfile.NetworkInterfaces[0].ID
	armNIC, _ := azureNICs.Get(nicID)
	ipconf := armNIC.Properties.IPConfigurations[0]
	subnetID := ipconf.Properties.Subnet.ID
	requestedIP := ipconf.Properties.PrivateIPAddress

	azureRealMu.Lock()
	if existing := azureRealVMs[vm.ID]; existing != nil && existing.Alive() {
		azureRealMu.Unlock()
		return nil
	}
	legacyNIC := azureRealNICs[nicID]
	delete(azureRealNICs, nicID)
	tap := azureRealVMNICs[nicID]
	subnet := azureRealSubnets[subnetID]
	azureRealMu.Unlock()
	if legacyNIC != nil {
		_ = legacyNIC.Close(ctx)
	}
	if subnet == nil {
		sn, ok := azureSubnets.Get(subnetID)
		if !ok {
			return fmt.Errorf("subnet %s not found", subnetID)
		}
		if err := azureCreateRealSubnet(ctx, sn); err != nil {
			return err
		}
		azureRealMu.Lock()
		subnet = azureRealSubnets[subnetID]
		azureRealMu.Unlock()
	}
	if tap == nil {
		privateIP := net.ParseIP(requestedIP)
		if requestedIP == "" {
			privateIP = nil
		}
		created, err := subnet.AttachTapNIC(ctx, realexec.TapNICSpec{
			TapName:   azureRealName("zt", nicID),
			PrivateIP: privateIP,
			MAC:       azureNICMAC(nicID),
		})
		if err != nil {
			return err
		}
		tap = created
		azureRealMu.Lock()
		azureRealVMNICs[nicID] = tap
		azureRealMu.Unlock()
		armNIC.Properties.IPConfigurations[0].Properties.PrivateIPAddress = tap.PrivateIP.String()
		armNIC.Properties.MacAddress = formatAzureMAC(azureNICMAC(nicID))
		azureNICs.Put(nicID, armNIC)
	}
	azureMetadataVMsByIP.Store(tap.PrivateIP.String(), azureMetadataVM{
		VM:       vm,
		NIC:      armNIC,
		SubnetID: subnetID,
	})
	metadataPort, err := simHostMetadataPort()
	if err != nil {
		return err
	}
	if err := subnet.ConfigureMetadataDNAT(ctx, metadataPort, azureRealName("zmd", subnetID)); err != nil {
		return fmt.Errorf("configure Azure IMDS routing for %s: %w", vm.ID, err)
	}
	vmProc, err := realexec.StartFirecrackerVM(ctx, realexec.FirecrackerVMConfig{
		ID:        "azure-" + vm.ID,
		Tap:       tap,
		MAC:       azureNICMAC(nicID),
		VCPUCount: 1,
		MemoryMiB: 512,
	})
	if err != nil {
		return err
	}
	azureRealMu.Lock()
	if old := azureRealVMs[vm.ID]; old != nil {
		_ = old.Stop(context.Background())
	}
	azureRealVMs[vm.ID] = vmProc
	azureRealMu.Unlock()
	return azureApplyRealNSGsToNIC(ctx, armNIC)
}

func azureStopRealVM(ctx context.Context, vmID string) error {
	azureRealMu.Lock()
	vm := azureRealVMs[vmID]
	delete(azureRealVMs, vmID)
	azureRealMu.Unlock()
	if vm == nil {
		return nil
	}
	return vm.Stop(ctx)
}

func azureDeleteRealVM(ctx context.Context, vm VirtualMachine) error {
	var errs []error
	errs = append(errs, azureStopRealVM(ctx, vm.ID))
	for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
		azureRealMu.Lock()
		tap := azureRealVMNICs[ref.ID]
		delete(azureRealVMNICs, ref.ID)
		azureRealMu.Unlock()
		if tap != nil {
			azureMetadataVMsByIP.Delete(tap.PrivateIP.String())
			errs = append(errs, tap.Close(ctx))
		}
	}
	return errors.Join(errs...)
}

func azureRealVMAlive(vmID string) bool {
	azureRealMu.Lock()
	vm := azureRealVMs[vmID]
	azureRealMu.Unlock()
	return vm != nil && vm.Alive()
}

func azureIngressPacketRules(nic NetworkInterface) ([]realexec.PacketRule, bool) {
	nsgs := azureAttachedNSGs(nic)
	if len(nsgs) == 0 {
		return nil, false
	}
	var rules []realexec.PacketRule
	for _, nsg := range nsgs {
		securityRules := append([]SecurityRule(nil), nsg.Properties.SecurityRules...)
		sort.SliceStable(securityRules, func(i, j int) bool {
			if securityRules[i].Properties.Priority == securityRules[j].Properties.Priority {
				return securityRules[i].Name < securityRules[j].Name
			}
			return securityRules[i].Properties.Priority < securityRules[j].Properties.Priority
		})
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
			rules = append(rules, azurePacketRulesForSecurityRule(props, verdict)...)
		}
		for _, cidr := range azureNICVNetCIDRs(nic) {
			// The packet filter spells "every protocol" the way the host's rule
			// compiler does; the security-rule spelling "*" is Azure's and has
			// to be translated, exactly as it is for a rule's own protocol.
			rules = append(rules, realexec.PacketRule{
				Protocol:   azurePacketProtocol("*"),
				SourceCIDR: cidr,
				Action:     "accept",
			})
		}
	}
	return rules, true
}

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
	if nic.Properties.NetworkSecurityGroup != nil {
		add(nic.Properties.NetworkSecurityGroup.ID)
	}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil || azureSubnets == nil {
			continue
		}
		if subnet, ok := azureSubnets.Get(ipcfg.Properties.Subnet.ID); ok && subnet.Properties.NetworkSecurityGroup != nil {
			add(subnet.Properties.NetworkSecurityGroup.ID)
		}
	}
	return out
}

func azurePacketRulesForSecurityRule(props SecurityRuleProperties, verdict string) []realexec.PacketRule {
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
	ports := azurePortRanges(props.DestinationPortRange, props.DestinationPortRanges)
	var rules []realexec.PacketRule
	for _, source := range sources {
		for _, port := range ports {
			from, to := azureParsePortRange(port)
			rules = append(rules, realexec.PacketRule{
				Protocol:   azurePacketProtocol(props.Protocol),
				SourceCIDR: source,
				FromPort:   from,
				ToPort:     to,
				Action:     verdict,
			})
		}
	}
	return rules
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
			out = append(out, "168.63.129.16/32")
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
		vnetID := strings.Split(subnet.ID, "/subnets/")[0]
		if azureVnets == nil {
			continue
		}
		vnet, ok := azureVnets.Get(vnetID)
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

func azurePacketProtocol(protocol string) string {
	switch strings.ToLower(protocol) {
	case "", "*":
		return "all"
	default:
		return strings.ToLower(protocol)
	}
}

func azureParsePortRange(port string) (int, int) {
	if port == "" || port == "*" {
		return 0, 0
	}
	if from, to, ok := strings.Cut(port, "-"); ok {
		start, _ := strconv.Atoi(from)
		end, _ := strconv.Atoi(to)
		return start, end
	}
	value, _ := strconv.Atoi(port)
	return value, value
}

func azureConfigureRealNATGatewayForSubnet(ctx context.Context, subnet Subnet) error {
	if subnet.Properties.NatGateway == nil {
		return nil
	}
	gw, ok := azureNatGateways.Get(subnet.Properties.NatGateway.ID)
	if !ok {
		return fmt.Errorf("NAT gateway %s not found", subnet.Properties.NatGateway.ID)
	}
	// Microsoft Azure permits a NAT gateway to be created and associated with
	// a subnet before a public IP address or public IP prefix is associated.
	// That intermediate control-plane state has no outbound data plane yet.
	// A later NAT-gateway update carrying the public addressing calls this
	// function again and programs the real network fabric.
	if len(gw.Properties.PublicIPAddresses) == 0 && len(gw.Properties.PublicIPPrefixes) == 0 {
		return nil
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
	} else if len(gw.Properties.PublicIPPrefixes) > 0 {
		ip, err := realexec.ReserveAzurePublicIPv4(gw.ID, nil)
		if err != nil {
			return err
		}
		publicIP = ip
		azureRealMu.Lock()
		azureRealNatIPs[gw.ID] = ip
		azureRealMu.Unlock()
	} else {
		return fmt.Errorf("NAT gateway %s has no public IP address or prefix", gw.ID)
	}
	vnetID := strings.Split(subnet.ID, "/subnets/")[0]
	azureRealMu.Lock()
	network := azureRealVnets[vnetID]
	azureRealMu.Unlock()
	if network == nil {
		vnet, ok := azureVnets.Get(vnetID)
		if !ok {
			return fmt.Errorf("virtual network %s not found", vnetID)
		}
		if err := azureCreateRealVnet(ctx, vnet); err != nil {
			return err
		}
		azureRealMu.Lock()
		network = azureRealVnets[vnetID]
		azureRealMu.Unlock()
	}
	cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
	if err != nil {
		return err
	}
	return network.ConfigureSNAT(ctx, cidr, publicIP, azureRealName("zsn", subnet.ID))
}

func azureDeleteRealNATGateway(natID string) {
	azureRealMu.Lock()
	ip := azureRealNatIPs[natID]
	delete(azureRealNatIPs, natID)
	azureRealMu.Unlock()
	realexec.ReleasePublicIPv4(ip)
}

func azureSubnetGateway(cidr string) net.IP {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil || ip.To4() == nil {
		return nil
	}
	out := append(net.IP(nil), ip.To4()...)
	out[3]++
	return out
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

func azureNICMAC(nicID string) string {
	id := strings.NewReplacer("/", "", "-", "", "_", "").Replace(nicID)
	var b [3]byte
	for i := range id {
		b[i%3] ^= id[i]
	}
	return fmt.Sprintf("02:15:5d:%02x:%02x:%02x", b[0], b[1], b[2])
}

func formatAzureMAC(mac string) string {
	return strings.ToUpper(strings.ReplaceAll(mac, ":", "-"))
}
