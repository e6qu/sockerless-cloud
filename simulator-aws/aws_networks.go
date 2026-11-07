package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The sidecar answers the cloud's resolver and metadata addresses on the
// network.
type awsWorkloadNetwork struct {
	Name    string
	Sidecar netip.Addr
}

func (n awsWorkloadNetwork) DNS() []string { return []string{n.Sidecar.String()} }

func (n awsWorkloadNetwork) MetadataOrigin() string { return "http://" + n.Sidecar.String() }

// The fixed link-local addresses AWS serves metadata on inside a VPC: the
// instance metadata service, and the Amazon ECS agent's task metadata and
// container credentials endpoints.
var (
	awsInstanceMetadataAddress = netip.MustParseAddr("169.254.169.254")
	awsECSAgentAddress         = netip.MustParseAddr("169.254.170.2")
)

// LinkLocalRoutes routes each address to the network's sidecar, which answers
// at it.
func (n awsWorkloadNetwork) LinkLocalRoutes(addrs ...netip.Addr) []sim.Route {
	routes := make([]sim.Route, 0, len(addrs))
	for _, a := range addrs {
		routes = append(routes, sim.Route{Destination: netip.PrefixFrom(a, 32), Via: n.Sidecar})
	}
	return routes
}

var errNoNetworkSidecars = errors.New("the Route 53 Resolver is not running, so no network sidecar can start")

// The platform network carries workloads on no VPC; the scope separates
// concurrent simulators.
func awsPlatformNetworkName() string {
	return "sockerless-sim-aws-platform-" + sim.WorkloadScope()
}

// The sidecar sits at the network's second host address.
func awsPlatformNetwork(ctx context.Context) (awsWorkloadNetwork, error) {
	sidecars, err := awsNetworkSidecars()
	if err != nil {
		return awsWorkloadNetwork{}, err
	}
	name := awsPlatformNetworkName()
	if _, _, err := sim.EnsurePlatformNetwork(ctx, name, nil); err != nil {
		return awsWorkloadNetwork{}, err
	}
	running, err := sidecars.Ensure(ctx, name, netip.Addr{})
	if err != nil {
		return awsWorkloadNetwork{}, err
	}
	return awsWorkloadNetwork{Name: name, Sidecar: running.Address}, nil
}

func awsNetworkSubnet(ctx context.Context, network string) (netip.Prefix, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("inspecting a network"); err != nil {
		return netip.Prefix{}, err
	}
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
