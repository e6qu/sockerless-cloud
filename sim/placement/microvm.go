package placement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

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
	// shares serves the host directories guests mount for their binds.
	shares shares
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

// Leave takes the agent off an engine network the cloud is removing, once
// nothing of the cloud's is left on it.
func (b *MicroVMBackend) Leave(ctx context.Context, network string) error {
	return b.agent.Leave(ctx, network)
}

// ErrNoNetwork is returned for a workload that names no engine network or
// no address: a guest has no engine-assigned address to fall back on.
var ErrNoNetwork = errors.New("a microVM workload needs a network the agent joined and an address on it")

// ErrBindMounts is wrapped for a workload whose bind the backend cannot
// serve to a guest: a named volume, which the engine holds where this
// process cannot read it, or a single file. A host directory is served as a
// file share the guest mounts, and the backend refuses the rest rather than
// run the workload without them.
var ErrBindMounts = errors.New("a microVM workload cannot bind an engine volume or a single file into its filesystem")

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
	image, err := imageProcess(ctx, cli, w)
	if err != nil {
		return nil, err
	}
	subnet, err := networkPrefix(ctx, cli, w.Network)
	if err != nil {
		return nil, err
	}
	addr, err := netip.ParseAddr(w.IPv4Address)
	if err != nil || !subnet.Contains(addr) {
		return nil, fmt.Errorf("address %q is not inside %s", w.IPv4Address, subnet)
	}
	gateway := subnet.Masked().Addr().Next().String()
	mounts, err := b.shares.mountsFor(w, gateway)
	if err != nil {
		return nil, err
	}
	id := guestID(w)
	var extra []nodeagent.GuestNetwork
	for i, attachment := range w.ExtraNetworks {
		extraAgentAddr, ok := b.agent.AddressOn(attachment.Network)
		if !ok {
			return nil, fmt.Errorf("%w: the agent has not joined %s", ErrNoNetwork, attachment.Network)
		}
		extraSubnet, err := networkPrefix(ctx, cli, attachment.Network)
		if err != nil {
			return nil, err
		}
		if attachment.IPv4Address == "" {
			return nil, fmt.Errorf("%w: a guest's interface on %s needs an address", ErrNoNetwork, attachment.Network)
		}
		extraAddr, err := netip.ParseAddr(attachment.IPv4Address)
		if err != nil || !extraSubnet.Contains(extraAddr) {
			return nil, fmt.Errorf("address %q is not inside %s", attachment.IPv4Address, extraSubnet)
		}
		extra = append(extra, nodeagent.GuestNetwork{
			AgentAddress: extraAgentAddr.String(),
			Address:      netip.PrefixFrom(extraAddr, extraSubnet.Bits()).String(),
			Gateway:      extraSubnet.Masked().Addr().Next().String(),
			MAC:          guestMAC(b.macPrefix, extraAddr, fmt.Sprintf("%s-%d", id, i+1)),
		})
	}
	spec := nodeagent.GuestSpec{
		ID:         id,
		Hostname:   w.Hostname,
		Hosts:      w.ExtraHosts,
		Labels:     sim.OwnershipLabels(w.Labels),
		WorkingDir: image.workingDir,
		Command:    image.command,
		Args:       image.args,
		Env:        image.env,
		User:       image.user,
		Network: nodeagent.GuestNetwork{
			AgentAddress: agentAddr.String(),
			Address:      netip.PrefixFrom(addr, subnet.Bits()).String(),
			Gateway:      gateway,
			MAC:          guestMAC(b.macPrefix, addr, id),
			DNS:          w.DNS,
		},
		StopGrace:     w.CancelGracePeriod,
		Seed:          seedFiles(w),
		Mounts:        mounts,
		ExtraNetworks: extra,
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
	agent := b.agent.Client()
	if _, err := agent.Create(ctx, spec); err != nil {
		return nil, err
	}
	if err := b.uploadRootFS(ctx, cli, id, image.id, w, func(archive io.Reader) error { return agent.UploadRootFS(ctx, id, archive) }); err != nil {
		_ = agent.Delete(context.WithoutCancel(ctx), id)
		return nil, err
	}
	if _, err := agent.Start(ctx, id); err != nil {
		_ = agent.Delete(context.WithoutCancel(ctx), id)
		return nil, err
	}
	return newMicroVMHandle(agent, id, w.IPv4Address, sink, w.CancelGracePeriod, w.KeepOnExit), nil
}

// PlaceMember runs w as a member of the owner's guest: its image's
// filesystem becomes a root filesystem of its own on one of the guest's
// disk slots, and its command a process tree beside the guest's first
// process, on the guest's network.
func (b *MicroVMBackend) PlaceMember(ctx context.Context, owner string, w Workload, sink sim.LogSink) (Handle, error) {
	if err := memberShape(w); err != nil {
		return nil, err
	}
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("placing a microVM member"); err != nil {
		return nil, err
	}
	agent := b.agent.Client()
	guest, err := agent.Get(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("member %s of guest %s: %w", w.Name, owner, err)
	}
	mounts, err := b.shares.mountsFor(w, guest.Gateway)
	if err != nil {
		return nil, err
	}
	if w.Architecture == "" {
		w.Architecture = "linux/" + b.agent.Architecture()
	}
	image, err := imageProcess(ctx, cli, w)
	if err != nil {
		return nil, err
	}
	name := guestID(w)
	spec := nodeagent.MemberSpec{
		Name:       name,
		Labels:     sim.OwnershipLabels(w.Labels),
		Command:    image.command,
		Args:       image.args,
		Env:        image.env,
		WorkingDir: image.workingDir,
		User:       image.user,
		Seed:       seedFiles(w),
		StopGrace:  w.CancelGracePeriod,
		Mounts:     mounts,
	}
	if _, err := agent.CreateMember(ctx, owner, spec); err != nil {
		return nil, err
	}
	if err := b.uploadRootFS(ctx, cli, name, image.id, w, func(archive io.Reader) error { return agent.UploadMemberRootFS(ctx, owner, name, archive) }); err != nil {
		return nil, err
	}
	if _, err := agent.StartMember(ctx, owner, name); err != nil {
		return nil, err
	}
	return newMemberHandle(agent, owner, name, guest.Address, sink, w.CancelGracePeriod), nil
}

// imageDetails is the process an image declares, under the workload's own
// command, arguments and environment.
type imageDetails struct {
	id         string
	command    []string
	args       []string
	env        map[string]string
	workingDir string
	user       string
}

// imageProcess pulls the workload's image and resolves the process it runs:
// the image's entrypoint and command, environment, working directory and
// user, with the workload's own over them, as a container runtime does.
func imageProcess(ctx context.Context, cli *client.Client, w Workload) (imageDetails, error) {
	if err := sim.PullImageWithCredential(ctx, w.Image, w.Architecture, w.RegistryAuth); err != nil {
		return imageDetails{}, err
	}
	inspect, err := cli.ImageInspect(ctx, w.Image)
	if err != nil {
		return imageDetails{}, fmt.Errorf("inspect %s: %w", w.Image, err)
	}
	details := imageDetails{id: inspect.ID, env: map[string]string{}, workingDir: w.WorkingDir}
	if cfg := inspect.Config; cfg != nil {
		for _, kv := range cfg.Env {
			k, v, _ := strings.Cut(kv, "=")
			details.env[k] = v
		}
		details.command = append([]string(nil), cfg.Entrypoint...)
		details.args = append([]string(nil), cfg.Cmd...)
		if details.workingDir == "" {
			details.workingDir = cfg.WorkingDir
		}
		details.user = cfg.User
	}
	for k, v := range w.Env {
		details.env[k] = v
	}
	if len(w.Command) > 0 {
		details.command = append([]string(nil), w.Command...)
		details.args = append([]string(nil), w.Args...)
	} else if len(w.Args) > 0 {
		details.args = append([]string(nil), w.Args...)
	}
	if len(details.command) == 0 && len(details.args) > 0 {
		details.command, details.args = details.args, nil
	}
	if len(details.command) == 0 {
		return imageDetails{}, fmt.Errorf("image %s names no entrypoint or command and the workload gives none", w.Image)
	}
	return details, nil
}

func seedFiles(w Workload) []nodeagent.SeedFile {
	var out []nodeagent.SeedFile
	for _, f := range w.Seed {
		out = append(out, nodeagent.SeedFile{Path: f.Path, Mode: f.Mode, Data: f.Data})
	}
	return out
}

// Restart boots a kept guest again on its root filesystem.
func (b *MicroVMBackend) Restart(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	agent := b.agent.Client()
	state, err := agent.Start(ctx, id)
	if err != nil {
		return nil, err
	}
	return newMicroVMHandle(agent, id, state.Address, sink, w.CancelGracePeriod, w.KeepOnExit), nil
}

// Remove discards a kept guest.
func (b *MicroVMBackend) Remove(ctx context.Context, id string) error {
	return b.agent.Client().Delete(ctx, id)
}

// Console is what the guest's first process wrote across its boots, which
// the agent keeps while the guest exists.
func (b *MicroVMBackend) Console(ctx context.Context, id string) (string, error) {
	var out strings.Builder
	err := b.agent.Client().Logs(ctx, id, nodeagent.LogOptions{}, func(line nodeagent.LogLine) {
		out.WriteString(line.Text)
		out.WriteByte('\n')
	})
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// Capture imports the guest's root filesystem, as the agent exports it, as
// an engine image under reference.
func (b *MicroVMBackend) Capture(ctx context.Context, id, reference string) error {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("capturing a microVM workload's disk"); err != nil {
		return err
	}
	archive, err := b.agent.Client().ExportRootFS(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	result, err := cli.ImageImport(ctx, client.ImageImportSource{Source: archive, SourceName: "-"}, reference, client.ImageImportOptions{
		Platform: ocispec.Platform{OS: "linux", Architecture: b.agent.Architecture()},
	})
	if err != nil {
		return fmt.Errorf("import the disk of guest %s as %s: %w", id, reference, err)
	}
	defer func() { _ = result.Close() }()
	decoder := json.NewDecoder(result)
	for {
		var message struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("import the disk of guest %s as %s: %w", id, reference, err)
		}
		if message.Error != "" {
			return fmt.Errorf("import the disk of guest %s as %s: %s", id, reference, message.Error)
		}
	}
}

// uploadRootFS streams the image's filesystem, as the engine exports a
// container created from it, through upload.
func (b *MicroVMBackend) uploadRootFS(ctx context.Context, cli *client.Client, id, imageID string, w Workload, upload func(io.Reader) error) error {
	// The container is created to be exported, never started, and the
	// engine still wants a command: a captured image, imported from an
	// archive, declares none.
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: imageID, Cmd: []string{"/sbin/sockerless-init"}, Labels: map[string]string{"sockerless-sim-export": id}},
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
	return upload(archive)
}

// Adopt takes over a guest a previous simulator process placed on an agent
// this process adopted, running or kept after its exit, or a member of one
// by its owner/name ID.
func (b *MicroVMBackend) Adopt(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	agent := b.agent.Client()
	owner, member, isMember := strings.Cut(id, "/")
	state, err := agent.Get(ctx, owner)
	if err != nil {
		return nil, err
	}
	if !isMember {
		return newMicroVMHandle(agent, id, state.Address, sink, w.CancelGracePeriod, w.KeepOnExit), nil
	}
	for _, m := range state.Members {
		if m.Name == member {
			return newMemberHandle(agent, owner, member, state.Address, sink, w.CancelGracePeriod), nil
		}
	}
	return nil, fmt.Errorf("guest %s has no member %s: %w", owner, member, nodeagent.ErrNotFound)
}

// Find lists the agent's guests carrying every label and this simulator's
// persistent identity.
func (b *MicroVMBackend) Find(ctx context.Context, labels map[string]string) ([]Existing, error) {
	guests, err := b.agent.Client().List(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]string{}
	if state, ok := sim.OwnershipLabels(nil)["sockerless-sim-state"]; ok {
		want["sockerless-sim-state"] = state
	}
	for k, v := range labels {
		want[k] = v
	}
	var out []Existing
	for _, g := range guests {
		if matchesLabels(g.Labels, want) {
			out = append(out, Existing{ID: g.ID, Running: g.Status == nodeagent.GuestRunning && g.ExitedAt.IsZero(), Labels: g.Labels})
		}
		for _, m := range g.Members {
			if matchesLabels(m.Labels, want) {
				out = append(out, Existing{ID: g.ID + "/" + m.Name, Running: m.Status == nodeagent.GuestRunning, Labels: m.Labels})
			}
		}
	}
	return out, nil
}

// matchesLabels reports whether have carries every label of want; an empty
// wanted value asks only for the key's presence, as the engine's label
// filter does.
func matchesLabels(have, want map[string]string) bool {
	for k, v := range want {
		got, ok := have[k]
		if !ok || (v != "" && got != v) {
			return false
		}
	}
	return true
}

// guestMAC is the guest interface's address under the cloud's prefix: a
// two-octet prefix, Compute Engine's 42:01, takes the four octets of the
// interface's IPv4 address, as Compute Engine does; a three-octet prefix
// takes three octets hashed from the guest's identifier.
func guestMAC(prefix string, addr netip.Addr, id string) string {
	if strings.Count(prefix, ":") == 1 {
		octets := addr.As4()
		return fmt.Sprintf("%s:%02x:%02x:%02x:%02x", prefix, octets[0], octets[1], octets[2], octets[3])
	}
	return ipam.DeriveMAC(prefix, id)
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
	agent   *nodeagent.Client
	id      string
	address string
	grace   time.Duration
	keep    bool
	exited  chan struct{}
	result  sim.ProcessResult
	once    sync.Once
}

func newMicroVMHandle(agent *nodeagent.Client, id, address string, sink sim.LogSink, grace time.Duration, keep bool) *microVMHandle {
	h := &microVMHandle{agent: agent, id: id, address: address, grace: grace, keep: keep, exited: make(chan struct{})}
	started := time.Now()
	go func() {
		ctx := context.Background()
		_ = agent.Logs(ctx, id, nodeagent.LogOptions{Follow: true, CurrentBoot: true, Process: nodeagent.InitProcess}, func(line nodeagent.LogLine) {
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
			if !state.ExitedAt.IsZero() {
				h.result.StoppedAt = state.ExitedAt
			}
			close(h.exited)
		})
		if !h.keep {
			// The machine outlives its first process while members run; the
			// guest goes once it has stopped, as a container that is not
			// kept goes once it exits.
			_, _ = agent.WaitStopped(ctx, id)
			_ = agent.Delete(ctx, id)
		}
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

// Cancel and Stop are one thing for a guest: guestinit sends the first
// process SIGTERM, kills it after the grace, and reports how it exited.
func (h *microVMHandle) Cancel() { h.Stop() }

func (h *microVMHandle) Stop() {
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

func (h *microVMHandle) Address(context.Context) (string, error) { return h.address, nil }

// memberHandle is a member of a guest: a process tree the guest runs from
// the member's own root filesystem, on the guest's network.
type memberHandle struct {
	agent   *nodeagent.Client
	owner   string
	name    string
	address string
	grace   time.Duration
	exited  chan struct{}
	once    sync.Once
	result  sim.ProcessResult
}

func newMemberHandle(agent *nodeagent.Client, owner, name, address string, sink sim.LogSink, grace time.Duration) *memberHandle {
	h := &memberHandle{agent: agent, owner: owner, name: name, address: address, grace: grace, exited: make(chan struct{})}
	started := time.Now()
	go func() {
		ctx := context.Background()
		_ = agent.Logs(ctx, owner, nodeagent.LogOptions{Follow: true, CurrentBoot: true, Process: name}, func(line nodeagent.LogLine) {
			sink.WriteLog(sim.LogLine{Timestamp: line.Time, Text: line.Text, Stream: line.Stream})
		})
		state, err := agent.WaitMember(ctx, owner, name)
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
			close(h.exited)
		})
	}()
	return h
}

func (h *memberHandle) ID() string              { return h.owner + "/" + h.name }
func (h *memberHandle) Kind() Kind              { return MicroVM }
func (h *memberHandle) Exited() <-chan struct{} { return h.exited }

func (h *memberHandle) Wait() sim.ProcessResult {
	<-h.exited
	return h.result
}

func (h *memberHandle) Cancel() { h.Stop() }

func (h *memberHandle) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), h.grace+30*time.Second)
	defer cancel()
	_ = h.agent.StopMember(ctx, h.owner, h.name)
}

func (h *memberHandle) Exec(ctx context.Context, command []string) (ExecResult, error) {
	out, err := h.agent.ExecIn(ctx, h.owner, h.name, command)
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode}, nil
}

func (h *memberHandle) PublishedPort(context.Context, int) (int, error) {
	return 0, fmt.Errorf("member %s of guest %s publishes no host port; reach the guest at its network address", h.name, h.owner)
}

func (h *memberHandle) Address(context.Context) (string, error) { return h.address, nil }

// SetIngress replaces the rules at the guest's tap.
func (h *microVMHandle) SetIngress(ctx context.Context, rules []nodeagent.PacketRule) error {
	return h.agent.SetIngress(ctx, h.id, rules)
}
