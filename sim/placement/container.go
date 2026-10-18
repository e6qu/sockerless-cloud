package placement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
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
// namespace.
func (ContainerBackend) PlaceMember(ctx context.Context, owner string, w Workload, sink sim.LogSink) (Handle, error) {
	if err := memberShape(w); err != nil {
		return nil, err
	}
	w.NetworkMode = "container:" + owner
	h, err := sim.StartContainerSyncContext(ctx, w, sink)
	if err != nil {
		return nil, err
	}
	return containerHandle{h, w.CancelGracePeriod}, nil
}

func (ContainerBackend) Adopt(_ context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	h, err := sim.AdoptContainer(id, w, sink)
	if err != nil {
		return nil, err
	}
	return containerHandle{h, w.CancelGracePeriod}, nil
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
func (c containerHandle) Stop()                   { sim.StopContainer(c.ContainerID, c.grace) }

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
