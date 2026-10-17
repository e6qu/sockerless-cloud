package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// awsWorkloadNetwork is an engine network a workload lands on and the sidecar
// that answers the cloud's resolver and metadata addresses inside it.
type awsWorkloadNetwork struct {
	Name    string
	Sidecar netip.Addr
}

// DNS is the resolver list a workload on the network gets.
func (n awsWorkloadNetwork) DNS() []string { return []string{n.Sidecar.String()} }

// MetadataOrigin is the HTTP origin the sidecar forwards to the simulator.
func (n awsWorkloadNetwork) MetadataOrigin() string { return "http://" + n.Sidecar.String() }

var errNoNetworkSidecars = errors.New("the Route 53 Resolver is not running, so no network sidecar can start")

// awsPlatformNetworkName names the engine network carrying every workload that
// is on no VPC. The scope keeps a concurrent simulator's platform network apart.
func awsPlatformNetworkName() string {
	return "sockerless-sim-aws-platform-" + sim.WorkloadScope()
}

// awsPlatformNetwork ensures the platform network and its sidecar at the
// network's second host address.
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

// awsNetworkSubnet is the IPv4 range an engine network holds.
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
