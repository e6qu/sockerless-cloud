package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
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

// rdsTakeFirstClusterBackup starts the engine of an available Aurora cluster
// that keeps automated backups and holds none yet, in the background, so the
// start takes its first automated backup: Aurora backs a cluster up when it
// creates or restores it, or turns its automated backups on, whether or not a
// client has connected.
func rdsTakeFirstClusterBackup(clusterID string) {
	cluster, ok := rdsClusters.Get(clusterID)
	plane, served := rdsLoadAuroraDataPlane(clusterID)
	if !ok || !served || cluster.Status != "available" || cluster.BackupRetentionPeriod == 0 ||
		len(cluster.BaseBackups) > 0 || !rdsKeepsLog(plane.engine.Engine) {
		return
	}
	bg.Go(func() {
		if err := plane.backups.takeFirstStarting(); err != nil && !errors.Is(err, errRDSBackupsStopped) {
			log.Printf("Amazon Aurora %s: %v", clusterID, err)
		}
	})
}

// rdsRecoverBaseBackupScript prints, base64-encoded, the newest binary log
// file of the crash image at DATA, then runs the engine's own crash recovery on
// it and shuts the engine down cleanly. SERVER_COMMAND, one argument a line,
// is rdsBaseBackupRecoveryServer's command; ADMIN pings the server, and
// reports it alive once it accepts connections even when it refuses the
// unauthenticated ping. The file is printed first because the start rotates
// the binary log.
const rdsRecoverBaseBackupScript = `set -e
last=$(tail -n 1 "$DATA/binlog.index")
last=${last##*/}
echo "binlog $last"
base64 < "$DATA/$last"
set --
while IFS= read -r arg; do
	[ -z "$arg" ] || set -- "$@" "$arg"
done <<COMMAND
$SERVER_COMMAND
COMMAND
log=$(mktemp)
"$@" > "$log" 2>&1 &
server=$!
until "$ADMIN" --socket=` + rdsRecoverySocket + ` ping >/dev/null 2>&1; do
	if ! kill -0 "$server" 2>/dev/null; then
		echo "the engine stopped during crash recovery:"
		cat "$log"
		exit 1
	fi
	sleep 0.1
done
kill -TERM "$server"
if ! wait "$server"; then
	echo "the engine did not shut down cleanly after crash recovery:"
	cat "$log"
	exit 1
fi
`

// rdsRecoverySocket is the socket the recovering server listens on, in place
// of any network.
const rdsRecoverySocket = "/tmp/recover.sock"

// rdsBaseBackupRecoveryServer is the command that runs engine's crash recovery
// on a base backup, and the admin command that pings it. The server runs with
// the arguments the engine runs with, its binary log among them: that recovery
// commits each transaction InnoDB holds prepared whose XID the binary log
// holds, and rolls back the rest, which is the cut rdsBinlogConsistentStart
// reads. A server started without the binary log, as the replay's is, rolls
// back every prepared transaction, so a crash image it opened would lose the
// transactions committed as the capture froze the engine.
func rdsBaseBackupRecoveryServer(engine dbengine.Engine) (server []string, admin string, err error) {
	binary, admin := "mysqld", "mysqladmin"
	if engine.Client == dbengine.MariaDB114.Client {
		binary, admin = "mariadbd", "mariadb-admin"
	}
	server = []string{binary, "--user=mysql", "--skip-networking", "--socket=" + rdsRecoverySocket}
	for _, arg := range engine.Args {
		if arg == "" || strings.ContainsAny(arg, "\n\r") {
			return nil, "", fmt.Errorf("the engine argument %q cannot be passed one to a line", arg)
		}
		server = append(server, arg)
	}
	return server, admin, nil
}

// rdsRecoverBaseBackup recovers a base backup's volume, a crash image of the
// engine, the way the engine recovers it with its binary log, and returns the
// binary log file and offset where the replay onto it starts. The volume then
// holds a clean shutdown, so every later start of it, with or without the
// binary log, opens the same transactions the offset assumes.
func rdsRecoverBaseBackup(engine dbengine.Engine, volume, label string) (string, int, error) {
	server, admin, err := rdsBaseBackupRecoveryServer(engine)
	if err != nil {
		return "", 0, err
	}
	env := map[string]string{"SERVER_COMMAND": strings.Join(server, "\n"), "ADMIN": admin}
	listing, err := rdsRunVolumeHelper(engine, rdsRecoverBaseBackupScript, env, rdsReplaySandbox,
		[]string{volume + ":" + engine.DataPath}, label)
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
// first transaction the capture does not hold once rdsRecoverBaseBackup has
// recovered it. MySQL and MariaDB commit a transaction in InnoDB only once
// the whole transaction is in the binary log, and crash recovery with the
// binary log commits exactly the transactions the binary log holds whole, so
// the first transaction the file holds only in part, or the end of its last
// complete event, is where the captured data ends.
func rdsBinlogConsistentStart(data []byte) (int, error) {
	if !bytes.HasPrefix(data, []byte(rdsBinlogMagic)) {
		return 0, fmt.Errorf("the file does not start with the binary log magic number")
	}
	return rdsBinlogWholeTransactionsEnd(data, len(rdsBinlogMagic)), nil
}

// rdsBinlogWholeTransactionsEnd is the offset, from offset on, of the first
// transaction data holds only in part, or the end of its last complete event.
func rdsBinlogWholeTransactionsEnd(data []byte, offset int) int {
	open := -1
	// A MariaDB GTID event opens the transaction itself, and a standalone one
	// opens a single statement with no BEGIN, COMMIT or XID around it.
	mariaDB, standalone := false, false
	for offset+rdsBinlogEventHeaderLength <= len(data) {
		size := int(binary.LittleEndian.Uint32(data[offset+9:]))
		if size < rdsBinlogEventHeaderLength || offset+size > len(data) {
			break
		}
		event := data[offset : offset+size]
		switch event[4] {
		case rdsBinlogGTIDEvent, rdsBinlogAnonymousGTID:
			open, mariaDB = offset, false
		case rdsBinlogMariaDBGTIDEvent:
			open, mariaDB, standalone = offset, true, rdsBinlogMariaDBStandalone(event)
		case rdsBinlogXIDEvent:
			open = -1
		case rdsBinlogQueryEvent:
			switch {
			case open < 0:
			case mariaDB:
				if standalone || rdsBinlogQueryIs(event, "COMMIT") || rdsBinlogQueryIs(event, "ROLLBACK") {
					open = -1
				}
			case !rdsBinlogQueryIs(event, "BEGIN"):
				open = -1
			}
		}
		offset += size
	}
	if open >= 0 {
		return open
	}
	return offset
}

// rdsBinlogMariaDBStandalone reports whether a MariaDB GTID event carries
// FL_STANDALONE: its event group is one statement, DDL or non-transactional,
// that no BEGIN opened.
func rdsBinlogMariaDBStandalone(event []byte) bool {
	at := rdsBinlogEventHeaderLength + rdsBinlogMariaDBGTIDFlagsOffset
	return at < len(event) && event[at]&rdsBinlogMariaDBStandaloneFlag != 0
}

// rdsBinlogQueryIs reports whether a query event's statement is statement,
// such as the BEGIN that opens a transaction or the COMMIT that ends a MariaDB
// one. The event may end in a 4-byte checksum.
func rdsBinlogQueryIs(event []byte, statement string) bool {
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
	return bytes.HasPrefix(query, []byte(statement)) && (len(query) == len(statement) || len(query) == len(statement)+4)
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
