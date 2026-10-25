package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerComputeInstanceTemplates mounts global instance templates.
func registerComputeInstanceTemplates(srv *sim.Server, instanceTemplates sim.Store[ComputeInstanceTemplate]) {
	// Instance templates — global, no networking side-effects.
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/instanceTemplates", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var req ComputeInstanceTemplate
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, req.Name) {
			return
		}
		selfLink := fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/global/instanceTemplates/%s", project, req.Name)
		req.Kind = "compute#instanceTemplate"
		req.Id = computeNumericID()
		req.SelfLink = selfLink
		req.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if req.Properties == nil {
			req.Properties = map[string]any{}
		}
		storeKey := fmt.Sprintf("projects/%s/global/instanceTemplates/%s", project, req.Name)
		instanceTemplates.Put(storeKey, req)
		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/instanceTemplates/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		storeKey := fmt.Sprintf("projects/%s/global/instanceTemplates/%s", project, name)
		tmpl, ok := instanceTemplates.Get(storeKey)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instanceTemplate %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, tmpl)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/instanceTemplates", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/instanceTemplates/", project)
		all := instanceTemplates.Filter(func(t ComputeInstanceTemplate) bool {
			return strings.HasPrefix(t.SelfLink, "https://www.googleapis.com/compute/v1/"+prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		listed, listOK := gcpApplyComputeListParams(w, r, all)
		if !listOK {
			return
		}
		all = listed
		page, next, ok := paginateListCompute(w, r, all, gcpAsc(func(v ComputeInstanceTemplate) string { return v.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#instanceTemplateList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/instanceTemplates/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		storeKey := fmt.Sprintf("projects/%s/global/instanceTemplates/%s", project, name)
		selfLink := fmt.Sprintf("https://www.googleapis.com/compute/v1/%s", storeKey)
		instanceTemplates.Delete(storeKey)
		op := newComputeOpWithType(project, "global", selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Aggregated list — gcloud instance-templates list calls this endpoint.
	// Real GCP returns items keyed by scope ("global" for global resources).
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/instanceTemplates", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/instanceTemplates/", project)
		all := instanceTemplates.Filter(func(t ComputeInstanceTemplate) bool {
			return strings.HasPrefix(t.SelfLink, "https://www.googleapis.com/compute/v1/"+prefix)
		})
		if all == nil {
			all = []ComputeInstanceTemplate{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#instanceTemplateAggregatedList",
			"items": map[string]any{
				"global": map[string]any{"instanceTemplates": all},
			},
		})
	})
}
