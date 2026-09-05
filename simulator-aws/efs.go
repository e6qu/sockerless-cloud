package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// efsHostRoot returns the on-disk backing directory for the whole
// simulated EFS slice. Each filesystem + access point becomes a
// subdirectory so Docker tasks the ECS slice launches can bind-mount
// a real host path and observe the same files across tasks.
// Resolution order: SIM_EFS_DATA_DIR (explicit override), then
// <SIM_DATA_DIR>/efs (so file contents survive a simulator restart
// alongside the SQLite control-plane state), then a temp directory.
func efsHostRoot() string {
	return simScopedDataDir("SIM_EFS_DATA_DIR", "efs", "sockerless-sim-efs")
}

// EFSFileSystemHostDir returns the on-disk directory backing a
// simulated EFS filesystem. Created lazily; safe for concurrent
// callers. Exported for use by the ECS task runner.
func EFSFileSystemHostDir(fsID string) string {
	dir := filepath.Join(efsHostRoot(), fsID)
	created := false
	if _, err := os.Stat(dir); err != nil {
		created = true
	}
	_ = os.MkdirAll(dir, 0o777)
	if created {
		// MkdirAll's mode is masked by the umask; chmod (which isn't) so a
		// directly-mounted filesystem (an EFS volume without an access point)
		// is writable by a non-root / uid-mapped workload.
		_ = os.Chmod(dir, 0o777)
	}
	return dir
}

// EFSAccessPointHostDir returns the on-disk directory that an EFS
// access point points at (FileSystemHostDir + AccessPoint.RootDirectory).
// Returns "" when the access point does not exist.
func EFSAccessPointHostDir(apID string) string {
	ap, ok := efsAccessPoints.Get(apID)
	if !ok {
		return ""
	}
	root := EFSFileSystemHostDir(ap.FileSystemId)
	if ap.RootDirectory != nil && ap.RootDirectory.Path != "" {
		root = filepath.Join(root, strings.TrimPrefix(ap.RootDirectory.Path, "/"))
	}
	ensureAccessPointRootDir(root, ap.RootDirectory)
	return root
}

// ensureAccessPointRootDir creates an access point's root directory, applying
// the RootDirectory.CreationInfo (owner uid/gid + permissions) exactly as real
// EFS does — but only when the directory is first created, so a workload that
// later changes the perms keeps them on subsequent mounts.
//
// Why this matters: `os.MkdirAll`'s mode argument is masked by the process
// umask (typically 022), so a requested 0777 lands as 0755 — and CreationInfo
// was otherwise ignored entirely. A gitlab-runner build volume created with
// CreationInfo{0777, 1000:1000} would then be 0755 root and the (non-root, or
// uid-mapped) job container couldn't write to it.
func ensureAccessPointRootDir(root string, rd *EFSRootDirectory) {
	if _, err := os.Stat(root); err == nil {
		return // already exists — don't clobber workload-set ownership/perms
	}
	_ = os.MkdirAll(root, 0o777)

	// Default to 0777 when no CreationInfo is supplied so the mount is writable
	// regardless of the umask (the prior behaviour intended 0777 but the umask
	// reduced it). CreationInfo, when present, is authoritative.
	mode := os.FileMode(0o777)
	if rd != nil && rd.CreationInfo != nil && rd.CreationInfo.Permissions != "" {
		if parsed, err := strconv.ParseUint(rd.CreationInfo.Permissions, 8, 32); err == nil {
			mode = os.FileMode(parsed)
		}
	}
	_ = os.Chmod(root, mode) // chmod is not umask-masked; this is the real mode

	// Best-effort chown to the access point's owner. Requires privilege (the
	// sim runs as root inside the harness/CI container); if it fails — e.g. a
	// developer running the sim natively as a non-root user — the permissions
	// above still make the directory usable.
	if rd != nil && rd.CreationInfo != nil {
		_ = os.Chown(root, int(rd.CreationInfo.OwnerUid), int(rd.CreationInfo.OwnerGid))
	}
}

// EFS types

type EFSFileSystem struct {
	FileSystemId    string `json:"FileSystemId"`
	FileSystemArn   string `json:"FileSystemArn"`
	CreationToken   string `json:"CreationToken"`
	CreationTime    int64  `json:"CreationTime"`
	LifeCycleState  string `json:"LifeCycleState"`
	Name            string `json:"Name,omitempty"`
	OwnerId         string `json:"OwnerId"`
	PerformanceMode string `json:"PerformanceMode"`
	ThroughputMode  string `json:"ThroughputMode"`
	// ProvisionedThroughputInMibps is only present when ThroughputMode is
	// "provisioned"; AWS omits it otherwise. *float64 so it round-trips a
	// 0-vs-unset distinction the SDK reads back.
	ProvisionedThroughputInMibps *float64 `json:"ProvisionedThroughputInMibps,omitempty"`
	NumberOfMountTargets         int      `json:"NumberOfMountTargets"`
	SizeInBytes                  struct {
		Value int64 `json:"Value"`
	} `json:"SizeInBytes"`
	// FileSystemProtection carries the replication-overwrite-protection status,
	// set by UpdateFileSystemProtection and echoed on the FileSystemDescription.
	FileSystemProtection *EFSFileSystemProtection `json:"FileSystemProtection,omitempty"`
	Tags                 []EFSTag                 `json:"Tags,omitempty"`
}

// EFSFileSystemProtection mirrors the wire FileSystemProtectionDescription the
// SDK reads from both UpdateFileSystemProtection and the FileSystemDescription.
type EFSFileSystemProtection struct {
	ReplicationOverwriteProtection string `json:"ReplicationOverwriteProtection"`
}

type EFSTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type EFSMountTarget struct {
	MountTargetId        string   `json:"MountTargetId"`
	FileSystemId         string   `json:"FileSystemId"`
	SubnetId             string   `json:"SubnetId"`
	VpcId                string   `json:"VpcId,omitempty"`
	IpAddress            string   `json:"IpAddress"`
	LifeCycleState       string   `json:"LifeCycleState"`
	NetworkInterfaceId   string   `json:"NetworkInterfaceId"`
	AvailabilityZoneId   string   `json:"AvailabilityZoneId"`
	AvailabilityZoneName string   `json:"AvailabilityZoneName"`
	OwnerId              string   `json:"OwnerId"`
	SecurityGroups       []string `json:"-"`
}

type EFSAccessPoint struct {
	AccessPointId  string            `json:"AccessPointId"`
	AccessPointArn string            `json:"AccessPointArn"`
	ClientToken    string            `json:"ClientToken,omitempty"`
	FileSystemId   string            `json:"FileSystemId"`
	LifeCycleState string            `json:"LifeCycleState"`
	Name           string            `json:"Name,omitempty"`
	OwnerId        string            `json:"OwnerId"`
	RootDirectory  *EFSRootDirectory `json:"RootDirectory,omitempty"`
	PosixUser      *EFSPosixUser     `json:"PosixUser,omitempty"`
	Tags           []EFSTag          `json:"Tags,omitempty"`
}

type EFSRootDirectory struct {
	Path         string           `json:"Path,omitempty"`
	CreationInfo *EFSCreationInfo `json:"CreationInfo,omitempty"`
}

type EFSCreationInfo struct {
	OwnerGid    int64  `json:"OwnerGid"`
	OwnerUid    int64  `json:"OwnerUid"`
	Permissions string `json:"Permissions"`
}

type EFSPosixUser struct {
	Gid           int64   `json:"Gid"`
	Uid           int64   `json:"Uid"`
	SecondaryGids []int64 `json:"SecondaryGids,omitempty"`
}

type EFSLifecyclePolicy struct {
	TransitionToIA                  string `json:"TransitionToIA,omitempty"`
	TransitionToPrimaryStorageClass string `json:"TransitionToPrimaryStorageClass,omitempty"`
	TransitionToArchive             string `json:"TransitionToArchive,omitempty"`
}

// EFSReplicationConfig is the stored form of a replication configuration,
// keyed by source file-system id. It mirrors the wire
// ReplicationConfigurationDescription the SDK reads back.
type EFSReplicationConfig struct {
	SourceFileSystemId          string               `json:"SourceFileSystemId"`
	SourceFileSystemRegion      string               `json:"SourceFileSystemRegion"`
	SourceFileSystemArn         string               `json:"SourceFileSystemArn"`
	OriginalSourceFileSystemArn string               `json:"OriginalSourceFileSystemArn"`
	SourceFileSystemOwnerId     string               `json:"SourceFileSystemOwnerId"`
	CreationTime                int64                `json:"CreationTime"`
	Destinations                []EFSReplicationDest `json:"Destinations"`
}

type EFSReplicationDest struct {
	Status                  string `json:"Status"`
	FileSystemId            string `json:"FileSystemId"`
	Region                  string `json:"Region"`
	LastReplicatedTimestamp int64  `json:"LastReplicatedTimestamp,omitempty"`
	OwnerId                 string `json:"OwnerId,omitempty"`
	RoleArn                 string `json:"RoleArn,omitempty"`
}

// State stores
var (
	efsFileSystems        sim.Store[EFSFileSystem]
	efsMountTargets       sim.Store[EFSMountTarget]
	efsAccessPoints       sim.Store[EFSAccessPoint]
	efsLifecyclePolicies  sim.Store[[]EFSLifecyclePolicy]
	efsFileSystemPolicies sim.Store[string]               // fsID -> JSON policy
	efsBackupPolicies     sim.Store[string]               // fsID -> backup status
	efsReplications       sim.Store[EFSReplicationConfig] // source fsID -> config
	efsAccountPref        sim.Store[string]               // account -> ResourceIdType
)

func efsArn(resourceType, id string) string {
	return fmt.Sprintf("arn:aws:elasticfilesystem:"+awsRegion()+":"+awsAccountID()+":%s/%s", resourceType, id)
}

func registerEFS(srv *sim.Server) {
	efsFileSystems = sim.MakeStore[EFSFileSystem](srv.DB(), "efs_file_systems")
	efsMountTargets = sim.MakeStore[EFSMountTarget](srv.DB(), "efs_mount_targets")
	efsAccessPoints = sim.MakeStore[EFSAccessPoint](srv.DB(), "efs_access_points")
	efsLifecyclePolicies = sim.MakeStore[[]EFSLifecyclePolicy](srv.DB(), "efs_lifecycle_policies")
	efsFileSystemPolicies = sim.MakeStore[string](srv.DB(), "efs_file_system_policies")
	efsBackupPolicies = sim.MakeStore[string](srv.DB(), "efs_backup_policies")
	efsReplications = sim.MakeStore[EFSReplicationConfig](srv.DB(), "efs_replications")
	efsAccountPref = sim.MakeStore[string](srv.DB(), "efs_account_preferences")

	mux := srv

	fsResource := cloudTrailRESTResource("AWS::EFS::FileSystem", "id")
	mtResource := cloudTrailRESTResource("AWS::EFS::MountTarget", "id")
	apResource := cloudTrailRESTResource("AWS::EFS::AccessPoint", "id")
	mux.HandleFunc("POST /2015-02-01/file-systems", cloudTrailRecordedREST("CreateFileSystem", "elasticfilesystem.amazonaws.com", nil, handleEFSCreateFileSystem))
	mux.HandleFunc("GET /2015-02-01/file-systems", cloudTrailRecordedREST("DescribeFileSystems", "elasticfilesystem.amazonaws.com", nil, handleEFSDescribeFileSystems))
	mux.HandleFunc("PUT /2015-02-01/file-systems/{id}", cloudTrailRecordedREST("UpdateFileSystem", "elasticfilesystem.amazonaws.com", fsResource, handleEFSUpdateFileSystem))
	mux.HandleFunc("PUT /2015-02-01/file-systems/{id}/protection", cloudTrailRecordedREST("UpdateFileSystemProtection", "elasticfilesystem.amazonaws.com", fsResource, handleEFSUpdateFileSystemProtection))
	mux.HandleFunc("DELETE /2015-02-01/file-systems/{id}", cloudTrailRecordedREST("DeleteFileSystem", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDeleteFileSystem))
	mux.HandleFunc("PUT /2015-02-01/file-systems/{id}/lifecycle-configuration", cloudTrailRecordedREST("PutLifecycleConfiguration", "elasticfilesystem.amazonaws.com", fsResource, handleEFSPutLifecycleConfiguration))
	mux.HandleFunc("GET /2015-02-01/file-systems/{id}/lifecycle-configuration", cloudTrailRecordedREST("DescribeLifecycleConfiguration", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDescribeLifecycleConfiguration))

	mux.HandleFunc("POST /2015-02-01/mount-targets", cloudTrailRecordedREST("CreateMountTarget", "elasticfilesystem.amazonaws.com", nil, handleEFSCreateMountTarget))
	mux.HandleFunc("GET /2015-02-01/mount-targets", cloudTrailRecordedREST("DescribeMountTargets", "elasticfilesystem.amazonaws.com", nil, handleEFSDescribeMountTargets))
	mux.HandleFunc("GET /2015-02-01/mount-targets/{id}/security-groups", cloudTrailRecordedREST("DescribeMountTargetSecurityGroups", "elasticfilesystem.amazonaws.com", mtResource, handleEFSDescribeMountTargetSecurityGroups))
	mux.HandleFunc("PUT /2015-02-01/mount-targets/{id}/security-groups", cloudTrailRecordedREST("ModifyMountTargetSecurityGroups", "elasticfilesystem.amazonaws.com", mtResource, handleEFSModifyMountTargetSecurityGroups))
	mux.HandleFunc("DELETE /2015-02-01/mount-targets/{id}", cloudTrailRecordedREST("DeleteMountTarget", "elasticfilesystem.amazonaws.com", mtResource, handleEFSDeleteMountTarget))

	mux.HandleFunc("POST /2015-02-01/access-points", cloudTrailRecordedREST("CreateAccessPoint", "elasticfilesystem.amazonaws.com", nil, handleEFSCreateAccessPoint))
	mux.HandleFunc("GET /2015-02-01/access-points", cloudTrailRecordedREST("DescribeAccessPoints", "elasticfilesystem.amazonaws.com", nil, handleEFSDescribeAccessPoints))
	mux.HandleFunc("DELETE /2015-02-01/access-points/{id}", cloudTrailRecordedREST("DeleteAccessPoint", "elasticfilesystem.amazonaws.com", apResource, handleEFSDeleteAccessPoint))

	// File system policy
	mux.HandleFunc("PUT /2015-02-01/file-systems/{id}/policy", cloudTrailRecordedREST("PutFileSystemPolicy", "elasticfilesystem.amazonaws.com", fsResource, handleEFSPutFileSystemPolicy))
	mux.HandleFunc("GET /2015-02-01/file-systems/{id}/policy", cloudTrailRecordedREST("DescribeFileSystemPolicy", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDescribeFileSystemPolicy))
	mux.HandleFunc("DELETE /2015-02-01/file-systems/{id}/policy", cloudTrailRecordedREST("DeleteFileSystemPolicy", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDeleteFileSystemPolicy))

	// Backup policy
	mux.HandleFunc("PUT /2015-02-01/file-systems/{id}/backup-policy", cloudTrailRecordedREST("PutBackupPolicy", "elasticfilesystem.amazonaws.com", fsResource, handleEFSPutBackupPolicy))
	mux.HandleFunc("GET /2015-02-01/file-systems/{id}/backup-policy", cloudTrailRecordedREST("DescribeBackupPolicy", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDescribeBackupPolicy))

	// Replication. The list route must register before the {id} route so
	// "/file-systems/replication-configurations" doesn't bind {id}.
	mux.HandleFunc("GET /2015-02-01/file-systems/replication-configurations", cloudTrailRecordedREST("DescribeReplicationConfigurations", "elasticfilesystem.amazonaws.com", nil, handleEFSDescribeReplicationConfigurations))
	mux.HandleFunc("POST /2015-02-01/file-systems/{id}/replication-configuration", cloudTrailRecordedREST("CreateReplicationConfiguration", "elasticfilesystem.amazonaws.com", fsResource, handleEFSCreateReplicationConfiguration))
	mux.HandleFunc("DELETE /2015-02-01/file-systems/{id}/replication-configuration", cloudTrailRecordedREST("DeleteReplicationConfiguration", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDeleteReplicationConfiguration))

	// Account preferences
	mux.HandleFunc("PUT /2015-02-01/account-preferences", cloudTrailRecordedREST("PutAccountPreferences", "elasticfilesystem.amazonaws.com", nil, handleEFSPutAccountPreferences))
	mux.HandleFunc("GET /2015-02-01/account-preferences", cloudTrailRecordedREST("DescribeAccountPreferences", "elasticfilesystem.amazonaws.com", nil, handleEFSDescribeAccountPreferences))

	// Resource-ARN tagging API (file systems + access points)
	apTagResource := cloudTrailRESTResource("AWS::EFS::FileSystem", "id")
	mux.HandleFunc("POST /2015-02-01/resource-tags/{id}", cloudTrailRecordedREST("TagResource", "elasticfilesystem.amazonaws.com", apTagResource, handleEFSTagResource))
	mux.HandleFunc("GET /2015-02-01/resource-tags/{id}", cloudTrailRecordedREST("ListTagsForResource", "elasticfilesystem.amazonaws.com", apTagResource, handleEFSListTagsForResource))
	mux.HandleFunc("DELETE /2015-02-01/resource-tags/{id}", cloudTrailRecordedREST("UntagResource", "elasticfilesystem.amazonaws.com", apTagResource, handleEFSUntagResource))

	// Legacy file-system tagging API
	mux.HandleFunc("POST /2015-02-01/create-tags/{id}", cloudTrailRecordedREST("CreateTags", "elasticfilesystem.amazonaws.com", fsResource, handleEFSCreateTags))
	mux.HandleFunc("GET /2015-02-01/tags/{id}", cloudTrailRecordedREST("DescribeTags", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDescribeTags))
	mux.HandleFunc("GET /2015-02-01/tags/{id}/", cloudTrailRecordedREST("DescribeTags", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDescribeTags)) // CLI appends a trailing slash
	mux.HandleFunc("POST /2015-02-01/delete-tags/{id}", cloudTrailRecordedREST("DeleteTags", "elasticfilesystem.amazonaws.com", fsResource, handleEFSDeleteTags))
}

// efsResourceTags returns a pointer-style accessor for the tags of a
// resource-tags ResourceId (a file system OR an access point id), so the
// resource-ARN tagging API can mutate whichever store owns the resource.
// Returns (getTags, setTags, true) when the id resolves; (_,_,false) otherwise.
func efsResourceTags(id string) (func() []EFSTag, func([]EFSTag), bool) {
	if _, ok := efsFileSystems.Get(id); ok {
		get := func() []EFSTag {
			fs, _ := efsFileSystems.Get(id)
			return fs.Tags
		}
		set := func(tags []EFSTag) {
			efsFileSystems.Update(id, func(fs *EFSFileSystem) { fs.Tags = tags })
		}
		return get, set, true
	}
	if _, ok := efsAccessPoints.Get(id); ok {
		get := func() []EFSTag {
			ap, _ := efsAccessPoints.Get(id)
			return ap.Tags
		}
		set := func(tags []EFSTag) {
			efsAccessPoints.Update(id, func(ap *EFSAccessPoint) { ap.Tags = tags })
		}
		return get, set, true
	}
	return nil, nil, false
}

// efsMergeTags upserts the given tags into the existing slice by key (real EFS
// TagResource/CreateTags overwrite the value of an existing key).
func efsMergeTags(existing, incoming []EFSTag) []EFSTag {
	idx := map[string]int{}
	out := append([]EFSTag(nil), existing...)
	for i, t := range out {
		idx[t.Key] = i
	}
	for _, t := range incoming {
		if i, ok := idx[t.Key]; ok {
			out[i].Value = t.Value
		} else {
			idx[t.Key] = len(out)
			out = append(out, t)
		}
	}
	return out
}

func handleEFSPutFileSystemPolicy(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	var req struct {
		Policy                         string `json:"Policy"`
		BypassPolicyLockoutSafetyCheck bool   `json:"BypassPolicyLockoutSafetyCheck"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Policy == "" {
		AWSError(w, "InvalidPolicyException", "Policy is required", http.StatusBadRequest)
		return
	}
	efsFileSystemPolicies.Put(fsId, req.Policy)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"FileSystemId": fsId,
		"Policy":       req.Policy,
	})
}

func handleEFSDescribeFileSystemPolicy(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	policy, ok := efsFileSystemPolicies.Get(fsId)
	if !ok {
		AWSErrorf(w, "PolicyNotFound", http.StatusNotFound,
			"Policy not found for file system '%s'", fsId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"FileSystemId": fsId,
		"Policy":       policy,
	})
}

func handleEFSDeleteFileSystemPolicy(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	efsFileSystemPolicies.Delete(fsId)
	w.WriteHeader(http.StatusOK)
}

func handleEFSPutBackupPolicy(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	var req struct {
		BackupPolicy struct {
			Status string `json:"Status"`
		} `json:"BackupPolicy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.BackupPolicy.Status == "" {
		AWSError(w, "BadRequest", "BackupPolicy.Status is required", http.StatusBadRequest)
		return
	}
	efsBackupPolicies.Put(fsId, req.BackupPolicy.Status)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"BackupPolicy": map[string]any{"Status": req.BackupPolicy.Status},
	})
}

func handleEFSDescribeBackupPolicy(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	status, ok := efsBackupPolicies.Get(fsId)
	if !ok {
		// EFS returns DISABLED when no policy has been set on the file system.
		status = "DISABLED"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"BackupPolicy": map[string]any{"Status": status},
	})
}

func handleEFSCreateReplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	srcId := sim.PathParam(r, "id")
	src, ok := efsFileSystems.Get(srcId)
	if !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", srcId)
		return
	}
	if _, exists := efsReplications.Get(srcId); exists {
		AWSErrorf(w, "ReplicationAlreadyExists", http.StatusConflict,
			"File system '%s' already has a replication configuration", srcId)
		return
	}
	var req struct {
		Destinations []struct {
			Region               string `json:"Region"`
			AvailabilityZoneName string `json:"AvailabilityZoneName"`
			KmsKeyId             string `json:"KmsKeyId"`
			FileSystemId         string `json:"FileSystemId"`
			RoleArn              string `json:"RoleArn"`
		} `json:"Destinations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Destinations) == 0 {
		AWSError(w, "BadRequest", "Destinations is required", http.StatusBadRequest)
		return
	}

	now := time.Now().Unix()
	var dests []EFSReplicationDest
	for _, d := range req.Destinations {
		region := d.Region
		if region == "" {
			region = awsRegion()
		}
		destFsId := d.FileSystemId
		if destFsId == "" {
			// No destination specified: EFS creates a new destination file system.
			destFsId = "fs-" + generateUUID()[:8]
			destFs := EFSFileSystem{
				FileSystemId:    destFsId,
				FileSystemArn:   efsArn("file-system", destFsId),
				CreationToken:   generateUUID(),
				CreationTime:    now,
				LifeCycleState:  "available",
				OwnerId:         awsAccountID(),
				PerformanceMode: "generalPurpose",
				ThroughputMode:  "bursting",
			}
			efsFileSystems.Put(destFsId, destFs)
		}
		dests = append(dests, EFSReplicationDest{
			Status:       "ENABLED",
			FileSystemId: destFsId,
			Region:       region,
			OwnerId:      awsAccountID(),
			RoleArn:      d.RoleArn,
		})
	}

	cfg := EFSReplicationConfig{
		SourceFileSystemId:          srcId,
		SourceFileSystemRegion:      awsRegion(),
		SourceFileSystemArn:         src.FileSystemArn,
		OriginalSourceFileSystemArn: src.FileSystemArn,
		SourceFileSystemOwnerId:     awsAccountID(),
		CreationTime:                now,
		Destinations:                dests,
	}
	efsReplications.Put(srcId, cfg)
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleEFSDescribeReplicationConfigurations(w http.ResponseWriter, r *http.Request) {
	fsId := r.URL.Query().Get("FileSystemId")
	var reps []EFSReplicationConfig
	if fsId != "" {
		// EFS matches when the id is either the source or a destination.
		for _, c := range efsReplications.List() {
			if c.SourceFileSystemId == fsId {
				reps = append(reps, c)
				continue
			}
			for _, d := range c.Destinations {
				if d.FileSystemId == fsId {
					reps = append(reps, c)
					break
				}
			}
		}
	} else {
		reps = efsReplications.List()
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].SourceFileSystemId < reps[j].SourceFileSystemId })
	if reps == nil {
		reps = []EFSReplicationConfig{}
	}
	page, next := awsPageExplicit(reps, r.URL.Query().Get("NextToken"), atoiDefault(r.URL.Query().Get("MaxResults"), 0))
	resp := map[string]any{"Replications": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEFSDeleteReplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	srcId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(srcId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", srcId)
		return
	}
	if !efsReplications.Delete(srcId) {
		AWSErrorf(w, "ReplicationNotFound", http.StatusNotFound,
			"File system '%s' does not have a replication configuration", srcId)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleEFSPutAccountPreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceIdType string `json:"ResourceIdType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceIdType != "LONG_ID" && req.ResourceIdType != "SHORT_ID" {
		AWSError(w, "BadRequest", "ResourceIdType must be LONG_ID or SHORT_ID", http.StatusBadRequest)
		return
	}
	efsAccountPref.Put(awsAccountID(), req.ResourceIdType)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ResourceIdPreference": map[string]any{
			"ResourceIdType": req.ResourceIdType,
			"Resources":      []string{"FILE_SYSTEM", "MOUNT_TARGET"},
		},
	})
}

func handleEFSDescribeAccountPreferences(w http.ResponseWriter, r *http.Request) {
	idType, ok := efsAccountPref.Get(awsAccountID())
	if !ok {
		idType = "LONG_ID"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ResourceIdPreference": map[string]any{
			"ResourceIdType": idType,
			"Resources":      []string{"FILE_SYSTEM", "MOUNT_TARGET"},
		},
	})
}

func handleEFSTagResource(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "id")
	get, set, ok := efsResourceTags(id)
	if !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"Resource '%s' does not exist", id)
		return
	}
	var req struct {
		Tags []EFSTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	set(efsMergeTags(get(), req.Tags))
	w.WriteHeader(http.StatusOK)
}

func handleEFSListTagsForResource(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "id")
	get, _, ok := efsResourceTags(id)
	if !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"Resource '%s' does not exist", id)
		return
	}
	tags := get()
	if tags == nil {
		tags = []EFSTag{}
	}
	page, next := awsPageExplicit(tags, r.URL.Query().Get("NextToken"), atoiDefault(r.URL.Query().Get("MaxResults"), 0))
	resp := map[string]any{"Tags": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEFSUntagResource(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "id")
	get, set, ok := efsResourceTags(id)
	if !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"Resource '%s' does not exist", id)
		return
	}
	remove := map[string]bool{}
	for _, k := range r.URL.Query()["tagKeys"] {
		remove[k] = true
	}
	var kept []EFSTag
	for _, t := range get() {
		if !remove[t.Key] {
			kept = append(kept, t)
		}
	}
	set(kept)
	w.WriteHeader(http.StatusOK)
}

func handleEFSCreateTags(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	var req struct {
		Tags []EFSTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	efsFileSystems.Update(fsId, func(fs *EFSFileSystem) {
		fs.Tags = efsMergeTags(fs.Tags, req.Tags)
	})
	w.WriteHeader(http.StatusNoContent)
}

func handleEFSDescribeTags(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	fs, ok := efsFileSystems.Get(fsId)
	if !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	tags := fs.Tags
	if tags == nil {
		tags = []EFSTag{}
	}
	page, next := awsPageExplicit(tags, r.URL.Query().Get("Marker"), atoiDefault(r.URL.Query().Get("MaxItems"), 0))
	resp := map[string]any{"Tags": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEFSDeleteTags(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	var req struct {
		TagKeys []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	remove := map[string]bool{}
	for _, k := range req.TagKeys {
		remove[k] = true
	}
	efsFileSystems.Update(fsId, func(fs *EFSFileSystem) {
		var kept []EFSTag
		for _, t := range fs.Tags {
			if !remove[t.Key] {
				kept = append(kept, t)
			}
		}
		fs.Tags = kept
	})
	w.WriteHeader(http.StatusNoContent)
}

func handleEFSCreateFileSystem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CreationToken   string   `json:"CreationToken"`
		PerformanceMode string   `json:"PerformanceMode"`
		ThroughputMode  string   `json:"ThroughputMode"`
		Tags            []EFSTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.CreationToken == "" {
		req.CreationToken = generateUUID()
	}
	if req.PerformanceMode == "" {
		req.PerformanceMode = "generalPurpose"
	}
	if req.ThroughputMode == "" {
		req.ThroughputMode = "bursting"
	}

	fsId := "fs-" + generateUUID()[:8]

	// Extract name from tags
	var name string
	for _, tag := range req.Tags {
		if tag.Key == "Name" {
			name = tag.Value
		}
	}

	fs := EFSFileSystem{
		FileSystemId:    fsId,
		FileSystemArn:   efsArn("file-system", fsId),
		CreationToken:   req.CreationToken,
		CreationTime:    time.Now().Unix(),
		LifeCycleState:  "available",
		Name:            name,
		OwnerId:         awsAccountID(),
		PerformanceMode: req.PerformanceMode,
		ThroughputMode:  req.ThroughputMode,
		Tags:            req.Tags,
	}
	efsFileSystems.Put(fsId, fs)

	sim.WriteJSON(w, http.StatusCreated, fs)
}

func handleEFSDescribeFileSystems(w http.ResponseWriter, r *http.Request) {
	fsId := r.URL.Query().Get("FileSystemId")
	creationToken := r.URL.Query().Get("CreationToken")

	var fileSystems []EFSFileSystem
	if fsId != "" {
		fs, ok := efsFileSystems.Get(fsId)
		if ok {
			// Update mount target count
			count := 0
			for _, mt := range efsMountTargets.List() {
				if mt.FileSystemId == fsId {
					count++
				}
			}
			fs.NumberOfMountTargets = count
			fileSystems = append(fileSystems, fs)
		}
	} else {
		fileSystems = efsFileSystems.List()
		sort.Slice(fileSystems, func(i, j int) bool { return fileSystems[i].FileSystemId < fileSystems[j].FileSystemId })
		// Refresh each file system's live mount-target count so the list path
		// matches the by-id path (real EFS always reports the current count).
		for i := range fileSystems {
			count := 0
			for _, mt := range efsMountTargets.List() {
				if mt.FileSystemId == fileSystems[i].FileSystemId {
					count++
				}
			}
			fileSystems[i].NumberOfMountTargets = count
		}
	}
	if creationToken != "" {
		var f []EFSFileSystem
		for _, fs := range fileSystems {
			if fs.CreationToken == creationToken {
				f = append(f, fs)
			}
		}
		fileSystems = f
	}
	if fileSystems == nil {
		fileSystems = []EFSFileSystem{}
	}

	page, next := awsPageExplicit(fileSystems, r.URL.Query().Get("Marker"), atoiDefault(r.URL.Query().Get("MaxItems"), 0))
	resp := map[string]any{"FileSystems": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEFSDeleteFileSystem(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "id")
	if !efsFileSystems.Delete(id) {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", id)
		return
	}
	efsLifecyclePolicies.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleEFSUpdateFileSystem updates the throughput mode and/or provisioned
// throughput of an existing file system, returning the full
// FileSystemDescription with the live mount-target count (HTTP 202, matching
// real EFS).
func handleEFSUpdateFileSystem(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	var req struct {
		ThroughputMode               string   `json:"ThroughputMode"`
		ProvisionedThroughputInMibps *float64 `json:"ProvisionedThroughputInMibps"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ThroughputMode == "provisioned" && req.ProvisionedThroughputInMibps == nil {
		AWSError(w, "BadRequest",
			"ProvisionedThroughputInMibps is required when ThroughputMode is provisioned",
			http.StatusBadRequest)
		return
	}
	efsFileSystems.Update(fsId, func(fs *EFSFileSystem) {
		if req.ThroughputMode != "" {
			fs.ThroughputMode = req.ThroughputMode
		}
		if fs.ThroughputMode == "provisioned" {
			fs.ProvisionedThroughputInMibps = req.ProvisionedThroughputInMibps
		} else {
			// Leaving provisioned mode clears the provisioned value, as real EFS does.
			fs.ProvisionedThroughputInMibps = nil
		}
	})
	fs, _ := efsFileSystems.Get(fsId)
	count := 0
	for _, mt := range efsMountTargets.List() {
		if mt.FileSystemId == fsId {
			count++
		}
	}
	fs.NumberOfMountTargets = count
	sim.WriteJSON(w, http.StatusAccepted, fs)
}

// handleEFSUpdateFileSystemProtection sets the file system's
// ReplicationOverwriteProtection and returns the FileSystemProtectionDescription.
func handleEFSUpdateFileSystemProtection(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}
	var req struct {
		ReplicationOverwriteProtection string `json:"ReplicationOverwriteProtection"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	status := req.ReplicationOverwriteProtection
	if status != "ENABLED" && status != "DISABLED" {
		AWSError(w, "BadRequest",
			"ReplicationOverwriteProtection must be ENABLED or DISABLED", http.StatusBadRequest)
		return
	}
	efsFileSystems.Update(fsId, func(fs *EFSFileSystem) {
		fs.FileSystemProtection = &EFSFileSystemProtection{ReplicationOverwriteProtection: status}
	})
	sim.WriteJSON(w, http.StatusOK, EFSFileSystemProtection{ReplicationOverwriteProtection: status})
}

func handleEFSPutLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}

	var req struct {
		LifecyclePolicies []EFSLifecyclePolicy `json:"LifecyclePolicies"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.LifecyclePolicies == nil {
		req.LifecyclePolicies = []EFSLifecyclePolicy{}
	}
	efsLifecyclePolicies.Put(fsId, req.LifecyclePolicies)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"LifecyclePolicies": req.LifecyclePolicies,
	})
}

func handleEFSDescribeLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	fsId := sim.PathParam(r, "id")
	if _, ok := efsFileSystems.Get(fsId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", fsId)
		return
	}

	policies, ok := efsLifecyclePolicies.Get(fsId)
	if !ok {
		policies = []EFSLifecyclePolicy{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"LifecyclePolicies": policies,
	})
}

func handleEFSCreateMountTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FileSystemId   string   `json:"FileSystemId"`
		SubnetId       string   `json:"SubnetId"`
		IpAddress      string   `json:"IpAddress"`
		SecurityGroups []string `json:"SecurityGroups"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.FileSystemId == "" || req.SubnetId == "" {
		AWSError(w, "BadRequest", "FileSystemId and SubnetId are required", http.StatusBadRequest)
		return
	}

	if _, ok := efsFileSystems.Get(req.FileSystemId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", req.FileSystemId)
		return
	}

	if req.IpAddress == "" {
		// Real EFS allocates the mount target IP from the subnet's CIDR
		// block — not from a global counter. Match that contract.
		ip, ipErr := AllocateSubnetIP(req.SubnetId)
		if ipErr != nil {
			AWSError(w, "SubnetNotFound", ipErr.Error(), http.StatusBadRequest)
			return
		}
		req.IpAddress = ip
	}

	mtId := "fsmt-" + generateUUID()[:8]
	vpcId := ""
	if sn, ok := ec2Subnets.Get(req.SubnetId); ok {
		vpcId = sn.VpcId
	}
	mt := EFSMountTarget{
		MountTargetId:        mtId,
		FileSystemId:         req.FileSystemId,
		SubnetId:             req.SubnetId,
		VpcId:                vpcId,
		IpAddress:            req.IpAddress,
		LifeCycleState:       "available",
		NetworkInterfaceId:   "eni-" + generateUUID()[:8],
		AvailabilityZoneId:   "use1-az1",
		AvailabilityZoneName: awsAvailabilityZone(),
		OwnerId:              awsAccountID(),
		SecurityGroups:       req.SecurityGroups,
	}
	efsMountTargets.Put(mtId, mt)

	sim.WriteJSON(w, http.StatusOK, mt)
}

// efsDescribeList renders the common EFS describe-list shape: optional
// single-id lookup, else optional FileSystemId filter, else list-all; a
// deterministic sort; explicit-page-size pagination; a token-keyed body.
// Shared by DescribeMountTargets and DescribeAccessPoints (which differ only
// in element type, id field, and the marker/token query+response key names).
func efsDescribeList[T any](
	r *http.Request,
	store sim.Store[T],
	idParam, fsParam string,
	matchesFS func(item T, fsID string) bool,
	less func(a, b T) bool,
	markerParam, maxParam, listKey, tokenKey string,
) map[string]any {
	var items []T
	if id := r.URL.Query().Get(idParam); id != "" {
		if it, ok := store.Get(id); ok {
			items = append(items, it)
		}
	} else if fsID := r.URL.Query().Get(fsParam); fsID != "" {
		items = store.Filter(func(it T) bool { return matchesFS(it, fsID) })
		sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })
	} else {
		items = store.List()
		sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })
	}
	if items == nil {
		items = []T{}
	}
	page, next := awsPageExplicit(items, r.URL.Query().Get(markerParam), atoiDefault(r.URL.Query().Get(maxParam), 0))
	resp := map[string]any{listKey: page}
	if next != "" {
		resp[tokenKey] = next
	}
	return resp
}

func handleEFSDescribeMountTargets(w http.ResponseWriter, r *http.Request) {
	resp := efsDescribeList(r, efsMountTargets, "MountTargetId", "FileSystemId",
		func(mt EFSMountTarget, fsID string) bool { return mt.FileSystemId == fsID },
		func(a, b EFSMountTarget) bool { return a.MountTargetId < b.MountTargetId },
		"Marker", "MaxItems", "MountTargets", "NextMarker")
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEFSCreateAccessPoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FileSystemId  string            `json:"FileSystemId"`
		ClientToken   string            `json:"ClientToken"`
		PosixUser     *EFSPosixUser     `json:"PosixUser"`
		RootDirectory *EFSRootDirectory `json:"RootDirectory"`
		Tags          []EFSTag          `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.FileSystemId == "" {
		AWSError(w, "BadRequest", "FileSystemId is required", http.StatusBadRequest)
		return
	}

	if _, ok := efsFileSystems.Get(req.FileSystemId); !ok {
		AWSErrorf(w, "FileSystemNotFound", http.StatusNotFound,
			"File system '%s' does not exist", req.FileSystemId)
		return
	}

	apId := "fsap-" + generateUUID()[:8]

	var name string
	for _, tag := range req.Tags {
		if tag.Key == "Name" {
			name = tag.Value
		}
	}

	ap := EFSAccessPoint{
		AccessPointId:  apId,
		AccessPointArn: efsArn("access-point", apId),
		ClientToken:    req.ClientToken,
		FileSystemId:   req.FileSystemId,
		LifeCycleState: "available",
		Name:           name,
		OwnerId:        awsAccountID(),
		RootDirectory:  req.RootDirectory,
		PosixUser:      req.PosixUser,
		Tags:           req.Tags,
	}
	efsAccessPoints.Put(apId, ap)

	// Pre-create the host-side directory so the ECS task runner can
	// bind-mount it directly without racing on first use.
	_ = EFSAccessPointHostDir(apId)

	sim.WriteJSON(w, http.StatusOK, ap)
}

func handleEFSDescribeAccessPoints(w http.ResponseWriter, r *http.Request) {
	resp := efsDescribeList(r, efsAccessPoints, "AccessPointId", "FileSystemId",
		func(ap EFSAccessPoint, fsID string) bool { return ap.FileSystemId == fsID },
		func(a, b EFSAccessPoint) bool { return a.AccessPointId < b.AccessPointId },
		"NextToken", "MaxResults", "AccessPoints", "NextToken")
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEFSDeleteAccessPoint(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "id")
	if id == "" {
		// Try extracting from path manually
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) > 0 {
			id = parts[len(parts)-1]
		}
	}
	if !efsAccessPoints.Delete(id) {
		AWSErrorf(w, "AccessPointNotFound", http.StatusNotFound,
			"Access point '%s' does not exist", id)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleEFSDescribeMountTargetSecurityGroups(w http.ResponseWriter, r *http.Request) {
	mtId := sim.PathParam(r, "id")
	mt, ok := efsMountTargets.Get(mtId)
	if !ok {
		AWSErrorf(w, "MountTargetNotFound", http.StatusNotFound,
			"Mount target '%s' does not exist", mtId)
		return
	}

	sgs := mt.SecurityGroups
	if sgs == nil {
		sgs = []string{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"SecurityGroups": sgs,
	})
}

func handleEFSModifyMountTargetSecurityGroups(w http.ResponseWriter, r *http.Request) {
	mtId := sim.PathParam(r, "id")
	if _, ok := efsMountTargets.Get(mtId); !ok {
		AWSErrorf(w, "MountTargetNotFound", http.StatusNotFound,
			"Mount target '%s' does not exist", mtId)
		return
	}

	var req struct {
		SecurityGroups []string `json:"SecurityGroups"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequest", "Invalid request body", http.StatusBadRequest)
		return
	}

	efsMountTargets.Update(mtId, func(mt *EFSMountTarget) {
		mt.SecurityGroups = req.SecurityGroups
	})

	w.WriteHeader(http.StatusNoContent)
}

func handleEFSDeleteMountTarget(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "id")
	if !efsMountTargets.Delete(id) {
		AWSErrorf(w, "MountTargetNotFound", http.StatusNotFound,
			"Mount target '%s' does not exist", id)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
