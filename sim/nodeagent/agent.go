package nodeagent

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Agent is a node agent the simulator started as a container: the one
// container holding /dev/kvm, /dev/net/tun and the network capability inside
// its own namespace.
type Agent struct {
	handle *sim.ContainerHandle
	client *Client
	token  string
	// architecture is the engine's, which is the only one the agent's guests
	// run on.
	architecture string

	mu       sync.Mutex
	networks map[string]netip.Addr
}

// Options place the agent.
type Options struct {
	// Name is the container's name.
	Name string
	// Network is the engine network the agent starts on, at Address; the
	// simulator reaches the agent's API there. Every other network comes
	// through Join.
	Network string
	Address netip.Addr
	Labels  map[string]string
}

// Start builds the agent image if the engine lacks it, starts the agent and
// returns once its API answers.
func Start(ctx context.Context, opts Options, sink sim.LogSink) (*Agent, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("starting a node agent"); err != nil {
		return nil, err
	}
	if opts.Network == "" || !opts.Address.IsValid() {
		return nil, fmt.Errorf("node agent: a network and an address on it are required")
	}
	image, platform, err := EnsureImage(ctx, cli)
	if err != nil {
		return nil, err
	}
	token := sim.RandomHex(48)
	labels := map[string]string{"sockerless-sim-node-agent": "true"}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Name:         opts.Name,
		Network:      opts.Network,
		IPv4Address:  opts.Address.String(),
		Labels:       labels,
		Devices:      []string{"/dev/kvm", "/dev/net/tun"},
		Env:          map[string]string{"NODEAGENT_TOKEN": token},
		Sandbox: sim.SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"NET_ADMIN", "NET_RAW", "SYS_ADMIN", "MKNOD", "CHOWN", "FOWNER", "DAC_OVERRIDE", "SETUID", "SETGID", "KILL"},
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
		},
	}, sink)
	if err != nil {
		return nil, fmt.Errorf("node agent: %w", err)
	}
	a := &Agent{handle: handle, token: token, architecture: strings.TrimPrefix(platform, "linux/"), networks: map[string]netip.Addr{opts.Network: opts.Address}}
	a.client = NewClient("http://"+net.JoinHostPort(opts.Address.String(), strconv.Itoa(apiPort)), token)
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := a.client.WaitReady(readyCtx); err != nil {
		handle.Cancel()
		return nil, fmt.Errorf("node agent: %w", err)
	}
	return a, nil
}

// Adopt takes over a node agent a previous process of this persistent
// simulator left running, found by its labels, with its API reached on
// network, and reports false when none runs. The agent's token is read from
// the container's environment, where the process that started it put it.
func Adopt(ctx context.Context, network string, labels map[string]string, sink sim.LogSink) (*Agent, bool, error) {
	if err := sim.RequireContainerRuntime("adopting a node agent"); err != nil {
		return nil, false, err
	}
	want := map[string]string{"sockerless-sim-node-agent": "true"}
	for k, v := range labels {
		want[k] = v
	}
	existing, err := sim.FindExistingContainers(want)
	if err != nil {
		return nil, false, fmt.Errorf("node agent: find: %w", err)
	}
	var id string
	for _, c := range existing {
		if c.Running {
			id = c.ID
			break
		}
	}
	if id == "" {
		return nil, false, nil
	}
	env, err := sim.ContainerEnvironment(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("node agent: %w", err)
	}
	token := env["NODEAGENT_TOKEN"]
	if token == "" {
		return nil, false, fmt.Errorf("node agent: container %s carries no token", id[:12])
	}
	addresses, err := sim.ContainerNetworkAddresses(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("node agent: %w", err)
	}
	address, ok := addresses[network]
	if !ok {
		return nil, false, fmt.Errorf("node agent: container %s is not on %s", id[:12], network)
	}
	info, err := sim.DockerClient().Info(ctx, client.InfoOptions{})
	if err != nil {
		return nil, false, fmt.Errorf("node agent: engine info: %w", err)
	}
	arch, err := goArch(info.Info.Architecture)
	if err != nil {
		return nil, false, err
	}
	handle, err := sim.AdoptContainer(id, sim.ContainerConfig{Name: "node-agent"}, sink)
	if err != nil {
		return nil, false, fmt.Errorf("node agent: %w", err)
	}
	a := &Agent{handle: handle, token: token, architecture: arch, networks: addresses}
	a.client = NewClient("http://"+net.JoinHostPort(address.String(), strconv.Itoa(apiPort)), token)
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.client.WaitReady(readyCtx); err != nil {
		return nil, false, fmt.Errorf("node agent: adopted %s: %w", id[:12], err)
	}
	return a, true, nil
}

// apiPort is the port the agent program serves on.
const apiPort = 8700

// Client is the agent's API client.
func (a *Agent) Client() *Client { return a.client }

// Architecture is the OCI architecture the agent's guests run on.
func (a *Agent) Architecture() string { return a.architecture }

// ContainerID is the agent container's ID.
func (a *Agent) ContainerID() string { return a.handle.ContainerID }

// Join connects the agent to an engine network at the address the cloud
// leased for it, so guests can bridge onto that network, and returns the
// address; a network already joined returns the address it has there.
func (a *Agent) Join(ctx context.Context, network string, address netip.Addr) (netip.Addr, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if have, ok := a.networks[network]; ok {
		return have, nil
	}
	if err := sim.ConnectContainerToNetworkBehindGateway(a.handle.ContainerID, network, address); err != nil {
		return netip.Addr{}, fmt.Errorf("node agent: join %s at %s: %w", network, address, err)
	}
	got := sim.ContainerIPv4On(ctx, a.handle.ContainerID, network)
	if !got.IsValid() {
		return netip.Addr{}, fmt.Errorf("node agent: joined %s but holds no address on it", network)
	}
	a.networks[network] = got
	return got, nil
}

// AddressOn is the agent's address on a network it joined.
func (a *Agent) AddressOn(network string) (netip.Addr, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	addr, ok := a.networks[network]
	return addr, ok
}

// Stop ends every guest and the agent.
func (a *Agent) Stop() {
	a.handle.Cancel()
	<-a.handle.Exited()
}
