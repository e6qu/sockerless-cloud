package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"strings"
	"sync"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

var (
	gcpRealHost = realexec.NewHost()
	// gcpRealMu guards the real-execution fabric maps. Resolving an instance's
	// mirror targets only reads them; anything that creates or tears down
	// fabric keeps taking Lock.
	gcpRealMu             sync.RWMutex
	gcpRealNetworks       = map[string]*realexec.Network{}
	gcpRealSubnets        = map[string]*realexec.Subnet{}
	gcpRealSubnetNetworks = map[string]string{}
	gcpRealNICs           = map[string]*realexec.NamespaceNIC{}
	gcpRealVMNICs         = map[string]*realexec.TapNIC{}
	gcpRealVMs            = map[string]*realexec.FirecrackerVM{}
)

func gcpRequireNetworkHost(w http.ResponseWriter) bool {
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		sim.GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION",
			"real Compute networking requires Linux network namespace, bridge, veth, route, and nftables host capabilities: %v", err)
		return false
	}
	return true
}

func gcpRealName(prefix, id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	suffix := fmt.Sprintf("%08x", h.Sum32())
	if len(prefix) >= 15 {
		return prefix[:15]
	}
	return (prefix + suffix)[:min(15, len(prefix)+len(suffix))]
}

func gcpCreateRealNetwork(ctx context.Context, selfLink string) error {
	gcpRealMu.Lock()
	if _, ok := gcpRealNetworks[selfLink]; ok {
		gcpRealMu.Unlock()
		return nil
	}
	namespaceName := gcpRealName("gn", selfLink)
	network, err := gcpRealHost.CreateNetworkNamespace(ctx, namespaceName)
	if err != nil {
		gcpRealMu.Unlock()
		return err
	}
	gcpRealNetworks[selfLink] = network
	gcpRealMu.Unlock()
	return nil
}

func gcpDeleteRealNetwork(ctx context.Context, selfLink string) error {
	gcpRealMu.Lock()
	network := gcpRealNetworks[selfLink]
	delete(gcpRealNetworks, selfLink)
	for nicID, nic := range gcpRealNICs {
		if strings.Contains(nicID, selfLink) {
			_ = nic.Close(ctx)
			delete(gcpRealNICs, nicID)
		}
	}
	for nicID, nic := range gcpRealVMNICs {
		if strings.Contains(nicID, selfLink) {
			gcpMetadataInstancesByIP.Delete(nic.PrivateIP.String())
			_ = nic.Close(ctx)
			delete(gcpRealVMNICs, nicID)
		}
	}
	for instanceID, vm := range gcpRealVMs {
		if strings.Contains(instanceID, selfLink) {
			_ = vm.Stop(ctx)
			delete(gcpRealVMs, instanceID)
		}
	}
	for subnetLink, subnet := range gcpRealSubnets {
		if gcpRealSubnetNetworks[subnetLink] == selfLink {
			_ = subnet.Close(ctx)
			delete(gcpRealSubnets, subnetLink)
			delete(gcpRealSubnetNetworks, subnetLink)
		}
	}
	gcpRealMu.Unlock()
	if network == nil {
		return nil
	}
	return network.Close(ctx)
}

func gcpCreateRealSubnetwork(ctx context.Context, subnet ComputeSubnetwork) error {
	gcpRealMu.Lock()
	if _, ok := gcpRealSubnets[subnet.SelfLink]; ok {
		gcpRealMu.Unlock()
		return nil
	}
	network := gcpRealNetworks[subnet.Network]
	gcpRealMu.Unlock()
	if network == nil {
		if err := gcpCreateRealNetwork(ctx, subnet.Network); err != nil {
			return err
		}
		gcpRealMu.Lock()
		if _, ok := gcpRealSubnets[subnet.SelfLink]; ok {
			gcpRealMu.Unlock()
			return nil
		}
		network = gcpRealNetworks[subnet.Network]
	} else {
		gcpRealMu.Lock()
	}
	realSubnet, err := network.CreateSubnet(ctx, realexec.SubnetSpec{
		Name:       subnet.SelfLink,
		BridgeName: gcpRealName("gs", subnet.SelfLink),
		CIDR:       subnet.IpCidrRange,
		Gateway:    net.ParseIP(subnet.GatewayAddress),
	})
	if err != nil {
		gcpRealMu.Unlock()
		return err
	}
	gcpRealSubnets[subnet.SelfLink] = realSubnet
	gcpRealSubnetNetworks[subnet.SelfLink] = subnet.Network
	gcpRealMu.Unlock()
	return nil
}

func gcpDeleteRealSubnetwork(ctx context.Context, selfLink string) error {
	gcpRealMu.Lock()
	subnet := gcpRealSubnets[selfLink]
	delete(gcpRealSubnets, selfLink)
	delete(gcpRealSubnetNetworks, selfLink)
	gcpRealMu.Unlock()
	if subnet == nil {
		return nil
	}
	return subnet.Close(ctx)
}

func gcpDeleteRealNIC(ctx context.Context, nicID string) error {
	gcpRealMu.Lock()
	nic := gcpRealNICs[nicID]
	delete(gcpRealNICs, nicID)
	tap := gcpRealVMNICs[nicID]
	delete(gcpRealVMNICs, nicID)
	gcpRealMu.Unlock()
	var errs []error
	if nic != nil {
		errs = append(errs, nic.Close(ctx))
	}
	if tap != nil {
		gcpMetadataInstancesByIP.Delete(tap.PrivateIP.String())
		errs = append(errs, tap.Close(ctx))
	}
	return errors.Join(errs...)
}

func gcpStartRealVM(ctx context.Context, inst *ComputeInstance) error {
	if inst == nil {
		return fmt.Errorf("compute instance is required")
	}
	if len(inst.NetworkInterfaces) != 1 {
		return fmt.Errorf("firecracker-backed Compute Engine slice requires exactly one network interface, got %d", len(inst.NetworkInterfaces))
	}
	ni := &inst.NetworkInterfaces[0]
	nicID := inst.SelfLink + "/" + ni.Name
	gcpRealMu.Lock()
	if vm := gcpRealVMs[inst.SelfLink]; vm != nil && vm.Alive() {
		if tap := gcpRealVMNICs[nicID]; tap != nil {
			ni.NetworkIP = tap.PrivateIP.String()
		}
		gcpRealMu.Unlock()
		return nil
	}
	tap := gcpRealVMNICs[nicID]
	subnet := gcpRealSubnets[ni.Subnetwork]
	gcpRealMu.Unlock()
	if subnet == nil {
		sn, ok := gcpSubnetworks.Get(ni.Subnetwork)
		if !ok {
			return fmt.Errorf("subnetwork %s not found", ni.Subnetwork)
		}
		if err := gcpCreateRealSubnetwork(ctx, sn); err != nil {
			return err
		}
		gcpRealMu.Lock()
		subnet = gcpRealSubnets[ni.Subnetwork]
		gcpRealMu.Unlock()
	}
	if tap == nil {
		privateIP := net.ParseIP(ni.NetworkIP)
		if ni.NetworkIP == "" {
			privateIP = nil
		}
		created, err := subnet.AttachTapNIC(ctx, realexec.TapNICSpec{
			TapName:   gcpRealName("gt", nicID),
			PrivateIP: privateIP,
			MAC:       gcpNICMAC(nicID),
		})
		if err != nil {
			return err
		}
		tap = created
		gcpRealMu.Lock()
		gcpRealVMNICs[nicID] = tap
		gcpRealMu.Unlock()
	}
	ni.NetworkIP = tap.PrivateIP.String()
	gcpMetadataInstancesByIP.Store(tap.PrivateIP.String(), *inst)
	metadataPort, err := hostMetadataPort()
	if err != nil {
		return err
	}
	if err := subnet.ConfigureMetadataDNAT(ctx, metadataPort, gcpRealName("gmd", ni.Network)); err != nil {
		return fmt.Errorf("configure Compute Engine metadata routing for %s: %w", inst.SelfLink, err)
	}
	vm, err := realexec.StartFirecrackerVM(ctx, realexec.FirecrackerVMConfig{
		ID:            "gcp-" + inst.SelfLink,
		Tap:           tap,
		MAC:           gcpNICMAC(nicID),
		VCPUCount:     1,
		MemoryMiB:     512,
		MetadataHosts: []string{"metadata.google.internal", "metadata"},
	})
	if err != nil {
		return err
	}
	gcpRealMu.Lock()
	if old := gcpRealVMs[inst.SelfLink]; old != nil {
		_ = old.Stop(context.Background())
	}
	gcpRealVMs[inst.SelfLink] = vm
	gcpRealMu.Unlock()
	return nil
}

func gcpStopRealVM(ctx context.Context, selfLink string) error {
	gcpRealMu.Lock()
	vm := gcpRealVMs[selfLink]
	delete(gcpRealVMs, selfLink)
	gcpRealMu.Unlock()
	if vm == nil {
		return nil
	}
	return vm.Stop(ctx)
}

func gcpDeleteRealVM(ctx context.Context, inst ComputeInstance) error {
	var errs []error
	errs = append(errs, gcpStopRealVM(ctx, inst.SelfLink))
	for _, ni := range inst.NetworkInterfaces {
		errs = append(errs, gcpDeleteRealNIC(ctx, inst.SelfLink+"/"+ni.Name))
	}
	return errors.Join(errs...)
}

func gcpRealVMAlive(selfLink string) bool {
	gcpRealMu.Lock()
	vm := gcpRealVMs[selfLink]
	gcpRealMu.Unlock()
	return vm != nil && vm.Alive()
}

func gcpConfigureRealRouterNAT(ctx context.Context, router ComputeRouter) error {
	gcpRealMu.Lock()
	network := gcpRealNetworks[router.Network]
	gcpRealMu.Unlock()
	if network == nil {
		if err := gcpCreateRealNetwork(ctx, router.Network); err != nil {
			return err
		}
		gcpRealMu.Lock()
		network = gcpRealNetworks[router.Network]
		gcpRealMu.Unlock()
	}
	for _, nat := range router.Nats {
		publicIP := net.IP(nil)
		for _, ref := range nat.NatIps {
			if addr, ok := gcpComputeAddressByRef(ref); ok {
				publicIP = net.ParseIP(addr.Address)
				break
			}
		}
		if publicIP == nil {
			ip, err := realexec.ReserveGCPPublicIPv4(router.SelfLink+"/"+nat.Name, nil)
			if err != nil {
				return err
			}
			publicIP = ip
		}
		sources := gcpNATSourceCIDRs(router.Network, nat)
		for _, cidr := range sources {
			if err := network.ConfigureSNAT(ctx, cidr, publicIP, gcpRealName("gsn", router.SelfLink+nat.Name+cidr)); err != nil {
				return err
			}
		}
	}
	return nil
}

func gcpComputeAddressByRef(ref string) (ComputeAddress, bool) {
	for _, addr := range gcpAddresses.List() {
		if ref == addr.SelfLink || strings.HasSuffix(ref, "/"+addr.SelfLink) || ref == addr.Name {
			return addr, true
		}
	}
	return ComputeAddress{}, false
}

func gcpNATSourceCIDRs(networkLink string, nat ComputeRouterNAT) []string {
	if strings.EqualFold(nat.SourceSubnetworkIpRangesToNat, "ALL_SUBNETWORKS_ALL_IP_RANGES") {
		var cidrs []string
		for _, sn := range gcpSubnetworks.List() {
			if sn.Network == networkLink {
				cidrs = append(cidrs, sn.IpCidrRange)
			}
		}
		return cidrs
	}
	var cidrs []string
	for _, snRef := range nat.Subnetworks {
		link := snRef.Name
		if sn, ok := gcpSubnetworks.Get(link); ok {
			cidrs = append(cidrs, sn.IpCidrRange)
		}
	}
	return cidrs
}

func gcpSubnetGateway(cidr string) string {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil || ip.To4() == nil {
		return ""
	}
	out := append(net.IP(nil), ip.To4()...)
	out[3]++
	return out.String()
}

func gcpNICMAC(nicID string) string {
	id := strings.NewReplacer("/", "", "-", "", "_", "").Replace(nicID)
	var b [3]byte
	for i := range id {
		b[i%3] ^= id[i]
	}
	return fmt.Sprintf("02:42:ac:%02x:%02x:%02x", b[0], b[1], b[2])
}
