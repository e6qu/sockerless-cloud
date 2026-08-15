package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

type ServiceUsageState struct {
	Name   string `json:"name"`
	Config struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"config"`
	State  string `json:"state"`
	Parent string `json:"parent"`
}

func registerServiceUsage(srv *sim.Server) {
	services := sim.MakeStore[ServiceUsageState](srv.DB(), "service_usage")

	// Enable/disable service. Service Usage names its long-running operations
	// in the top-level `operations/{id}` collection — the collection its own
	// operations.get, operations.delete and operations.cancel address — so the
	// shared LRO record is renamed into it rather than left under the
	// projects/locations shape most other services use.
	srv.HandleFunc("POST /v1/projects/{project}/services/{serviceAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		serviceAction := sim.PathParam(r, "serviceAction")
		service, action, _ := strings.Cut(serviceAction, ":")

		name := fmt.Sprintf("projects/%s/services/%s", project, service)

		switch action {
		case "enable":
			svc := ServiceUsageState{
				Name:   name,
				State:  "ENABLED",
				Parent: "projects/" + project,
			}
			svc.Config.Name = service
			svc.Config.Title = service
			services.Put(name, svc)

			op := renameGCPOperation(
				newLRO(project, "global", svc, "type.googleapis.com/google.api.serviceusage.v1.EnableServiceResponse"),
				"operations")
			sim.WriteJSON(w, http.StatusOK, op)
		case "disable":
			services.Update(name, func(s *ServiceUsageState) {
				s.State = "DISABLED"
			})

			op := renameGCPOperation(
				newLRO(project, "global", nil, "type.googleapis.com/google.api.serviceusage.v1.DisableServiceResponse"),
				"operations")
			sim.WriteJSON(w, http.StatusOK, op)
		default:
			http.NotFound(w, r)
		}
	})

	// Get service
	srv.HandleFunc("GET /v1/projects/{project}/services/{service}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		service := sim.PathParam(r, "service")
		name := fmt.Sprintf("projects/%s/services/%s", project, service)

		svc, ok := services.Get(name)
		if !ok {
			// Return as ENABLED by default (optimistic for simulator)
			svc = ServiceUsageState{
				Name:   name,
				State:  "ENABLED",
				Parent: "projects/" + project,
			}
			svc.Config.Name = service
			svc.Config.Title = service
		}

		sim.WriteJSON(w, http.StatusOK, svc)
	})

	// List services (batch enable support)
	srv.HandleFunc("GET /v1/projects/{project}/services", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/services/", project)

		svcs := services.Filter(func(s ServiceUsageState) bool {
			return len(s.Name) > len(prefix) && s.Name[:len(prefix)] == prefix
		})
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
		// Honor the `filter` (e.g. state:ENABLED) and `orderBy` query params.
		svcs = gcpApplyListParams(svcs, r)

		page, next, ok := paginateList(w, r, svcs)
		if !ok {
			return
		}
		if page == nil {
			page = []ServiceUsageState{}
		}
		resp := map[string]any{"services": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Delete a long-running operation (serviceusage.operations.delete).
	// Per AIP / google.longrunning, this drops the client's interest in
	// the operation result and returns google.protobuf.Empty ({}). It does
	// not cancel the operation. The shared crOperations store backs every
	// service's LROs, so the deletion is honored across the sim.
	srv.HandleFunc("DELETE /v1/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("operations/%s", sim.PathParam(r, "operation"))
		if crOperations != nil {
			crOperations.Delete(name)
		}
		sim.WriteJSON(w, http.StatusOK, struct{}{})
	})

	// Batch enable services
	srv.HandleFunc("POST /v1/projects/{project}/services:batchEnable", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")

		var req struct {
			ServiceIds []string `json:"serviceIds"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		for _, serviceId := range req.ServiceIds {
			name := fmt.Sprintf("projects/%s/services/%s", project, serviceId)
			svc := ServiceUsageState{
				Name:   name,
				State:  "ENABLED",
				Parent: "projects/" + project,
			}
			svc.Config.Name = serviceId
			svc.Config.Title = serviceId
			services.Put(name, svc)
		}

		op := renameGCPOperation(
			newLRO(project, "global", nil, "type.googleapis.com/google.api.serviceusage.v1.BatchEnableServicesResponse"),
			"operations")
		sim.WriteJSON(w, http.StatusOK, op)
	})
}
