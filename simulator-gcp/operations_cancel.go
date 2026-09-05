package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// google.longrunning.Operations.CancelOperation — the AIP-151 custom method
// the vendored Discovery documents declare on their long-running-operation
// collections, mounted here for every Google Cloud service the simulator
// serves that spells it the standard way.
//
// The contract the documents themselves state, word for word, is that
// cancellation is best effort and that a client "can use Operations.GetOperation
// or other methods to check whether the cancellation succeeded or whether the
// operation completed despite cancellation". A completed operation is therefore
// a documented outcome of cancel, not an error: the recorded result is left
// exactly as it stands and google.protobuf.Empty comes back. An operation the
// service has no record of is NOT_FOUND, the same as a get of that name.
//
// Two services spell the method their own way and answer differently; they are
// served where they are implemented rather than here:
//
//   - Cloud SQL Admin's sql.operations.cancel is the service's own method
//     ("Cancels an instance operation that has been performed on an instance"),
//     refuses an operation that is not in progress, and supports only the
//     import and export operation types — sqladmin.go.
//   - Cloud Build's build operations are projections of the build record
//     rather than rows in an operation store, and cancelling one terminates the
//     build steps that are running — cloudbuild.go.

// gcpOperationStores are the stores the standard cancel resolves a name
// against. Cloud Run, API Gateway, Artifact Registry, Eventarc, Memorystore for
// Redis, Service Usage, Cloud Spanner and Cloud Resource Manager all record
// their operations in crOperations through newLRO; Cloud Logging keeps its own
// scope-parented store, and its project-scope operations share a URI with the
// Cloud Run collection, so both have to be searched for a name.
func gcpOperationStores() []sim.Store[Operation] {
	var stores []sim.Store[Operation]
	if crOperations != nil {
		stores = append(stores, crOperations)
	}
	if logOperations != nil {
		stores = append(stores, logOperations)
	}
	return stores
}

// gcpLookupOperation finds a recorded operation by name in whichever store
// holds it.
func gcpLookupOperation(name string) (Operation, bool) {
	for _, store := range gcpOperationStores() {
		if op, ok := store.Get(name); ok {
			return op, true
		}
	}
	return Operation{}, false
}

// handleGCPCancelOperation answers CancelOperation for one operation name.
//
// Every operation the simulator records is complete before its name can reach
// a client: the work each service's LRO wraps runs inside the request that
// returns the operation, so the record is written with done set. A cancel is
// therefore always the late cancel the method's own description contemplates —
// "the operation completed despite cancellation" — and the honest answer is to
// leave the recorded result untouched and return Empty.
// TestGCPOperationsAreRecordedComplete pins that invariant, so an operation
// store that ever starts holding unfinished work fails there rather than
// silently getting a no-op cancel here.
func handleGCPCancelOperation(w http.ResponseWriter, name string) {
	if _, ok := gcpLookupOperation(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// registerOperationsCancel mounts the cancel spellings that do not sit under
// the projects/locations operation collection (which the Cloud Run fan-in in
// cloudrunservices.go dispatches, since it owns that URI for every service
// sharing it).
//
// Cloud Build and Service Usage both declare a cancel on the top-level
// operations collection at the identical URI "v1/operations/{name}:cancel",
// so one handler answers both, routing on the name each service mints:
// Cloud Build's build operations are named operations/build/{project}/{id}.
func registerOperationsCancel(srv *sim.Server) {
	srv.HandleFunc("POST /v1/operations/{opAction...}", func(w http.ResponseWriter, r *http.Request) {
		opAction := sim.PathParam(r, "opAction")
		tail, verb := splitColonVerb(opAction)
		if verb != "cancel" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown operation action %q", opAction)
			return
		}
		name := "operations/" + tail
		// Cloud Build's build operations are a projection of the build
		// record, and cancelling one cancels the build.
		if parts := strings.Split(name, "/"); len(parts) == 4 && parts[1] == "build" {
			handleCloudBuildCancelOperation(w, parts[3])
			return
		}
		handleGCPCancelOperation(w, name)
	})
}

// gcpLocationOperationName is the resource name of an operation in a
// projects/{project}/locations/{location} collection.
func gcpLocationOperationName(project, location, operation string) string {
	return fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, operation)
}
