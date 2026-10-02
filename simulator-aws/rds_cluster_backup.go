package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// Amazon Aurora backs every cluster volume up continuously, so
// RestoreDBClusterToPointInTime returns to any time between the cluster's
// EarliestRestorableTime and its LatestRestorableTime.
//
// The simulator keeps that backup as a base backup and the engine's own log.
// The base backup captures the cluster volume once, when the engine first
// accepts clients and before the endpoint relays any client to it, so it holds
// what the volume held at every time between the cluster's creation and the
// capture. After it, Aurora PostgreSQL's engine archives every completed
// write-ahead log segment into the cluster volume, and Aurora MySQL's engine
// writes its binary log there, starting a new binary log file for the capture.
// A restore to a time seeds the new cluster volume from the source's base
// backup and replays the source's log onto it up to RestoreToTime:
// PostgreSQL's archive recovery replays the write-ahead log when the new
// engine first starts, and MySQL's replication applier replays the binary log
// before the cluster becomes available.

const rdsWALArchive = "sockerless_wal_archive"

func rdsClusterBaseBackupVolume(clusterID string) string {
	return rdsVolume("cluster-base", clusterID)
}

// rdsRestorableTimeLayout renders restorable times to the millisecond, the
// precision the RDS API reports them in.
const rdsRestorableTimeLayout = "2006-01-02T15:04:05.000Z"

// rdsAuroraEngine is the engine an Aurora cluster runs. PostgreSQL archives
// each completed write-ahead log segment into the cluster volume; the archive
// command runs in the data directory, and refuses to overwrite a segment it
// archived already.
func rdsAuroraEngine(engineName string) dbengine.Engine {
	engine, _ := rdsEngine(engineName)
	if engine.Family == dbengine.Postgres {
		engine.Args = append(append([]string(nil), engine.Args...),
			"-c", "archive_mode=on",
			"-c", "archive_command=mkdir -p "+rdsWALArchive+" && test ! -f "+rdsWALArchive+"/%f && cp %p "+rdsWALArchive+"/%f")
	}
	return engine
}

// ready reconciles the master password and takes the cluster's base backup on
// the engine's first start.
func (plane *rdsAuroraDataPlane) ready() error {
	if err := plane.applyPendingMasterPassword(); err != nil {
		return err
	}
	return plane.captureBaseBackup()
}

func (plane *rdsAuroraDataPlane) captureBaseBackup() error {
	cluster, err := plane.cluster()
	if err != nil {
		return err
	}
	if cluster.BaseBackupTime != "" {
		return nil
	}
	if plane.engine.Engine.Family == dbengine.MySQL {
		password, err := rdsAuroraBackendPassword(cluster)
		if err != nil {
			return err
		}
		if err := plane.engine.Exec([]string{plane.engine.Engine.Client, "--user=root", "--password=" + password, "--execute=FLUSH BINARY LOGS"}); err != nil {
			return fmt.Errorf("start the binary log file that follows the base backup: %w", err)
		}
	}
	capturedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := sim.CaptureVolume(context.Background(), rdsClusterVolume(plane.clusterID), rdsClusterBaseBackupVolume(plane.clusterID), "rds"); err != nil {
		return fmt.Errorf("capture the base backup of the cluster volume: %w", err)
	}
	rdsClusters.Update(plane.clusterID, func(stored *RDSCluster) {
		if stored.DbClusterResourceId == cluster.DbClusterResourceId {
			stored.BaseBackupTime = capturedAt.Format(rdsRestorableTimeLayout)
		}
	})
	return nil
}

// rdsRestorableWindow is the window a cluster restores to a time in, from its
// base backup to now; ok is false until the base backup exists.
func rdsRestorableWindow(cluster RDSCluster) (earliest, latest time.Time, ok bool) {
	if cluster.BaseBackupTime == "" {
		return time.Time{}, time.Time{}, false
	}
	earliest, err := time.Parse(rdsRestorableTimeLayout, cluster.BaseBackupTime)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return earliest, time.Now().UTC(), true
}

func renderRDSRestorableWindow(cluster RDSCluster) string {
	earliest, latest, ok := rdsRestorableWindow(cluster)
	if !ok {
		return ""
	}
	return fmt.Sprintf("<EarliestRestorableTime>%s</EarliestRestorableTime><LatestRestorableTime>%s</LatestRestorableTime>",
		earliest.Format(rdsRestorableTimeLayout), latest.Format(rdsRestorableTimeLayout))
}

// rdsHelperSandbox confines the one-shot containers that assemble a restore's
// log: they copy files as root and keep their owners.
var rdsHelperSandbox = sim.SandboxProfile{
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

type rdsHelperOutput struct {
	mu    sync.Mutex
	lines []string
}

func (o *rdsHelperOutput) WriteLog(line sim.LogLine) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lines = append(o.lines, line.Text)
}

func (o *rdsHelperOutput) Lines() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.lines...)
}

// rdsRunVolumeHelper runs script under /bin/sh in the engine's image with the
// given volume binds and returns its output lines. DATA names the engine's
// data directory, where the image declares a volume: a bind there keeps the
// engine from creating an anonymous volume for the helper.
func rdsRunVolumeHelper(engine dbengine.Engine, script string, env map[string]string, sandbox sim.SandboxProfile, binds []string, label string) ([]string, error) {
	environment := map[string]string{"DATA": engine.DataPath}
	for name, value := range env {
		environment[name] = value
	}
	output := &rdsHelperOutput{}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        engine.Image,
		Architecture: "linux/amd64",
		Command:      []string{"/bin/sh"},
		Args:         []string{"-c", script},
		Env:          environment,
		Timeout:      10 * time.Minute,
		Binds:        binds,
		Labels:       map[string]string{"sockerless-rds-restore": label},
		Sandbox:      sandbox,
	}, output)
	if err != nil {
		return nil, fmt.Errorf("start the restore helper: %w", err)
	}
	result := handle.Wait()
	lines := output.Lines()
	if result.ExitCode != 0 || result.Error != nil {
		return nil, fmt.Errorf("restore helper exited %d (%v): %s", result.ExitCode, result.Error, strings.Join(lines, "\n"))
	}
	return lines, nil
}

// rdsAssembleWALScript copies the source's archived write-ahead log and the
// segments still in its pg_wal into the new volume's archive, passing over a
// segment the engine removes meanwhile, which it archived already, then lists
// the commit and abort records of the newest timeline. pg_waldump reads only up to
// the last segment it names, and reports the end of the log as an error in the
// record it cannot read; any other error fails the restore.
const rdsAssembleWALScript = `set -e
archive="$DATA/` + rdsWALArchive + `"
mkdir -p "$archive"
for f in /source/` + rdsWALArchive + `/* /source/pg_wal/*; do
	[ -f "$f" ] || continue
	name=${f##*/}
	case "$name" in
	*.history) ;;
	*) echo "$name" | grep -Eq '^[0-9A-F]{24}$' || continue ;;
	esac
	[ -e "$archive/$name" ] || cp -p "$f" "$archive/$name" 2>/dev/null || [ ! -e "$f" ] || exit 1
done
chown -R "$(stat -c %u:%g "$DATA/PG_VERSION")" "$archive"
cd "$archive"
timeline=$(ls | grep -E '^[0-9A-F]{24}$' | cut -c1-8 | sort | tail -n 1)
[ -n "$timeline" ] || exit 0
segments=$(ls | grep -E "^${timeline}[0-9A-F]{16}$" | sort)
pg_waldump --path=. --rmgr=Transaction "$(echo "$segments" | head -n 1)" "$(echo "$segments" | tail -n 1)" 2>&1 || true
`

// rdsConfigureRecoveryScript has the engine's next start run archive
// recovery from the assembled archive, to RECOVERY_TARGET_TIME when one is
// set and to the end of the log otherwise, and then promote.
const rdsConfigureRecoveryScript = `set -e
cd "$DATA"
owner=$(stat -c %u:%g PG_VERSION)
touch postgresql.auto.conf
grep -Ev '^[[:space:]]*(restore_command|recovery_target)' postgresql.auto.conf > /tmp/auto.conf || true
{
	cat /tmp/auto.conf
	echo "restore_command = 'cp ` + rdsWALArchive + `/%f %p'"
	echo "recovery_target_action = 'promote'"
	if [ -n "$RECOVERY_TARGET_TIME" ]; then
		echo "recovery_target_time = '$RECOVERY_TARGET_TIME'"
	fi
} > postgresql.auto.conf
: > recovery.signal
chown "$owner" postgresql.auto.conf recovery.signal
`

var rdsWALTransactionRecord = regexp.MustCompile(`desc: (?:COMMIT|ABORT) (\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(?:\.\d+)?) UTC`)

// rdsWALHasTransactionAfter reports whether the pg_waldump listing holds a
// commit or abort record later than target. Archive recovery fails when the
// log ends before it reaches its target time, so a target no record passes
// replays the whole log instead, which holds the same data.
func rdsWALHasTransactionAfter(listing []string, target time.Time) (bool, error) {
	later := false
	for _, line := range listing {
		if strings.HasPrefix(line, "pg_waldump: error:") && !strings.Contains(line, "error in WAL record") {
			return false, errors.New(strings.TrimPrefix(line, "pg_waldump: error: "))
		}
		match := rdsWALTransactionRecord.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		committed, err := time.Parse("2006-01-02 15:04:05.999999999", match[1])
		if err != nil {
			return false, fmt.Errorf("read the time of WAL record %q: %w", line, err)
		}
		if committed.After(target) {
			later = true
		}
	}
	return later, nil
}

// rdsReplayLogToRestoreTime replays the source cluster's log onto the new
// cluster volume, which holds the source's base backup, up to the restore
// time. The helpers read the log from the source cluster volume while its
// engine runs: every transaction that ended by the restore time is in the log
// already, and a record the engine is still writing ends after it.
func rdsReplayLogToRestoreTime(cluster RDSCluster) error {
	target, err := time.Parse(time.RFC3339Nano, cluster.RestoreToTime)
	if err != nil {
		return fmt.Errorf("read RestoreToTime %q: %w", cluster.RestoreToTime, err)
	}
	if !sim.VolumeExists(cluster.RestoreLogVolume) {
		return fmt.Errorf("the source cluster volume %s no longer exists", cluster.RestoreLogVolume)
	}
	engine := rdsAuroraEngine(cluster.Engine)
	if engine.Family == dbengine.MySQL {
		return rdsReplayBinaryLog(engine, cluster, cluster.RestoreLogVolume, target)
	}
	return rdsPrepareArchiveRecovery(engine, cluster.DBClusterIdentifier, cluster.RestoreLogVolume, target)
}

// rdsPrepareArchiveRecovery adds the write-ahead log of sourceVolume to the
// new cluster volume and configures the archive recovery its engine runs to
// the restore time when it first starts.
func rdsPrepareArchiveRecovery(engine dbengine.Engine, clusterID, sourceVolume string, target time.Time) error {
	clusterVolume := rdsClusterVolume(clusterID)
	listing, err := rdsRunVolumeHelper(engine, rdsAssembleWALScript, map[string]string{"TZ": "UTC"}, rdsHelperSandbox,
		[]string{sourceVolume + ":/source:ro", clusterVolume + ":" + engine.DataPath}, clusterID)
	if err != nil {
		return err
	}
	later, err := rdsWALHasTransactionAfter(listing, target)
	if err != nil {
		return fmt.Errorf("read the source write-ahead log: %w", err)
	}
	recoveryTarget := ""
	if later {
		recoveryTarget = target.UTC().Format("2006-01-02 15:04:05.999999") + "+00"
	}
	_, err = rdsRunVolumeHelper(engine, rdsConfigureRecoveryScript, map[string]string{"RECOVERY_TARGET_TIME": recoveryTarget}, rdsHelperSandbox,
		[]string{clusterVolume + ":" + engine.DataPath}, clusterID)
	return err
}
