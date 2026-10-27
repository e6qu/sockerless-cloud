package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Spanner administration beyond spanner.go's instance and database CRUD:
// instance partitions, the four operation collections, the AIP-141 IAM verbs,
// database roles, GetDatabaseDdl and UpdateDatabase. Each answers from the
// data plane's own state: GetDatabaseDdl reports the DDL the SQLite engine
// ran, and roles fold out of the database's CREATE ROLE and DROP ROLE.

// spannerInstancePartition mirrors the Discovery InstancePartition schema — the
// compute/storage subdivision an instance can be split into.
type spannerInstancePartition struct {
	Name                 string   `json:"name"`
	Config               string   `json:"config,omitempty"`
	DisplayName          string   `json:"displayName,omitempty"`
	NodeCount            int      `json:"nodeCount,omitempty"`
	ProcessingUnits      int      `json:"processingUnits,omitempty"`
	State                string   `json:"state,omitempty"`
	CreateTime           string   `json:"createTime,omitempty"`
	UpdateTime           string   `json:"updateTime,omitempty"`
	ReferencingDatabases []string `json:"referencingDatabases,omitempty"`
	ReferencingBackups   []string `json:"referencingBackups,omitempty"`
	Etag                 string   `json:"etag,omitempty"`
}

// spannerQueryParam reads a query parameter under any spelling Google's JSON
// transcoding accepts: the Discovery-generated clients send
// `backupScheduleId`, the Terraform provider `backup_schedule_id`.
func spannerQueryParam(r *http.Request, names ...string) string {
	query := r.URL.Query()
	for _, name := range names {
		if v := strings.TrimSpace(query.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// spannerRouteOperation serves the google.longrunning.Operations methods on one
// operation name: get, delete, and the ":cancel" custom method.
func spannerRouteOperation(w http.ResponseWriter, r *http.Request, verb, name string) bool {
	if verb != "" {
		if verb != "cancel" || r.Method != http.MethodPost {
			return false
		}
		handleSpannerCancelOperation(w, name)
		return true
	}
	switch r.Method {
	case http.MethodGet:
		op, ok := crOperations.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
			return true
		}
		sim.WriteJSON(w, http.StatusOK, op)
	case http.MethodDelete:
		if !crOperations.Delete(name) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
			return true
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	default:
		return false
	}
	return true
}

// handleSpannerCancelOperation implements CancelOperation, which is best
// effort: every Cloud Spanner operation the simulator records is done before a
// client holds its name, so a cancel leaves the result alone and answers
// Empty, as Cloud Spanner does with a late cancel.
func handleSpannerCancelOperation(w http.ResponseWriter, name string) {
	if _, ok := crOperations.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleSpannerInstanceConfigOperationAction is the colon-verb fan-in for the
// instance-config (and ssdCaches) operation collections, whose only custom
// method is cancel.
func handleSpannerInstanceConfigOperationAction(w http.ResponseWriter, r *http.Request) {
	operation, verb := spannerColonVerb(sim.PathParam(r, "operation"))
	if verb != "cancel" {
		gcpMethodNotFound(w)
		return
	}
	base := spannerInstanceConfigName(sim.PathParam(r, "project"), sim.PathParam(r, "config"))
	name := fmt.Sprintf("%s/operations/%s", base, operation)
	if ssd := sim.PathParam(r, "ssdCache"); ssd != "" {
		name = fmt.Sprintf("%s/ssdCaches/%s/operations/%s", base, ssd, operation)
	}
	handleSpannerCancelOperation(w, name)
}

// handleSpannerListOperationsUnder lists every operation whose name sits under
// prefix, newest name last, honoring the standard filter/pageSize/pageToken
// list parameters.
func handleSpannerListOperationsUnder(w http.ResponseWriter, r *http.Request, prefix string) {
	gcpWriteList(w, r, crOperations, prefix, "operations", func(v Operation) string { return v.Name })
}

// handleSpannerListInstanceScopedOperations serves the instance's own
// operations collection and the three per-resource-kind operation collections
// (databaseOperations, backupOperations, instancePartitionOperations), each of
// which reports the operations recorded against that kind of child resource.
func handleSpannerListInstanceScopedOperations(w http.ResponseWriter, r *http.Request, instance, collection string) {
	base := spannerInstanceName(sim.PathParam(r, "project"), instance)
	switch collection {
	case "operations":
		handleSpannerListOperationsUnder(w, r, base+"/operations/")
	case "databaseOperations":
		handleSpannerListOperationsUnder(w, r, base+"/databases/")
	case "backupOperations":
		handleSpannerListOperationsUnder(w, r, base+"/backups/")
	case "instancePartitionOperations":
		handleSpannerListOperationsUnder(w, r, base+"/instancePartitions/")
	}
}

func handleSpannerInstanceAction(w http.ResponseWriter, r *http.Request, instance, verb string) {
	name := spannerInstanceName(sim.PathParam(r, "project"), instance)
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		if _, ok := spannerInstances.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
			return
		}
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, verb)
	case "move":
		handleSpannerMoveInstance(w, r, name)
	default:
		gcpMethodNotFound(w)
	}
}

// handleSpannerMoveInstance moves the instance to the requested configuration;
// its databases keep their rows.
func handleSpannerMoveInstance(w http.ResponseWriter, r *http.Request, name string) {
	inst, ok := spannerInstances.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	var req struct {
		TargetConfig string `json:"targetConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.TargetConfig == "" {
		GCPError(w, http.StatusBadRequest, "targetConfig is required", "INVALID_ARGUMENT")
		return
	}
	if req.TargetConfig == inst.Config {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"instance %q is already in configuration %q", name, req.TargetConfig)
		return
	}
	inst.Config = req.TargetConfig
	spannerInstances.Put(name, inst)
	op := newSpannerOperation(name+"/operations", map[string]any{
		"@type":        "type.googleapis.com/google.spanner.admin.instance.v1.MoveInstanceMetadata",
		"targetConfig": req.TargetConfig,
		"startTime":    nowTimestamp(),
	}, map[string]any{"@type": "type.googleapis.com/google.spanner.admin.instance.v1.MoveInstanceResponse"})
	sim.WriteJSON(w, http.StatusOK, op)
}

// newSpannerOperation records a completed long-running operation under a
// collection ("projects/p/instances/i/backups/b/operations") with the metadata
// and response the corresponding Cloud Spanner method documents.
func newSpannerOperation(collection string, metadata, response map[string]any) Operation {
	op := Operation{
		Name:     strings.TrimRight(collection, "/") + "/_" + strings.ReplaceAll(sim.NewUUID(), "-", "_"),
		Metadata: metadata,
		Done:     true,
		Response: response,
	}
	crOperations.Put(op.Name, op)
	return op
}

// newSpannerFailedOperation records a completed operation carrying an error
// instead of a response.
func newSpannerFailedOperation(collection string, metadata map[string]any, code int, message string) Operation {
	op := Operation{
		Name:     strings.TrimRight(collection, "/") + "/_" + strings.ReplaceAll(sim.NewUUID(), "-", "_"),
		Metadata: metadata,
		Done:     true,
		Error:    &OperationError{Code: code, Message: message},
	}
	crOperations.Put(op.Name, op)
	return op
}

// handleSpannerDatabaseAction is the colon-verb fan-in on a database resource.
func handleSpannerDatabaseAction(w http.ResponseWriter, r *http.Request, instance, database, verb string) {
	name := spannerDatabaseName(sim.PathParam(r, "project"), instance, database)
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		if _, ok := spannerDatabases.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
			return
		}
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, verb)
	case "addSplitPoints":
		handleSpannerAddSplitPoints(w, r, name)
	case "changequorum":
		handleSpannerChangeQuorum(w, r, name)
	default:
		gcpMethodNotFound(w)
	}
}

// handleSpannerAddSplitPoints records where a database is to be split. A
// split point names a key in a table or index; Spanner answers an empty body.
func handleSpannerAddSplitPoints(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := spannerDatabases.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	var req struct {
		SplitPoints []struct {
			Table string `json:"table"`
			Index string `json:"index"`
		} `json:"splitPoints"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if len(req.SplitPoints) == 0 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"addSplitPoints needs the split points to add")
		return
	}
	for _, point := range req.SplitPoints {
		if point.Table == "" && point.Index == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"a split point names the table or index it splits")
			return
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleSpannerChangeQuorum moves a database between quorum types, as a
// dual-region failover does, and records the new quorum for reads.
func handleSpannerChangeQuorum(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		QuorumType map[string]any `json:"quorumType"`
		Etag       string         `json:"etag"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if len(req.QuorumType) == 0 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"changequorum needs the quorum type to move to")
		return
	}
	// QuorumInfo records the change (who asked, when, and the quorum type moved
	// to), with the type inside it.
	quorum := map[string]any{
		"quorumType": req.QuorumType,
		"initiator":  "USER",
		"startTime":  time.Now().UTC().Format(time.RFC3339),
	}
	// A request carrying the etag changes the quorum only while it matches, so two
	// concurrent changes cannot both apply.
	quorum["etag"] = gcpPolicyETag()
	stale := false
	if !spannerDatabases.Update(name, func(db *spannerDatabase) {
		if current, _ := db.QuorumInfo["etag"].(string); req.Etag != "" && req.Etag != current {
			stale = true
			return
		}
		db.QuorumInfo = quorum
	}) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	if stale {
		GCPErrorf(w, http.StatusConflict, "ABORTED",
			"The etag does not match the database's current quorum info; read the database again and retry.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name + "/operations/changequorum", "done": true,
		"response": map[string]any{"name": name},
	})
}

// handleSpannerGetDatabaseDdl answers the DDL statements the SQLite engine ran
// to build the schema.
func handleSpannerGetDatabaseDdl(w http.ResponseWriter, r *http.Request, instance, database string) {
	name := spannerDatabaseName(sim.PathParam(r, "project"), instance, database)
	if _, ok := spannerDatabases.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	ddl, _ := spannerDDLs.Get(name)
	body := map[string]any{}
	if len(ddl.Statements) > 0 {
		body["statements"] = ddl.Statements
	}
	if ddl.ProtoDescriptors != "" {
		body["protoDescriptors"] = ddl.ProtoDescriptors
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// handleSpannerUpdateDatabase sets enableDropProtection, UpdateDatabase's one
// writable field, which DropDatabase and DeleteInstance enforce.
func handleSpannerUpdateDatabase(w http.ResponseWriter, r *http.Request, instance, database string) {
	name := spannerDatabaseName(sim.PathParam(r, "project"), instance, database)
	db, ok := spannerDatabases.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	var req spannerDatabase
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	mask := spannerQueryParam(r, "updateMask", "update_mask")
	if mask == "" {
		GCPError(w, http.StatusBadRequest, "updateMask is required", "INVALID_ARGUMENT")
		return
	}
	for _, field := range strings.Split(mask, ",") {
		switch strings.TrimSpace(field) {
		case "enableDropProtection", "enable_drop_protection":
			db.EnableDropProtection = req.EnableDropProtection
		default:
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"field %q is not updatable on a Cloud Spanner database", strings.TrimSpace(field))
			return
		}
	}
	spannerDatabases.Put(name, db)
	op := newSpannerOperation(name+"/operations", map[string]any{
		"@type":    "type.googleapis.com/google.spanner.admin.database.v1.UpdateDatabaseMetadata",
		"request":  map[string]any{"database": gcpResourceToMap(db), "updateMask": mask},
		"progress": map[string]any{"progressPercent": 100, "startTime": db.CreateTime, "endTime": nowTimestamp()},
	}, spannerDatabaseResponse(db))
	sim.WriteJSON(w, http.StatusOK, op)
}

// spannerDatabaseResponse renders a database as the protobuf Any payload an
// operation's response field carries.
func spannerDatabaseResponse(db spannerDatabase) map[string]any {
	m := gcpResourceToMap(db)
	m["@type"] = "type.googleapis.com/google.spanner.admin.database.v1.Database"
	return m
}

// spannerCreateRoleRe and spannerDropRoleRe recognize the fine-grained access
// control DDL that defines a database's roles.
var (
	spannerCreateRoleRe = regexp.MustCompile("(?is)^\\s*CREATE\\s+ROLE\\s+`?([A-Za-z_][A-Za-z0-9_]*)`?\\s*$")
	spannerDropRoleRe   = regexp.MustCompile("(?is)^\\s*DROP\\s+ROLE\\s+`?([A-Za-z_][A-Za-z0-9_]*)`?\\s*$")
)

// spannerDatabaseRoleNames folds the DDL history into the roles that exist,
// sorted as Cloud Spanner lists them, with the built-in "public" role.
func spannerDatabaseRoleNames(dbName string) []string {
	roles := map[string]bool{"public": true}
	ddl, ok := spannerDDLs.Get(dbName)
	if ok {
		for _, stmt := range ddl.Statements {
			if m := spannerCreateRoleRe.FindStringSubmatch(stmt); m != nil {
				roles[m[1]] = true
				continue
			}
			if m := spannerDropRoleRe.FindStringSubmatch(stmt); m != nil {
				delete(roles, m[1])
			}
		}
	}
	out := make([]string, 0, len(roles))
	for role := range roles {
		out = append(out, role)
	}
	sort.Strings(out)
	return out
}

func handleSpannerListDatabaseRoles(w http.ResponseWriter, r *http.Request, instance, database string) {
	name := spannerDatabaseName(sim.PathParam(r, "project"), instance, database)
	if _, ok := spannerDatabases.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	type databaseRole struct {
		Name string `json:"name"`
	}
	var roles []databaseRole
	for _, role := range spannerDatabaseRoleNames(name) {
		roles = append(roles, databaseRole{Name: name + "/databaseRoles/" + role})
	}
	listed, listOK := gcpApplyListParams(w, r, roles)
	if !listOK {
		return
	}
	roles = listed
	page, next, ok := paginateList(w, r, roles, gcpAsc(func(role databaseRole) string { return role.Name }))
	if !ok {
		return
	}
	body := map[string]any{"databaseRoles": page}
	if next != "" {
		body["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// handleSpannerDatabaseRoleIAM serves the one IAM verb a database role exposes.
func handleSpannerDatabaseRoleIAM(w http.ResponseWriter, r *http.Request, resource, verb string) {
	if verb != "testIamPermissions" {
		gcpMethodNotFound(w)
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), resource, verb)
}

func spannerInstancePartitionName(project, instance, partition string) string {
	return fmt.Sprintf("%s/instancePartitions/%s", spannerInstanceName(project, instance), partition)
}

func handleSpannerCreateInstancePartition(w http.ResponseWriter, r *http.Request, instance string) {
	project := sim.PathParam(r, "project")
	instanceName := spannerInstanceName(project, instance)
	if _, ok := spannerInstances.Get(instanceName); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instanceName)
		return
	}
	var req struct {
		InstancePartitionID string                   `json:"instancePartitionId"`
		InstancePartition   spannerInstancePartition `json:"instancePartition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.InstancePartitionID == "" {
		GCPError(w, http.StatusBadRequest, "instancePartitionId is required", "INVALID_ARGUMENT")
		return
	}
	partition := req.InstancePartition
	partition.Name = spannerInstancePartitionName(project, instance, req.InstancePartitionID)
	if _, exists := spannerInstancePartitions.Get(partition.Name); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "instance partition %q already exists", partition.Name)
		return
	}
	if partition.Config == "" {
		GCPError(w, http.StatusBadRequest, "instancePartition.config is required", "INVALID_ARGUMENT")
		return
	}
	if partition.NodeCount == 0 && partition.ProcessingUnits == 0 {
		GCPError(w, http.StatusBadRequest,
			"exactly one of instancePartition.nodeCount or instancePartition.processingUnits is required", "INVALID_ARGUMENT")
		return
	}
	if partition.DisplayName == "" {
		partition.DisplayName = req.InstancePartitionID
	}
	now := nowTimestamp()
	partition.CreateTime = now
	partition.UpdateTime = now
	partition.State = "READY"
	partition.Etag = sim.NewUUID()
	spannerInstancePartitions.Put(partition.Name, partition)
	op := newSpannerOperation(partition.Name+"/operations", map[string]any{
		"@type":             "type.googleapis.com/google.spanner.admin.instance.v1.CreateInstancePartitionMetadata",
		"instancePartition": gcpResourceToMap(partition),
		"startTime":         now,
		"endTime":           now,
	}, spannerInstancePartitionResponse(partition))
	sim.WriteJSON(w, http.StatusOK, op)
}

func spannerInstancePartitionResponse(partition spannerInstancePartition) map[string]any {
	m := gcpResourceToMap(partition)
	m["@type"] = "type.googleapis.com/google.spanner.admin.instance.v1.InstancePartition"
	return m
}

func handleSpannerListInstancePartitions(w http.ResponseWriter, r *http.Request, instance string) {
	prefix := spannerInstanceName(sim.PathParam(r, "project"), instance) + "/instancePartitions/"
	gcpWriteList(w, r, spannerInstancePartitions, prefix, "instancePartitions", func(v spannerInstancePartition) string { return v.Name })
}

func handleSpannerGetInstancePartition(w http.ResponseWriter, r *http.Request, instance, partition string) {
	name := spannerInstancePartitionName(sim.PathParam(r, "project"), instance, partition)
	stored, ok := spannerInstancePartitions.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance partition %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, stored)
}

func handleSpannerUpdateInstancePartition(w http.ResponseWriter, r *http.Request, instance, partition string) {
	name := spannerInstancePartitionName(sim.PathParam(r, "project"), instance, partition)
	stored, ok := spannerInstancePartitions.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance partition %q not found", name)
		return
	}
	var req struct {
		InstancePartition spannerInstancePartition `json:"instancePartition"`
		FieldMask         string                   `json:"fieldMask"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if strings.TrimSpace(req.FieldMask) == "" {
		GCPError(w, http.StatusBadRequest, "fieldMask is required", "INVALID_ARGUMENT")
		return
	}
	for _, field := range strings.Split(req.FieldMask, ",") {
		switch strings.TrimSpace(field) {
		case "nodeCount", "node_count":
			stored.NodeCount = req.InstancePartition.NodeCount
		case "processingUnits", "processing_units":
			stored.ProcessingUnits = req.InstancePartition.ProcessingUnits
		case "displayName", "display_name":
			stored.DisplayName = req.InstancePartition.DisplayName
		default:
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"field %q is not updatable on a Cloud Spanner instance partition", strings.TrimSpace(field))
			return
		}
	}
	stored.UpdateTime = nowTimestamp()
	stored.Etag = sim.NewUUID()
	spannerInstancePartitions.Put(name, stored)
	op := newSpannerOperation(name+"/operations", map[string]any{
		"@type":             "type.googleapis.com/google.spanner.admin.instance.v1.UpdateInstancePartitionMetadata",
		"instancePartition": gcpResourceToMap(stored),
		"startTime":         stored.CreateTime,
		"endTime":           stored.UpdateTime,
	}, spannerInstancePartitionResponse(stored))
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSpannerDeleteInstancePartition(w http.ResponseWriter, r *http.Request, instance, partition string) {
	name := spannerInstancePartitionName(sim.PathParam(r, "project"), instance, partition)
	stored, ok := spannerInstancePartitions.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance partition %q not found", name)
		return
	}
	if len(stored.ReferencingDatabases) > 0 || len(stored.ReferencingBackups) > 0 {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"instance partition %q is still referenced and cannot be deleted", name)
		return
	}
	spannerInstancePartitions.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
