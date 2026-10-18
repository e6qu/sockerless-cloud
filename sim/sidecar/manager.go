package sidecar

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// Manager keeps one sidecar per engine network a simulator places workloads
// on, each with its own listener on the simulator's resolver and its own
// token, and removes them with their networks.
type Manager struct {
	resolver   *dnsserver.Server
	httpOrigin string
	sink       sim.LogSink

	mu      sync.Mutex
	running map[string]*Running
}

// Running is a sidecar the manager started.
type Running struct {
	Network string
	Address netip.Addr
	token   string
	handle  *sim.ContainerHandle
}

// NewManager serves sidecars that forward DNS to resolver and HTTP to the
// simulator at httpOrigin, as workloads reach it (workloadhost.CallbackAddr).
func NewManager(resolver *dnsserver.Server, httpOrigin string, sink sim.LogSink) *Manager {
	if sink == nil {
		sink = sim.NoopSink{}
	}
	return &Manager{resolver: resolver, httpOrigin: httpOrigin, sink: sink, running: map[string]*Running{}}
}

// Ensure runs the sidecar of an engine network that exists, at address — or,
// when address is the zero value, at the second host of the network's range,
// which every cloud reserves — and returns it; a network whose sidecar runs
// gets that one. A sidecar a previous simulator process left on the network
// is replaced: its token and listener died with that process.
func (m *Manager) Ensure(ctx context.Context, network string, address netip.Addr) (*Running, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.running[network]; ok {
		return r, nil
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("starting a network sidecar"); err != nil {
		return nil, err
	}
	if !address.IsValid() {
		subnet, err := networkSubnet(ctx, cli, network)
		if err != nil {
			return nil, err
		}
		address = subnet.Masked().Addr().Next().Next()
	}
	removeStale(network)
	callbackHost, err := workloadhost.CallbackHost()
	if err != nil {
		return nil, err
	}
	port, err := m.resolver.ListenNetwork(network)
	if err != nil {
		return nil, err
	}
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, network)
	handle, err := Start(ctx, Spec{
		Network:      network,
		Address:      address,
		Name:         containerName(network),
		DNSUpstream:  DNSUpstream(callbackHost, port),
		HTTPUpstream: m.httpOrigin,
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
	}, m.sink)
	if err != nil {
		sim.UnregisterSidecarToken(token)
		_ = m.resolver.CloseNetwork(network)
		return nil, fmt.Errorf("sidecar for %s: %w", network, err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := WaitReady(readyCtx, address); err != nil {
		handle.Cancel()
		sim.UnregisterSidecarToken(token)
		_ = m.resolver.CloseNetwork(network)
		return nil, err
	}
	r := &Running{Network: network, Address: address, token: token, handle: handle}
	m.running[network] = r
	return r, nil
}

// Remove stops the network's sidecar, if one runs, so the network can go.
func (m *Manager) Remove(network string) {
	m.mu.Lock()
	r, ok := m.running[network]
	delete(m.running, network)
	m.mu.Unlock()
	if ok {
		m.stop(r)
	}
	removeStale(network)
}

// Close stops every sidecar.
func (m *Manager) Close() {
	m.mu.Lock()
	all := m.running
	m.running = map[string]*Running{}
	m.mu.Unlock()
	for _, r := range all {
		m.stop(r)
	}
}

func (m *Manager) stop(r *Running) {
	r.handle.Cancel()
	<-r.handle.Exited()
	sim.UnregisterSidecarToken(r.token)
	_ = m.resolver.CloseNetwork(r.Network)
}

// removeStale removes a sidecar container of the network that this manager
// did not start.
func removeStale(network string) {
	existing, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-sidecar": network})
	if err != nil {
		return
	}
	for _, c := range existing {
		sim.StopAndRemoveContainer(c.ID, 0)
	}
}

func containerName(network string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(network))
	return "sockerless-sim-sidecar-" + strconv.FormatUint(h.Sum64(), 36)
}

func networkSubnet(ctx context.Context, cli *client.Client, network string) (netip.Prefix, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("sidecar: inspect network %s: %w", network, err)
	}
	for _, cfg := range info.Network.IPAM.Config {
		if cfg.Subnet.IsValid() && cfg.Subnet.Addr().Is4() {
			return cfg.Subnet, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("sidecar: network %s has no IPv4 range", network)
}
