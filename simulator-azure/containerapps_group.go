package main

import (
	"context"
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// acaGroup is a running main container and the sidecars that share its
// network namespace — a replica or a job execution — placed on the
// containerapps backend.
type acaGroup struct {
	Main     placement.Handle
	Sidecars []placement.Handle
}

// acaStartGroup places main and then each sidecar in main's network
// namespace, so a sidecar listening on localhost:<port> is reachable from
// main on the same address. When any member fails to start, the members
// already started are cancelled and the error names the member.
func acaStartGroup(ctx context.Context, main workload.Container, sidecars []workload.Container, sink sim.LogSink) (*acaGroup, error) {
	backend, err := azurePlacementBackend(placementContainerApps)
	if err != nil {
		return nil, err
	}
	mainHandle, err := acaPlace(ctx, backend, "main", main, sink)
	if err != nil {
		return nil, err
	}
	group := &acaGroup{Main: mainHandle}
	for _, c := range sidecars {
		if c.Config.NetworkMode != "" || c.Config.Network != "" || len(c.Config.NetworkAliases) > 0 || len(c.Config.ExtraHosts) > 0 {
			group.cancel()
			return nil, fmt.Errorf("sidecar container %q names its own network; it joins the main container's", c.Name)
		}
		c.Config.NetworkMode = "container:" + mainHandle.ID()
		handle, err := acaPlace(ctx, backend, "sidecar", c, sink)
		if err != nil {
			group.cancel()
			return nil, err
		}
		group.Sidecars = append(group.Sidecars, handle)
	}
	return group, nil
}

// acaPlace places one member on the backend with the platform its image
// declares; a member naming a platform of its own is a caller's mistake.
func acaPlace(ctx context.Context, backend placement.Backend, role string, c workload.Container, sink sim.LogSink) (placement.Handle, error) {
	if c.Config.Architecture != "" {
		return nil, fmt.Errorf("%s container %q names its platform; the group reads it off the image", role, c.Name)
	}
	platform, err := workload.LocalImagePlatform(ctx, c.Config.Image, c.Config.RegistryAuth)
	if err != nil {
		return nil, fmt.Errorf("resolve %s container %q image platform: %w", role, c.Name, err)
	}
	c.Config.Architecture = platform
	handle, err := backend.Place(ctx, c.Config, sink)
	if err != nil {
		return nil, fmt.Errorf("start %s container %q: %w", role, c.Name, err)
	}
	return handle, nil
}

func (g *acaGroup) cancel() {
	for _, h := range g.Sidecars {
		h.Cancel()
	}
	g.Main.Cancel()
}

// Stop ends the group within grace: the sidecars first, then the main.
func (g *acaGroup) Stop(grace time.Duration) {
	for _, h := range g.Sidecars {
		h.Cancel()
	}
	g.Main.Cancel()
	deadline := time.After(grace + 30*time.Second)
	for _, h := range append([]placement.Handle{g.Main}, g.Sidecars...) {
		select {
		case <-h.Exited():
		case <-deadline:
			return
		}
	}
}

// acaStartSidecar places one sidecar in the main container's network
// namespace.
func acaStartSidecar(ctx context.Context, mainID string, c workload.Container, sink sim.LogSink) (placement.Handle, error) {
	backend, err := azurePlacementBackend(placementContainerApps)
	if err != nil {
		return nil, err
	}
	c.Config.NetworkMode = "container:" + mainID
	return acaPlace(ctx, backend, "sidecar", c, sink)
}

// acaStopHandles stops every handle within grace, waiting for each to end.
func acaStopHandles(handles []placement.Handle, grace time.Duration) {
	for _, handle := range handles {
		handle.Cancel()
	}
	deadline := time.After(grace + 30*time.Second)
	for _, handle := range handles {
		select {
		case <-handle.Exited():
		case <-deadline:
			return
		}
	}
}
