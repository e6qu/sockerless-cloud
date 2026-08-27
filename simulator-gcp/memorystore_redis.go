package main

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// simRedisHost derives a deterministic RFC1918 address from the
// instance ID so terraform-provider-google reads + redis-cli probes
// see a syntactically valid IP rather than a `.example` placeholder.
func simRedisHost(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	v := h.Sum32()
	return fmt.Sprintf("10.%d.%d.%d", (v>>16)&0xff, (v>>8)&0xff, v&0xff)
}

// Cloud Memorystore for Redis v1 — REST surface scoped to instance
// lifecycle. Real API: https://redis.googleapis.com/$discovery/rest?version=v1
// The Redis engine itself is not simulated; the sim reports
// State=READY immediately after Create.

type MSRedisInstance struct {
	Name              string            `json:"name"` // projects/{p}/locations/{loc}/instances/{id}
	DisplayName       string            `json:"displayName,omitempty"`
	Tier              string            `json:"tier,omitempty"`
	RedisVersion      string            `json:"redisVersion,omitempty"`
	MemorySizeGb      int               `json:"memorySizeGb,omitempty"`
	Host              string            `json:"host,omitempty"`
	Port              int               `json:"port,omitempty"`
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
}

var msRedisInstances sim.Store[MSRedisInstance]

func registerMemorystoreRedis(srv *sim.Server) {
	msRedisInstances = sim.MakeStore[MSRedisInstance](srv.DB(), "memorystore_redis")

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/instances", handleMSRedisCreate)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances/{id}", handleMSRedisGet)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances", handleMSRedisList)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/instances/{id}", handleMSRedisPatch)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/instances/{id}", handleMSRedisDelete)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/instances/{id}/authString", handleMSRedisAuthString)
	// Maintenance state machines:
	//   READY → UPGRADING → READY        (upgrade)
	//   READY → FAILING_OVER → READY     (failover)
	// Sim collapses both transitions inline (no async work to wait
	// on), but the State field is set + restored so SDKs reading
	// the instance during the LRO see a value other than zero.
	//
	// Go ServeMux can't parse `{id}:upgrade`; capture the action
	// suffix in a single wildcard and split on `:` in the handler.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/instances/{idAction}", handleMSRedisAction)

	registerMemorystoreRedisClusters(srv)
}

// Memorystore for Redis Cluster API
//
// The cluster surface is the newer Redis Cluster product (sharded, ACL
// policies, automated/manual backups). Resources carry a lifecycle State
// the SDK reads back; the sim collapses every transition to its terminal
// value (ACTIVE/READY) because there is no asynchronous work to wait on,
// and every mutating call returns a synchronous done=true Operation just
// like the instance surface above.

// MSRedisCluster mirrors google.cloud.redis.v1.Cluster — only the fields the
// Discovery schema declares (the runtime spec-validator rejects any member
// not defined by the Cluster schema).
type MSRedisCluster struct {
	Name                      string                     `json:"name"`
	CreateTime                string                     `json:"createTime,omitempty"`
	State                     string                     `json:"state,omitempty"`
	Uid                       string                     `json:"uid,omitempty"`
	ReplicaCount              int                        `json:"replicaCount,omitempty"`
	AuthorizationMode         string                     `json:"authorizationMode,omitempty"`
	TransitEncryptionMode     string                     `json:"transitEncryptionMode,omitempty"`
	SizeGb                    int                        `json:"sizeGb,omitempty"`
	ShardCount                int                        `json:"shardCount,omitempty"`
	DiscoveryEndpoints        []MSRedisDiscoveryEndpoint `json:"discoveryEndpoints,omitempty"`
	NodeType                  string                     `json:"nodeType,omitempty"`
	PreciseSizeGb             float64                    `json:"preciseSizeGb,omitempty"`
	RedisConfigs              map[string]string          `json:"redisConfigs,omitempty"`
	DeletionProtectionEnabled bool                       `json:"deletionProtectionEnabled,omitempty"`
	Labels                    map[string]string          `json:"labels,omitempty"`
	BackupCollection          string                     `json:"backupCollection,omitempty"`
	AclPolicy                 string                     `json:"aclPolicy,omitempty"`
}

// MSRedisDiscoveryEndpoint mirrors google.cloud.redis.v1.DiscoveryEndpoint.
type MSRedisDiscoveryEndpoint struct {
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// MSRedisBackupCollection mirrors google.cloud.redis.v1.BackupCollection.
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

// MSRedisBackup mirrors google.cloud.redis.v1.Backup.
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

// MSRedisBackupFile mirrors google.cloud.redis.v1.BackupFile.
type MSRedisBackupFile struct {
	FileName   string `json:"fileName,omitempty"`
	SizeBytes  string `json:"sizeBytes,omitempty"`
	CreateTime string `json:"createTime,omitempty"`
}

// MSRedisAclPolicy mirrors google.cloud.redis.v1.AclPolicy.
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

// MSRedisAclRule mirrors google.cloud.redis.v1.AclRule.
type MSRedisAclRule struct {
	Username string `json:"username,omitempty"`
	Rule     string `json:"rule,omitempty"`
}

// MSRedisTokenAuthUser mirrors google.cloud.redis.v1.TokenAuthUser.
type MSRedisTokenAuthUser struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

// MSRedisAuthToken mirrors google.cloud.redis.v1.AuthToken.
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

const msRedisClusterType = "type.googleapis.com/google.cloud.redis.v1.Cluster"

func registerMemorystoreRedisClusters(srv *sim.Server) {
	msRedisClusters = sim.MakeStore[MSRedisCluster](srv.DB(), "memorystore_redis_clusters")
	msRedisBackupColls = sim.MakeStore[MSRedisBackupCollection](srv.DB(), "memorystore_redis_backup_collections")
	msRedisBackups = sim.MakeStore[MSRedisBackup](srv.DB(), "memorystore_redis_backups")
	msRedisAclPolicies = sim.MakeStore[MSRedisAclPolicy](srv.DB(), "memorystore_redis_acl_policies")
	msRedisAclRevisions = sim.MakeStore[MSRedisAclPolicyRevision](srv.DB(), "memorystore_redis_acl_policy_revisions")
	msRedisTokenAuthUsers = sim.MakeStore[MSRedisTokenAuthUser](srv.DB(), "memorystore_redis_token_auth_users")
	msRedisAuthTokens = sim.MakeStore[MSRedisAuthToken](srv.DB(), "memorystore_redis_auth_tokens")

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
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "clusterId query parameter is required")
		return
	}
	var req MSRedisCluster
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, id)
	shardCount := defaultInt(req.ShardCount, 1)
	replicaCount := req.ReplicaCount
	cluster := MSRedisCluster{
		Name:                      name,
		CreateTime:                nowTimestamp(),
		State:                     "ACTIVE",
		Uid:                       generateUUID(),
		ReplicaCount:              replicaCount,
		AuthorizationMode:         defaultStr(req.AuthorizationMode, "AUTH_MODE_DISABLED"),
		TransitEncryptionMode:     defaultStr(req.TransitEncryptionMode, "TRANSIT_ENCRYPTION_MODE_DISABLED"),
		ShardCount:                shardCount,
		SizeGb:                    shardCount * (replicaCount + 1) * 13,
		PreciseSizeGb:             float64(shardCount*(replicaCount+1)) * 13.0,
		NodeType:                  defaultStr(req.NodeType, "REDIS_HIGHMEM_MEDIUM"),
		RedisConfigs:              req.RedisConfigs,
		DeletionProtectionEnabled: req.DeletionProtectionEnabled,
		Labels:                    req.Labels,
		AclPolicy:                 req.AclPolicy,
		DiscoveryEndpoints: []MSRedisDiscoveryEndpoint{{
			Address: simRedisHost(id),
			Port:    6379,
		}},
	}
	msRedisClusters.Put(name, cluster)
	op := newLRO(project, location, cluster, msRedisClusterType)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisClusterGet(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	c, ok := msRedisClusters.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
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
	if _, ok := msRedisClusters.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	var req MSRedisCluster
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
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
	msRedisClusters.Update(name, func(c *MSRedisCluster) {
		if wants("replicaCount") {
			c.ReplicaCount = req.ReplicaCount
		}
		if wants("shardCount") {
			c.ShardCount = req.ShardCount
		}
		if wants("redisConfigs") {
			c.RedisConfigs = req.RedisConfigs
		}
		if wants("deletionProtectionEnabled") {
			c.DeletionProtectionEnabled = req.DeletionProtectionEnabled
		}
		if wants("labels") {
			c.Labels = req.Labels
		}
		// Recompute derived size after a topology change.
		c.SizeGb = c.ShardCount * (c.ReplicaCount + 1) * 13
		c.PreciseSizeGb = float64(c.ShardCount*(c.ReplicaCount+1)) * 13.0
	})
	updated, _ := msRedisClusters.Get(name)
	op := newLRO(project, location, updated, msRedisClusterType)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisClusterDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, sim.PathParam(r, "id"))
	if !msRedisClusters.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	op := newLRO(project, location, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisClusterAction(w http.ResponseWriter, r *http.Request) {
	idAction := sim.PathParam(r, "idAction")
	id, action, found := strings.Cut(idAction, ":")
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on cluster %q", idAction)
		return
	}
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, id)
	cluster, ok := msRedisClusters.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	switch action {
	case "backup":
		handleMSRedisClusterBackup(w, r, project, location, id, cluster)
	case "rescheduleClusterMaintenance":
		// No maintenance window is simulated; the reschedule settles
		// synchronously and the cluster stays ACTIVE.
		op := newLRO(project, location, cluster, msRedisClusterType)
		sim.WriteJSON(w, http.StatusOK, op)
	case "addTokenAuthUser":
		handleMSRedisAddTokenAuthUser(w, r, project, location, id)
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on cluster %q", action, id)
	}
}

func handleMSRedisClusterBackup(w http.ResponseWriter, r *http.Request, project, location, clusterID string, cluster MSRedisCluster) {
	var req struct {
		Ttl      string `json:"ttl"`
		BackupID string `json:"backupId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
			Uid:        generateUUID(),
			CreateTime: nowTimestamp(),
		})
	}
	backupID := req.BackupID
	if backupID == "" {
		backupID = "backup-" + generateUUID()
	}
	backupName := bcName + "/backups/" + backupID
	backup := MSRedisBackup{
		Name:           backupName,
		CreateTime:     nowTimestamp(),
		Cluster:        cluster.Name,
		ClusterUid:     cluster.Uid,
		TotalSizeBytes: "0",
		EngineVersion:  "REDIS_7_2",
		NodeType:       cluster.NodeType,
		ReplicaCount:   cluster.ReplicaCount,
		ShardCount:     cluster.ShardCount,
		BackupType:     "ON_DEMAND",
		State:          "ACTIVE",
		Uid:            generateUUID(),
	}
	msRedisBackups.Put(backupName, backup)
	op := newLRO(project, location, cluster, msRedisClusterType)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisClusterGetCA(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	if _, ok := msRedisClusters.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name + "/certificateAuthority",
		"managedServerCa": map[string]any{
			"caCerts": []map[string]any{{
				"certificates": []string{simRedisCACert(name)},
			}},
		},
	})
}

func handleMSRedisSharedRegionalCA(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("projects/%s/locations/%s/sharedRegionalCertificateAuthority", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name,
		"managedServerCa": map[string]any{
			"caCerts": []map[string]any{{
				"certificates": []string{simRedisCACert(name)},
			}},
		},
	})
}

// simRedisCACert returns a deterministic PEM-shaped placeholder so callers
// reading the certificate authority see a syntactically PEM-bounded blob.
func simRedisCACert(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return fmt.Sprintf("-----BEGIN CERTIFICATE-----\nsim-redis-ca-%08x\n-----END CERTIFICATE-----", h.Sum32())
}

func handleMSRedisAddTokenAuthUser(w http.ResponseWriter, r *http.Request, project, location, clusterID string) {
	var req struct {
		TokenAuthUser string `json:"tokenAuthUser"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	userID := req.TokenAuthUser
	if userID == "" {
		userID = "user-" + generateUUID()
	}
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s", project, location, clusterID, userID)
	msRedisTokenAuthUsers.Put(name, MSRedisTokenAuthUser{Name: name, State: "ACTIVE"})
	op := newLRO(project, location, map[string]any{"name": name},
		"type.googleapis.com/google.cloud.redis.v1.TokenAuthUser")
	sim.WriteJSON(w, http.StatusOK, op)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tokenAuthUser not found: %s", name)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tokenAuthUser not found: %s", name)
		return
	}
	op := newLRO(project, location, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisTokenAuthUserAction(w http.ResponseWriter, r *http.Request) {
	tidAction := sim.PathParam(r, "tidAction")
	tid, action, found := strings.Cut(tidAction, ":")
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on tokenAuthUser %q", tidAction)
		return
	}
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	clusterID := sim.PathParam(r, "id")
	userName := fmt.Sprintf("projects/%s/locations/%s/clusters/%s/tokenAuthUsers/%s", project, location, clusterID, tid)
	if _, ok := msRedisTokenAuthUsers.Get(userName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tokenAuthUser not found: %s", userName)
		return
	}
	if action != "addAuthToken" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on tokenAuthUser %q", action, tid)
		return
	}
	var req struct {
		AuthToken MSRedisAuthToken `json:"authToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	tokenID := req.AuthToken.Name
	if tokenID == "" {
		tokenID = "token-" + generateUUID()
	}
	tokenName := userName + "/authTokens/" + tokenID
	token := MSRedisAuthToken{
		Name:       tokenName,
		Token:      generateUUID(),
		CreateTime: nowTimestamp(),
		State:      "ACTIVE",
	}
	msRedisAuthTokens.Put(tokenName, token)
	op := newLRO(project, location, token, "type.googleapis.com/google.cloud.redis.v1.AuthToken")
	sim.WriteJSON(w, http.StatusOK, op)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "authToken not found: %s", name)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "authToken not found: %s", name)
		return
	}
	op := newLRO(project, location, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backupCollection not found: %s", name)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", name)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", name)
		return
	}
	op := newLRO(project, location, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisBackupAction(w http.ResponseWriter, r *http.Request) {
	bidAction := sim.PathParam(r, "bidAction")
	bid, action, found := strings.Cut(bidAction, ":")
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on backup %q", bidAction)
		return
	}
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/backupCollections/%s/backups/%s",
		project, location, sim.PathParam(r, "bc"), bid)
	backup, ok := msRedisBackups.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", name)
		return
	}
	if action != "export" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on backup %q", action, bid)
		return
	}
	var req struct {
		GcsBucket string `json:"gcsBucket"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	op := newLRO(project, location, backup, "type.googleapis.com/google.cloud.redis.v1.Backup")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisAclPolicyCreate(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("aclPolicyId")
	if id == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "aclPolicyId query parameter is required")
		return
	}
	var req MSRedisAclPolicy
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
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
		Etag:       generateUUID(),
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", name)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", name)
		return
	}
	var req MSRedisAclPolicy
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
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
		p.Etag = generateUUID()
		p.UpdateTime = time.Now().UTC().Format(time.RFC3339)
	})
	updated, _ := msRedisAclPolicies.Get(name)
	revision := strconv.Itoa(msRedisAclRevisionCount(name) + 1)
	updated.Version = revision
	msRedisAclPolicies.Put(name, updated)
	msRedisPutAclRevision(updated, revision)
	op := newLRO(project, location, updated, "type.googleapis.com/google.cloud.redis.v1.AclPolicy")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisAclPolicyDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := fmt.Sprintf("projects/%s/locations/%s/aclPolicies/%s", project, location, sim.PathParam(r, "id"))
	if !msRedisAclPolicies.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", name)
		return
	}
	for _, revision := range msRedisAclRevisions.List() {
		if strings.HasPrefix(revision.Name, name+"/revisions/") {
			msRedisAclRevisions.Delete(revision.Name)
		}
	}
	op := newLRO(project, location, nil, "type.googleapis.com/google.protobuf.Empty")
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy not found: %s", policyName)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "aclPolicy revision not found: %s", name)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"unknown action on memorystore instance %q", idAction)
		return
	}
	switch action {
	case "upgrade":
		handleMSRedisUpgrade(w, r, id)
	case "failover":
		handleMSRedisFailover(w, r, id)
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"unknown action %q on memorystore instance %q", action, id)
	}
}

func handleMSRedisUpgrade(w http.ResponseWriter, r *http.Request, id string) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	key := msRedisInstanceName(project, location, id)
	inst, ok := msRedisInstances.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"Memorystore instance %q not found", id)
		return
	}
	var req struct {
		RedisVersion string `json:"redisVersion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.RedisVersion != "" {
		inst.RedisVersion = req.RedisVersion
	}
	inst.State = "READY"
	msRedisInstances.Put(key, inst)
	now := nowTimestamp()
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":     "operations/upgrade-" + generateUUID(),
		"done":     true,
		"metadata": map[string]any{"operationType": "UPGRADE_INSTANCE", "startTime": now, "endTime": now},
		"response": inst,
	})
}

func handleMSRedisFailover(w http.ResponseWriter, r *http.Request, id string) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	key := msRedisInstanceName(project, location, id)
	inst, ok := msRedisInstances.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"Memorystore instance %q not found", id)
		return
	}
	inst.State = "READY"
	msRedisInstances.Put(key, inst)
	now := nowTimestamp()
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":     "operations/failover-" + generateUUID(),
		"done":     true,
		"metadata": map[string]any{"operationType": "FAILOVER_INSTANCE", "startTime": now, "endTime": now},
		"response": inst,
	})
}

func msRedisInstanceName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, id)
}

func handleMSRedisCreate(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("instanceId")
	if id == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "instanceId query parameter is required")
		return
	}
	var req MSRedisInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	inst := MSRedisInstance{
		Name:         msRedisInstanceName(project, location, id),
		DisplayName:  req.DisplayName,
		Tier:         defaultStr(req.Tier, "BASIC"),
		RedisVersion: defaultStr(req.RedisVersion, "REDIS_7_0"),
		MemorySizeGb: defaultInt(req.MemorySizeGb, 1),
		// Real Memorystore returns an RFC1918 address that the workload
		// connects to; emit a deterministic 10.x.x.x derived from the
		// instance ID so callers and terraform-provider-google reads
		// see a syntactically valid IP rather than a `.example` placeholder
		// that resolves to NXDOMAIN.
		Host:                  simRedisHost(id),
		Port:                  6379,
		State:                 "READY",
		CreateTime:            nowTimestamp(),
		Labels:                req.Labels,
		AuthorizedNetwork:     req.AuthorizedNetwork,
		AuthEnabled:           req.AuthEnabled,
		RedisConfigs:          req.RedisConfigs,
		ConnectMode:           defaultStr(req.ConnectMode, "DIRECT_PEERING"),
		TransitEncryptionMode: defaultStr(req.TransitEncryptionMode, "DISABLED"),
	}
	msRedisInstances.Put(inst.Name, inst)
	op := newLRO(project, location, inst, "type.googleapis.com/google.cloud.redis.v1.Instance")
	sim.WriteJSON(w, http.StatusOK, op)
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
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, inst)
}

func handleMSRedisList(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/instances/", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	var out []MSRedisInstance
	for _, i := range msRedisInstances.List() {
		if strings.HasPrefix(i.Name, prefix) {
			out = append(out, i)
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
	if _, ok := msRedisInstances.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	var req MSRedisInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	// Honour updateMask: only the named paths are written, so a
	// displayName-only patch leaves memorySizeGb/labels/etc. untouched.
	// An empty mask means "update everything supplied" (legacy behaviour).
	mask := r.URL.Query().Get("updateMask")
	fields := map[string]bool{}
	for _, f := range strings.Split(mask, ",") {
		if f = strings.TrimSpace(f); f != "" {
			fields[f] = true
		}
	}
	wants := func(name string) bool { return len(fields) == 0 || fields[name] }
	msRedisInstances.Update(name, func(i *MSRedisInstance) {
		if wants("displayName") {
			i.DisplayName = req.DisplayName
		}
		if wants("memorySizeGb") {
			i.MemorySizeGb = req.MemorySizeGb
		}
		if wants("labels") {
			i.Labels = req.Labels
		}
		if wants("redisConfigs") {
			i.RedisConfigs = req.RedisConfigs
		}
	})
	updated, _ := msRedisInstances.Get(name)
	op := newLRO(project, location, updated, "type.googleapis.com/google.cloud.redis.v1.Instance")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleMSRedisDelete(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := msRedisInstanceName(project, location, sim.PathParam(r, "id"))
	if !msRedisInstances.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	op := newLRO(project, location, nil, "type.googleapis.com/google.protobuf.Empty")
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleMSRedisAuthString returns the instance AUTH string. Real Memorystore
// only issues an AUTH string when OSS Redis AUTH is enabled on the instance;
// with AUTH disabled the response carries an empty authString.
func handleMSRedisAuthString(w http.ResponseWriter, r *http.Request) {
	name := msRedisInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "id"))
	inst, ok := msRedisInstances.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	out := map[string]any{}
	if inst.AuthEnabled {
		h := fnv.New32a()
		_, _ = h.Write([]byte(name + ":auth"))
		out["authString"] = fmt.Sprintf("sim-redis-auth-%08x", h.Sum32())
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
