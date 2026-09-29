package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/realexec/fabric"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// gcpFabric realizes VPC networks, subnetworks, the taps behind instance
// network interfaces and the instances' Firecracker machines.
var gcpFabric = fabric.New[string](fabric.Options{
	NetworkPrefix: "gn",
	SubnetPrefix:  "gs",
	Reserved:      gcpSubnetReservation,
	OnTapClosed:   func(tap *realexec.TapNIC) { gcpMetadataInstancesByIP.Delete(tap.PrivateIP.String()) },
})

// gcpSubnetReservation is what Google Cloud VPC keeps in every subnet's
// primary range: the network address, the default gateway, the
// second-to-last address and the broadcast address.
var gcpSubnetReservation = realexec.HostReservation{First: 2, Last: 2}

const gcpMACPrefix = "02:42:ac"

func gcpRequireNetworkHost(w http.ResponseWriter) bool {
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION",
			"real Compute networking requires Linux network namespace, bridge, veth, route, and nftables host capabilities: %v", err)
		return false
	}
	return true
}

func gcpCreateRealSubnetwork(ctx context.Context, subnet ComputeSubnetwork) error {
	_, err := gcpFabric.EnsureSubnet(ctx, gcpNetworkKey(subnet.SelfLink, subnet.Network), subnet.SelfLink, subnet.IpCidrRange, net.ParseIP(subnet.GatewayAddress))
	return err
}

// gcpNetworkKey is the fabric key of the network a resource references, however
// the reference is spelled: a subnetwork or router naming its network by full
// URL joins the same namespace as one naming it by relative path.
func gcpNetworkKey(resourceSelfLink, networkRef string) string {
	return normalizeComputeGlobalNetworkRef(gcpProjectFromSelfLink(resourceSelfLink), networkRef)
}

func gcpNICKey(inst *ComputeInstance, ni ComputeNetworkInterface) string {
	return inst.SelfLink + "/" + ni.Name
}

func gcpStartRealVM(ctx context.Context, inst *ComputeInstance) error {
	if inst == nil {
		return fmt.Errorf("compute instance is required")
	}
	if len(inst.NetworkInterfaces) != 1 {
		return fmt.Errorf("firecracker-backed Compute Engine slice requires exactly one network interface, got %d", len(inst.NetworkInterfaces))
	}
	ni := &inst.NetworkInterfaces[0]
	nicID := gcpNICKey(inst, *ni)
	metadataPort, err := workloadhost.ListenPort(simListenAddr)
	if err != nil {
		return err
	}
	var privateIP net.IP
	if ni.NetworkIP != "" {
		privateIP = net.ParseIP(ni.NetworkIP)
	}
	_, tap, _, err := gcpFabric.StartVM(ctx, fabric.VMSpec[string]{
		Key:     inst.SelfLink,
		Network: ni.Network,
		Subnet:  ni.Subnetwork,
		NIC:     nicID,
		EnsureSubnet: func(ctx context.Context) error {
			sn, ok := gcpSubnetworks.Get(ni.Subnetwork)
			if !ok {
				return fmt.Errorf("subnetwork %s not found", ni.Subnetwork)
			}
			return gcpCreateRealSubnetwork(ctx, sn)
		},
		Tap: realexec.TapNICSpec{
			TapName:   fabric.LinuxName("gt", nicID),
			PrivateIP: privateIP,
			MAC:       fabric.DeriveMAC(gcpMACPrefix, nicID),
		},
		MetadataPort:  metadataPort,
		MetadataTable: fabric.LinuxName("gmd", ni.Network),
		Machine: realexec.FirecrackerVMConfig{
			ID:            "gcp-" + inst.SelfLink,
			VCPUCount:     1,
			MemoryMiB:     512,
			MetadataHosts: []string{"metadata.google.internal", "metadata"},
		},
		BeforeBoot: func(tap *realexec.TapNIC, _ *realexec.FirecrackerVMConfig) error {
			ni.NetworkIP = tap.PrivateIP.String()
			gcpMetadataInstancesByIP.Store(tap.PrivateIP.String(), *inst)
			return nil
		},
	})
	if tap != nil {
		ni.NetworkIP = tap.PrivateIP.String()
	}
	return err
}

func gcpDeleteRealVM(ctx context.Context, inst ComputeInstance) error {
	errs := []error{gcpFabric.StopVM(ctx, inst.SelfLink, nil)}
	for _, ni := range inst.NetworkInterfaces {
		errs = append(errs, gcpFabric.DeleteNIC(ctx, gcpNICKey(&inst, ni)))
	}
	return errors.Join(errs...)
}

func gcpRouterNATOwner(routerSelfLink, natName string) string {
	return routerSelfLink + "/" + natName
}

// gcpConfigureRealRouterNAT programs every Cloud NAT gateway of the router.
// A gateway without NAT addresses of its own translates to one address it
// reserves once and keeps until the gateway goes.
func gcpConfigureRealRouterNAT(ctx context.Context, router ComputeRouter) error {
	network := gcpNetworkKey(router.SelfLink, router.Network)
	if _, err := gcpFabric.EnsureNetwork(ctx, network); err != nil {
		return err
	}
	for _, nat := range router.Nats {
		owner := gcpRouterNATOwner(router.SelfLink, nat.Name)
		var publicIP net.IP
		for _, ref := range nat.NatIps {
			if addr, ok := gcpComputeAddressByRef(ref); ok {
				publicIP = net.ParseIP(addr.Address)
				break
			}
		}
		if publicIP != nil && gcpFabric.OwnedPublicIP(owner) != nil {
			if err := gcpFabric.ReleaseOwned(ctx, owner); err != nil {
				return err
			}
		}
		if publicIP == nil {
			ip, err := gcpFabric.ReservePublicIP(owner, realexec.ReserveGCPPublicIPv4)
			if err != nil {
				return err
			}
			publicIP = ip
		}
		if err := gcpFabric.ConfigureSNAT(ctx, network, owner, gcpNATSourceCIDRs(network, nat), publicIP); err != nil {
			return err
		}
	}
	return nil
}

// gcpReleaseRealRouterNAT withdraws the gateways of router that keep does
// not name, returning the addresses they reserved.
func gcpReleaseRealRouterNAT(ctx context.Context, router ComputeRouter, keep []ComputeRouterNAT) error {
	kept := map[string]bool{}
	for _, nat := range keep {
		kept[nat.Name] = true
	}
	var errs []error
	for _, nat := range router.Nats {
		if !kept[nat.Name] {
			errs = append(errs, gcpFabric.ReleaseOwned(ctx, gcpRouterNATOwner(router.SelfLink, nat.Name)))
		}
	}
	return errors.Join(errs...)
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
			if gcpNetworkKey(sn.SelfLink, sn.Network) == networkLink {
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

// gcpSubnetGateway is the gatewayAddress Compute Engine reports for a
// subnetwork's primary range, or "" for a range it cannot hold.
func gcpSubnetGateway(cidr string) string {
	if gw := fabric.FirstHostGateway(cidr); gw != nil {
		return gw.String()
	}
	return ""
}
