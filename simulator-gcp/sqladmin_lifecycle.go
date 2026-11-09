package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// The Cloud SQL instance lifecycle and replication actions. Each one that does
// work runs as a RUNNING sql#operation against the instance's engine and
// stored state and reaches DONE when that work is done; Cloud SQL runs one
// operation on an instance at a time. The simulator creates no read replica
// and no source representation instance, so the actions that need one refuse
// every instance it holds, as Cloud SQL refuses an instance that is not one,
// and declare the gap past that point.

// sqlLifecycleActions maps each action's path verb to its handler.
var sqlLifecycleActions = map[string]http.HandlerFunc{
	"restart":                     handleSQLRestart,
	"failover":                    handleSQLFailover,
	"demote":                      handleSQLDemote,
	"demoteMaster":                handleSQLDemoteMaster,
	"startReplica":                handleSQLReplicaAction("instances.startReplica"),
	"stopReplica":                 handleSQLReplicaAction("instances.stopReplica"),
	"promoteReplica":              handleSQLReplicaAction("instances.promoteReplica"),
	"switchover":                  handleSQLReplicaAction("instances.switchover"),
	"resetReplicaSize":            handleSQLReplicaAction("projects.instances.resetReplicaSize"),
	"startExternalSync":           handleSQLStartExternalSync,
	"performDiskShrink":           handleSQLPerformDiskShrink,
	"preCheckMajorVersionUpgrade": handleSQLPreCheckMajorVersionUpgrade,
	"rescheduleMaintenance":       handleSQLRescheduleMaintenance,
	"truncateLog":                 handleSQLTruncateLog,
	"reencrypt":                   handleSQLReencrypt,
}

func registerCloudSQLLifecyclePrefix(srv *sim.Server, prefix string) {
	for action, handler := range sqlLifecycleActions {
		srv.HandleFunc("POST "+prefix+"/projects/{project}/instances/{instance}/"+action, handler)
	}
	srv.HandleFunc("GET "+prefix+"/projects/{project}/instances/{instance}/getDiskShrinkConfig", handleSQLGetDiskShrinkConfig)
}

const sqlReplicationGap = "the simulator creates no Cloud SQL read replica and replicates no instance"

const sqlExternalServerGap = "the simulator replicates from no external database server"

// sqlOperationGate serialises the in-progress check with the operation it
// admits, so two concurrent requests cannot both start.
var sqlOperationGate sync.Mutex

// sqlBeginInstanceOperation mints a RUNNING operation on the instance, or
// refuses with Cloud SQL's message while another one runs.
func sqlBeginInstanceOperation(w http.ResponseWriter, project, instance, opType string) (SQLOperation, bool) {
	sqlOperationGate.Lock()
	defer sqlOperationGate.Unlock()
	for _, op := range sqlOperations.List() {
		if op.TargetProject == project && op.TargetID == instance && op.Status != "DONE" {
			GCPErrorf(w, http.StatusConflict, "ABORTED",
				"Operation failed because another operation was already in progress. Try your request after the current operation is complete.")
			return SQLOperation{}, false
		}
	}
	return newSQLOperationRunning(project, opType, instance), true
}

// sqlRecoverOperations settles every operation a control-plane restart caught
// unfinished: the goroutine doing its work died with the earlier process, and
// a RUNNING operation left behind would refuse every later one on its
// instance.
func sqlRecoverOperations() {
	for _, op := range sqlOperations.List() {
		if op.Status != "DONE" {
			sqlSettleOperation(op.TargetProject, op.Name,
				fmt.Errorf("the operation did not finish before the Cloud SQL control plane restarted"))
		}
	}
}

// sqlLifecycleTarget loads the addressed instance, answering NOT_FOUND when
// it does not exist.
func sqlLifecycleTarget(w http.ResponseWriter, r *http.Request) (SQLInstance, bool) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, name))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", name)
		return SQLInstance{}, false
	}
	return inst, true
}

func sqlSetting(settings map[string]any, key string) string {
	value, _ := settings[key].(string)
	return value
}

// sqlRequireRunning refuses an instance that is not serving: one in another
// state, or one stopped through activationPolicy NEVER.
func sqlRequireRunning(w http.ResponseWriter, inst SQLInstance) bool {
	if inst.State != "RUNNABLE" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"The instance %s is %s; the operation needs a RUNNABLE instance.", inst.Name, inst.State)
		return false
	}
	if sqlSetting(inst.Settings, "activationPolicy") == "NEVER" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"The instance %s is stopped: set its activation policy to ALWAYS to start it.", inst.Name)
		return false
	}
	return true
}

// sqlRequireReadReplica refuses an instance that is not a read replica.
func sqlRequireReadReplica(w http.ResponseWriter, inst SQLInstance) bool {
	if inst.InstanceType != "READ_REPLICA_INSTANCE" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"The instance %s is not a read replica.", inst.Name)
		return false
	}
	return true
}

// sqlRestartEngine stops the instance's engine, which drains its sessions,
// and brings a running one up again, returning once it accepts connections.
// An engine no client started yet starts on the next connection.
func sqlRestartEngine(project, instance string) error {
	plane, ok := sqlLoadDataPlane(project, instance)
	if !ok {
		return nil
	}
	wasRunning := plane.Running()
	if err := plane.Stop(); err != nil {
		return fmt.Errorf("stop the database engine: %w", err)
	}
	if !wasRunning {
		return nil
	}
	if err := plane.Ensure(); err != nil {
		return fmt.Errorf("start the database engine: %w", err)
	}
	return nil
}

func handleSQLRestart(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok || !sqlRequireRunning(w, inst) {
		return
	}
	op, ok := sqlBeginInstanceOperation(w, inst.Project, inst.Name, "RESTART")
	if !ok {
		return
	}
	bg.Go(func() {
		sqlSettleOperation(inst.Project, op.Name, sqlRestartEngine(inst.Project, inst.Name))
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

// sqlRegionZones are the zones the simulator places a region's instances in,
// in the order Cloud SQL's "Any" picks from.
func sqlRegionZones(region string) []string {
	return []string{region + "-a", region + "-b", region + "-c"}
}

func sqlLocationPreference(settings map[string]any) map[string]any {
	pref, _ := settings["locationPreference"].(map[string]any)
	return pref
}

// sqlPlaceZones sets the zone an instance serves from and, for a REGIONAL
// (high availability) instance, the secondary zone of its standby. A zone the
// locationPreference names wins when it changed from before; otherwise the
// instance keeps the zones it serves from, which a failover swaps.
func sqlPlaceZones(inst *SQLInstance, before map[string]any) {
	pref := sqlLocationPreference(inst.Settings)
	relocate := !reflect.DeepEqual(before, pref)
	zone, _ := pref["zone"].(string)
	secondary, _ := pref["secondaryZone"].(string)
	if zone != "" && (relocate || inst.GceZone == "") {
		inst.GceZone = zone
	}
	if inst.GceZone == "" {
		inst.GceZone = sqlRegionZones(inst.Region)[0]
	}
	if sqlSetting(inst.Settings, "availabilityType") != "REGIONAL" {
		inst.SecondaryGceZone = ""
		return
	}
	if secondary != "" && secondary != inst.GceZone && (relocate || inst.SecondaryGceZone == "") {
		inst.SecondaryGceZone = secondary
	}
	if inst.SecondaryGceZone == "" || inst.SecondaryGceZone == inst.GceZone {
		for _, candidate := range sqlRegionZones(inst.Region) {
			if candidate != inst.GceZone {
				inst.SecondaryGceZone = candidate
				break
			}
		}
	}
}

// sqlInt64 reads an int64 field the API carries as a JSON string, or as a
// number from a client that sends one.
func sqlInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}
	return 0, false
}

func handleSQLFailover(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		FailoverContext struct {
			SettingsVersion any `json:"settingsVersion"`
		} `json:"failoverContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if sqlSetting(inst.Settings, "availabilityType") != "REGIONAL" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"The instance %s is not configured for high availability: set settings.availabilityType to REGIONAL before you initiate a failover.", inst.Name)
		return
	}
	if !sqlRequireRunning(w, inst) {
		return
	}
	if requested, given := sqlInt64(req.FailoverContext.SettingsVersion); given && requested != 0 {
		current, _ := sqlInt64(inst.Settings["settingsVersion"])
		if requested != current {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"The settings version %d does not match the instance's current settings version %d.", requested, current)
			return
		}
	}
	op, ok := sqlBeginInstanceOperation(w, inst.Project, inst.Name, "FAILOVER")
	if !ok {
		return
	}
	bg.Go(func() {
		// The standby takes over from the secondary zone; the sessions on the
		// failed primary end.
		err := sqlRestartEngine(inst.Project, inst.Name)
		if err == nil {
			sqlInstances.Update(sqlInstanceKey(inst.Project, inst.Name), func(i *SQLInstance) {
				sqlPlaceZones(i, sqlLocationPreference(i.Settings))
				i.GceZone, i.SecondaryGceZone = i.SecondaryGceZone, i.GceZone
			})
		}
		sqlSettleOperation(inst.Project, op.Name, err)
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLReplicaAction(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inst, ok := sqlLifecycleTarget(w, r)
		if !ok || !sqlRequireReadReplica(w, inst) {
			return
		}
		sqlUnserved(method, sqlReplicationGap)(w, r)
	}
}

// sqlRequireSourceRepresentation refuses a name that does not identify a
// source representation instance, the on-premises primary of an external
// replication.
func sqlRequireSourceRepresentation(w http.ResponseWriter, project, field, name string) bool {
	if name == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s is required.", field)
		return false
	}
	source, ok := sqlInstances.Get(sqlInstanceKey(project, name))
	if !ok || source.InstanceType != "ON_PREMISES_INSTANCE" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"%s %s does not name a source representation instance.", field, name)
		return false
	}
	return true
}

// sqlRequireStandalone refuses an instance that is not a stand-alone primary.
func sqlRequireStandalone(w http.ResponseWriter, inst SQLInstance) bool {
	if inst.InstanceType != "CLOUD_SQL_INSTANCE" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"The instance %s is not a stand-alone Cloud SQL instance.", inst.Name)
		return false
	}
	return true
}

func handleSQLDemote(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		DemoteContext struct {
			SourceRepresentativeInstanceName string `json:"sourceRepresentativeInstanceName"`
		} `json:"demoteContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if !sqlRequireStandalone(w, inst) ||
		!sqlRequireSourceRepresentation(w, inst.Project, "demoteContext.sourceRepresentativeInstanceName", req.DemoteContext.SourceRepresentativeInstanceName) {
		return
	}
	sqlUnserved("instances.demote", sqlExternalServerGap)(w, r)
}

func handleSQLDemoteMaster(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		DemoteMasterContext struct {
			MasterInstanceName string `json:"masterInstanceName"`
		} `json:"demoteMasterContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if !sqlRequireStandalone(w, inst) ||
		!sqlRequireSourceRepresentation(w, inst.Project, "demoteMasterContext.masterInstanceName", req.DemoteMasterContext.MasterInstanceName) {
		return
	}
	sqlUnserved("instances.demoteMaster", sqlExternalServerGap)(w, r)
}

// handleSQLStartExternalSync refuses an instance that does not replicate a
// source representation instance; external sync migrates only into such a
// replica.
func handleSQLStartExternalSync(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok || !sqlRequireReadReplica(w, inst) {
		return
	}
	sqlUnserved("projects.instances.startExternalSync", sqlExternalServerGap)(w, r)
}

func handleSQLReencrypt(w http.ResponseWriter, r *http.Request) {
	if _, ok := sqlLifecycleTarget(w, r); !ok {
		return
	}
	sqlUnserved("instances.reencrypt", "the simulator encrypts no Cloud SQL instance with a customer-managed Cloud KMS key")(w, r)
}

// sqlSharedCoreTiers are the shared-core machine types.
var sqlSharedCoreTiers = map[string]bool{"db-f1-micro": true, "db-g1-small": true}

// sqlMinimumStorageGb is the smallest storage capacity Cloud SQL provisions.
const sqlMinimumStorageGb = 10

// sqlDataDiskSizeGb is the instance's provisioned storage, which defaults to
// the minimum.
func sqlDataDiskSizeGb(settings map[string]any) int64 {
	if size, ok := sqlInt64(settings["dataDiskSizeGb"]); ok && size > 0 {
		return size
	}
	return sqlMinimumStorageGb
}

type sqlHelperOutput struct {
	mu    sync.Mutex
	lines []string
}

func (o *sqlHelperOutput) WriteLog(line sim.LogLine) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = append(o.lines, line.Text)
}

func (o *sqlHelperOutput) Lines() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.lines...)
}

// sqlStorageUsedBytes measures the data the instance's volume holds, read
// from a one-shot container of the engine's image that mounts the volume
// read-only. An instance whose engine never started holds none.
func sqlStorageUsedBytes(inst SQLInstance) (int64, error) {
	version, ok := sqlFindEngineVersion(inst.DatabaseVersion)
	if !ok || sim.RequireContainerRuntime("measuring a Cloud SQL instance's storage") != nil {
		return 0, nil
	}
	volume := sqlInstanceVolume(inst.Project, inst.Name)
	if !sim.VolumeExists(volume) {
		return 0, nil
	}
	engine := sqlEngine(version)
	output := &sqlHelperOutput{}
	handle, err := sim.StartContainerSyncContext(context.Background(), sim.ContainerConfig{
		Image:        engine.Image,
		Architecture: "linux/amd64",
		Command:      []string{"du"},
		Args:         []string{"-sk", engine.DataPath},
		Timeout:      5 * time.Minute,
		Binds:        []string{volume + ":" + engine.DataPath + ":ro"},
		Labels:       map[string]string{"sockerless-cloudsql-storage": inst.Project + "/" + inst.Name},
		Sandbox:      SandboxCloudRun,
	}, output)
	if err != nil {
		return 0, fmt.Errorf("start the storage measurement: %w", err)
	}
	result := handle.Wait()
	lines := output.Lines()
	if result.ExitCode != 0 || result.Error != nil {
		return 0, fmt.Errorf("storage measurement exited %d (%v): %s", result.ExitCode, result.Error, strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == engine.DataPath {
			kib, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse the storage measurement %q: %w", line, err)
			}
			return kib * 1024, nil
		}
	}
	return 0, fmt.Errorf("the storage measurement printed no total: %s", strings.Join(lines, "\n"))
}

// sqlMinimalShrinkTargetGb is the smallest capacity that holds the
// instance's data, and never less than the minimum storage Cloud SQL
// provisions.
func sqlMinimalShrinkTargetGb(inst SQLInstance) (int64, error) {
	used, err := sqlStorageUsedBytes(inst)
	if err != nil {
		return 0, err
	}
	gb := int64(math.Ceil(float64(used) / float64(1<<30)))
	return max(gb, sqlMinimumStorageGb), nil
}

// sqlRequireShrinkable refuses what storage shrink does not support.
func sqlRequireShrinkable(w http.ResponseWriter, inst SQLInstance) bool {
	if inst.InstanceType == "READ_REPLICA_INSTANCE" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"You can't check the minimum storage capacity of read replica %s: only primary instances support it.", inst.Name)
		return false
	}
	if tier := sqlSetting(inst.Settings, "tier"); sqlSharedCoreTiers[tier] {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"Storage shrink operations are not supported on shared-core instances: instance %s runs %s.", inst.Name, tier)
		return false
	}
	return true
}

func handleSQLGetDiskShrinkConfig(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok || !sqlRequireShrinkable(w, inst) {
		return
	}
	minimal, err := sqlMinimalShrinkTargetGb(inst)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "measure the storage of instance %s: %s", inst.Name, err.Error())
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":                "sql#getDiskShrinkConfig",
		"minimalTargetSizeGb": strconv.FormatInt(minimal, 10),
	})
}

// handleSQLPerformDiskShrink reduces the instance's provisioned storage to
// the target, which must be smaller than it and hold the instance's data; the
// instance restarts when the shrink completes. The Operation enum names no
// shrink, so the operation reports the settings UPDATE it makes.
func handleSQLPerformDiskShrink(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		TargetSizeGb any `json:"targetSizeGb"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	target, given := sqlInt64(req.TargetSizeGb)
	if !given || target <= 0 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "targetSizeGb is required and must be positive.")
		return
	}
	if !sqlRequireShrinkable(w, inst) || !sqlRequireRunning(w, inst) {
		return
	}
	current := sqlDataDiskSizeGb(inst.Settings)
	if target >= current {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"The target size of %d GB must be smaller than the instance's current storage capacity of %d GB.", target, current)
		return
	}
	minimal, err := sqlMinimalShrinkTargetGb(inst)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "measure the storage of instance %s: %s", inst.Name, err.Error())
		return
	}
	if target < minimal {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"The target size of %d GB is smaller than the minimum of %d GB this instance can currently shrink to.", target, minimal)
		return
	}
	op, ok := sqlBeginInstanceOperation(w, inst.Project, inst.Name, "UPDATE")
	if !ok {
		return
	}
	bg.Go(func() {
		sqlInstances.Update(sqlInstanceKey(inst.Project, inst.Name), func(i *SQLInstance) {
			if i.Settings == nil {
				i.Settings = map[string]any{}
			}
			i.Settings["dataDiskSizeGb"] = strconv.FormatInt(target, 10)
			i.Settings["settingsVersion"] = sqlNextSettingsVersion(i.Settings)
		})
		sqlSettleOperation(inst.Project, op.Name, sqlRestartEngine(inst.Project, inst.Name))
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSQLRescheduleMaintenance(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		Reschedule *struct {
			RescheduleType string `json:"rescheduleType"`
			ScheduleTime   string `json:"scheduleTime"`
		} `json:"reschedule"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.Reschedule == nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reschedule is required.")
		return
	}
	switch req.Reschedule.RescheduleType {
	case "IMMEDIATE", "NEXT_AVAILABLE_WINDOW":
	case "SPECIFIC_TIME":
		if _, err := time.Parse(time.RFC3339Nano, req.Reschedule.ScheduleTime); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"reschedule.scheduleTime must be an RFC 3339 timestamp when rescheduleType is SPECIFIC_TIME.")
			return
		}
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"reschedule.rescheduleType must be IMMEDIATE, NEXT_AVAILABLE_WINDOW or SPECIFIC_TIME.")
		return
	}
	// Cloud SQL reschedules only an upcoming maintenance event it announced in
	// scheduledMaintenance; the simulator releases no maintenance update and
	// so schedules none.
	GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
		"The instance %s has no scheduled maintenance to reschedule.", inst.Name)
}

// sqlTruncatableLogs maps truncateLog's logType to the MySQL log table it
// empties.
var sqlTruncatableLogs = map[string]string{
	"MYSQL_GENERAL_TABLE": "mysql.general_log",
	"MYSQL_SLOW_TABLE":    "mysql.slow_log",
}

func handleSQLTruncateLog(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		TruncateLogContext struct {
			LogType string `json:"logType"`
		} `json:"truncateLogContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if !strings.HasPrefix(inst.DatabaseVersion, "MYSQL_") {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"truncateLog truncates MySQL log tables only; instance %s runs %s.", inst.Name, inst.DatabaseVersion)
		return
	}
	table, known := sqlTruncatableLogs[req.TruncateLogContext.LogType]
	if !known {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"truncateLogContext.logType must be MYSQL_GENERAL_TABLE or MYSQL_SLOW_TABLE.")
		return
	}
	if !sqlRequireRunning(w, inst) {
		return
	}
	op, ok := sqlBeginInstanceOperation(w, inst.Project, inst.Name, "TRUNCATE_LOG")
	if !ok {
		return
	}
	bg.Go(func() {
		sqlSettleOperation(inst.Project, op.Name, sqlTruncateLogTable(inst, table))
	})
	sim.WriteJSON(w, http.StatusOK, op)
}

// sqlTruncateLogTable empties a MySQL log table in the instance's engine,
// which it brings up first.
func sqlTruncateLogTable(inst SQLInstance, table string) error {
	plane, ok := sqlLoadDataPlane(inst.Project, inst.Name)
	if !ok {
		return nil
	}
	if err := plane.Ensure(); err != nil {
		return fmt.Errorf("start the database engine: %w", err)
	}
	return plane.execMySQL("TRUNCATE TABLE " + table)
}

// sqlEngineOutput runs command in the instance's running engine and returns
// what it wrote to standard output.
func sqlEngineOutput(plane *sqlDataPlane, command []string) (string, error) {
	containers, err := sim.FindExistingContainers(plane.Labels)
	if err != nil {
		return "", err
	}
	containerID := ""
	for _, container := range containers {
		if container.Running {
			containerID = container.ID
		}
	}
	if containerID == "" {
		return "", fmt.Errorf("the database engine is not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	release, err := sim.HoldThawed(ctx, containerID)
	if err != nil {
		return "", err
	}
	defer release()
	docker := sim.DockerClient()
	created, err := docker.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", fmt.Errorf("create engine command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attach engine command: %w", err)
	}
	var stdout, stderr bytes.Buffer
	_, copyErr := stdcopy.StdCopy(&stdout, &stderr, attached.Reader)
	attached.Close()
	if copyErr != nil {
		return "", fmt.Errorf("read engine command output: %w", copyErr)
	}
	inspected, err := docker.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect engine command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return "", fmt.Errorf("engine command exited %d: %s", inspected.ExitCode, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	return stdout.String(), nil
}

// sqlPostgresRows runs query in database as the admin user and returns each
// row's columns.
func sqlPostgresRows(plane *sqlDataPlane, database, query string) ([][]string, error) {
	out, err := sqlEngineOutput(plane, []string{"psql", "-v", "ON_ERROR_STOP=1", "-At", "-F", "\x1f",
		"-U", "postgres", "-d", database, "-c", query})
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" {
			rows = append(rows, strings.Split(line, "\x1f"))
		}
	}
	return rows, nil
}

// sqlMajorVersion splits a databaseVersion into its engine prefix and its
// major version: POSTGRES_15 is (POSTGRES, 15), POSTGRES_9_6 is (POSTGRES,
// 9.6), MYSQL_8_0_31 is (MYSQL, 8.0).
func sqlMajorVersion(databaseVersion string) (string, string) {
	engine, rest, _ := strings.Cut(databaseVersion, "_")
	parts := strings.Split(rest, "_")
	if len(parts) >= 2 && (engine == "MYSQL" || parts[0] == "9") {
		return engine, parts[0] + "." + parts[1]
	}
	return engine, rest
}

// sqlPostgresPreChecks are the checks of Cloud SQL for PostgreSQL's major
// version upgrade precheck that read the engine, each in every database it
// connects to, with the message and action its documentation gives. The
// simulator's engine grants the postgres role where Cloud SQL grants
// cloudsqlsuperuser.
var sqlPostgresPreChecks = []struct {
	query   string
	message string
	action  string
	item    func(database string, row []string) string
}{
	{
		query: `SELECT DISTINCT n.nspname, c.relname FROM pg_depend d ` +
			`JOIN pg_rewrite w ON w.oid = d.objid JOIN pg_class c ON c.oid = w.ev_class ` +
			`JOIN pg_namespace n ON n.oid = c.relnamespace ` +
			`WHERE d.classid = 'pg_rewrite'::regclass AND d.refclassid = 'pg_class'::regclass ` +
			`AND d.refobjid = 'pg_catalog.pg_stat_activity'::regclass AND c.oid <> d.refobjid ` +
			`AND n.nspname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1, 2`,
		message: "Please remove the following usages of views that depend on functions returning data types of pg_stat_activity before attempting an upgrade:",
		action:  "Drop these views using DROP VIEW view_name;, perform the upgrade, then recreate them listing the columns explicitly.",
		item: func(database string, row []string) string {
			return fmt.Sprintf("(database: %s, schema name: %s, view name: %s)", database, row[0], row[1])
		},
	},
	{
		query: `SELECT DISTINCT n.nspname, c.relname FROM pg_depend d ` +
			`JOIN pg_rewrite w ON w.oid = d.objid JOIN pg_class c ON c.oid = w.ev_class ` +
			`JOIN pg_namespace n ON n.oid = c.relnamespace ` +
			`WHERE d.classid = 'pg_rewrite'::regclass AND d.refclassid = 'pg_class'::regclass ` +
			`AND d.refobjid = 'pg_catalog.pg_stat_replication'::regclass AND c.oid <> d.refobjid ` +
			`AND n.nspname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1, 2`,
		message: "Please remove the following usages of views that depend on functions returning data types of pg_stat_replication before attempting an upgrade:",
		action:  "Drop these views using DROP VIEW view_name;, perform the upgrade, then recreate them listing the columns explicitly.",
		item: func(database string, row []string) string {
			return fmt.Sprintf("(database: %s, schema name: %s, view name: %s)", database, row[0], row[1])
		},
	},
	{
		query: `SELECT t.evtname, r.rolname FROM pg_event_trigger t JOIN pg_roles r ON t.evtowner = r.oid ` +
			`WHERE NOT pg_has_role(r.rolname, 'postgres', 'member') ORDER BY 1`,
		message: "Please ensure that the owners of all event triggers have the cloudsqlsuperuser role assigned to them before attempting an upgrade:",
		action:  "Connect to the identified database using psql or Cloud SQL Studio and change the trigger's owner to a postgres user.",
		item: func(database string, row []string) string {
			return fmt.Sprintf("(database: %s, triggerName %s, owner: %s)", database, row[0], row[1])
		},
	},
}

// sqlPostgresPreCheck runs the precheck against the instance's engine,
// which it brings up first, and returns its findings.
func sqlPostgresPreCheck(inst SQLInstance) ([]map[string]any, error) {
	plane, ok := sqlLoadDataPlane(inst.Project, inst.Name)
	if !ok {
		return []map[string]any{}, nil
	}
	if err := plane.Ensure(); err != nil {
		return nil, fmt.Errorf("start the database engine: %w", err)
	}
	databases, err := sqlPostgresRows(plane, "postgres",
		`SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("list the databases: %w", err)
	}
	findings := []map[string]any{}
	prepared, err := sqlPostgresRows(plane, "postgres", `SELECT database, gid FROM pg_prepared_xacts ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list the prepared transactions: %w", err)
	}
	if len(prepared) > 0 {
		items := make([]string, 0, len(prepared))
		for _, row := range prepared {
			items = append(items, fmt.Sprintf("(database: %s, gid: %s)", row[0], row[1]))
		}
		findings = append(findings, sqlPreCheckFinding(
			"Please commit/rollback the following usages of 'Uncommitted Prepared Statements' before attempting an upgrade:",
			items, "Either commit or roll back the prepared statement."))
	}
	for _, check := range sqlPostgresPreChecks {
		var items []string
		for _, database := range databases {
			rows, err := sqlPostgresRows(plane, database[0], check.query)
			if err != nil {
				return nil, fmt.Errorf("check database %s: %w", database[0], err)
			}
			for _, row := range rows {
				items = append(items, check.item(database[0], row))
			}
		}
		if len(items) > 0 {
			findings = append(findings, sqlPreCheckFinding(check.message, items, check.action))
		}
	}
	return findings, nil
}

func sqlPreCheckFinding(message string, items []string, action string) map[string]any {
	return map[string]any{
		"message":         message + "\n" + strings.Join(items, "\n"),
		"messageType":     "ERROR",
		"actionsRequired": []string{action},
	}
}

func handleSQLPreCheckMajorVersionUpgrade(w http.ResponseWriter, r *http.Request) {
	inst, ok := sqlLifecycleTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		PreCheckMajorVersionUpgradeContext struct {
			TargetDatabaseVersion string `json:"targetDatabaseVersion"`
		} `json:"preCheckMajorVersionUpgradeContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	target := req.PreCheckMajorVersionUpgradeContext.TargetDatabaseVersion
	if target == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"preCheckMajorVersionUpgradeContext.targetDatabaseVersion is required.")
		return
	}
	if inst.InstanceType != "CLOUD_SQL_INSTANCE" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"The instance %s must be a primary instance: the precheck doesn't support replica instances.", inst.Name)
		return
	}
	currentEngine, currentMajor := sqlMajorVersion(inst.DatabaseVersion)
	targetEngine, targetMajor := sqlMajorVersion(target)
	if currentEngine != targetEngine {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"The target database version %s is not a version of the instance's engine, which runs %s.", target, inst.DatabaseVersion)
		return
	}
	switch currentEngine {
	case "POSTGRES":
		currentNumber, currentErr := strconv.ParseFloat(currentMajor, 64)
		targetNumber, targetErr := strconv.ParseFloat(targetMajor, 64)
		if currentErr != nil || targetErr != nil || targetNumber <= currentNumber {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"The target database version %s must be a later major version than %s.", target, inst.DatabaseVersion)
			return
		}
	case "MYSQL":
		if sqlNextMySQLMajor[currentMajor] != targetMajor {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"The precheck checks an upgrade to the next consecutive major version only: %s does not follow %s.", target, inst.DatabaseVersion)
			return
		}
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"The major version upgrade precheck checks Cloud SQL for PostgreSQL and MySQL instances; instance %s runs %s.", inst.Name, inst.DatabaseVersion)
		return
	}
	if !sqlRequireRunning(w, inst) {
		return
	}
	if currentEngine == "MYSQL" {
		sqlUnserved("instances.preCheckMajorVersionUpgrade for MySQL",
			"Cloud SQL runs the MySQL Shell upgrade checker utility, which the simulator does not run")(w, r)
		return
	}
	op, ok := sqlBeginInstanceOperation(w, inst.Project, inst.Name, "PRE_CHECK_MAJOR_VERSION_UPGRADE")
	if !ok {
		return
	}
	key := sqlOperationKey(inst.Project, op.Name)
	sqlOperations.Update(key, func(o *SQLOperation) {
		o.PreCheckMajorVersionUpgradeContext = map[string]any{
			"kind":                  "sql#preCheckMajorVersionUpgradeContext",
			"targetDatabaseVersion": target,
		}
	})
	op, _ = sqlOperations.Get(key)
	bg.Go(func() {
		findings, err := sqlPostgresPreCheck(inst)
		if err == nil {
			sqlOperations.Update(key, func(o *SQLOperation) {
				o.PreCheckMajorVersionUpgradeContext["preCheckResponse"] = findings
			})
		}
		sqlSettleOperation(inst.Project, op.Name, err)
	})
	sim.WriteJSON(w, http.StatusOK, op)
}
