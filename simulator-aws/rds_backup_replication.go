package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// StartDBInstanceAutomatedBackupsReplication, called in the destination
// Region, replicates a DB instance's automated backups there: a replicated
// automated backup holds its own copy of every automated snapshot the source
// takes and of the source's log, so it restores to a time after the source
// and the source's own automated backups are gone. The simulator holds its DB
// instances in its own Region, so the destination is any other Region a
// request is signed for, and only requests signed for that Region read,
// restore and delete the replicated backup. The copy is pending until it
// holds an automated snapshot and replicating after; a restore from it first
// brings its log up to the source's. StopDBInstanceAutomatedBackupsReplication,
// or the source's deletion, takes a last copy and leaves the backup retained,
// and it expires once its own backup retention period has passed since then.

var (
	// rdsReplicatedBackups holds the replicated automated backups, keyed by
	// DBInstanceAutomatedBackupsArn.
	rdsReplicatedBackups sim.Store[RDSInstanceAutomatedBackup]
	// rdsReplicationMu serialises the copies into replicated automated backups.
	rdsReplicationMu sync.Mutex
)

func rdsReplicatedSnapshotVolume(replicaID, snapshotID string) string {
	return rdsVolume("replicated-snapshot", replicaID+"."+strings.ReplaceAll(snapshotID, ":", "."))
}

func rdsReplicatedLogVolume(replicaID string) string {
	return rdsVolume("replicated-backup", replicaID)
}

func (b RDSInstanceAutomatedBackup) replicated() bool { return b.DestinationRegion != "" }

// logVolume is the volume holding the log a restore from the backup replays.
func (b RDSInstanceAutomatedBackup) logVolume() string {
	if b.replicated() {
		return rdsReplicatedLogVolume(b.ReplicaID)
	}
	return rdsRetainedBackupVolume(b.DbiResourceId)
}

// snapshotVolume is the volume holding one of the backup's automated
// snapshots.
func (b RDSInstanceAutomatedBackup) snapshotVolume(snapshotID string) string {
	if b.replicated() {
		return rdsReplicatedSnapshotVolume(b.ReplicaID, snapshotID)
	}
	return rdsSnapshotVolume(snapshotID)
}

// restoring reports whether a creating restore reads the backup.
func (b RDSInstanceAutomatedBackup) restoring(restoring map[string]bool) bool {
	if restoring[b.logVolume()] {
		return true
	}
	for _, base := range b.BaseBackups {
		if restoring[b.snapshotVolume(base.SnapshotID)] {
			return true
		}
	}
	return false
}

// rdsRequestRegion is the Region a request is signed for.
func rdsRequestRegion(r *http.Request) string {
	if region := iamRequestedRegion(r); region != "" {
		return region
	}
	return awsRegion()
}

// rdsReplicatedBackupIn is the replicated automated backup arn names in
// region.
func rdsReplicatedBackupIn(region, arn string) (RDSInstanceAutomatedBackup, bool) {
	backup, ok := rdsReplicatedBackups.Get(arn)
	if !ok || backup.DestinationRegion != region {
		return RDSInstanceAutomatedBackup{}, false
	}
	return backup, true
}

// rdsSourceReplicationIn is the replicated automated backup the source
// instance replicates to region.
func rdsSourceReplicationIn(source RDSInstance, region string) (RDSInstanceAutomatedBackup, bool) {
	for _, arn := range source.AutomatedBackupsReplications {
		if backup, ok := rdsReplicatedBackupIn(region, arn); ok {
			return backup, true
		}
	}
	return RDSInstanceAutomatedBackup{}, false
}

func rdsReplicatedBackupARN(region, replicaID string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:auto-backup:%s", region, awsAccountID(), replicaID)
}

func handleRDSStartAutomatedBackupsReplication(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	destination := rdsRequestRegion(r)
	sourceARN := r.FormValue("SourceDBInstanceArn")
	if sourceARN == "" {
		rdsErrorXML(w, "MissingParameter", "The parameter SourceDBInstanceArn must be provided.", http.StatusBadRequest, requestID)
		return
	}
	source, ok := rdsInstanceByArn(sourceARN)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %s not found.", sourceARN), http.StatusNotFound, requestID)
		return
	}
	if destination == awsRegion() {
		rdsErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("The source DB instance %s is in %s; automated backups replicate to a different Region.", sourceARN, destination),
			http.StatusBadRequest, requestID)
		return
	}
	live, ok := rdsLiveInstanceAutoBackup(source)
	if !ok {
		rdsErrorXML(w, "InvalidDBInstanceState",
			fmt.Sprintf("DB instance %s keeps no automated backups to replicate.", source.DBInstanceIdentifier), http.StatusBadRequest, requestID)
		return
	}
	if existing, replicating := rdsSourceReplicationIn(source, destination); replicating {
		rdsErrorXML(w, "InvalidDBInstanceAutomatedBackupState",
			fmt.Sprintf("DB instance %s already replicates its automated backups to %s as %s.",
				source.DBInstanceIdentifier, destination, existing.DBInstanceAutomatedBackupsArn),
			http.StatusBadRequest, requestID)
		return
	}
	retention := 7
	if value := r.FormValue("BackupRetentionPeriod"); value != "" {
		retention = atoiOrZero(value)
		if retention < 1 || retention > 35 || fmt.Sprint(retention) != value {
			rdsErrorXML(w, "InvalidParameterValue", "The parameter BackupRetentionPeriod must be a value from 1 to 35.", http.StatusBadRequest, requestID)
			return
		}
	}
	replica := live
	replica.ReplicaID = "ab-" + strings.ToLower(strings.TrimPrefix(rdsResourceID(), "db-"))
	replica.DBInstanceAutomatedBackupsArn = rdsReplicatedBackupARN(destination, replica.ReplicaID)
	replica.DestinationRegion = destination
	replica.Status = "pending"
	replica.BackupRetentionPeriod = retention
	replica.KmsKeyId = r.FormValue("KmsKeyId")
	replica.Tags = parseAWSQueryTagMap(r, "Tags.Tag")
	replica.BaseBackups = nil
	replica.AutomatedBackupsReplications = nil
	replica.MasterUserSecret = append([]byte(nil), source.MasterUserSecret...)
	replica.BackendMasterUserSecret = append([]byte(nil), source.BackendMasterUserSecret...)
	rdsReplicatedBackups.Put(replica.DBInstanceAutomatedBackupsArn, replica)
	rdsInstances.Update(source.DBInstanceIdentifier, func(instance *RDSInstance) {
		instance.AutomatedBackupsReplications = append(instance.AutomatedBackupsReplications, replica.DBInstanceAutomatedBackupsArn)
	})
	rdsScheduleReplication(replica.DBInstanceAutomatedBackupsArn)
	rdsXMLResponse(w, "StartDBInstanceAutomatedBackupsReplication", renderRDSInstanceAutoBackup(replica), requestID)
}

func handleRDSStopAutomatedBackupsReplication(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	destination := rdsRequestRegion(r)
	sourceARN := r.FormValue("SourceDBInstanceArn")
	source, ok := rdsInstanceByArn(sourceARN)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %s not found.", sourceARN), http.StatusNotFound, requestID)
		return
	}
	replica, ok := rdsSourceReplicationIn(source, destination)
	if !ok {
		rdsErrorXML(w, "InvalidDBInstanceState",
			fmt.Sprintf("DB instance %s does not replicate its automated backups to %s.", source.DBInstanceIdentifier, destination),
			http.StatusBadRequest, requestID)
		return
	}
	retained, err := rdsRetainReplicatedBackup(r.Context(), replica.DBInstanceAutomatedBackupsArn)
	if err != nil {
		rdsErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, requestID)
		return
	}
	rdsXMLResponse(w, "StopDBInstanceAutomatedBackupsReplication", renderRDSInstanceAutoBackup(retained), requestID)
}

// rdsScheduleReplication copies into the replicated automated backup what it
// does not hold yet, in the background.
func rdsScheduleReplication(arn string) {
	bg.Go(func() {
		if _, err := rdsSyncReplicatedBackup(context.Background(), arn); err != nil {
			log.Printf("Amazon RDS replicated automated backup %s: %v", arn, err)
		}
	})
}

// rdsSyncReplicatedBackup copies into a pending or replicating automated
// backup every automated snapshot the source took after the newest it holds,
// and the source's log as it stands, then expires the automated snapshots its
// own backup retention period no longer covers.
func rdsSyncReplicatedBackup(ctx context.Context, arn string) (RDSInstanceAutomatedBackup, error) {
	rdsReplicationMu.Lock()
	defer rdsReplicationMu.Unlock()
	replica, ok := rdsReplicatedBackups.Get(arn)
	if !ok || replica.Status == "retained" {
		return replica, nil
	}
	source, ok := rdsInstances.Get(replica.DBInstanceIdentifier)
	if !ok || source.DbiResourceId != replica.DbiResourceId {
		return replica, errRDSReplicationSourceGone
	}
	restoring := rdsRestoringVolumes()
	var newest time.Time
	if held := len(replica.BaseBackups); held > 0 {
		newest = replica.BaseBackups[held-1].takenAt()
	}
	bases := append([]RDSBaseBackup(nil), replica.BaseBackups...)
	for _, base := range source.BaseBackups {
		if !base.takenAt().After(newest) {
			continue
		}
		if err := sim.CaptureVolume(ctx, rdsSnapshotVolume(base.SnapshotID), replica.snapshotVolume(base.SnapshotID), "rds"); err != nil {
			return replica, fmt.Errorf("copy the automated snapshot %s: %w", base.SnapshotID, err)
		}
		bases = append(bases, base)
	}
	copied := rdsRetainedLatest(replica.LatestTime)
	// A restore reading the log keeps the copy it started from.
	if !restoring[replica.logVolume()] {
		copied = time.Now().UTC()
		sim.RemoveVolumeSettled(replica.logVolume(), "rds")
		if err := sim.CaptureVolume(ctx, rdsInstanceVolume(source.DBInstanceIdentifier), replica.logVolume(), "rds"); err != nil {
			return replica, fmt.Errorf("copy the log: %w", err)
		}
	}
	keep := rdsBaseBackupsToKeep(bases, copied.AddDate(0, 0, -replica.BackupRetentionPeriod))
	for _, base := range bases[:len(bases)-len(keep)] {
		if restoring[replica.snapshotVolume(base.SnapshotID)] {
			keep = append([]RDSBaseBackup{base}, keep...)
			continue
		}
		sim.RemoveVolumeSettled(replica.snapshotVolume(base.SnapshotID), "rds")
	}
	var synced RDSInstanceAutomatedBackup
	rdsReplicatedBackups.Update(arn, func(stored *RDSInstanceAutomatedBackup) {
		stored.BaseBackups = keep
		stored.LatestTime = copied.Format(rdsRestorableTimeLayout)
		stored.EngineVersion = source.EngineVersion
		stored.AllocatedStorage = source.AllocatedStorage
		stored.MasterUserSecret = append([]byte(nil), source.MasterUserSecret...)
		stored.BackendMasterUserSecret = append([]byte(nil), source.BackendMasterUserSecret...)
		if len(keep) > 0 {
			stored.Status = "replicating"
		}
		synced = *stored
	})
	return synced, nil
}

var errRDSReplicationSourceGone = errors.New("the source DB instance no longer exists")

// rdsRetainReplicatedBackup takes a last copy into a replicated automated
// backup, ends its replication and leaves it retained. One whose source is
// gone keeps the copy it holds.
func rdsRetainReplicatedBackup(ctx context.Context, arn string) (RDSInstanceAutomatedBackup, error) {
	if _, err := rdsSyncReplicatedBackup(ctx, arn); err != nil && !errors.Is(err, errRDSReplicationSourceGone) {
		return RDSInstanceAutomatedBackup{}, err
	}
	var retained RDSInstanceAutomatedBackup
	rdsReplicatedBackups.Update(arn, func(stored *RDSInstanceAutomatedBackup) {
		stored.Status = "retained"
		retained = *stored
	})
	rdsInstances.Update(retained.DBInstanceIdentifier, func(instance *RDSInstance) {
		if instance.DbiResourceId != retained.DbiResourceId {
			return
		}
		kept := instance.AutomatedBackupsReplications[:0]
		for _, replication := range instance.AutomatedBackupsReplications {
			if replication != arn {
				kept = append(kept, replication)
			}
		}
		instance.AutomatedBackupsReplications = kept
	})
	rdsArmRetainedBackupExpiry(rdsRetainedExpiry(retained.LatestTime, retained.BackupRetentionPeriod))
	return retained, nil
}

// rdsRetainInstanceReplications leaves every automated backup a deleted
// instance replicates retained, once its engine has stopped.
func rdsRetainInstanceReplications(instance RDSInstance) {
	for _, arn := range instance.AutomatedBackupsReplications {
		if _, err := rdsRetainReplicatedBackup(context.Background(), arn); err != nil {
			log.Printf("Amazon RDS %s: retain the replicated automated backup %s: %v", instance.DBInstanceIdentifier, arn, err)
		}
	}
}

// rdsReplicateInstanceBackups copies an instance's new automated snapshot into
// every automated backup it replicates.
func rdsReplicateInstanceBackups(instance RDSInstance) {
	for _, arn := range instance.AutomatedBackupsReplications {
		rdsScheduleReplication(arn)
	}
}

// rdsRemoveReplicatedBackup deletes a replicated automated backup with its
// copies.
func rdsRemoveReplicatedBackup(backup RDSInstanceAutomatedBackup) {
	rdsReplicatedBackups.Delete(backup.DBInstanceAutomatedBackupsArn)
	for _, base := range backup.BaseBackups {
		sim.RemoveVolumeSettled(backup.snapshotVolume(base.SnapshotID), "rds")
	}
	sim.RemoveVolumeSettled(backup.logVolume(), "rds")
}

// rdsRecoverReplicatedBackups arms the expiry of the retained replicated
// automated backups and resumes the copies into the others.
func rdsRecoverReplicatedBackups() {
	for _, backup := range rdsReplicatedBackups.List() {
		if backup.Status == "retained" {
			rdsArmRetainedBackupExpiry(rdsRetainedExpiry(backup.LatestTime, backup.BackupRetentionPeriod))
			continue
		}
		arn := backup.DBInstanceAutomatedBackupsArn
		bg.Go(func() {
			if _, err := rdsSyncReplicatedBackup(context.Background(), arn); errors.Is(err, errRDSReplicationSourceGone) {
				_, err = rdsRetainReplicatedBackup(context.Background(), arn)
				if err != nil {
					log.Printf("Amazon RDS replicated automated backup %s: %v", arn, err)
				}
			} else if err != nil {
				log.Printf("Amazon RDS replicated automated backup %s: %v", arn, err)
			}
		})
	}
}
