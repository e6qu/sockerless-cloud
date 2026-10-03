package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// Amazon Aurora backs every cluster volume up continuously, so
// RestoreDBClusterToPointInTime returns to any time in the backup retention
// period up to the cluster's LatestRestorableTime, and it takes a daily
// automated DB cluster snapshot in the cluster's PreferredBackupWindow.
//
// The simulator keeps that backup as base backups and the engine's own log.
// Each base backup is an automated DB cluster snapshot: the first is taken
// when the engine first accepts clients, before the endpoint relays any
// client to it, and another at the start of every PreferredBackupWindow while
// the engine runs. Aurora PostgreSQL's engine archives every completed
// write-ahead log segment into the cluster volume, and Aurora MySQL's engine
// keeps its binary log there. A restore to a time seeds the new cluster
// volume from the newest base backup taken by then and replays the source's
// log onto it up to RestoreToTime: PostgreSQL's archive recovery replays the
// write-ahead log when the new engine first starts, and MySQL's replication
// applier replays the binary log before the cluster becomes available.
//
// An automated snapshot expires once it is older than the cluster's
// BackupRetentionPeriod. Its volume stays as long as no newer base backup
// was taken by the earliest restorable time, and then goes with the log
// written before the oldest base backup that remains.

const rdsWALArchive = "sockerless_wal_archive"

// rdsAuroraEngine is the engine an Aurora cluster runs. PostgreSQL archives
// each completed write-ahead log segment into the cluster volume; the archive
// command runs in the data directory, and refuses to overwrite a segment it
// archived already. MySQL keeps its binary log until the backup retention
// period lets it go, rather than for its own 30-day expiry.
func rdsAuroraEngine(engineName string) dbengine.Engine {
	engine, _ := rdsEngine(engineName)
	switch engine.Family {
	case dbengine.Postgres:
		engine.Args = append(append([]string(nil), engine.Args...),
			"-c", "archive_mode=on",
			"-c", "archive_command=mkdir -p "+rdsWALArchive+" && test ! -f "+rdsWALArchive+"/%f && cp %p "+rdsWALArchive+"/%f")
	case dbengine.MySQL:
		engine.Args = append(append([]string(nil), engine.Args...), "--binlog-expire-logs-seconds=0")
	}
	return engine
}

// RDSClusterBaseBackup is a capture of a cluster volume that a restore to a
// later time starts from: the automated DB cluster snapshot whose volume holds
// it, when it was taken, and for Aurora MySQL the binary log file and offset
// of the first transaction the capture does not hold.
type RDSClusterBaseBackup struct {
	SnapshotID   string
	Time         string
	BinlogFile   string `json:",omitempty"`
	BinlogOffset int    `json:",omitempty"`
}

func (b RDSClusterBaseBackup) volume() string { return rdsClusterSnapshotVolume(b.SnapshotID) }

func (b RDSClusterBaseBackup) takenAt() time.Time {
	at, _ := time.Parse(rdsRestorableTimeLayout, b.Time)
	return at
}

// rdsAutomatedSnapshotID names the automated DB cluster snapshot of clusterID
// taken at, the way Aurora names them.
func rdsAutomatedSnapshotID(clusterID string, at time.Time) string {
	return "rds:" + clusterID + "-" + at.UTC().Format("2006-01-02-15-04")
}

// ready reconciles the master password and takes the cluster's first
// automated backup on the engine's first start.
func (plane *rdsAuroraDataPlane) ready() error {
	if err := plane.applyPendingMasterPassword(); err != nil {
		return err
	}
	cluster, err := plane.cluster()
	if err != nil {
		return err
	}
	if len(cluster.BaseBackups) > 0 {
		return nil
	}
	return plane.takeAutomatedBackup()
}

// takeAutomatedBackup captures the cluster volume as an automated DB cluster
// snapshot and records it as the cluster's newest base backup. The capture
// holds the engine frozen, so the volume it copies is what a crash at that
// instant would leave: the engine's recovery makes it consistent, and for
// MySQL the binary log in the copy says which transactions it holds.
func (plane *rdsAuroraDataPlane) takeAutomatedBackup() error {
	plane.captureMu.Lock()
	defer plane.captureMu.Unlock()
	cluster, err := plane.cluster()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshotID := rdsAutomatedSnapshotID(cluster.DBClusterIdentifier, now)
	if _, exists := rdsClusterSnapshots.Get(snapshotID); exists {
		return nil
	}
	snapshot := rdsNewClusterSnapshot(cluster, snapshotID)
	snapshot.SnapshotType = "automated"
	snapshot.SnapshotCreateTime = now.Format(time.RFC3339)
	rdsClusterSnapshots.Put(snapshotID, snapshot)
	base := RDSClusterBaseBackup{SnapshotID: snapshotID, Time: now.Format(rdsRestorableTimeLayout)}
	err = sim.CaptureVolume(context.Background(), rdsClusterVolume(plane.clusterID), base.volume(), "rds")
	if err == nil && plane.engine.Engine.Family == dbengine.MySQL {
		base.BinlogFile, base.BinlogOffset, err = rdsBaseBackupBinlogStart(plane.engine.Engine, base.volume(), plane.clusterID)
	}
	if err != nil {
		rdsSettleClusterSnapshot(snapshotID, "failed", err.Error())
		return fmt.Errorf("take the automated backup of the cluster volume: %w", err)
	}
	rdsSettleClusterSnapshot(snapshotID, "available", "")
	rdsClusters.Update(plane.clusterID, func(stored *RDSCluster) {
		if stored.DbClusterResourceId == cluster.DbClusterResourceId {
			stored.BaseBackups = append(stored.BaseBackups, base)
		}
	})
	return nil
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
func rdsBaseBackupBinlogStart(engine dbengine.Engine, volume, clusterID string) (string, int, error) {
	listing, err := rdsRunVolumeHelper(engine, rdsLastBinaryLogScript, nil, rdsHelperSandbox,
		[]string{volume + ":" + engine.DataPath + ":ro"}, clusterID)
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

// scheduleAutomatedBackups arms the cluster's next automated backup at the
// start of its PreferredBackupWindow.
func (plane *rdsAuroraDataPlane) scheduleAutomatedBackups() {
	cluster, err := plane.cluster()
	if err != nil {
		return
	}
	next, err := rdsNextBackupTime(cluster.PreferredBackupWindow, time.Now())
	if err != nil {
		log.Printf("Amazon Aurora %s: no automated backups: %v", plane.clusterID, err)
		return
	}
	plane.backupMu.Lock()
	defer plane.backupMu.Unlock()
	if plane.backupsStopped {
		return
	}
	if plane.backupTimer != nil {
		plane.backupTimer.Stop()
	}
	plane.backupTimer = time.AfterFunc(time.Until(next), func() {
		plane.runAutomatedBackup()
		plane.scheduleAutomatedBackups()
	})
}

func (plane *rdsAuroraDataPlane) stopAutomatedBackups() {
	plane.backupMu.Lock()
	defer plane.backupMu.Unlock()
	plane.backupsStopped = true
	if plane.backupTimer != nil {
		plane.backupTimer.Stop()
	}
}

// runAutomatedBackup takes the window's automated backup of an available
// cluster whose engine runs, and expires what the retention period no longer
// covers.
func (plane *rdsAuroraDataPlane) runAutomatedBackup() {
	cluster, err := plane.cluster()
	if err != nil {
		return
	}
	if cluster.Status == "available" && plane.engine.Running() {
		if err := plane.takeAutomatedBackup(); err != nil {
			log.Printf("Amazon Aurora %s: %v", plane.clusterID, err)
		}
	}
	if err := plane.expireAutomatedBackups(time.Now()); err != nil {
		log.Printf("Amazon Aurora %s: expire automated backups: %v", plane.clusterID, err)
	}
}

// rdsBaseBackupsToKeep is the base backups a restore to a time after cutoff
// can start from: the newest taken by cutoff, and every later one.
func rdsBaseBackupsToKeep(bases []RDSClusterBaseBackup, cutoff time.Time) []RDSClusterBaseBackup {
	first := 0
	for i, base := range bases {
		if !base.takenAt().After(cutoff) {
			first = i
		}
	}
	return bases[first:]
}

// expireAutomatedBackups deletes the cluster's automated snapshots older than
// its backup retention period, and the base backups and log no restore to a
// restorable time needs. A base backup a creating restore seeds from stays
// until a later run.
func (plane *rdsAuroraDataPlane) expireAutomatedBackups(now time.Time) error {
	plane.captureMu.Lock()
	defer plane.captureMu.Unlock()
	cluster, err := plane.cluster()
	if err != nil {
		return err
	}
	cutoff := now.UTC().AddDate(0, 0, -cluster.BackupRetentionPeriod)
	for _, snapshot := range rdsClusterSnapshots.List() {
		created, err := time.Parse(time.RFC3339, snapshot.SnapshotCreateTime)
		if snapshot.SnapshotType == "automated" && snapshot.DbClusterResourceId == cluster.DbClusterResourceId &&
			snapshot.Status == "available" && err == nil && created.Before(cutoff) {
			rdsClusterSnapshots.Delete(snapshot.DBClusterSnapshotIdentifier)
		}
	}
	keep := rdsBaseBackupsToKeep(cluster.BaseBackups, cutoff)
	expired := cluster.BaseBackups[:len(cluster.BaseBackups)-len(keep)]
	seeding := map[string]bool{}
	for _, other := range rdsClusters.List() {
		if other.Status == "creating" && other.RestoreSourceVolume != "" {
			seeding[other.RestoreSourceVolume] = true
		}
	}
	for i, base := range expired {
		if seeding[base.volume()] {
			keep = cluster.BaseBackups[i:]
			expired = expired[:i]
			break
		}
	}
	if len(expired) == 0 {
		return nil
	}
	rdsClusters.Update(plane.clusterID, func(stored *RDSCluster) {
		if stored.DbClusterResourceId == cluster.DbClusterResourceId {
			stored.BaseBackups = append([]RDSClusterBaseBackup(nil), keep...)
		}
	})
	for _, base := range expired {
		if _, listed := rdsClusterSnapshots.Get(base.SnapshotID); !listed {
			sim.RemoveVolumeSettled(base.volume(), "rds")
		}
	}
	return plane.pruneLogBefore(keep[0])
}

// pruneLogBefore removes the log the running engine wrote before base, which
// no restore replays any more: the binary log files before base's, or the
// archived write-ahead log segments before the one base's recovery starts
// from. A stopped engine's log waits for the next run.
func (plane *rdsAuroraDataPlane) pruneLogBefore(base RDSClusterBaseBackup) error {
	if !plane.engine.Running() {
		return nil
	}
	engine := plane.engine.Engine
	if engine.Family == dbengine.MySQL {
		cluster, err := plane.cluster()
		if err != nil {
			return err
		}
		password, err := rdsAuroraBackendPassword(cluster)
		if err != nil {
			return err
		}
		return plane.engine.Exec([]string{engine.Client, "--user=root", "--password=" + password,
			"--execute=PURGE BINARY LOGS TO " + dbengine.QuoteMySQLLiteral(base.BinlogFile)})
	}
	lines, err := rdsRunVolumeHelper(engine, `pg_controldata "$DATA" | sed -n "s/^Latest checkpoint's REDO WAL file: *//p"`,
		nil, rdsHelperSandbox, []string{base.volume() + ":" + engine.DataPath + ":ro"}, plane.clusterID)
	if err != nil {
		return err
	}
	redo := ""
	for _, line := range lines {
		if line = strings.TrimSpace(line); len(line) == 24 {
			redo = line
		}
	}
	if redo == "" {
		return fmt.Errorf("base backup %s reports no REDO WAL file", base.SnapshotID)
	}
	script := `cd "` + engine.DataPath + `/` + rdsWALArchive + `" 2>/dev/null || exit 0
ls | grep -E '^[0-9A-F]{24}$' | awk -v keep="` + redo + `" 'substr($0, 9) < substr(keep, 9)' | xargs -r rm -f`
	return plane.engine.Exec([]string{"sh", "-c", script})
}

// rdsRemoveAutomatedBackups deletes a deleted cluster's automated snapshots
// and base backups.
func rdsRemoveAutomatedBackups(cluster RDSCluster) {
	volumes := map[string]bool{}
	for _, base := range cluster.BaseBackups {
		volumes[base.volume()] = true
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

// rdsRestorableWindow is the window a cluster restores to a time in: from its
// oldest base backup, or the start of its backup retention period when that
// is later, to now; ok is false until the first base backup exists.
func rdsRestorableWindow(cluster RDSCluster) (earliest, latest time.Time, ok bool) {
	if len(cluster.BaseBackups) == 0 {
		return time.Time{}, time.Time{}, false
	}
	earliest = cluster.BaseBackups[0].takenAt()
	latest = time.Now().UTC()
	if cutoff := latest.AddDate(0, 0, -cluster.BackupRetentionPeriod).Truncate(time.Millisecond); cutoff.After(earliest) {
		earliest = cutoff
	}
	return earliest, latest, true
}

// rdsBaseBackupFor is the newest base backup of cluster taken by target.
func rdsBaseBackupFor(cluster RDSCluster, target time.Time) (RDSClusterBaseBackup, bool) {
	var found RDSClusterBaseBackup
	ok := false
	for _, base := range cluster.BaseBackups {
		if !base.takenAt().After(target) {
			found, ok = base, true
		}
	}
	return found, ok
}

func renderRDSRestorableWindow(cluster RDSCluster) string {
	earliest, latest, ok := rdsRestorableWindow(cluster)
	if !ok {
		return ""
	}
	return fmt.Sprintf("<EarliestRestorableTime>%s</EarliestRestorableTime><LatestRestorableTime>%s</LatestRestorableTime>",
		earliest.Format(rdsRestorableTimeLayout), latest.Format(rdsRestorableTimeLayout))
}
