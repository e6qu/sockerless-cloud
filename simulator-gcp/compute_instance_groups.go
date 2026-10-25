package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerComputeInstanceGroups(srv *sim.Server) {
	groups := sim.MakeStore[storedComputeInstanceGroup](srv.DB(), "compute_instance_groups")
	// Shared so the instance verbs can report which groups refer to an
	// instance, as gcpFirewalls is shared for the same reason.
	gcpComputeInstanceGroups = groups
	gcpInstanceGroups = groups

	instanceGroupSelfLink := func(project, zone, name string) string {
		return fmt.Sprintf("projects/%s/zones/%s/instanceGroups/%s", project, zone, name)
	}

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		var group ComputeInstanceGroup
		if err := sim.ReadJSON(r, &group); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, group.Name) {
			return
		}
		group.Kind = "compute#instanceGroup"
		group.Id = computeNumericID()
		group.SelfLink = instanceGroupSelfLink(project, zone, group.Name)
		group.Zone = fmt.Sprintf("projects/%s/zones/%s", project, zone)
		group.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		group.Fingerprint = computeFingerprint()
		groups.Put(group.SelfLink, storedComputeInstanceGroup{ComputeInstanceGroup: group})
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, group.SelfLink, "insert"))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		group, ok := groups.Get(instanceGroupSelfLink(project, zone, name))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, wireInstanceGroup(group))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instanceGroups", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		prefix := fmt.Sprintf("projects/%s/zones/%s/instanceGroups/", project, zone)
		stored := groups.Filter(func(group storedComputeInstanceGroup) bool { return strings.HasPrefix(group.SelfLink, prefix) })
		items := make([]ComputeInstanceGroup, 0, len(stored))
		for _, group := range stored {
			items = append(items, wireInstanceGroup(group))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#instanceGroupList", "items": items})
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		groups.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "delete"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/addInstances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		var req struct {
			Instances []ComputeInstanceGroupInstance `json:"instances"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !groups.Update(selfLink, func(group *storedComputeInstanceGroup) {
			for _, inst := range req.Instances {
				if !gcpInstanceGroupHasInstance(*group, inst.Instance) {
					group.Instances = append(group.Instances, inst)
				}
			}
			group.Fingerprint = computeFingerprint()
		}) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "addInstances"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/removeInstances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		var req struct {
			Instances []ComputeInstanceGroupInstance `json:"instances"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		remove := map[string]bool{}
		for _, inst := range req.Instances {
			remove[inst.Instance] = true
		}
		if !groups.Update(selfLink, func(group *storedComputeInstanceGroup) {
			filtered := group.Instances[:0]
			for _, inst := range group.Instances {
				if !remove[inst.Instance] {
					filtered = append(filtered, inst)
				}
			}
			group.Instances = filtered
			group.Fingerprint = computeFingerprint()
		}) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "removeInstances"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/listInstances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		group, ok := groups.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		items := make([]map[string]any, 0, len(group.Instances))
		for _, inst := range group.Instances {
			items = append(items, map[string]any{
				"instance": inst.Instance,
				"status":   "RUNNING",
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#instanceGroupsListInstances",
			"id":    computeNumericID(),
			"items": items,
		})
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instanceGroups/{name}/setNamedPorts", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceGroupSelfLink(project, zone, name)
		var req struct {
			Fingerprint string                          `json:"fingerprint"`
			NamedPorts  []ComputeInstanceGroupNamedPort `json:"namedPorts"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		if !groups.Update(selfLink, func(group *storedComputeInstanceGroup) {
			// setNamedPorts honors the fingerprint for optimistic concurrency
			// (a stale read-modify-write must 412).
			if !fingerprintMatches(group.Fingerprint, req.Fingerprint) {
				conflict = true
				return
			}
			group.NamedPorts = req.NamedPorts
			group.Fingerprint = computeFingerprint()
		}) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "named ports fingerprint mismatch; the resource was modified concurrently")
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setNamedPorts"))
	})
}
