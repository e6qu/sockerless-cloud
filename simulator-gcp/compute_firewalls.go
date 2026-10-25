package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerComputeFirewalls mounts VPC firewall rules.
func registerComputeFirewalls(srv *sim.Server, firewalls sim.Store[ComputeFirewall]) {
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/firewalls", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var fw ComputeFirewall
		if err := sim.ReadJSON(r, &fw); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, fw.Name) {
			return
		}
		if msg := gcpInvalidFirewallPort(fw); msg != "" {
			GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
			return
		}
		fw.Kind = "compute#firewall"
		fw.Id = computeNumericID()
		fw.SelfLink = fmt.Sprintf("projects/%s/global/firewalls/%s", project, fw.Name)
		fw.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if fw.Direction == "" {
			fw.Direction = "INGRESS"
		}
		if fw.Priority == 0 {
			fw.Priority = 1000
		}
		if _, exists := firewalls.Get(fw.SelfLink); computeConflict(w, exists, "firewall", fw.Name) {
			return
		}
		firewalls.Put(fw.SelfLink, fw)
		gcpReapplyIngress()
		op := newComputeOp(project, "global", fw.SelfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/global/firewalls/%s", project, name)
		fw, ok := firewalls.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewall %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, fw)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/firewalls", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/firewalls/", project)
		computeWriteScopedList(w, r, firewalls, "compute#firewallList", prefix, func(v ComputeFirewall) string { return v.SelfLink }, func(v ComputeFirewall) string { return v.Name })
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/global/firewalls/%s", project, name)
		if computeNotFound(w, firewalls.Delete(selfLink), "firewall", name) {
			return
		}
		gcpReapplyIngress()
		// Real Compute Engine stamps a delete operation `operationType: "delete"`;
		// gcloud keys off it to report the resource as deleted instead of
		// re-fetching it (which then 404s and fails the command).
		op := newComputeOpWithType(project, "global", selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("PATCH /compute/v1/projects/{project}/global/firewalls/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/global/firewalls/%s", project, name)
		var patch ComputeFirewall
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if msg := gcpInvalidFirewallPort(patch); msg != "" {
			GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
			return
		}
		ok := firewalls.Update(selfLink, func(fw *ComputeFirewall) {
			if patch.Description != "" {
				fw.Description = patch.Description
			}
			if patch.SourceRanges != nil {
				fw.SourceRanges = patch.SourceRanges
			}
			if patch.SourceTags != nil {
				fw.SourceTags = patch.SourceTags
			}
			if patch.TargetTags != nil {
				fw.TargetTags = patch.TargetTags
			}
			if patch.Allowed != nil {
				fw.Allowed = patch.Allowed
			}
			if patch.Denied != nil {
				fw.Denied = patch.Denied
			}
			if patch.Direction != "" {
				fw.Direction = patch.Direction
			}
			if patch.Priority != 0 {
				fw.Priority = patch.Priority
			}
			fw.Disabled = patch.Disabled
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewall %q not found", name)
			return
		}
		gcpReapplyIngress()
		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})
}
