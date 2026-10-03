package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// A DB instance keeps automated backups the way an Aurora cluster does:
// automated DB snapshots named rds:<instance>-<yyyy-mm-dd-hh-mm> as base
// backups, and the engine's own log in the instance volume.
// RestoreDBInstanceToPointInTime seeds the new instance's volume from the
// newest base backup taken by the restore time and replays the source's log
// onto it, and RestoreDBInstanceFromS3 imports a Percona XtraBackup into an
// RDS for MySQL instance's volume, with the machinery the cluster restores
// use. The instance is creating while its volume is seeded, and its endpoint
// serves once the volume holds the restored data.

type rdsInstanceBackups struct{ instanceID string }

func (o rdsInstanceBackups) backupState() (rdsBackupState, error) {
	instance, ok := rdsInstances.Get(o.instanceID)
	if !ok {
		return rdsBackupState{}, fmt.Errorf("DB instance %s no longer exists", o.instanceID)
	}
	secret := instance.BackendMasterUserSecret
	if len(secret) == 0 {
		secret = instance.MasterUserSecret
	}
	return rdsBackupState{
		identifier:    instance.DBInstanceIdentifier,
		resourceID:    instance.DbiResourceId,
		window:        instance.PreferredBackupWindow,
		retention:     instance.BackupRetentionPeriod,
		available:     instance.DBInstanceStatus == "available",
		bases:         instance.BaseBackups,
		backendSecret: secret,
	}, nil
}

func (o rdsInstanceBackups) recordAutomatedSnapshot(snapshotID string, at time.Time) bool {
	if _, exists := rdsSnapshots.Get(snapshotID); exists {
		return false
	}
	instance, ok := rdsInstances.Get(o.instanceID)
	if !ok {
		return false
	}
	rdsSnapshots.Put(snapshotID, RDSSnapshot{
		DBSnapshotIdentifier: snapshotID,
		DBInstanceIdentifier: instance.DBInstanceIdentifier,
		DbiResourceId:        instance.DbiResourceId,
		Engine:               instance.Engine,
		EngineVersion:        instance.EngineVersion,
		Status:               "creating",
		AllocatedStorage:     instance.AllocatedStorage,
		MasterUsername:       instance.MasterUsername,
		DBName:               instance.DBName,
		SnapshotCreateTime:   at.Format(time.RFC3339),
		SnapshotType:         "automated",
		Port:                 instance.Port,
		ARN:                  rdsSnapshotARN(snapshotID),
		MasterUserSecret:     append([]byte(nil), instance.MasterUserSecret...),
	})
	return true
}

func (o rdsInstanceBackups) settleAutomatedSnapshot(snapshotID, status, reason string) {
	rdsSettleSnapshot(snapshotID, status, reason)
}

func (o rdsInstanceBackups) snapshotListed(snapshotID string) bool {
	_, ok := rdsSnapshots.Get(snapshotID)
	return ok
}

func (o rdsInstanceBackups) snapshotVolume(snapshotID string) string {
	return rdsSnapshotVolume(snapshotID)
}

func (o rdsInstanceBackups) expireAutomatedSnapshots(resourceID string, cutoff time.Time) {
	for _, snapshot := range rdsSnapshots.List() {
		created, err := time.Parse(time.RFC3339, snapshot.SnapshotCreateTime)
		if snapshot.SnapshotType == "automated" && snapshot.DbiResourceId == resourceID &&
			snapshot.Status == "available" && err == nil && created.Before(cutoff) {
			rdsSnapshots.Delete(snapshot.DBSnapshotIdentifier)
		}
	}
}

func (o rdsInstanceBackups) setBaseBackups(resourceID string, update func([]RDSBaseBackup) []RDSBaseBackup) {
	rdsInstances.Update(o.instanceID, func(stored *RDSInstance) {
		if stored.DbiResourceId == resourceID {
			stored.BaseBackups = update(stored.BaseBackups)
		}
	})
}

// rdsRemoveInstanceAutomatedBackups deletes a deleted instance's automated
// snapshots and base backups.
func rdsRemoveInstanceAutomatedBackups(instance RDSInstance) {
	volumes := map[string]bool{}
	for _, base := range instance.BaseBackups {
		volumes[rdsSnapshotVolume(base.SnapshotID)] = true
	}
	for _, snapshot := range rdsSnapshots.List() {
		if snapshot.SnapshotType == "automated" && snapshot.DbiResourceId == instance.DbiResourceId {
			rdsSnapshots.Delete(snapshot.DBSnapshotIdentifier)
			volumes[rdsSnapshotVolume(snapshot.DBSnapshotIdentifier)] = true
		}
	}
	for volume := range volumes {
		sim.RemoveVolumeSettled(volume, "rds")
	}
}

// rdsInstanceBackupSettings reads BackupRetentionPeriod, 0 to 35 days, and
// PreferredBackupWindow from a request that creates an instance, defaulting
// to a day and the simulator's window.
func rdsInstanceBackupSettings(r *http.Request, retention int, window string) (int, string, string) {
	if value := r.FormValue("BackupRetentionPeriod"); value != "" {
		retention = atoiOrZero(value)
		if retention < 0 || retention > 35 || fmt.Sprint(retention) != value {
			return 0, "", "The parameter BackupRetentionPeriod must be a value from 0 to 35."
		}
	}
	if value := r.FormValue("PreferredBackupWindow"); value != "" {
		window = value
	}
	if _, err := rdsNextBackupTime(window, time.Now()); err != nil {
		return 0, "", "The backup window must be in the format hh24:mi-hh24:mi."
	}
	return retention, window, ""
}

// renderRDSInstanceBackups renders an instance's backup settings: an Aurora
// DB instance reports its cluster's, which backs the cluster volume up.
func renderRDSInstanceBackups(instance RDSInstance) string {
	if instance.DBClusterIdentifier != "" {
		if cluster, ok := rdsClusters.Get(instance.DBClusterIdentifier); ok {
			instance.BackupRetentionPeriod, instance.PreferredBackupWindow = cluster.BackupRetentionPeriod, cluster.PreferredBackupWindow
		}
		instance.BaseBackups = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", instance.BackupRetentionPeriod)
	fmt.Fprintf(&b, "<PreferredBackupWindow>%s</PreferredBackupWindow>", xmlEscape(instance.PreferredBackupWindow))
	if len(instance.BaseBackups) > 0 {
		fmt.Fprintf(&b, "<LatestRestorableTime>%s</LatestRestorableTime>", time.Now().UTC().Format(rdsRestorableTimeLayout))
	}
	return b.String()
}

func handleRDSRestoreInstanceToPointInTime(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	newID := r.FormValue("TargetDBInstanceIdentifier")
	if newID == "" {
		rdsErrorXML(w, "MissingParameter", "The parameter TargetDBInstanceIdentifier must be provided.", http.StatusBadRequest, requestID)
		return
	}
	src, retained, ok := rdsInstancePointInTimeSource(w, r)
	if !ok {
		return
	}
	srcID := src.DBInstanceIdentifier
	if _, exists := rdsInstances.Get(newID); exists {
		rdsErrorXML(w, "DBInstanceAlreadyExists", fmt.Sprintf("DBInstance %s already exists.", newID), http.StatusConflict, requestID)
		return
	}
	if src.DBClusterIdentifier != "" {
		rdsErrorXML(w, "InvalidParameterCombination",
			fmt.Sprintf("DB instance %s is a member of DB cluster %s; restore the cluster with RestoreDBClusterToPointInTime.", srcID, src.DBClusterIdentifier),
			http.StatusBadRequest, requestID)
		return
	}
	if src.BackupRetentionPeriod == 0 {
		rdsErrorXML(w, "PointInTimeRestoreNotEnabled",
			fmt.Sprintf("SourceDBInstanceIdentifier %s refers to a DB instance with BackupRetentionPeriod equal to 0.", srcID),
			http.StatusBadRequest, requestID)
		return
	}
	target, ok := rdsInstanceRestoreTime(w, r)
	if !ok {
		return
	}
	inst := rdsInstanceFromSource(r, newID, src, r.FormValue("Engine"))
	if !strings.EqualFold(inst.Engine, src.Engine) {
		rdsErrorXML(w, "InvalidParameterCombination",
			fmt.Sprintf("The engine %s is not compatible with the engine %s of DB instance %s.", inst.Engine, src.Engine, srcID),
			http.StatusBadRequest, requestID)
		return
	}
	inst.Engine = src.Engine
	// A live source restores from its own volume, a retained automated backup
	// from the volume its instance left at its deletion.
	logVolume, latest := rdsInstanceVolume(srcID), time.Now().UTC()
	if retained != nil {
		logVolume, latest = rdsRetainedBackupVolume(retained.DbiResourceId), rdsRetainedLatest(retained.LatestTime)
	}
	inst.RestoreSourceVolume = logVolume
	earliest, windowOpen := rdsRestorableWindow(src.BaseBackups, src.BackupRetentionPeriod, time.Now().UTC())
	if retained != nil && (!windowOpen || earliest.After(latest)) {
		rdsErrorXML(w, "InvalidRestoreFault",
			fmt.Sprintf("The automated backup of DB instance %s holds no restorable time.", srcID), http.StatusBadRequest, requestID)
		return
	}
	if !target.IsZero() {
		base, found := rdsBaseBackupFor(src.BaseBackups, target)
		if !windowOpen || !found || target.Before(earliest) || target.After(latest) {
			rdsErrorXML(w, "InvalidRestoreFault",
				fmt.Sprintf("The restore time %s is outside the restorable window of DB instance %s.", r.FormValue("RestoreTime"), srcID),
				http.StatusBadRequest, requestID)
			return
		}
		inst.RestoreSourceVolume = rdsSnapshotVolume(base.SnapshotID)
		inst.RestoreLogVolume = logVolume
		inst.RestoreToTime = target.Format(time.RFC3339Nano)
		inst.RestoreBinlogFile, inst.RestoreBinlogOffset = base.BinlogFile, base.BinlogOffset
	}
	inst.MasterUserSecret = append([]byte(nil), src.MasterUserSecret...)
	inst.BackendMasterUserSecret = append([]byte(nil), src.BackendMasterUserSecret...)
	if inst.RestoreToTime != "" && strings.EqualFold(inst.Engine, "mysql") {
		// The binary log replay installs the master password.
		inst.BackendMasterUserSecret = append([]byte(nil), inst.MasterUserSecret...)
	}
	rdsStartInstanceRestore(w, r, inst, src, "RestoreDBInstanceToPointInTime")
}

// rdsInstancePointInTimeSource resolves the source of a restore to a time:
// a live instance by SourceDBInstanceIdentifier or SourceDbiResourceId, or a
// deleted instance's retained automated backup by SourceDbiResourceId or
// SourceDBInstanceAutomatedBackupsArn, which retained returns.
func rdsInstancePointInTimeSource(w http.ResponseWriter, r *http.Request) (src RDSInstance, retained *RDSInstanceAutomatedBackup, ok bool) {
	requestID := sim.RequestID(r.Context())
	if srcID := r.FormValue("SourceDBInstanceIdentifier"); srcID != "" {
		src, ok = rdsInstances.Get(srcID)
		if !ok {
			rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %s not found.", srcID), http.StatusNotFound, requestID)
		}
		return src, nil, ok
	}
	resourceID, arn := r.FormValue("SourceDbiResourceId"), r.FormValue("SourceDBInstanceAutomatedBackupsArn")
	if resourceID == "" && arn == "" {
		rdsErrorXML(w, "MissingParameter",
			"One of SourceDBInstanceIdentifier, SourceDbiResourceId or SourceDBInstanceAutomatedBackupsArn must be provided.",
			http.StatusBadRequest, requestID)
		return RDSInstance{}, nil, false
	}
	for _, instance := range rdsInstances.List() {
		if (resourceID != "" && instance.DbiResourceId == resourceID) || (arn != "" && rdsInstanceAutoBackupARN(instance.DbiResourceId) == arn) {
			return instance, nil, true
		}
	}
	for _, backup := range rdsInstanceAutomatedBackups.List() {
		if (resourceID != "" && backup.DbiResourceId == resourceID) || (arn != "" && backup.DBInstanceAutomatedBackupsArn == arn) {
			return rdsRetainedInstanceSource(backup), &backup, true
		}
	}
	rdsErrorXML(w, "DBInstanceAutomatedBackupNotFound",
		fmt.Sprintf("No automated backup of DB instance %s%s was found.", resourceID, arn), http.StatusNotFound, requestID)
	return RDSInstance{}, nil, false
}

// rdsInstanceRestoreTime reads RestoreTime or UseLatestRestorableTime: a zero
// time is the latest restorable time.
func rdsInstanceRestoreTime(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	requestID := sim.RequestID(r.Context())
	latest := strings.EqualFold(r.FormValue("UseLatestRestorableTime"), "true")
	restoreTime := r.FormValue("RestoreTime")
	switch {
	case latest && restoreTime != "":
		rdsErrorXML(w, "InvalidParameterCombination", "Cannot specify both RestoreTime and UseLatestRestorableTime.", http.StatusBadRequest, requestID)
		return time.Time{}, false
	case !latest && restoreTime == "":
		rdsErrorXML(w, "InvalidParameterCombination", "RestoreTime must be specified unless UseLatestRestorableTime is enabled.", http.StatusBadRequest, requestID)
		return time.Time{}, false
	case latest:
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, restoreTime)
	if err != nil {
		rdsErrorXML(w, "InvalidParameterValue", fmt.Sprintf("RestoreTime %q is not a valid timestamp.", restoreTime), http.StatusBadRequest, requestID)
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

// rdsStartInstanceRestore records a restored instance as creating with the
// source's backup settings unless the request names its own, and seeds its
// volume in the background.
func rdsStartInstanceRestore(w http.ResponseWriter, r *http.Request, inst, src RDSInstance, operation string) {
	retention, window, problem := rdsInstanceBackupSettings(r, src.BackupRetentionPeriod, src.PreferredBackupWindow)
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	inst.BackupRetentionPeriod, inst.PreferredBackupWindow = retention, window
	inst.DBInstanceStatus = "creating"
	inst.EnableIAMDatabaseAuthentication = strings.EqualFold(r.FormValue("EnableIAMDatabaseAuthentication"), "true")
	inst.DeletionProtection = strings.EqualFold(r.FormValue("DeletionProtection"), "true")
	if src.Port > 0 && r.FormValue("Port") == "" {
		inst.Port = src.Port
	} else if port := atoiOrZero(r.FormValue("Port")); port > 0 {
		inst.Port = port
	}
	rdsInstances.Put(inst.DBInstanceIdentifier, inst)
	id := inst.DBInstanceIdentifier
	bg.Go(func() { rdsFinishInstanceRestore(id) })
	rdsXMLResponse(w, operation, renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSRestoreInstanceFromS3(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	newID := r.FormValue("DBInstanceIdentifier")
	for _, name := range []string{"DBInstanceIdentifier", "DBInstanceClass", "Engine", "MasterUsername", "SourceEngine", "SourceEngineVersion", "S3BucketName", "S3IngestionRoleArn"} {
		if r.FormValue(name) == "" {
			rdsErrorXML(w, "MissingParameter", fmt.Sprintf("The parameter %s must be provided.", name), http.StatusBadRequest, requestID)
			return
		}
	}
	if _, exists := rdsInstances.Get(newID); exists {
		rdsErrorXML(w, "DBInstanceAlreadyExists", fmt.Sprintf("DBInstance %s already exists.", newID), http.StatusConflict, requestID)
		return
	}
	if engine := r.FormValue("Engine"); !strings.EqualFold(engine, "mysql") {
		rdsErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("Engine %s does not restore from Amazon S3; only mysql does.", engine), http.StatusBadRequest, requestID)
		return
	}
	if r.FormValue("SourceEngine") != "mysql" {
		rdsErrorXML(w, "InvalidParameterValue", "The source engine must be mysql.", http.StatusBadRequest, requestID)
		return
	}
	if version := r.FormValue("SourceEngineVersion"); version != "8.0" && !strings.HasPrefix(version, "8.0.") {
		rdsErrorXML(w, "InvalidParameterCombination",
			fmt.Sprintf("Source engine version %s cannot be restored into RDS for MySQL 8.0, which takes MySQL 8.0 backups.", version),
			http.StatusBadRequest, requestID)
		return
	}
	password := r.FormValue("MasterUserPassword")
	if !rdsValidMasterPassword(password) {
		rdsErrorXML(w, "InvalidParameterValue",
			"The parameter MasterUserPassword is not a valid password. It must contain from 8 to 41 printable ASCII characters other than '/', '\"' and '@'.",
			http.StatusBadRequest, requestID)
		return
	}
	bucket, prefix, role := r.FormValue("S3BucketName"), r.FormValue("S3Prefix"), r.FormValue("S3IngestionRoleArn")
	if err := rdsS3IngestionReadable(bucket, prefix, role); err != nil {
		rdsErrorXML(w, "InvalidS3BucketFault",
			fmt.Sprintf("The specified Amazon S3 bucket name can't be found or Amazon RDS isn't authorized to access it: %v", err),
			http.StatusBadRequest, requestID)
		return
	}
	sealed, err := rdsSealMasterPassword(password)
	if err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, requestID)
		return
	}
	inst := rdsInstanceFromSource(r, newID, RDSInstance{}, "mysql")
	inst.MasterUsername = r.FormValue("MasterUsername")
	inst.DBName = r.FormValue("DBName")
	inst.MasterUserSecret = sealed
	inst.BackendMasterUserSecret = append([]byte(nil), sealed...)
	inst.ImportS3Bucket, inst.ImportS3Prefix, inst.ImportS3Role = bucket, prefix, role
	rdsStartInstanceRestore(w, r, inst, RDSInstance{BackupRetentionPeriod: 1, PreferredBackupWindow: rdsDefaultBackupWindow}, "RestoreDBInstanceFromS3")
}

// rdsCreatingInstance reports whether id still names the creating instance
// whose DbiResourceId is resourceID.
func rdsCreatingInstance(id, resourceID string) bool {
	instance, ok := rdsInstances.Get(id)
	return ok && instance.DBInstanceStatus == "creating" && instance.DbiResourceId == resourceID
}

// rdsFinishInstanceRestore seeds a restored instance's volume — from the
// source instance's volume for a restore to the latest restorable time, from
// its base backup and log for a restore to a time, or from a Percona
// XtraBackup in Amazon S3 — then binds its endpoint and lands it available.
// A restore to a time that fails lands the instance incompatible-restore, an
// import that fails lands it failed. A seed a previous process left part-way
// starts again on an empty volume, and an instance deleted while it seeded
// goes once the seed ends.
func rdsFinishInstanceRestore(id string) {
	instance, ok := rdsInstances.Get(id)
	if !ok || instance.DBInstanceStatus != "creating" {
		return
	}
	resourceID := instance.DbiResourceId
	volume := rdsInstanceVolume(id)
	sim.RemoveVolumeSettled(volume, "rds")
	engine, _ := rdsLoggingEngine(instance.Engine)
	var err error
	failed := "incompatible-restore"
	if instance.ImportS3Bucket != "" {
		failed = "failed"
		err = rdsImportXtraBackup(rdsImportTarget{
			engine: engine, volume: volume, label: id,
			masterUsername: instance.MasterUsername, masterUserSecret: instance.MasterUserSecret, database: rdsDatabaseName(instance),
		}, instance.ImportS3Bucket, instance.ImportS3Prefix, instance.ImportS3Role)
	} else {
		err = sim.CaptureVolume(context.Background(), instance.RestoreSourceVolume, volume, "rds")
		if err == nil && instance.RestoreToTime != "" {
			err = rdsLogReplay{
				engine: engine, volume: volume, label: id, logVolume: instance.RestoreLogVolume,
				restoreToTime: instance.RestoreToTime, binlogFile: instance.RestoreBinlogFile, binlogOffset: instance.RestoreBinlogOffset,
				masterUsername: instance.MasterUsername, masterUserSecret: instance.MasterUserSecret,
			}.run()
		}
	}
	status := "available"
	if err == nil && rdsCreatingInstance(id, resourceID) {
		err = rdsStartInstanceEngine(&instance)
	}
	if err != nil {
		log.Printf("Amazon RDS %s: restore: %v", id, err)
		status = failed
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId != resourceID || stored.DBInstanceStatus != "creating" {
			return
		}
		stored.DBInstanceStatus = status
		if status == "available" {
			stored.Endpoint, stored.Port = instance.Endpoint, instance.Port
		}
		stored.RestoreSourceVolume, stored.RestoreLogVolume, stored.RestoreToTime = "", "", ""
		stored.RestoreBinlogFile, stored.RestoreBinlogOffset = "", 0
		stored.ImportS3Bucket, stored.ImportS3Prefix, stored.ImportS3Role = "", "", ""
	})
	rdsFinishInstanceDeletion(id, resourceID)
	rdsExpireRetainedBackups()
}
