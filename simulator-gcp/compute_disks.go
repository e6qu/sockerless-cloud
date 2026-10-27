package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerComputeDisks serves the zonal `compute#disk` collection and its
// aggregated list, each write answering a zonal operation.
func registerComputeDisks(srv *sim.Server) {
	disks := sim.MakeStore[ComputeDisk](srv.DB(), "compute_disks")
	// Shared so the disk verbs write the same disks the lifecycle serves.
	gcpComputeZoneDisks = disks

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		var d ComputeDisk
		if err := sim.ReadJSON(r, &d); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, d.Name) {
			return
		}
		d.Kind = "compute#disk"
		d.Id = computeNumericID()
		d.SelfLink = fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, d.Name)
		d.Zone = fmt.Sprintf("projects/%s/zones/%s", project, zone)
		d.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		d.Status = "READY"
		if d.SizeGb == "" {
			d.SizeGb = "10"
		}
		if d.Type == "" {
			d.Type = fmt.Sprintf("projects/%s/zones/%s/diskTypes/pd-standard", project, zone)
		}
		d.LabelFingerprint = sim.NewUUID()[:8]
		disks.Put(d.SelfLink, d)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, d.SelfLink, "insert"))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/disks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		d, ok := disks.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, d)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/disks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		prefix := fmt.Sprintf("projects/%s/zones/%s/disks/", project, zone)
		items := disks.Filter(func(d ComputeDisk) bool {
			return strings.HasPrefix(d.SelfLink, prefix)
		})
		if items == nil {
			items = []ComputeDisk{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#diskList",
			"items": items,
		})
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/disks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		if _, ok := disks.Get(selfLink); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		disks.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "delete"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/resize", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		var req struct {
			SizeGb gcpInt64 `json:"sizeGb"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := disks.Update(selfLink, func(d *ComputeDisk) {
			if req.SizeGb != "" {
				d.SizeGb = req.SizeGb
			}
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "resize"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/disks/{name}/setLabels", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		var req struct {
			Labels           map[string]string `json:"labels"`
			LabelFingerprint string            `json:"labelFingerprint"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		ok := disks.Update(selfLink, func(d *ComputeDisk) {
			if !fingerprintMatches(d.LabelFingerprint, req.LabelFingerprint) {
				conflict = true
				return
			}
			d.Labels = req.Labels
			d.LabelFingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found in zone %q", name, zone)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "labelFingerprint mismatch; the resource was modified concurrently")
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setLabels"))
	})

	// The aggregated list groups disks by zone key.
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/disks", func(w http.ResponseWriter, r *http.Request) {
		computeWriteZonalAggregate(w, disks, sim.PathParam(r, "project"), "compute#diskAggregatedList", "disks", func(v ComputeDisk) string { return v.SelfLink })
	})

	// An instance insert leaves its operation RUNNING while the machine boots, so
	// the GET answers the operation's state at once and the wait blocks for it, as
	// Compute Engine's does.
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/operations/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteOperation(w, sim.PathParam(r, "name"))
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/operations/{name}/wait", func(w http.ResponseWriter, r *http.Request) {
		computeWaitOperation(w, r, sim.PathParam(r, "name"))
	})
}
