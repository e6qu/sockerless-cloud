package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A DB instance or DB cluster with a backup retention period keeps automated
// backups: its automated snapshots and the log since the oldest of them. A
// DB instance's automated backup is creating until its first automated
// snapshot and active after it. DeleteDBInstance and DeleteDBCluster remove
// the automated backups unless DeleteAutomatedBackups is false; then the
// deletion keeps them as a retained automated backup — the base backups, a
// copy of the volume holding the log up to the deletion, and the properties
// a restore reads — which RestoreDBInstanceToPointInTime and
// RestoreDBClusterToPointInTime restore through SourceDbiResourceId,
// SourceDBInstanceAutomatedBackupsArn or SourceDbClusterResourceId, and
// which expires once its backup retention period has passed since the
// deletion.

// RDSInstanceAutomatedBackup is a deleted DB instance's retained automated
// backup, keyed by the instance's DbiResourceId.
type RDSInstanceAutomatedBackup struct {
	DBInstanceAutomatedBackupsArn    string
	DBInstanceArn                    string
	DBInstanceIdentifier             string
	DbiResourceId                    string
	Region                           string
	Engine                           string
	EngineVersion                    string
	DBInstanceClass                  string
	Status                           string
	AllocatedStorage                 int
	MasterUsername                   string
	DBName                           string
	Port                             int
	InstanceCreateTime               string
	BackupRetentionPeriod            int
	PreferredBackupWindow            string
	IAMDatabaseAuthenticationEnabled bool
	// LatestTime is when the instance was deleted.
	LatestTime              string
	BaseBackups             []RDSBaseBackup `json:",omitempty"`
	MasterUserSecret        []byte          `json:",omitempty"`
	BackendMasterUserSecret []byte          `json:",omitempty"`
}

// RDSClusterAutomatedBackup is a deleted DB cluster's retained automated
// backup, keyed by the cluster's DbClusterResourceId.
type RDSClusterAutomatedBackup struct {
	DBClusterAutomatedBackupsArn     string
	DBClusterArn                     string
	DBClusterIdentifier              string
	DbClusterResourceId              string
	Region                           string
	Engine                           string
	EngineVersion                    string
	EngineMode                       string
	Status                           string
	AllocatedStorage                 int
	MasterUsername                   string
	DatabaseName                     string
	Port                             int
	ClusterCreateTime                string
	BackupRetentionPeriod            int
	PreferredBackupWindow            string
	IAMDatabaseAuthenticationEnabled bool
	StorageEncrypted                 bool
	// LatestTime is when the cluster was deleted.
	LatestTime              string
	BaseBackups             []RDSBaseBackup `json:",omitempty"`
	MasterUserSecret        []byte          `json:",omitempty"`
	BackendMasterUserSecret []byte          `json:",omitempty"`
}

// rdsRetainedBackupVolume names the volume that holds a retained automated
// backup's log: the deleted resource's volume as its engine left it.
func rdsRetainedBackupVolume(resourceID string) string {
	return rdsVolume("auto-backup", resourceID)
}

func rdsRetainedLatest(latest string) time.Time {
	at, _ := time.Parse(rdsRestorableTimeLayout, latest)
	return at
}

func rdsRetainedExpiry(latest string, retention int) time.Time {
	return rdsRetainedLatest(latest).AddDate(0, 0, retention)
}

// rdsRetainedWindow is the window a retained automated backup restores to a
// time in, ending at the deletion.
func rdsRetainedWindow(bases []RDSBaseBackup, retention int, latest string) (earliest, end time.Time, ok bool) {
	end = rdsRetainedLatest(latest)
	earliest, ok = rdsRestorableWindow(bases, retention, time.Now().UTC())
	if ok && earliest.After(end) {
		return time.Time{}, time.Time{}, false
	}
	return earliest, end, ok
}

// rdsRetainInstanceBackups keeps a deleted instance's automated backups once
// its engine has stopped.
func rdsRetainInstanceBackups(instance RDSInstance) error {
	if err := sim.CaptureVolume(context.Background(), rdsInstanceVolume(instance.DBInstanceIdentifier),
		rdsRetainedBackupVolume(instance.DbiResourceId), "rds"); err != nil {
		return fmt.Errorf("keep the log volume: %w", err)
	}
	backup := RDSInstanceAutomatedBackup{
		DBInstanceAutomatedBackupsArn:    rdsInstanceAutoBackupARN(instance.DbiResourceId),
		DBInstanceArn:                    instance.ARN,
		DBInstanceIdentifier:             instance.DBInstanceIdentifier,
		DbiResourceId:                    instance.DbiResourceId,
		Region:                           awsRegion(),
		Engine:                           instance.Engine,
		EngineVersion:                    instance.EngineVersion,
		DBInstanceClass:                  instance.DBInstanceClass,
		Status:                           "retained",
		AllocatedStorage:                 instance.AllocatedStorage,
		MasterUsername:                   instance.MasterUsername,
		DBName:                           instance.DBName,
		Port:                             instance.Port,
		InstanceCreateTime:               instance.InstanceCreateTime,
		BackupRetentionPeriod:            instance.BackupRetentionPeriod,
		PreferredBackupWindow:            instance.PreferredBackupWindow,
		IAMDatabaseAuthenticationEnabled: instance.EnableIAMDatabaseAuthentication,
		LatestTime:                       time.Now().UTC().Format(rdsRestorableTimeLayout),
		BaseBackups:                      instance.BaseBackups,
		MasterUserSecret:                 instance.MasterUserSecret,
		BackendMasterUserSecret:          instance.BackendMasterUserSecret,
	}
	rdsInstanceAutomatedBackups.Put(instance.DbiResourceId, backup)
	rdsArmRetainedBackupExpiry(rdsRetainedExpiry(backup.LatestTime, backup.BackupRetentionPeriod))
	return nil
}

// rdsRetainClusterBackups keeps a deleted cluster's automated backups once its
// engine has stopped.
func rdsRetainClusterBackups(cluster RDSCluster) error {
	if err := sim.CaptureVolume(context.Background(), rdsClusterVolume(cluster.DBClusterIdentifier),
		rdsRetainedBackupVolume(cluster.DbClusterResourceId), "rds"); err != nil {
		return fmt.Errorf("keep the log volume: %w", err)
	}
	backup := RDSClusterAutomatedBackup{
		DBClusterAutomatedBackupsArn:     rdsClusterAutoBackupARN(cluster.DbClusterResourceId),
		DBClusterArn:                     cluster.ARN,
		DBClusterIdentifier:              cluster.DBClusterIdentifier,
		DbClusterResourceId:              cluster.DbClusterResourceId,
		Region:                           awsRegion(),
		Engine:                           cluster.Engine,
		EngineVersion:                    cluster.EngineVersion,
		EngineMode:                       cluster.EngineMode,
		Status:                           "retained",
		AllocatedStorage:                 cluster.AllocatedStorage,
		MasterUsername:                   cluster.MasterUsername,
		DatabaseName:                     cluster.DatabaseName,
		Port:                             cluster.Port,
		ClusterCreateTime:                cluster.ClusterCreateTime,
		BackupRetentionPeriod:            cluster.BackupRetentionPeriod,
		PreferredBackupWindow:            cluster.PreferredBackupWindow,
		IAMDatabaseAuthenticationEnabled: cluster.EnableIAMDatabaseAuthentication,
		StorageEncrypted:                 cluster.StorageEncrypted,
		LatestTime:                       time.Now().UTC().Format(rdsRestorableTimeLayout),
		BaseBackups:                      cluster.BaseBackups,
		MasterUserSecret:                 cluster.MasterUserSecret,
		BackendMasterUserSecret:          cluster.BackendMasterUserSecret,
	}
	rdsClusterAutomatedBackups.Put(cluster.DbClusterResourceId, backup)
	rdsArmRetainedBackupExpiry(rdsRetainedExpiry(backup.LatestTime, backup.BackupRetentionPeriod))
	return nil
}

// rdsRemoveRetainedInstanceBackup deletes a retained automated backup with its
// automated snapshots and volumes.
func rdsRemoveRetainedInstanceBackup(backup RDSInstanceAutomatedBackup) {
	rdsInstanceAutomatedBackups.Delete(backup.DbiResourceId)
	rdsRemoveInstanceAutomatedBackups(RDSInstance{DbiResourceId: backup.DbiResourceId, BaseBackups: backup.BaseBackups})
	sim.RemoveVolumeSettled(rdsRetainedBackupVolume(backup.DbiResourceId), "rds")
}

func rdsRemoveRetainedClusterBackup(backup RDSClusterAutomatedBackup) {
	rdsClusterAutomatedBackups.Delete(backup.DbClusterResourceId)
	rdsRemoveAutomatedBackups(RDSCluster{DbClusterResourceId: backup.DbClusterResourceId, BaseBackups: backup.BaseBackups})
	sim.RemoveVolumeSettled(rdsRetainedBackupVolume(backup.DbClusterResourceId), "rds")
}

// rdsRestoringVolumes are the volumes a creating restore reads: the volume it
// seeds from and the volume whose log it replays.
func rdsRestoringVolumes() map[string]bool {
	volumes := map[string]bool{}
	for _, cluster := range rdsClusters.List() {
		if cluster.Status == "creating" {
			volumes[cluster.RestoreSourceVolume] = true
			volumes[cluster.RestoreLogVolume] = true
		}
	}
	for _, instance := range rdsInstances.List() {
		if instance.DBInstanceStatus == "creating" {
			volumes[instance.RestoreSourceVolume] = true
			volumes[instance.RestoreLogVolume] = true
		}
	}
	delete(volumes, "")
	return volumes
}

// rdsRetainedBackupRestoring reports whether a creating restore reads the
// retained automated backup of resourceID.
func rdsRetainedBackupRestoring(restoring map[string]bool, resourceID string, bases []RDSBaseBackup, snapshotVolume func(string) string) bool {
	if restoring[rdsRetainedBackupVolume(resourceID)] {
		return true
	}
	for _, base := range bases {
		if restoring[snapshotVolume(base.SnapshotID)] {
			return true
		}
	}
	return false
}

// rdsArmRetainedBackupExpiry expires the retained automated backups due at
// expiry.
func rdsArmRetainedBackupExpiry(expiry time.Time) {
	time.AfterFunc(time.Until(expiry), rdsExpireRetainedBackups)
}

// rdsExpireRetainedBackups deletes the retained automated backups whose
// retention period has passed. One a creating restore still reads goes when
// that restore ends.
func rdsExpireRetainedBackups() {
	now := time.Now()
	restoring := rdsRestoringVolumes()
	for _, backup := range rdsInstanceAutomatedBackups.List() {
		if now.Before(rdsRetainedExpiry(backup.LatestTime, backup.BackupRetentionPeriod)) ||
			rdsRetainedBackupRestoring(restoring, backup.DbiResourceId, backup.BaseBackups, rdsSnapshotVolume) {
			continue
		}
		rdsRemoveRetainedInstanceBackup(backup)
	}
	for _, backup := range rdsClusterAutomatedBackups.List() {
		if now.Before(rdsRetainedExpiry(backup.LatestTime, backup.BackupRetentionPeriod)) ||
			rdsRetainedBackupRestoring(restoring, backup.DbClusterResourceId, backup.BaseBackups, rdsClusterSnapshotVolume) {
			continue
		}
		rdsRemoveRetainedClusterBackup(backup)
	}
}

// rdsRecoverRetainedBackups arms the expiry of the retained automated backups
// a previous process kept, and drops the rows an earlier simulator stored for
// live resources: the listing reads a live resource's automated backup from
// the resource itself.
func rdsRecoverRetainedBackups() {
	for _, backup := range rdsInstanceAutomatedBackups.List() {
		if backup.Status != "retained" {
			rdsInstanceAutomatedBackups.Delete(backup.DbiResourceId)
			continue
		}
		rdsArmRetainedBackupExpiry(rdsRetainedExpiry(backup.LatestTime, backup.BackupRetentionPeriod))
	}
	for _, backup := range rdsClusterAutomatedBackups.List() {
		if backup.Status != "retained" || backup.LatestTime == "" {
			rdsClusterAutomatedBackups.Delete(backup.DbClusterResourceId)
			continue
		}
		rdsArmRetainedBackupExpiry(rdsRetainedExpiry(backup.LatestTime, backup.BackupRetentionPeriod))
	}
}

// rdsLiveInstanceAutoBackup is the automated backup of a live DB instance
// that keeps one: an instance outside a DB cluster, with a backup retention
// period, on an engine that keeps the log a restore replays.
func rdsLiveInstanceAutoBackup(instance RDSInstance) (RDSInstanceAutomatedBackup, bool) {
	engine, ok := rdsEngine(instance.Engine)
	if instance.DBClusterIdentifier != "" || instance.BackupRetentionPeriod == 0 || !ok || !rdsKeepsLog(engine) {
		return RDSInstanceAutomatedBackup{}, false
	}
	status := "active"
	if len(instance.BaseBackups) == 0 {
		status = "creating"
	}
	return RDSInstanceAutomatedBackup{
		DBInstanceAutomatedBackupsArn:    rdsInstanceAutoBackupARN(instance.DbiResourceId),
		DBInstanceArn:                    instance.ARN,
		DBInstanceIdentifier:             instance.DBInstanceIdentifier,
		DbiResourceId:                    instance.DbiResourceId,
		Region:                           awsRegion(),
		Engine:                           instance.Engine,
		EngineVersion:                    instance.EngineVersion,
		DBInstanceClass:                  instance.DBInstanceClass,
		Status:                           status,
		AllocatedStorage:                 instance.AllocatedStorage,
		MasterUsername:                   instance.MasterUsername,
		DBName:                           instance.DBName,
		Port:                             instance.Port,
		InstanceCreateTime:               instance.InstanceCreateTime,
		BackupRetentionPeriod:            instance.BackupRetentionPeriod,
		PreferredBackupWindow:            instance.PreferredBackupWindow,
		IAMDatabaseAuthenticationEnabled: instance.EnableIAMDatabaseAuthentication,
		BaseBackups:                      instance.BaseBackups,
	}, true
}

func rdsRenderRestoreWindow(b *strings.Builder, earliest, latest time.Time) {
	fmt.Fprintf(b, "<RestoreWindow><EarliestTime>%s</EarliestTime><LatestTime>%s</LatestTime></RestoreWindow>",
		earliest.Format(rdsRestorableTimeLayout), latest.Format(rdsRestorableTimeLayout))
}

func renderRDSInstanceAutoBackup(backup RDSInstanceAutomatedBackup) string {
	var b strings.Builder
	b.WriteString("<DBInstanceAutomatedBackup>")
	fmt.Fprintf(&b, "<DBInstanceAutomatedBackupsArn>%s</DBInstanceAutomatedBackupsArn>", xmlEscape(backup.DBInstanceAutomatedBackupsArn))
	fmt.Fprintf(&b, "<DBInstanceArn>%s</DBInstanceArn>", xmlEscape(backup.DBInstanceArn))
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(backup.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(backup.DbiResourceId))
	fmt.Fprintf(&b, "<Region>%s</Region>", xmlEscape(backup.Region))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(backup.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(backup.EngineVersion))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(backup.Status))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", backup.AllocatedStorage)
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(backup.MasterUsername))
	fmt.Fprintf(&b, "<Port>%d</Port>", backup.Port)
	fmt.Fprintf(&b, "<InstanceCreateTime>%s</InstanceCreateTime>", xmlEscape(backup.InstanceCreateTime))
	fmt.Fprintf(&b, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", backup.BackupRetentionPeriod)
	fmt.Fprintf(&b, "<PreferredBackupWindow>%s</PreferredBackupWindow>", xmlEscape(backup.PreferredBackupWindow))
	fmt.Fprintf(&b, "<IAMDatabaseAuthenticationEnabled>%t</IAMDatabaseAuthenticationEnabled>", backup.IAMDatabaseAuthenticationEnabled)
	if backup.Status == "retained" {
		if earliest, latest, ok := rdsRetainedWindow(backup.BaseBackups, backup.BackupRetentionPeriod, backup.LatestTime); ok {
			rdsRenderRestoreWindow(&b, earliest, latest)
		}
	} else if latest := time.Now().UTC(); len(backup.BaseBackups) > 0 {
		earliest, _ := rdsRestorableWindow(backup.BaseBackups, backup.BackupRetentionPeriod, latest)
		rdsRenderRestoreWindow(&b, earliest, latest)
	}
	b.WriteString("</DBInstanceAutomatedBackup>")
	return b.String()
}

func renderRDSClusterAutoBackup(backup RDSClusterAutomatedBackup) string {
	var b strings.Builder
	b.WriteString("<DBClusterAutomatedBackup>")
	fmt.Fprintf(&b, "<DBClusterAutomatedBackupsArn>%s</DBClusterAutomatedBackupsArn>", xmlEscape(backup.DBClusterAutomatedBackupsArn))
	fmt.Fprintf(&b, "<DBClusterArn>%s</DBClusterArn>", xmlEscape(backup.DBClusterArn))
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(backup.DBClusterIdentifier))
	fmt.Fprintf(&b, "<DbClusterResourceId>%s</DbClusterResourceId>", xmlEscape(backup.DbClusterResourceId))
	fmt.Fprintf(&b, "<Region>%s</Region>", xmlEscape(backup.Region))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(backup.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(backup.EngineVersion))
	fmt.Fprintf(&b, "<EngineMode>%s</EngineMode>", xmlEscape(backup.EngineMode))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(backup.Status))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", backup.AllocatedStorage)
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(backup.MasterUsername))
	fmt.Fprintf(&b, "<Port>%d</Port>", backup.Port)
	fmt.Fprintf(&b, "<ClusterCreateTime>%s</ClusterCreateTime>", xmlEscape(backup.ClusterCreateTime))
	fmt.Fprintf(&b, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", backup.BackupRetentionPeriod)
	fmt.Fprintf(&b, "<PreferredBackupWindow>%s</PreferredBackupWindow>", xmlEscape(backup.PreferredBackupWindow))
	fmt.Fprintf(&b, "<IAMDatabaseAuthenticationEnabled>%t</IAMDatabaseAuthenticationEnabled>", backup.IAMDatabaseAuthenticationEnabled)
	fmt.Fprintf(&b, "<StorageEncrypted>%t</StorageEncrypted>", backup.StorageEncrypted)
	if earliest, latest, ok := rdsRetainedWindow(backup.BaseBackups, backup.BackupRetentionPeriod, backup.LatestTime); ok {
		rdsRenderRestoreWindow(&b, earliest, latest)
	}
	b.WriteString("</DBClusterAutomatedBackup>")
	return b.String()
}

// rdsFilterValues is every value of the request's filter name.
func rdsFilterValues(r *http.Request, name string) []string {
	var values []string
	for n := 1; ; n++ {
		prefix := fmt.Sprintf("Filters.Filter.%d", n)
		filterName := r.FormValue(prefix + ".Name")
		if filterName == "" {
			return values
		}
		if filterName != name {
			continue
		}
		for v := 1; ; v++ {
			value := r.FormValue(fmt.Sprintf("%s.Values.Value.%d", prefix, v))
			if value == "" {
				break
			}
			values = append(values, value)
		}
	}
}

// rdsFilterMatches reports whether a filter that names values admits any of
// candidates; a filter the request does not name admits everything.
func rdsFilterMatches(values []string, candidates ...string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		for _, candidate := range candidates {
			if strings.EqualFold(value, candidate) {
				return true
			}
		}
	}
	return false
}

// rdsInstanceAutoBackups is every DB instance's automated backup: the live
// instances' and the retained ones.
func rdsInstanceAutoBackups() []RDSInstanceAutomatedBackup {
	var backups []RDSInstanceAutomatedBackup
	for _, instance := range rdsInstances.List() {
		if backup, ok := rdsLiveInstanceAutoBackup(instance); ok {
			backups = append(backups, backup)
		}
	}
	return append(backups, rdsInstanceAutomatedBackups.List()...)
}

func handleRDSDescribeInstanceAutomatedBackups(w http.ResponseWriter, r *http.Request) {
	statuses := rdsFilterValues(r, "status")
	instanceIDs := append(rdsFilterValues(r, "db-instance-id"), r.FormValue("DBInstanceIdentifier"))
	resourceIDs := append(rdsFilterValues(r, "dbi-resource-id"), r.FormValue("DbiResourceId"))
	arn := r.FormValue("DBInstanceAutomatedBackupsArn")
	var b strings.Builder
	b.WriteString("<DBInstanceAutomatedBackups>")
	for _, backup := range rdsInstanceAutoBackups() {
		if !rdsFilterMatches(statuses, backup.Status) ||
			!rdsFilterMatches(rdsNonEmpty(instanceIDs), backup.DBInstanceIdentifier, backup.DBInstanceArn) ||
			!rdsFilterMatches(rdsNonEmpty(resourceIDs), backup.DbiResourceId) ||
			(arn != "" && arn != backup.DBInstanceAutomatedBackupsArn) {
			continue
		}
		b.WriteString(renderRDSInstanceAutoBackup(backup))
	}
	b.WriteString("</DBInstanceAutomatedBackups>")
	rdsXMLResponse(w, "DescribeDBInstanceAutomatedBackups", b.String(), sim.RequestID(r.Context()))
}

func rdsNonEmpty(values []string) []string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func handleRDSDeleteInstanceAutomatedBackup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	arn, resourceID := r.FormValue("DBInstanceAutomatedBackupsArn"), r.FormValue("DbiResourceId")
	for _, backup := range rdsInstanceAutoBackups() {
		if (arn == "" || arn != backup.DBInstanceAutomatedBackupsArn) && (resourceID == "" || resourceID != backup.DbiResourceId) {
			continue
		}
		if backup.Status != "retained" {
			rdsErrorXML(w, "InvalidDBInstanceAutomatedBackupState",
				fmt.Sprintf("The automated backup %s belongs to the DB instance %s, which still exists.", backup.DbiResourceId, backup.DBInstanceIdentifier),
				http.StatusBadRequest, requestID)
			return
		}
		if rdsRetainedBackupRestoring(rdsRestoringVolumes(), backup.DbiResourceId, backup.BaseBackups, rdsSnapshotVolume) {
			rdsErrorXML(w, "InvalidDBInstanceAutomatedBackupState",
				fmt.Sprintf("The automated backup %s is being restored.", backup.DbiResourceId), http.StatusBadRequest, requestID)
			return
		}
		rdsRemoveRetainedInstanceBackup(backup)
		backup.Status = "deleting"
		rdsXMLResponse(w, "DeleteDBInstanceAutomatedBackup", renderRDSInstanceAutoBackup(backup), requestID)
		return
	}
	rdsErrorXML(w, "DBInstanceAutomatedBackupNotFound", "Automated backup not found", http.StatusNotFound, requestID)
}

// handleRDSDescribeClusterAutomatedBackups lists the retained automated
// backups of deleted DB clusters, the only status DBClusterAutomatedBackup
// names.
func handleRDSDescribeClusterAutomatedBackups(w http.ResponseWriter, r *http.Request) {
	statuses := rdsFilterValues(r, "status")
	clusterIDs := rdsNonEmpty(append(rdsFilterValues(r, "db-cluster-id"), r.FormValue("DBClusterIdentifier")))
	resourceIDs := rdsNonEmpty(append(rdsFilterValues(r, "db-cluster-resource-id"), r.FormValue("DbClusterResourceId")))
	var b strings.Builder
	b.WriteString("<DBClusterAutomatedBackups>")
	for _, backup := range rdsClusterAutomatedBackups.List() {
		if !rdsFilterMatches(statuses, backup.Status) ||
			!rdsFilterMatches(clusterIDs, backup.DBClusterIdentifier, backup.DBClusterArn) ||
			!rdsFilterMatches(resourceIDs, backup.DbClusterResourceId) {
			continue
		}
		b.WriteString(renderRDSClusterAutoBackup(backup))
	}
	b.WriteString("</DBClusterAutomatedBackups>")
	rdsXMLResponse(w, "DescribeDBClusterAutomatedBackups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterAutomatedBackup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	resourceID := r.FormValue("DbClusterResourceId")
	backup, ok := rdsClusterAutomatedBackups.Get(resourceID)
	if !ok {
		for _, cluster := range rdsClusters.List() {
			if cluster.DbClusterResourceId == resourceID {
				rdsErrorXML(w, "InvalidDBClusterAutomatedBackupStateFault",
					fmt.Sprintf("The automated backup %s belongs to the DB cluster %s, which still exists.", resourceID, cluster.DBClusterIdentifier),
					http.StatusBadRequest, requestID)
				return
			}
		}
		rdsErrorXML(w, "DBClusterAutomatedBackupNotFoundFault", "Automated backup not found", http.StatusNotFound, requestID)
		return
	}
	if rdsRetainedBackupRestoring(rdsRestoringVolumes(), backup.DbClusterResourceId, backup.BaseBackups, rdsClusterSnapshotVolume) {
		rdsErrorXML(w, "InvalidDBClusterAutomatedBackupStateFault",
			fmt.Sprintf("The automated backup %s is being restored.", resourceID), http.StatusBadRequest, requestID)
		return
	}
	rdsRemoveRetainedClusterBackup(backup)
	backup.Status = "deleting"
	rdsXMLResponse(w, "DeleteDBClusterAutomatedBackup", renderRDSClusterAutoBackup(backup), requestID)
}

// rdsRetainedInstanceSource is the deleted instance a retained automated
// backup restores, with the volume holding its log.
func rdsRetainedInstanceSource(backup RDSInstanceAutomatedBackup) RDSInstance {
	return RDSInstance{
		DBInstanceIdentifier:    backup.DBInstanceIdentifier,
		DbiResourceId:           backup.DbiResourceId,
		DBInstanceClass:         backup.DBInstanceClass,
		Engine:                  backup.Engine,
		EngineVersion:           backup.EngineVersion,
		MasterUsername:          backup.MasterUsername,
		DBName:                  backup.DBName,
		AllocatedStorage:        backup.AllocatedStorage,
		Port:                    backup.Port,
		BackupRetentionPeriod:   backup.BackupRetentionPeriod,
		PreferredBackupWindow:   backup.PreferredBackupWindow,
		BaseBackups:             backup.BaseBackups,
		MasterUserSecret:        backup.MasterUserSecret,
		BackendMasterUserSecret: backup.BackendMasterUserSecret,
	}
}

func rdsRetainedClusterSource(backup RDSClusterAutomatedBackup) RDSCluster {
	return RDSCluster{
		DBClusterIdentifier:             backup.DBClusterIdentifier,
		DbClusterResourceId:             backup.DbClusterResourceId,
		Engine:                          backup.Engine,
		EngineVersion:                   backup.EngineVersion,
		EngineMode:                      backup.EngineMode,
		MasterUsername:                  backup.MasterUsername,
		DatabaseName:                    backup.DatabaseName,
		AllocatedStorage:                backup.AllocatedStorage,
		Port:                            backup.Port,
		StorageEncrypted:                backup.StorageEncrypted,
		EnableIAMDatabaseAuthentication: backup.IAMDatabaseAuthenticationEnabled,
		BackupRetentionPeriod:           backup.BackupRetentionPeriod,
		PreferredBackupWindow:           backup.PreferredBackupWindow,
		BaseBackups:                     backup.BaseBackups,
		MasterUserSecret:                backup.MasterUserSecret,
		BackendMasterUserSecret:         backup.BackendMasterUserSecret,
	}
}

// rdsKeepOrRemoveInstanceBackups keeps a deleted instance's automated backups
// when its deletion asked for that and removes them otherwise, once its
// engine has stopped.
func rdsKeepOrRemoveInstanceBackups(instance RDSInstance) {
	if instance.RetainAutomatedBackups && instance.BackupRetentionPeriod > 0 {
		err := rdsRetainInstanceBackups(instance)
		if err == nil {
			return
		}
		log.Printf("Amazon RDS %s: retain automated backups: %v", instance.DBInstanceIdentifier, err)
	}
	rdsRemoveInstanceAutomatedBackups(instance)
}

func rdsKeepOrRemoveClusterBackups(cluster RDSCluster) {
	if cluster.RetainAutomatedBackups && cluster.BackupRetentionPeriod > 0 {
		err := rdsRetainClusterBackups(cluster)
		if err == nil {
			return
		}
		log.Printf("Amazon RDS cluster %s: retain automated backups: %v", cluster.DBClusterIdentifier, err)
	}
	rdsRemoveAutomatedBackups(cluster)
}
