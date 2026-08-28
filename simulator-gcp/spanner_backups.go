package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Cloud Spanner backups, backup schedules and RestoreDatabase.
//
// A backup here holds the database's real bytes. CreateBackup serializes the
// database's live SQLite engine — the very engine ExecuteSql and Commit run
// against — into a self-contained SQLite image with VACUUM INTO, and stores
// that image alongside the schema statements that built it. RestoreDatabase
// materializes a new database's engine from that image, so the rows a client
// wrote before the backup are the rows it reads after the restore, byte for
// byte. Nothing about a backup is derived from metadata alone.

// spannerBackup mirrors the Discovery Backup schema. Sizes are int64 fields,
// which the JSON mapping spells as decimal strings.
type spannerBackup struct {
	Name                 string   `json:"name"`
	Database             string   `json:"database,omitempty"`
	VersionTime          string   `json:"versionTime,omitempty"`
	ExpireTime           string   `json:"expireTime,omitempty"`
	MaxExpireTime        string   `json:"maxExpireTime,omitempty"`
	CreateTime           string   `json:"createTime,omitempty"`
	SizeBytes            string   `json:"sizeBytes,omitempty"`
	FreeableSizeBytes    string   `json:"freeableSizeBytes,omitempty"`
	ExclusiveSizeBytes   string   `json:"exclusiveSizeBytes,omitempty"`
	OldestVersionTime    string   `json:"oldestVersionTime,omitempty"`
	State                string   `json:"state,omitempty"`
	DatabaseDialect      string   `json:"databaseDialect,omitempty"`
	ReferencingDatabases []string `json:"referencingDatabases,omitempty"`
	ReferencingBackups   []string `json:"referencingBackups,omitempty"`
	BackupSchedules      []string `json:"backupSchedules,omitempty"`
}

// spannerBackupImage is the backup's payload: the SQLite image of the database
// at the moment the backup was taken, plus the DDL that built its schema. It is
// kept out of the Backup resource because the API never returns bytes to a
// client — a client gets them back only by restoring.
type spannerBackupImage struct {
	Backup           string   `json:"backup"`
	Image            []byte   `json:"image"`
	Statements       []string `json:"statements,omitempty"`
	ProtoDescriptors string   `json:"protoDescriptors,omitempty"`
	DatabaseDialect  string   `json:"databaseDialect,omitempty"`
}

// spannerBackupSchedule mirrors the Discovery BackupSchedule schema.
type spannerBackupSchedule struct {
	Name                  string                     `json:"name"`
	Spec                  *spannerBackupScheduleSpec `json:"spec,omitempty"`
	RetentionDuration     string                     `json:"retentionDuration,omitempty"`
	EncryptionConfig      json.RawMessage            `json:"encryptionConfig,omitempty"`
	FullBackupSpec        json.RawMessage            `json:"fullBackupSpec,omitempty"`
	IncrementalBackupSpec json.RawMessage            `json:"incrementalBackupSpec,omitempty"`
	UpdateTime            string                     `json:"updateTime,omitempty"`
}

type spannerBackupScheduleSpec struct {
	CronSpec *spannerCrontabSpec `json:"cronSpec,omitempty"`
}

type spannerCrontabSpec struct {
	Text           string `json:"text,omitempty"`
	TimeZone       string `json:"timeZone,omitempty"`
	CreationWindow string `json:"creationWindow,omitempty"`
}

// spannerBackupScheduleRun records when a schedule last produced a backup, so
// the scheduler creates one backup per crontab occurrence and no more.
type spannerBackupScheduleRun struct {
	Schedule string `json:"schedule"`
	LastRun  string `json:"lastRun"`
}

// real bytes: capture and restore of a database's SQLite image

// spannerSQLiteStringLiteral renders a filesystem path as a SQLite string
// literal for the VACUUM INTO / ATTACH DATABASE statements, which take the path
// inline rather than as a bind parameter.
func spannerSQLiteStringLiteral(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
}

// spannerCaptureBackupImage serializes a database's live engine into a
// self-contained SQLite image. VACUUM INTO writes a consistent snapshot of
// everything the engine holds — schema, rows, indexes and the applied-DDL
// bookkeeping — which is what makes the restore give back the same rows rather
// than an empty database.
func spannerCaptureBackupImage(dbName string) ([]byte, error) {
	b, err := spannerBackendFor(dbName)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dir, err := os.MkdirTemp("", "spanner-backup-")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "stage backup image for %s: %v", dbName, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	target := filepath.Join(dir, "image.db")
	if _, err := b.db.Exec("VACUUM INTO " + spannerSQLiteStringLiteral(target)); err != nil {
		return nil, status.Errorf(codes.Internal, "snapshot %s: %v", dbName, err)
	}
	image, err := os.ReadFile(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read backup image for %s: %v", dbName, err)
	}
	return image, nil
}

// spannerMaterializeFromImage builds a database's engine from a backup image.
// The image is attached as a second SQLite database and its schema and every
// row are copied into the new engine, so the restored database starts life
// holding exactly the content the backup captured.
func spannerMaterializeFromImage(ctx context.Context, dbName string, image []byte) error {
	if len(image) == 0 {
		return status.Errorf(codes.FailedPrecondition, "backup image for %s holds no bytes", dbName)
	}
	dir, err := os.MkdirTemp("", "spanner-restore-")
	if err != nil {
		return status.Errorf(codes.Internal, "stage restore image for %s: %v", dbName, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	source := filepath.Join(dir, "image.db")
	if err := os.WriteFile(source, image, 0o600); err != nil {
		return status.Errorf(codes.Internal, "write restore image for %s: %v", dbName, err)
	}

	b, err := spannerBackendFor(dbName)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	conn, err := b.db.Conn(ctx)
	if err != nil {
		return status.Errorf(codes.Internal, "open restore connection for %s: %v", dbName, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE "+spannerSQLiteStringLiteral(source)+" AS restore_src"); err != nil {
		return status.Errorf(codes.Internal, "attach restore image for %s: %v", dbName, err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "DETACH DATABASE restore_src") }()

	type schemaEntry struct{ kind, name, sql string }
	rows, err := conn.QueryContext(ctx,
		`SELECT type, name, sql FROM restore_src.sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return status.Errorf(codes.Internal, "read restore image schema for %s: %v", dbName, err)
	}
	var entries []schemaEntry
	for rows.Next() {
		var e schemaEntry
		if err := rows.Scan(&e.kind, &e.name, &e.sql); err != nil {
			_ = rows.Close()
			return status.Errorf(codes.Internal, "scan restore image schema for %s: %v", dbName, err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return status.Errorf(codes.Internal, "read restore image schema for %s: %v", dbName, err)
	}
	_ = rows.Close()

	// Tables and their rows first, then the indexes, views and triggers that
	// depend on them.
	for _, e := range entries {
		if e.kind != "table" {
			continue
		}
		var present int
		if err := conn.QueryRowContext(ctx,
			`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = ?`, e.name).Scan(&present); err != nil {
			return status.Errorf(codes.Internal, "inspect restore target %s: %v", dbName, err)
		}
		if present == 0 {
			if _, err := conn.ExecContext(ctx, e.sql); err != nil {
				return status.Errorf(codes.Internal, "restore table %s into %s: %v", e.name, dbName, err)
			}
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT OR REPLACE INTO main.`+quoteIdent(e.name)+` SELECT * FROM restore_src.`+quoteIdent(e.name)); err != nil {
			return status.Errorf(codes.Internal, "restore rows of %s into %s: %v", e.name, dbName, err)
		}
	}
	for _, e := range entries {
		if e.kind == "table" {
			continue
		}
		if _, err := conn.ExecContext(ctx, e.sql); err != nil {
			return status.Errorf(codes.Internal, "restore %s %s into %s: %v", e.kind, e.name, dbName, err)
		}
	}

	applied, err := spannerReadAppliedDDLCount(b.db)
	if err != nil {
		return status.Errorf(codes.Internal, "read applied DDL count for %s: %v", dbName, err)
	}
	b.appliedDDLCount = applied
	return nil
}

// backups

func spannerBackupName(project, instance, backup string) string {
	return fmt.Sprintf("%s/backups/%s", spannerInstanceName(project, instance), backup)
}

// spannerTakeBackup captures the database's bytes and records the backup. It is
// the single path every backup travels — the API's CreateBackup and the backup
// scheduler both call it, so a scheduled backup holds real bytes exactly as a
// client-requested one does.
func spannerTakeBackup(name, dbName string, createTime, expireTime time.Time, schedules []string) (spannerBackup, error) {
	db, ok := spannerDatabases.Get(dbName)
	if !ok {
		return spannerBackup{}, status.Errorf(codes.NotFound, "database %q not found", dbName)
	}
	image, err := spannerCaptureBackupImage(dbName)
	if err != nil {
		return spannerBackup{}, err
	}
	ddl, _ := spannerDDLs.Get(dbName)
	stamp := createTime.UTC().Format(time.RFC3339Nano)
	backup := spannerBackup{
		Name:                 name,
		Database:             dbName,
		VersionTime:          stamp,
		OldestVersionTime:    stamp,
		CreateTime:           stamp,
		ExpireTime:           expireTime.UTC().Format(time.RFC3339Nano),
		MaxExpireTime:        createTime.UTC().Add(365 * 24 * time.Hour).Format(time.RFC3339Nano),
		SizeBytes:            strconv.Itoa(len(image)),
		ExclusiveSizeBytes:   strconv.Itoa(len(image)),
		FreeableSizeBytes:    "0",
		State:                "READY",
		DatabaseDialect:      db.DatabaseDialect,
		ReferencingDatabases: []string{},
		BackupSchedules:      schedules,
	}
	spannerBackups.Put(backup.Name, backup)
	spannerBackupImages.Put(backup.Name, spannerBackupImage{
		Backup:           backup.Name,
		Image:            image,
		Statements:       ddl.Statements,
		ProtoDescriptors: ddl.ProtoDescriptors,
		DatabaseDialect:  db.DatabaseDialect,
	})
	return backup, nil
}

func spannerBackupResponse(backup spannerBackup) map[string]any {
	m := gcpResourceToMap(backup)
	m["@type"] = "type.googleapis.com/google.spanner.admin.database.v1.Backup"
	return m
}

func handleSpannerCreateBackup(w http.ResponseWriter, r *http.Request, instance string) {
	project := sim.PathParam(r, "project")
	instanceName := spannerInstanceName(project, instance)
	if _, ok := spannerInstances.Get(instanceName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instanceName)
		return
	}
	backupID := spannerQueryParam(r, "backupId", "backup_id")
	var req spannerBackup
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if backupID == "" {
		sim.GCPError(w, http.StatusBadRequest, "backupId is required", "INVALID_ARGUMENT")
		return
	}
	if req.Database == "" {
		sim.GCPError(w, http.StatusBadRequest, "backup.database is required", "INVALID_ARGUMENT")
		return
	}
	expireTime, err := time.Parse(time.RFC3339Nano, req.ExpireTime)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "backup.expireTime is required and must be a timestamp: %v", err)
		return
	}
	name := spannerBackupName(project, instance, backupID)
	if _, exists := spannerBackups.Get(name); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "backup %q already exists", name)
		return
	}
	metadata := map[string]any{
		"@type":    "type.googleapis.com/google.spanner.admin.database.v1.CreateBackupMetadata",
		"name":     name,
		"database": req.Database,
		"progress": map[string]any{"progressPercent": 100, "startTime": nowTimestamp(), "endTime": nowTimestamp()},
	}
	backup, err := spannerTakeBackup(name, req.Database, time.Now(), expireTime, nil)
	if err != nil {
		st := status.Convert(err)
		op := newSpannerFailedOperation(name+"/operations", metadata, int(st.Code()), st.Message())
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	op := newSpannerOperation(name+"/operations", metadata, spannerBackupResponse(backup))
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSpannerCopyBackup(w http.ResponseWriter, r *http.Request, instance string) {
	project := sim.PathParam(r, "project")
	instanceName := spannerInstanceName(project, instance)
	if _, ok := spannerInstances.Get(instanceName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instanceName)
		return
	}
	var req struct {
		BackupID     string `json:"backupId"`
		SourceBackup string `json:"sourceBackup"`
		ExpireTime   string `json:"expireTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.BackupID == "" || req.SourceBackup == "" {
		sim.GCPError(w, http.StatusBadRequest, "backupId and sourceBackup are required", "INVALID_ARGUMENT")
		return
	}
	expireTime, err := time.Parse(time.RFC3339Nano, req.ExpireTime)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "expireTime is required and must be a timestamp: %v", err)
		return
	}
	source, ok := spannerBackups.Get(req.SourceBackup)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", req.SourceBackup)
		return
	}
	sourceImage, ok := spannerBackupImages.Get(req.SourceBackup)
	if !ok {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "backup %q holds no image", req.SourceBackup)
		return
	}
	name := spannerBackupName(project, instance, req.BackupID)
	if _, exists := spannerBackups.Get(name); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "backup %q already exists", name)
		return
	}
	// A copy carries the source's captured bytes: restoring the copy gives back
	// the same rows the source would.
	copied := source
	copied.Name = name
	copied.CreateTime = nowTimestamp()
	copied.ExpireTime = expireTime.UTC().Format(time.RFC3339Nano)
	copied.BackupSchedules = nil
	copied.ReferencingBackups = nil
	spannerBackups.Put(name, copied)
	image := sourceImage
	image.Backup = name
	spannerBackupImages.Put(name, image)
	op := newSpannerOperation(name+"/operations", map[string]any{
		"@type":        "type.googleapis.com/google.spanner.admin.database.v1.CopyBackupMetadata",
		"name":         name,
		"sourceBackup": source.Name,
		"progress":     map[string]any{"progressPercent": 100, "startTime": copied.CreateTime, "endTime": copied.CreateTime},
	}, spannerBackupResponse(copied))
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleSpannerGetBackup(w http.ResponseWriter, r *http.Request, instance, backup string) {
	name := spannerBackupName(sim.PathParam(r, "project"), instance, backup)
	stored, ok := spannerBackups.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, stored)
}

func handleSpannerListBackups(w http.ResponseWriter, r *http.Request, instance string) {
	prefix := spannerInstanceName(sim.PathParam(r, "project"), instance) + "/backups/"
	out := spannerBackups.Filter(func(b spannerBackup) bool { return strings.HasPrefix(b.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	out = gcpApplyListParams(out, r)
	page, next, ok := paginateList(w, r, out)
	if !ok {
		return
	}
	body := map[string]any{"backups": page}
	if next != "" {
		body["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleSpannerUpdateBackup(w http.ResponseWriter, r *http.Request, instance, backup string) {
	name := spannerBackupName(sim.PathParam(r, "project"), instance, backup)
	stored, ok := spannerBackups.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", name)
		return
	}
	var req spannerBackup
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	mask := spannerQueryParam(r, "updateMask", "update_mask")
	if mask == "" {
		sim.GCPError(w, http.StatusBadRequest, "updateMask is required", "INVALID_ARGUMENT")
		return
	}
	for _, field := range strings.Split(mask, ",") {
		switch strings.TrimSpace(field) {
		case "expireTime", "expire_time":
			if _, err := time.Parse(time.RFC3339Nano, req.ExpireTime); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "expireTime must be a timestamp: %v", err)
				return
			}
			stored.ExpireTime = req.ExpireTime
		default:
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"field %q is not updatable on a Cloud Spanner backup", strings.TrimSpace(field))
			return
		}
	}
	spannerBackups.Put(name, stored)
	sim.WriteJSON(w, http.StatusOK, stored)
}

func handleSpannerDeleteBackup(w http.ResponseWriter, r *http.Request, instance, backup string) {
	name := spannerBackupName(sim.PathParam(r, "project"), instance, backup)
	if !spannerBackups.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", name)
		return
	}
	spannerBackupImages.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// spannerDeleteBackupsUnder removes every backup (and its captured bytes) whose
// name sits under prefix — the cleanup DeleteInstance performs.
func spannerDeleteBackupsUnder(prefix string) {
	for _, backup := range spannerBackups.List() {
		if strings.HasPrefix(backup.Name, prefix) {
			spannerBackups.Delete(backup.Name)
			spannerBackupImages.Delete(backup.Name)
		}
	}
}

func handleSpannerBackupIAM(w http.ResponseWriter, r *http.Request, instance, backup, verb string) {
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), spannerBackupName(sim.PathParam(r, "project"), instance, backup), verb)
	default:
		gcpMethodNotFound(w)
	}
}

// RestoreDatabase

func handleSpannerRestoreDatabase(w http.ResponseWriter, r *http.Request, instance string) {
	project := sim.PathParam(r, "project")
	instanceName := spannerInstanceName(project, instance)
	if _, ok := spannerInstances.Get(instanceName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instanceName)
		return
	}
	var req struct {
		DatabaseID string `json:"databaseId"`
		Backup     string `json:"backup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.DatabaseID == "" || req.Backup == "" {
		sim.GCPError(w, http.StatusBadRequest, "databaseId and backup are required", "INVALID_ARGUMENT")
		return
	}
	backup, ok := spannerBackups.Get(req.Backup)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", req.Backup)
		return
	}
	image, ok := spannerBackupImages.Get(req.Backup)
	if !ok {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "backup %q holds no image", req.Backup)
		return
	}
	name := spannerDatabaseName(project, instance, req.DatabaseID)
	if _, exists := spannerDatabases.Get(name); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "database %q already exists", name)
		return
	}

	metadata := map[string]any{
		"@type":      "type.googleapis.com/google.spanner.admin.database.v1.RestoreDatabaseMetadata",
		"name":       name,
		"sourceType": "BACKUP",
		"backupInfo": map[string]any{
			"backup":         backup.Name,
			"versionTime":    backup.VersionTime,
			"createTime":     backup.CreateTime,
			"sourceDatabase": backup.Database,
		},
		"progress": map[string]any{"progressPercent": 100, "startTime": nowTimestamp(), "endTime": nowTimestamp()},
	}
	// The engine must be materialized before the schema store learns about the
	// database: spannerBackendFor replays recorded DDL onto a fresh engine, and
	// the restored image already carries that schema and its rows.
	if err := spannerMaterializeFromImage(r.Context(), name, image.Image); err != nil {
		if dropErr := spannerDropBackend(name); dropErr != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "release backing store for %q: %v", name, dropErr)
			return
		}
		st := status.Convert(err)
		op := newSpannerFailedOperation(name+"/operations", metadata, int(st.Code()), st.Message())
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	spannerDDLs.Put(name, spannerDatabaseDDL{
		Database:         name,
		Statements:       image.Statements,
		ProtoDescriptors: image.ProtoDescriptors,
	})
	now := nowTimestamp()
	db := spannerDatabase{
		Name:                   name,
		State:                  "READY",
		CreateTime:             now,
		VersionRetentionPeriod: "1h",
		EarliestVersionTime:    now,
		DatabaseDialect:        image.DatabaseDialect,
		RestoreInfo: &spannerRestoreInfo{
			SourceType: "BACKUP",
			BackupInfo: &spannerBackupInfo{
				Backup:         backup.Name,
				VersionTime:    backup.VersionTime,
				CreateTime:     backup.CreateTime,
				SourceDatabase: backup.Database,
			},
		},
	}
	spannerDatabases.Put(name, db)
	backup.ReferencingDatabases = appendUniqueString(backup.ReferencingDatabases, name)
	spannerBackups.Put(backup.Name, backup)

	op := newSpannerOperation(name+"/operations", metadata, spannerDatabaseResponse(db))
	sim.WriteJSON(w, http.StatusOK, op)
}

func appendUniqueString(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

// backup schedules

func handleSpannerCreateBackupSchedule(w http.ResponseWriter, r *http.Request, instance, database string) {
	dbName := spannerDatabaseName(sim.PathParam(r, "project"), instance, database)
	if _, ok := spannerDatabases.Get(dbName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", dbName)
		return
	}
	scheduleID := spannerQueryParam(r, "backupScheduleId", "backup_schedule_id")
	var req spannerBackupSchedule
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if scheduleID == "" {
		sim.GCPError(w, http.StatusBadRequest, "backupScheduleId is required", "INVALID_ARGUMENT")
		return
	}
	schedule := req
	schedule.Name = dbName + "/backupSchedules/" + scheduleID
	if _, exists := spannerBackupSchedules.Get(schedule.Name); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "backup schedule %q already exists", schedule.Name)
		return
	}
	if err := spannerValidateBackupSchedule(schedule); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	schedule.UpdateTime = nowTimestamp()
	spannerBackupSchedules.Put(schedule.Name, schedule)
	// The schedule starts counting from now: a crontab occurrence that fell
	// before the schedule existed is not one it missed.
	spannerBackupScheduleRuns.Put(schedule.Name, spannerBackupScheduleRun{
		Schedule: schedule.Name,
		LastRun:  time.Now().UTC().Format(time.RFC3339Nano),
	})
	sim.WriteJSON(w, http.StatusOK, schedule)
}

// spannerParseDuration parses the protobuf Duration wire form ("3600s") the
// Cloud Spanner API uses for retention windows.
func spannerParseDuration(s string) (time.Duration, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("duration is empty")
	}
	if !strings.HasSuffix(trimmed, "s") {
		return 0, fmt.Errorf("duration %q must end in 's'", s)
	}
	d, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("duration %q is not a valid protobuf Duration: %w", s, err)
	}
	return d, nil
}

func spannerValidateBackupSchedule(schedule spannerBackupSchedule) error {
	if schedule.Spec == nil || schedule.Spec.CronSpec == nil || strings.TrimSpace(schedule.Spec.CronSpec.Text) == "" {
		return fmt.Errorf("spec.cronSpec.text is required")
	}
	if _, err := spannerParseCrontab(schedule.Spec.CronSpec.Text); err != nil {
		return err
	}
	if strings.TrimSpace(schedule.RetentionDuration) == "" {
		return fmt.Errorf("retentionDuration is required")
	}
	if _, err := spannerParseDuration(schedule.RetentionDuration); err != nil {
		return err
	}
	return nil
}

func handleSpannerGetBackupSchedule(w http.ResponseWriter, r *http.Request, database, scheduleID string) {
	name := database + "/backupSchedules/" + scheduleID
	stored, ok := spannerBackupSchedules.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup schedule %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, stored)
}

func handleSpannerListBackupSchedules(w http.ResponseWriter, r *http.Request, instance, database string) {
	dbName := spannerDatabaseName(sim.PathParam(r, "project"), instance, database)
	if _, ok := spannerDatabases.Get(dbName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "database %q not found", dbName)
		return
	}
	prefix := dbName + "/backupSchedules/"
	out := spannerBackupSchedules.Filter(func(s spannerBackupSchedule) bool { return strings.HasPrefix(s.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	out = gcpApplyListParams(out, r)
	page, next, ok := paginateList(w, r, out)
	if !ok {
		return
	}
	body := map[string]any{"backupSchedules": page}
	if next != "" {
		body["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleSpannerUpdateBackupSchedule(w http.ResponseWriter, r *http.Request, database, scheduleID string) {
	name := database + "/backupSchedules/" + scheduleID
	stored, ok := spannerBackupSchedules.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup schedule %q not found", name)
		return
	}
	var req spannerBackupSchedule
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	mask := spannerQueryParam(r, "updateMask", "update_mask")
	if mask == "" {
		sim.GCPError(w, http.StatusBadRequest, "updateMask is required", "INVALID_ARGUMENT")
		return
	}
	for _, field := range strings.Split(mask, ",") {
		switch strings.TrimSpace(field) {
		case "spec.cronSpec.text", "spec":
			stored.Spec = req.Spec
		case "retentionDuration", "retention_duration":
			stored.RetentionDuration = req.RetentionDuration
		case "encryptionConfig", "encryption_config":
			stored.EncryptionConfig = req.EncryptionConfig
		default:
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"field %q is not updatable on a Cloud Spanner backup schedule", strings.TrimSpace(field))
			return
		}
	}
	if err := spannerValidateBackupSchedule(stored); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	stored.UpdateTime = nowTimestamp()
	spannerBackupSchedules.Put(name, stored)
	sim.WriteJSON(w, http.StatusOK, stored)
}

func handleSpannerDeleteBackupSchedule(w http.ResponseWriter, r *http.Request, database, scheduleID string) {
	name := database + "/backupSchedules/" + scheduleID
	if !spannerBackupSchedules.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup schedule %q not found", name)
		return
	}
	spannerBackupScheduleRuns.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// spannerDeleteBackupSchedulesFor removes the schedules attached to a database
// that is going away. The backups they already produced outlive the database,
// exactly as backups do on the real service.
func spannerDeleteBackupSchedulesFor(dbName string) {
	prefix := dbName + "/backupSchedules/"
	for _, schedule := range spannerBackupSchedules.List() {
		if strings.HasPrefix(schedule.Name, prefix) {
			spannerBackupSchedules.Delete(schedule.Name)
			spannerBackupScheduleRuns.Delete(schedule.Name)
		}
	}
}

func handleSpannerBackupScheduleIAM(w http.ResponseWriter, r *http.Request, resource, verb string) {
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), resource, verb)
	default:
		gcpMethodNotFound(w)
	}
}

// the scheduler: crontab occurrences produce real backups

// spannerCrontab is a parsed five-field crontab expression (minute, hour,
// day-of-month, month, day-of-week) in UTC — the form Cloud Spanner's
// CrontabSpec.text takes.
type spannerCrontab struct {
	minute, hour, dom, month, dow map[int]bool
}

func spannerParseCrontab(text string) (spannerCrontab, error) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 5 {
		return spannerCrontab{}, fmt.Errorf("crontab %q must have 5 fields (minute hour day-of-month month day-of-week)", text)
	}
	ranges := []struct {
		lo, hi int
		out    *map[int]bool
	}{}
	var cron spannerCrontab
	ranges = append(ranges,
		struct {
			lo, hi int
			out    *map[int]bool
		}{0, 59, &cron.minute},
		struct {
			lo, hi int
			out    *map[int]bool
		}{0, 23, &cron.hour},
		struct {
			lo, hi int
			out    *map[int]bool
		}{1, 31, &cron.dom},
		struct {
			lo, hi int
			out    *map[int]bool
		}{1, 12, &cron.month},
		struct {
			lo, hi int
			out    *map[int]bool
		}{0, 7, &cron.dow},
	)
	for i, spec := range ranges {
		set, err := spannerParseCronField(fields[i], spec.lo, spec.hi)
		if err != nil {
			return spannerCrontab{}, fmt.Errorf("crontab %q field %d: %w", text, i+1, err)
		}
		*spec.out = set
	}
	// Cron accepts both 0 and 7 for Sunday.
	if cron.dow[7] {
		cron.dow[0] = true
	}
	return cron, nil
}

func spannerParseCronField(field string, lo, hi int) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty term")
		}
		step := 1
		if slash := strings.Index(part, "/"); slash >= 0 {
			n, err := strconv.Atoi(part[slash+1:])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid step %q", part[slash+1:])
			}
			step = n
			part = part[:slash]
		}
		start, end := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			bounds := strings.SplitN(part, "-", 2)
			a, errA := strconv.Atoi(strings.TrimSpace(bounds[0]))
			b, errB := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if errA != nil || errB != nil || a < lo || b > hi || a > b {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			start, end = a, b
		default:
			n, err := strconv.Atoi(part)
			if err != nil || n < lo || n > hi {
				return nil, fmt.Errorf("invalid value %q", part)
			}
			start, end = n, n
		}
		for v := start; v <= end; v += step {
			out[v] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("matches nothing")
	}
	return out, nil
}

// matches reports whether the crontab fires at t (UTC, minute resolution).
// Cron's day fields are a union when either is restricted, which is the rule
// crond and Cloud Scheduler both follow.
func (c spannerCrontab) matches(t time.Time) bool {
	t = t.UTC()
	if !c.minute[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	domRestricted := len(c.dom) < 31
	dowRestricted := len(c.dow) < 8
	domMatch := c.dom[t.Day()]
	dowMatch := c.dow[int(t.Weekday())]
	switch {
	case domRestricted && dowRestricted:
		return domMatch || dowMatch
	case domRestricted:
		return domMatch
	case dowRestricted:
		return dowMatch
	}
	return true
}

// spannerBackupScheduleWindow bounds how far back the scheduler looks for a
// missed crontab occurrence. A simulator that was not running did not take the
// backups it was asleep for, and the real service does not backfill either.
const spannerBackupScheduleWindow = 24 * time.Hour

// spannerRunDueBackupSchedules takes one real backup for every schedule whose
// crontab fired since it last ran. It is called by the scheduler loop with the
// wall clock; taking `now` as an argument keeps the rule under test at a chosen
// instant without changing what the running simulator does.
func spannerRunDueBackupSchedules(now time.Time) []string {
	var created []string
	for _, schedule := range spannerBackupSchedules.List() {
		cron, err := spannerParseCrontab(schedule.Spec.GetCronText())
		if err != nil {
			continue
		}
		retention, err := spannerParseDuration(schedule.RetentionDuration)
		if err != nil {
			continue
		}
		run, _ := spannerBackupScheduleRuns.Get(schedule.Name)
		lastRun, err := time.Parse(time.RFC3339Nano, run.LastRun)
		if err != nil {
			lastRun = now.Add(-spannerBackupScheduleWindow)
		}
		occurrence, fired := spannerLatestCronOccurrence(cron, lastRun, now)
		if !fired {
			continue
		}
		dbName := schedule.Name[:strings.LastIndex(schedule.Name, "/backupSchedules/")]
		scheduleID := schedule.Name[strings.LastIndex(schedule.Name, "/")+1:]
		instanceName := dbName[:strings.LastIndex(dbName, "/databases/")]
		backupName := fmt.Sprintf("%s/backups/%s-%s", instanceName, scheduleID, occurrence.UTC().Format("20060102t150405"))
		if _, exists := spannerBackups.Get(backupName); exists {
			continue
		}
		if _, err := spannerTakeBackup(backupName, dbName, occurrence, occurrence.Add(retention), []string{schedule.Name}); err != nil {
			continue
		}
		spannerBackupScheduleRuns.Put(schedule.Name, spannerBackupScheduleRun{
			Schedule: schedule.Name,
			LastRun:  now.UTC().Format(time.RFC3339Nano),
		})
		created = append(created, backupName)
	}
	sort.Strings(created)
	return created
}

// GetCronText reads the crontab text out of a possibly-absent spec.
func (s *spannerBackupScheduleSpec) GetCronText() string {
	if s == nil || s.CronSpec == nil {
		return ""
	}
	return s.CronSpec.Text
}

// spannerLatestCronOccurrence returns the most recent minute in (after, now]
// at which the crontab fired, bounded by the scheduler's look-back window.
func spannerLatestCronOccurrence(cron spannerCrontab, after, now time.Time) (time.Time, bool) {
	cursor := now.UTC().Truncate(time.Minute)
	floor := after.UTC().Truncate(time.Minute)
	if window := now.UTC().Add(-spannerBackupScheduleWindow).Truncate(time.Minute); floor.Before(window) {
		floor = window
	}
	for !cursor.Before(floor) {
		if cursor.After(after) && cron.matches(cursor) {
			return cursor, true
		}
		cursor = cursor.Add(-time.Minute)
	}
	return time.Time{}, false
}

// spannerRunBackupScheduleLoop is the running simulator's backup scheduler: it
// wakes often enough to catch every minute a crontab can name and takes the
// backups that are due. Started from main, so building the server in-process
// (route conformance, coverage probing) does not start a clock.
func spannerRunBackupScheduleLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		spannerRunDueBackupSchedules(time.Now())
	}
}
