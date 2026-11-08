package sidecar

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/gwlb"
)

// GatewayLoadBalancerEndpoint is one Gateway Load Balancer endpoint a node
// serves: its identifier, and its address with the prefix length of the
// network it sits on.
type GatewayLoadBalancerEndpoint struct {
	ID      string
	Network string
	Address netip.Prefix
}

// GatewayLoadBalancerNodeSpec places one zone's node of a Gateway Load
// Balancer: on the load balancer's network at its address there, and on each
// endpoint's network at the endpoint's address.
type GatewayLoadBalancerNodeSpec struct {
	// Node names the node in its state requests.
	Node      string
	Name      string
	Network   string
	Address   netip.Addr
	Endpoints []GatewayLoadBalancerEndpoint
	// HTTPUpstream is the simulator's HTTP origin as the node reaches it,
	// where it asks for its state.
	HTTPUpstream string
	// Token is the credential the simulator minted for this node.
	Token      string
	ExtraHosts []string
	Labels     map[string]string
}

// StartGatewayLoadBalancerNode runs a node and returns its handle once it
// carries traffic, or when ctx ends first; the container goes with the
// handle's Cancel. The node programs its routing and its TUN devices in its
// own network namespace, which the one capability it keeps and the TUN device
// it is handed allow, and nothing else.
func StartGatewayLoadBalancerNode(ctx context.Context, spec GatewayLoadBalancerNodeSpec, sink sim.LogSink) (*sim.ContainerHandle, error) {
	if spec.Node == "" || spec.Network == "" || !spec.Address.IsValid() || len(spec.Endpoints) == 0 || spec.HTTPUpstream == "" || spec.Token == "" {
		return nil, errors.New("sidecar: a Gateway Load Balancer node needs a node name, a network, an address, endpoints, the HTTP upstream and a token")
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("starting a Gateway Load Balancer node"); err != nil {
		return nil, err
	}
	image, platform, err := EnsureImage(ctx, cli)
	if err != nil {
		return nil, err
	}
	// The engine puts a container on a network once; a second address on a
	// network the node is already on is the node's own to add.
	joined := map[string]bool{spec.Network: true}
	var join []sim.NetworkAttachment
	endpoints := make([]string, 0, len(spec.Endpoints))
	for _, e := range spec.Endpoints {
		if !joined[e.Network] {
			joined[e.Network] = true
			join = append(join, sim.NetworkAttachment{Network: e.Network, IPv4Address: e.Address.Addr().String()})
		}
		endpoints = append(endpoints, e.ID+"="+e.Address.String())
	}
	labels := map[string]string{"sockerless-sim-gwlb-node": spec.Node}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	serving := make(chan struct{})
	var once sync.Once
	var last lastLines
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Name:         spec.Name,
		Network:      spec.Network,
		IPv4Address:  spec.Address.String(),
		JoinNetworks: join,
		Args:         []string{"gwlb"},
		ExtraHosts:   spec.ExtraHosts,
		Labels:       labels,
		Devices:      []string{"/dev/net/tun"},
		// The node inherits the host's reverse-path filter, which drops what
		// an appliance returns: it leaves a TUN device that holds no address.
		Sysctls: map[string]string{
			"net.ipv4.conf.all.rp_filter":     "0",
			"net.ipv4.conf.default.rp_filter": "0",
		},
		Env: map[string]string{
			gwlb.EnvNode:            spec.Node,
			gwlb.EnvListen:          spec.Address.String(),
			gwlb.EnvEndpoints:       strings.Join(endpoints, ","),
			"SIDECAR_HTTP_UPSTREAM": spec.HTTPUpstream,
			"SIDECAR_TOKEN":         spec.Token,
		},
		Sandbox: sim.SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"NET_ADMIN"},
			NoNewPrivileges:  true,
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
			ReadonlyRootfs:   true,
		},
	}, sim.FuncSink(func(line sim.LogLine) {
		last.add(line.Text)
		sink.WriteLog(line)
		if strings.HasPrefix(line.Text, gwlb.ServingPrefix) {
			once.Do(func() { close(serving) })
		}
	}))
	if err != nil {
		return nil, err
	}
	select {
	case <-serving:
		return handle, nil
	case <-handle.Exited():
		handle.Cancel()
		return nil, fmt.Errorf("sidecar: the Gateway Load Balancer node %s ended before it served: %s", spec.Name, &last)
	case <-ctx.Done():
		handle.Cancel()
		return nil, ctx.Err()
	}
}
