package main

import (
	"context"
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
	mainHandle, err := workload.Place(ctx, backend, "main", main, sink)
	if err != nil {
		return nil, err
	}
	group := &acaGroup{Main: mainHandle}
	for _, c := range sidecars {
		handle, err := workload.PlaceMember(ctx, backend, mainHandle.ID(), "sidecar", c, sink)
		if err != nil {
			group.cancel()
			return nil, err
		}
		group.Sidecars = append(group.Sidecars, handle)
	}
	return group, nil
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
