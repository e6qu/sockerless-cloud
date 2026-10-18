package sim

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// CloudNetwork is a cloud network resource — an Amazon VPC, a Compute Engine
// subnetwork, an Azure virtual network — realized as one engine network over
// the resource's own address range, so every member's engine-assigned address
// is the address the cloud API reports for its interface.
type CloudNetwork struct {
	// Name is the engine network's name, derived from the resource's ID.
	Name string
	// CIDR is the resource's IPv4 range.
	CIDR netip.Prefix
	// Gateway is the address the cloud's router answers on; the zero value
	// takes the first host of CIDR, where Amazon VPC, Google Cloud VPC and
	// Azure Virtual Network all put it.
	Gateway netip.Addr
	// Labels are added to the engine network beside the simulator's own.
	Labels map[string]string
	// DynamicRange, when set, is the part of CIDR the engine allocates
	// addresses from; the simulator leases the rest itself, for workloads
	// the engine does not place.
	DynamicRange netip.Prefix
}

// ErrAddressRangeInUse is wrapped by EnsureCloudNetwork when another live
// engine network already holds a range overlapping the requested one. The
// engine's IPAM admits one network per range, so two live cloud networks
// sharing a CIDR do not coexist on one engine.
var ErrAddressRangeInUse = errors.New("address range held by another engine network")

var cloudNetworkMu sync.Mutex

// EnsureCloudNetwork creates the engine network realizing a cloud network
// resource, or returns the one its name already holds. A range an engine
// network of a dead simulator run still holds is reclaimed first; a range a
// live or foreign network holds fails with ErrAddressRangeInUse. Returns the
// engine network's ID.
func EnsureCloudNetwork(ctx context.Context, spec CloudNetwork) (string, error) {
	cloudNetworkMu.Lock()
	defer cloudNetworkMu.Unlock()
	cli := DockerClient()
	if err := RequireContainerRuntime("creating a cloud network"); err != nil {
		return "", err
	}
	if !spec.CIDR.IsValid() || !spec.CIDR.Addr().Is4() {
		return "", fmt.Errorf("cloud network %s: %q is not an IPv4 range", spec.Name, spec.CIDR)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if existing, err := cli.NetworkInspect(ctx, spec.Name, client.NetworkInspectOptions{}); err == nil {
		return existing.Network.ID, nil
	}
	gateway := spec.Gateway
	if !gateway.IsValid() {
		gateway = spec.CIDR.Masked().Addr().Next()
	}
	create := func() (string, error) {
		config := network.IPAMConfig{Subnet: spec.CIDR.Masked(), Gateway: gateway}
		if spec.DynamicRange.IsValid() {
			config.IPRange = spec.DynamicRange.Masked()
		}
		resp, err := cli.NetworkCreate(ctx, spec.Name, client.NetworkCreateOptions{
			Driver: "bridge",
			IPAM: &network.IPAM{
				Config: []network.IPAMConfig{config},
			},
			Labels: simulatorLabels(spec.Labels),
		})
		if err != nil {
			return "", err
		}
		return resp.ID, nil
	}
	id, err := create()
	if err == nil {
		return id, nil
	}
	if existing, inspectErr := cli.NetworkInspect(ctx, spec.Name, client.NetworkInspectOptions{}); inspectErr == nil {
		return existing.Network.ID, nil
	}
	if !addressRangeInUseError(err) {
		return "", fmt.Errorf("cloud network create %s (%s): %w", spec.Name, spec.CIDR, err)
	}
	if reclaimOrphanedRange(ctx, cli, spec.CIDR) {
		if id, err = create(); err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("cloud network create %s (%s): %w: %w", spec.Name, spec.CIDR, ErrAddressRangeInUse, err)
}

// platformRange is where platform networks take their ranges from: the
// shared address space of RFC 6598, which no cloud hands a customer resource,
// so a platform network never collides with a cloud network's CIDR.
var platformRange = netip.MustParsePrefix("100.64.0.0/10")

// EnsurePlatformNetwork creates the engine network the workloads on no cloud
// network join, or returns the one its name already holds, and reports its
// IPv4 range. The network takes a /24 of the shared address space chosen by
// its name, moving to the next /24 while a live network holds the chosen
// one, so two simulators on one engine each get their own. The engine
// allocates container addresses from the upper half of the /24; the lower
// half is the simulator's to lease through LeasePlatformAddress, for the
// sidecars, the node agent and the guests the engine does not place. A
// platform network an earlier release created without that split is
// recreated while nothing is attached to it.
func EnsurePlatformNetwork(ctx context.Context, name string, labels map[string]string) (string, netip.Prefix, error) {
	if err := RequireContainerRuntime("creating the platform network"); err != nil {
		return "", netip.Prefix{}, err
	}
	cli := DockerClient()
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if existing, err := cli.NetworkInspect(inspectCtx, name, client.NetworkInspectOptions{}); err == nil {
		for _, cfg := range existing.Network.IPAM.Config {
			if !cfg.Subnet.IsValid() || !cfg.Subnet.Addr().Is4() {
				continue
			}
			if cfg.IPRange.IsValid() {
				return existing.Network.ID, cfg.Subnet, nil
			}
			if len(existing.Network.Containers) > 0 {
				return "", netip.Prefix{}, fmt.Errorf("platform network %s predates leased addresses and has containers attached; remove it once they are gone", name)
			}
			if _, err := cli.NetworkRemove(inspectCtx, existing.Network.ID, client.NetworkRemoveOptions{}); err != nil {
				return "", netip.Prefix{}, fmt.Errorf("platform network %s predates leased addresses: remove it: %w", name, err)
			}
			break
		}
	}
	const slots = 1 << 14 // /24s in a /10
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	first := int(h.Sum32() % slots)
	base := platformRange.Addr().As4()
	for i := range slots {
		slot := (first + i) % slots
		addr := netip.AddrFrom4([4]byte{base[0], base[1] | byte(slot>>8), byte(slot), 0})
		cidr := netip.PrefixFrom(addr, 24)
		id, err := EnsureCloudNetwork(ctx, CloudNetwork{Name: name, CIDR: cidr, Labels: labels, DynamicRange: platformDynamicRange(cidr)})
		if err == nil {
			return id, cidr, nil
		}
		if !errors.Is(err, ErrAddressRangeInUse) {
			return "", netip.Prefix{}, err
		}
	}
	return "", netip.Prefix{}, fmt.Errorf("platform network %s: every /24 of %s is held by a live network", name, platformRange)
}

// platformDynamicRange is the upper half of a platform network's /24, which
// the engine allocates from.
func platformDynamicRange(cidr netip.Prefix) netip.Prefix {
	octets := cidr.Masked().Addr().As4()
	octets[3] = 128
	return netip.PrefixFrom(netip.AddrFrom4(octets), 25)
}

// platformLeases is the ledger of the lower half of each platform network:
// the simulator's own addresses, which the engine never allocates.
var platformLeases = struct {
	sync.Mutex
	byNetwork map[string]*ipam.IPAM
	ranges    map[string]netip.Prefix
}{byNetwork: map[string]*ipam.IPAM{}, ranges: map[string]netip.Prefix{}}

// platformLeaseFloor is the first address of a platform network the ledger
// leases: the addresses below it are the gateway's and the fixed ones the
// simulator's sidecars and node agent take.
const platformLeaseFloor = 16

func platformLedger(ctx context.Context, network string) (*ipam.IPAM, error) {
	platformLeases.Lock()
	defer platformLeases.Unlock()
	if ledger, ok := platformLeases.byNetwork[network]; ok {
		return ledger, nil
	}
	cli := DockerClient()
	if err := RequireContainerRuntime("leasing a platform address"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	details, err := cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect platform network %s: %w", network, err)
	}
	var cidr netip.Prefix
	var gateway netip.Addr
	for _, cfg := range details.Network.IPAM.Config {
		if cfg.Subnet.IsValid() && cfg.Subnet.Addr().Is4() {
			if !cfg.IPRange.IsValid() {
				return nil, fmt.Errorf("platform network %s predates leased addresses; remove it once nothing is attached", network)
			}
			cidr, gateway = cfg.Subnet, cfg.Gateway
		}
	}
	if !cidr.IsValid() {
		return nil, fmt.Errorf("platform network %s has no IPv4 range", network)
	}
	if !gateway.IsValid() {
		gateway = cidr.Masked().Addr().Next()
	}
	// The ledger covers the lower half: everything from the floor up to the
	// engine's range, counted as the addresses the subnet keeps at each end.
	ledger, err := ipam.NewIPAMWithReserved(cidr.String(), net.IP(gateway.AsSlice()), ipam.HostReservation{First: platformLeaseFloor, Last: 1 << (32 - cidr.Bits() - 1)})
	if err != nil {
		return nil, err
	}
	// Containers attached already, this process's or an earlier one's, hold
	// their addresses whichever half they fall in.
	for id, endpoint := range details.Network.Containers {
		if endpoint.IPv4Address.IsValid() {
			_, _ = ledger.Reserve(id, net.IP(endpoint.IPv4Address.Addr().AsSlice()))
		}
	}
	platformLeases.byNetwork[network] = ledger
	platformLeases.ranges[network] = cidr
	return ledger, nil
}

// LeasePlatformAddress leases an address of the platform network to owner,
// the one requested when it is valid, and returns it with the release that
// gives it back. The engine never allocates the address to a container, so a
// guest the engine does not place can take it.
func LeasePlatformAddress(ctx context.Context, network, owner string, requested netip.Addr) (netip.Addr, func(), error) {
	ledger, err := platformLedger(ctx, network)
	if err != nil {
		return netip.Addr{}, nil, err
	}
	var want net.IP
	if requested.IsValid() {
		want = net.IP(requested.AsSlice())
	}
	ip, err := ledger.Reserve(owner, want)
	if err != nil {
		return netip.Addr{}, nil, fmt.Errorf("platform network %s: %w", network, err)
	}
	addr, _ := netip.AddrFromSlice(ip)
	addr = addr.Unmap()
	return addr, func() { ledger.Release(ip) }, nil
}

// ReservePlatformAddress records that owner holds addr on the platform
// network when the address is one of the network's — the address of a
// workload an earlier process placed there, which the engine's view of the
// network does not show for a guest — and returns the release. An address
// outside the network is another network's, and the release does nothing.
func ReservePlatformAddress(ctx context.Context, network string, addr netip.Addr, owner string) (func(), error) {
	ledger, err := platformLedger(ctx, network)
	if err != nil {
		return nil, err
	}
	platformLeases.Lock()
	cidr := platformLeases.ranges[network]
	platformLeases.Unlock()
	if !cidr.Contains(addr) {
		return func() {}, nil
	}
	ip := net.IP(addr.AsSlice())
	if _, err := ledger.Reserve(owner, ip); err != nil {
		if errors.Is(err, ipam.ErrAddressInUse) {
			// The engine's view already recorded it: a container's address.
			return func() {}, nil
		}
		return nil, fmt.Errorf("platform network %s: %w", network, err)
	}
	return func() { ledger.Release(ip) }, nil
}

// addressRangeInUseError classifies a network-create failure as "this range
// is taken": netavark reports "subnet … is already used on the host or by
// another config", Docker "Pool overlaps with other one on this address
// space". Anything else is a real daemon failure the caller must surface.
func addressRangeInUseError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already used") || strings.Contains(msg, "overlap")
}

// reclaimOrphanedRange removes simulator networks overlapping the range that
// no container is attached to and that a dead simulator run created, and
// reports whether it removed any.
//
// All the conditions are load-bearing. The simulator label keeps the sweep
// inside networks this project made. The overlap keeps it to what blocks the
// caller. No attached containers means nothing is using it. A different run
// id means the process that made it is not this one, and only that process
// being gone makes the network an orphan: another live simulator on the same
// host has idle networks between two of its workloads too, and they are its.
// So the owner recorded on the network is checked — same hostname, hence the
// same pid namespace, and a pid no process holds — and a network whose owner
// cannot be checked (another hostname, or a network an older release created
// without an owner) is left where it is.
func reclaimOrphanedRange(ctx context.Context, cli *client.Client, cidr netip.Prefix) bool {
	nets, err := cli.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "sockerless-sim=true"),
	})
	if err != nil {
		return false
	}
	reclaimed := false
	for _, n := range nets.Items {
		if (simulatorRunID != "" && n.Labels["sockerless-sim-run"] == simulatorRunID) || !ownerIsDead(n.Labels) {
			continue
		}
		details, err := cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err != nil || len(details.Network.Containers) > 0 {
			continue
		}
		overlaps := false
		for _, cfg := range details.Network.IPAM.Config {
			if cfg.Subnet.IsValid() && cfg.Subnet.Overlaps(cidr) {
				overlaps = true
				break
			}
		}
		if !overlaps {
			continue
		}
		if _, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err == nil {
			reclaimed = true
		}
	}
	return reclaimed
}

// ownerIsDead reports whether the process recorded on a simulator resource's
// labels is known to be gone: it ran on this host, and its pid is not held.
func ownerIsDead(labels map[string]string) bool {
	if labels["sockerless-sim-host"] == "" || labels["sockerless-sim-host"] != simulatorOwnerHost {
		return false
	}
	pid, err := strconv.Atoi(labels["sockerless-sim-pid"])
	if err != nil || pid <= 0 {
		return false
	}
	return !ProcessAlive(pid)
}

// ConnectContainerToNetworkAt connects a running container to an engine
// network at a fixed IPv4 address inside the network's range, with the given
// DNS aliases.
func ConnectContainerToNetworkAt(containerName, networkName string, aliases []string, ipv4 netip.Addr) error {
	return connectContainerToNetwork(containerName, networkName, aliases, ipv4, 0)
}

// ConnectContainerToNetworkBehindGateway connects a container to a network
// at an address without that network ever providing the container's default
// gateway: the engine picks the gateway endpoint by priority and then by
// network name, and a container that bridges guests onto the networks it
// joins must keep its default route on the network it started on.
func ConnectContainerToNetworkBehindGateway(containerName, networkName string, ipv4 netip.Addr) error {
	return connectContainerToNetwork(containerName, networkName, nil, ipv4, -1)
}

func connectContainerToNetwork(containerName, networkName string, aliases []string, ipv4 netip.Addr, gwPriority int) error {
	cli := DockerClient()
	if err := RequireContainerRuntime("connecting a container to a network"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := cli.NetworkConnect(ctx, networkName, client.NetworkConnectOptions{
		Container: containerName,
		EndpointConfig: &network.EndpointSettings{
			Aliases:    aliases,
			IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ipv4},
			GwPriority: gwPriority,
		},
	})
	return err
}

// ContainerNetworkAddresses is a container's IPv4 address on each engine
// network it is attached to, by network name.
func ContainerNetworkAddresses(ctx context.Context, containerID string) (map[string]netip.Addr, error) {
	cli := DockerClient()
	if err := RequireContainerRuntime("inspecting a container's networks"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", containerID, err)
	}
	out := map[string]netip.Addr{}
	if inspected.Container.NetworkSettings == nil {
		return out, nil
	}
	for name, ep := range inspected.Container.NetworkSettings.Networks {
		if ep != nil && ep.IPAddress.IsValid() {
			out[name] = ep.IPAddress
		}
	}
	return out, nil
}

// ContainerEnvironment is the environment a container was created with.
func ContainerEnvironment(ctx context.Context, containerID string) (map[string]string, error) {
	cli := DockerClient()
	if err := RequireContainerRuntime("inspecting a container's environment"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", containerID, err)
	}
	out := map[string]string{}
	if inspected.Container.Config == nil {
		return out, nil
	}
	for _, kv := range inspected.Container.Config.Env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out, nil
}

// ContainerIPv4On returns a container's address on the named engine network,
// or the zero value when it is not on it.
func ContainerIPv4On(ctx context.Context, containerID, network string) netip.Addr {
	cli := DockerClient()
	if cli == nil {
		return netip.Addr{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.NetworkSettings == nil {
		return netip.Addr{}
	}
	if ep := inspected.Container.NetworkSettings.Networks[network]; ep != nil && ep.IPAddress.IsValid() {
		return ep.IPAddress
	}
	return netip.Addr{}
}
