package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Memorystore for Redis v1. Real API:
// https://redis.googleapis.com/$discovery/rest?version=v1
// Each instance and cluster runs a real Redis engine; see
// memorystore_redis_engine.go.

type MSRedisInstance struct {
	Name              string            `json:"name"` // projects/{p}/locations/{loc}/instances/{id}
	DisplayName       string            `json:"displayName,omitempty"`
	Tier              string            `json:"tier,omitempty"`
	RedisVersion      string            `json:"redisVersion,omitempty"`
	MemorySizeGb      int               `json:"memorySizeGb,omitempty"`
	Host              string            `json:"host,omitempty"`
	Port              int               `json:"port,omitempty"`
	ReplicaCount      int               `json:"replicaCount,omitempty"`
	ReadReplicasMode  string            `json:"readReplicasMode,omitempty"`
	ReadEndpoint      string            `json:"readEndpoint,omitempty"`
	ReadEndpointPort  int               `json:"readEndpointPort,omitempty"`
	State             string            `json:"state,omitempty"`
	CreateTime        string            `json:"createTime,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	AuthorizedNetwork string            `json:"authorizedNetwork,omitempty"`
	AuthEnabled       bool              `json:"authEnabled,omitempty"`
	RedisConfigs      map[string]string `json:"redisConfigs,omitempty"`
	// connectMode + transitEncryptionMode have provider defaults; the read-back
	// must echo them or terraform-provider-google plans a replacement.
	ConnectMode           string `json:"connectMode,omitempty"`
	TransitEncryptionMode string `json:"transitEncryptionMode,omitempty"`
	// MaintenanceSchedule is output-only: the service reports the upcoming
	// window, and rescheduleMaintenance moves it.
	MaintenanceSchedule map[string]any            `json:"maintenanceSchedule,omitempty"`
	PersistenceConfig   *MSRedisPersistenceConfig `json:"persistenceConfig,omitempty"`
	ServerCaCerts       []MSRedisTLSCertificate   `json:"serverCaCerts,omitempty"`
}

var msRedisInstances sim.Store[MSRedisInstance]

func registerMemorystoreRedis(srv *sim.Server) {
	msRedisInstances = sim.MakeStore[MSRedisInstance](srv.DB(), "memorystore_redis")
	msRedisPlaneRecords = sim.MakeStore[msRedisPlaneRecord](srv.DB(), "memorystore_redis_data_planes")
	msRedisCAs = sim.MakeStore[msRedisCertificateAuthority](srv.DB(), "memorystore_redis_certificate_authorities")
	mode, err := sim.ResolveRuntimeMode()
	if err != nil {
		log.Fatalf("Memorystore for Redis: %v", err)
	}
	msRedisEnginesEnabled = mode.ExecutesWorkloads()

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/instances", handleMSRedisCreate)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances/{id}", handleMSRedisGet)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances", handleMSRedisList)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/instances/{id}", handleMSRedisPatch)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/instances/{id}", handleMSRedisDelete)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances/{id}/authString", handleMSRedisAuthString)
	// Go's ServeMux cannot spell `{id}:upgrade`; one wildcard captures the
	// custom method and the handler splits it.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/instances/{idAction}", handleMSRedisAction)

	registerMemorystoreRedisClusters(srv)

	// A persistent control-plane restart rebinds every endpoint and re-adopts
	// the engine containers the earlier process left running. The API-only
	// tier runs no engines.
	if msRedisRunsEngines() {
		if err := msRedisRecoverPlanes(); err != nil {
			log.Fatalf("recover Memorystore for Redis data planes: %v", err)
		}
	}
}

// Memorystore for Redis Cluster: sharded clusters, ACL policies, token-auth
// users and backups. A mutating method does its work inside the request and
// returns the finished operation.

// MSRedisCluster mirrors google.cloud.redis.cluster.v1.Cluster — only the fields the
// Discovery schema declares (the runtime spec-validator rejects any member
// not defined by the Cluster schema).
type MSRedisCluster struct {
	Name                      string                           `json:"name"`
	CreateTime                string                           `json:"createTime,omitempty"`
	State                     string                           `json:"state,omitempty"`
	Uid                       string                           `json:"uid,omitempty"`
	ReplicaCount              int                              `json:"replicaCount,omitempty"`
	AuthorizationMode         string                           `json:"authorizationMode,omitempty"`
	TransitEncryptionMode     string                           `json:"transitEncryptionMode,omitempty"`
	SizeGb                    int                              `json:"sizeGb,omitempty"`
	ShardCount                int                              `json:"shardCount,omitempty"`
	DiscoveryEndpoints        []MSRedisDiscoveryEndpoint       `json:"discoveryEndpoints,omitempty"`
	NodeType                  string                           `json:"nodeType,omitempty"`
	PreciseSizeGb             float64                          `json:"preciseSizeGb,omitempty"`
	RedisConfigs              map[string]string                `json:"redisConfigs,omitempty"`
	DeletionProtectionEnabled bool                             `json:"deletionProtectionEnabled,omitempty"`
	Labels                    map[string]string                `json:"labels,omitempty"`
	BackupCollection          string                           `json:"backupCollection,omitempty"`
	AclPolicy                 string                           `json:"aclPolicy,omitempty"`
	ServerCaMode              string                           `json:"serverCaMode,omitempty"`
	PscConfigs                []map[string]any                 `json:"pscConfigs,omitempty"`
	PersistenceConfig         *MSRedisClusterPersistenceConfig `json:"persistenceConfig,omitempty"`
}

// MSRedisGcsBackupSource mirrors google.cloud.redis.cluster.v1.GcsBackupSource.
type MSRedisGcsBackupSource struct {
	Uris []string `json:"uris,omitempty"`
}

// MSRedisManagedBackupSource mirrors
// google.cloud.redis.cluster.v1.ManagedBackupSource.
type MSRedisManagedBackupSource struct {
	Backup string `json:"backup,omitempty"`
}

// msRedisClusterCreateRequest is a cluster create's body: the cluster, and
// the input-only sources it imports its data from.
type msRedisClusterCreateRequest struct {
	MSRedisCluster
	GcsSource           *MSRedisGcsBackupSource     `json:"gcsSource,omitempty"`
	ManagedBackupSource *MSRedisManagedBackupSource `json:"managedBackupSource,omitempty"`
}

// MSRedisDiscoveryEndpoint mirrors google.cloud.redis.cluster.v1.DiscoveryEndpoint.
type MSRedisDiscoveryEndpoint struct {
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// MSRedisBackupCollection mirrors google.cloud.redis.cluster.v1.BackupCollection.
type MSRedisBackupCollection struct {
	Name                 string `json:"name"`
	ClusterUid           string `json:"clusterUid,omitempty"`
	Cluster              string `json:"cluster,omitempty"`
	Uid                  string `json:"uid,omitempty"`
	CreateTime           string `json:"createTime,omitempty"`
	TotalBackupSizeBytes string `json:"totalBackupSizeBytes,omitempty"`
	TotalBackupCount     string `json:"totalBackupCount,omitempty"`
	LastBackupTime       string `json:"lastBackupTime,omitempty"`
}

// MSRedisBackup mirrors google.cloud.redis.cluster.v1.Backup.
type MSRedisBackup struct {
	Name           string              `json:"name"`
	CreateTime     string              `json:"createTime,omitempty"`
	Cluster        string              `json:"cluster,omitempty"`
	ClusterUid     string              `json:"clusterUid,omitempty"`
	TotalSizeBytes string              `json:"totalSizeBytes,omitempty"`
	ExpireTime     string              `json:"expireTime,omitempty"`
	EngineVersion  string              `json:"engineVersion,omitempty"`
	BackupFiles    []MSRedisBackupFile `json:"backupFiles,omitempty"`
	NodeType       string              `json:"nodeType,omitempty"`
	ReplicaCount   int                 `json:"replicaCount,omitempty"`
	ShardCount     int                 `json:"shardCount,omitempty"`
	BackupType     string              `json:"backupType,omitempty"`
	State          string              `json:"state,omitempty"`
	Uid            string              `json:"uid,omitempty"`
}

// MSRedisBackupFile mirrors google.cloud.redis.cluster.v1.BackupFile.
type MSRedisBackupFile struct {
	FileName   string `json:"fileName,omitempty"`
	SizeBytes  string `json:"sizeBytes,omitempty"`
	CreateTime string `json:"createTime,omitempty"`
}

// MSRedisAclPolicy mirrors google.cloud.redis.cluster.v1.AclPolicy.
type MSRedisAclPolicy struct {
	Name       string           `json:"name"`
	Rules      []MSRedisAclRule `json:"rules,omitempty"`
	State      string           `json:"state,omitempty"`
	Version    string           `json:"version,omitempty"`
	Etag       string           `json:"etag,omitempty"`
	CreateTime string           `json:"createTime,omitempty"`
	UpdateTime string           `json:"updateTime,omitempty"`
}

type MSRedisAclPolicyRevision struct {
	Name             string           `json:"name"`
	RevisionNumber   string           `json:"revisionNumber"`
	CreateTime       string           `json:"createTime"`
	Snapshot         MSRedisAclPolicy `json:"snapshot"`
	AttachedClusters []string         `json:"attachedClusters,omitempty"`
}

// MSRedisAclRule mirrors google.cloud.redis.cluster.v1.AclRule.
type MSRedisAclRule struct {
	Username string `json:"username,omitempty"`
	Rule     string `json:"rule,omitempty"`
}

// MSRedisTokenAuthUser mirrors google.cloud.redis.cluster.v1.TokenAuthUser.
type MSRedisTokenAuthUser struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

// MSRedisAuthToken mirrors google.cloud.redis.cluster.v1.AuthToken.
type MSRedisAuthToken struct {
	Name       string `json:"name"`
	Token      string `json:"token,omitempty"`
	CreateTime string `json:"createTime,omitempty"`
	State      string `json:"state,omitempty"`
}

var (
	msRedisClusters       sim.Store[MSRedisCluster]
	msRedisBackupColls    sim.Store[MSRedisBackupCollection]
	msRedisBackups        sim.Store[MSRedisBackup]
	msRedisAclPolicies    sim.Store[MSRedisAclPolicy]
	msRedisAclRevisions   sim.Store[MSRedisAclPolicyRevision]
	msRedisTokenAuthUsers sim.Store[MSRedisTokenAuthUser]
	msRedisAuthTokens     sim.Store[MSRedisAuthToken]
)

const msRedisClusterType = "type.googleapis.com/google.cloud.redis.cluster.v1.Cluster"

// The redis v1 document carries two OperationMetadata messages:
// GoogleCloudRedisV1OperationMetadata, the google.cloud.redis.v1 message the
// instance methods declare (statusDetail, cancelRequested), and
// OperationMetadata, the google.cloud.redis.cluster.v1 message of the
// Memorystore for Redis Cluster methods (statusMessage, requestedCancellation).
func redisInstanceLRO(r *http.Request, project, location, target string, resource any, typeName string) Operation {
	return newLRO(project, location, resource, typeName,
		gcpStandardOperationMetadata("type.googleapis.com/google.cloud.redis.v1.OperationMetadata", gcpOperationVerb(r), target))
}

func redisClusterLRO(r *http.Request, project, location, target string, resource any, typeName string) Operation {
	return newLRO(project, location, resource, typeName,
		gcpStandardOperationMetadata("type.googleapis.com/google.cloud.redis.cluster.v1.OperationMetadata", gcpOperationVerb(r), target))
}

func registerMemorystoreRedisClusters(srv *sim.Server) {
	msRedisClusters = sim.MakeStore[MSRedisCluster](srv.DB(), "memorystore_redis_clusters")
	msRedisBackupColls = sim.MakeStore[MSRedisBackupCollection](srv.DB(), "memorystore_redis_backup_collections")
	msRedisBackups = sim.MakeStore[MSRedisBackup](srv.DB(), "memorystore_redis_backups")
	msRedisAclPolicies = sim.MakeStore[MSRedisAclPolicy](srv.DB(), "memorystore_redis_acl_policies")
	msRedisAclRevisions = sim.MakeStore[MSRedisAclPolicyRevision](srv.DB(), "memorystore_redis_acl_policy_revisions")
	msRedisTokenAuthUsers = sim.MakeStore[MSRedisTokenAuthUser](srv.DB(), "memorystore_redis_token_auth_users")
	msRedisAuthTokens = sim.MakeStore[MSRedisAuthToken](srv.DB(), "memorystore_redis_auth_tokens")
	msRedisBackupContents = sim.MakeStore[msRedisBackupContent](srv.DB(), "memorystore_redis_backup_contents")
	msRedisOpenBackupPayloads(srv)

	base := "/v1/projects/{project}/locations/{location}"

	// Clusters.
	srv.HandleFunc("POST "+base+"/clusters", handleMSRedisClusterCreate)
	srv.HandleFunc("GET "+base+"/clusters", handleMSRedisClusterList)
	srv.HandleFunc("GET "+base+"/clusters/{id}", handleMSRedisClusterGet)
	srv.HandleFunc("PATCH "+base+"/clusters/{id}", handleMSRedisClusterPatch)
	srv.HandleFunc("DELETE "+base+"/clusters/{id}", handleMSRedisClusterDelete)
	// Cluster colon-verbs (:backup, :rescheduleClusterMaintenance,
	// :addTokenAuthUser) fan in on a single wildcard, same as the
	// instance surface — Go's mux can't spell `{id}:verb`.
	srv.HandleFunc("POST "+base+"/clusters/{idAction}", handleMSRedisClusterAction)
	srv.HandleFunc("GET "+base+"/clusters/{id}/certificateAuthority", handleMSRedisClusterGetCA)

	// Token-auth users + auth tokens (nested under a cluster).
	srv.HandleFunc("GET "+base+"/clusters/{id}/tokenAuthUsers", handleMSRedisTokenAuthUserList)
	srv.HandleFunc("GET "+base+"/clusters/{id}/tokenAuthUsers/{tid}", handleMSRedisTokenAuthUserGet)
	srv.HandleFunc("DELETE "+base+"/clusters/{id}/tokenAuthUsers/{tid}", handleMSRedisTokenAuthUserDelete)
	srv.HandleFunc("POST "+base+"/clusters/{id}/tokenAuthUsers/{tidAction}", handleMSRedisTokenAuthUserAction)
	srv.HandleFunc("GET "+base+"/clusters/{id}/tokenAuthUsers/{tid}/authTokens", handleMSRedisAuthTokenList)
	srv.HandleFunc("GET "+base+"/clusters/{id}/tokenAuthUsers/{tid}/authTokens/{atid}", handleMSRedisAuthTokenGet)
	srv.HandleFunc("DELETE "+base+"/clusters/{id}/tokenAuthUsers/{tid}/authTokens/{atid}", handleMSRedisAuthTokenDelete)

	// Backup collections + backups.
	srv.HandleFunc("GET "+base+"/backupCollections", handleMSRedisBackupCollectionList)
	srv.HandleFunc("GET "+base+"/backupCollections/{bc}", handleMSRedisBackupCollectionGet)
	srv.HandleFunc("GET "+base+"/backupCollections/{bc}/backups", handleMSRedisBackupList)
	srv.HandleFunc("GET "+base+"/backupCollections/{bc}/backups/{bid}", handleMSRedisBackupGet)
	srv.HandleFunc("DELETE "+base+"/backupCollections/{bc}/backups/{bid}", handleMSRedisBackupDelete)
	srv.HandleFunc("POST "+base+"/backupCollections/{bc}/backups/{bidAction}", handleMSRedisBackupAction)

	// ACL policies.
	srv.HandleFunc("POST "+base+"/aclPolicies", handleMSRedisAclPolicyCreate)
	srv.HandleFunc("GET "+base+"/aclPolicies", handleMSRedisAclPolicyList)
	srv.HandleFunc("GET "+base+"/aclPolicies/{id}", handleMSRedisAclPolicyGet)
	srv.HandleFunc("PATCH "+base+"/aclPolicies/{id}", handleMSRedisAclPolicyPatch)
	srv.HandleFunc("DELETE "+base+"/aclPolicies/{id}", handleMSRedisAclPolicyDelete)
	srv.HandleFunc("GET "+base+"/aclPolicies/{id}/revisions", handleMSRedisAclPolicyRevisionList)
	srv.HandleFunc("GET "+base+"/aclPolicies/{id}/revisions/{revision}", handleMSRedisAclPolicyRevisionGet)

	// Shared regional certificate authority.
	srv.HandleFunc("GET "+base+"/sharedRegionalCertificateAuthority", handleMSRedisSharedRegionalCA)
}

func msRedisLocationPrefix(r *http.Request, collection string) string {
	return fmt.Sprintf("projects/%s/locations/%s/%s/", sim.PathParam(r, "project"), sim.PathParam(r, "location"), collection)
}

func handleMSRedisClusterCreate(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("clusterId")
	if id == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "clusterId query parameter is required")
		return
	}
	var req msRedisClusterCreateRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, id)
	shardCount := defaultInt(req.ShardCount, 1)
	replicaCount := req.ReplicaCount
	if replicaCount < 0 || replicaCount > msRedisMaxClusterReplicas {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "replicaCount %d is outside 0-%d", replicaCount, msRedisMaxClusterReplicas)
		return
	}
	persistence, err := msRedisClusterPersistence(msRedisPersistence{}, req.PersistenceConfig, time.Now())
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	cluster := MSRedisCluster{
		Name:                      name,
		CreateTime:                nowTimestamp(),
		State:                     "ACTIVE",
		Uid:                       sim.NewUUID(),
		ReplicaCount:              replicaCount,
		AuthorizationMode:         defaultStr(req.AuthorizationMode, "AUTH_MODE_DISABLED"),
		TransitEncryptionMode:     defaultStr(req.TransitEncryptionMode, "TRANSIT_ENCRYPTION_MODE_DISABLED"),
		ShardCount:                shardCount,
		NodeType:                  defaultStr(req.NodeType, "REDIS_HIGHMEM_MEDIUM"),
		RedisConfigs:              req.RedisConfigs,
		DeletionProtectionEnabled: req.DeletionProtectionEnabled,
		Labels:                    req.Labels,
		AclPolicy:                 req.AclPolicy,
		PscConfigs:                req.PscConfigs,
		PersistenceConfig:         persistence.clusterConfig(),
	}
	msRedisClusterSize(&cluster)
	switch cluster.AuthorizationMode {
	case "AUTH_MODE_DISABLED", "AUTH_MODE_IAM_AUTH", "AUTH_MODE_TOKEN_AUTH":
	case "AUTH_MODE_UNSPECIFIED":
		cluster.AuthorizationMode = "AUTH_MODE_DISABLED"
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "authorizationMode %q is not supported", cluster.AuthorizationMode)
		return
	}
	switch cluster.TransitEncryptionMode {
	case "TRANSIT_ENCRYPTION_MODE_DISABLED", "TRANSIT_ENCRYPTION_MODE_SERVER_AUTHENTICATION":
	case "TRANSIT_ENCRYPTION_MODE_UNSPECIFIED":
		cluster.TransitEncryptionMode = "TRANSIT_ENCRYPTION_MODE_DISABLED"
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "transitEncryptionMode %q is not supported", cluster.TransitEncryptionMode)
		return
	}
	if msRedisClusterTLS(cluster) {
		switch req.ServerCaMode {
		case "", "SERVER_CA_MODE_UNSPECIFIED", "SERVER_CA_MODE_GOOGLE_MANAGED_PER_INSTANCE_CA":
			cluster.ServerCaMode = "SERVER_CA_MODE_GOOGLE_MANAGED_PER_INSTANCE_CA"
		case "SERVER_CA_MODE_GOOGLE_MANAGED_SHARED_CA":
			cluster.ServerCaMode = req.ServerCaMode
		default:
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"serverCaMode %q is not supported: this simulator serves no Certificate Authority Service CA pool", req.ServerCaMode)
			return
		}
	}
	if _, err := msRedisEngineDirectives(0, cluster.RedisConfigs); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if _, exists := msRedisClusters.Get(name); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "cluster %s already exists", name)
		return
	}
	sources, err := msRedisClusterImportSources(req.GcsSource, req.ManagedBackupSource)
	if err != nil {
		var failure msRedisOperationError
		if errors.As(err, &failure) && failure.code == rpcNotFound {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%v", err)
		} else if errors.As(err, &failure) && failure.code == rpcInvalidArgument {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		} else {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "%v", err)
		}
		return
	}
	if len(sources) > 0 && !msRedisRunsEngines() {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"no Redis engine can load the import: this simulator runs API-only")
		return
	}
	spec, err := msRedisClusterSpec(cluster)
	if err == nil && spec.CA != "" {
		_, err = msRedisEnsureCA(spec.CA)
	}
	op := redisClusterLRO(r, project, location, name, cluster, msRedisClusterType)
	if err != nil {
		sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
		return
	}
	record := msRedisPlaneRecord{}
	if err := msRedisInstallCluster(&cluster, &record, spec); err != nil {
		msRedisReleaseClusterCA(cluster)
		sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
		return
	}
	if err := msRedisStartEngine(name); err != nil {
		msRedisReleaseClusterCA(cluster)
		sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
		return
	}
	if plane, ok := msRedisLoadPlane(name); ok {
		for _, source := range sources {
			if err := plane.ImportRDB(source); err != nil {
				msRedisRemovePlane(name)
				msRedisReleaseClusterCA(cluster)
				sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
				return
			}
		}
		record = plane.record(record)
	}
	msRedisPlaneRecords.Put(name, record)
	msRedisClusters.Put(name, cluster)
	op = redisClusterLRO(r, project, location, name, cluster, msRedisClusterType)
	sim.WriteJSON(w, http.StatusOK, op)
}

// msRedisMaxClusterReplicas is the most replicas a shard runs.
const msRedisMaxClusterReplicas = 5

// msRedisClusterSize derives a cluster's memory size from its node count.
func msRedisClusterSize(c *MSRedisCluster) {
	c.SizeGb = c.ShardCount * (c.ReplicaCount + 1) * 13
	c.PreciseSizeGb = float64(c.ShardCount*(c.ReplicaCount+1)) * 13.0
}

// msRedisReleaseClusterCA forgets a cluster's own certificate authority; a
// region's shared one outlives it.
func msRedisReleaseClusterCA(cluster MSRedisCluster) {
	if msRedisClusterCA(cluster) == cluster.Name {
		msRedisReleaseCA(cluster.Name)
	}
}

func handleMSRedisClusterGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	c, ok := msRedisClusters.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleMSRedisClusterList(w http.ResponseWriter, r *http.Request) {
	prefix := msRedisLocationPrefix(r, "clusters")
	out := []MSRedisCluster{}
	for _, c := range msRedisClusters.List() {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"clusters": out})
}

func handleMSRedisClusterPatch(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, sim.PathParam(r, "id"))
	current, ok := msRedisClusters.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	var req MSRedisCluster
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	wants := msRedisUpdateMask(r)
	shards, replicas := current.ShardCount, current.ReplicaCount
	if wants("shardCount") {
		shards = req.ShardCount
	}
	if wants("replicaCount") {
		replicas = req.ReplicaCount
	}
	if shards < 1 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "shardCount %d is not a positive number of shards", shards)
		return
	}
	if replicas < 0 || replicas > msRedisMaxClusterReplicas {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "replicaCount %d is outside 0-%d", replicas, msRedisMaxClusterReplicas)
		return
	}
	redisConfigs := current.RedisConfigs
	if wants("redisConfigs") {
		redisConfigs = req.RedisConfigs
	}
	directives, err := msRedisEngineDirectives(0, redisConfigs)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	persistence := msRedisPersistenceOfCluster(current.PersistenceConfig)
	if wants("persistenceConfig") {
		if persistence, err = msRedisClusterPersistence(persistence, req.PersistenceConfig, time.Now()); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
	}
	reshape := shards != current.ShardCount || replicas != current.ReplicaCount
	if reshape {
		msRedisClusters.Update(name, func(c *MSRedisCluster) { c.State = "UPDATING" })
	}
	if plane, ok := msRedisLoadPlane(name); ok {
		plane.opMu.Lock()
		if reshape {
			err = plane.ResizeCluster(shards, replicas)
			msRedisSaveRecord(name)
		}
		if err == nil && wants("redisConfigs") {
			err = plane.Configure(directives)
		}
		if err == nil && wants("persistenceConfig") {
			err = plane.SetPersistence(persistence)
		}
		plane.opMu.Unlock()
	}
	msRedisClusters.Update(name, func(c *MSRedisCluster) {
		c.State = "ACTIVE"
		if err != nil {
			return
		}
		c.ShardCount, c.ReplicaCount = shards, replicas
		c.RedisConfigs = redisConfigs
		c.PersistenceConfig = persistence.clusterConfig()
		if wants("deletionProtectionEnabled") {
			c.DeletionProtectionEnabled = req.DeletionProtectionEnabled
		}
		if wants("labels") {
			c.Labels = req.Labels
		}
		if wants("pscConfigs") {
			c.PscConfigs = req.PscConfigs
		}
		if wants("nodeType") && req.NodeType != "" {
			c.NodeType = req.NodeType
		}
		msRedisClusterSize(c)
	})
	updated, _ := msRedisClusters.Get(name)
	op := redisClusterLRO(r, project, location, name, updated, msRedisClusterType)
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
}

// msRedisUpdateMask reads a patch's updateMask; an empty mask names every
// field the body carries.
func msRedisUpdateMask(r *http.Request) func(string) bool {
	fields := map[string]bool{}
	for _, f := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			fields[f] = true
		}
	}
	return func(name string) bool { return len(fields) == 0 || fields[name] }
}

func handleMSRedisClusterDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, sim.PathParam(r, "id"))
	cluster, ok := msRedisClusters.Get(name)
	if ok && cluster.DeletionProtectionEnabled {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"cluster %s has deletion protection enabled; disable it before deleting the cluster", name)
		return
	}
	if !ok || !msRedisClusters.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	msRedisRemovePlane(name)
	msRedisReleaseClusterCA(cluster)
	op := redisClusterLRO(r, project, location, name, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisClusterAction(w http.ResponseWriter, r *http.Request) {
	idAction := sim.PathParam(r, "idAction")
	id, action, found := strings.Cut(idAction, ":")
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on cluster %q", idAction)
		return
	}
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, id)
	cluster, ok := msRedisClusters.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	switch action {
	case "backup":
		handleMSRedisClusterBackup(w, r, project, location, id, cluster)
	case "rescheduleClusterMaintenance":
		// No maintenance window is simulated; the reschedule settles
		// synchronously and the cluster stays ACTIVE.
		op := redisClusterLRO(r, project, location, name, cluster, msRedisClusterType)
		sim.WriteJSON(w, http.StatusOK, op)
	case "addTokenAuthUser":
		handleMSRedisAddTokenAuthUser(w, r, project, location, id)
	default:
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on cluster %q", action, id)
	}
}

func handleMSRedisClusterBackup(w http.ResponseWriter, r *http.Request, project, location, clusterID string, cluster MSRedisCluster) {
	var req struct {
		Ttl      string `json:"ttl"`
		BackupID string `json:"backupId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	plane, err := msRedisEngine(cluster.Name)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "%v", err)
		return
	}
	// A manual backup materializes a BackupCollection for the cluster
	// (named after the cluster) and a Backup within it — the same shape
	// the BackupCollections/Backups read APIs enumerate.
	bcName := fmt.Sprintf("projects/%s/locations/%s/backupCollections/%s", project, location, clusterID)
	if _, ok := msRedisBackupColls.Get(bcName); !ok {
		msRedisBackupColls.Put(bcName, MSRedisBackupCollection{
			Name:       bcName,
			Cluster:    cluster.Name,
			ClusterUid: cluster.Uid,
			Uid:        sim.NewUUID(),
			CreateTime: nowTimestamp(),
		})
	}
	backupID := req.BackupID
	if backupID == "" {
		backupID = "backup-" + sim.NewUUID()
	}
	backupName := bcName + "/backups/" + backupID
	content, files, total, err := msRedisSnapshotCluster(plane)
	op := redisClusterLRO(r, project, location, cluster.Name, cluster, msRedisClusterType)
	if err != nil {
		sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
		return
	}
	backup := MSRedisBackup{
		Name:           backupName,
		CreateTime:     nowTimestamp(),
		Cluster:        cluster.Name,
		ClusterUid:     cluster.Uid,
		TotalSizeBytes: strconv.FormatInt(total, 10),
		EngineVersion:  msRedisClusterEngineVersion,
		BackupFiles:    files,
		NodeType:       cluster.NodeType,
		ReplicaCount:   cluster.ReplicaCount,
		ShardCount:     cluster.ShardCount,
		BackupType:     "ON_DEMAND",
		State:          "ACTIVE",
		Uid:            sim.NewUUID(),
	}
	msRedisReleaseBackup(backupName)
	msRedisBackupContents.Put(backupName, content)
	msRedisBackups.Put(backupName, backup)
	sim.WriteJSON(w, http.StatusOK, op)
}

// msRedisSnapshotCluster takes an RDB snapshot of every shard's primary and
// keeps each as a payload.
func msRedisSnapshotCluster(plane *msRedisPlane) (msRedisBackupContent, []MSRedisBackupFile, int64, error) {
	plane.opMu.Lock()
	defer plane.opMu.Unlock()
	var content msRedisBackupContent
	var files []MSRedisBackupFile
	var total int64
	release := func() {
		for _, file := range content.Files {
			_ = msRedisBackupPayloads.Remove(file.Ref)
		}
	}
	if err := plane.Ensure(); err != nil {
		return content, nil, 0, err
	}
	primaries, _, err := plane.shardMap()
	if err != nil {
		return content, nil, 0, err
	}
	for shard, node := range primaries {
		data, err := plane.Snapshot(node)
		if err != nil {
			release()
			return msRedisBackupContent{}, nil, 0, err
		}
		ref, _, err := msRedisBackupPayloads.Write(data)
		if err != nil {
			release()
			return msRedisBackupContent{}, nil, 0, err
		}
		name := fmt.Sprintf("shard-%d.rdb", shard)
		content.Files = append(content.Files, msRedisBackupFileContent{FileName: name, Ref: ref})
		files = append(files, MSRedisBackupFile{FileName: name, SizeBytes: strconv.Itoa(len(data)), CreateTime: nowTimestamp()})
		total += int64(len(data))
	}
	return content, files, total, nil
}

func handleMSRedisClusterGetCA(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	cluster, ok := msRedisClusters.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	out := map[string]any{"name": name + "/certificateAuthority"}
	if msRedisClusterTLS(cluster) {
		ca, err := msRedisEnsureCA(msRedisClusterCA(cluster))
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
			return
		}
		out["managedServerCa"] = msRedisManagedServerCA(ca)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func msRedisManagedServerCA(ca msRedisCertificateAuthority) map[string]any {
	return map[string]any{
		"caCerts": []map[string]any{{"certificates": []string{ca.CertPEM}}},
	}
}

func handleMSRedisSharedRegionalCA(w http.ResponseWriter, r *http.Request) {
	name := msRedisSharedCAName(sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	ca, err := msRedisEnsureCA(name)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":            name,
		"managedServerCa": msRedisManagedServerCA(ca),
	})
}

func handleMSRedisAddTokenAuthUser(w http.ResponseWriter, r *http.Request, project, location, clusterID string) {
	var req struct {
		TokenAuthUser string `json:"tokenAuthUser"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	userID := req.TokenAuthUser
	if userID == "" {
		userID = "user-" + sim.NewUUID()
	}
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s", project, location, clusterID, userID)
	msRedisTokenAuthUsers.Put(name, MSRedisTokenAuthUser{Name: name, State: "ACTIVE"})
	cluster := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, clusterID)
	op := redisClusterLRO(r, project, location, cluster, MSRedisTokenAuthUser{Name: name, State: "ACTIVE"},
		"type.googleapis.com/google.cloud.redis.cluster.v1.TokenAuthUser")
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, msRedisApplyUsers(cluster)))
}

func handleMSRedisTokenAuthUserList(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	out := []MSRedisTokenAuthUser{}
	for _, u := range msRedisTokenAuthUsers.List() {
		// Exclude nested authToken-derived rows by requiring exactly the
		// tokenAuthUser depth (no further path segments).
		if strings.HasPrefix(u.Name, prefix) && !strings.Contains(strings.TrimPrefix(u.Name, prefix), "/") {
			out = append(out, u)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tokenAuthUsers": out})
}

func handleMSRedisTokenAuthUserGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"), sim.PathParam(r, "tid"))
	u, ok := msRedisTokenAuthUsers.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tokenAuthUser not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, u)
}

func handleMSRedisTokenAuthUserDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s",
		project, location, sim.PathParam(r, "id"), sim.PathParam(r, "tid"))
	if !msRedisTokenAuthUsers.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tokenAuthUser not found: %s", name)
		return
	}
	for _, token := range msRedisAuthTokens.List() {
		if strings.HasPrefix(token.Name, name+"/authTokens/") {
			msRedisAuthTokens.Delete(token.Name)
		}
	}
	cluster := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, sim.PathParam(r, "id"))
	op := redisClusterLRO(r, project, location, name, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, msRedisApplyUsers(cluster)))
}

func handleMSRedisTokenAuthUserAction(w http.ResponseWriter, r *http.Request) {
	tidAction := sim.PathParam(r, "tidAction")
	tid, action, found := strings.Cut(tidAction, ":")
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on tokenAuthUser %q", tidAction)
		return
	}
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	clusterID := sim.PathParam(r, "id")
	userName := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s", project, location, clusterID, tid)
	if _, ok := msRedisTokenAuthUsers.Get(userName); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tokenAuthUser not found: %s", userName)
		return
	}
	if action != "addAuthToken" {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on tokenAuthUser %q", action, tid)
		return
	}
	var req struct {
		AuthToken MSRedisAuthToken `json:"authToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	tokenID := req.AuthToken.Name
	if tokenID == "" {
		tokenID = "token-" + sim.NewUUID()
	}
	tokenName := userName + "/authTokens/" + tokenID
	token := MSRedisAuthToken{
		Name:       tokenName,
		Token:      sim.NewUUID(),
		CreateTime: nowTimestamp(),
		State:      "ACTIVE",
	}
	msRedisAuthTokens.Put(tokenName, token)
	cluster := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, clusterID)
	op := redisClusterLRO(r, project, location, userName, token, "type.googleapis.com/google.cloud.redis.cluster.v1.AuthToken")
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, msRedisApplyUsers(cluster)))
}

func handleMSRedisAuthTokenList(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s/authTokens/",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"), sim.PathParam(r, "tid"))
	out := []MSRedisAuthToken{}
	for _, tok := range msRedisAuthTokens.List() {
		if strings.HasPrefix(tok.Name, prefix) {
			out = append(out, tok)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"authTokens": out})
}

func handleMSRedisAuthTokenGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s/authTokens/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"), sim.PathParam(r, "tid"), sim.PathParam(r, "atid"))
	tok, ok := msRedisAuthTokens.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "authToken not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, tok)
}

func handleMSRedisAuthTokenDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s/authTokens/%s",
		project, location, sim.PathParam(r, "id"), sim.PathParam(r, "tid"), sim.PathParam(r, "atid"))
	if !msRedisAuthTokens.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "authToken not found: %s", name)
		return
	}
	cluster := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, sim.PathParam(r, "id"))
	op := redisClusterLRO(r, project, location, name, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, msRedisApplyUsers(cluster)))
}

func handleMSRedisBackupCollectionList(w http.ResponseWriter, r *http.Request) {
	prefix := msRedisLocationPrefix(r, "backupCollections")
	out := []MSRedisBackupCollection{}
	for _, bc := range msRedisBackupColls.List() {
		if strings.HasPrefix(bc.Name, prefix) {
			out = append(out, bc)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"backupCollections": out})
}

func handleMSRedisBackupCollectionGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/backupCollections/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "bc"))
	bc, ok := msRedisBackupColls.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backupCollection not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, bc)
}

func handleMSRedisBackupList(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/backupCollections/%s/backups/",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "bc"))
	out := []MSRedisBackup{}
	for _, b := range msRedisBackups.List() {
		if strings.HasPrefix(b.Name, prefix) {
			out = append(out, b)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"backups": out})
}

func handleMSRedisBackupGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/backupCollections/%s/backups/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "bc"), sim.PathParam(r, "bid"))
	b, ok := msRedisBackups.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, b)
}

func handleMSRedisBackupDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/backupCollections/%s/backups/%s",
		project, location, sim.PathParam(r, "bc"), sim.PathParam(r, "bid"))
	if !msRedisBackups.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", name)
		return
	}
	msRedisReleaseBackup(name)
	op := redisClusterLRO(r, project, location, name, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisBackupAction(w http.ResponseWriter, r *http.Request) {
	bidAction := sim.PathParam(r, "bidAction")
	bid, action, found := strings.Cut(bidAction, ":")
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on backup %q", bidAction)
		return
	}
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/backupCollections/%s/backups/%s",
		project, location, sim.PathParam(r, "bc"), bid)
	backup, ok := msRedisBackups.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", name)
		return
	}
	if action != "export" {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on backup %q", action, bid)
		return
	}
	var req struct {
		GcsBucket string `json:"gcsBucket"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	bucket := strings.TrimPrefix(req.GcsBucket, "gs://")
	if bucket == "" || strings.Contains(bucket, "/") {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "gcsBucket %q is not a Cloud Storage bucket name", req.GcsBucket)
		return
	}
	op := redisClusterLRO(r, project, location, name, backup, "type.googleapis.com/google.cloud.redis.cluster.v1.Backup")
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, msRedisExportBackup(name, bid, bucket)))
}

// msRedisExportBackup writes every file of a backup to the bucket, under a
// folder named after the backup.
func msRedisExportBackup(name, backupID, bucket string) error {
	if _, ok := gcsBuckets.Get(bucket); !ok {
		return msRedisOperationFailure(rpcNotFound, "bucket %q not found", bucket)
	}
	content, ok := msRedisBackupContents.Get(name)
	if !ok {
		return fmt.Errorf("backup %s holds no files", name)
	}
	for _, file := range content.Files {
		data, err := msRedisBackupPayloads.Read(file.Ref)
		if err != nil {
			return fmt.Errorf("read backup file %s: %w", file.FileName, err)
		}
		if _, err := persistGCSObjectBytes(bucket, backupID+"/"+file.FileName, data,
			GCSObject{ContentType: "application/octet-stream"}, gcsPreconditions{}); err != nil {
			return err
		}
	}
	return nil
}

func handleMSRedisAclPolicyCreate(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("aclPolicyId")
	if id == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "aclPolicyId query parameter is required")
		return
	}
	var req MSRedisAclPolicy
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	name := fmt.Sprintf("projects/%s/locations/%s/aclPolicies/%s", project, location, id)
	policy := MSRedisAclPolicy{
		Name:  name,
		Rules: req.Rules,
		State: "ACTIVE",
		// version is a drift-resolution counter (int64 serialized as a
		// string on the wire); a freshly created policy starts at 1.
		Version:    defaultStr(req.Version, "1"),
		Etag:       sim.NewUUID(),
		CreateTime: time.Now().UTC().Format(time.RFC3339),
		UpdateTime: time.Now().UTC().Format(time.RFC3339),
	}
	msRedisAclPolicies.Put(name, policy)
	msRedisPutAclRevision(policy, "1")
	// aclPolicies.create returns the AclPolicy resource directly (not an LRO).
	sim.WriteJSON(w, http.StatusOK, policy)
}

func handleMSRedisAclPolicyGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/aclPolicies/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	p, ok := msRedisAclPolicies.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleMSRedisAclPolicyList(w http.ResponseWriter, r *http.Request) {
	prefix := msRedisLocationPrefix(r, "aclPolicies")
	out := []MSRedisAclPolicy{}
	for _, p := range msRedisAclPolicies.List() {
		if strings.HasPrefix(p.Name, prefix) {
			out = append(out, p)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"aclPolicies": out})
}

func handleMSRedisAclPolicyPatch(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/aclPolicies/%s", project, location, sim.PathParam(r, "id"))
	if _, ok := msRedisAclPolicies.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", name)
		return
	}
	var req MSRedisAclPolicy
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	mask := r.URL.Query().Get("updateMask")
	fields := map[string]bool{}
	for _, f := range strings.Split(mask, ",") {
		if f = strings.TrimSpace(f); f != "" {
			fields[f] = true
		}
	}
	wants := func(n string) bool { return len(fields) == 0 || fields[n] }
	msRedisAclPolicies.Update(name, func(p *MSRedisAclPolicy) {
		if wants("rules") {
			p.Rules = req.Rules
		}
		if wants("version") && req.Version != "" {
			p.Version = req.Version
		}
		p.Etag = sim.NewUUID()
		p.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	})
	updated, _ := msRedisAclPolicies.Get(name)
	revision := strconv.Itoa(msRedisAclRevisionCount(name) + 1)
	updated.Version = revision
	msRedisAclPolicies.Put(name, updated)
	msRedisPutAclRevision(updated, revision)
	op := redisClusterLRO(r, project, location, name, updated, "type.googleapis.com/google.cloud.redis.cluster.v1.AclPolicy")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisAclPolicyDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/aclPolicies/%s", project, location, sim.PathParam(r, "id"))
	if !msRedisAclPolicies.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", name)
		return
	}
	for _, revision := range msRedisAclRevisions.List() {
		if strings.HasPrefix(revision.Name, name+"/revisions/") {
			msRedisAclRevisions.Delete(revision.Name)
		}
	}
	op := redisClusterLRO(r, project, location, name, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func msRedisPutAclRevision(policy MSRedisAclPolicy, revisionNumber string) {
	snapshot := policy
	snapshot.Rules = append([]MSRedisAclRule(nil), policy.Rules...)
	name := policy.Name + "/revisions/" + revisionNumber
	msRedisAclRevisions.Put(name, MSRedisAclPolicyRevision{
		Name:           name,
		RevisionNumber: revisionNumber,
		CreateTime:     time.Now().UTC().Format(time.RFC3339),
		Snapshot:       snapshot,
	})
}

func msRedisAclRevisionCount(policyName string) int {
	count := 0
	for _, revision := range msRedisAclRevisions.List() {
		if strings.HasPrefix(revision.Name, policyName+"/revisions/") {
			count++
		}
	}
	return count
}

func handleMSRedisAclPolicyRevisionList(w http.ResponseWriter, r *http.Request) {
	policyName := fmt.Sprintf("projects/%s/locations/%s/aclPolicies/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	if _, ok := msRedisAclPolicies.Get(policyName); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", policyName)
		return
	}
	var revisions []MSRedisAclPolicyRevision
	for _, revision := range msRedisAclRevisions.List() {
		if strings.HasPrefix(revision.Name, policyName+"/revisions/") {
			revisions = append(revisions, revision)
		}
	}
	sort.Slice(revisions, func(i, j int) bool {
		left, _ := strconv.Atoi(revisions[i].RevisionNumber)
		right, _ := strconv.Atoi(revisions[j].RevisionNumber)
		return left > right
	})
	page, next, ok := paginateList(w, r, revisions)
	if !ok {
		return
	}
	response := map[string]any{"aclPolicyRevisions": page}
	if next != "" {
		response["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func handleMSRedisAclPolicyRevisionGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/aclPolicies/%s/revisions/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"),
		sim.PathParam(r, "id"), sim.PathParam(r, "revision"))
	revision, ok := msRedisAclRevisions.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy revision not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, revision)
}

func handleMSRedisAction(w http.ResponseWriter, r *http.Request) {
	idAction := sim.PathParam(r, "idAction")
	id, action, found := strings.Cut(idAction, ":")
	if gcpV1InstancesIsCloudRun(r, action) {
		cloudRunAdminV1InstanceIAM(w, r, id, action)
		return
	}
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"unknown action on memorystore instance %q", idAction)
		return
	}
	switch action {
	case "upgrade":
		handleMSRedisUpgrade(w, r, id)
	case "failover":
		handleMSRedisFailover(w, r, id)
	case "rescheduleMaintenance":
		handleMSRedisRescheduleMaintenance(w, r, id)
	case "import":
		handleMSRedisTransfer(w, r, id, "import")
	case "export":
		handleMSRedisTransfer(w, r, id, "export")
	default:
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"unknown action %q on memorystore instance %q", action, id)
	}
}

func msRedisInstanceName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, id)
}

func handleMSRedisCreate(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("instanceId")
	if id == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "instanceId query parameter is required")
		return
	}
	var req MSRedisInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	inst := MSRedisInstance{
		Name:                  msRedisInstanceName(project, location, id),
		DisplayName:           req.DisplayName,
		Tier:                  defaultStr(req.Tier, "BASIC"),
		RedisVersion:          defaultStr(req.RedisVersion, "REDIS_7_0"),
		MemorySizeGb:          defaultInt(req.MemorySizeGb, 1),
		ReadReplicasMode:      req.ReadReplicasMode,
		State:                 "READY",
		CreateTime:            nowTimestamp(),
		Labels:                req.Labels,
		AuthorizedNetwork:     req.AuthorizedNetwork,
		AuthEnabled:           req.AuthEnabled,
		RedisConfigs:          req.RedisConfigs,
		ConnectMode:           defaultStr(req.ConnectMode, "DIRECT_PEERING"),
		TransitEncryptionMode: defaultStr(req.TransitEncryptionMode, "DISABLED"),
	}
	if inst.TransitEncryptionMode == "TRANSIT_ENCRYPTION_MODE_UNSPECIFIED" {
		inst.TransitEncryptionMode = "DISABLED"
	}
	if inst.TransitEncryptionMode != "DISABLED" && inst.TransitEncryptionMode != "SERVER_AUTHENTICATION" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "transitEncryptionMode %q is not one of SERVER_AUTHENTICATION or DISABLED", inst.TransitEncryptionMode)
		return
	}
	if inst.Tier == "STANDARD_HA" {
		inst.ReadReplicasMode = defaultStr(inst.ReadReplicasMode, "READ_REPLICAS_DISABLED")
	}
	if err := msRedisValidateReplicas(inst.Tier, inst.ReadReplicasMode, req.ReplicaCount); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	inst.ReplicaCount = msRedisInstanceReplicas(inst.Tier, inst.ReadReplicasMode, req.ReplicaCount)
	persistence, err := msRedisInstancePersistence(msRedisPersistence{}, req.PersistenceConfig, time.Now())
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	inst.PersistenceConfig = persistence.instanceConfig(time.Now())
	spec, err := msRedisInstanceSpec(inst)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if _, exists := msRedisInstances.Get(inst.Name); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "instance %s already exists", inst.Name)
		return
	}
	op := redisInstanceLRO(r, project, location, inst.Name, inst, msRedisInstanceType)
	if msRedisInstanceTLS(inst) {
		ca, err := msRedisEnsureCA(inst.Name)
		if err != nil {
			sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
			return
		}
		inst.ServerCaCerts = []MSRedisTLSCertificate{ca.tlsCertificate()}
	}
	record := msRedisPlaneRecord{}
	if inst.AuthEnabled {
		record.AuthString = sim.NewUUID()
	}
	if err := msRedisInstallInstance(&inst, &record, spec); err != nil {
		msRedisReleaseCA(inst.Name)
		sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
		return
	}
	if err := msRedisStartEngine(inst.Name); err != nil {
		msRedisReleaseCA(inst.Name)
		sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
		return
	}
	msRedisPlaneRecords.Put(inst.Name, record)
	msRedisInstances.Put(inst.Name, inst)
	op = redisInstanceLRO(r, project, location, inst.Name, msRedisInstanceView(inst), msRedisInstanceType)
	sim.WriteJSON(w, http.StatusOK, op)
}

// msRedisValidateReplicas refuses a replica count the tier and read-replicas
// mode do not allow: none on the Basic Tier, one on the Standard Tier without
// read replicas, and one to five with them.
func msRedisValidateReplicas(tier, readReplicasMode string, requested int) error {
	switch {
	case tier != "STANDARD_HA" && requested != 0:
		return fmt.Errorf("replicaCount %d is not valid for a Basic Tier instance, which runs no replica", requested)
	case tier == "STANDARD_HA" && readReplicasMode != "READ_REPLICAS_ENABLED" && requested > 1:
		return fmt.Errorf("replicaCount %d needs read replicas enabled; without them a Standard Tier instance runs one replica", requested)
	case requested < 0 || requested > msRedisMaxInstanceReplicas:
		return fmt.Errorf("replicaCount %d is outside 1-%d", requested, msRedisMaxInstanceReplicas)
	}
	return nil
}

// msRedisMaxInstanceReplicas is the most read replicas an instance runs.
const msRedisMaxInstanceReplicas = 5

// msRedisInstanceView is an instance as the service reports it, with the next
// RDB snapshot time as of now.
func msRedisInstanceView(inst MSRedisInstance) MSRedisInstance {
	if inst.PersistenceConfig != nil {
		inst.PersistenceConfig = msRedisPersistenceOfInstance(inst.PersistenceConfig).instanceConfig(time.Now())
	}
	return inst
}

func handleMSRedisGet(w http.ResponseWriter, r *http.Request) {
	idVerb := sim.PathParam(r, "id")
	id, verb, _ := strings.Cut(idVerb, ":")
	if gcpV1InstancesIsCloudRun(r, verb) {
		cloudRunAdminV1InstanceIAM(w, r, id, verb)
		return
	}
	name := msRedisInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), idVerb)
	inst, ok := msRedisInstances.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, msRedisInstanceView(inst))
}

func handleMSRedisList(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/instances/", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	var out []MSRedisInstance
	for _, i := range msRedisInstances.List() {
		if strings.HasPrefix(i.Name, prefix) {
			out = append(out, msRedisInstanceView(i))
		}
	}
	if out == nil {
		out = []MSRedisInstance{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func handleMSRedisPatch(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := msRedisInstanceName(project, location, sim.PathParam(r, "id"))
	current, ok := msRedisInstances.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	var req MSRedisInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	wants := msRedisUpdateMask(r)
	memorySizeGb, redisConfigs := current.MemorySizeGb, current.RedisConfigs
	if wants("memorySizeGb") {
		memorySizeGb = req.MemorySizeGb
	}
	if wants("redisConfigs") {
		redisConfigs = req.RedisConfigs
	}
	directives, err := msRedisEngineDirectives(memorySizeGb, redisConfigs)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	readReplicasMode, replicas := current.ReadReplicasMode, current.ReplicaCount
	if wants("readReplicasMode") && req.ReadReplicasMode != "" {
		if current.Tier != "STANDARD_HA" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read replicas need a Standard Tier instance")
			return
		}
		readReplicasMode = req.ReadReplicasMode
		if readReplicasMode == "READ_REPLICAS_DISABLED" {
			replicas = 1
		}
	}
	if wants("replicaCount") && current.Tier == "STANDARD_HA" && (req.ReplicaCount != 0 || readReplicasMode != "READ_REPLICAS_ENABLED") {
		if err := msRedisValidateReplicas(current.Tier, readReplicasMode, req.ReplicaCount); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		replicas = msRedisInstanceReplicas(current.Tier, readReplicasMode, req.ReplicaCount)
	}
	persistence := msRedisPersistenceOfInstance(current.PersistenceConfig)
	if wants("persistenceConfig") {
		if persistence, err = msRedisInstancePersistence(persistence, req.PersistenceConfig, time.Now()); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
	}
	authEnabled := current.AuthEnabled
	if wants("authEnabled") {
		authEnabled = req.AuthEnabled
	}
	record, _ := msRedisPlaneRecords.Get(name)
	authString := record.AuthString
	if authEnabled != current.AuthEnabled {
		authString = ""
		if authEnabled {
			authString = sim.NewUUID()
		}
	}
	readEndpoint, readEndpointPort := current.ReadEndpoint, current.ReadEndpointPort
	if plane, ok := msRedisLoadPlane(name); ok {
		plane.opMu.Lock()
		if wants("memorySizeGb") || wants("redisConfigs") {
			if err = plane.Configure(directives); err != nil {
				err = msRedisOperationFailure(rpcInvalidArgument, "the Redis engine refused the configuration: %v", err)
			}
		}
		if err == nil && readReplicasMode != current.ReadReplicasMode {
			port := msRedisPort
			if msRedisInstanceTLS(current) {
				port = msRedisTLSPort
			}
			enabled := readReplicasMode == "READ_REPLICAS_ENABLED"
			if readEndpoint, err = plane.SetReadEndpoint(enabled, port); err == nil {
				readEndpointPort = 0
				if enabled {
					readEndpointPort = port
				}
			}
		}
		if err == nil && replicas != current.ReplicaCount {
			msRedisSetState(name, "UPDATING")
			err = plane.ResizeInstance(replicas)
		}
		if err == nil && wants("persistenceConfig") {
			err = plane.SetPersistence(persistence)
		}
		if err == nil && authString != record.AuthString {
			err = plane.SetPassword(authString)
		}
		plane.opMu.Unlock()
		msRedisSaveRecord(name)
	}
	if err == nil {
		msRedisPlaneRecords.Update(name, func(rec *msRedisPlaneRecord) { rec.AuthString = authString })
	}
	msRedisInstances.Update(name, func(i *MSRedisInstance) {
		i.State = "READY"
		if err != nil {
			return
		}
		if wants("displayName") {
			i.DisplayName = req.DisplayName
		}
		if wants("labels") {
			i.Labels = req.Labels
		}
		i.MemorySizeGb, i.RedisConfigs = memorySizeGb, redisConfigs
		i.ReadReplicasMode, i.ReplicaCount = readReplicasMode, replicas
		i.ReadEndpoint, i.ReadEndpointPort = readEndpoint, readEndpointPort
		i.PersistenceConfig = persistence.instanceConfig(time.Now())
		i.AuthEnabled = authEnabled
	})
	updated, _ := msRedisInstances.Get(name)
	op := redisInstanceLRO(r, project, location, name, msRedisInstanceView(updated), msRedisInstanceType)
	sim.WriteJSON(w, http.StatusOK, msRedisSettle(op, err))
}

func handleMSRedisDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := msRedisInstanceName(project, location, sim.PathParam(r, "id"))
	if !msRedisInstances.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	msRedisRemovePlane(name)
	msRedisReleaseCA(name)
	op := redisInstanceLRO(r, project, location, name, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleMSRedisAuthString returns the AUTH string the instance's engine
// requires. Memorystore issues one only when OSS Redis AUTH is enabled; with
// AUTH disabled the response carries an empty authString.
func handleMSRedisAuthString(w http.ResponseWriter, r *http.Request) {
	name := msRedisInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	inst, ok := msRedisInstances.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	out := map[string]any{}
	if record, ok := msRedisPlaneRecords.Get(name); ok && inst.AuthEnabled && record.AuthString != "" {
		out["authString"] = record.AuthString
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
func defaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// handleMSRedisRescheduleMaintenance moves the upcoming maintenance window.
// SPECIFIC_TIME takes the caller's time; the other modes resolve to one the
// service picks, which here is the moment the reschedule was asked for.
func handleMSRedisRescheduleMaintenance(w http.ResponseWriter, r *http.Request, id string) {
	key := msRedisInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), id)
	inst, ok := msRedisInstances.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Memorystore instance %q not found", id)
		return
	}
	var req struct {
		RescheduleType string `json:"rescheduleType"`
		ScheduleTime   string `json:"scheduleTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid reschedule body: %v", err)
		return
	}
	scheduled := req.ScheduleTime
	switch req.RescheduleType {
	case "SPECIFIC_TIME":
		if scheduled == "" {
			GCPError(w, http.StatusBadRequest,
				"scheduleTime is required when rescheduleType is SPECIFIC_TIME", "INVALID_ARGUMENT")
			return
		}
	case "IMMEDIATE", "NEXT_AVAILABLE_WINDOW":
		scheduled = nowTimestamp()
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"rescheduleType %q is not one of IMMEDIATE, NEXT_AVAILABLE_WINDOW or SPECIFIC_TIME",
			req.RescheduleType)
		return
	}
	inst.MaintenanceSchedule = map[string]any{
		"startTime":            scheduled,
		"endTime":              scheduled,
		"canReschedule":        true,
		"scheduleDeadlineTime": scheduled,
	}
	msRedisInstances.Put(key, inst)
	now := nowTimestamp()
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":     "operations/reschedule-maintenance-" + sim.NewUUID(),
		"done":     true,
		"metadata": map[string]any{"operationType": "RESCHEDULE_MAINTENANCE", "startTime": now, "endTime": now},
		"response": inst,
	})
}
