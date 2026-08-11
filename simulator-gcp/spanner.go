package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc/status"
)

type spannerInstance struct {
	Name        string            `json:"name"`
	Config      string            `json:"config,omitempty"`
	DisplayName string            `json:"displayName,omitempty"`
	NodeCount   int               `json:"nodeCount,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	State       string            `json:"state,omitempty"`
}

type spannerDatabase struct {
	Name                   string `json:"name"`
	State                  string `json:"state,omitempty"`
	CreateTime             string `json:"createTime,omitempty"`
	VersionRetentionPeriod string `json:"versionRetentionPeriod,omitempty"`
	EarliestVersionTime    string `json:"earliestVersionTime,omitempty"`
}

type spannerDatabaseDDL struct {
	Database         string   `json:"database"`
	Statements       []string `json:"statements,omitempty"`
	ProtoDescriptors string   `json:"protoDescriptors,omitempty"`
}

type spannerSession struct {
	Name       string            `json:"name"`
	CreateTime string            `json:"createTime"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// spannerReplicaInfo mirrors the Discovery ReplicaInfo schema (replica
// placement within an instance configuration).
type spannerReplicaInfo struct {
	Location              string `json:"location,omitempty"`
	Type                  string `json:"type,omitempty"`
	DefaultLeaderLocation bool   `json:"defaultLeaderLocation,omitempty"`
}

// spannerInstanceConfig mirrors the Discovery InstanceConfig schema.
type spannerInstanceConfig struct {
	Name             string               `json:"name"`
	DisplayName      string               `json:"displayName,omitempty"`
	ConfigType       string               `json:"configType,omitempty"`
	Replicas         []spannerReplicaInfo `json:"replicas,omitempty"`
	OptionalReplicas []spannerReplicaInfo `json:"optionalReplicas,omitempty"`
	BaseConfig       string               `json:"baseConfig,omitempty"`
	Labels           map[string]string    `json:"labels,omitempty"`
	Etag             string               `json:"etag,omitempty"`
	LeaderOptions    []string             `json:"leaderOptions,omitempty"`
	Reconciling      bool                 `json:"reconciling,omitempty"`
	State            string               `json:"state,omitempty"`
}

var (
	spannerInstances       sim.Store[spannerInstance]
	spannerDatabases       sim.Store[spannerDatabase]
	spannerDDLs            sim.Store[spannerDatabaseDDL]
	spannerSessions        sim.Store[spannerSession]
	spannerInstanceConfigs sim.Store[spannerInstanceConfig]
)

func registerSpanner(srv *sim.Server) {
	spannerInstances = sim.MakeStore[spannerInstance](srv.DB(), "spanner_instances")
	spannerDatabases = sim.MakeStore[spannerDatabase](srv.DB(), "spanner_databases")
	spannerDDLs = sim.MakeStore[spannerDatabaseDDL](srv.DB(), "spanner_database_ddls")
	spannerSessions = sim.MakeStore[spannerSession](srv.DB(), "spanner_sessions")
	spannerInstanceConfigs = sim.MakeStore[spannerInstanceConfig](srv.DB(), "spanner_instance_configs")

	const base = "/spanner/v1/projects/{project}/instances"
	srv.HandleFunc("POST "+base, handleSpannerCreateInstance)
	srv.HandleFunc("GET "+base, handleSpannerListInstances)
	srv.HandleFunc("GET "+base+"/{rest...}", handleSpannerInstanceChild)
	srv.HandleFunc("POST "+base+"/{rest...}", handleSpannerInstanceChild)
	srv.HandleFunc("PATCH "+base+"/{rest...}", handleSpannerInstanceChild)
	srv.HandleFunc("DELETE "+base+"/{rest...}", handleSpannerInstanceChild)

	// Instance configurations: real CRUD plus their long-running operations
	// and ssdCaches operations. Registered in flatPath form so the routes
	// align with the Discovery method URIs.
	const cfgBase = "/spanner/v1/projects/{project}/instanceConfigs"
	srv.HandleFunc("POST "+cfgBase, handleSpannerCreateInstanceConfig)
	srv.HandleFunc("GET "+cfgBase, handleSpannerListInstanceConfigs)
	srv.HandleFunc("GET "+cfgBase+"/{config}", handleSpannerGetInstanceConfig)
	srv.HandleFunc("PATCH "+cfgBase+"/{config}", handleSpannerUpdateInstanceConfig)
	srv.HandleFunc("DELETE "+cfgBase+"/{config}", handleSpannerDeleteInstanceConfig)
	srv.HandleFunc("GET "+cfgBase+"/{config}/operations", handleSpannerListInstanceConfigOperations)
	srv.HandleFunc("GET "+cfgBase+"/{config}/operations/{operation}", handleSpannerGetInstanceConfigOperation)
	srv.HandleFunc("DELETE "+cfgBase+"/{config}/operations/{operation}", handleSpannerDeleteInstanceConfigOperation)
	srv.HandleFunc("GET "+cfgBase+"/{config}/ssdCaches/{ssdCache}/operations", handleSpannerListInstanceConfigOperations)
	srv.HandleFunc("GET "+cfgBase+"/{config}/ssdCaches/{ssdCache}/operations/{operation}", handleSpannerGetInstanceConfigOperation)
	srv.HandleFunc("DELETE "+cfgBase+"/{config}/ssdCaches/{ssdCache}/operations/{operation}", handleSpannerDeleteInstanceConfigOperation)

	srv.HandleFunc("GET /spanner/v1/projects/{project}/instanceConfigOperations", handleSpannerListInstanceConfigOperationsCollection)
	srv.HandleFunc("GET /spanner/v1/scans", handleSpannerListScans)
}

func spannerInstanceName(project, instance string) string {
	return fmt.Sprintf("projects/%s/instances/%s", project, instance)
}

func spannerDatabaseName(project, instance, database string) string {
	return fmt.Sprintf("%s/databases/%s", spannerInstanceName(project, instance), database)
}

func spannerSessionName(project, instance, database, session string) string {
	return fmt.Sprintf("%s/sessions/%s", spannerDatabaseName(project, instance, database), session)
}

func handleSpannerDatabases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleSpannerCreateDatabase(w, r)
	case http.MethodGet:
		handleSpannerListDatabases(w, r)
	default:
		sim.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	}
}

func handleSpannerSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleSpannerCreateSession(w, r)
	case http.MethodGet:
		handleSpannerListSessions(w, r)
	default:
		sim.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	}
}

func handleSpannerInstanceChild(w http.ResponseWriter, r *http.Request) {
	parts := spannerRestParts(r)
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		handleSpannerGetInstance(w, r)
	case len(parts) == 1 && r.Method == http.MethodPatch:
		handleSpannerUpdateInstance(w, r)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		handleSpannerDeleteInstance(w, r)
	case len(parts) == 3 && parts[1] == "operations" && r.Method == http.MethodGet:
		handleSpannerGetOperation(w, r)
	case len(parts) == 2 && parts[1] == "databases":
		handleSpannerDatabases(w, r)
	case len(parts) == 3 && parts[1] == "databases" && r.Method == http.MethodGet:
		handleSpannerGetDatabase(w, r)
	case len(parts) == 3 && parts[1] == "databases" && r.Method == http.MethodDelete:
		handleSpannerDeleteDatabase(w, r)
	case len(parts) == 4 && parts[1] == "databases" && parts[3] == "ddl" && r.Method == http.MethodPatch:
		handleSpannerUpdateDatabaseDdl(w, r)
	case len(parts) == 5 && parts[1] == "databases" && parts[3] == "operations" && r.Method == http.MethodGet:
		handleSpannerGetOperation(w, r)
	case len(parts) == 4 && parts[1] == "databases" && parts[3] == "sessions:batchCreate" && r.Method == http.MethodPost:
		handleSpannerBatchCreateSessionsREST(w, r)
	case len(parts) == 4 && parts[1] == "databases" && parts[3] == "sessions":
		handleSpannerSessions(w, r)
	case len(parts) == 5 && parts[1] == "databases" && parts[3] == "sessions" && strings.Contains(parts[4], ":") && r.Method == http.MethodPost:
		handleSpannerSessionActionREST(w, r)
	case len(parts) == 5 && parts[1] == "databases" && parts[3] == "sessions" && r.Method == http.MethodGet:
		handleSpannerGetSession(w, r)
	case len(parts) == 5 && parts[1] == "databases" && parts[3] == "sessions" && r.Method == http.MethodDelete:
		handleSpannerDeleteSession(w, r)
	default:
		// The "{rest...}" mount routes the tail itself, so this arm is the
		// sub-router's own miss: no Cloud Spanner method the simulator serves
		// has this shape. Google's frontend answers an unmatched URI the same
		// way.
		gcpMethodNotFound(w)
	}
}

func spannerRestParts(r *http.Request) []string {
	rest := strings.Trim(sim.PathParam(r, "rest"), "/")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "/")
}

func spannerPathPart(r *http.Request, name string, index int) string {
	if value := sim.PathParam(r, name); value != "" {
		return value
	}
	parts := spannerRestParts(r)
	if index >= 0 && index < len(parts) {
		return parts[index]
	}
	return ""
}

func handleSpannerCreateInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instanceID := r.URL.Query().Get("instanceId")
	var req struct {
		InstanceID string          `json:"instanceId"`
		Instance   spannerInstance `json:"instance"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if instanceID == "" {
		instanceID = req.InstanceID
	}
	if instanceID == "" {
		sim.GCPError(w, http.StatusBadRequest, "instanceId is required", "INVALID_ARGUMENT")
		return
	}
	inst := req.Instance
	inst.Name = spannerInstanceName(project, instanceID)
	if inst.DisplayName == "" {
		inst.DisplayName = instanceID
	}
	if inst.Config == "" {
		inst.Config = fmt.Sprintf("projects/%s/instanceConfigs/regional-us-central1", project)
	}
	if inst.NodeCount == 0 {
		inst.NodeCount = 1
	}
	inst.State = "READY"
	spannerInstances.Put(inst.Name, inst)
	op := newSpannerInstanceLRO(project, instanceID, inst, "type.googleapis.com/google.spanner.admin.instance.v1.Instance")
	sim.WriteJSON(w, http.StatusOK, op)
}

func newSpannerInstanceLRO(project, instance string, resource any, typeName string) Operation {
	op := newLRO(project, "global", resource, typeName)
	return renameGCPOperation(op, fmt.Sprintf("projects/%s/instances/%s/operations", project, instance))
}

func newSpannerDatabaseLRO(project, instance, database string, resource any, typeName string) Operation {
	op := newLRO(project, "global", resource, typeName)
	databaseName := spannerDatabaseName(project, instance, database)
	op.Metadata = map[string]any{
		"@type":    "type.googleapis.com/google.spanner.admin.database.v1.CreateDatabaseMetadata",
		"database": databaseName,
		"resource": databaseName,
	}
	return renameGCPOperation(op, fmt.Sprintf("projects/%s/instances/%s/databases/%s/operations", project, instance, database))
}

func handleSpannerGetOperation(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	parts := spannerRestParts(r)
	var name string
	if len(parts) == 3 && parts[1] == "operations" {
		name = fmt.Sprintf("projects/%s/instances/%s/operations/%s", project, parts[0], parts[2])
	}
	if len(parts) == 5 && parts[1] == "databases" && parts[3] == "operations" {
		name = fmt.Sprintf("projects/%s/instances/%s/databases/%s/operations/%s", project, parts[0], parts[2], parts[4])
	}
	if name == "" {
		sim.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}
	if op, ok := crOperations.Get(name); ok {
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
}

func handleSpannerListInstances(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/instances/", sim.PathParam(r, "project"))
	out := spannerInstances.Filter(func(inst spannerInstance) bool { return strings.HasPrefix(inst.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func handleSpannerGetInstance(w http.ResponseWriter, r *http.Request) {
	name := spannerInstanceName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0))
	inst, ok := spannerInstances.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, inst)
}

func handleSpannerDeleteInstance(w http.ResponseWriter, r *http.Request) {
	name := spannerInstanceName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0))
	if !spannerInstances.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	for _, db := range spannerDatabases.List() {
		if strings.HasPrefix(db.Name, name+"/databases/") {
			spannerDatabases.Delete(db.Name)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSpannerUpdateInstance(w http.ResponseWriter, r *http.Request) {
	project, instanceID := sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0)
	name := spannerInstanceName(project, instanceID)
	inst, ok := spannerInstances.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	var req struct {
		Instance  spannerInstance `json:"instance"`
		FieldMask string          `json:"fieldMask"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	for _, field := range strings.Split(req.FieldMask, ",") {
		switch strings.TrimSpace(field) {
		case "nodeCount":
			inst.NodeCount = req.Instance.NodeCount
		case "displayName":
			inst.DisplayName = req.Instance.DisplayName
		case "labels":
			inst.Labels = req.Instance.Labels
		}
	}
	spannerInstances.Put(name, inst)
	op := newSpannerInstanceLRO(project, instanceID, inst, "type.googleapis.com/google.spanner.admin.instance.v1.Instance")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSpannerCreateDatabase(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0)
	if _, ok := spannerInstances.Get(spannerInstanceName(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	var req struct {
		CreateStatement string `json:"createStatement"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	dbID := spannerDatabaseIDFromStatement(req.CreateStatement)
	if dbID == "" {
		sim.GCPError(w, http.StatusBadRequest, "CREATE DATABASE statement is required", "INVALID_ARGUMENT")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	db := spannerDatabase{
		Name:                   spannerDatabaseName(project, instance, dbID),
		State:                  "READY",
		CreateTime:             now,
		VersionRetentionPeriod: "1h",
		EarliestVersionTime:    now,
	}
	spannerDatabases.Put(db.Name, db)
	op := newSpannerDatabaseLRO(project, instance, dbID, db, "type.googleapis.com/google.spanner.admin.database.v1.Database")
	sim.WriteJSON(w, http.StatusOK, op)
}

var createDatabaseRe = regexp.MustCompile(`(?i)^\s*CREATE\s+DATABASE\s+` + "`?" + `([A-Za-z0-9_-]+)` + "`?")

func spannerDatabaseIDFromStatement(stmt string) string {
	m := createDatabaseRe.FindStringSubmatch(stmt)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func handleSpannerListDatabases(w http.ResponseWriter, r *http.Request) {
	prefix := spannerInstanceName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0)) + "/databases/"
	out := spannerDatabases.Filter(func(db spannerDatabase) bool { return strings.HasPrefix(db.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"databases": out})
}

func handleSpannerGetDatabase(w http.ResponseWriter, r *http.Request) {
	name := spannerDatabaseName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0), spannerPathPart(r, "database", 2))
	db, ok := spannerDatabases.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, db)
}

func handleSpannerDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	name := spannerDatabaseName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0), spannerPathPart(r, "database", 2))
	if !spannerDatabases.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	spannerDDLs.Delete(name)
	if err := spannerDropBackend(name); err != nil {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "release backing store for %q: %v", name, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSpannerUpdateDatabaseDdl(w http.ResponseWriter, r *http.Request) {
	project, instance, database := sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0), spannerPathPart(r, "database", 2)
	name := spannerDatabaseName(project, instance, database)
	if _, ok := spannerDatabases.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	var req struct {
		OperationID      string   `json:"operationId"`
		ProtoDescriptors string   `json:"protoDescriptors"`
		Statements       []string `json:"statements"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if len(req.Statements) == 0 {
		sim.GCPError(w, http.StatusBadRequest, "statements is required", "INVALID_ARGUMENT")
		return
	}
	if req.OperationID != "" {
		opName := fmt.Sprintf("%s/operations/%s", name, req.OperationID)
		if _, ok := crOperations.Get(opName); ok {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "operation %q already exists", opName)
			return
		}
	}
	if err := spannerApplyDDLStatements(name, req.Statements); err != nil {
		op := newSpannerDatabaseDDLOperation(name, req.OperationID, req.Statements, err)
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	ddl, _ := spannerDDLs.Get(name)
	ddl.Database = name
	ddl.Statements = append(ddl.Statements, req.Statements...)
	if req.ProtoDescriptors != "" {
		ddl.ProtoDescriptors = req.ProtoDescriptors
	}
	spannerDDLs.Put(name, ddl)
	op := newSpannerDatabaseDDLOperation(name, req.OperationID, req.Statements, nil)
	sim.WriteJSON(w, http.StatusOK, op)
}

func newSpannerDatabaseDDLOperation(database, operationID string, statements []string, operationErr error) Operation {
	if operationID == "" {
		operationID = "_" + strings.ReplaceAll(generateUUID(), "-", "_")
	}
	name := fmt.Sprintf("%s/operations/%s", database, operationID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	op := Operation{
		Name: name,
		Metadata: map[string]any{
			"@type":      "type.googleapis.com/google.spanner.admin.database.v1.UpdateDatabaseDdlMetadata",
			"database":   database,
			"statements": statements,
			"commitTime": now,
		},
		Done:     true,
		Response: map[string]any{"@type": "type.googleapis.com/google.protobuf.Empty"},
	}
	if operationErr != nil {
		st := status.Convert(operationErr)
		delete(op.Metadata, "commitTime")
		op.Response = nil
		op.Error = &OperationError{Code: int(st.Code()), Message: st.Message()}
	}
	if crOperations != nil {
		crOperations.Put(op.Name, op)
	}
	return op
}

func handleSpannerCreateSession(w http.ResponseWriter, r *http.Request) {
	project, instance, database := sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0), spannerPathPart(r, "database", 2)
	if _, ok := spannerDatabases.Get(spannerDatabaseName(project, instance, database)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", database)
		return
	}
	var req struct {
		Session spannerSession `json:"session"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	sessionID := generateUUID()
	sess := req.Session
	sess.Name = spannerSessionName(project, instance, database, sessionID)
	sess.CreateTime = time.Now().UTC().Format(time.RFC3339Nano)
	spannerSessions.Put(sess.Name, sess)
	sim.WriteJSON(w, http.StatusOK, sess)
}

func handleSpannerListSessions(w http.ResponseWriter, r *http.Request) {
	prefix := spannerDatabaseName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0), spannerPathPart(r, "database", 2)) + "/sessions/"
	out := spannerSessions.Filter(func(sess spannerSession) bool { return strings.HasPrefix(sess.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func handleSpannerGetSession(w http.ResponseWriter, r *http.Request) {
	name := spannerSessionName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0), spannerPathPart(r, "database", 2), spannerPathPart(r, "session", 4))
	sess, ok := spannerSessions.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "session %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, sess)
}

func handleSpannerDeleteSession(w http.ResponseWriter, r *http.Request) {
	name := spannerSessionName(sim.PathParam(r, "project"), spannerPathPart(r, "instance", 0), spannerPathPart(r, "database", 2), spannerPathPart(r, "session", 4))
	if !spannerSessions.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "session %q not found", name)
		return
	}
	spannerRollbackSessionTransactions(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func spannerInstanceConfigName(project, config string) string {
	return fmt.Sprintf("projects/%s/instanceConfigs/%s", project, config)
}

func newSpannerInstanceConfigLRO(project, config string, resource any, typeName string) Operation {
	op := newLRO(project, "global", resource, typeName)
	return renameGCPOperation(op, fmt.Sprintf("projects/%s/instanceConfigs/%s/operations", project, config))
}

func handleSpannerCreateInstanceConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req struct {
		InstanceConfigID string                `json:"instanceConfigId"`
		InstanceConfig   spannerInstanceConfig `json:"instanceConfig"`
		ValidateOnly     bool                  `json:"validateOnly"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	configID := req.InstanceConfigID
	if configID == "" {
		sim.GCPError(w, http.StatusBadRequest, "instanceConfigId is required", "INVALID_ARGUMENT")
		return
	}
	cfg := req.InstanceConfig
	cfg.Name = spannerInstanceConfigName(project, configID)
	if cfg.DisplayName == "" {
		cfg.DisplayName = configID
	}
	if _, ok := spannerInstanceConfigs.Get(cfg.Name); ok {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "instance config %q already exists", cfg.Name)
		return
	}
	cfg.ConfigType = "USER_MANAGED"
	cfg.State = "READY"
	cfg.Reconciling = false
	if cfg.Etag == "" {
		cfg.Etag = generateUUID()
	}
	if req.ValidateOnly {
		op := newSpannerInstanceConfigLRO(project, configID, cfg, "type.googleapis.com/google.spanner.admin.instance.v1.InstanceConfig")
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	spannerInstanceConfigs.Put(cfg.Name, cfg)
	op := newSpannerInstanceConfigLRO(project, configID, cfg, "type.googleapis.com/google.spanner.admin.instance.v1.InstanceConfig")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSpannerListInstanceConfigs(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/instanceConfigs/", sim.PathParam(r, "project"))
	out := spannerInstanceConfigs.Filter(func(c spannerInstanceConfig) bool { return strings.HasPrefix(c.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"instanceConfigs": out})
}

func handleSpannerGetInstanceConfig(w http.ResponseWriter, r *http.Request) {
	name := spannerInstanceConfigName(sim.PathParam(r, "project"), sim.PathParam(r, "config"))
	cfg, ok := spannerInstanceConfigs.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance config %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleSpannerUpdateInstanceConfig(w http.ResponseWriter, r *http.Request) {
	project, configID := sim.PathParam(r, "project"), sim.PathParam(r, "config")
	name := spannerInstanceConfigName(project, configID)
	cfg, ok := spannerInstanceConfigs.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance config %q not found", name)
		return
	}
	var req struct {
		InstanceConfig spannerInstanceConfig `json:"instanceConfig"`
		UpdateMask     string                `json:"updateMask"`
		ValidateOnly   bool                  `json:"validateOnly"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	for _, field := range strings.Split(req.UpdateMask, ",") {
		switch strings.TrimSpace(field) {
		case "displayName":
			cfg.DisplayName = req.InstanceConfig.DisplayName
		case "labels":
			cfg.Labels = req.InstanceConfig.Labels
		}
	}
	cfg.Etag = generateUUID()
	if !req.ValidateOnly {
		spannerInstanceConfigs.Put(name, cfg)
	}
	op := newSpannerInstanceConfigLRO(project, configID, cfg, "type.googleapis.com/google.spanner.admin.instance.v1.InstanceConfig")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSpannerDeleteInstanceConfig(w http.ResponseWriter, r *http.Request) {
	name := spannerInstanceConfigName(sim.PathParam(r, "project"), sim.PathParam(r, "config"))
	if !spannerInstanceConfigs.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance config %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSpannerListInstanceConfigOperations(w http.ResponseWriter, r *http.Request) {
	base := spannerInstanceConfigName(sim.PathParam(r, "project"), sim.PathParam(r, "config"))
	var prefix string
	if ssd := sim.PathParam(r, "ssdCache"); ssd != "" {
		prefix = fmt.Sprintf("%s/ssdCaches/%s/operations/", base, ssd)
	} else {
		prefix = base + "/operations/"
	}
	out := crOperations.Filter(func(op Operation) bool { return strings.HasPrefix(op.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operations": out})
}

func handleSpannerGetInstanceConfigOperation(w http.ResponseWriter, r *http.Request) {
	base := spannerInstanceConfigName(sim.PathParam(r, "project"), sim.PathParam(r, "config"))
	var name string
	if ssd := sim.PathParam(r, "ssdCache"); ssd != "" {
		name = fmt.Sprintf("%s/ssdCaches/%s/operations/%s", base, ssd, sim.PathParam(r, "operation"))
	} else {
		name = fmt.Sprintf("%s/operations/%s", base, sim.PathParam(r, "operation"))
	}
	if op, ok := crOperations.Get(name); ok {
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
}

func handleSpannerDeleteInstanceConfigOperation(w http.ResponseWriter, r *http.Request) {
	base := spannerInstanceConfigName(sim.PathParam(r, "project"), sim.PathParam(r, "config"))
	var name string
	if ssd := sim.PathParam(r, "ssdCache"); ssd != "" {
		name = fmt.Sprintf("%s/ssdCaches/%s/operations/%s", base, ssd, sim.PathParam(r, "operation"))
	} else {
		name = fmt.Sprintf("%s/operations/%s", base, sim.PathParam(r, "operation"))
	}
	crOperations.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSpannerListInstanceConfigOperationsCollection(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/instanceConfigs/", sim.PathParam(r, "project"))
	out := crOperations.Filter(func(op Operation) bool { return strings.HasPrefix(op.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operations": out})
}

func handleSpannerListScans(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"scans": []any{}})
}
