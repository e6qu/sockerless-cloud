package workload

import (
	"context"
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Container is one member of a container group: the name the cloud's resource
// gives it, for errors, and the configuration to start it with. StartGroup
// and StartSidecars read Config.Architecture off the image, pulling with
// Config.RegistryAuth, so a member never names its own platform.
type Container struct {
	Name   string
	Config sim.ContainerConfig
}

// Group is a running main container and the sidecars that share its network
// namespace — a Cloud Run task, a Container Apps replica or job execution.
type Group struct {
	Main     *sim.ContainerHandle
	Sidecars []*sim.ContainerHandle
}

// StartGroup starts main and then each sidecar in main's network namespace,
// so a sidecar listening on localhost:<port> is reachable from main on the
// same address. When any member fails to start, the members already started
// are cancelled and the error names the member.
func StartGroup(ctx context.Context, main Container, sidecars []Container, sink sim.LogSink) (*Group, error) {
	handle, err := start(ctx, "main", main, sink)
	if err != nil {
		return nil, err
	}
	sidecarHandles, err := StartSidecars(ctx, handle.ContainerID, sidecars, sink)
	if err != nil {
		handle.Cancel()
		return nil, err
	}
	return &Group{Main: handle, Sidecars: sidecarHandles}, nil
}

// StartSidecars starts each sidecar in the network namespace of the running
// container mainID. When one fails to start, the sidecars already started are
// cancelled; mainID stays the caller's to stop.
func StartSidecars(ctx context.Context, mainID string, sidecars []Container, sink sim.LogSink) ([]*sim.ContainerHandle, error) {
	var handles []*sim.ContainerHandle
	for _, c := range sidecars {
		if c.Config.NetworkMode != "" || c.Config.Network != "" || len(c.Config.NetworkAliases) > 0 || len(c.Config.ExtraHosts) > 0 {
			cancelAll(handles)
			return nil, fmt.Errorf("sidecar container %q names its own network; it joins the main container's", c.Name)
		}
		c.Config.NetworkMode = "container:" + mainID
		handle, err := start(ctx, "sidecar", c, sink)
		if err != nil {
			cancelAll(handles)
			return nil, err
		}
		handles = append(handles, handle)
	}
	return handles, nil
}

func start(ctx context.Context, role string, c Container, sink sim.LogSink) (*sim.ContainerHandle, error) {
	if c.Config.Architecture != "" {
		return nil, fmt.Errorf("%s container %q names its platform; the group reads it off the image", role, c.Name)
	}
	platform, err := LocalImagePlatform(ctx, c.Config.Image, c.Config.RegistryAuth)
	if err != nil {
		return nil, fmt.Errorf("resolve %s container %q image platform: %w", role, c.Name, err)
	}
	c.Config.Architecture = platform
	handle, err := sim.StartContainerSyncContext(ctx, c.Config, sink)
	if err != nil {
		return nil, fmt.Errorf("start %s container %q: %w", role, c.Name, err)
	}
	return handle, nil
}

func cancelAll(handles []*sim.ContainerHandle) {
	for _, h := range handles {
		h.Cancel()
	}
}

// Stop sends main the stop signal, giving it grace to exit before the engine
// kills it, and then cancels every member. A nil group stops nothing.
func (g *Group) Stop(grace time.Duration) {
	if g == nil {
		return
	}
	if g.Main != nil {
		sim.StopContainer(g.Main.ContainerID, grace)
		g.Main.Cancel()
	}
	for _, h := range g.Sidecars {
		if h != nil {
			h.Cancel()
		}
	}
}
