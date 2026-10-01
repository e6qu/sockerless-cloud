package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

const msRedisInstanceType = "type.googleapis.com/google.cloud.redis.v1.Instance"

// msRedisOperationError is an operation that finished with a google.rpc.Code.
type msRedisOperationError struct {
	code int
	err  error
}

func (e msRedisOperationError) Error() string { return e.err.Error() }

const (
	rpcInvalidArgument = 3
	rpcNotFound        = 5
	rpcInternal        = 13
)

func msRedisOperationFailure(code int, format string, args ...any) error {
	return msRedisOperationError{code: code, err: fmt.Errorf(format, args...)}
}

// msRedisSettle records the outcome of a long-running method that ran inside
// the request: the resource on success, the error on failure.
func msRedisSettle(op Operation, err error) Operation {
	if err == nil {
		return op
	}
	code := rpcInternal
	var failure msRedisOperationError
	if errors.As(err, &failure) {
		code = failure.code
	}
	op.Response = nil
	op.Error = &OperationError{Code: code, Message: err.Error()}
	if crOperations != nil {
		crOperations.Put(op.Name, op)
	}
	return op
}

// msRedisSetState moves an instance to state and returns it as stored.
func msRedisSetState(key, state string) MSRedisInstance {
	msRedisInstances.Update(key, func(i *MSRedisInstance) { i.State = state })
	inst, _ := msRedisInstances.Get(key)
	return inst
}

func handleMSRedisUpgrade(w http.ResponseWriter, r *http.Request, id string) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	key := msRedisInstanceName(project, location, id)
	if _, ok := msRedisInstances.Get(key); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Memorystore instance %q not found", id)
		return
	}
	var req struct {
		RedisVersion string `json:"redisVersion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	image, known := msRedisEngineImage(req.RedisVersion)
	if !known {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "redisVersion %q is not a supported Redis version", req.RedisVersion)
		return
	}
	msRedisSetState(key, "UPGRADING")
	var err error
	if plane, ok := msRedisLoadPlane(key); ok {
		// The new engine starts from the old one's dataset.
		plane.opMu.Lock()
		err = plane.Restart(nil, image)
		plane.opMu.Unlock()
	}
	msRedisInstances.Update(key, func(i *MSRedisInstance) {
		if err == nil {
			i.RedisVersion = req.RedisVersion
		}
		i.State = "READY"
	})
	inst, _ := msRedisInstances.Get(key)
	op := redisInstanceLRO(r, project, location, key, inst, msRedisInstanceType)
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
}

// handleMSRedisFailover promotes a replica to primary. The primary endpoint
// follows the new primary, and the old one becomes its replica.
func handleMSRedisFailover(w http.ResponseWriter, r *http.Request, id string) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	key := msRedisInstanceName(project, location, id)
	inst, ok := msRedisInstances.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Memorystore instance %q not found", id)
		return
	}
	var err error
	if plane, running := msRedisLoadPlane(key); running && inst.ReplicaCount > 0 {
		msRedisSetState(key, "FAILING_OVER")
		plane.opMu.Lock()
		next := (plane.Primary() + 1) % plane.topology.nodes()
		if err = plane.Failover(next); err == nil {
			msRedisPlaneRecords.Update(key, func(record *msRedisPlaneRecord) { record.Primary = next })
		}
		plane.opMu.Unlock()
	}
	inst = msRedisSetState(key, "READY")
	op := redisInstanceLRO(r, project, location, key, inst, msRedisInstanceType)
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
}

// msRedisParseObjectURI splits gs://bucket/object.
func msRedisParseObjectURI(uri string) (bucket, object string, ok bool) {
	rest, found := strings.CutPrefix(uri, "gs://")
	if !found {
		return "", "", false
	}
	bucket, object, found = strings.Cut(rest, "/")
	return bucket, object, found && bucket != "" && object != ""
}

// handleMSRedisTransfer moves an instance's dataset to or from a Cloud Storage
// object as an RDB file. The instance is IMPORTING or EXPORTING while the
// transfer runs and READY when it settles.
//
// An export has the primary write its dataset with SAVE and stores the file
// it wrote. An import stages the file as the primary's snapshot and restarts
// the engine on it, so the instance afterwards holds only the imported data,
// as Memorystore documents.
func handleMSRedisTransfer(w http.ResponseWriter, r *http.Request, id, direction string) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	key := msRedisInstanceName(project, location, id)
	if _, ok := msRedisInstances.Get(key); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Memorystore instance %q not found", id)
		return
	}
	var req struct {
		InputConfig *struct {
			GcsSource *struct {
				URI string `json:"uri"`
			} `json:"gcsSource"`
		} `json:"inputConfig"`
		OutputConfig *struct {
			GcsDestination *struct {
				URI string `json:"uri"`
			} `json:"gcsDestination"`
		} `json:"outputConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	uri := ""
	switch direction {
	case "import":
		if req.InputConfig != nil && req.InputConfig.GcsSource != nil {
			uri = req.InputConfig.GcsSource.URI
		}
	case "export":
		if req.OutputConfig != nil && req.OutputConfig.GcsDestination != nil {
			uri = req.OutputConfig.GcsDestination.URI
		}
	}
	if uri == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"an %s needs the Cloud Storage URI it reads from or writes to", direction)
		return
	}
	bucket, object, ok := msRedisParseObjectURI(uri)
	if !ok {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"%q is not a Cloud Storage URI", uri)
		return
	}
	plane, err := msRedisEngine(key)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "%v", err)
		return
	}
	msRedisSetState(key, strings.ToUpper(direction)+"ING")
	plane.opMu.Lock()
	if direction == "export" {
		err = msRedisExportInstance(plane, bucket, object)
	} else {
		err = msRedisImportInstance(plane, bucket, object)
	}
	plane.opMu.Unlock()
	inst := msRedisSetState(key, "READY")
	op := redisInstanceLRO(r, project, location, key, inst, msRedisInstanceType)
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
}

func msRedisExportInstance(plane *msRedisPlane, bucket, object string) error {
	if _, ok := gcsBuckets.Get(bucket); !ok {
		return msRedisOperationFailure(rpcNotFound, "bucket %q not found", bucket)
	}
	if err := plane.Ensure(); err != nil {
		return err
	}
	data, err := plane.Snapshot(plane.Primary())
	if err != nil {
		return err
	}
	if _, err := persistGCSObjectBytes(bucket, object, data, GCSObject{ContentType: "application/octet-stream"}, gcsPreconditions{}); err != nil {
		return err
	}
	return nil
}

func msRedisImportInstance(plane *msRedisPlane, bucket, object string) error {
	if _, ok := gcsBuckets.Get(bucket); !ok {
		return msRedisOperationFailure(rpcNotFound, "bucket %q not found", bucket)
	}
	data, err := GCSObjectBytes(bucket, object)
	if err != nil {
		return msRedisOperationFailure(rpcNotFound, "%v", err)
	}
	if !msRedisIsRDB(data) {
		return msRedisOperationFailure(rpcInvalidArgument, "gs://%s/%s is not an RDB file", bucket, object)
	}
	return plane.Restart(data, "")
}
