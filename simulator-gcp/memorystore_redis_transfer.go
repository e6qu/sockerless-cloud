package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
)

const msRedisInstanceType = "type.googleapis.com/google.cloud.redis.v1.Instance"

// msRedisOperationError is an operation that finished with a google.rpc.Code.
type msRedisOperationError struct {
	code int
	err  error
}

func (e msRedisOperationError) Error() string { return e.err.Error() }

const (
	rpcInvalidArgument    = 3
	rpcNotFound           = 5
	rpcFailedPrecondition = 9
	rpcInternal           = 13
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
	var rule *cacheengine.InvalidRuleError
	var source *cacheengine.ImportError
	switch {
	case errors.As(err, &failure):
		code = failure.code
	case errors.As(err, &rule), errors.As(err, &source):
		code = rpcInvalidArgument
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
	op := redisInstanceLRO(r, project, location, key, msRedisInstanceView(inst), msRedisInstanceType)
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
}

// handleMSRedisFailover promotes a replica to primary. The primary endpoint
// follows the new primary, and the old one becomes its replica. A Basic Tier
// instance has no replica to promote, so the service refuses the failover. A
// failover in LIMITED_DATA_LOSS mode, the default, promotes the replica only
// while its replication offset trails the primary's by less than
// msRedisLimitedDataLossBytes.
func handleMSRedisFailover(w http.ResponseWriter, r *http.Request, id string) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	key := msRedisInstanceName(project, location, id)
	inst, ok := msRedisInstances.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Memorystore instance %q not found", id)
		return
	}
	var req struct {
		DataProtectionMode json.RawMessage `json:"dataProtectionMode"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	// The Go REST client sends the enum's number, and other clients its name.
	mode, err := fsDecodeEnum(req.DataProtectionMode, map[int]string{
		0: "DATA_PROTECTION_MODE_UNSPECIFIED", 1: "LIMITED_DATA_LOSS", 2: "FORCE_DATA_LOSS",
	})
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid dataProtectionMode: %v", err)
		return
	}
	switch mode {
	case "", "DATA_PROTECTION_MODE_UNSPECIFIED", "LIMITED_DATA_LOSS", "FORCE_DATA_LOSS":
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"dataProtectionMode %q is not one of LIMITED_DATA_LOSS or FORCE_DATA_LOSS", mode)
		return
	}
	if inst.Tier != "STANDARD_HA" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"Failover is only supported for Standard Tier instances; instance %q is in the %s tier", id, inst.Tier)
		return
	}
	if plane, running := msRedisLoadPlane(key); running {
		msRedisSetState(key, "FAILING_OVER")
		plane.opMu.Lock()
		next := plane.Primary()
		for _, node := range plane.Nodes() {
			if node != next {
				next = node
				break
			}
		}
		if mode != "FORCE_DATA_LOSS" {
			err = plane.checkReplicationGap(next)
		}
		if err == nil {
			if err = plane.Failover(next); err == nil {
				msRedisSaveRecord(key)
			}
		}
		plane.opMu.Unlock()
	}
	inst = msRedisSetState(key, "READY")
	op := redisInstanceLRO(r, project, location, key, msRedisInstanceView(inst), msRedisInstanceType)
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
}

// msRedisLimitedDataLossBytes is the replication offset gap Memorystore for
// Redis tolerates in a LIMITED_DATA_LOSS failover: 30 MB.
const msRedisLimitedDataLossBytes = 30 << 20

// checkReplicationGap fails unless replica has acknowledged the primary's
// replication stream to within msRedisLimitedDataLossBytes, by the primary's
// own account, so a replica that stopped acknowledging trails by everything
// written since.
func (p *msRedisPlane) checkReplicationGap(replica int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	ip, err := p.NodeIP(replica)
	if err != nil {
		return err
	}
	reply, err := p.Command(p.Primary(), "INFO", "replication")
	if err != nil {
		return err
	}
	text, _ := reply.(string)
	info := cacheengine.ParseInfo(text)
	primaryOffset, err := strconv.ParseInt(info["master_repl_offset"], 10, 64)
	if err != nil {
		return fmt.Errorf("the primary reports no replication offset: %w", err)
	}
	for name, value := range info {
		if !strings.HasPrefix(name, "slave") {
			continue
		}
		fields := map[string]string{}
		for _, field := range strings.Split(value, ",") {
			if k, v, ok := strings.Cut(field, "="); ok {
				fields[k] = v
			}
		}
		if fields["ip"] != ip {
			continue
		}
		replicaOffset, err := strconv.ParseInt(fields["offset"], 10, 64)
		if err != nil {
			return fmt.Errorf("the primary reports no replication offset for the replica: %w", err)
		}
		if gap := primaryOffset - replicaOffset; gap >= msRedisLimitedDataLossBytes {
			return msRedisOperationFailure(rpcFailedPrecondition,
				"the replica trails the primary by %d bytes of replication, more than the %d LIMITED_DATA_LOSS allows; retry once it catches up, or fail over with FORCE_DATA_LOSS",
				gap, msRedisLimitedDataLossBytes)
		}
		return nil
	}
	return msRedisOperationFailure(rpcFailedPrecondition,
		"the replica is not replicating from the primary, so a LIMITED_DATA_LOSS failover cannot bound the data it loses")
}

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
	op := redisInstanceLRO(r, project, location, key, msRedisInstanceView(inst), msRedisInstanceType)
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
	if !cacheengine.IsRDB(data) {
		return msRedisOperationFailure(rpcInvalidArgument, "gs://%s/%s is not an RDB file", bucket, object)
	}
	return plane.Restart(data, "")
}

// msRedisImportSource is one RDB file a new cluster loads.
type msRedisImportSource = cacheengine.ImportSource

// msRedisClusterImportSources reads the RDB files a cluster create names in
// gcsSource or managedBackupSource.
func msRedisClusterImportSources(gcs *MSRedisGcsBackupSource, managed *MSRedisManagedBackupSource) ([]msRedisImportSource, error) {
	switch {
	case gcs != nil && managed != nil:
		return nil, msRedisOperationFailure(rpcInvalidArgument, "a cluster imports from gcsSource or managedBackupSource, not both")
	case gcs != nil:
		if len(gcs.Uris) == 0 {
			return nil, msRedisOperationFailure(rpcInvalidArgument, "gcsSource names no Cloud Storage object")
		}
		var sources []msRedisImportSource
		for _, uri := range gcs.Uris {
			bucket, object, ok := msRedisParseObjectURI(uri)
			if !ok {
				return nil, msRedisOperationFailure(rpcInvalidArgument, "%q is not a Cloud Storage URI", uri)
			}
			if _, ok := gcsBuckets.Get(bucket); !ok {
				return nil, msRedisOperationFailure(rpcNotFound, "bucket %q not found", bucket)
			}
			data, err := GCSObjectBytes(bucket, object)
			if err != nil {
				return nil, msRedisOperationFailure(rpcNotFound, "%v", err)
			}
			if !cacheengine.IsRDB(data) {
				return nil, msRedisOperationFailure(rpcInvalidArgument, "%s is not an RDB file", uri)
			}
			sources = append(sources, msRedisImportSource{Name: uri, Data: data})
		}
		return sources, nil
	case managed != nil:
		name := strings.TrimPrefix(managed.Backup, "//redis.googleapis.com/")
		if _, ok := msRedisBackups.Get(name); !ok {
			return nil, msRedisOperationFailure(rpcNotFound, "backup %q not found", managed.Backup)
		}
		content, ok := msRedisBackupContents.Get(name)
		if !ok {
			return nil, msRedisOperationFailure(rpcFailedPrecondition, "backup %s holds no files", name)
		}
		var sources []msRedisImportSource
		for _, file := range content.Files {
			data, err := msRedisBackupPayloads.Read(file.Ref)
			if err != nil {
				return nil, fmt.Errorf("read backup file %s: %w", file.FileName, err)
			}
			sources = append(sources, msRedisImportSource{Name: name + "/" + file.FileName, Data: data})
		}
		return sources, nil
	}
	return nil, nil
}
