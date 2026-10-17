package placement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
)

// MicroVMBackend places workloads as Firecracker guests on a node agent. A
// workload's image becomes the guest's root filesystem, its command the
// guest's first process, and its network and address its place on the
// engine network the agent joined for it.
type MicroVMBackend struct {
	agent *nodeagent.Agent
	// macPrefix is the three-octet prefix the guests' MACs derive under.
	macPrefix string
}

// NewMicroVMBackend places on agent.
func NewMicroVMBackend(agent *nodeagent.Agent, macPrefix string) *MicroVMBackend {
	if macPrefix == "" {
		macPrefix = "02:fc:00"
	}
	return &MicroVMBackend{agent: agent, macPrefix: macPrefix}
}

func (*MicroVMBackend) Kind() Kind { return MicroVM }

// Agent is the node agent the backend places on.
func (b *MicroVMBackend) Agent() *nodeagent.Agent { return b.agent }

// Join makes the agent a member of an engine network at the address the
// cloud leased for it, before the first workload lands there.
func (b *MicroVMBackend) Join(ctx context.Context, network string, address netip.Addr) error {
	_, err := b.agent.Join(ctx, network, address)
	return err
}

// ErrNoNetwork is returned for a workload that names no engine network or
// no address: a guest has no engine-assigned address to fall back on.
var ErrNoNetwork = errors.New("a microVM workload needs a network the agent joined and an address on it")

func (b *MicroVMBackend) Place(ctx context.Context, w Workload, sink sim.LogSink) (Handle, error) {
	if w.Network == "" || w.IPv4Address == "" {
		return nil, ErrNoNetwork
	}
	agentAddr, ok := b.agent.AddressOn(w.Network)
	if !ok {
		return nil, fmt.Errorf("%w: the agent has not joined %s", ErrNoNetwork, w.Network)
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("placing a microVM workload"); err != nil {
		return nil, err
	}
	if err := sim.PullImageWithCredential(ctx, w.Image, w.Architecture, w.RegistryAuth); err != nil {
		return nil, err
	}
	inspect, err := cli.ImageInspect(ctx, w.Image)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", w.Image, err)
	}
	subnet, err := networkPrefix(ctx, cli, w.Network)
	if err != nil {
		return nil, err
	}
	addr, err := netip.ParseAddr(w.IPv4Address)
	if err != nil || !subnet.Contains(addr) {
		return nil, fmt.Errorf("address %q is not inside %s", w.IPv4Address, subnet)
	}
	id := guestID(w)
	spec := nodeagent.GuestSpec{
		ID:         id,
		Hostname:   w.Hostname,
		WorkingDir: w.WorkingDir,
		Env:        map[string]string{},
		Network: nodeagent.GuestNetwork{
			AgentAddress: agentAddr.String(),
			Address:      netip.PrefixFrom(addr, subnet.Bits()).String(),
			Gateway:      subnet.Masked().Addr().Next().String(),
			MAC:          ipam.DeriveMAC(b.macPrefix, id),
			DNS:          w.DNS,
		},
		StopGrace: w.CancelGracePeriod,
	}
	if cfg := inspect.Config; cfg != nil {
		for _, kv := range cfg.Env {
			k, v, _ := strings.Cut(kv, "=")
			spec.Env[k] = v
		}
		spec.Command = append([]string(nil), cfg.Entrypoint...)
		spec.Args = append([]string(nil), cfg.Cmd...)
		if spec.WorkingDir == "" {
			spec.WorkingDir = cfg.WorkingDir
		}
		spec.User = cfg.User
	}
	for k, v := range w.Env {
		spec.Env[k] = v
	}
	if len(w.Command) > 0 {
		spec.Command = append([]string(nil), w.Command...)
		spec.Args = append([]string(nil), w.Args...)
	} else if len(w.Args) > 0 {
		spec.Args = append([]string(nil), w.Args...)
	}
	if len(spec.Command) == 0 && len(spec.Args) > 0 {
		spec.Command, spec.Args = spec.Args, nil
	}
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("image %s names no entrypoint or command and the workload gives none", w.Image)
	}
	if w.NanoCPU > 0 {
		spec.VCPUs = int((w.NanoCPU + 999_999_999) / 1_000_000_000)
	}
	if w.MemoryBytes > 0 {
		spec.MemoryMiB = int(w.MemoryBytes >> 20)
		if spec.MemoryMiB < 128 {
			spec.MemoryMiB = 128
		}
	}
	for _, f := range w.Seed {
		spec.Seed = append(spec.Seed, nodeagent.SeedFile{Path: f.Path, Mode: f.Mode, Data: f.Data})
	}
	agent := b.agent.Client()
	if _, err := agent.Create(ctx, spec); err != nil {
		return nil, err
	}
	if err := b.uploadRootFS(ctx, cli, agent, id, inspect.ID, w); err != nil {
		_ = agent.Delete(context.WithoutCancel(ctx), id)
		return nil, err
	}
	if _, err := agent.Start(ctx, id); err != nil {
		_ = agent.Delete(context.WithoutCancel(ctx), id)
		return nil, err
	}
	return newMicroVMHandle(agent, id, sink, w.CancelGracePeriod, w.KeepOnExit), nil
}

// Restart boots a kept guest again on its root filesystem.
func (b *MicroVMBackend) Restart(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	agent := b.agent.Client()
	if _, err := agent.Start(ctx, id); err != nil {
		return nil, err
	}
	return newMicroVMHandle(agent, id, sink, w.CancelGracePeriod, w.KeepOnExit), nil
}

// Remove discards a kept guest.
func (b *MicroVMBackend) Remove(ctx context.Context, id string) error {
	return b.agent.Client().Delete(ctx, id)
}

// uploadRootFS streams the image's filesystem, as the engine exports a
// container created from it, into the agent.
func (b *MicroVMBackend) uploadRootFS(ctx context.Context, cli *client.Client, agent *nodeagent.Client, id, imageID string, w Workload) error {
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: imageID, Labels: map[string]string{"sockerless-sim-export": id}},
	})
	if err != nil {
		return fmt.Errorf("export %s: create: %w", w.Image, err)
	}
	defer func() {
		removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = cli.ContainerRemove(removeCtx, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	archive, err := cli.ContainerExport(ctx, created.ID, client.ContainerExportOptions{})
	if err != nil {
		return fmt.Errorf("export %s: %w", w.Image, err)
	}
	defer func() { _ = archive.Close() }()
	return agent.UploadRootFS(ctx, id, archive)
}

func (b *MicroVMBackend) Adopt(context.Context, string, Workload, sim.LogSink) (Handle, error) {
	return nil, errors.New("a microVM guest outlives no simulator process: there is nothing to adopt")
}

func guestID(w Workload) string {
	name := strings.TrimPrefix(w.Name, "sockerless-sim-")
	if name == "" {
		name = "guest-" + sim.RandomHex(12)
	}
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	id := b.String()
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

func networkPrefix(ctx context.Context, cli *client.Client, network string) (netip.Prefix, error) {
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

type microVMHandle struct {
	agent  *nodeagent.Client
	id     string
	grace  time.Duration
	keep   bool
	exited chan struct{}
	result sim.ProcessResult
	once   sync.Once
}

func newMicroVMHandle(agent *nodeagent.Client, id string, sink sim.LogSink, grace time.Duration, keep bool) *microVMHandle {
	h := &microVMHandle{agent: agent, id: id, grace: grace, keep: keep, exited: make(chan struct{})}
	started := time.Now()
	go func() {
		ctx := context.Background()
		_ = agent.Logs(ctx, id, true, func(line nodeagent.LogLine) {
			sink.WriteLog(sim.LogLine{Timestamp: line.Time, Text: line.Text, Stream: line.Stream})
		})
		state, err := agent.Wait(ctx, id)
		h.once.Do(func() {
			h.result = sim.ProcessResult{ExitCode: state.ExitCode, StartedAt: started, StoppedAt: time.Now()}
			if err != nil {
				h.result.Error = err
			} else if state.Error != "" {
				h.result.Error = errors.New(state.Error)
			}
			if !state.StartedAt.IsZero() {
				h.result.StartedAt = state.StartedAt
			}
			if !state.StoppedAt.IsZero() {
				h.result.StoppedAt = state.StoppedAt
			}
			if !h.keep {
				_ = agent.Delete(ctx, id)
			}
			close(h.exited)
		})
	}()
	return h
}

func (h *microVMHandle) ID() string              { return h.id }
func (h *microVMHandle) Kind() Kind              { return MicroVM }
func (h *microVMHandle) Exited() <-chan struct{} { return h.exited }

func (h *microVMHandle) Wait() sim.ProcessResult {
	<-h.exited
	return h.result
}

func (h *microVMHandle) Cancel() {
	ctx, cancel := context.WithTimeout(context.Background(), h.grace+30*time.Second)
	defer cancel()
	_ = h.agent.Stop(ctx, h.id)
}

func (h *microVMHandle) Exec(ctx context.Context, command []string) (ExecResult, error) {
	out, err := h.agent.Exec(ctx, h.id, command)
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode}, nil
}

// PublishedPort has nothing to return: a guest publishes no port on the
// host's loopback, and the simulator reaches it at its address.
func (h *microVMHandle) PublishedPort(context.Context, int) (int, error) {
	return 0, fmt.Errorf("guest %s publishes no host port; reach it at its network address", h.id)
}

// SetIngress replaces the rules at the guest's tap.
func (h *microVMHandle) SetIngress(ctx context.Context, rules []nodeagent.PacketRule) error {
	return h.agent.SetIngress(ctx, h.id, rules)
}

var _ io.Reader = (io.Reader)(nil)
