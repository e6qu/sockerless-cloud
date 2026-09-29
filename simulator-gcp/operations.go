package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// gcpOperationFilterDoc is the document a ListOperations filter reads. A proto
// bool the service never set is false, not absent, so `done = false` selects
// an operation still running however its record was written.
func gcpOperationFilterDoc(op Operation) listq.Doc {
	doc := gcpResourceToMap(op)
	doc["done"] = op.Done
	return doc
}

func registerOperations(srv *sim.Server) {
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	// The AIP-151 cancel spellings that sit outside the projects/locations
	// operation collection.
	registerOperationsCancel(srv)

	// AIP-151 ListOperations over crOperations, the store every service
	// records its operations in. `name` scopes to a collection, `filter` is
	// AIP-160 over the google.longrunning.Operation message, and
	// pageSize/pageToken page the result in name order.
	srv.HandleFunc("GET /v1/operations", func(w http.ResponseWriter, r *http.Request) {
		filter, err := gcpParseFilterExpr(r.URL.Query().Get("filter"))
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}
		namePrefix := r.URL.Query().Get("name")
		all := crOperations.List()
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		out := make([]Operation, 0, len(all))
		for _, op := range all {
			if namePrefix != "" && !strings.HasPrefix(op.Name, namePrefix) {
				continue
			}
			if filter.Eval(gcpOperationFilterDoc(op)) {
				out = append(out, op)
			}
		}
		page, next, ok := paginateList(w, r, out)
		if !ok {
			return
		}
		resp := map[string]any{"operations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// operations.get takes the whole remaining path, not one segment: the
	// documents declare it as `v2/{+name}` over names matching `^operations/.*$`,
	// and a real operation name carries its resource's own path inside it —
	// "operations/projects/{project}/operations/{id}" for Cloud Bigtable admin.
	// Matching one segment answers the flat names and 404s the rest, which is
	// what a client polling a create hits.
	srv.HandleFunc("GET /v2/operations/{operation...}", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("operations/%s", sim.PathParam(r, "operation"))
		if op, ok := crOperations.Get(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
	})

	// Get operation - v1 prefix
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opID := sim.PathParam(r, "operation")
		name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)

		if op, ok := gcpLookupOperation(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		// Real GCP returns NOT_FOUND when an operation doesn't exist; a
		// synthetic done=true response would mask a client-side bug.
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
	})

	// Get operation - v2 prefix. Cloud Logging's scope-parented operations
	// share this URI with the services that record into crOperations, and
	// CancelOperation already resolves a name across both stores, so a read
	// that searched only one would refuse a name its own cancel accepts.
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opID := sim.PathParam(r, "operation")
		name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)

		if op, ok := gcpLookupOperation(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
	})
}
