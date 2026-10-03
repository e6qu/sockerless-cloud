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

// rdsRestorableTimeLayout renders restorable times to the millisecond, the
// precision the RDS API reports them in.
const rdsRestorableTimeLayout = "2006-01-02T15:04:05.000Z"

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
	handle, err := sim.StartContainerSyncContext(context.Background(), sim.ContainerConfig{
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
// set and to the end of the log otherwise, and then promote. hot_standby off
// keeps the engine refusing clients with 57P03 until it has promoted, so the
// restored cluster is available only once it accepts writes.
const rdsConfigureRecoveryScript = `set -e
cd "$DATA"
owner=$(stat -c %u:%g PG_VERSION)
touch postgresql.auto.conf
grep -Ev '^[[:space:]]*(restore_command|recovery_target|hot_standby)' postgresql.auto.conf > /tmp/auto.conf || true
{
	cat /tmp/auto.conf
	echo "restore_command = 'cp ` + rdsWALArchive + `/%f %p'"
	echo "recovery_target_action = 'promote'"
	echo "hot_standby = off"
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
