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

// Cloud Spanner instance/database administration beyond the core instance and
// database CRUD in spanner.go: instance partitions, the four operation
// collections, the AIP-141 IAM verbs, database roles, GetDatabaseDdl and
// UpdateDatabase.
//
// Every handler here answers from the same state the data plane uses. The DDL
// the schema store holds is the DDL the SQLite engine executed, so GetDatabaseDdl
// reports the schema SQL actually sees; UpdateDatabase's drop protection is
// enforced by DropDatabase and DeleteInstance; database roles are folded out of
// the database's own CREATE ROLE / DROP ROLE statements.

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

// spannerQueryParam reads a query parameter under any of the spellings Google's
// JSON transcoding accepts for it. A field declared `backup_schedule_id` in the
// proto is spelled `backupScheduleId` by the Discovery-generated clients and
// `backup_schedule_id` by the terraform google provider, and the real API
// frontend answers to both.
func spannerQueryParam(r *http.Request, names ...string) string {
	query := r.URL.Query()
	for _, name := range names {
		if v := strings.TrimSpace(query.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// operations: get / delete / cancel across every Cloud Spanner collection

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

// handleSpannerCancelOperation implements CancelOperation. Cancellation is
// best-effort in the long-running-operations contract: it asks the service to
// stop the work and returns Empty, and the caller polls GetOperation to learn
// the outcome. Every Cloud Spanner operation the simulator creates has already
// reached done before the client can hold its name, so the ask always arrives
// after completion and leaves the operation's recorded result untouched —
// exactly what the real service does with a late cancel.
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
	out := crOperations.Filter(func(op Operation) bool { return strings.HasPrefix(op.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	out = gcpApplyListParams(out, r)
	page, next, ok := paginateList(w, r, out)
	if !ok {
		return
	}
	body := map[string]any{"operations": page}
	if next != "" {
		body["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, body)
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

// instance custom methods: the IAM triple and MoveInstance

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

// handleSpannerMoveInstance implements MoveInstance: the instance's own
// configuration changes to the requested target, which is the state the move
// leaves behind. The databases the instance holds keep their rows — a move
// relocates the instance, it does not recreate its content.
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
		Name:     strings.TrimRight(collection, "/") + "/_" + strings.ReplaceAll(generateUUID(), "-", "_"),
		Metadata: metadata,
		Done:     true,
		Response: response,
	}
	crOperations.Put(op.Name, op)
	return op
}

// newSpannerFailedOperation records a completed operation that carries an
// error instead of a response — the shape a Cloud Spanner LRO takes when the
// work it started could not be finished.
func newSpannerFailedOperation(collection string, metadata map[string]any, code int, message string) Operation {
	op := Operation{
		Name:     strings.TrimRight(collection, "/") + "/_" + strings.ReplaceAll(generateUUID(), "-", "_"),
		Metadata: metadata,
		Done:     true,
		Error:    &OperationError{Code: code, Message: message},
	}
	crOperations.Put(op.Name, op)
	return op
}

// database custom methods, UpdateDatabase and GetDatabaseDdl

// handleSpannerDatabaseAction is the colon-verb fan-in on a database resource.
// The IAM triple is served; addSplitPoints and changequorum are reported as
// unknown methods because the simulator holds no key-space split map and no
// replication quorum for them to act on.
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

// handleSpannerAddSplitPoints records where a database is to be split. A split
// point names a key in a table or index, so one naming neither describes no
// split — and Spanner answers with an empty body, having taken the request.
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

// handleSpannerChangeQuorum moves a database between quorum types, which is how
// a dual-region database is failed over. The new quorum is recorded on the
// database, so a read afterwards reports the quorum the database is actually
// in.
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
	// A database's quorum info is the record of the change — who asked for it,
	// when, and the quorum type moved to — and the type sits inside it. Storing
	// the type as the info put the type's own members at the top level, where
	// the schema has none of them.
	quorum := map[string]any{
		"quorumType": req.QuorumType,
		"initiator":  "USER",
		"startTime":  time.Now().UTC().Format(time.RFC3339),
	}
	if req.Etag != "" {
		quorum["etag"] = req.Etag
	}
	if !spannerDatabases.Update(name, func(db *spannerDatabase) { db.QuorumInfo = quorum }) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name + "/operations/changequorum", "done": true,
		"response": map[string]any{"name": name},
	})
}

// handleSpannerGetDatabaseDdl reports the database's schema as the list of DDL
// statements that built it — the same statements the SQLite engine executed, so
// the answer describes the schema SQL actually sees.
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

// handleSpannerUpdateDatabase implements UpdateDatabase, whose only writable
// field is enableDropProtection. The flag is enforced: DropDatabase and
// DeleteInstance both refuse while it is set.
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

// database roles

// spannerCreateRoleRe and spannerDropRoleRe recognize the fine-grained access
// control DDL that defines a database's roles.
var (
	spannerCreateRoleRe = regexp.MustCompile("(?is)^\\s*CREATE\\s+ROLE\\s+`?([A-Za-z_][A-Za-z0-9_]*)`?\\s*$")
	spannerDropRoleRe   = regexp.MustCompile("(?is)^\\s*DROP\\s+ROLE\\s+`?([A-Za-z_][A-Za-z0-9_]*)`?\\s*$")
)

// spannerDatabaseRoleNames folds the database's DDL history into the set of
// roles that currently exist, in the order Cloud Spanner lists them (sorted,
// with the built-in "public" role every database has).
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
	roles = gcpApplyListParams(roles, r)
	page, next, ok := paginateList(w, r, roles)
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

// instance partitions

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
	partition.Etag = generateUUID()
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
	out := spannerInstancePartitions.Filter(func(p spannerInstancePartition) bool { return strings.HasPrefix(p.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	out = gcpApplyListParams(out, r)
	page, next, ok := paginateList(w, r, out)
	if !ok {
		return
	}
	body := map[string]any{"instancePartitions": page}
	if next != "" {
		body["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, body)
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
	stored.Etag = generateUUID()
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
