package main

import (
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Compute Engine settings resource exists for every project in its scope, so
// the store holds only the projects that patched theirs, and a read that
// misses returns Compute Engine's defaults rather than a 404.

// registerComputeSettings serves the snapshot settings, which a project has
// globally and per region, and the instance settings it has per zone.
func registerComputeSettings(srv *sim.Server) {
	snapshots := sim.MakeStore[map[string]any](srv.DB(), "compute_snapshot_settings")
	instances := sim.MakeStore[map[string]any](srv.DB(), "compute_instance_settings")

	// A snapshot's default storage location: Compute Engine's own default is
	// nearest multi-region to the source disk.
	snapshotDefaults := func() map[string]any {
		// SnapshotSettings carries no kind: the schema declares only its two
		// location members.
		return map[string]any{
			"storageLocation": map[string]any{
				"policy": "NEAREST_MULTI_REGION",
			},
		}
	}
	instanceDefaults := func(r *http.Request) map[string]any {
		return map[string]any{
			"kind":     "compute#instanceSettings",
			"zone":     sim.PathParam(r, "zone"),
			"metadata": map[string]any{"kind": "compute#instanceSettingsMetadata"},
		}
	}

	// Build the two handlers here and mount them at literal paths below, so the
	// surface tables see the routes.
	read := func(store sim.Store[map[string]any],
		key func(*http.Request) string, defaults func(*http.Request) map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			settings, ok := store.Get(key(r))
			if !ok {
				settings = defaults(r)
			}
			sim.WriteJSON(w, http.StatusOK, settings)
		}
	}
	write := func(scope computeScopeKind, store sim.Store[map[string]any],
		key func(*http.Request) string, defaults func(*http.Request) map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			k := key(r)
			settings, ok := store.Get(k)
			if !ok {
				settings = defaults(r)
			}
			for field, value := range body {
				if field == "kind" {
					continue
				}
				settings[field] = value
			}
			store.Put(k, settings)
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(
				sim.PathParam(r, "project"), computeScopeSegment(scope, r), k, "patch"))
		}
	}

	globalSnapshotKey := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/global/snapshotSettings", sim.PathParam(r, "project"))
	}
	regionSnapshotKey := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/regions/%s/snapshotSettings",
			sim.PathParam(r, "project"), sim.PathParam(r, "region"))
	}
	instanceKey := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/zones/%s/instanceSettings",
			sim.PathParam(r, "project"), sim.PathParam(r, "zone"))
	}
	snapshotFallback := func(*http.Request) map[string]any { return snapshotDefaults() }

	const (
		globalSnapshots = "/compute/v1/projects/{project}/global/snapshotSettings"
		regionSnapshots = "/compute/v1/projects/{project}/regions/{region}/snapshotSettings"
		zoneInstances   = "/compute/v1/projects/{project}/zones/{zone}/instanceSettings"
	)
	srv.HandleFunc("GET "+globalSnapshots, read(snapshots, globalSnapshotKey, snapshotFallback))
	srv.HandleFunc("PATCH "+globalSnapshots, write(cScopeGlobal, snapshots, globalSnapshotKey, snapshotFallback))
	srv.HandleFunc("GET "+regionSnapshots, read(snapshots, regionSnapshotKey, snapshotFallback))
	srv.HandleFunc("PATCH "+regionSnapshots, write(cScopeRegion, snapshots, regionSnapshotKey, snapshotFallback))
	srv.HandleFunc("GET "+zoneInstances, read(instances, instanceKey, instanceDefaults))
	srv.HandleFunc("PATCH "+zoneInstances, write(cScopeZone, instances, instanceKey, instanceDefaults))
}
