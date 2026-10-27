package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// google.longrunning.Operations.CancelOperation, the AIP-151 method the
// Discovery documents declare on their operation collections, for every
// service that spells it the standard way. The documents call cancellation
// best effort, so a completed operation keeps its result and the call answers
// google.protobuf.Empty; an unrecorded operation is NOT_FOUND. Cloud SQL
// Admin's sql.operations.cancel (sqladmin.go) and Cloud Build's build
// operations (cloudbuild.go) are served where those services live.

// gcpOperationStores are the stores the standard cancel resolves a name in:
// crOperations, where newLRO records, and Cloud Logging's scope-parented
// store, whose project-scope operations share the Cloud Run collection's URI.
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
			return gcpCurrentOperation(op), true
		}
	}
	return Operation{}, false
}

// handleGCPCancelOperation answers CancelOperation for one name. A done
// operation keeps its result. The unfinished ones are Cloud Run's RunJob,
// whose cancel cancels the execution, and a Cloud Run worker pool's or
// instance's deploy, whose cancel stops the instances it was starting;
// TestGCPOperationsAreRecordedComplete holds every other service to recording
// operations complete.
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

// registerOperationsCancel mounts the cancel spellings outside the
// projects/locations operation collection, which the Cloud Run fan-in in
// cloudrunservices.go serves. Cloud Build and Service Usage both declare
// "v1/operations/{name}:cancel", so one handler routes on the name: Cloud
// Build's are operations/build/{project}/{id}.
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
