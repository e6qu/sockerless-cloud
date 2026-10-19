package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// gcpWorkloadNetwork is where a workload container sits: an engine network
// and the sidecar that answers the network's resolver and metadata addresses.
// Address is the fixed address the workload takes on the network; empty lets
// the engine pick one.
type gcpWorkloadNetwork struct {
	Network string
	Sidecar netip.Addr
	Address string
	// release returns a leased address to its subnetwork's ledger; nil when
	// the engine chose the address.
	release func()
}

// env names the sidecar as the metadata server, which every Google client
// library honours ahead of metadata.google.internal.
func (n gcpWorkloadNetwork) env() map[string]string {
	host := n.Sidecar.String()
	return map[string]string{
		"GCE_METADATA_HOST": host,
		"GCE_METADATA_IP":   host,
		"GCE_METADATA_ROOT": host,
	}
}

func (n gcpWorkloadNetwork) dns() []string {
	return []string{n.Sidecar.String()}
}

// extraHosts are the hosts entries the guest agent writes on a Compute Engine
// machine for the metadata server, plus what a workload needs to reach the
// engine's host.
func (n gcpWorkloadNetwork) extraHosts() []string {
	host := n.Sidecar.String()
	return append(workloadhost.ExtraHosts(), "metadata.google.internal:"+host, "metadata:"+host)
}

// apply attaches a container to the network with the sidecar as its resolver
// and metadata server.
func (n gcpWorkloadNetwork) apply(cfg *sim.ContainerConfig) {
	cfg.Network = n.Network
	cfg.IPv4Address = n.Address
	cfg.DNS = n.dns()
	cfg.Env = workloadhost.MergeEnv(cfg.Env, n.env())
	cfg.ExtraHosts = append(cfg.ExtraHosts, n.extraHosts()...)
}

// done returns the workload's address to its ledger once the container is gone.
func (n gcpWorkloadNetwork) done() {
	if n.release != nil {
		n.release()
	}
}

// gcpNetworkSidecars maps every engine network with a running sidecar to the
// sidecar's address; the Cloud DNS catalog answers metadata.google.internal
// from it.
var gcpNetworkSidecars sync.Map

// gcpNetworkSidecar is the address of the sidecar serving an engine network.
func gcpNetworkSidecar(network string) (netip.Addr, bool) {
	v, ok := gcpNetworkSidecars.Load(network)
	if !ok {
		return netip.Addr{}, false
	}
	addr, ok := v.(netip.Addr)
	return addr, ok
}

func gcpEnsureNetworkSidecar(ctx context.Context, network string, address netip.Addr) (netip.Addr, error) {
	if networkSidecars == nil {
		return netip.Addr{}, errors.New("the simulator runs no network sidecars: it serves the control planes only")
	}
	running, err := networkSidecars.Ensure(ctx, network, address)
	if err != nil {
		return netip.Addr{}, err
	}
	gcpNetworkSidecars.Store(network, running.Address)
	return running.Address, nil
}

func gcpRemoveNetworkSidecar(network string) {
	if networkSidecars != nil {
		networkSidecars.Remove(network)
	}
	gcpNetworkSidecars.Delete(network)
}

// gcpPlatformNetworkName is the engine network that carries every workload on
// no VPC network: a Cloud Run or Cloud Functions instance without VPC egress,
// a Cloud Build step. Its sidecar resolves public zones only, as Google
// resolves for such a workload.
func gcpPlatformNetworkName() string {
	digest := sha256.Sum256([]byte(sim.WorkloadScope()))
	return "sockerless-sim-gcp-platform-" + hex.EncodeToString(digest[:6])
}

var gcpPlatformNetworkMu sync.Mutex

func gcpPlatformNetwork(ctx context.Context) (gcpWorkloadNetwork, error) {
	gcpPlatformNetworkMu.Lock()
	defer gcpPlatformNetworkMu.Unlock()
	name := gcpPlatformNetworkName()
	if _, _, err := sim.EnsurePlatformNetwork(ctx, name, nil); err != nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("platform network: %w", err)
	}
	sidecar, err := gcpEnsureNetworkSidecar(ctx, name, netip.Addr{})
	if err != nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("platform network: %w", err)
	}
	return gcpWorkloadNetwork{Network: name, Sidecar: sidecar}, nil
}

// gcpNetworkRange reads an engine network's IPv4 range.
func gcpNetworkRange(ctx context.Context, network string) (netip.Prefix, error) {
	cli := sim.DockerClient()
	if cli == nil {
		return netip.Prefix{}, errors.New("no container engine")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("inspect network %s: %w", network, err)
	}
	for _, cfg := range info.Network.IPAM.Config {
		if cfg.Subnet.IsValid() && cfg.Subnet.Addr().Is4() {
			return cfg.Subnet, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("network %s has no IPv4 range", network)
}

// gcpRangeAddress is the address n hosts before the end of an IPv4 range:
// 1 is the broadcast address, 2 the second-to-last address Google reserves
// for its resolver and metadata server, 3 the one before it.
func gcpRangeAddress(cidr netip.Prefix, fromEnd int) netip.Addr {
	base := cidr.Masked().Addr().As4()
	bits := 32 - cidr.Bits()
	n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	n += 1<<bits - uint32(fromEnd)
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

// gcpSubnetworkSidecarAddress is the second-to-last address of a subnetwork's
// primary range, which Google Cloud VPC reserves for the metadata server and
// resolver of the subnet.
func gcpSubnetworkSidecarAddress(cidr netip.Prefix) netip.Addr { return gcpRangeAddress(cidr, 2) }

// gcpSubnetworkAgentAddress is the address before the sidecar's, which the
// subnetwork's ledger keeps for the node agent when a service runs on
// microVMs.
func gcpSubnetworkAgentAddress(cidr netip.Prefix) netip.Addr { return gcpRangeAddress(cidr, 3) }

// gcpEnsureSubnetworkNetwork realizes a subnetwork as an engine network with
// its sidecar, registers the network with Cloud DNS so a query through the
// sidecar sees the private zones visible to the subnetwork's VPC network, and
// joins the node agent to it when a service runs on microVMs.
func gcpEnsureSubnetworkNetwork(ctx context.Context, subnet ComputeSubnetwork) (gcpWorkloadNetwork, error) {
	spec, err := gcpSubnetworkNetwork(subnet)
	if err != nil {
		return gcpWorkloadNetwork{}, err
	}
	if _, err := sim.EnsureCloudNetwork(ctx, spec); err != nil {
		return gcpWorkloadNetwork{}, err
	}
	sidecar, err := gcpEnsureNetworkSidecar(ctx, spec.Name, gcpSubnetworkSidecarAddress(spec.CIDR))
	if err != nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("subnetwork %s: %w", subnet.SelfLink, err)
	}
	cloudDNSResolverNetworks.Store(spec.Name, gcpNetworkKey(subnet.SelfLink, subnet.Network))
	if gcpMicroVMBackend != nil {
		if err := gcpMicroVMBackend.Join(ctx, spec.Name, gcpSubnetworkAgentAddress(spec.CIDR)); err != nil {
			return gcpWorkloadNetwork{}, fmt.Errorf("subnetwork %s: %w", subnet.SelfLink, err)
		}
	}
	if err := gcpEnsureSubnetworkNAT(ctx, subnet, spec.Name); err != nil {
		return gcpWorkloadNetwork{}, err
	}
	gcpSubnetworkNetworksUp.Store(subnet.SelfLink, spec.Name)
	return gcpWorkloadNetwork{Network: spec.Name, Sidecar: sidecar}, nil
}

// gcpRemoveSubnetworkNetwork removes the engine network of a subnetwork, its
// sidecar first, and is idempotent for a subnetwork that never had one.
func gcpRemoveSubnetworkNetwork(ctx context.Context, subnet ComputeSubnetwork) error {
	name := gcpSubnetworkNetworkName(subnet.SelfLink)
	gcpRemoveSubnetworkNAT(subnet.SelfLink)
	gcpSubnetworkNetworksUp.Delete(subnet.SelfLink)
	gcpRemoveNetworkSidecar(name)
	cloudDNSResolverNetworks.Delete(name)
	if gcpMicroVMBackend != nil {
		if err := gcpMicroVMBackend.Leave(ctx, name); err != nil {
			return err
		}
	}
	return sim.RemoveDockerNetworkContext(ctx, name)
}

// gcpLeaseWorkloadAddress leases a workload that is not a Compute Engine
// instance — a Cloud Run instance with direct VPC egress — an address of the
// subnetwork, and returns it with the release that gives it back.
func gcpLeaseWorkloadAddress(subnetLink, owner string) (string, func(), error) {
	gcpSubnetIPAMs.Lock()
	defer gcpSubnetIPAMs.Unlock()
	pool, err := gcpSubnetworkIPAMLocked(subnetLink)
	if err != nil {
		return "", nil, err
	}
	ip, err := pool.Reserve(owner, nil)
	if err != nil {
		return "", nil, fmt.Errorf("subnetwork %s: %w", subnetLink, err)
	}
	release := func() {
		gcpSubnetIPAMs.Lock()
		defer gcpSubnetIPAMs.Unlock()
		if pool, ok := gcpSubnetIPAMs.pools[subnetLink]; ok {
			pool.Release(ip)
		}
	}
	return ip.String(), release, nil
}

// gcpAttachToSubnetwork places a workload on a subnetwork's engine network at
// an address leased from the subnetwork.
func gcpAttachToSubnetwork(ctx context.Context, subnetLink, owner string) (gcpWorkloadNetwork, error) {
	if gcpSubnetworks == nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("subnetwork %s not found", subnetLink)
	}
	subnet, ok := gcpSubnetworks.Get(subnetLink)
	if !ok {
		return gcpWorkloadNetwork{}, fmt.Errorf("subnetwork %s not found", subnetLink)
	}
	netw, err := gcpEnsureSubnetworkNetwork(ctx, subnet)
	if err != nil {
		return gcpWorkloadNetwork{}, err
	}
	address, release, err := gcpLeaseWorkloadAddress(subnetLink, owner)
	if err != nil {
		return gcpWorkloadNetwork{}, err
	}
	netw.Address, netw.release = address, release
	return netw, nil
}

// gcpConnectorNetworkName is the engine network a Serverless VPC Access
// connector's own /28 range is realized as.
func gcpConnectorNetworkName(connectorName string) string {
	sum := sha256.Sum256([]byte(connectorName))
	return "gcp-connector-" + hex.EncodeToString(sum[:8])
}

const gcpConnectorNetworkLabel = "sockerless-gcp-connector"

// gcpCloudRunNetwork is the network a Cloud Run workload runs on: the
// subnetwork its direct VPC egress names, the subnetwork or range of the
// Serverless VPC Access connector it names, or the platform network when it
// has no VPC egress. project and region are the workload's own, which
// resolve the short names a VpcAccess may carry.
func gcpCloudRunNetwork(ctx context.Context, project, region string, vpc *VpcAccess, owner string) (gcpWorkloadNetwork, error) {
	switch {
	case vpc != nil && len(vpc.NetworkInterfaces) > 0:
		ni := vpc.NetworkInterfaces[0]
		network := normalizeComputeGlobalNetworkRef(project, gcpDefaultString(ni.Network, "default"))
		subnetRef := ni.Subnetwork
		if subnetRef == "" {
			// Cloud Run: "If subnetwork is not specified, the subnetwork with
			// the same name as the network will be used."
			subnetRef = network[strings.LastIndex(network, "/")+1:]
		}
		return gcpAttachToSubnetwork(ctx, normalizeComputeSubnetworkRef(project, region, subnetRef), owner)
	case vpc != nil && vpc.Connector != "":
		return gcpConnectorNetwork(ctx, project, region, vpc.Connector, owner)
	}
	return gcpPlatformWorkloadNetwork(ctx, owner)
}

// gcpPlatformWorkloadNetwork places a workload on the platform network at an
// address leased from the simulator's half of it, which the engine never
// allocates, so the workload lands at the same address whichever backend
// places it.
func gcpPlatformWorkloadNetwork(ctx context.Context, owner string) (gcpWorkloadNetwork, error) {
	netw, err := gcpPlatformNetwork(ctx)
	if err != nil {
		return gcpWorkloadNetwork{}, err
	}
	address, release, err := sim.LeasePlatformAddress(ctx, netw.Network, owner, netip.Addr{})
	if err != nil {
		return gcpWorkloadNetwork{}, err
	}
	netw.Address = address.String()
	netw.release = release
	return netw, nil
}

// gcpReservePlatformAddress records the address of a workload an earlier
// process placed on the platform network, so no lease of this process takes
// it, and returns the release.
func gcpReservePlatformAddress(ctx context.Context, owner placement.Handle, name string) (func(), error) {
	address, err := owner.Address(ctx)
	if err != nil {
		return nil, err
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return nil, fmt.Errorf("address %q of %s: %w", address, name, err)
	}
	return sim.ReservePlatformAddress(ctx, gcpPlatformNetworkName(), addr, name)
}

// gcpConnectorNetwork places a workload where a Serverless VPC Access
// connector's instances sit: in the subnetwork the connector names, or in an
// engine network over the connector's own range inside its VPC network.
func gcpConnectorNetwork(ctx context.Context, project, region, ref, owner string) (gcpWorkloadNetwork, error) {
	name := ref
	if !strings.Contains(ref, "/") {
		name = fmt.Sprintf("projects/%s/locations/%s/connectors/%s", project, region, ref)
	}
	if gcpVPCConnectors == nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("VPC access connector %s not found", name)
	}
	conn, ok := gcpVPCConnectors.Get(name)
	if !ok {
		return gcpWorkloadNetwork{}, fmt.Errorf("VPC access connector %s not found", name)
	}
	if conn.Subnet != nil && conn.Subnet.Name != "" {
		subnetProject := gcpDefaultString(conn.Subnet.ProjectId, resourceProject(name))
		return gcpAttachToSubnetwork(ctx, fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", subnetProject, resourceLocation(name), conn.Subnet.Name), owner)
	}
	cidr, err := netip.ParsePrefix(conn.IpCidrRange)
	if err != nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("VPC access connector %s: ipCidrRange %q: %w", name, conn.IpCidrRange, err)
	}
	spec := sim.CloudNetwork{
		Name:   gcpConnectorNetworkName(name),
		CIDR:   cidr,
		Labels: map[string]string{gcpConnectorNetworkLabel: name},
	}
	if _, err := sim.EnsureCloudNetwork(ctx, spec); err != nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("VPC access connector %s: %w", name, err)
	}
	sidecar, err := gcpEnsureNetworkSidecar(ctx, spec.Name, netip.Addr{})
	if err != nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("VPC access connector %s: %w", name, err)
	}
	cloudDNSResolverNetworks.Store(spec.Name, normalizeComputeGlobalNetworkRef(resourceProject(name), conn.Network))
	return gcpWorkloadNetwork{Network: spec.Name, Sidecar: sidecar}, nil
}

// gcpRemoveConnectorNetwork removes the engine network a connector's range was
// realized as, if one exists.
func gcpRemoveConnectorNetwork(ctx context.Context, connectorName string) error {
	name := gcpConnectorNetworkName(connectorName)
	gcpRemoveNetworkSidecar(name)
	cloudDNSResolverNetworks.Delete(name)
	return sim.RemoveDockerNetworkContext(ctx, name)
}
