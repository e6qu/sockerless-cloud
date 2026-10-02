package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// An Aurora MySQL restore to a time replays the source's binary log onto the
// base backup with MySQL's own replication applier: the binary log files
// written since the base backup become the relay log of a server started on
// the new cluster volume, and its SQL thread applies them up to the first
// transaction that committed after RestoreToTime.

const (
	rdsBinlogMagic             = "\xfebin"
	rdsBinlogEventHeaderLength = 19
	rdsBinlogGTIDEvent         = 33
	rdsBinlogAnonymousGTID     = 34
	// A GTID event's post-header is its flags, source UUID, sequence number,
	// logical-timestamp type, last_committed and sequence_number; the
	// immediate commit timestamp follows it in 7 bytes, whose top bit says an
	// original commit timestamp follows.
	rdsBinlogGTIDPostHeaderLength = 42
	rdsBinlogCommitTimestampBytes = 7
)

// rdsListBinaryLogScript prints, base64-encoded, every binary log file of the
// source from the file the base backup started onward.
const rdsListBinaryLogScript = `set -e
start=$(tail -n 1 "$DATA/binlog.index")
start=${start##*/}
if ! grep -qx "./$start" /source/binlog.index; then
	echo "the source no longer holds binary log file $start"
	exit 1
fi
for f in $(sed 's|^\./||' /source/binlog.index); do
	[ "$f" \< "$start" ] && continue
	echo "binlog $f"
	base64 -w 76 "/source/$f"
done
`

// rdsReplayBinaryLogScript copies RELAY_FILES into the data directory as a
// relay log, cutting the last at LAST_SIZE, and applies it with a server that
// writes no binary log and accepts no network client. A user only the init
// file creates drives the applier, runs SET_MASTER_PASSWORD, and goes before
// the server stops.
const rdsReplayBinaryLogScript = `set -e
cd "$DATA"
n=0
: > sockerless-replay.index
for f in $RELAY_FILES; do
	n=$((n + 1))
	relay=$(printf 'sockerless-replay.%06d' "$n")
	cp "/source/$f" "$relay"
	echo "./$relay" >> sockerless-replay.index
done
truncate -s "$LAST_SIZE" "$relay"
chown mysql:mysql sockerless-replay.*
password=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')
printf "CREATE USER 'sockerless_replay'@'localhost' IDENTIFIED BY '%s';\nGRANT ALL ON *.* TO 'sockerless_replay'@'localhost' WITH GRANT OPTION;\n" "$password" > /tmp/replay-init.sql
chmod 644 /tmp/replay-init.sql
mysqld --user=mysql --skip-log-bin --server-id=4294967295 --skip-networking --socket=/tmp/replay.sock \
	--relay-log=sockerless-replay --relay-log-index=sockerless-replay.index --skip-replica-start \
	--replica-parallel-workers=0 --init-file=/tmp/replay-init.sql &
server=$!
client() {
	mysql --socket=/tmp/replay.sock --user=sockerless_replay --password="$password" --batch --skip-column-names "$@" 2>/dev/null
}
until client --execute='SELECT 1' >/dev/null; do
	kill -0 "$server"
	sleep 0.1
done
client --execute="CHANGE REPLICATION SOURCE TO RELAY_LOG_FILE='sockerless-replay.000001', RELAY_LOG_POS=4, SOURCE_HOST='sockerless-replay'; START REPLICA SQL_THREAD UNTIL RELAY_LOG_FILE='$relay', RELAY_LOG_POS=$LAST_SIZE"
while client --execute='SHOW REPLICA STATUS\G' | grep -q 'Replica_SQL_Running: Yes'; do
	sleep 0.1
done
failure=$(client --execute='SHOW REPLICA STATUS\G' | sed -n 's/^ *Last_SQL_Error: //p')
client --execute="STOP REPLICA; RESET REPLICA ALL"
if [ -z "$failure" ]; then
	client --execute="$SET_MASTER_PASSWORD"
fi
client --execute="DROP USER 'sockerless_replay'@'localhost'"
kill -TERM "$server"
wait "$server"
if [ -n "$failure" ]; then
	echo "$failure"
	exit 1
fi
`

// rdsReplaySandbox lets the replaying server drop from root to the mysql user,
// and the script signal it.
var rdsReplaySandbox = func() sim.SandboxProfile {
	profile := rdsHelperSandbox
	profile.CapAdd = append(append([]string(nil), profile.CapAdd...), "SETUID", "SETGID", "KILL")
	return profile
}()

type rdsBinlogFile struct {
	name string
	data []byte
}

// rdsReadBinaryLogListing decodes the files rdsListBinaryLogScript printed.
func rdsReadBinaryLogListing(lines []string) ([]rdsBinlogFile, error) {
	var files []rdsBinlogFile
	var encoded strings.Builder
	flush := func() error {
		if len(files) == 0 {
			return nil
		}
		data, err := base64.StdEncoding.DecodeString(encoded.String())
		if err != nil {
			return fmt.Errorf("decode binary log file %s: %w", files[len(files)-1].name, err)
		}
		files[len(files)-1].data = data
		encoded.Reset()
		return nil
	}
	for _, line := range lines {
		if name, ok := strings.CutPrefix(line, "binlog "); ok {
			if err := flush(); err != nil {
				return nil, err
			}
			files = append(files, rdsBinlogFile{name: strings.TrimSpace(name)})
			continue
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("unexpected binary log listing line %q", line)
		}
		encoded.WriteString(strings.TrimSpace(line))
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("the source holds no binary log since the base backup")
	}
	return files, nil
}

// rdsBinaryLogReplayRange names the files to replay and the size the last is
// cut to: just before the first transaction whose immediate commit timestamp
// is after target, or the end of the last complete event.
func rdsBinaryLogReplayRange(files []rdsBinlogFile, target time.Time) ([]string, int, error) {
	targetMicros := target.UnixMicro()
	var names []string
	end := 0
	for _, file := range files {
		if !strings.HasPrefix(string(file.data), rdsBinlogMagic) {
			return nil, 0, fmt.Errorf("binary log file %s does not start with the binary log magic number", file.name)
		}
		names = append(names, file.name)
		offset := len(rdsBinlogMagic)
		for offset+rdsBinlogEventHeaderLength <= len(file.data) {
			size := int(binary.LittleEndian.Uint32(file.data[offset+9:]))
			if size < rdsBinlogEventHeaderLength || offset+size > len(file.data) {
				break
			}
			eventType := file.data[offset+4]
			timestampAt := offset + rdsBinlogEventHeaderLength + rdsBinlogGTIDPostHeaderLength
			if (eventType == rdsBinlogGTIDEvent || eventType == rdsBinlogAnonymousGTID) &&
				timestampAt+rdsBinlogCommitTimestampBytes <= offset+size {
				var encoded [8]byte
				copy(encoded[:], file.data[timestampAt:timestampAt+rdsBinlogCommitTimestampBytes])
				committed := int64(binary.LittleEndian.Uint64(encoded[:]) &^ (1 << 55))
				if committed > targetMicros {
					return names, offset, nil
				}
			}
			offset += size
		}
		end = offset
	}
	return names, end, nil
}

// rdsReplayBinaryLog replays the binary log in sourceVolume, the source
// cluster volume, onto the new cluster volume up to target. The replay brings
// back the master password of the restore time, so the new cluster's master
// password, the one its record carries, replaces it.
func rdsReplayBinaryLog(engine dbengine.Engine, cluster RDSCluster, sourceVolume string, target time.Time) error {
	clusterID := cluster.DBClusterIdentifier
	clusterVolume := rdsClusterVolume(clusterID)
	_, password, ok := kmsDecryptBytes(cluster.MasterUserSecret)
	if !ok {
		return fmt.Errorf("decrypt the Amazon Aurora master-user credential")
	}
	listing, err := rdsRunVolumeHelper(engine, rdsListBinaryLogScript, nil, rdsHelperSandbox,
		[]string{sourceVolume + ":/source:ro", clusterVolume + ":" + engine.DataPath + ":ro"}, clusterID)
	if err != nil {
		return err
	}
	files, err := rdsReadBinaryLogListing(listing)
	if err != nil {
		return err
	}
	names, lastSize, err := rdsBinaryLogReplayRange(files, target)
	if err != nil {
		return err
	}
	_, err = rdsRunVolumeHelper(engine, rdsReplayBinaryLogScript,
		map[string]string{
			"RELAY_FILES":         strings.Join(names, " "),
			"LAST_SIZE":           strconv.Itoa(lastSize),
			"SET_MASTER_PASSWORD": rdsMySQLSetMasterPasswordStatement(cluster.MasterUsername, string(password)),
		},
		rdsReplaySandbox, []string{sourceVolume + ":/source:ro", clusterVolume + ":" + engine.DataPath}, clusterID)
	if err != nil {
		return fmt.Errorf("replay the binary log: %w", err)
	}
	return nil
}
