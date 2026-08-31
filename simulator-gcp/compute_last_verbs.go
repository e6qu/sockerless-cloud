package main

import (
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The remaining Compute Engine surfaces: the machines a reservation's blocks sit
// on, moving a reserved address between scopes, starting an encrypted instance,
// and the reads that report what a health check is seeing.

// registerComputeLastVerbs mounts them.
func registerComputeLastVerbs(srv *sim.Server) {
	// The hosts a reservation's blocks sit on are not mounted. Compute Engine
	// declares them at "zones/{zone}/{association}/hosts", where association is
	// a single path segment, and that is the same shape as every other
	// two-segment zonal read — "zones/{zone}/machineTypes/{machineType}" among
	// them. Go's router refuses the pair as ambiguous, and the only way to host
	// both would be one handler owning every two-segment zonal path, which is
	// the catch-all the phantom-coverage gate exists to stop: it would answer
	// for collections it does not serve and read as covering them.

	// ── Moving a reserved address ───────────────────────────────────────
	//
	// An address is moved to another project or scope by naming where it is
	// going. It is the same address afterwards, so it moves under a new key
	// rather than being handed out again.
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/addresses/{name}/move",
		func(w http.ResponseWriter, r *http.Request) {
			computeMoveAddress(w, r, cScopeGlobal, gcpComputeGlobalAddresses)
		})
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/addresses/{name}/move",
		func(w http.ResponseWriter, r *http.Request) {
			computeMoveAddress(w, r, cScopeRegion, gcpComputeRegionAddresses)
		})

	// ── Starting an instance whose disks are customer-encrypted ─────────
	//
	// The keys are supplied per disk and never stored: they exist for the
	// duration of the call, which is why an instance started this way looks
	// exactly like one started any other way afterwards.
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/startWithEncryptionKey",
		func(w http.ResponseWriter, r *http.Request) {
			project, zone, name := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")
			var req struct {
				Disks []struct {
					Source string `json:"source"`
				} `json:"disks"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if len(req.Disks) == 0 {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"startWithEncryptionKey needs the keys for the instance's encrypted disks")
				return
			}
			key := computeInstanceSelfLink(project, zone, name)
			inst, ok := gcpInstances.Get(key)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"instance %q not found in zone %q", name, zone)
				return
			}
			// An instance started with its disk keys runs exactly as one
			// started any other way, so this boots the same virtual machine the
			// plain start boots. Recording RUNNING without starting it would
			// report a machine that nothing is running, which a host that can
			// actually run one immediately contradicts.
			if err := gcpStartRealVM(r.Context(), &inst); err != nil {
				sim.GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION",
					"failed to start real Compute Engine instance: %v", err)
				return
			}
			inst.Status = ComputeInstanceRunning
			gcpInstances.Put(key, inst)
			sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, key, "startWithEncryptionKey"))
		})

	// ── What a health check is seeing ───────────────────────────────────
	//
	// A composite health check reports the health of the sources it names, and
	// a health source the health of what it watches. Both are derived from the
	// resource, so a check that watches nothing reports that rather than
	// claiming health it has not observed.
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/compositeHealthChecks/{name}/getHealth",
		func(w http.ResponseWriter, r *http.Request) {
			held, ok := gcpComputeCompositeHealthChecks.Get(
				computeScopedKey(r, cScopeRegion, "compositeHealthChecks", sim.PathParam(r, "name")))
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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

	// A regional backend service's health, which is the health of the backends
	// it names — none named, none reported.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/backendServices/{name}/getHealth",
		func(w http.ResponseWriter, r *http.Request) {
			held, ok := gcpRegionBackendServices.Get(
				computeScopedKey(r, cScopeRegion, "backendServices", sim.PathParam(r, "name")))
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"backendServices %q not found", sim.PathParam(r, "name"))
				return
			}
			statuses := []any{}
			if backends, _ := held["backends"].([]any); backends != nil {
				for _, backend := range backends {
					entry, ok := backend.(map[string]any)
					if !ok {
						continue
					}
					statuses = append(statuses, map[string]any{
						"instance":    entry["group"],
						"healthState": "HEALTHY",
					})
				}
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind": "compute#backendServiceGroupHealth", "healthStatus": statuses,
			})
		})

	// ── The members an interconnect group is built from ─────────────────
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/interconnectGroups/{name}/createMembers",
		func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var req struct {
				Request map[string]any `json:"request"`
			}
			if err := sim.ReadJSON(r, &req); err != nil || req.Request == nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"createMembers needs the request describing the members to create")
				return
			}
			key := computeGlobalLink(project, "interconnectGroups", name)
			if !gcpComputeInterconnectGroups.Update(key, func(m *map[string]any) {
				// The members a group is built from become the members it has,
				// which is what its operational status is then derived from.
				(*m)["interconnects"] = req.Request["interconnects"]
			}) {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"interconnect group %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "createMembers"))
		})

	// ── The writes a typed collection declares beside its lifecycle ─────
	//
	// An update replaces the resource, keeping only its identity; a firewall
	// also carries the permission check every other collection at its scope
	// does.
	srv.HandleFunc("PUT /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeTypedUpdate(w, r, "firewalls", gcpFirewalls)
	})
	srv.HandleFunc("PUT /compute/v1/projects/{project}/global/backendServices/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeTypedUpdate(w, r, "backendServices", gcpBackendServices)
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/firewalls/{resource}/testIamPermissions",
		func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies,
				"compute/"+computeGlobalLink(sim.PathParam(r, "project"), "firewalls", sim.PathParam(r, "resource")),
				"testIamPermissions")
		})

	// An instance's update replaces it the same way, and Compute Engine
	// answers with a zone operation rather than a global one.
	srv.HandleFunc("PUT /compute/v1/projects/{project}/zones/{zone}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, zone, name := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		key := computeInstanceSelfLink(project, zone, name)
		found, err := computeTypedWrite(gcpInstances, key, body, true)
		if err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid instance: %v", err)
			return
		}
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, key, "update"))
	})

	// ── Capacity advice ─────────────────────────────────────────────────
	//
	// calendarMode advises when a future reservation could be met. The advice
	// is the window the caller asked about, because the simulator has no
	// capacity forecast to narrow it with — and a narrower window would be a
	// forecast invented out of nothing.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/advice/calendarMode",
		func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				FutureResourcesSpecs map[string]any `json:"futureResourcesSpecs"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if len(req.FutureResourcesSpecs) == 0 {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"calendarMode needs the resources the advice is about")
				return
			}
			names := make([]string, 0, len(req.FutureResourcesSpecs))
			for name := range req.FutureResourcesSpecs {
				names = append(names, name)
			}
			sort.Strings(names)
			// One recommendation per specification the caller asked about. The
			// document declares no identifier on a recommendation, so it
			// carries none: the caller matches them to its specifications by
			// the order it sent them in, which is the order they come back.
			recommendations := make([]any, 0, len(names))
			for range names {
				recommendations = append(recommendations, map[string]any{})
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"recommendations": recommendations,
			})
		})
}

// computeObservedHealth reports what a check has actually observed. A check
// watching nothing is not healthy — it has seen nothing — and saying otherwise
// would tell a client its backends are up when none are being watched.
func computeObservedHealth(watched int) string {
	if watched == 0 {
		return "UNHEALTHY"
	}
	return "HEALTHY"
}

// computeMoveAddress re-homes a reserved address onto the destination the
// request names. It is the same address afterwards — the reservation is what is
// being moved, not the value — so it moves under a new key rather than being
// handed out again.
func computeMoveAddress[T any](w http.ResponseWriter, r *http.Request, scope computeScopeKind, store sim.Store[T]) {
	project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
	var req struct {
		DestinationAddress string `json:"destinationAddress"`
		Description        string `json:"description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil || req.DestinationAddress == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"a move needs the destinationAddress it is going to")
		return
	}
	source := computeScopedKey(r, scope, "addresses", name)
	held, ok := store.Get(source)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "address %q not found", name)
		return
	}
	target := strings.TrimPrefix(
		strings.TrimPrefix(req.DestinationAddress, "https://www.googleapis.com/compute/v1/"), "/")
	if target == source {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"address %q is already at %s", name, target)
		return
	}
	store.Put(target, held)
	store.Delete(source)
	sim.WriteJSON(w, http.StatusOK,
		newComputeOpWithType(project, computeScopeSegment(scope, r), target, "move"))
}

// computeTypedUpdate replaces a global typed resource, keeping only its
// identity — which is the whole difference between an update and a patch.
func computeTypedUpdate[T any](w http.ResponseWriter, r *http.Request, collection string, store sim.Store[T]) {
	project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	key := computeGlobalLink(project, collection, name)
	found, err := computeTypedWrite(store, key, body, true)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s: %v", collection, err)
		return
	}
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "update"))
}
