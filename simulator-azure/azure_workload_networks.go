package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Every workload container is a member of an engine network that carries a
// sidecar: a virtual network's engine network, a Container Apps environment's
// own network, or the platform network for a workload on no virtual network.
// The sidecar answers DNS at the address Azure reserves for its resolver and
// forwards metadata to the simulator, so the catalog attributes a query to
// the network it came through and the metadata handlers see the workload's
// own address.

// azureWorkloadNetwork is the engine network a workload joins and the
// resolver it is handed: the sidecar's address on that network.
type azureWorkloadNetwork struct {
	Network string
	DNS     string
}

func (n azureWorkloadNetwork) resolvers() []string { return []string{n.DNS} }

// azureResolverNetworks maps an engine network to the virtual network it
// realizes, which decides the private zones a query through its sidecar sees.
var azureResolverNetworks sync.Map

func azureNetworkVNet(network string) string {
	if network == "" {
		return ""
	}
	v, _ := azureResolverNetworks.Load(network)
	id, _ := v.(string)
	return id
}

func azurePlatformNetworkName() string {
	return "sockerless-sim-azure-platform-" + sim.WorkloadScope()
}

// azureEnsureSidecar runs the sidecar of an engine network that exists and
// returns its address; the zero address takes the second host of the
// network's range.
func azureEnsureSidecar(ctx context.Context, network string, address netip.Addr) (string, error) {
	if networkSidecars == nil {
		return "", fmt.Errorf("network %s: the simulator's resolver is not running, so it has no sidecar to give the network", network)
	}
	running, err := networkSidecars.Ensure(ctx, network, address)
	if err != nil {
		return "", err
	}
	return running.Address.String(), nil
}

func azureRemoveSidecar(network string) {
	if networkSidecars != nil {
		networkSidecars.Remove(network)
	}
}

// azurePlatformNetwork is the network every workload on no virtual network
// joins, created with its sidecar on first use.
func azurePlatformNetwork(ctx context.Context) (azureWorkloadNetwork, error) {
	name := azurePlatformNetworkName()
	if _, err := sim.EnsureDockerNetwork(name); err != nil {
		return azureWorkloadNetwork{}, fmt.Errorf("platform network: %w", err)
	}
	dns, err := azureEnsureSidecar(ctx, name, netip.Addr{})
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	return azureWorkloadNetwork{Network: name, DNS: dns}, nil
}

// azureVNetNetwork realizes the virtual network as an engine network with its
// sidecar at the address Azure reserves for its resolver, and registers the
// network so the catalog answers its queries with the zones linked to the
// virtual network.
func azureVNetNetwork(ctx context.Context, vnetID string) (azureWorkloadNetwork, error) {
	vnet, ok := azureVnets.Get(vnetID)
	if !ok {
		return azureWorkloadNetwork{}, fmt.Errorf("virtual network %s not found", vnetID)
	}
	prefix, err := azureVNetIPv4Prefix(vnet)
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	name := dockerNetForVNet(vnet.ID)
	if _, err := sim.EnsureCloudNetwork(ctx, sim.CloudNetwork{Name: name, CIDR: prefix}); err != nil {
		return azureWorkloadNetwork{}, err
	}
	azureResolverNetworks.Store(name, strings.ToLower(vnet.ID))
	dns, err := azureEnsureSidecar(ctx, name, azureVNetResolverAddress(vnet.ID, prefix))
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	return azureWorkloadNetwork{Network: name, DNS: dns}, nil
}

// azureVNetResolverAddress is the second usable address of the virtual
// network's lowest subnet — one of the two addresses Azure reserves in every
// subnet "to map the Azure DNS IPs to the VNet space" — or the virtual
// network's own second host while it has no subnet. The subnet ledger keeps
// tenants off it as part of its reservation.
func azureVNetResolverAddress(vnetID string, vnetPrefix netip.Prefix) netip.Addr {
	base := vnetPrefix.Masked().Addr()
	chosen := netip.Addr{}
	if azureSubnets != nil {
		subnetPrefix := strings.ToLower(vnetID) + "/subnets/"
		for _, subnet := range azureSubnets.List() {
			if !strings.HasPrefix(strings.ToLower(subnet.ID), subnetPrefix) {
				continue
			}
			cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
			if err != nil {
				continue
			}
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil || !vnetPrefix.Contains(prefix.Masked().Addr()) {
				continue
			}
			if !chosen.IsValid() || prefix.Masked().Addr().Less(chosen) {
				chosen = prefix.Masked().Addr()
			}
		}
	}
	if chosen.IsValid() {
		base = chosen
	}
	return base.Next().Next()
}

// azureVNetMicroVMAgentAddress is the third usable address of the same
// subnet, the other address Azure reserves, which a node agent takes on the
// virtual network.
func azureVNetMicroVMAgentAddress(vnetID string, vnetPrefix netip.Prefix) netip.Addr {
	return azureVNetResolverAddress(vnetID, vnetPrefix).Next()
}

// azureRemoveVNetNetwork removes the virtual network's engine network, its
// sidecar first; the engine refuses while a workload is still attached.
func azureRemoveVNetNetwork(vnetID string) error {
	network := dockerNetForVNet(vnetID)
	azureRemoveSidecar(network)
	if err := sim.RemoveDockerNetwork(network); err != nil {
		return err
	}
	azureResolverNetworks.Delete(network)
	return nil
}

// azureSubnetVNetNetwork is the engine network of the subnet's virtual
// network, with the subnet's own identifier for leases on it.
func azureSubnetVNetNetwork(ctx context.Context, subnetID string) (azureWorkloadNetwork, error) {
	if _, ok := azureSubnetByID(subnetID); !ok {
		return azureWorkloadNetwork{}, fmt.Errorf("subnet %s not found", subnetID)
	}
	return azureVNetNetwork(ctx, azureVNetIDOfSubnet(subnetID))
}

// azureLeaseWorkloadAddress leases an address in the subnet for a workload
// container that has no network interface resource of its own — a Container
// Apps replica, an App Service site, a container group's container — under
// owner, and returns it.
func azureLeaseWorkloadAddress(subnetID, owner string) (string, error) {
	ip, err := azureLeaseSubnetAddress(subnetID, owner, nil)
	if err != nil {
		return "", fmt.Errorf("lease an address in subnet %s for %s: %w", subnetID, owner, err)
	}
	return ip.String(), nil
}

func azureReleaseWorkloadAddress(subnetID, owner, address string) {
	azureReleaseSubnetAddress(subnetID, owner, net.ParseIP(address))
}

// azureReleaseOnExit returns the workload's lease once it has exited.
func azureReleaseOnExit(exited <-chan struct{}, subnetID, owner, address string) {
	go func() {
		<-exited
		azureReleaseWorkloadAddress(subnetID, owner, address)
	}()
}

// azureRemoveWorkloadNetworks removes the engine networks a simulator with no
// persistent state created for its workloads, once their sidecars and
// workloads are gone; a persistent simulator's next run finds them again.
func azureRemoveWorkloadNetworks() {
	if sim.HasPersistentWorkloadIdentity() {
		return
	}
	azureResolverNetworks.Range(func(key, _ any) bool {
		if network, ok := key.(string); ok {
			_ = sim.RemoveDockerNetwork(network)
		}
		return true
	})
	_ = sim.RemoveDockerNetwork(azurePlatformNetworkName())
}
