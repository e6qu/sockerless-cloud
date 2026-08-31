package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Compute Engine's bulk creates and the verbs that reshape a resource in place.
//
// A bulk insert creates a run of resources from one request, naming them from a
// pattern the caller gives, and answers with a single operation covering the
// whole run — so what a client observes afterwards is the run existing, not a
// per-resource operation each.

// computeBulkNames renders the names a bulk insert creates. Compute Engine
// substitutes a run of hashes in the pattern with a zero-padded index, so
// "worker-###" yields worker-001 upward; a pattern with no hashes is invalid,
// because every resource in the run would have the same name.
func computeBulkNames(pattern string, count int) ([]string, error) {
	start := strings.IndexByte(pattern, '#')
	if start < 0 {
		return nil, fmt.Errorf("namePattern %q has no # to number the instances with", pattern)
	}
	width := 0
	for i := start; i < len(pattern) && pattern[i] == '#'; i++ {
		width++
	}
	head, tail := pattern[:start], pattern[start+width:]
	names := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		names = append(names, fmt.Sprintf("%s%0*d%s", head, width, i, tail))
	}
	return names, nil
}

// registerComputeBulkVerbs serves the bulk creates and the in-place writes that
// sit beside them.
func registerComputeBulkVerbs(srv *sim.Server) {
	// instances.bulkInsert creates a run from one set of properties. The run's
	// members are real instances afterwards — a list finds them — which is the
	// part a caller depends on.
	bulkInstances := func(scope computeScopeKind) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			// count and minCount are int64, which Compute Engine puts on the
			// wire as strings — the generated client sends them that way.
			var req struct {
				Count              int64          `json:"count,string"`
				MinCount           int64          `json:"minCount,string"`
				NamePattern        string         `json:"namePattern"`
				InstanceProperties map[string]any `json:"instanceProperties"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if req.Count <= 0 {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"a bulk insert needs a count greater than zero")
				return
			}
			names, err := computeBulkNames(req.NamePattern, int(req.Count))
			if err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			zone := sim.PathParam(r, "zone")
			if scope == cScopeRegion {
				// A regional bulk insert places its instances in the region's
				// zones; with nothing to distribute across yet, the first zone
				// is where they land.
				zone = sim.PathParam(r, "region") + "-a"
			}
			for _, name := range names {
				key := computeInstanceSelfLink(project, zone, name)
				if _, taken := gcpInstances.Get(key); taken {
					sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
						"the bulk insert would overwrite instance %q", name)
					return
				}
			}
			for _, name := range names {
				instance := ComputeInstance{
					Kind:              "compute#instance",
					Id:                computeNumericID(),
					Name:              name,
					SelfLink:          computeInstanceSelfLink(project, zone, name),
					Zone:              fmt.Sprintf("projects/%s/zones/%s", project, zone),
					Status:            ComputeInstanceRunning,
					CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
				}
				if machineType, _ := req.InstanceProperties["machineType"].(string); machineType != "" {
					instance.MachineType = machineType
				}
				gcpInstances.Put(instance.SelfLink, instance)
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project,
				computeScopeSegment(scope, r),
				fmt.Sprintf("projects/%s/%s/instances", project, computeScopeSegment(scope, r)),
				"bulkInsert"))
		}
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/bulkInsert",
		bulkInstances(cScopeZone))
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/instances/bulkInsert",
		bulkInstances(cScopeRegion))

	// disks.bulkInsert creates a run of disks from a consistency group or a
	// snapshot group. The source names what the run is restored from, and a run
	// with no source has nothing to create.
	bulkDisks := func(scope computeScopeKind) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			var req map[string]any
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			source := ""
			for _, member := range []string{"sourceConsistencyGroupPolicy", "snapshotGroupParameters", "instantSnapshotGroupParameters"} {
				if _, sent := req[member]; sent {
					source = member
					break
				}
			}
			if source == "" {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"a bulk disk insert needs the group it restores from")
				return
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project,
				computeScopeSegment(scope, r),
				fmt.Sprintf("projects/%s/%s/disks", project, computeScopeSegment(scope, r)),
				"bulkInsert"))
		}
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks/bulkInsert",
		bulkDisks(cScopeZone))
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/disks/bulkInsert",
		bulkDisks(cScopeRegion))

	// bulkSetLabels writes one label set across the disks a filter selects, so
	// the resource it names is the collection rather than any one disk.
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks/bulkSetLabels",
		func(w http.ResponseWriter, r *http.Request) {
			project, zone := sim.PathParam(r, "project"), sim.PathParam(r, "zone")
			// The request carries a list of label-sets rather than one, so
			// they are applied in the order given — the last write of a key
			// is the one that stands, which is what a list in order means.
			var req struct {
				Requests []struct {
					Labels map[string]string `json:"labels"`
				} `json:"requests"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if len(req.Requests) == 0 {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"bulkSetLabels needs at least one set of labels to apply")
				return
			}
			// The resource query parameter names the disk the labels go on,
			// which is how Compute Engine scopes the call.
			wanted := r.URL.Query().Get("resource")
			if wanted == "" {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"bulkSetLabels needs the resource whose labels are being set")
				return
			}
			key := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, wanted)
			if !gcpComputeZoneDisks.Update(key, func(d *ComputeDisk) {
				if d.Labels == nil {
					d.Labels = map[string]string{}
				}
				for _, apply := range req.Requests {
					for label, value := range apply.Labels {
						d.Labels[label] = value
					}
				}
			}) {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found", wanted)
				return
			}
			sim.WriteJSON(w, http.StatusOK,
				newComputeOpWithType(project, "zones/"+zone, key, "bulkSetLabels"))
		})

	// A zonal disk's patch, which changes the members a disk can be edited in
	// place without being recreated.
	srv.HandleFunc("PATCH /compute/v1/projects/{project}/zones/{zone}/disks/{name}",
		func(w http.ResponseWriter, r *http.Request) {
			project, zone, name := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			key := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
			found, err := computeTypedWrite(gcpComputeZoneDisks, key, body, false)
			if err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid disk: %v", err)
				return
			}
			if !found {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK,
				newComputeOpWithType(project, "zones/"+zone, key, "patch"))
		})
}
