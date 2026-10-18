package sim

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
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
		resp, err := cli.NetworkCreate(ctx, spec.Name, client.NetworkCreateOptions{
			Driver: "bridge",
			IPAM: &network.IPAM{
				Config: []network.IPAMConfig{{Subnet: spec.CIDR.Masked(), Gateway: gateway}},
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
// one, so two simulators on one engine each get their own.
func EnsurePlatformNetwork(ctx context.Context, name string, labels map[string]string) (string, netip.Prefix, error) {
	if err := RequireContainerRuntime("creating the platform network"); err != nil {
		return "", netip.Prefix{}, err
	}
	cli := DockerClient()
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if existing, err := cli.NetworkInspect(inspectCtx, name, client.NetworkInspectOptions{}); err == nil {
		for _, cfg := range existing.Network.IPAM.Config {
			if cfg.Subnet.IsValid() && cfg.Subnet.Addr().Is4() {
				return existing.Network.ID, cfg.Subnet, nil
			}
		}
		return "", netip.Prefix{}, fmt.Errorf("platform network %s has no IPv4 range", name)
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
		id, err := EnsureCloudNetwork(ctx, CloudNetwork{Name: name, CIDR: cidr, Labels: labels})
		if err == nil {
			return id, cidr, nil
		}
		if !errors.Is(err, ErrAddressRangeInUse) {
			return "", netip.Prefix{}, err
		}
	}
	return "", netip.Prefix{}, fmt.Errorf("platform network %s: every /24 of %s is held by a live network", name, platformRange)
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
