package main

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The remaining Compute Engine surfaces: moving a reserved address, the health
// a check observes, interconnect group members, typed updates and capacity
// advice.

func registerComputeLastVerbs(srv *sim.Server) {
	// Compute Engine declares a reservation's hosts at
	// "zones/{zone}/{association}/hosts", which ServeMux refuses as ambiguous with
	// every two-segment zonal read; hosting both would take a catch-all the
	// phantom-coverage gate forbids, so the hosts stay unmounted.

	// A moved address is the same reservation, so it moves under a new key rather
	// than being handed out again.
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/addresses/{name}/move",
		func(w http.ResponseWriter, r *http.Request) {
			computeMoveAddress(w, r, cScopeGlobal, gcpComputeGlobalAddresses)
		})
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/addresses/{name}/move",
		func(w http.ResponseWriter, r *http.Request) {
			computeMoveAddress(w, r, cScopeRegion, gcpComputeRegionAddresses)
		})

	// A composite health check reports the health of the sources it names, and a
	// health source that of what it watches; a check watching nothing claims no
	// health.
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/compositeHealthChecks/{name}/getHealth",
		func(w http.ResponseWriter, r *http.Request) {
			held, ok := gcpComputeCompositeHealthChecks.Get(
				computeScopedKey(r, cScopeRegion, "compositeHealthChecks", sim.PathParam(r, "name")))
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"composite health check %q not found", sim.PathParam(r, "name"))
				return
			}
			sources := []any{}
			if named, _ := held["healthSources"].([]any); named != nil {
				sources = named
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind":          "compute#compositeHealthCheckHealth",
				"healthSources": sources,
				"healthState":   computeObservedHealth(len(sources)),
			})
		})
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/healthSources/{name}/getHealth",
		func(w http.ResponseWriter, r *http.Request) {
			held, ok := gcpComputeHealthSources.Get(
				computeScopedKey(r, cScopeRegion, "healthSources", sim.PathParam(r, "name")))
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"health source %q not found", sim.PathParam(r, "name"))
				return
			}
			sources := []any{}
			if named, _ := held["sources"].([]any); named != nil {
				sources = named
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind":        "compute#healthSourceHealth",
				"sources":     sources,
				"healthState": computeObservedHealth(len(sources)),
			})
		})

	// A regional backend service's health: what its health checks last found
	// for the backends of the named group, as the health checker recorded it —
	// the same rule the global backend services report by.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/backendServices/{name}/getHealth",
		func(w http.ResponseWriter, r *http.Request) {
			held, ok := gcpRegionBackendServices.Get(
				computeScopedKey(r, cScopeRegion, "backendServices", sim.PathParam(r, "name")))
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"backendServices %q not found", sim.PathParam(r, "name"))
				return
			}
			var req struct {
				Group string `json:"group"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			bs, ok := gcpDecodeComputeResource[ComputeBackendService](held)
			if !ok {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
					"backend service %q is not a readable backendService", sim.PathParam(r, "name"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind":         "compute#backendServiceGroupHealth",
				"healthStatus": gcpBackendServiceHealth(bs, req.Group),
			})
		})

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/interconnectGroups/{name}/createMembers",
		func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var req struct {
				Request map[string]any `json:"request"`
			}
			if err := sim.ReadJSON(r, &req); err != nil || req.Request == nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"createMembers needs the request describing the members to create")
				return
			}
			key := computeGlobalLink(project, "interconnectGroups", name)
			if !gcpComputeInterconnectGroups.Update(key, func(m *map[string]any) {
				// The members become the group's interconnects, which its operational status
				// derives from.
				(*m)["interconnects"] = req.Request["interconnects"]
			}) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"interconnect group %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "createMembers"))
		})

	// An update replaces the resource, keeping its identity; a firewall update
	// also carries its scope's permission check.
	srv.HandleFunc("PUT /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeTypedUpdate(w, r, "firewalls", gcpFirewalls, gcpReapplyIngressAfter)
	})
	srv.HandleFunc("PUT /compute/v1/projects/{project}/global/backendServices/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeTypedUpdate(w, r, "backendServices", gcpBackendServices, nil)
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/firewalls/{resource}/testIamPermissions",
		func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies,
				"compute/"+computeGlobalLink(sim.PathParam(r, "project"), "firewalls", sim.PathParam(r, "resource")),
				"testIamPermissions")
		})

	// An instance update answers a zone operation rather than a global one.
	srv.HandleFunc("PUT /compute/v1/projects/{project}/zones/{zone}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, zone, name := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		key := computeInstanceSelfLink(project, zone, name)
		found, err := computeTypedWrite(gcpInstances, key, body, true)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid instance: %v", err)
			return
		}
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, key, "update"))
	})

	// calendarMode answers the window the caller asked about: the simulator has no
	// capacity forecast to narrow it with.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/advice/calendarMode",
		func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				FutureResourcesSpecs map[string]any `json:"futureResourcesSpecs"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if len(req.FutureResourcesSpecs) == 0 {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"calendarMode needs the resources the advice is about")
				return
			}
			names := make([]string, 0, len(req.FutureResourcesSpecs))
			for name := range req.FutureResourcesSpecs {
				names = append(names, name)
			}
			sort.Strings(names)
			// A recommendation carries no identifier, so they come back in the order the
			// caller sent its specifications.
			recommendations := make([]any, 0, len(names))
			for range names {
				recommendations = append(recommendations, map[string]any{})
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"recommendations": recommendations,
			})
		})
}

// computeObservedHealth reports a check watching nothing as not healthy: it has
// observed nothing.
func computeObservedHealth(watched int) string {
	if watched == 0 {
		return "UNHEALTHY"
	}
	return "HEALTHY"
}

// computeMoveAddress re-homes a reserved address onto the request's
// destination under a new key; the reservation moves, not the value.
func computeMoveAddress[T any](w http.ResponseWriter, r *http.Request, scope computeScopeKind, store sim.Store[T]) {
	project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
	var req struct {
		DestinationAddress string `json:"destinationAddress"`
		Description        string `json:"description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil || req.DestinationAddress == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"a move needs the destinationAddress it is going to")
		return
	}
	source := computeScopedKey(r, scope, "addresses", name)
	held, ok := store.Get(source)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "address %q not found", name)
		return
	}
	target := strings.TrimPrefix(
		strings.TrimPrefix(req.DestinationAddress, "https://www.googleapis.com/compute/v1/"), "/")
	if target == source {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"address %q is already at %s", name, target)
		return
	}
	store.Put(target, held)
	store.Delete(source)
	sim.WriteJSON(w, http.StatusOK,
		newComputeOpWithType(project, computeScopeSegment(scope, r), target, "move"))
}

// computeTypedUpdate replaces a global typed resource, keeping only its
// identity. realize, when set, applies the replacement before the operation is
// answered.
func computeTypedUpdate[T any](w http.ResponseWriter, r *http.Request, collection string, store sim.Store[T], realize func(context.Context) error) {
	project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	key := computeGlobalLink(project, collection, name)
	found, err := computeTypedWrite(store, key, body, true)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s: %v", collection, err)
		return
	}
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, name)
		return
	}
	if realize != nil {
		if err := realize(r.Context()); err != nil {
			GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to apply real %s: %v", collection, err)
			return
		}
	}
	sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "update"))
}
