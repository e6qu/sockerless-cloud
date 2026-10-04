package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

const rdsWALArchive = "sockerless_wal_archive"

// rdsClusterBackups is an Aurora cluster as the owner of its automated
// backups, which are automated DB cluster snapshots of its cluster volume.
type rdsClusterBackups struct{ clusterID string }

func (o rdsClusterBackups) backupState() (rdsBackupState, error) {
	cluster, ok := rdsClusters.Get(o.clusterID)
	if !ok {
		return rdsBackupState{}, fmt.Errorf("DB cluster %s no longer exists", o.clusterID)
	}
	secret := cluster.BackendMasterUserSecret
	if len(secret) == 0 {
		secret = cluster.MasterUserSecret
	}
	return rdsBackupState{
		identifier:    cluster.DBClusterIdentifier,
		resourceID:    cluster.DbClusterResourceId,
		window:        cluster.PreferredBackupWindow,
		retention:     cluster.BackupRetentionPeriod,
		available:     cluster.Status == "available",
		bases:         cluster.BaseBackups,
		backendSecret: secret,
	}, nil
}

func (o rdsClusterBackups) recordAutomatedSnapshot(snapshotID string, at time.Time) bool {
	if _, exists := rdsClusterSnapshots.Get(snapshotID); exists {
		return false
	}
	cluster, ok := rdsClusters.Get(o.clusterID)
	if !ok {
		return false
	}
	snapshot := rdsNewClusterSnapshot(cluster, snapshotID)
	snapshot.SnapshotType = "automated"
	snapshot.SnapshotCreateTime = at.Format(time.RFC3339)
	rdsClusterSnapshots.Put(snapshotID, snapshot)
	return true
}

func (o rdsClusterBackups) settleAutomatedSnapshot(snapshotID, status, reason string) {
	rdsSettleClusterSnapshot(snapshotID, status, reason)
}

func (o rdsClusterBackups) snapshotListed(snapshotID string) bool {
	_, ok := rdsClusterSnapshots.Get(snapshotID)
	return ok
}

func (o rdsClusterBackups) snapshotVolume(snapshotID string) string {
	return rdsClusterSnapshotVolume(snapshotID)
}

func (o rdsClusterBackups) expireAutomatedSnapshots(resourceID string, cutoff time.Time) {
	for _, snapshot := range rdsClusterSnapshots.List() {
		created, err := time.Parse(time.RFC3339, snapshot.SnapshotCreateTime)
		if snapshot.SnapshotType == "automated" && snapshot.DbClusterResourceId == resourceID &&
			snapshot.Status == "available" && err == nil && created.Before(cutoff) {
			rdsClusterSnapshots.Delete(snapshot.DBClusterSnapshotIdentifier)
		}
	}
}

func (o rdsClusterBackups) setBaseBackups(resourceID string, update func([]RDSBaseBackup) []RDSBaseBackup) {
	rdsClusters.Update(o.clusterID, func(stored *RDSCluster) {
		if stored.DbClusterResourceId == resourceID {
			stored.BaseBackups = update(stored.BaseBackups)
		}
	})
}

// ready reconciles the master password and the engine's accounts, and takes
// the cluster's first automated backup on the engine's first start.
func (plane *rdsAuroraDataPlane) ready() error {
	if err := plane.applyPendingMasterPassword(); err != nil {
		return err
	}
	if err := plane.prepareEngineAccounts(); err != nil {
		return fmt.Errorf("prepare the Amazon Aurora engine accounts: %w", err)
	}
	return plane.backups.takeFirst()
}

// rdsLastBinaryLogScript prints, base64-encoded, the newest binary log file
// of the volume at DATA.
const rdsLastBinaryLogScript = `set -e
last=$(tail -n 1 "$DATA/binlog.index")
last=${last##*/}
echo "binlog $last"
base64 -w 76 "$DATA/$last"
`

// rdsBaseBackupBinlogStart reads the newest binary log file of a base
// backup's volume and returns where the replay onto it starts.
func rdsBaseBackupBinlogStart(engine dbengine.Engine, volume, label string) (string, int, error) {
	listing, err := rdsRunVolumeHelper(engine, rdsLastBinaryLogScript, nil, rdsHelperSandbox,
		[]string{volume + ":" + engine.DataPath + ":ro"}, label)
	if err != nil {
		return "", 0, err
	}
	files, err := rdsReadBinaryLogListing(listing)
	if err != nil {
		return "", 0, err
	}
	last := files[len(files)-1]
	offset, err := rdsBinlogConsistentStart(last.data)
	if err != nil {
		return "", 0, fmt.Errorf("binary log file %s: %w", last.name, err)
	}
	return last.name, offset, nil
}

const (
	rdsBinlogQueryEvent = 2
	rdsBinlogXIDEvent   = 16
	// A query event's post-header is the thread ID, execution time, database
	// name length, error code and status variables length.
	rdsBinlogQueryPostHeaderLength = 13
)

// rdsBinlogConsistentStart is the offset in a captured binary log file of the
// first transaction the capture does not hold. MySQL commits a transaction in
// InnoDB only once the whole transaction is in the binary log, and crash
// recovery commits exactly the transactions the binary log holds whole, so the
// first transaction the file holds only in part, or the end of its last
// complete event, is where the captured data ends.
func rdsBinlogConsistentStart(data []byte) (int, error) {
	if !bytes.HasPrefix(data, []byte(rdsBinlogMagic)) {
		return 0, fmt.Errorf("the file does not start with the binary log magic number")
	}
	offset, open := len(rdsBinlogMagic), -1
	for offset+rdsBinlogEventHeaderLength <= len(data) {
		size := int(binary.LittleEndian.Uint32(data[offset+9:]))
		if size < rdsBinlogEventHeaderLength || offset+size > len(data) {
			break
		}
		switch data[offset+4] {
		case rdsBinlogGTIDEvent, rdsBinlogAnonymousGTID:
			open = offset
		case rdsBinlogXIDEvent:
			open = -1
		case rdsBinlogQueryEvent:
			if open >= 0 && !rdsBinlogQueryBegins(data[offset:offset+size]) {
				open = -1
			}
		}
		offset += size
	}
	if open >= 0 {
		return open, nil
	}
	return offset, nil
}

// rdsBinlogQueryBegins reports whether a query event is the BEGIN that opens a
// transaction, rather than a statement that completes one. The event may end
// in a 4-byte checksum.
func rdsBinlogQueryBegins(event []byte) bool {
	postHeader := rdsBinlogEventHeaderLength
	if len(event) < postHeader+rdsBinlogQueryPostHeaderLength {
		return false
	}
	databaseLength := int(event[postHeader+8])
	statusLength := int(binary.LittleEndian.Uint16(event[postHeader+11:]))
	start := postHeader + rdsBinlogQueryPostHeaderLength + statusLength + databaseLength + 1
	if start > len(event) {
		return false
	}
	query := event[start:]
	return bytes.HasPrefix(query, []byte("BEGIN")) && (len(query) == 5 || len(query) == 9)
}

// rdsNextBackupTime is the next start of a PreferredBackupWindow,
// hh24:mi-hh24:mi in UTC, after now.
func rdsNextBackupTime(window string, now time.Time) (time.Time, error) {
	start, _, found := strings.Cut(window, "-")
	hour, minute, ok := strings.Cut(start, ":")
	h, hourErr := strconv.Atoi(hour)
	m, minuteErr := strconv.Atoi(minute)
	if !found || !ok || hourErr != nil || minuteErr != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("PreferredBackupWindow %q is not hh24:mi-hh24:mi", window)
	}
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// rdsRemoveAutomatedBackups deletes a deleted cluster's automated snapshots
// and base backups.
func rdsRemoveAutomatedBackups(cluster RDSCluster) {
	volumes := map[string]bool{}
	for _, base := range cluster.BaseBackups {
		volumes[rdsClusterSnapshotVolume(base.SnapshotID)] = true
	}
	for _, snapshot := range rdsClusterSnapshots.List() {
		if snapshot.SnapshotType == "automated" && snapshot.DbClusterResourceId == cluster.DbClusterResourceId {
			rdsClusterSnapshots.Delete(snapshot.DBClusterSnapshotIdentifier)
			volumes[rdsClusterSnapshotVolume(snapshot.DBClusterSnapshotIdentifier)] = true
		}
	}
	for volume := range volumes {
		sim.RemoveVolumeSettled(volume, "rds")
	}
}

// rdsClusterRestorableWindow is the window a cluster restores to a time in,
// ending now.
func rdsClusterRestorableWindow(cluster RDSCluster) (earliest, latest time.Time, ok bool) {
	latest = time.Now().UTC()
	earliest, ok = rdsRestorableWindow(cluster.BaseBackups, cluster.BackupRetentionPeriod, latest)
	return earliest, latest, ok
}

func renderRDSRestorableWindow(cluster RDSCluster) string {
	earliest, latest, ok := rdsClusterRestorableWindow(cluster)
	if !ok {
		return ""
	}
	return fmt.Sprintf("<EarliestRestorableTime>%s</EarliestRestorableTime><LatestRestorableTime>%s</LatestRestorableTime>",
		earliest.Format(rdsRestorableTimeLayout), latest.Format(rdsRestorableTimeLayout))
}
