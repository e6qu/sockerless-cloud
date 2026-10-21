package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// gcpSubnetReservation is what Google Cloud VPC keeps in every subnet's
// primary range: the network address, the default gateway, the
// second-to-last address and the broadcast address.
var gcpSubnetReservation = ipam.HostReservation{First: 2, Last: 2}

// gcpSubnetGateway is the gatewayAddress Compute Engine reports for a
// subnetwork's primary range, or "" for a range it cannot hold.
func gcpSubnetGateway(cidr string) string {
	if gw := ipam.FirstHostGateway(cidr); gw != nil {
		return gw.String()
	}
	return ""
}

// gcpNetworkKey is the key of the network a resource references, however the
// reference is spelled: a subnetwork or router naming its network by full URL
// joins the same network as one naming it by relative path.
func gcpNetworkKey(resourceSelfLink, networkRef string) string {
	return normalizeComputeGlobalNetworkRef(gcpProjectFromSelfLink(resourceSelfLink), networkRef)
}

func gcpNICKey(inst *ComputeInstance, ni ComputeNetworkInterface) string {
	return inst.SelfLink + "/" + ni.Name
}

// gcpSubnetworkNetworkName is the engine network a subnetwork is realized as.
// The engine bounds a network's name, and a subnetwork's self link does not
// fit, so the name carries a digest of it; the self link itself rides on the
// network's label. The digest takes in the simulator's workload scope, so two
// simulators on one engine that each hold the same subnetwork realize it as
// two networks.
func gcpSubnetworkNetworkName(subnetworkSelfLink string) string {
	sum := sha256.Sum256([]byte(sim.WorkloadScope() + "\x00" + subnetworkSelfLink))
	return "gcp-subnetwork-" + hex.EncodeToString(sum[:8])
}

const gcpSubnetworkNetworkLabel = "sockerless-gcp-subnetwork"

// gcpSubnetworkNetwork is the engine network realizing a subnetwork: one
// over the subnetwork's own primary range, with the subnetwork's gateway, so a
// workload attached to it takes the address its instance interface reports.
func gcpSubnetworkNetwork(subnet ComputeSubnetwork) (sim.CloudNetwork, error) {
	cidr, err := netip.ParsePrefix(subnet.IpCidrRange)
	if err != nil {
		return sim.CloudNetwork{}, fmt.Errorf("subnetwork %s: %w", subnet.SelfLink, err)
	}
	spec := sim.CloudNetwork{
		Name:   gcpSubnetworkNetworkName(subnet.SelfLink),
		CIDR:   cidr,
		Labels: map[string]string{gcpSubnetworkNetworkLabel: subnet.SelfLink},
	}
	if subnet.GatewayAddress != "" {
		gateway, err := netip.ParseAddr(subnet.GatewayAddress)
		if err != nil {
			return sim.CloudNetwork{}, fmt.Errorf("subnetwork %s: gatewayAddress %q: %w", subnet.SelfLink, subnet.GatewayAddress, err)
		}
		spec.Gateway = gateway
	}
	return spec, nil
}

// gcpSubnetIPAMs holds the lease ledger of every subnetwork an address was
// asked of since the process started. The stored instances are the ledger
// across restarts: a subnetwork's ledger is rebuilt from their interfaces the
// first time it is needed.
var gcpSubnetIPAMs = struct {
	sync.Mutex
	pools map[string]*ipam.IPAM
}{pools: map[string]*ipam.IPAM{}}

func gcpSubnetworkIPAMLocked(subnetLink string) (*ipam.IPAM, error) {
	if pool, ok := gcpSubnetIPAMs.pools[subnetLink]; ok {
		return pool, nil
	}
	if gcpSubnetworks == nil {
		return nil, fmt.Errorf("subnetwork %s not found", subnetLink)
	}
	subnet, ok := gcpSubnetworks.Get(subnetLink)
	if !ok {
		return nil, fmt.Errorf("subnetwork %s not found", subnetLink)
	}
	gateway := ipam.FirstHostGateway(subnet.IpCidrRange)
	if gateway == nil {
		return nil, fmt.Errorf("subnetwork %s has no IPv4 range to lease from: %q", subnetLink, subnet.IpCidrRange)
	}
	pool, err := ipam.NewIPAMWithReserved(subnet.IpCidrRange, gateway, gcpSubnetReservation)
	if err != nil {
		return nil, fmt.Errorf("subnetwork %s: %w", subnetLink, err)
	}
	// A service on microVMs needs the node agent on the subnetwork, at the
	// address before the one Google reserves, ahead of the first guest; the
	// subnetwork's Cloud NAT has its sidecar at the one before that on either
	// backend.
	if cidr, err := netip.ParsePrefix(subnet.IpCidrRange); err == nil {
		if gcpMicroVMBackend != nil {
			if _, err := pool.Reserve("node-agent", net.ParseIP(gcpSubnetworkAgentAddress(cidr).String())); err != nil {
				return nil, fmt.Errorf("subnetwork %s: reserve the node agent's address: %w", subnetLink, err)
			}
		}
		if _, err := pool.Reserve("cloud-nat", net.ParseIP(gcpSubnetworkNATAddress(cidr).String())); err != nil {
			return nil, fmt.Errorf("subnetwork %s: reserve the Cloud NAT's address: %w", subnetLink, err)
		}
	}
	if gcpInstances != nil {
		for _, inst := range gcpInstancesBySubnetwork.LookupAll(gcpInstances, subnetLink, func(inst ComputeInstance) []string {
			keys := make([]string, 0, len(inst.NetworkInterfaces))
			for _, ni := range inst.NetworkInterfaces {
				keys = append(keys, ni.Subnetwork)
			}
			return keys
		}) {
			for _, ni := range inst.NetworkInterfaces {
				if ni.Subnetwork != subnetLink || ni.NetworkIP == "" {
					continue
				}
				if _, err := pool.Reserve(gcpNICKey(&inst, ni), net.ParseIP(ni.NetworkIP)); err != nil {
					return nil, fmt.Errorf("stored interface %s holds %s: %w", gcpNICKey(&inst, ni), ni.NetworkIP, err)
				}
			}
		}
	}
	gcpSubnetIPAMs.pools[subnetLink] = pool
	return pool, nil
}

// gcpForgetSubnetworkIPAM drops a subnetwork's ledger, so the next lease
// rebuilds it from the store: after the subnetwork's delete there is nothing
// to rebuild, and after expandIpCidrRange the rebuilt ledger spans the wider
// range.
func gcpForgetSubnetworkIPAM(subnetLink string) {
	gcpSubnetIPAMs.Lock()
	defer gcpSubnetIPAMs.Unlock()
	delete(gcpSubnetIPAMs.pools, subnetLink)
}

// gcpLeaseInstanceAddress leases the interface the address it asked for, or
// the next free one of its subnetwork, and reports a refusal the way Compute
// Engine words it.
func gcpLeaseInstanceAddress(inst *ComputeInstance, index int) error {
	ni := &inst.NetworkInterfaces[index]
	field := fmt.Sprintf("resource.networkInterfaces[%d].networkIP", index)
	var requested net.IP
	if ni.NetworkIP != "" {
		if requested = net.ParseIP(ni.NetworkIP); requested == nil {
			return computeFieldRefusal{reason: "invalid", message: fmt.Sprintf("Invalid value for field '%s': '%s'.", field, ni.NetworkIP)}
		}
	}
	gcpSubnetIPAMs.Lock()
	defer gcpSubnetIPAMs.Unlock()
	pool, err := gcpSubnetworkIPAMLocked(ni.Subnetwork)
	if err != nil {
		return err
	}
	ip, err := pool.Reserve(gcpNICKey(inst, *ni), requested)
	switch {
	case errors.Is(err, ipam.ErrAddressInUse):
		return computeFieldRefusal{reason: "invalid", message: fmt.Sprintf(
			"Invalid value for field '%s': '%s'. IP address '%s' is already in use.", field, ni.NetworkIP, ni.NetworkIP)}
	case errors.Is(err, ipam.ErrNoAvailableIP):
		return computeFieldRefusal{reason: "IP_SPACE_EXHAUSTED", message: fmt.Sprintf("IP space of '%s' is exhausted.", ni.Subnetwork)}
	case err != nil:
		return computeFieldRefusal{reason: "invalid", message: fmt.Sprintf("Invalid value for field '%s': '%s'. %v.", field, ni.NetworkIP, err)}
	}
	ni.NetworkIP = ip.String()
	return nil
}

// gcpReleaseInstanceAddresses returns an instance's leases to their
// subnetworks' ledgers. A ledger not built yet needs no release: it is
// rebuilt from the store, which no longer holds the instance.
func gcpReleaseInstanceAddresses(inst ComputeInstance) {
	gcpSubnetIPAMs.Lock()
	defer gcpSubnetIPAMs.Unlock()
	for _, ni := range inst.NetworkInterfaces {
		if pool, ok := gcpSubnetIPAMs.pools[ni.Subnetwork]; ok {
			pool.Release(net.ParseIP(ni.NetworkIP))
		}
		for _, config := range ni.AccessConfigs {
			if config.NatIP != "" && !gcpStaticAddressHeld(config.NatIP) {
				ipam.ReleasePublicIPv4(net.ParseIP(config.NatIP))
			}
		}
	}
}

// gcpStaticAddressHeld reports whether a reserved address holds ip, which
// outlives the instance that uses it.
func gcpStaticAddressHeld(ip string) bool {
	if gcpAddresses == nil {
		return false
	}
	for _, addr := range gcpAddresses.List() {
		if addr.Address == ip {
			return true
		}
	}
	return false
}

var gcpInstancesBySubnetwork sim.GenerationIndex[ComputeInstance]
