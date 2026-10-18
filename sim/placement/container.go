package placement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	return containerHandle{h}, nil
}

func (ContainerBackend) Adopt(_ context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	h, err := sim.AdoptContainer(id, w, sink)
	if err != nil {
		return nil, err
	}
	return containerHandle{h}, nil
}

func (ContainerBackend) Restart(_ context.Context, id string, w Workload, sink sim.LogSink) (Handle, error) {
	if err := sim.StartExistingContainer(id); err != nil {
		return nil, fmt.Errorf("restart container %s: %w", id, err)
	}
	h, err := sim.AdoptContainer(id, w, sink)
	if err != nil {
		return nil, err
	}
	return containerHandle{h}, nil
}

func (ContainerBackend) Remove(_ context.Context, id string) error {
	return sim.RemoveContainer(id)
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

type containerHandle struct{ *sim.ContainerHandle }

func (c containerHandle) ID() string              { return c.ContainerID }
func (c containerHandle) Kind() Kind              { return Container }
func (c containerHandle) Wait() sim.ProcessResult { return c.ContainerHandle.Wait() }
func (c containerHandle) Exited() <-chan struct{} { return c.ContainerHandle.Exited() }
func (c containerHandle) Cancel()                 { c.ContainerHandle.Cancel() }

func (c containerHandle) PublishedPort(ctx context.Context, port int) (int, error) {
	return c.ContainerHandle.PublishedPort(ctx, port)
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
