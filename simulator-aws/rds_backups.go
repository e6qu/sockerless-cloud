package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// Amazon RDS backs an Aurora cluster or a DB instance up continuously, so a
// restore to a time returns to any time in the backup retention period up to
// the resource's latest restorable time, and it takes a daily automated
// snapshot in the resource's PreferredBackupWindow.
//
// The simulator keeps that backup as base backups and the engine's own log.
// Each base backup is an automated snapshot: the first is taken when the
// engine first accepts clients, before the endpoint relays any client to it,
// and another at the start of every PreferredBackupWindow, which starts an
// engine no client has started. An Aurora cluster or a DB instance starts its
// engine for the first one as soon as it keeps automated backups, the way
// Amazon RDS backs a new resource up before any client connects. A PostgreSQL
// engine archives every completed write-ahead log segment into its volume,
// and a MySQL engine keeps its binary log there. A restore
// to a time seeds the new volume from the newest base backup taken by then
// and replays the source's log onto it up to the restore time: PostgreSQL's
// archive recovery replays the write-ahead log when the new engine first
// starts, and MySQL's replication applier replays the binary log before the
// restored resource becomes available.
//
// An automated snapshot expires once it is older than the resource's
// BackupRetentionPeriod. Its volume stays as long as no newer base backup was
// taken by the earliest restorable time, and then goes with the log written
// before the oldest base backup that remains.

// RDSBaseBackup is a capture of a volume that a restore to a later time
// starts from: the automated snapshot whose volume holds it, when it was
// taken, and for MySQL the binary log file and offset of the first
// transaction the capture does not hold.
type RDSBaseBackup struct {
	SnapshotID   string
	Time         string
	BinlogFile   string `json:",omitempty"`
	BinlogOffset int    `json:",omitempty"`
}

func (b RDSBaseBackup) takenAt() time.Time {
	at, _ := time.Parse(rdsRestorableTimeLayout, b.Time)
	return at
}

// rdsBackupOwner is the resource whose automated backups rdsAutomatedBackups
// keeps: an Aurora cluster or a DB instance.
type rdsBackupOwner interface {
	backupState() (rdsBackupState, error)
	// recordAutomatedSnapshot records a creating automated snapshot of the
	// owner, and reports false when one of that name exists already.
	recordAutomatedSnapshot(snapshotID string, at time.Time) bool
	settleAutomatedSnapshot(snapshotID, status, reason string)
	snapshotListed(snapshotID string) bool
	snapshotVolume(snapshotID string) string
	// expireAutomatedSnapshots deletes the owner's available automated
	// snapshots taken before cutoff.
	expireAutomatedSnapshots(resourceID string, cutoff time.Time)
	setBaseBackups(resourceID string, update func([]RDSBaseBackup) []RDSBaseBackup)
}

// rdsBackupState is what an automated backup run reads of its owner.
type rdsBackupState struct {
	identifier string
	resourceID string
	window     string
	retention  int
	available  bool
	bases      []RDSBaseBackup
	// backendSecret seals the master password the engine holds.
	backendSecret []byte
}

// rdsAutomatedBackups takes, schedules and expires one resource's automated
// backups of the volume its engine runs on.
type rdsAutomatedBackups struct {
	owner  rdsBackupOwner
	engine *dbengine.Instance
	volume string

	// captureMu serialises taking and expiring automated backups.
	captureMu      sync.Mutex
	backupMu       sync.Mutex
	backupTimer    *time.Timer
	backupsStopped bool
	// startMu is held across an engine start for a backup, so awaitStart can
	// wait for one to give up rather than leave an engine running on a volume
	// its resource let go of.
	startMu sync.Mutex
}

// rdsKeepsLog reports whether engine keeps the log a restore to a time
// replays: PostgreSQL's archived write-ahead log or MySQL's binary log.
func rdsKeepsLog(engine dbengine.Engine) bool {
	return engine.Client == dbengine.Postgres16.Client || engine.Client == dbengine.MySQL80.Client
}

// rdsLoggingEngine is the engine an Aurora cluster or a DB instance runs,
// keeping its log for restores to a time. PostgreSQL archives each completed
// write-ahead log segment into the volume; the archive command runs in the
// data directory, and refuses to overwrite a segment it archived already.
// MySQL keeps its binary log until the backup retention period lets it go,
// rather than for its own 30-day expiry.
func rdsLoggingEngine(engineName string) (dbengine.Engine, bool) {
	engine, ok := rdsEngine(engineName)
	switch engine.Client {
	case dbengine.Postgres16.Client:
		engine.Args = append(append([]string(nil), engine.Args...),
			"-c", "archive_mode=on",
			"-c", "archive_command=mkdir -p "+rdsWALArchive+" && test ! -f "+rdsWALArchive+"/%f && cp %p "+rdsWALArchive+"/%f")
	case dbengine.MySQL80.Client:
		engine.Args = append(append([]string(nil), engine.Args...), "--binlog-expire-logs-seconds=0")
	}
	return engine, ok
}

// rdsAutomatedSnapshotID names the automated snapshot of identifier taken
// at, the way Amazon RDS names them.
func rdsAutomatedSnapshotID(identifier string, at time.Time) string {
	return "rds:" + identifier + "-" + at.UTC().Format("2006-01-02-15-04")
}

// takeFirst takes the resource's first automated backup when it keeps
// automated backups and holds none yet.
func (b *rdsAutomatedBackups) takeFirst() error {
	return b.takeIf(func(state rdsBackupState) bool {
		return state.retention > 0 && len(state.bases) == 0 && rdsKeepsLog(b.engine.Engine)
	})
}

// takeFirstStarting starts the engine when no client has, which takes the
// first automated backup, and otherwise takes it on the running engine.
func (b *rdsAutomatedBackups) takeFirstStarting() error {
	b.startMu.Lock()
	defer b.startMu.Unlock()
	if err := b.ensureEngine(); err != nil {
		return fmt.Errorf("start the engine for the first automated backup: %w", err)
	}
	return b.takeFirst()
}

// ensureEngine brings the engine up for a backup unless the backups have
// stopped, and stops an engine whose start the stop raced, which would
// otherwise outlive the data plane that closed it.
func (b *rdsAutomatedBackups) ensureEngine() error {
	if b.stopped() {
		return errRDSBackupsStopped
	}
	err := b.engine.Ensure()
	if b.stopped() {
		_ = b.engine.Stop()
		return errRDSBackupsStopped
	}
	return err
}

var errRDSBackupsStopped = errors.New("the resource's data plane stopped")

// takeIf takes an automated backup when wanted holds for the resource's
// backup state as the capture begins.
func (b *rdsAutomatedBackups) takeIf(wanted func(rdsBackupState) bool) error {
	b.captureMu.Lock()
	defer b.captureMu.Unlock()
	state, err := b.owner.backupState()
	if err != nil {
		return err
	}
	if !wanted(state) {
		return nil
	}
	return b.capture(state)
}

// capture captures the volume as an automated snapshot and records it as the
// resource's newest base backup. The capture holds the engine frozen, so the
// volume it copies is what a crash at that instant would leave: the engine's
// recovery makes it consistent, and for MySQL the binary log in the copy says
// which transactions it holds.
func (b *rdsAutomatedBackups) capture(state rdsBackupState) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshotID := rdsAutomatedSnapshotID(state.identifier, now)
	if !b.owner.recordAutomatedSnapshot(snapshotID, now) {
		return nil
	}
	base := RDSBaseBackup{SnapshotID: snapshotID, Time: now.Format(rdsRestorableTimeLayout)}
	volume := b.owner.snapshotVolume(snapshotID)
	err := sim.CaptureVolume(context.Background(), b.volume, volume, "rds")
	if err == nil && b.engine.Engine.Family == dbengine.MySQL {
		base.BinlogFile, base.BinlogOffset, err = rdsBaseBackupBinlogStart(b.engine.Engine, volume, state.identifier)
	}
	if err != nil {
		b.owner.settleAutomatedSnapshot(snapshotID, "failed", err.Error())
		return fmt.Errorf("take the automated backup of the volume: %w", err)
	}
	b.owner.settleAutomatedSnapshot(snapshotID, "available", "")
	b.owner.setBaseBackups(state.resourceID, func(bases []RDSBaseBackup) []RDSBaseBackup {
		return append(bases, base)
	})
	return nil
}

// schedule arms the resource's next automated backup at the start of its
// PreferredBackupWindow.
func (b *rdsAutomatedBackups) schedule() {
	state, err := b.owner.backupState()
	if err != nil {
		return
	}
	next, err := rdsNextBackupTime(state.window, time.Now())
	if err != nil {
		log.Printf("Amazon RDS %s: no automated backups: %v", state.identifier, err)
		return
	}
	b.backupMu.Lock()
	defer b.backupMu.Unlock()
	if b.backupsStopped {
		return
	}
	if b.backupTimer != nil {
		b.backupTimer.Stop()
	}
	b.backupTimer = time.AfterFunc(time.Until(next), func() {
		b.run()
		b.schedule()
	})
}

// stop disarms the resource's automated backups. The caller closes the
// engine next, which ends an engine start a backup began, and then waits for
// that start with awaitStart.
func (b *rdsAutomatedBackups) stop() {
	b.backupMu.Lock()
	defer b.backupMu.Unlock()
	b.backupsStopped = true
	if b.backupTimer != nil {
		b.backupTimer.Stop()
	}
}

func (b *rdsAutomatedBackups) awaitStart() {
	b.startMu.Lock()
	defer b.startMu.Unlock()
}

// holdStart waits for an engine start a backup began, and keeps another from
// beginning until release.
func (b *rdsAutomatedBackups) holdStart() (release func()) {
	b.startMu.Lock()
	return b.startMu.Unlock
}

func (b *rdsAutomatedBackups) stopped() bool {
	b.backupMu.Lock()
	defer b.backupMu.Unlock()
	return b.backupsStopped
}

// run takes the window's automated backup of an available resource, starting
// its engine when no client has, and expires what the retention period no
// longer covers. An engine start that took the first automated backup holds
// the window's.
func (b *rdsAutomatedBackups) run() {
	state, err := b.owner.backupState()
	if err != nil {
		return
	}
	if state.available && state.retention > 0 && rdsKeepsLog(b.engine.Engine) {
		if err := b.takeWindowBackup(time.Now().UTC().Truncate(time.Millisecond)); err != nil && !errors.Is(err, errRDSBackupsStopped) {
			log.Printf("Amazon RDS %s: %v", state.identifier, err)
		}
	}
	if err := b.expire(time.Now()); err != nil {
		log.Printf("Amazon RDS %s: expire automated backups: %v", state.identifier, err)
	}
}

func (b *rdsAutomatedBackups) takeWindowBackup(began time.Time) error {
	b.startMu.Lock()
	defer b.startMu.Unlock()
	if err := b.ensureEngine(); err != nil {
		return fmt.Errorf("start the engine for the automated backup: %w", err)
	}
	return b.takeIf(func(state rdsBackupState) bool {
		return len(state.bases) == 0 || state.bases[len(state.bases)-1].takenAt().Before(began)
	})
}

// rdsBaseBackupsToKeep is the base backups a restore to a time after cutoff
// can start from: the newest taken by cutoff, and every later one.
func rdsBaseBackupsToKeep(bases []RDSBaseBackup, cutoff time.Time) []RDSBaseBackup {
	first := 0
	for i, base := range bases {
		if !base.takenAt().After(cutoff) {
			first = i
		}
	}
	return bases[first:]
}

// rdsSeedingVolumes are the volumes a creating restore seeds from.
func rdsSeedingVolumes() map[string]bool {
	seeding := map[string]bool{}
	for _, cluster := range rdsClusters.List() {
		if cluster.Status == "creating" && cluster.RestoreSourceVolume != "" {
			seeding[cluster.RestoreSourceVolume] = true
		}
	}
	for _, instance := range rdsInstances.List() {
		if instance.DBInstanceStatus == "creating" && instance.RestoreSourceVolume != "" {
			seeding[instance.RestoreSourceVolume] = true
		}
	}
	return seeding
}

// expire deletes the resource's automated snapshots older than its backup
// retention period, and the base backups and log no restore to a restorable
// time needs. A retention period of 0 turns automated backups off, and every
// base backup goes. A base backup a creating restore seeds from stays until a
// later run.
func (b *rdsAutomatedBackups) expire(now time.Time) error {
	b.captureMu.Lock()
	defer b.captureMu.Unlock()
	state, err := b.owner.backupState()
	if err != nil {
		return err
	}
	cutoff := now.UTC().AddDate(0, 0, -state.retention)
	b.owner.expireAutomatedSnapshots(state.resourceID, cutoff)
	var keep []RDSBaseBackup
	if state.retention > 0 {
		keep = rdsBaseBackupsToKeep(state.bases, cutoff)
	}
	expired := state.bases[:len(state.bases)-len(keep)]
	seeding := rdsSeedingVolumes()
	for i, base := range expired {
		if seeding[b.owner.snapshotVolume(base.SnapshotID)] {
			keep = state.bases[i:]
			expired = expired[:i]
			break
		}
	}
	if len(expired) == 0 {
		return nil
	}
	b.owner.setBaseBackups(state.resourceID, func([]RDSBaseBackup) []RDSBaseBackup {
		return append([]RDSBaseBackup(nil), keep...)
	})
	for _, base := range expired {
		if !b.owner.snapshotListed(base.SnapshotID) {
			sim.RemoveVolumeSettled(b.owner.snapshotVolume(base.SnapshotID), "rds")
		}
	}
	if len(keep) == 0 {
		return nil
	}
	return b.pruneLogBefore(state, keep[0])
}

// pruneLogBefore removes the log the running engine wrote before base, which
// no restore replays any more: the binary log files before base's, or the
// archived write-ahead log segments before the one base's recovery starts
// from. A stopped engine's log waits for the next run.
func (b *rdsAutomatedBackups) pruneLogBefore(state rdsBackupState, base RDSBaseBackup) error {
	if !b.engine.Running() {
		return nil
	}
	engine := b.engine.Engine
	if engine.Family == dbengine.MySQL {
		_, password, ok := kmsDecryptBytes(state.backendSecret)
		if !ok {
			return fmt.Errorf("decrypt the master-user credential")
		}
		return b.engine.Exec([]string{engine.Client, "--user=root", "--password=" + string(password),
			"--execute=PURGE BINARY LOGS TO " + dbengine.QuoteMySQLLiteral(base.BinlogFile)})
	}
	lines, err := rdsRunVolumeHelper(engine, `pg_controldata "$DATA" | sed -n "s/^Latest checkpoint's REDO WAL file: *//p"`,
		nil, rdsHelperSandbox, []string{b.owner.snapshotVolume(base.SnapshotID) + ":" + engine.DataPath + ":ro"}, state.identifier)
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
	return b.engine.Exec([]string{"sh", "-c", script})
}

// rdsRestorableWindow is the window a resource with bases and retention
// restores to a time in, ending at latest: from its oldest base backup, or
// the start of the backup retention period before latest when that is later;
// ok is false until the first base backup exists.
func rdsRestorableWindow(bases []RDSBaseBackup, retention int, latest time.Time) (earliest time.Time, ok bool) {
	if len(bases) == 0 {
		return time.Time{}, false
	}
	earliest = bases[0].takenAt()
	if cutoff := latest.AddDate(0, 0, -retention).Truncate(time.Millisecond); cutoff.After(earliest) {
		earliest = cutoff
	}
	return earliest, true
}

// rdsBaseBackupFor is the newest of bases taken by target.
func rdsBaseBackupFor(bases []RDSBaseBackup, target time.Time) (RDSBaseBackup, bool) {
	var found RDSBaseBackup
	ok := false
	for _, base := range bases {
		if !base.takenAt().After(target) {
			found, ok = base, true
		}
	}
	return found, ok
}
