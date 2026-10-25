package main

import (
	"fmt"
	"net/http"
	"slices"
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

	// operations.get takes the whole remaining path, not one segment: the
	// documents declare it as `v2/{+name}` over names matching `^operations/.*$`,
	// and a real operation name carries its resource's own path inside it —
	// "operations/projects/{project}/operations/{id}" for Cloud Bigtable admin.
	// Matching one segment answers the flat names and 404s the rest, which is
	// what a client polling a create hits.
	srv.HandleFunc("GET /v2/operations/{operation...}", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("operations/%s", sim.PathParam(r, "operation"))
		if op, ok := gcpLookupOperation(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/operations", handleLocationOperationsList("v1"))
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/operations", handleLocationOperationsList("v2"))
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/operations/{operation}", handleLocationOperationDelete("v1"))
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/operations/{operation}", handleLocationOperationDelete("v2"))

	// Get operation - v1 prefix. Cloud Build's regional build operations are
	// a view of the build record rather than a stored operation, so the
	// location's builds are searched after the operation stores.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opID := sim.PathParam(r, "operation")
		name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)

		if op, ok := gcpLookupOperation(name); ok {
			sim.WriteJSON(w, http.StatusOK, op)
			return
		}
		if build, ok := cbLocationBuild(project, location, opID); ok {
			cbWriteBuildOperation(w, project, build)
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

// gcpLocationOperationPackages names, per API version, the services that
// publish projects.locations.operations.list at that version's path — keyed
// by the host Google serves each from — and the proto packages of the
// metadata messages their operations carry. The Cloud Run Admin API's two
// versions spell one collection of operation records.
var gcpLocationOperationPackages = map[string]map[string][]string{
	"v1": {
		"apigateway":     {"google.cloud.apigateway.v1"},
		"eventarc":       {"google.cloud.eventarc.v1"},
		"redis":          {"google.cloud.redis.v1", "google.cloud.redis.cluster.v1"},
		"run":            {"google.cloud.run.v1", "google.cloud.run.v2"},
		"vpcaccess":      {"google.cloud.vpcaccess.v1"},
		"cloudscheduler": {"google.cloud.scheduler.v1"},
	},
	"v2": {
		"cloudfunctions": {"google.cloud.functions.v2"},
		"cloudtasks":     {"google.cloud.tasks.v2"},
		"logging":        {"google.logging.v2"},
		"run":            {"google.cloud.run.v1", "google.cloud.run.v2"},
	},
}

// gcpOperationPackage returns the proto package of the message an operation's
// metadata carries: "google.cloud.eventarc.v1" for
// type.googleapis.com/google.cloud.eventarc.v1.OperationMetadata.
func gcpOperationPackage(op Operation) string {
	typeURL, _ := op.Metadata["@type"].(string)
	typeName := typeURL[strings.LastIndex(typeURL, "/")+1:]
	if i := strings.LastIndex(typeName, "."); i >= 0 {
		return typeName[:i]
	}
	return ""
}

// gcpLocationOperationOwned reports whether the projects.locations.operations
// collection a request addresses at version holds op. Google serves each
// service's collection from its own host, so a request whose Host names a
// service reaches that service's operations only. A request to a bare address
// names no service, and reaches the operations of every service publishing
// the collection at that version.
func gcpLocationOperationOwned(r *http.Request, version string, op Operation) bool {
	services := gcpLocationOperationPackages[version]
	if host := gcpServiceFromHost(r); host != "" {
		return slices.Contains(services[host], gcpOperationPackage(op))
	}
	pkg := gcpOperationPackage(op)
	for _, packages := range services {
		if slices.Contains(packages, pkg) {
			return true
		}
	}
	return false
}

// handleLocationOperationsList serves projects.locations.operations.list at
// version, in operation name order.
func handleLocationOperationsList(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := gcpLocationOperationName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), "")
		owned := func(op Operation) bool {
			return strings.HasPrefix(op.Name, prefix) && gcpLocationOperationOwned(r, version, op)
		}
		out := crOperations.Filter(owned)
		if logOperations != nil {
			out = append(out, logOperations.Filter(owned)...)
		}
		for i, op := range out {
			out[i] = gcpCurrentOperation(op)
		}
		page, next, ok := paginateList(w, r, out, gcpAsc(func(v Operation) string { return v.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"operations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	}
}

// handleLocationOperationDelete serves projects.locations.operations.delete at
// version over the operations its collection holds.
func handleLocationOperationDelete(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := gcpLocationOperationName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "operation"))
		op, ok := crOperations.Get(name)
		if !ok || !gcpLocationOperationOwned(r, version, op) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
			return
		}
		crOperations.Delete(name)
		// google.longrunning.DeleteOperation returns google.protobuf.Empty.
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	}
}
