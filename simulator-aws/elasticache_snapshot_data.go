package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
)

// A snapshot keeps the RDB file each shard's primary writes with SAVE; a
// resource created from it loads them into its engine: an instance's primary
// starts from the file, a cluster imports each one shard by shard.

type ecSnapshotFile struct {
	NodeGroupID string `json:"nodeGroupId"`
	Node        int    `json:"node"`
	Ref         string `json:"ref"`
	Size        int64  `json:"size"`
}

type ecSnapshotContent struct {
	Files []ecSnapshotFile `json:"files"`
}

type ECNodeSnapshot struct {
	CacheClusterId     string
	NodeGroupId        string
	CacheNodeId        string
	SnapshotCreateTime string
}

var (
	ecSnapshotContents sim.Store[ecSnapshotContent]
	ecSnapshotPayloads *blobstore.Payloads
)

func ecNodeSnapshotKey(name string) string { return "snapshot:" + name }

func ecServerlessSnapshotKey(name string) string { return "serverlesscachesnapshot:" + name }

func ecOpenSnapshotPayloads(srv *sim.Server) {
	ecSnapshotContents = sim.MakeStore[ecSnapshotContent](srv.DB(), "elasticache_snapshot_contents")
	payloads, err := srv.Payloads("elasticache-snapshots")
	if err != nil {
		log.Fatalf("Amazon ElastiCache snapshot contents: %v", err)
	}
	referenced := map[string]bool{}
	for _, content := range ecSnapshotContents.List() {
		for _, file := range content.Files {
			referenced[file.Ref] = true
		}
	}
	if _, err := payloads.Sweep(func(ref string) bool { return referenced[ref] }); err != nil {
		log.Fatalf("Amazon ElastiCache snapshot contents: %v", err)
	}
	ecSnapshotPayloads = payloads
}

func ecReleaseFiles(files []ecSnapshotFile) {
	for _, file := range files {
		if err := ecSnapshotPayloads.Remove(file.Ref); err != nil {
			log.Printf("Amazon ElastiCache snapshot file %s: %v", file.Ref, err)
		}
	}
}

func ecReleaseSnapshotContent(key string) {
	content, ok := ecSnapshotContents.Get(key)
	if !ok {
		return
	}
	ecSnapshotContents.Delete(key)
	ecReleaseFiles(content.Files)
}

// Every file gets a copy of its own, so deleting either snapshot leaves the
// other whole.
func ecCopySnapshotContent(from, to string) error {
	content, ok := ecSnapshotContents.Get(from)
	if !ok {
		return nil
	}
	var copied ecSnapshotContent
	for _, file := range content.Files {
		data, err := ecSnapshotPayloads.Read(file.Ref)
		if err != nil {
			ecReleaseFiles(copied.Files)
			return err
		}
		ref, _, err := ecSnapshotPayloads.Write(data)
		if err != nil {
			ecReleaseFiles(copied.Files)
			return err
		}
		file.Ref = ref
		copied.Files = append(copied.Files, file)
	}
	ecSnapshotContents.Put(to, copied)
	return nil
}

func ecSnapshotSources(key, name string) ([]cacheengine.ImportSource, error) {
	content, ok := ecSnapshotContents.Get(key)
	if !ok {
		return nil, nil
	}
	sources := make([]cacheengine.ImportSource, 0, len(content.Files))
	for _, file := range content.Files {
		data, err := ecSnapshotPayloads.Read(file.Ref)
		if err != nil {
			return nil, fmt.Errorf("read snapshot %s: %w", name, err)
		}
		sources = append(sources, cacheengine.ImportSource{Name: name + "-" + file.NodeGroupID + ".rdb", Data: data})
	}
	return sources, nil
}

func ecEngineIsCluster(key string) bool {
	record, ok := ecEngineRecords.Get(key)
	return ok && record.Cluster
}

// Each shard's primary writes its dataset, or node alone when node is not
// negative. The caller holds engine.opMu.
func ecCaptureSnapshot(engine *ecEngine, node int) (ecSnapshotContent, error) {
	var content ecSnapshotContent
	if engine.redis == nil {
		return content, errors.New("a Memcached engine writes no snapshot")
	}
	if err := engine.redis.Ensure(); err != nil {
		return content, err
	}
	nodes := []int{node}
	if node < 0 {
		nodes = []int{engine.redis.Primary()}
		if ecEngineIsCluster(engine.key) {
			primaries, _, err := engine.redis.ShardMap()
			if err != nil {
				return content, err
			}
			nodes = primaries
		}
	}
	for shard, index := range nodes {
		data, err := engine.redis.Snapshot(index)
		if err != nil {
			ecReleaseFiles(content.Files)
			return ecSnapshotContent{}, err
		}
		ref, digests, err := ecSnapshotPayloads.Write(data)
		if err != nil {
			ecReleaseFiles(content.Files)
			return ecSnapshotContent{}, err
		}
		content.Files = append(content.Files, ecSnapshotFile{NodeGroupID: ecCacheNodeID(shard), Node: index, Ref: ref, Size: digests.Size})
	}
	return content, nil
}

func ecContentSize(content ecSnapshotContent) int64 {
	var total int64
	for _, file := range content.Files {
		total += file.Size
	}
	return total
}

// Answers creating; the snapshot turns available once its engine has written
// every file, and goes when the engine writes none. A simulator that runs no
// engines keeps a snapshot of no data. done runs once the capture settles.
func ecTakeSnapshot(s ECSnapshot, engineKey string, node int, memberOf func(node int) string, done func()) ECSnapshot {
	s.SnapshotStatus = "creating"
	if s.EngineVersion == "" {
		s.EngineVersion = ecDefaultEngineVersion(s.Engine)
	}
	ecSnapshots.Put(s.SnapshotName, s)
	engine, _ := ecLoadEngine(engineKey)
	var content ecSnapshotContent
	ecRunInBackground(engine, func(e *ecEngine) error {
		var err error
		content, err = ecCaptureSnapshot(e, node)
		return err
	}, func(err error) {
		if err != nil {
			ecSnapshots.Delete(s.SnapshotName)
			done()
			return
		}
		ecSnapshotContents.Put(ecNodeSnapshotKey(s.SnapshotName), content)
		taken := ecNow()
		var nodes []ECNodeSnapshot
		for _, file := range content.Files {
			nodes = append(nodes, ECNodeSnapshot{
				CacheClusterId: memberOf(file.Node), NodeGroupId: file.NodeGroupID,
				CacheNodeId: ecCacheNodeID(0), SnapshotCreateTime: taken,
			})
		}
		kept := ecSnapshots.Update(s.SnapshotName, func(stored *ECSnapshot) {
			stored.SnapshotStatus, stored.NodeSnapshots = "available", nodes
		})
		if !kept {
			ecReleaseSnapshotContent(ecNodeSnapshotKey(s.SnapshotName))
		}
		done()
	})
	return s
}

// A snapshot of the cluster or replication group named in r, taken before its
// engine goes. Without FinalSnapshotIdentifier, deleteEngine runs at once.
// Answers the error itself and reports false.
func ecFinalSnapshot(w http.ResponseWriter, r *http.Request, s ECSnapshot, engineKey string, memberOf func(int) string) (func(deleteEngine func()), bool) {
	name := r.FormValue("FinalSnapshotIdentifier")
	if name == "" {
		return func(deleteEngine func()) { deleteEngine() }, true
	}
	requestID := sim.RequestID(r.Context())
	if s.Engine == "memcached" {
		ecErrorXML(w, "SnapshotFeatureNotSupportedFault", "Snapshots are not supported for clusters running Memcached.", http.StatusBadRequest, requestID)
		return nil, false
	}
	if _, exists := ecSnapshots.Get(name); exists {
		ecErrorXML(w, "SnapshotAlreadyExistsFault", fmt.Sprintf("Snapshot %s already exists.", name), http.StatusBadRequest, requestID)
		return nil, false
	}
	s.SnapshotName, s.ARN, s.SnapshotSource = name, ecSnapshotARN(name), "manual"
	return func(deleteEngine func()) {
		_ = ecTakeSnapshot(s, engineKey, -1, memberOf, deleteEngine)
	}, true
}

// What a create loads into its new engine.
type ecRestore struct {
	// Node-based snapshot being restored, which reports restoring meanwhile.
	snapshot string
	sources  []cacheengine.ImportSource
	engine   string
	version  string
}

func (rs ecRestore) any() bool { return rs.snapshot != "" || len(rs.sources) > 0 }

// Starts the engine and loads the restore into it.
func (rs ecRestore) start(cluster bool) func(*ecEngine) error {
	return func(e *ecEngine) error {
		if err := e.start(); err != nil {
			return err
		}
		if err := ecLoadRDBs(e, cluster, rs.sources); err != nil {
			return &ecRestoreError{err: err}
		}
		return nil
	}
}

func (rs ecRestore) finish() {
	if rs.snapshot != "" {
		ecSnapshots.Update(rs.snapshot, func(s *ECSnapshot) {
			if s.Restores > 0 {
				s.Restores--
			}
			if s.SnapshotStatus == "restoring" && s.Restores == 0 {
				s.SnapshotStatus = "available"
			}
		})
	}
}

type ecRestoreError struct{ err error }

func (e *ecRestoreError) Error() string { return "restore the snapshot: " + e.err.Error() }

func (e *ecRestoreError) Unwrap() error { return e.err }

func ecLoadRDBs(engine *ecEngine, cluster bool, sources []cacheengine.ImportSource) error {
	if len(sources) == 0 || engine.redis == nil {
		return nil
	}
	if cluster {
		for _, source := range sources {
			if err := engine.redis.ImportRDB(source); err != nil {
				return err
			}
		}
		return nil
	}
	return engine.redis.Restart(sources[0].Data, "")
}

// SnapshotName or SnapshotArns of a CreateCacheCluster or
// CreateReplicationGroup request. Answers the error itself and reports false.
func ecRequestedRestore(w http.ResponseWriter, r *http.Request) (ecRestore, bool) {
	requestID := sim.RequestID(r.Context())
	name := r.FormValue("SnapshotName")
	arns := ecParseStringList(r, "SnapshotArns", "SnapshotArn")
	var restore ecRestore
	switch {
	case name != "" && len(arns) > 0:
		ecErrorXML(w, "InvalidParameterCombination", "Specify either SnapshotName or SnapshotArns, not both.", http.StatusBadRequest, requestID)
		return restore, false
	case name != "":
		s, ok := ecSnapshots.Get(name)
		if !ok {
			ecErrorXML(w, "SnapshotNotFoundFault", fmt.Sprintf("Snapshot %s not found.", name), http.StatusNotFound, requestID)
			return restore, false
		}
		if s.SnapshotStatus != "available" && s.SnapshotStatus != "restoring" {
			ecErrorXML(w, "InvalidSnapshotState", fmt.Sprintf("Snapshot %s is %s, not available.", name, s.SnapshotStatus), http.StatusBadRequest, requestID)
			return restore, false
		}
		sources, err := ecSnapshotSources(ecNodeSnapshotKey(name), name)
		if err != nil {
			ecErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, requestID)
			return restore, false
		}
		restore = ecRestore{snapshot: name, sources: sources, engine: s.Engine, version: s.EngineVersion}
	case len(arns) > 0:
		for _, arn := range arns {
			source, message := ecS3RDB(arn)
			if message != "" {
				ecErrorXML(w, "InvalidParameterValue", message, http.StatusBadRequest, requestID)
				return restore, false
			}
			restore.sources = append(restore.sources, source)
		}
	}
	if len(restore.sources) > 0 && !ecRunsEngines() {
		ecErrorXML(w, "InvalidParameterCombination", "No engine can load the snapshot: this simulator runs no Amazon ElastiCache data plane.", http.StatusBadRequest, requestID)
		return restore, false
	}
	return restore, true
}

// Holds the snapshot at restoring while the new resource loads it.
func (rs ecRestore) begin() {
	if rs.snapshot != "" {
		ecSnapshots.Update(rs.snapshot, func(s *ECSnapshot) { s.SnapshotStatus, s.Restores = "restoring", s.Restores+1 })
	}
}

// An RDB file in Amazon S3, as arn:aws:s3:::bucket/key names it; the message
// says why it cannot load.
func ecS3RDB(arn string) (cacheengine.ImportSource, string) {
	path, ok := strings.CutPrefix(arn, "arn:aws:s3:::")
	bucket, key, found := strings.Cut(path, "/")
	if !ok || !found || bucket == "" || key == "" {
		return cacheengine.ImportSource{}, fmt.Sprintf("%s is not the ARN of an Amazon S3 object.", arn)
	}
	if strings.Contains(key, ",") {
		return cacheengine.ImportSource{}, fmt.Sprintf("The Amazon S3 object name in %s contains a comma.", arn)
	}
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		return cacheengine.ImportSource{}, fmt.Sprintf("The Amazon S3 object %s does not exist.", arn)
	}
	data, err := s3ObjectData(obj)
	if err != nil {
		return cacheengine.ImportSource{}, fmt.Sprintf("Read the Amazon S3 object %s: %v", arn, err)
	}
	if !cacheengine.IsRDB(data) {
		return cacheengine.ImportSource{}, fmt.Sprintf("The Amazon S3 object %s is not an RDB file.", arn)
	}
	return cacheengine.ImportSource{Name: arn, Data: data}, ""
}

// Writes each file as <prefix>-<node group>.rdb, as ElastiCache names an
// exported backup's files.
func ecExportSnapshot(key, bucket, prefix string) error {
	if _, ok := s3Buckets_.Get(bucket); !ok {
		return fmt.Errorf("the Amazon S3 bucket %s does not exist", bucket)
	}
	content, _ := ecSnapshotContents.Get(key)
	for _, file := range content.Files {
		data, err := ecSnapshotPayloads.Read(file.Ref)
		if err != nil {
			return err
		}
		if _, err := s3PutServiceObject(bucket, prefix+"-"+file.NodeGroupID+".rdb", data, "application/octet-stream", nil); err != nil {
			return err
		}
	}
	return nil
}

// Answers creating, and available once the engine has written its file; done
// runs once the capture settles.
func ecTakeServerlessSnapshot(s ECServerlessCacheSnapshot, done func()) ECServerlessCacheSnapshot {
	s.Status = "creating"
	ecServerlessSnapshots.Put(s.ServerlessCacheSnapshotName, s)
	engine, _ := ecLoadEngine(ecEngineKey(ecKindServerless, s.CacheName))
	var content ecSnapshotContent
	ecRunInBackground(engine, func(e *ecEngine) error {
		var err error
		content, err = ecCaptureSnapshot(e, -1)
		return err
	}, func(err error) {
		defer done()
		name := s.ServerlessCacheSnapshotName
		if err != nil {
			ecServerlessSnapshots.Delete(name)
			return
		}
		key := ecServerlessSnapshotKey(name)
		ecSnapshotContents.Put(key, content)
		if !ecServerlessSnapshots.Update(name, func(stored *ECServerlessCacheSnapshot) {
			stored.Status = "available"
			stored.BytesUsedForCache = fmt.Sprint(ecContentSize(content))
		}) {
			ecReleaseSnapshotContent(key)
		}
	})
	return s
}

// The serverless or node-based snapshots SnapshotArnsToRestore names.
// Answers the error itself and reports false.
func ecRequestedServerlessRestore(w http.ResponseWriter, r *http.Request) (ecRestore, bool) {
	requestID := sim.RequestID(r.Context())
	var restore ecRestore
	for _, arn := range ecParseStringList(r, "SnapshotArnsToRestore", "SnapshotArn") {
		key, name, engine, status, found := ecSnapshotByARN(arn)
		if !found {
			ecErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Snapshot %s not found.", arn), http.StatusBadRequest, requestID)
			return restore, false
		}
		if status != "available" && status != "restoring" {
			ecErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Snapshot %s is %s, not available.", arn, status), http.StatusBadRequest, requestID)
			return restore, false
		}
		sources, err := ecSnapshotSources(key, name)
		if err != nil {
			ecErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, requestID)
			return restore, false
		}
		restore.sources = append(restore.sources, sources...)
		restore.engine = engine
	}
	if len(restore.sources) > 0 && !ecRunsEngines() {
		ecErrorXML(w, "InvalidParameterCombination", "No engine can load the snapshot: this simulator runs no Amazon ElastiCache data plane.", http.StatusBadRequest, requestID)
		return restore, false
	}
	return restore, true
}

func ecSnapshotByARN(arn string) (key, name, engine, status string, found bool) {
	for _, s := range ecServerlessSnapshots.List() {
		if s.ARN == arn {
			return ecServerlessSnapshotKey(s.ServerlessCacheSnapshotName), s.ServerlessCacheSnapshotName, s.CacheEngine, s.Status, true
		}
	}
	for _, s := range ecSnapshots.List() {
		if s.ARN == arn {
			return ecNodeSnapshotKey(s.SnapshotName), s.SnapshotName, s.Engine, s.SnapshotStatus, true
		}
	}
	return "", "", "", "", false
}

// A capture or restore an earlier process left running did not finish: the
// snapshot it was writing goes, and its resource is available again.
func ecRecoverSnapshots() {
	for _, s := range ecSnapshots.List() {
		switch s.SnapshotStatus {
		case "creating":
			ecSnapshots.Delete(s.SnapshotName)
			ecReleaseSnapshotContent(ecNodeSnapshotKey(s.SnapshotName))
		case "restoring":
			ecSnapshots.Update(s.SnapshotName, func(stored *ECSnapshot) { stored.SnapshotStatus, stored.Restores = "available", 0 })
		}
	}
	for _, s := range ecServerlessSnapshots.List() {
		if s.Status == "creating" {
			ecServerlessSnapshots.Delete(s.ServerlessCacheSnapshotName)
			ecReleaseSnapshotContent(ecServerlessSnapshotKey(s.ServerlessCacheSnapshotName))
		}
	}
	for _, c := range ecClusters.List() {
		if c.CacheClusterStatus == "snapshotting" {
			ecSetClusterAvailable(c.CacheClusterId, c.Incarnation)
		}
	}
	for _, g := range ecReplGroups.List() {
		if g.Status == "snapshotting" {
			ecReplGroups.Update(g.ReplicationGroupId, func(stored *ECReplicationGroup) { stored.Status = "available" })
		}
	}
}
