package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

func registerOperations(srv *sim.Server) {
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	// The AIP-151 cancel spellings that sit outside the projects/locations
	// operation collection.
	registerOperationsCancel(srv)

	// AIP-151 list operations across the sim. crOperations is the
	// shared store every service writes its LROs into via newLRO,
	// so a List over it projects every cross-service operation
	// (Cloud Run, Memorystore, API Gateway, Cloud SQL LRO, Cloud
	// Functions deploy, etc.). Real GCP supports `filter` and
	// `pageToken` query parameters; the sim supports a basic
	// `filter=done:true|false` and a `name` prefix match — enough
	// for the audit/monitoring tools the AIP-151 endpoint targets.
	srv.HandleFunc("GET /v1/operations", func(w http.ResponseWriter, r *http.Request) {
		all := crOperations.List()
		filter := r.URL.Query().Get("filter")
		namePrefix := r.URL.Query().Get("name")
		out := make([]Operation, 0, len(all))
		for _, op := range all {
			if namePrefix != "" && !strings.HasPrefix(op.Name, namePrefix) {
				continue
			}
			if filter == "done:true" && !op.Done {
				continue
			}
			if filter == "done:false" && op.Done {
				continue
			}
			out = append(out, op)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"operations": out})
	})

	srv.HandleFunc("GET /v2/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("operations/%s", sim.PathParam(r, "operation"))
		if op, ok := crOperations.Get(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
	})

	// Get operation - v1 prefix
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opID := sim.PathParam(r, "operation")
		name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)

		if op, ok := crOperations.Get(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		// Real GCP returns NOT_FOUND when an operation doesn't exist; the
		// previous synthetic done=true response masked client-side bugs.
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
	})

	// Get operation - v2 prefix
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opID := sim.PathParam(r, "operation")
		name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)

		if op, ok := crOperations.Get(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
	})
}
