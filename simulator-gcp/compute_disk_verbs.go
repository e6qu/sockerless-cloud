package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The verbs a Compute Engine disk carries beyond its lifecycle.
//
// A disk's resource policies, the key it is encrypted under, the snapshot taken
// from it and the asynchronous replication it takes part in are things the disk
// remembers, so each is written to the record the reads return. The snapshot a
// createSnapshot produces is a real snapshot in the collection the snapshots
// API serves: a caller that takes one and then lists them finds it.
//
// The zonal disks are a typed record and the regional ones a map, which is the
// shape each collection was already stored in. The verbs are written once
// against a small accessor over both, so the two scopes cannot drift into
// serving the same verb differently.

// The stores the disk verbs share with the collections that own them.
var (
	gcpComputeZoneDisks   sim.Store[ComputeDisk]
	gcpComputeRegionDisks sim.Store[map[string]any]
	gcpComputeSnapshots   sim.Store[map[string]any]
)

// computeDiskAccessor reads and writes one scope's disks, whatever shape that
// scope stores them in.
type computeDiskAccessor struct {
	policies    func(key string) ([]string, bool)
	setPolicies func(key string, values []string)
	setField    func(key, field string, value map[string]any)
	clearField  func(key, field string)
	hasField    func(key, field string) bool
	sizeGb      func(key string) (any, bool)
	each        func(func(key string, policies []string))
}

func zoneDiskAccessor() computeDiskAccessor {
	store := gcpComputeZoneDisks
	return computeDiskAccessor{
		policies: func(key string) ([]string, bool) {
			disk, ok := store.Get(key)
			return disk.ResourcePolicies, ok
		},
		setPolicies: func(key string, values []string) {
			store.Update(key, func(d *ComputeDisk) { d.ResourcePolicies = values })
		},
		setField: func(key, field string, value map[string]any) {
			store.Update(key, func(d *ComputeDisk) {
				switch field {
				case "diskEncryptionKey":
					d.DiskEncryptionKey = value
				case "asyncPrimaryDisk":
					d.AsyncPrimaryDisk = value
				case "resourceStatus":
					d.ResourceStatus = value
				}
			})
		},
		clearField: func(key, field string) {
			store.Update(key, func(d *ComputeDisk) {
				if field == "asyncPrimaryDisk" {
					d.AsyncPrimaryDisk = nil
				}
			})
		},
		hasField: func(key, field string) bool {
			disk, ok := store.Get(key)
			return ok && field == "asyncPrimaryDisk" && disk.AsyncPrimaryDisk != nil
		},
		sizeGb: func(key string) (any, bool) {
			disk, ok := store.Get(key)
			return disk.SizeGb, ok
		},
		each: func(visit func(string, []string)) {
			for _, disk := range store.List() {
				visit(computeTrimSelfLink(disk.SelfLink), disk.ResourcePolicies)
			}
		},
	}
}

func regionDiskAccessor() computeDiskAccessor {
	store := gcpComputeRegionDisks
	return computeDiskAccessor{
		policies: func(key string) ([]string, bool) {
			disk, ok := store.Get(key)
			if !ok {
				return nil, false
			}
			return computeMemberList(disk, "resourcePolicies"), true
		},
		setPolicies: func(key string, values []string) {
			store.Update(key, func(d *map[string]any) { computeSetMemberList(*d, "resourcePolicies", values) })
		},
		setField: func(key, field string, value map[string]any) {
			store.Update(key, func(d *map[string]any) { (*d)[field] = value })
		},
		clearField: func(key, field string) {
			store.Update(key, func(d *map[string]any) { delete(*d, field) })
		},
		hasField: func(key, field string) bool {
			disk, ok := store.Get(key)
			if !ok {
				return false
			}
			_, held := disk[field]
			return held
		},
		sizeGb: func(key string) (any, bool) {
			disk, ok := store.Get(key)
			if !ok {
				return nil, false
			}
			return disk["sizeGb"], true
		},
		each: func(visit func(string, []string)) {
			for _, disk := range store.List() {
				link, _ := disk["selfLink"].(string)
				visit(computeTrimSelfLink(link), computeMemberList(disk, "resourcePolicies"))
			}
		},
	}
}

func computeTrimSelfLink(link string) string {
	prefix := computeSelfLink("")
	if len(link) > len(prefix) && link[:len(prefix)] == prefix {
		return link[len(prefix):]
	}
	return link
}

func registerComputeDiskVerbs(srv *sim.Server) {
	// Called once per scope with the path written out, rather than composed in
	// a loop: the generated surface tables read the literal route out of each
	// registration.
	registerComputeDiskVerbsAt(srv, "/compute/v1/projects/{project}/zones/{zone}/disks",
		cScopeZone, zoneDiskAccessor)
	registerComputeDiskVerbsAt(srv, "/compute/v1/projects/{project}/regions/{region}/disks",
		cScopeRegion, regionDiskAccessor)
}

func registerComputeDiskVerbsAt(
	srv *sim.Server,
	base string,
	scope computeScopeKind,
	disks func() computeDiskAccessor,
) {
	{
		load := func(w http.ResponseWriter, r *http.Request) (string, computeDiskAccessor, bool) {
			key := computeScopedKey(r, scope, "disks", sim.PathParam(r, "name"))
			accessor := disks()
			if _, ok := accessor.policies(key); !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"disk %q not found", sim.PathParam(r, "name"))
				return "", accessor, false
			}
			return key, accessor, true
		}
		operation := func(r *http.Request, key, verb string) map[string]any {
			return newComputeOpWithType(sim.PathParam(r, "project"),
				computeScopeSegment(scope, r), computeSelfLink(key), verb)
		}

		policyVerb := func(verb string, add bool) {
			srv.HandleFunc("POST "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
				key, disks, ok := load(w, r)
				if !ok {
					return
				}
				var req struct {
					ResourcePolicies []string `json:"resourcePolicies"`
				}
				if err := sim.ReadJSON(r, &req); err != nil {
					GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
					return
				}
				held, _ := disks.policies(key)
				for _, policy := range req.ResourcePolicies {
					at := computeIndexOfString(held, policy)
					if add {
						if at >= 0 {
							GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
								"resource policy %s is already attached", policy)
							return
						}
						held = append(held, policy)
						continue
					}
					if at < 0 {
						GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
							"resource policy %s is not attached to this disk", policy)
						return
					}
					held = append(held[:at], held[at+1:]...)
				}
				disks.setPolicies(key, held)
				sim.WriteJSON(w, http.StatusOK, operation(r, key, verb))
			})
		}
		policyVerb("addResourcePolicies", true)
		policyVerb("removeResourcePolicies", false)

		srv.HandleFunc("POST "+base+"/{name}/updateKmsKey", func(w http.ResponseWriter, r *http.Request) {
			key, disks, ok := load(w, r)
			if !ok {
				return
			}
			var req struct {
				DiskEncryptionKey map[string]any `json:"diskEncryptionKey"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if req.DiskEncryptionKey == nil {
				GCPError(w, http.StatusBadRequest,
					"diskEncryptionKey is required to rekey a disk", "INVALID_ARGUMENT")
				return
			}
			disks.setField(key, "diskEncryptionKey", req.DiskEncryptionKey)
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "updateKmsKey"))
		})

		srv.HandleFunc("POST "+base+"/{name}/startAsyncReplication", func(w http.ResponseWriter, r *http.Request) {
			key, disks, ok := load(w, r)
			if !ok {
				return
			}
			var req struct {
				AsyncSecondaryDisk string `json:"asyncSecondaryDisk"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if req.AsyncSecondaryDisk == "" {
				GCPError(w, http.StatusBadRequest,
					"asyncSecondaryDisk names the disk to replicate to", "INVALID_ARGUMENT")
				return
			}
			disks.setField(key, "asyncPrimaryDisk", map[string]any{"disk": req.AsyncSecondaryDisk})
			disks.setField(key, "resourceStatus", map[string]any{
				"asyncPrimaryDisk": map[string]any{"state": "ACTIVE"},
			})
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "startAsyncReplication"))
		})

		srv.HandleFunc("POST "+base+"/{name}/stopAsyncReplication", func(w http.ResponseWriter, r *http.Request) {
			key, disks, ok := load(w, r)
			if !ok {
				return
			}
			if !disks.hasField(key, "asyncPrimaryDisk") {
				GCPError(w, http.StatusBadRequest, "this disk is not replicating", "INVALID_ARGUMENT")
				return
			}
			disks.clearField(key, "asyncPrimaryDisk")
			disks.setField(key, "resourceStatus", map[string]any{
				"asyncPrimaryDisk": map[string]any{"state": "STOPPED"},
			})
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "stopAsyncReplication"))
		})

		srv.HandleFunc("POST "+base+"/{name}/createSnapshot", func(w http.ResponseWriter, r *http.Request) {
			key, disks, ok := load(w, r)
			if !ok {
				return
			}
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			name, _ := body["name"].(string)
			if name == "" {
				GCPError(w, http.StatusBadRequest,
					"name is required to take a snapshot", "INVALID_ARGUMENT")
				return
			}
			snapshotKey := "projects/" + sim.PathParam(r, "project") + "/global/snapshots/" + name
			if _, exists := gcpComputeSnapshots.Get(snapshotKey); exists {
				GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "snapshot %q already exists", name)
				return
			}
			body["kind"] = "compute#snapshot"
			body["id"] = computeNumericID()
			body["selfLink"] = computeSelfLink(snapshotKey)
			body["status"] = "READY"
			body["sourceDisk"] = computeSelfLink(key)
			if size, held := disks.sizeGb(key); held {
				body["diskSizeGb"] = size
			}
			gcpComputeSnapshots.Put(snapshotKey, body)
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "createSnapshot"))
		})

		// The group-wide stop addresses a consistency group rather than one
		// disk, so every disk the named policy holds stops together — which is
		// the whole point of the group.
		srv.HandleFunc("POST "+base+"/stopGroupAsyncReplication", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ResourcePolicy string `json:"resourcePolicy"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if req.ResourcePolicy == "" {
				GCPError(w, http.StatusBadRequest,
					"resourcePolicy names the consistency group to stop", "INVALID_ARGUMENT")
				return
			}
			accessor := disks()
			accessor.each(func(key string, policies []string) {
				if !computeStringInSlice(policies, req.ResourcePolicy) {
					return
				}
				accessor.clearField(key, "asyncPrimaryDisk")
				accessor.setField(key, "resourceStatus", map[string]any{
					"asyncPrimaryDisk": map[string]any{"state": "STOPPED"},
				})
			})
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(sim.PathParam(r, "project"),
				computeScopeSegment(scope, r), computeSelfLink("disks"), "stopGroupAsyncReplication"))
		})
	}
}
