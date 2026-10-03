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
// An operation that is done takes the late cancel the method's own description
// contemplates — "the operation completed despite cancellation" — and keeps
// its recorded result. These stores hold two kinds of unfinished operation:
// Cloud Run's RunJob, whose cancel cancels the execution it runs, the
// operation completing with CANCELLED once the execution's workload has
// stopped; and a Cloud Run worker pool's or instance's deploy, whose cancel
// stops the instances it was starting. TestGCPOperationsAreRecordedComplete
// pins that every other service records its operations complete, so a store
// that starts holding other unfinished work fails there rather than silently
// getting a no-op cancel here.
func handleGCPCancelOperation(w http.ResponseWriter, name string) {
	op, ok := gcpLookupOperation(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
		return
	}
	gcpCancelOperationWork(op)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// gcpCancelOperationWork stops the work an unfinished operation stands for.
func gcpCancelOperationWork(op Operation) {
	if op.Done {
		return
	}
	typeName, _ := op.Metadata["@type"].(string)
	switch typeName {
	case cloudRunWorkerPoolType, cloudRunInstanceType:
		name, _ := op.Metadata["name"].(string)
		cancelCloudRunReconcile(name, typeName)
		return
	case cloudRunExecutionType:
	default:
		return
	}
	execName, _ := op.Metadata["name"].(string)
	parts := strings.Split(execName, "/")
	if len(parts) != 8 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "jobs" || parts[6] != "executions" {
		return
	}
	if exec, ok := crjExecutions.Get(execName); ok && exec.RunningCount > 0 {
		cancelCloudRunExecution(parts[1], parts[3], parts[5], parts[7])
	}
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
