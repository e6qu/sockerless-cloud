package main

import (
	"context"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// acaStartGroup places each sidecar in main's network namespace, so a sidecar
// on localhost:<port> is reachable from main. When a member fails to start,
// the members already started are cancelled.
func acaStartGroup(ctx context.Context, main workload.Container, sidecars []workload.Container, sink sim.LogSink) (*workload.Group, error) {
	backend, err := azurePlacementBackend(placementContainerApps)
	if err != nil {
		return nil, err
	}
	mainHandle, err := workload.Place(ctx, backend, "main", main, sink)
	if err != nil {
		return nil, err
	}
	group := &workload.Group{Main: mainHandle}
	for _, c := range sidecars {
		handle, err := workload.PlaceMember(ctx, backend, mainHandle.ID(), "sidecar", c, sink)
		if err != nil {
			acaCancelGroup(group)
			return nil, err
		}
		group.Sidecars = append(group.Sidecars, handle)
	}
	return group, nil
}

func acaCancelGroup(g *workload.Group) {
	for _, h := range g.Sidecars {
		h.Cancel()
	}
	g.Main.Cancel()
}
