package placement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/netrules"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
)

// ContainerBackend places workloads as containers on the engine the
// simulator started against.
type ContainerBackend struct{}

func (ContainerBackend) Kind() Kind { return Container }

func (ContainerBackend) Place(ctx context.Context, w Workload, sink sim.LogSink) (Handle, error) {
	h, err := sim.StartContainerSyncContext(ctx, w, sink)
	if err != nil {
		return nil, err
	}
	for _, extra := range w.ExtraNetworks {
		var at netip.Addr
		if extra.IPv4Address != "" {
			if at, err = netip.ParseAddr(extra.IPv4Address); err != nil {
				h.Cancel()
				return nil, fmt.Errorf("join %s at %q: %w", extra.Network, extra.IPv4Address, err)
			}
		}
		if err := sim.ConnectContainerToNetworkAt(h.ContainerID, extra.Network, nil, at); err != nil {
			h.Cancel()
			return nil, fmt.Errorf("join %s: %w", extra.Network, err)
		}
	}
	return containerHandle{h, w.CancelGracePeriod}, nil
}

// PlaceMember starts w as a container in the owner container's network
// namespace: the namespace of the forwarder the owner joined, when it was
// placed with forwards.
func (ContainerBackend) PlaceMember(ctx context.Context, owner string, w Workload, sink sim.LogSink) (Handle, error) {
	if err := memberShape(w); err != nil {
		return nil, err
	}
	forwarder, err := forwarderOf(ctx, owner)
	if err != nil {
		return nil, err
	}
	if forwarder != "" {
		owner = forwarder
	}
	w.NetworkMode = "container:" + owner
	h, err := sim.StartContainerSyncContext(ctx, w, sink)
	if err != nil {
		return nil, err
	}
	return containerHandle{h, w.CancelGracePeriod}, nil
}

// PlaceForwarded places a forwarder that owns the workload's network
// namespace, with every network setting w names, and starts w joined to it
// once the forwards listen. A workload that keeps its filesystem across stops
// or names a network mode of its own has no namespace a forwarder can own.
func (b ContainerBackend) PlaceForwarded(ctx context.Context, w Workload, forwards []Forward, sink sim.LogSink) (Handle, error) {
	if len(forwards) == 0 {
		return b.Place(ctx, w, sink)
	}
	if w.KeepOnExit || w.NetworkMode != "" {
		return nil, fmt.Errorf("place %s with forwards: a workload that keeps its filesystem or names its own network mode has no namespace a forwarder can own", w.Name)
	}
	namespace := sim.ContainerConfig{
		Network:        w.Network,
		NetworkAliases: w.NetworkAliases,
		IPv4Address:    w.IPv4Address,
		MACAddress:     w.MACAddress,
		Hostname:       w.Hostname,
		ExtraHosts:     w.ExtraHosts,
		DNS:            w.DNS,
		PublishPorts:   w.PublishPorts,
	}
	if w.Name != "" {
		// A dot keeps the name apart from every workload name a cloud
		// derives from its own resource names, none of which carry one.
		namespace.Name = w.Name + ".forwarder"
	}
	forwarder, err := sidecar.StartForwarder(ctx, sidecar.ForwarderSpec{Namespace: namespace, Forwards: forwards, Workload: w.Name}, sim.NoopSink{})
	if err != nil {
		return nil, fmt.Errorf("forwarder for %s: %w", w.Name, err)
	}
	release := func() {
		forwarder.Cancel()
		<-forwarder.Exited()
	}
	for _, extra := range w.ExtraNetworks {
		var at netip.Addr
		if extra.IPv4Address != "" {
			if at, err = netip.ParseAddr(extra.IPv4Address); err != nil {
				release()
				return nil, fmt.Errorf("join %s at %q: %w", extra.Network, extra.IPv4Address, err)
			}
		}
		if err := sim.ConnectContainerToNetworkAt(forwarder.ContainerID, extra.Network, nil, at); err != nil {
			release()
			return nil, fmt.Errorf("join %s: %w", extra.Network, err)
		}
	}
	member := w
	member.Labels = make(map[string]string, len(w.Labels)+1)
	for k, v := range w.Labels {
		member.Labels[k] = v
	}
	member.Labels[forwardedLabel] = forwarder.ContainerID
	member.Network, member.NetworkAliases, member.IPv4Address, member.MACAddress = "", nil, "", ""
	member.Hostname, member.ExtraHosts, member.DNS, member.PublishPorts, member.ExtraNetworks = "", nil, nil, nil, nil
	member.NetworkMode = "container:" + forwarder.ContainerID
	h, err := sim.StartContainerSyncContext(ctx, member, sink)
	if err != nil {
		release()
		return nil, err
	}
	return newForwardedHandle(containerHandle{h, w.CancelGracePeriod}, forwarder), nil
}

// forwardedLabel marks a workload placed with forwards; its value is the
// forwarder's container ID. A member placed beside the workload joins the
// same forwarder and carries no such label: the forwarder goes with the
// workload alone.
const forwardedLabel = "sockerless-sim-forwarded-by"

// Adopt takes over a container, and the forwarder it was placed with.
func (ContainerBackend) Adopt(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	forwarderID, err := adoptedForwarder(ctx, id)
	if err != nil {
		return nil, err
	}
	h, err := sim.AdoptContainer(id, w, sink)
	if err != nil {
		return nil, err
	}
	member := containerHandle{h, w.CancelGracePeriod}
	if forwarderID == "" {
		return member, nil
	}
	forwarder, err := sim.AdoptContainer(forwarderID, sim.ContainerConfig{}, sim.NoopSink{})
	if err != nil {
		h.Cancel()
		return nil, fmt.Errorf("adopt the forwarder of %s: %w", id, err)
	}
	return newForwardedHandle(member, forwarder), nil
}

// adoptedForwarder is the forwarder a container was placed with that still
// exists, or empty.
func adoptedForwarder(ctx context.Context, containerID string) (string, error) {
	forwarderID, err := forwarderOf(ctx, containerID)
	if err != nil || forwarderID == "" {
		return "", err
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := sim.DockerClient().ContainerInspect(inspectCtx, forwarderID, client.ContainerInspectOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("inspect the forwarder of %s: %w", containerID, err)
	}
	return forwarderID, nil
}

// forwarderOf is the forwarder a container was placed with, or empty.
func forwarderOf(ctx context.Context, containerID string) (string, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("reading a container's network namespace"); err != nil {
		return "", err
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	inspected, err := cli.ContainerInspect(inspectCtx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect container %s: %w", containerID, err)
	}
	if inspected.Container.Config == nil {
		return "", nil
	}
	return inspected.Container.Config.Labels[forwardedLabel], nil
}

// forwardedHandle is a workload placed behind a forwarder: the workload's
// handle, whose exit takes the forwarder with it before it reports.
type forwardedHandle struct {
	containerHandle
	forwarder *sim.ContainerHandle
	exited    chan struct{}
}

func newForwardedHandle(member containerHandle, forwarder *sim.ContainerHandle) *forwardedHandle {
	h := &forwardedHandle{containerHandle: member, forwarder: forwarder, exited: make(chan struct{})}
	go func() {
		<-member.ContainerHandle.Exited()
		forwarder.Cancel()
		<-forwarder.Exited()
		close(h.exited)
	}()
	return h
}

func (h *forwardedHandle) Exited() <-chan struct{} { return h.exited }

// PublishedPort is the forwarder's: the forwarder owns the namespace the
// workload's ports are published from.
func (h *forwardedHandle) PublishedPort(ctx context.Context, port int) (int, error) {
	return h.forwarder.PublishedPort(ctx, port)
}

func (h *forwardedHandle) Wait() sim.ProcessResult {
	<-h.exited
	return h.containerHandle.Wait()
}

func (ContainerBackend) Restart(_ context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	if err := sim.StartExistingContainer(id); err != nil {
		return nil, fmt.Errorf("restart container %s: %w", id, err)
	}
	h, err := sim.AdoptContainer(id, w, sink)
	if err != nil {
		return nil, err
	}
	return containerHandle{h, w.CancelGracePeriod}, nil
}

func (ContainerBackend) Remove(_ context.Context, id string) error {
	return sim.RemoveContainer(id)
}

func (ContainerBackend) Find(_ context.Context, labels map[string]string) ([]Existing, error) {
	found, err := sim.FindExistingContainers(labels)
	if err != nil {
		return nil, err
	}
	out := make([]Existing, 0, len(found))
	for _, c := range found {
		out = append(out, Existing{ID: c.ID, Running: c.Running, Labels: c.Labels})
	}
	return out, nil
}

func (ContainerBackend) Console(ctx context.Context, id string) (string, error) {
	return ContainerConsole(ctx, id)
}

func (ContainerBackend) Capture(ctx context.Context, id, reference string) error {
	_, err := sim.CommitContainer(ctx, id, reference)
	return err
}

// ContainerConsole is everything a container wrote, which the engine keeps
// across its stops and starts.
func ContainerConsole(ctx context.Context, containerID string) (string, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("reading a workload's console"); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	logs, err := cli.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", fmt.Errorf("read the console of %s: %w", containerID, err)
	}
	defer func() { _ = logs.Close() }()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, logs); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the console of %s: %w", containerID, err)
	}
	return out.String(), nil
}

type containerHandle struct {
	*sim.ContainerHandle
	grace time.Duration
}

func (c containerHandle) ID() string              { return c.ContainerID }
func (c containerHandle) Kind() Kind              { return Container }
func (c containerHandle) Wait() sim.ProcessResult { return c.ContainerHandle.Wait() }
func (c containerHandle) Exited() <-chan struct{} { return c.ContainerHandle.Exited() }
func (c containerHandle) Cancel()                 { c.ContainerHandle.Cancel() }
func (c containerHandle) SetIngress(context.Context, [][]netrules.PacketRule) error {
	return ErrNoIngressFilter
}
func (c containerHandle) SetGateway(context.Context, string) error { return ErrNoRouteControl }
func (c containerHandle) Stop()                                    { sim.StopContainer(c.ContainerID, c.grace) }

func (c containerHandle) PublishedPort(ctx context.Context, port int) (int, error) {
	return c.ContainerHandle.PublishedPort(ctx, port)
}

// Address is the container's address on its network; a member's container
// has none of its own, and the engine names the owner whose namespace it
// joined.
func (c containerHandle) Address(ctx context.Context) (string, error) {
	return ContainerAddress(ctx, c.ContainerID)
}

// ContainerAddress is a container's IPv4 address on its network, following
// a member container to the owner whose namespace it joined.
func ContainerAddress(ctx context.Context, containerID string) (string, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("reading a container's address"); err != nil {
		return "", err
	}
	id := containerID
	for range 8 {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		inspected, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		cancel()
		if err != nil {
			return "", fmt.Errorf("inspect container %s: %w", id, err)
		}
		if settings := inspected.Container.NetworkSettings; settings != nil {
			for _, ep := range settings.Networks {
				if ep != nil && ep.IPAddress.IsValid() {
					return ep.IPAddress.String(), nil
				}
			}
		}
		if inspected.Container.HostConfig == nil || !inspected.Container.HostConfig.NetworkMode.IsContainer() {
			return "", fmt.Errorf("container %s has no address on a network", containerID)
		}
		id = inspected.Container.HostConfig.NetworkMode.ConnectedContainer()
	}
	return "", fmt.Errorf("container %s joins a chain of namespaces too long to follow", containerID)
}

func (c containerHandle) Exec(ctx context.Context, command []string) (ExecResult, error) {
	return ExecInContainer(ctx, c.ContainerID, command)
}

// ExecInContainer runs a command in a running container through the engine
// and returns what it wrote and how it exited.
func ExecInContainer(ctx context.Context, containerID string, command []string) (ExecResult, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("running a command in a container"); err != nil {
		return ExecResult{}, err
	}
	created, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec create: %w", err)
	}
	attached, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil && err != io.EOF {
		return ExecResult{}, fmt.Errorf("exec read: %w", err)
	}
	for {
		inspected, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return ExecResult{}, fmt.Errorf("exec inspect: %w", err)
		}
		if !inspected.Running {
			return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: inspected.ExitCode}, nil
		}
		select {
		case <-ctx.Done():
			return ExecResult{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
