package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud SQL Admin v1 — REST surface. Real API path prefix is
// `/sql/v1beta4/...` in some clients and `/v1/...` in others;
// the Go `sqladmin/v1` client used by terraform-provider-google
// hits `/v1/projects/{p}/instances...`. Surface scoped to the
// 90th-percentile instance + database + user lifecycle. The
// database engine itself is not simulated; State=RUNNABLE
// immediately on insert.

type SQLInstance struct {
	Name                             string           `json:"name"`
	Project                          string           `json:"project,omitempty"`
	Region                           string           `json:"region,omitempty"`
	DatabaseVersion                  string           `json:"databaseVersion,omitempty"`
	State                            string           `json:"state,omitempty"`
	BackendType                      string           `json:"backendType,omitempty"`
	InstanceType                     string           `json:"instanceType,omitempty"`
	ConnectionName                   string           `json:"connectionName,omitempty"`
	GceZone                          string           `json:"gceZone,omitempty"`
	CreateTime                       string           `json:"createTime,omitempty"`
	DatabaseCenterIntegrationEnabled *bool            `json:"databaseCenterIntegrationEnabled,omitempty"`
	OnPremisesConfiguration          map[string]any   `json:"onPremisesConfiguration,omitempty"`
	Settings                         map[string]any   `json:"settings,omitempty"`
	IpAddresses                      []map[string]any `json:"ipAddresses,omitempty"`
	SelfLink                         string           `json:"selfLink,omitempty"`
}

type SQLDatabase struct {
	Name     string `json:"name"`
	Instance string `json:"instance,omitempty"`
	Project  string `json:"project,omitempty"`
	Charset  string `json:"charset,omitempty"`
	SelfLink string `json:"selfLink,omitempty"`
}

type SQLUser struct {
	Name                 string                `json:"name"`
	Instance             string                `json:"instance,omitempty"`
	Project              string                `json:"project,omitempty"`
	Host                 string                `json:"host,omitempty"`
	Type                 string                `json:"type,omitempty"`
	ServerRoles          []string              `json:"serverRoles,omitempty"`
	SQLServerUserDetails *SQLServerUserDetails `json:"sqlserverUserDetails,omitempty"`
}

type SQLServerUserDetails struct {
	Disabled    bool     `json:"disabled,omitempty"`
	ServerRoles []string `json:"serverRoles,omitempty"`
}

// sqlUserCredential holds a user's password sealed under the Cloud-SQL-owned
// Cloud KMS key — never in the clear, as on Google Cloud, where the password
// is write-only on the wire and encrypted at rest.
type sqlUserCredential struct {
	Sealed []byte `json:"sealed"`
}

// SQLOperation mirrors the v1 sqladmin Operation envelope, which
// differs from the cloud.google.com/operations.v1 envelope used by
// other GCP services (Memorystore / APIGW use the latter). The sim
// emits a simplified done-immediately version.
type SQLOperation struct {
	Kind          string                 `json:"kind"`
	Name          string                 `json:"name"`
	OperationType string                 `json:"operationType,omitempty"`
	Status        string                 `json:"status"`
	TargetProject string                 `json:"targetProject,omitempty"`
	TargetID      string                 `json:"targetId,omitempty"`
	InsertTime    string                 `json:"insertTime,omitempty"`
	EndTime       string                 `json:"endTime,omitempty"`
	Error         *SQLOperationErrorList `json:"error,omitempty"`
	SelfLink      string                 `json:"selfLink,omitempty"`
}

// SQLBackupRun models the per-instance backup state machine:
//
//	(insert)  → ENQUEUED
//	(internal-settle)        → RUNNING (transient — sim collapses)
//	(internal-settle)        → SUCCESSFUL
//	(delete)                 → row removed
//
// Real Cloud SQL exposes Status field; tests + tf-provider read it
// during the backup-completion wait loop. Sim collapses the in-flight
// states into SUCCESSFUL inline (documented per
// sim-state-machine-completeness skill).
type SQLBackupRun struct {
	Kind         string `json:"kind"`
	ID           int64  `json:"id,string"`
	Instance     string `json:"instance"`
	Description  string `json:"description,omitempty"`
	Status       string `json:"status"` // ENQUEUED|RUNNING|SUCCESSFUL|FAILED
	EnqueuedTime string `json:"enqueuedTime,omitempty"`
	StartTime    string `json:"startTime,omitempty"`
	EndTime      string `json:"endTime,omitempty"`
	Type         string `json:"type,omitempty"` // ON_DEMAND|AUTOMATED
	SelfLink     string `json:"selfLink,omitempty"`
}

// SQLSslCert mirrors the Cloud SQL SslCert resource — the per-instance
// client/server certificate the sslCerts resource manages.
type SQLSslCert struct {
	Kind             string `json:"kind"`
	CertSerialNumber string `json:"certSerialNumber,omitempty"`
	Cert             string `json:"cert,omitempty"`
	CreateTime       string `json:"createTime,omitempty"`
	CommonName       string `json:"commonName,omitempty"`
	ExpirationTime   string `json:"expirationTime,omitempty"`
	Sha1Fingerprint  string `json:"sha1Fingerprint,omitempty"`
	Instance         string `json:"instance,omitempty"`
	SelfLink         string `json:"selfLink,omitempty"`
}

// SQLBackup mirrors the Cloud SQL Backups resource (the projects.backups
// surface, distinct from the legacy per-instance backupRuns). The
// resource name is `projects/{project}/backups/{backup}`.
type SQLBackup struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Instance        string `json:"instance,omitempty"`
	Description     string `json:"description,omitempty"`
	Location        string `json:"location,omitempty"`
	State           string `json:"state,omitempty"`
	Type            string `json:"type,omitempty"`
	BackupKind      string `json:"backupKind,omitempty"`
	DatabaseVersion string `json:"databaseVersion,omitempty"`
	ExpiryTime      string `json:"expiryTime,omitempty"`
	SelfLink        string `json:"selfLink,omitempty"`
}

var (
	sqlInstances   sim.Store[SQLInstance]
	sqlDatabases   sim.Store[SQLDatabase]
	sqlUsers       sim.Store[SQLUser]
	sqlUserSecrets sim.Store[sqlUserCredential]
	sqlBackupRuns  sim.Store[SQLBackupRun]
	sqlOperations  sim.Store[SQLOperation]
	sqlSslCerts    sim.Store[SQLSslCert]
	sqlBackups     sim.Store[SQLBackup]
)

// sqlInstanceOperationActions are the instances.* action verbs whose real
// Cloud SQL response is the canonical Operation envelope. The simulator
// validates the target instance exists and returns a DONE Operation; the
// underlying database-engine behavior (failover, replica promotion, etc.)
// is not simulated.
var sqlInstanceOperationActions = []string{
	"restart", "failover", "demote", "demoteMaster", "export", "import",
	"reencrypt", "startReplica", "stopReplica",
	"promoteReplica", "switchover", "truncateLog", "resetSslConfig",
	"resetReplicaSize", "rotateServerCa", "rotateServerCertificate",
	"rotateEntraIdCertificate", "addServerCa", "addServerCertificate",
	"addEntraIdCertificate", "startExternalSync", "performDiskShrink",
	"preCheckMajorVersionUpgrade", "rescheduleMaintenance",
}

func registerCloudSQL(srv *sim.Server) {
	sqlInstances = sim.MakeStore[SQLInstance](srv.DB(), "sql_instances")
	sqlDatabases = sim.MakeStore[SQLDatabase](srv.DB(), "sql_databases")
	sqlUsers = sim.MakeStore[SQLUser](srv.DB(), "sql_users")
	sqlUserSecrets = sim.MakeStore[sqlUserCredential](srv.DB(), "sql_user_credentials")
	sqlBackupRuns = sim.MakeStore[SQLBackupRun](srv.DB(), "sql_backup_runs")
	sqlOperations = sim.MakeStore[SQLOperation](srv.DB(), "sql_operations")
	sqlSslCerts = sim.MakeStore[SQLSslCert](srv.DB(), "sql_ssl_certs")
	sqlBackups = sim.MakeStore[SQLBackup](srv.DB(), "sql_backups")

	registerCloudSQLPrefix(srv, "/v1")
	registerCloudSQLPrefix(srv, "/sql/v1beta4")

	// instances.pointInTimeRestore's v1beta4 spelling. The /v1 spelling's mux
	// pattern (`POST /v1/projects/{...}`) belongs to Cloud Resource Manager's
	// project colon-verb dispatcher, which forwards this verb to
	// handleSQLPointInTimeRestore; under /sql/v1beta4 the pattern is this
	// module's own, so it is mounted here — once, not per prefix.
	srv.HandleFunc("POST /sql/v1beta4/projects/{projectAction}", func(w http.ResponseWriter, r *http.Request) {
		project, verb, found := gcpCustomMethod(sim.PathParam(r, "projectAction"))
		if !found || verb != "pointInTimeRestore" {
			gcpMethodNotFound(w)
			return
		}
		handleSQLPointInTimeRestore(w, r, project)
	})

	// A persistent control-plane restart rebinds every instance's address
	// and re-adopts the engine containers the earlier process left running.
	// The API-only tier holds no engines and rebinding modeled instances
	// would invent listeners their addresses never promised.
	if sim.RequireContainerRuntime("the Cloud SQL data plane") == nil {
		if err := sqlRecoverDataPlanes(); err != nil {
			log.Fatalf("recover Cloud SQL data planes: %v", err)
		}
	}
}

func registerCloudSQLPrefix(srv *sim.Server, prefix string) {
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances", handleSQLInsertInstance)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}", handleSQLGetInstance)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances", handleSQLListInstances)
	srv.HandleFunc("PATCH "+prefix+"/projects/{project}/instances/{instance}", handleSQLPatchInstance)
	srv.HandleFunc("PUT "+prefix+"/projects/{project}/instances/{instance}", handleSQLUpdateInstance)
	srv.HandleFunc("DELETE "+prefix+"/projects/{project}/instances/{instance}", handleSQLDeleteInstance)

	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/databases", handleSQLInsertDatabase)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/databases/{database}", handleSQLGetDatabase)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/databases", handleSQLListDatabases)
	srv.HandleFunc("PATCH "+prefix+"/projects/{project}/instances/{instance}/databases/{database}", handleSQLPatchDatabase)
	srv.HandleFunc("PUT "+prefix+"/projects/{project}/instances/{instance}/databases/{database}", handleSQLUpdateDatabase)
	srv.HandleFunc("DELETE "+prefix+"/projects/{project}/instances/{instance}/databases/{database}", handleSQLDeleteDatabase)

	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/users", handleSQLInsertUser)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/users/{name}", handleSQLGetUser)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/users", handleSQLListUsers)
	srv.HandleFunc("PUT "+prefix+"/projects/{project}/instances/{instance}/users", handleSQLUpdateUser)
	srv.HandleFunc("DELETE "+prefix+"/projects/{project}/instances/{instance}/users", handleSQLDeleteUser)

	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/backupRuns", handleSQLInsertBackupRun)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/backupRuns", handleSQLListBackupRuns)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/backupRuns/{id}", handleSQLGetBackupRun)
	srv.HandleFunc("DELETE "+prefix+"/projects/{project}/instances/{instance}/backupRuns/{id}", handleSQLDeleteBackupRun)

	// sslCerts resource.
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/sslCerts", handleSQLInsertSslCert)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/sslCerts", handleSQLListSslCerts)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/sslCerts/{sha1Fingerprint}", handleSQLGetSslCert)
	srv.HandleFunc("DELETE "+prefix+"/projects/{project}/instances/{instance}/sslCerts/{sha1Fingerprint}", handleSQLDeleteSslCert)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/createEphemeral", handleSQLCreateEphemeralCert)

	// connect resource (connectSettings + :generateEphemeralCert colon-verb +
	// the DNS-name resolve, `locations/{location}/dns/{dnsName}:resolveConnectSettings`).
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/connectSettings", handleSQLConnectGet)
	srv.HandleFunc("GET "+prefix+"/locations/{location}/dns/{dnsNameAction}", handleSQLConnectResolve)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}", handleSQLInstanceColonVerb)

	// instances GET sub-resources.
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/getDiskShrinkConfig", handleSQLGetDiskShrinkConfig)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/getLatestRecoveryTime", handleSQLGetLatestRecoveryTime)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/listServerCas", handleSQLListServerCas)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/listServerCertificates", handleSQLListServerCertificates)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/listEntraIdCertificates", handleSQLListEntraIdCertificates)

	// instances action POSTs returning an Operation.
	for _, action := range sqlInstanceOperationActions {
		srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/"+action, handleSQLInstanceAction(action))
	}
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/clone", handleSQLCloneInstance)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/restoreBackup", handleSQLRestoreBackup)

	// instances action POSTs with bespoke response shapes.
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/executeSql", handleSQLExecuteSql)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/acquireSsrsLease", handleSQLAcquireSsrsLease)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/releaseSsrsLease", handleSQLReleaseSsrsLease)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/verifyExternalSyncSettings", handleSQLVerifyExternalSyncSettings)

	// Backups resource. (instances.pointInTimeRestore is the project-scoped
	// colon-verb `projects/{project}:pointInTimeRestore`; its /v1 Go-mux
	// spelling `POST /v1/projects/{...}` is owned by Cloud Resource Manager's
	// project colon-verb dispatcher on the collapsed single-port mux, which
	// forwards the verb here — see registerCloudSQL for the /sql/v1beta4
	// spelling this module mounts itself.)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/backups", handleSQLCreateBackup)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/backups", handleSQLListBackups)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/backups/{backup}", handleSQLGetBackup)
	srv.HandleFunc("PATCH "+prefix+"/projects/{project}/backups/{backup}", handleSQLUpdateBackup)
	srv.HandleFunc("DELETE "+prefix+"/projects/{project}/backups/{backup}", handleSQLDeleteBackup)

	srv.HandleFunc("GET "+prefix+"/projects/{project}/operations/{operation}", handleSQLGetOperation)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/operations", handleSQLListOperations)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/operations/{operation}/cancel", handleSQLCancelOperation)

	srv.HandleFunc("GET "+prefix+"/projects/{project}/tiers", handleSQLListTiers)
	srv.HandleFunc("GET "+prefix+"/flags", handleSQLListFlags)
}

func sqlBackupRunKey(project, instance string, id int64) string {
	return project + "/" + instance + "/" + strconv.FormatInt(id, 10)
}

func handleSQLInsertBackupRun(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	id := time.Now().UTC().UnixNano()
	now := nowTimestamp()
	br := SQLBackupRun{
		Kind:     "sql#backupRun",
		ID:       id,
		Instance: instance,
		// The run answers ENQUEUED and settles once the instance's volume is
		// captured — real work backs the state machine now: the capture is
		// copy-on-write where the engine's volume store supports block
		// cloning, and a full copy elsewhere.
		Status:       "ENQUEUED",
		EnqueuedTime: now,
		Type:         "ON_DEMAND",
		SelfLink:     gcpSelfLink(r, sqlAPIPrefix(r)+"/projects/"+project+"/instances/"+instance+"/backupRuns/"+strconv.FormatInt(id, 10)),
	}
	sqlBackupRuns.Put(sqlBackupRunKey(project, instance, id), br)
	op := newSQLOperationRunning(project, "BACKUP_VOLUME", instance)
	opName := op.Name
	simGo(func() {
		sqlBackupRuns.Update(sqlBackupRunKey(project, instance, id), func(b *SQLBackupRun) {
			b.Status = "RUNNING"
			b.StartTime = nowTimestamp()
		})
		captureErr := sqlCaptureVolume(project, instance, sqlBackupRunVolume(project, instance, id))
		status := "SUCCESSFUL"
		if captureErr != nil {
			status = "FAILED"
		}
		if !sqlBackupRuns.Update(sqlBackupRunKey(project, instance, id), func(b *SQLBackupRun) {
			b.Status = status
			b.EndTime = nowTimestamp()
		}) {
			// The run was deleted while its capture ran; the volume, if the
			// capture made one, goes with it.
			sqlRemoveBackupVolume(sqlBackupRunVolume(project, instance, id))
		}
		sqlSettleOperation(project, opName, captureErr)
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLListBackupRuns(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	prefix := project + "/" + instance + "/"
	all := sqlBackupRuns.Filter(func(b SQLBackupRun) bool {
		return strings.HasPrefix(sqlBackupRunKey(project, b.Instance, b.ID), prefix)
	})
	if all == nil {
		all = []SQLBackupRun{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":  "sql#backupRunsList",
		"items": all,
	})
}

func handleSQLGetBackupRun(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	id, ok := parseSQLBackupRunID(w, sim.PathParam(r, "id"))
	if !ok {
		return
	}
	br, ok := sqlBackupRuns.Get(sqlBackupRunKey(project, instance, id))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"backupRun %q on instance %q not found", sim.PathParam(r, "id"), instance)
		return
	}
	sim.WriteJSON(w, http.StatusOK, br)
}

func handleSQLDeleteBackupRun(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	id, ok := parseSQLBackupRunID(w, sim.PathParam(r, "id"))
	if !ok {
		return
	}
	if !sqlBackupRuns.Delete(sqlBackupRunKey(project, instance, id)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"backupRun %q on instance %q not found", sim.PathParam(r, "id"), instance)
		return
	}
	sqlRemoveBackupVolume(sqlBackupRunVolume(project, instance, id))
	op := newSQLOperation(project, "DELETE_BACKUP", instance)
	sim.WriteJSON(w, http.StatusOK, op)
}

func parseSQLBackupRunID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "backupRun id must be an int64: %s", raw)
		return 0, false
	}
	return id, true
}

// handleSQLCloneInstance creates a new instance from an existing
// source. Real Cloud SQL emits an LRO; sim returns the canonical
// completed Operation shape inline.
func handleSQLCloneInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	source := sim.PathParam(r, "instance")
	src, ok := sqlInstances.Get(sqlInstanceKey(project, source))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"source instance %q not found", source)
		return
	}
	var req struct {
		CloneContext struct {
			DestinationInstanceName string `json:"destinationInstanceName"`
		} `json:"cloneContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	dest := req.CloneContext.DestinationInstanceName
	if dest == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"cloneContext.destinationInstanceName is required")
		return
	}
	if _, exists := sqlInstances.Get(sqlInstanceKey(project, dest)); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
			"instance %q already exists", dest)
		return
	}
	cloned := src
	cloned.Name = dest
	cloned.SelfLink = gcpSelfLink(r, sqlAPIPrefix(r)+"/projects/"+project+"/instances/"+dest)
	// The clone carries the source's users, their credentials, and — through
	// the volume clone below — its data, as a Cloud SQL clone does.
	for _, u := range sqlUsers.List() {
		if u.Project != project || u.Instance != source {
			continue
		}
		clonedUser := u
		clonedUser.Instance = dest
		sqlUsers.Put(sqlUserKey(project, dest, u.Host, u.Name), clonedUser)
		if credential, ok := sqlUserSecrets.Get(sqlUserKey(project, source, u.Host, u.Name)); ok {
			sqlUserSecrets.Put(sqlUserKey(project, dest, u.Host, u.Name), credential)
		}
	}
	installed, installErr := sqlInstallDataPlane(&cloned)
	if installErr != nil || !installed {
		cloned.IpAddresses = []map[string]any{
			{"type": "PRIMARY", "ipAddress": "10.0.0.1"},
		}
	}
	sqlInstances.Put(sqlInstanceKey(project, dest), cloned)
	op := newSQLOperationRunning(project, "CLONE", dest)
	opName := op.Name
	simGo(func() {
		sqlSettleOperation(project, opName, sqlCloneVolume(project, source, dest))
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

// sqlParseBackupDRDatasource splits the Backup and Disaster Recovery Service
// datasource URI PointInTimeRestoreContext carries
// (projects/{project}/locations/{region}/backupVaults/{vault}/dataSources/{datasource})
// into the coordinates of the Cloud SQL instance whose backups it names. This
// simulator hosts no Backup and Disaster Recovery service, so the datasource
// segment is the protected instance's own name — the coordinate a deployment
// against this simulator configures, the way every other cross-service name
// here resolves.
func sqlParseBackupDRDatasource(name string) (project, instance string, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 8 || parts[0] != "projects" || parts[2] != "locations" ||
		parts[4] != "backupVaults" || parts[6] != "dataSources" {
		return "", "", false
	}
	for _, p := range parts[1:] {
		if p == "" {
			return "", "", false
		}
	}
	return parts[1], parts[7], true
}

// handleSQLPointInTimeRestore serves instances.pointInTimeRestore: a restore
// to a NEW instance from the source instance's backups, at the requested
// point in time. The restore clones the newest successful backup run captured
// at or before pointInTime — the backup-volume machinery
// sqladmin_backup_storage.go documents — so the target boots on the data the
// source held then. project is the URI parent
// (`projects/{project}:pointInTimeRestore`); the target instance is created
// in it. The route is dispatched from Cloud Resource Manager's project
// colon-verb fan-in for /v1 and from this module's own /sql/v1beta4 mount.
func handleSQLPointInTimeRestore(w http.ResponseWriter, r *http.Request, project string) {
	var req struct {
		Datasource     string `json:"datasource"`
		PointInTime    string `json:"pointInTime"`
		TargetInstance string `json:"targetInstance"`
		PreferredZone  string `json:"preferredZone"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.PointInTime == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "pointInTime is required")
		return
	}
	pointInTime, err := time.Parse(time.RFC3339, req.PointInTime)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "pointInTime must be an RFC 3339 timestamp: %v", err)
		return
	}
	if req.TargetInstance == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "targetInstance is required")
		return
	}
	// The target is named bare or as projects/{project}/instances/{name};
	// either way the instance lands in the request's parent project.
	target := req.TargetInstance
	if i := strings.LastIndex(target, "/"); i >= 0 {
		target = target[i+1:]
	}
	sourceProject, sourceInstance, ok := sqlParseBackupDRDatasource(req.Datasource)
	if !ok {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"datasource must be of the form projects/{project}/locations/{region}/backupVaults/{backupvault}/dataSources/{datasource}, got %q", req.Datasource)
		return
	}
	src, ok := sqlInstances.Get(sqlInstanceKey(sourceProject, sourceInstance))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "datasource %q names no Cloud SQL instance", req.Datasource)
		return
	}
	if _, exists := sqlInstances.Get(sqlInstanceKey(project, target)); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "instance %q already exists", target)
		return
	}

	// The newest successful backup run captured at or before the requested
	// time is the state the target restores to. No such run means the
	// requested time predates the instance's backup history — nothing real
	// to restore.
	var backupRun *SQLBackupRun
	for _, run := range sqlBackupRuns.List() {
		if run.Instance != sourceInstance || run.Status != "SUCCESSFUL" {
			continue
		}
		// A backup-run row carries no project of its own; the row under the
		// source project's key is the source's run, not a same-named
		// instance's in another project.
		if _, owned := sqlBackupRuns.Get(sqlBackupRunKey(sourceProject, sourceInstance, run.ID)); !owned {
			continue
		}
		captured, err := time.Parse(time.RFC3339, run.EndTime)
		if err != nil || captured.After(pointInTime) {
			continue
		}
		if backupRun == nil || run.EndTime > backupRun.EndTime {
			run := run
			backupRun = &run
		}
	}
	if backupRun == nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"no backup of instance %q exists at or before %s; the requested point in time cannot be restored", sourceInstance, req.PointInTime)
		return
	}

	// The target instance carries the source's configuration, users and
	// credentials, the way instances.clone builds its destination.
	restored := src
	restored.Name = target
	restored.Project = project
	restored.ConnectionName = fmt.Sprintf("%s:%s:%s", project, restored.Region, target)
	if req.PreferredZone != "" {
		restored.GceZone = req.PreferredZone
	}
	restored.SelfLink = gcpSelfLink(r, sqlAPIPrefix(r)+"/projects/"+project+"/instances/"+target)
	for _, u := range sqlUsers.List() {
		if u.Project != sourceProject || u.Instance != sourceInstance {
			continue
		}
		restoredUser := u
		restoredUser.Project = project
		restoredUser.Instance = target
		sqlUsers.Put(sqlUserKey(project, target, u.Host, u.Name), restoredUser)
		if credential, ok := sqlUserSecrets.Get(sqlUserKey(sourceProject, sourceInstance, u.Host, u.Name)); ok {
			sqlUserSecrets.Put(sqlUserKey(project, target, u.Host, u.Name), credential)
		}
	}
	installed, installErr := sqlInstallDataPlane(&restored)
	if installErr != nil || !installed {
		restored.IpAddresses = []map[string]any{
			{"type": "PRIMARY", "ipAddress": "10.0.0.1"},
		}
	}
	sqlInstances.Put(sqlInstanceKey(project, target), restored)

	// The vendored Operation.operationType enum publishes no point-in-time
	// value; the service performs this restore as a clone to a new instance,
	// and CLONE is the type the clone-based point-in-time path reports.
	op := newSQLOperationRunning(project, "CLONE", target)
	opName := op.Name
	backupVolume := sqlBackupRunVolume(sourceProject, sourceInstance, backupRun.ID)
	simGo(func() {
		sqlSettleOperation(project, opName, sqlRestoreVolume(project, target, backupVolume))
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

func sqlInstanceKey(project, instance string) string {
	return fmt.Sprintf("%s/%s", project, instance)
}

func sqlDatabaseKey(project, instance, database string) string {
	return fmt.Sprintf("%s/%s/%s", project, instance, database)
}

func sqlUserKey(project, instance, host, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, instance, host, name)
}

func sqlOperationKey(project, operation string) string {
	return project + "/" + operation
}

func sqlAPIPrefix(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/sql/v1beta4/") {
		return "/sql/v1beta4"
	}
	return "/v1"
}

// gcpSelfLink builds a fully-qualified selfLink rooted at the host
// the request arrived on, with `https` hard-coded. Real GCP emits
// `https://<service>.googleapis.com/v1/...` regardless of the
// caller's transport; the sim listens on plain HTTP locally but
// emitting `http://` selfLink URLs breaks downstream
// tooling that strips/expects an HTTPS-only contract. Same shape as
// the GCS `gcsObjectMetadata` hard-coded-https fix
func gcpSelfLink(r *http.Request, path string) string {
	host := r.Host
	if host == "" {
		host = "sqladmin.googleapis.com"
	}
	return fmt.Sprintf("https://%s%s", host, path)
}

func newSQLOperation(project, opType, targetID string) SQLOperation {
	now := nowTimestamp()
	op := SQLOperation{
		Kind:          "sql#operation",
		Name:          generateUUID(),
		OperationType: opType,
		Status:        "DONE",
		TargetProject: project,
		TargetID:      targetID,
		InsertTime:    now,
		EndTime:       now,
	}
	if sqlOperations != nil {
		sqlOperations.Put(sqlOperationKey(project, op.Name), op)
	}
	return op
}

// SQLOperationErrorList is the sql#operationErrors envelope a failed
// operation carries.
type SQLOperationErrorList struct {
	Kind   string              `json:"kind"`
	Errors []SQLOperationError `json:"errors"`
}

type SQLOperationError struct {
	Kind    string `json:"kind"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// newSQLOperationRunning mints a RUNNING operation for work that settles in
// the background — a backup capturing a volume, a restore cloning one back.
// Clients poll operations.get until DONE, exactly as against Cloud SQL.
func newSQLOperationRunning(project, opType, targetID string) SQLOperation {
	op := SQLOperation{
		Kind:          "sql#operation",
		Name:          generateUUID(),
		OperationType: opType,
		Status:        "RUNNING",
		TargetProject: project,
		TargetID:      targetID,
		InsertTime:    nowTimestamp(),
	}
	sqlOperations.Put(sqlOperationKey(project, op.Name), op)
	return op
}

// sqlSettleOperation completes a RUNNING operation, recording the failure —
// in the sql#operationErrors shape — when the work it tracked failed.
func sqlSettleOperation(project, name string, workErr error) {
	sqlOperations.Update(sqlOperationKey(project, name), func(op *SQLOperation) {
		op.Status = "DONE"
		op.EndTime = nowTimestamp()
		if workErr != nil {
			op.Error = &SQLOperationErrorList{
				Kind: "sql#operationErrors",
				Errors: []SQLOperationError{{
					Kind: "sql#operationError", Code: "INTERNAL_ERROR", Message: workErr.Error(),
				}},
			}
		}
	})
}

func handleSQLGetOperation(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "operation")
	op, ok := sqlOperations.Get(sqlOperationKey(project, name))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLListOperations(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var out []SQLOperation
	for _, op := range sqlOperations.List() {
		if op.TargetProject == project {
			out = append(out, op)
		}
	}
	if out == nil {
		out = []SQLOperation{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "sql#operationsList", "items": out})
}

func firstSQLUser(project, instance, name, host string) (SQLUser, bool) {
	if host != "" {
		return sqlUsers.Get(sqlUserKey(project, instance, host, name))
	}
	for _, u := range sqlUsers.List() {
		if u.Project == project && u.Instance == instance && u.Name == name {
			return u, true
		}
	}
	return SQLUser{}, false
}

func sqlOnPremisesConfiguration(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	// This simulator does not attach an on-premises source to Database
	// Migration Service, so the published output-only state is false.
	out["dmsManaged"] = false
	return out
}

func mergeSQLServerRoles(existing, added []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(added))
	merged := make([]string, 0, len(existing)+len(added))
	for _, role := range append(append([]string(nil), existing...), added...) {
		if _, duplicate := seen[role]; duplicate {
			continue
		}
		seen[role] = struct{}{}
		merged = append(merged, role)
	}
	return merged
}

func copySQLServerUserDetails(details *SQLServerUserDetails, roles []string) *SQLServerUserDetails {
	if details == nil && roles == nil {
		return nil
	}
	out := &SQLServerUserDetails{ServerRoles: append([]string(nil), roles...)}
	if details != nil {
		out.Disabled = details.Disabled
	}
	return out
}

func handleSQLInsertInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var wire struct {
		SQLInstance
		// rootPassword is write-only on the wire: it becomes the built-in
		// admin user's credential and never appears in a response.
		RootPassword string `json:"rootPassword"`
	}
	if err := sim.ReadJSON(r, &wire); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	req := wire.SQLInstance
	if req.Name == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name is required")
		return
	}
	inst := SQLInstance{
		Name:                             req.Name,
		Project:                          project,
		Region:                           defaultStr(req.Region, "us-central1"),
		DatabaseVersion:                  defaultStr(req.DatabaseVersion, "POSTGRES_15"),
		State:                            "RUNNABLE",
		BackendType:                      "SECOND_GEN",
		InstanceType:                     "CLOUD_SQL_INSTANCE",
		ConnectionName:                   fmt.Sprintf("%s:%s:%s", project, defaultStr(req.Region, "us-central1"), req.Name),
		CreateTime:                       nowTimestamp(),
		DatabaseCenterIntegrationEnabled: req.DatabaseCenterIntegrationEnabled,
		OnPremisesConfiguration:          sqlOnPremisesConfiguration(req.OnPremisesConfiguration),
		Settings:                         req.Settings,
		SelfLink:                         gcpSelfLink(r, fmt.Sprintf("%s/projects/%s/instances/%s", sqlAPIPrefix(r), project, req.Name)),
	}
	// The PRIMARY address is a listener this process owns at the engine's
	// conventional port. A host that cannot provide one (no container
	// runtime, or no loopback address offers the port) leaves the instance
	// modeled with the nominal address the slice always fabricated — said
	// out loud below, never silently.
	installed, err := sqlInstallDataPlane(&inst)
	if err != nil || !installed {
		inst.IpAddresses = []map[string]any{
			{"type": "PRIMARY", "ipAddress": "10.0.0.1"},
		}
		if family, hasEngine := sqlEngineFamily(inst.DatabaseVersion); hasEngine {
			reason := "this simulator was started API-only"
			if err != nil {
				reason = err.Error()
			} else if sim.RequireContainerRuntime("the Cloud SQL data plane") == nil {
				reason = "the host offers no loopback address at the engine's port"
			}
			fmt.Fprintf(os.Stderr, "[sim-cloudsql] instance %s/%s (%s) is modeled without a data plane: %s\n",
				project, req.Name, family, reason)
		}
	}
	sqlInstances.Put(sqlInstanceKey(project, req.Name), inst)
	// The built-in admin user Cloud SQL creates with the instance — postgres
	// for PostgreSQL, root for MySQL — listed by users.list like any other.
	if family, ok := sqlEngineFamily(inst.DatabaseVersion); ok {
		admin := sqlBuiltInAdminUser(family)
		host := ""
		if family == "mysql" {
			host = "%"
		}
		sqlUsers.Put(sqlUserKey(project, req.Name, host, admin), SQLUser{
			Name: admin, Instance: req.Name, Project: project, Host: host, Type: "BUILT_IN",
		})
		if wire.RootPassword != "" {
			sealed, sealErr := sqlSealSecret(wire.RootPassword)
			if sealErr != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "seal root credential: %s", sealErr.Error())
				return
			}
			sqlUserSecrets.Put(sqlUserKey(project, req.Name, host, admin), sqlUserCredential{Sealed: sealed})
		}
	}
	op := newSQLOperation(project, "CREATE", req.Name)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLGetInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, name))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, inst)
}

func handleSQLListInstances(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var out []SQLInstance
	for _, i := range sqlInstances.List() {
		if i.Project == project {
			out = append(out, i)
		}
	}
	if out == nil {
		out = []SQLInstance{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "sql#instancesList", "items": out})
}

func handleSQLPatchInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "instance")
	key := sqlInstanceKey(project, name)
	if _, ok := sqlInstances.Get(key); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	var req SQLInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	sqlInstances.Update(key, func(i *SQLInstance) {
		if req.DatabaseVersion != "" {
			i.DatabaseVersion = req.DatabaseVersion
		}
		if req.DatabaseCenterIntegrationEnabled != nil {
			i.DatabaseCenterIntegrationEnabled = req.DatabaseCenterIntegrationEnabled
		}
		if req.OnPremisesConfiguration != nil {
			i.OnPremisesConfiguration = sqlOnPremisesConfiguration(req.OnPremisesConfiguration)
		}
		// instances.patch merges settings.* sub-fields: keys present in the
		// patch body replace, keys it omits are preserved (a wholesale
		// replace would drop tier/backupConfiguration on a flags-only
		// patch). settingsVersion bumps on every successful update, as
		// the Cloud SQL Admin API requires for optimistic concurrency.
		if req.Settings != nil {
			if i.Settings == nil {
				i.Settings = map[string]any{}
			}
			for k, v := range req.Settings {
				i.Settings[k] = v
			}
		}
		if i.Settings != nil {
			i.Settings["settingsVersion"] = sqlNextSettingsVersion(i.Settings)
		}
	})
	op := newSQLOperation(project, "UPDATE", name)
	sim.WriteJSON(w, http.StatusOK, op)
}

// sqlNextSettingsVersion returns the next settingsVersion (current + 1, or 1
// when unset). The Cloud SQL Admin API encodes settingsVersion as a
// string-quoted int64, so emit a string.
func sqlNextSettingsVersion(settings map[string]any) string {
	cur := int64(0)
	switch v := settings["settingsVersion"].(type) {
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cur = n
		}
	case float64:
		cur = int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			cur = n
		}
	}
	return strconv.FormatInt(cur+1, 10)
}

func handleSQLDeleteInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "instance")
	if !sqlInstances.Delete(sqlInstanceKey(project, name)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	sqlStopDataPlane(project, name, true)
	// Cascade-clear databases + users.
	for _, d := range sqlDatabases.List() {
		if d.Instance == name && d.Project == project {
			sqlDatabases.Delete(sqlDatabaseKey(project, name, d.Name))
		}
	}
	for _, u := range sqlUsers.List() {
		if u.Instance == name && u.Project == project {
			key := sqlUserKey(project, name, u.Host, u.Name)
			sqlUsers.Delete(key)
			sqlUserSecrets.Delete(key)
		}
	}
	// Backup runs belong to the instance and go with it, volumes included —
	// the retained projects/{project}/backups resources survive, which is
	// their whole point.
	for _, b := range sqlBackupRuns.List() {
		if b.Instance == name {
			if sqlBackupRuns.Delete(sqlBackupRunKey(project, name, b.ID)) {
				sqlRemoveBackupVolume(sqlBackupRunVolume(project, name, b.ID))
			}
		}
	}
	op := newSQLOperation(project, "DELETE", name)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLInsertDatabase(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	var req SQLDatabase
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.Name == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name is required")
		return
	}
	db := SQLDatabase{
		Name:     req.Name,
		Instance: instance,
		Project:  project,
		Charset:  defaultStr(req.Charset, "UTF8"),
		SelfLink: gcpSelfLink(r, fmt.Sprintf("%s/projects/%s/instances/%s/databases/%s", sqlAPIPrefix(r), project, instance, req.Name)),
	}
	sqlDatabases.Put(sqlDatabaseKey(project, instance, req.Name), db)
	if err := sqlReconcileIfRunning(project, instance); err != nil {
		sim.GCPErrorf(w, http.StatusConflict, "FAILED_PRECONDITION", "create database in the engine: %s", err.Error())
		return
	}
	op := newSQLOperation(project, "CREATE_DATABASE", req.Name)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLGetDatabase(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	name := sim.PathParam(r, "database")
	db, ok := sqlDatabases.Get(sqlDatabaseKey(project, instance, name))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, db)
}

func handleSQLListDatabases(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	var out []SQLDatabase
	for _, d := range sqlDatabases.List() {
		if d.Project == project && d.Instance == instance {
			out = append(out, d)
		}
	}
	if out == nil {
		out = []SQLDatabase{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "sql#databasesList", "items": out})
}

func handleSQLDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	name := sim.PathParam(r, "database")
	key := sqlDatabaseKey(project, instance, name)
	if !sqlDatabases.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database not found: %s", name)
		return
	}
	if err := sqlEngineDropDatabaseIfRunning(project, instance, name); err != nil {
		sim.GCPErrorf(w, http.StatusConflict, "FAILED_PRECONDITION", "drop database from the engine: %s", err.Error())
		return
	}
	op := newSQLOperation(project, "DELETE_DATABASE", name)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLInsertUser(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	var req SQLUser
	var wire struct {
		SQLUser
		Password string `json:"password"`
	}
	if err := sim.ReadJSON(r, &wire); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	req = wire.SQLUser
	if req.Name == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name is required")
		return
	}
	serverRoles := req.ServerRoles
	if serverRoles == nil && req.SQLServerUserDetails != nil {
		serverRoles = req.SQLServerUserDetails.ServerRoles
	}
	u := SQLUser{
		Name:                 req.Name,
		Instance:             instance,
		Project:              project,
		Host:                 req.Host,
		Type:                 defaultStr(req.Type, "BUILT_IN"),
		ServerRoles:          append([]string(nil), serverRoles...),
		SQLServerUserDetails: copySQLServerUserDetails(req.SQLServerUserDetails, serverRoles),
	}
	sqlUsers.Put(sqlUserKey(project, instance, req.Host, req.Name), u)
	if wire.Password != "" {
		sealed, err := sqlSealSecret(wire.Password)
		if err != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "seal user credential: %s", err.Error())
			return
		}
		sqlUserSecrets.Put(sqlUserKey(project, instance, req.Host, req.Name), sqlUserCredential{Sealed: sealed})
	}
	if err := sqlReconcileIfRunning(project, instance); err != nil {
		sim.GCPErrorf(w, http.StatusConflict, "FAILED_PRECONDITION", "apply user to the database engine: %s", err.Error())
		return
	}
	op := newSQLOperation(project, "CREATE_USER", req.Name)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLGetUser(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	name := sim.PathParam(r, "name")
	host := r.URL.Query().Get("host")
	u, ok := firstSQLUser(project, instance, name, host)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "user not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, u)
}

func handleSQLListUsers(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	var out []SQLUser
	for _, u := range sqlUsers.List() {
		if u.Project == project && u.Instance == instance {
			out = append(out, u)
		}
	}
	if out == nil {
		out = []SQLUser{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "sql#usersList", "items": out})
}

func handleSQLUpdateUser(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	name := r.URL.Query().Get("name")
	host := r.URL.Query().Get("host")
	if name == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name query parameter is required")
		return
	}
	current, ok := firstSQLUser(project, instance, name, host)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "user not found: %s", name)
		return
	}
	var wire struct {
		SQLUser
		Password string `json:"password"`
	}
	if err := sim.ReadJSON(r, &wire); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	req := wire.SQLUser
	oldKey := sqlUserKey(project, instance, current.Host, current.Name)
	if req.Host != "" && req.Host != current.Host {
		sqlUsers.Delete(oldKey)
		if credential, exists := sqlUserSecrets.Get(oldKey); exists {
			sqlUserSecrets.Delete(oldKey)
			sqlUserSecrets.Put(sqlUserKey(project, instance, req.Host, current.Name), credential)
		}
		current.Host = req.Host
	}
	if req.Type != "" {
		current.Type = req.Type
	}
	serverRoles := req.ServerRoles
	if serverRoles == nil && req.SQLServerUserDetails != nil {
		serverRoles = req.SQLServerUserDetails.ServerRoles
	}
	if serverRoles != nil {
		if r.URL.Query().Get("revokeExistingServerRoles") == "true" {
			current.ServerRoles = append([]string(nil), serverRoles...)
		} else {
			current.ServerRoles = mergeSQLServerRoles(current.ServerRoles, serverRoles)
		}
	}
	if req.SQLServerUserDetails != nil {
		current.SQLServerUserDetails = copySQLServerUserDetails(req.SQLServerUserDetails, current.ServerRoles)
	} else if current.SQLServerUserDetails != nil {
		current.SQLServerUserDetails.ServerRoles = append([]string(nil), current.ServerRoles...)
	}
	sqlUsers.Put(sqlUserKey(project, instance, current.Host, current.Name), current)
	if wire.Password != "" {
		sealed, err := sqlSealSecret(wire.Password)
		if err != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "seal user credential: %s", err.Error())
			return
		}
		sqlUserSecrets.Put(sqlUserKey(project, instance, current.Host, current.Name), sqlUserCredential{Sealed: sealed})
	}
	if err := sqlReconcileIfRunning(project, instance); err != nil {
		sim.GCPErrorf(w, http.StatusConflict, "FAILED_PRECONDITION", "apply user to the database engine: %s", err.Error())
		return
	}
	op := newSQLOperation(project, "UPDATE_USER", name)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLDeleteUser(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	name := r.URL.Query().Get("name")
	host := r.URL.Query().Get("host")
	if name == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name query parameter is required")
		return
	}
	u, ok := firstSQLUser(project, instance, name, host)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "user not found: %s", name)
		return
	}
	key := sqlUserKey(project, instance, u.Host, u.Name)
	sqlUsers.Delete(key)
	sqlUserSecrets.Delete(key)
	if err := sqlEngineDropUserIfRunning(project, instance, u.Name); err != nil {
		sim.GCPErrorf(w, http.StatusConflict, "FAILED_PRECONDITION", "drop user from the database engine: %s", err.Error())
		return
	}
	op := newSQLOperation(project, "DELETE_USER", name)
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleSQLUpdateInstance implements instances.update — a full-replace PUT
// that returns an Operation. Unlike instances.patch (a sub-field merge),
// update replaces the mutable instance fields wholesale, bumping
// settingsVersion for the optimistic-concurrency contract.
func handleSQLUpdateInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "instance")
	key := sqlInstanceKey(project, name)
	if _, ok := sqlInstances.Get(key); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return
	}
	var req SQLInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	sqlInstances.Update(key, func(i *SQLInstance) {
		if req.DatabaseVersion != "" {
			i.DatabaseVersion = req.DatabaseVersion
		}
		i.DatabaseCenterIntegrationEnabled = req.DatabaseCenterIntegrationEnabled
		i.OnPremisesConfiguration = sqlOnPremisesConfiguration(req.OnPremisesConfiguration)
		// update is a full replace of settings; the previous map is
		// discarded. settingsVersion still advances.
		next := sqlNextSettingsVersion(i.Settings)
		i.Settings = req.Settings
		if i.Settings == nil {
			i.Settings = map[string]any{}
		}
		i.Settings["settingsVersion"] = next
	})
	op := newSQLOperation(project, "UPDATE", name)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLPatchDatabase(w http.ResponseWriter, r *http.Request) {
	handleSQLWriteDatabase(w, r, false)
}

func handleSQLUpdateDatabase(w http.ResponseWriter, r *http.Request) {
	handleSQLWriteDatabase(w, r, true)
}

// handleSQLWriteDatabase backs databases.patch (merge) and databases.update
// (full replace). Both return an Operation in real Cloud SQL.
func handleSQLWriteDatabase(w http.ResponseWriter, r *http.Request, replace bool) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	name := sim.PathParam(r, "database")
	key := sqlDatabaseKey(project, instance, name)
	if _, ok := sqlDatabases.Get(key); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database not found: %s", name)
		return
	}
	var req SQLDatabase
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	sqlDatabases.Update(key, func(d *SQLDatabase) {
		if replace {
			if req.Charset != "" {
				d.Charset = req.Charset
			} else {
				d.Charset = ""
			}
			return
		}
		if req.Charset != "" {
			d.Charset = req.Charset
		}
	})
	op := newSQLOperation(project, "UPDATE_DATABASE", name)
	sim.WriteJSON(w, http.StatusOK, op)
}

// sqlInstanceActionOpType maps an instances.* action verb to its Cloud SQL
// Operation.operationType. Verbs without a distinct enum fall back to the
// uppercased verb.
var sqlInstanceActionOpType = map[string]string{
	"restart":                     "RESTART",
	"failover":                    "FAILOVER",
	"demote":                      "DEMOTE",
	"demoteMaster":                "DEMOTE_MASTER",
	"export":                      "EXPORT",
	"import":                      "IMPORT",
	"reencrypt":                   "REENCRYPT",
	"restoreBackup":               "RESTORE_VOLUME",
	"startReplica":                "START_REPLICA",
	"stopReplica":                 "STOP_REPLICA",
	"promoteReplica":              "PROMOTE_REPLICA",
	"switchover":                  "SWITCHOVER",
	"truncateLog":                 "TRUNCATE_LOG",
	"resetSslConfig":              "RESET_SSL_CONFIG",
	"resetReplicaSize":            "RESET_REPLICA_SIZE",
	"rotateServerCa":              "ROTATE_SERVER_CA",
	"rotateServerCertificate":     "ROTATE_SERVER_CERTIFICATE",
	"rotateEntraIdCertificate":    "ROTATE_ENTRA_ID_CERTIFICATE",
	"addServerCa":                 "ADD_SERVER_CA",
	"addServerCertificate":        "ADD_SERVER_CERTIFICATE",
	"addEntraIdCertificate":       "ADD_ENTRA_ID_CERTIFICATE",
	"startExternalSync":           "START_EXTERNAL_SYNC",
	"performDiskShrink":           "SHRINK_DISK",
	"preCheckMajorVersionUpgrade": "PRE_CHECK_MAJOR_VERSION_UPGRADE",
	"rescheduleMaintenance":       "RESCHEDULE_MAINTENANCE",
}

// handleSQLRestoreBackup restores a backup onto the instance: the engine is
// stopped, its data volume replaced with a clone of the backup's, and the
// next connection boots on the restored data. The RESTORE_VOLUME operation
// runs until the swap completes, as on Cloud SQL.
func handleSQLRestoreBackup(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	var req struct {
		RestoreBackupContext struct {
			BackupRunID json.Number `json:"backupRunId"`
			InstanceID  string      `json:"instanceId"`
			Project     string      `json:"project"`
		} `json:"restoreBackupContext"`
		// The projects/{project}/backups form names the retained backup.
		Backup string `json:"backup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	var backupVolume string
	switch {
	case req.RestoreBackupContext.BackupRunID != "":
		id, err := req.RestoreBackupContext.BackupRunID.Int64()
		if err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "backupRunId must be an int64: %s", req.RestoreBackupContext.BackupRunID)
			return
		}
		sourceInstance := defaultStr(req.RestoreBackupContext.InstanceID, instance)
		sourceProject := defaultStr(req.RestoreBackupContext.Project, project)
		run, ok := sqlBackupRuns.Get(sqlBackupRunKey(sourceProject, sourceInstance, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backupRun %d on instance %q not found", id, sourceInstance)
			return
		}
		if run.Status != "SUCCESSFUL" {
			sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"backupRun %d is %s; it must be SUCCESSFUL to restore", id, run.Status)
			return
		}
		backupVolume = sqlBackupRunVolume(sourceProject, sourceInstance, id)
	case req.Backup != "":
		parts := strings.Split(req.Backup, "/")
		backupID := parts[len(parts)-1]
		backupProject := project
		if len(parts) >= 2 && parts[0] == "projects" {
			backupProject = parts[1]
		}
		backup, ok := sqlBackups.Get(sqlBackupKey(backupProject, backupID))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", req.Backup)
			return
		}
		if backup.State != "SUCCESSFUL" {
			sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"backup %s is %s; it must be SUCCESSFUL to restore", req.Backup, backup.State)
			return
		}
		backupVolume = sqlBackupVolume(backupProject, backupID)
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"restoreBackupContext.backupRunId or backup is required")
		return
	}
	op := newSQLOperationRunning(project, "RESTORE_VOLUME", instance)
	opName := op.Name
	simGo(func() {
		sqlSettleOperation(project, opName, sqlRestoreVolume(project, instance, backupVolume))
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleSQLInstanceAction returns a handler for one instances.* action verb
// whose real response is the canonical Operation envelope.
func handleSQLInstanceAction(action string) http.HandlerFunc {
	opType := sqlInstanceActionOpType[action]
	if opType == "" {
		opType = strings.ToUpper(action)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		instance := sim.PathParam(r, "instance")
		if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
			return
		}
		op := newSQLOperation(project, opType, instance)
		sim.WriteJSON(w, http.StatusOK, op)
	}
}

// handleSQLInstanceColonVerb handles the colon-verb POSTs addressed at
// `instances/{instance}:<verb>` (Go's mux captures the ":verb" suffix in
// the {instance} parameter). Currently `:generateEphemeralCert`.
func handleSQLInstanceColonVerb(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	raw := sim.PathParam(r, "instance")
	instance, verb, hasVerb := strings.Cut(raw, ":")
	if !hasVerb {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown instance operation: %s", raw)
		return
	}
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	switch verb {
	case "generateEphemeralCert":
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"ephemeralCert": sqlNewSslCert(r, project, instance, "ephemeral"),
		})
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown instance verb: %s", verb)
	}
}

// handleSQLExecuteSql serves instances.executeSql. The database engine is
// not simulated, so a faithful empty result set is returned with an OK
// status (the response shape matches SqlInstancesExecuteSqlResponse).
func handleSQLExecuteSql(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"results":  []any{},
		"messages": []any{},
	})
}

func handleSQLAcquireSsrsLease(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	op := newSQLOperation(project, "ACQUIRE_SSRS_LEASE", instance)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operationId": op.Name})
}

func handleSQLReleaseSsrsLease(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	op := newSQLOperation(project, "RELEASE_SSRS_LEASE", instance)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operationId": op.Name})
}

func handleSQLVerifyExternalSyncSettings(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":     "sql#verifyExternalSyncSettings",
		"errors":   []any{},
		"warnings": []any{},
	})
}

func handleSQLGetDiskShrinkConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":                "sql#getDiskShrinkConfig",
		"minimalTargetSizeGb": "10",
	})
}

func handleSQLGetLatestRecoveryTime(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	now := nowTimestamp()
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":               "sql#getLatestRecoveryTime",
		"latestRecoveryTime": now,
	})
}

func handleSQLListServerCas(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	ca := sqlNewSslCert(r, project, instance, "server-ca")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":          "sql#instancesListServerCas",
		"activeVersion": ca.Sha1Fingerprint,
		"certs":         []SQLSslCert{ca},
	})
}

func handleSQLListServerCertificates(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	ca := sqlNewSslCert(r, project, instance, "server-ca")
	srvCert := sqlNewSslCert(r, project, instance, "server-cert")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":          "sql#instancesListServerCertificates",
		"activeVersion": srvCert.Sha1Fingerprint,
		"caCerts":       []SQLSslCert{ca},
		"serverCerts":   []SQLSslCert{srvCert},
	})
}

func handleSQLListEntraIdCertificates(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":  "sql#instancesListEntraIdCertificates",
		"certs": []SQLSslCert{},
	})
}

// sqlNewSslCert builds a deterministic SslCert for an instance. The
// sha1Fingerprint is derived from the instance + role so repeat calls are
// stable. No real key material is generated; the cert field is empty.
func sqlNewSslCert(r *http.Request, project, instance, role string) SQLSslCert {
	fp := fmt.Sprintf("%x", []byte(project+"/"+instance+"/"+role))
	if len(fp) > 40 {
		fp = fp[:40]
	}
	now := nowTimestamp()
	return SQLSslCert{
		Kind:             "sql#sslCert",
		CertSerialNumber: "1",
		CommonName:       role + "." + instance,
		CreateTime:       now,
		ExpirationTime:   now,
		Sha1Fingerprint:  fp,
		Instance:         instance,
		SelfLink:         gcpSelfLink(r, sqlAPIPrefix(r)+"/projects/"+project+"/instances/"+instance+"/sslCerts/"+fp),
	}
}

func sqlSslCertKey(project, instance, fp string) string {
	return project + "/" + instance + "/" + fp
}

func handleSQLInsertSslCert(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	var req struct {
		CommonName string `json:"commonName"`
	}
	_ = sim.ReadJSON(r, &req)
	role := defaultStr(req.CommonName, "client")
	cert := sqlNewSslCert(r, project, instance, "client-"+role)
	sqlSslCerts.Put(sqlSslCertKey(project, instance, cert.Sha1Fingerprint), cert)
	op := newSQLOperation(project, "CREATE_SSL_CERT", instance)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":         "sql#sslCertsInsert",
		"clientCert":   map[string]any{"certInfo": cert},
		"serverCaCert": sqlNewSslCert(r, project, instance, "server-ca"),
		"operation":    op,
	})
}

func handleSQLListSslCerts(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	prefix := project + "/" + instance + "/"
	out := sqlSslCerts.Filter(func(c SQLSslCert) bool {
		return strings.HasPrefix(sqlSslCertKey(project, c.Instance, c.Sha1Fingerprint), prefix)
	})
	if out == nil {
		out = []SQLSslCert{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":  "sql#sslCertsList",
		"items": out,
	})
}

func handleSQLGetSslCert(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	fp := sim.PathParam(r, "sha1Fingerprint")
	c, ok := sqlSslCerts.Get(sqlSslCertKey(project, instance, fp))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sslCert not found: %s", fp)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleSQLDeleteSslCert(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	fp := sim.PathParam(r, "sha1Fingerprint")
	if !sqlSslCerts.Delete(sqlSslCertKey(project, instance, fp)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sslCert not found: %s", fp)
		return
	}
	op := newSQLOperation(project, "DELETE_SSL_CERT", instance)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLCreateEphemeralCert(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sim.WriteJSON(w, http.StatusOK, sqlNewSslCert(r, project, instance, "ephemeral"))
}

// sqlInstanceDNSName is the DNS name of an instance, in the
// "<suffix>.<region>.sql.goog" shape Cloud SQL publishes in
// ConnectSettings.dnsName. Real Cloud SQL mints the suffix as an opaque
// per-instance identifier; this deployment mints it deterministically from
// the instance's identity — the way connectionName is derived — so the name
// is stable across restarts and connect.resolve maps it back to the instance.
func sqlInstanceDNSName(inst SQLInstance) string {
	if inst.Name == "" || inst.Region == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(inst.Project + "/" + inst.Name))
	return hex.EncodeToString(sum[:8]) + "." + inst.Region + ".sql.goog"
}

// sqlConnectSettingsJSON renders the sql#connectSettings document for an
// instance — the payload connect.get serves and connect.resolve serves for
// the same instance found by DNS name.
func sqlConnectSettingsJSON(r *http.Request, inst SQLInstance) map[string]any {
	settings := map[string]any{
		"kind":            "sql#connectSettings",
		"databaseVersion": inst.DatabaseVersion,
		"backendType":     inst.BackendType,
		"region":          inst.Region,
		"ipAddresses":     inst.IpAddresses,
		"serverCaCert":    sqlNewSslCert(r, inst.Project, inst.Name, "server-ca"),
	}
	if dnsName := sqlInstanceDNSName(inst); dnsName != "" {
		settings["dnsName"] = dnsName
	}
	return settings
}

// handleSQLConnectGet serves connect.get (connectSettings), returning the
// instance's connect metadata (region, databaseVersion, IP addresses, CA).
func handleSQLConnectGet(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sim.WriteJSON(w, http.StatusOK, sqlConnectSettingsJSON(r, inst))
}

// handleSQLConnectResolve serves connect.resolve
// (`GET .../locations/{location}/dns/{dnsName}:resolveConnectSettings`):
// the reverse of connect.get, resolving an instance's published DNS name back
// to the same connect-settings document. Go's mux captures the
// "{dnsName}:{verb}" segment whole; the verb resolves first, the way Google's
// frontend resolves a method before the resource.
func handleSQLConnectResolve(w http.ResponseWriter, r *http.Request) {
	location := sim.PathParam(r, "location")
	dnsName, verb, found := gcpCustomMethod(sim.PathParam(r, "dnsNameAction"))
	if !found || verb != "resolveConnectSettings" {
		gcpMethodNotFound(w)
		return
	}
	// A DNS name is spelled with or without the zone-file trailing dot;
	// both address the same record.
	dnsName = strings.TrimSuffix(dnsName, ".")
	for _, inst := range sqlInstances.List() {
		if inst.Region == location && sqlInstanceDNSName(inst) == dnsName {
			sim.WriteJSON(w, http.StatusOK, sqlConnectSettingsJSON(r, inst))
			return
		}
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
		"DNS name %q does not resolve to a Cloud SQL instance in location %q", dnsName, location)
}

// sqlCancellableOperationTypes are the operation types Cloud SQL supports
// cancelling. The service's cancellation documentation is explicit that only
// an import or an export can be cancelled; every other type is refused by
// name.
var sqlCancellableOperationTypes = map[string]bool{"IMPORT": true, "EXPORT": true}

// handleSQLCancelOperation implements sql.operations.cancel. This is Cloud
// SQL's own method — "Cancels an instance operation that has been performed on
// an instance" — not the google.longrunning custom method the other services
// spell, and it does not share that method's best-effort, always-Empty answer.
// Cloud SQL refuses a cancel of an operation that is not in progress and a
// cancel of an operation type it cannot cancel, reporting each with the
// message the service publishes for it. A cancel it accepts returns Empty.
func handleSQLCancelOperation(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "operation")
	op, ok := sqlOperations.Get(sqlOperationKey(project, name))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation not found: %s", name)
		return
	}
	if op.Status == "DONE" {
		sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"You can't cancel operation %s because this operation isn't in progress.", name)
		return
	}
	if !sqlCancellableOperationTypes[op.OperationType] {
		sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"You can't cancel operation %s because Cloud SQL doesn't support the cancellation of an %s operation.",
			name, op.OperationType)
		return
	}
	sqlOperations.Update(sqlOperationKey(project, name), func(o *SQLOperation) {
		o.Status = "DONE"
		o.EndTime = nowTimestamp()
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQLListTiers(w http.ResponseWriter, r *http.Request) {
	// Representative Cloud SQL machine tiers (real db-* / db-custom values
	// the tiers.list API returns; RAM/DiskQuota are string-quoted int64
	// byte counts per the Tier schema).
	tiers := []map[string]any{
		{"kind": "sql#tier", "tier": "db-f1-micro", "RAM": "614400000", "DiskQuota": "3298534883328", "region": []string{"us-central1"}},
		{"kind": "sql#tier", "tier": "db-g1-small", "RAM": "1740800000", "DiskQuota": "3298534883328", "region": []string{"us-central1"}},
		{"kind": "sql#tier", "tier": "db-custom-1-3840", "RAM": "4026531840", "DiskQuota": "32212254720000", "region": []string{"us-central1"}},
		{"kind": "sql#tier", "tier": "db-custom-2-7680", "RAM": "8053063680", "DiskQuota": "32212254720000", "region": []string{"us-central1"}},
		{"kind": "sql#tier", "tier": "db-custom-4-15360", "RAM": "16106127360", "DiskQuota": "32212254720000", "region": []string{"us-central1"}},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "sql#tiersList", "items": tiers})
}

func handleSQLListFlags(w http.ResponseWriter, r *http.Request) {
	// Representative real Cloud SQL database flags (names, types, and
	// constraints match the documented flags.list output). appliesTo lists
	// the database versions each flag is valid for.
	flags := []map[string]any{
		{
			"kind": "sql#flag", "name": "max_connections", "type": "INTEGER",
			"appliesTo": []string{"POSTGRES_15", "POSTGRES_14", "MYSQL_8_0"},
			"minValue":  "14", "maxValue": "262143", "requiresRestart": false,
		},
		{
			"kind": "sql#flag", "name": "log_min_duration_statement", "type": "INTEGER",
			"appliesTo": []string{"POSTGRES_15", "POSTGRES_14"},
			"minValue":  "-1", "maxValue": "2147483647", "requiresRestart": false,
		},
		{
			"kind": "sql#flag", "name": "cloudsql.iam_authentication", "type": "BOOLEAN",
			"appliesTo": []string{"POSTGRES_15", "POSTGRES_14"}, "requiresRestart": true,
		},
		{
			"kind": "sql#flag", "name": "log_output", "type": "STRING",
			"appliesTo":           []string{"MYSQL_8_0", "MYSQL_5_7"},
			"allowedStringValues": []string{"NONE", "FILE", "TABLE"}, "requiresRestart": false,
		},
		{
			"kind": "sql#flag", "name": "character_set_server", "type": "STRING",
			"appliesTo":           []string{"MYSQL_8_0", "MYSQL_5_7"},
			"allowedStringValues": []string{"utf8", "utf8mb4", "latin1"}, "requiresRestart": true,
		},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "sql#flagsList", "items": flags})
}

func sqlBackupKey(project, backup string) string {
	return project + "/" + backup
}

func handleSQLCreateBackup(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req SQLBackup
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.Instance == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "instance is required")
		return
	}
	// The backups resource names its instance by full resource name
	// (projects/{project}/instances/{instance}); the bare name also appears
	// in the wild.
	instanceParts := strings.Split(req.Instance, "/")
	instanceName := instanceParts[len(instanceParts)-1]
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instanceName)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", req.Instance)
		return
	}
	id := strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	now := nowTimestamp()
	b := SQLBackup{
		Kind:        "sql#backup",
		Name:        fmt.Sprintf("projects/%s/backups/%s", project, id),
		Instance:    req.Instance,
		Description: req.Description,
		Location:    defaultStr(req.Location, "us"),
		// The backup answers ENQUEUED and settles once the instance's
		// volume is captured, like a backup run — it is one, retained at
		// the project scope.
		State:           "ENQUEUED",
		Type:            defaultStr(req.Type, "ON_DEMAND"),
		BackupKind:      "SNAPSHOT",
		DatabaseVersion: req.DatabaseVersion,
		ExpiryTime:      now,
		SelfLink:        gcpSelfLink(r, sqlAPIPrefix(r)+"/projects/"+project+"/backups/"+id),
	}
	sqlBackups.Put(sqlBackupKey(project, id), b)
	op := newSQLOperationRunning(project, "CREATE_BACKUP", id)
	opName := op.Name
	simGo(func() {
		captureErr := sqlCaptureVolume(project, instanceName, sqlBackupVolume(project, id))
		state := "SUCCESSFUL"
		if captureErr != nil {
			state = "FAILED"
		}
		if !sqlBackups.Update(sqlBackupKey(project, id), func(backup *SQLBackup) {
			backup.State = state
		}) {
			sqlRemoveBackupVolume(sqlBackupVolume(project, id))
		}
		sqlSettleOperation(project, opName, captureErr)
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLListBackups(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := project + "/"
	out := sqlBackups.Filter(func(b SQLBackup) bool {
		return strings.HasPrefix(b.Name, "projects/"+prefix)
	})
	if out == nil {
		out = []SQLBackup{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"backups": out})
}

func handleSQLGetBackup(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	id := sim.PathParam(r, "backup")
	b, ok := sqlBackups.Get(sqlBackupKey(project, id))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, b)
}

func handleSQLUpdateBackup(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	id := sim.PathParam(r, "backup")
	key := sqlBackupKey(project, id)
	if _, ok := sqlBackups.Get(key); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", id)
		return
	}
	var req SQLBackup
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	sqlBackups.Update(key, func(b *SQLBackup) {
		if req.Description != "" {
			b.Description = req.Description
		}
	})
	op := newSQLOperation(project, "UPDATE_BACKUP", id)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLDeleteBackup(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	id := sim.PathParam(r, "backup")
	if !sqlBackups.Delete(sqlBackupKey(project, id)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup not found: %s", id)
		return
	}
	sqlRemoveBackupVolume(sqlBackupVolume(project, id))
	op := newSQLOperation(project, "DELETE_BACKUP", id)
	sim.WriteJSON(w, http.StatusOK, op)
}
