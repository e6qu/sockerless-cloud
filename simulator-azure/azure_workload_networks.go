package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// Every workload container is a member of an engine network that carries a
// sidecar: the engine network of a virtual network's address prefix, a
// Container Apps environment's own network, or the platform network for a
// workload on no virtual network.
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
	if _, _, err := sim.EnsurePlatformNetwork(ctx, name, nil); err != nil {
		return azureWorkloadNetwork{}, fmt.Errorf("platform network: %w", err)
	}
	dns, err := azureEnsureSidecar(ctx, name, netip.Addr{})
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	return azureWorkloadNetwork{Network: name, DNS: dns}, nil
}

// azureVNetNetworkFor realizes the engine network of the virtual network's
// first address prefix, for an attach point that names the whole network and
// no subnet, and, for a service placed on guests, joins the node agent to it.
func azureVNetNetworkFor(ctx context.Context, service placement.Service, vnetID string) (azureWorkloadNetwork, error) {
	vnet, ok := azureVnets.Get(vnetID)
	if !ok {
		return azureWorkloadNetwork{}, fmt.Errorf("virtual network %s not found", vnetID)
	}
	prefix, err := azureVNetIPv4Prefix(vnet)
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	return azureJoinedVNetPrefixNetwork(ctx, service, vnet, prefix, true)
}

// azureSubnetNetwork realizes the engine network of the address prefix the
// subnet lies in — the network a workload in the subnet attaches to — and,
// for a service placed on guests, joins the node agent to it.
func azureSubnetNetwork(ctx context.Context, service placement.Service, subnetID string) (azureWorkloadNetwork, error) {
	subnet, ok := azureSubnetByID(subnetID)
	if !ok {
		return azureWorkloadNetwork{}, fmt.Errorf("subnet %s not found", subnetID)
	}
	vnet, ok := azureVnets.Get(azureVNetIDOfSubnet(subnet.ID))
	if !ok {
		return azureWorkloadNetwork{}, fmt.Errorf("virtual network %s not found", azureVNetIDOfSubnet(subnet.ID))
	}
	prefix, first, err := azureVNetPrefixOfSubnet(vnet, subnet)
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	return azureJoinedVNetPrefixNetwork(ctx, service, vnet, prefix, first)
}

// azureSubnetNetworkName names the engine network of the prefix the subnet
// lies in, without realizing it.
func azureSubnetNetworkName(subnetID string) (string, error) {
	subnet, ok := azureSubnetByID(subnetID)
	if !ok {
		return "", fmt.Errorf("subnet %s not found", subnetID)
	}
	vnet, ok := azureVnets.Get(azureVNetIDOfSubnet(subnet.ID))
	if !ok {
		return "", fmt.Errorf("virtual network %s not found", azureVNetIDOfSubnet(subnet.ID))
	}
	prefix, first, err := azureVNetPrefixOfSubnet(vnet, subnet)
	if err != nil {
		return "", err
	}
	return dockerNetForVNetPrefix(vnet.ID, prefix, first), nil
}

func azureJoinedVNetPrefixNetwork(ctx context.Context, service placement.Service, vnet VirtualNetwork, prefix netip.Prefix, first bool) (azureWorkloadNetwork, error) {
	network, err := azureVNetPrefixNetwork(ctx, vnet, prefix, first)
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	if err := azureJoinMicroVMNetwork(ctx, service, vnet.ID, prefix, network.Network); err != nil {
		return azureWorkloadNetwork{}, fmt.Errorf("join virtual network %s at %s: %w", vnet.ID, prefix, err)
	}
	return network, nil
}

// azureVNetPrefixNetwork realizes one address prefix of the virtual network
// as an engine network with its sidecar at the address Azure reserves for its
// resolver in that prefix, and registers the network so the catalog answers
// its queries with the zones linked to the virtual network. Each prefix is
// an engine network of its own: the engine's bridge driver spans one range
// per network. The engine isolates its bridges from each other, so a workload
// in one prefix does not reach one in another prefix of the same virtual
// network, where Azure routes between them.
func azureVNetPrefixNetwork(ctx context.Context, vnet VirtualNetwork, prefix netip.Prefix, first bool) (azureWorkloadNetwork, error) {
	name := dockerNetForVNetPrefix(vnet.ID, prefix, first)
	if _, err := sim.EnsureCloudNetwork(ctx, sim.CloudNetwork{Name: name, CIDR: prefix}); err != nil {
		return azureWorkloadNetwork{}, err
	}
	azureResolverNetworks.Store(name, strings.ToLower(vnet.ID))
	dns, err := azureEnsureSidecar(ctx, name, azureVNetResolverAddress(vnet.ID, prefix))
	if err != nil {
		return azureWorkloadNetwork{}, err
	}
	azureVNetNetworksUp.Store(name, strings.ToLower(vnet.ID))
	if err := azureEnsureVNetNATGateways(ctx, vnet.ID); err != nil {
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

// azureVNetNetworkNames lists the engine networks of the virtual network's
// prefixes: one per stored prefix, and any this process realized for a
// prefix the network no longer has.
func azureVNetNetworkNames(vnetID string) []string {
	names := []string{dockerNetForVNet(vnetID)}
	seen := map[string]bool{names[0]: true}
	if vnet, ok := azureVnets.Get(vnetID); ok {
		if prefixes, err := azureVNetIPv4Prefixes(vnet); err == nil {
			for i, prefix := range prefixes {
				if name := dockerNetForVNetPrefix(vnet.ID, prefix, i == 0); !seen[name] {
					names, seen[name] = append(names, name), true
				}
			}
		}
	}
	azureResolverNetworks.Range(func(key, value any) bool {
		if name, _ := key.(string); !seen[name] && value == strings.ToLower(vnetID) {
			names, seen[name] = append(names, name), true
		}
		return true
	})
	return names
}

// azureRemoveVNetNetwork removes the engine network of each of the virtual
// network's prefixes, its sidecar and the node agent off it first; the engine
// refuses while a workload is still attached.
func azureRemoveVNetNetwork(ctx context.Context, vnetID string) error {
	if azureNatGateways != nil {
		for _, gw := range azureNatGateways.List() {
			if subnets, _ := azureNATGatewaySubnets(gw.ID); len(subnets) > 0 && strings.EqualFold(azureVNetIDOfSubnet(subnets[0].ID), vnetID) {
				azureRemoveNATGatewayEgress(gw.ID)
			}
		}
	}
	for _, network := range azureVNetNetworkNames(vnetID) {
		azureVNetNetworksUp.Delete(network)
		azureRemoveSidecar(network)
		if err := azureLeaveMicroVMNetwork(ctx, network); err != nil {
			return err
		}
		if err := sim.RemoveDockerNetwork(network); err != nil {
			return err
		}
		azureResolverNetworks.Delete(network)
	}
	return nil
}

// azureWholeVNetSubnet is the subnet whose ledger records the address a
// connection naming a whole virtual network takes: the network's first
// subnet, so a network interface created later in the virtual network
// cannot lease the same address. A virtual network without a subnet has
// none.
func azureWholeVNetSubnet(vnet VirtualNetwork) string {
	for _, listed := range vnet.Properties.Subnets {
		if subnet, ok := azureSubnetByID(listed.ID); ok {
			return subnet.ID
		}
	}
	prefix := strings.ToLower(vnet.ID) + "/subnets/"
	subnets := azureSubnets.Filter(func(subnet Subnet) bool {
		return strings.HasPrefix(strings.ToLower(subnet.ID), prefix)
	})
	if len(subnets) == 0 {
		return ""
	}
	sort.Slice(subnets, func(i, j int) bool { return subnets[i].ID < subnets[j].ID })
	return subnets[0].ID
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

// azureWorkloadAddress leases the address a workload without a network
// interface resource of its own takes on network: one of the subnet's when
// the workload sits in a virtual network's subnet, else one of the
// simulator's half of the platform network, which the engine never
// allocates, so the workload lands at the same address whichever backend
// places it. The release gives it back.
func azureWorkloadAddress(ctx context.Context, network azureWorkloadNetwork, subnetID, owner string) (string, func(), error) {
	if subnetID != "" {
		address, err := azureLeaseWorkloadAddress(subnetID, owner)
		if err != nil {
			return "", nil, err
		}
		return address, func() { azureReleaseWorkloadAddress(subnetID, owner, address) }, nil
	}
	return azurePlatformAddress(ctx, network, owner)
}

// azureAdoptWorkloadAddress records that owner, a workload an earlier process
// placed, holds address on network, and returns the release.
func azureAdoptWorkloadAddress(ctx context.Context, network azureWorkloadNetwork, subnetID, owner, address string) (func(), error) {
	if subnetID != "" {
		if _, err := azureLeaseSubnetAddress(subnetID, owner, net.ParseIP(address)); err != nil {
			return nil, fmt.Errorf("hold address %s in subnet %s for %s: %w", address, subnetID, owner, err)
		}
		return func() { azureReleaseWorkloadAddress(subnetID, owner, address) }, nil
	}
	at, err := netip.ParseAddr(address)
	if err != nil {
		return nil, fmt.Errorf("address %q: %w", address, err)
	}
	return sim.ReservePlatformAddress(ctx, network.Network, at, owner)
}

// azurePlatformAddress leases the address a workload on no virtual network
// takes: one of the simulator's half of the network it joins.
func azurePlatformAddress(ctx context.Context, network azureWorkloadNetwork, owner string) (string, func(), error) {
	address, release, err := sim.LeasePlatformAddress(ctx, network.Network, owner, netip.Addr{})
	if err != nil {
		return "", nil, err
	}
	return address.String(), release, nil
}

// azureReleaseOnExit returns the workload's lease once it has exited.
func azureReleaseOnExit(exited <-chan struct{}, release func()) {
	go func() {
		<-exited
		release()
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
