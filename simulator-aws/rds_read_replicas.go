package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// An Amazon RDS read replica of a DB instance runs an engine of its own on a
// capture of its source's volume and replicates the source with the engine's
// own replication: a MySQL or MariaDB replica applies the source's binary log
// from the position the capture holds, and a PostgreSQL replica is a hot
// standby streaming the source's write-ahead log through a physical
// replication slot. The source's engine and its replicas' engines share a
// container network of the source's, on which the source answers as
// "source". The replica serves its sessions read-only, reports how far it
// lags as the ReplicaLag metric each minute, and PromoteReadReplica ends its
// replication and reopens it for writes.

const (
	rdsReplicationUser       = "rdsrepladmin"
	rdsReplicationSourceHost = "source"
	// rdsReplicaLagPeriod is the one-minute interval at which Amazon RDS
	// publishes its instance metrics.
	rdsReplicaLagPeriod = time.Minute
	// rdsReplicationStartBudget bounds the wait for a new replica's
	// replication to run.
	rdsReplicationStartBudget = 5 * time.Minute
)

// rdsIsReadReplica reports whether instance replicates a source as a read
// replica; a blue/green deployment's green instance does not.
func rdsIsReadReplica(instance RDSInstance) bool {
	return instance.ReadReplicaSource != "" && instance.BlueGreenDeploymentIdentifier == ""
}

// rdsReadReplicasOf lists the read replicas of source.
func rdsReadReplicasOf(source RDSInstance) []RDSInstance {
	var replicas []RDSInstance
	for _, id := range source.ReadReplicas {
		if replica, ok := rdsInstances.Get(id); ok && rdsIsReadReplica(replica) && replica.ReadReplicaSource == source.DBInstanceIdentifier {
			replicas = append(replicas, replica)
		}
	}
	return replicas
}

// rdsReplicationNetwork names the container network a source shares with its
// read replicas.
func rdsReplicationNetwork(source RDSInstance) string {
	return "sockerless-rds-replication-" + strings.ToLower(source.DbiResourceId)
}

// rdsReplicationSlot names the physical replication slot a PostgreSQL source
// keeps for a replica.
func rdsReplicationSlot(replica RDSInstance) string {
	return "rds_" + strings.ReplaceAll(strings.ToLower(replica.DbiResourceId), "-", "_")
}

// rdsReplicaServerID is the server ID a MySQL-family replica runs with, which
// differs from its source's.
func rdsReplicaServerID(instance RDSInstance) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(instance.DbiResourceId))
	return strconv.FormatUint(uint64(h.Sum32()%(1<<31-2))+2, 10)
}

// rdsReplicaEngineArgs gives a MySQL-family replica's engine its own server ID
// and runs it read_only, as RDS's {TrueIfReplica} default does.
func rdsReplicaEngineArgs(engine dbengine.Engine, instance RDSInstance) dbengine.Engine {
	if !rdsIsReadReplica(instance) || engine.Family != dbengine.MySQL {
		return engine
	}
	engine.Args = append(append([]string(nil), engine.Args...), "--server-id="+rdsReplicaServerID(instance), "--read-only=ON")
	return engine
}

// rdsReplicationRoot is the instance at the top of source's replication
// chain, which holds the replication user's credential.
func rdsReplicationRoot(source RDSInstance) RDSInstance {
	for rdsIsReadReplica(source) {
		parent, ok := rdsInstances.Get(source.ReadReplicaSource)
		if !ok {
			break
		}
		source = parent
	}
	return source
}

// rdsReplicationPassword is the replication user's password, which the root
// of source's chain records sealed and creates its engine user with.
func rdsReplicationPassword(source RDSInstance) (string, error) {
	root := rdsReplicationRoot(source)
	if len(root.ReplicationUserSecret) == 0 {
		raw := make([]byte, 15)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		password := fmt.Sprintf("%x", raw)
		sealed, err := rdsSealMasterPassword(password)
		if err != nil {
			return "", err
		}
		rdsInstances.Update(root.DBInstanceIdentifier, func(stored *RDSInstance) {
			if len(stored.ReplicationUserSecret) == 0 {
				stored.ReplicationUserSecret = sealed
			}
		})
		root, _ = rdsInstances.Get(root.DBInstanceIdentifier)
	}
	_, password, ok := kmsDecryptBytes(root.ReplicationUserSecret)
	if !ok {
		return "", fmt.Errorf("decrypt the replication user's credential")
	}
	return string(password), nil
}

// rdsEngineContainer is the running engine container of a DB instance.
func rdsEngineContainer(instanceID string) (string, error) {
	containers, err := sim.FindExistingContainers(map[string]string{"sockerless-rds-instance": instanceID})
	if err != nil {
		return "", err
	}
	for _, container := range containers {
		if container.Running {
			return container.ID, nil
		}
	}
	return "", fmt.Errorf("the engine of DB instance %s is not running", instanceID)
}

// rdsJoinReplicationNetwork attaches an instance's running engine to network,
// answering there as aliases.
func rdsJoinReplicationNetwork(instanceID, network string, aliases []string) error {
	if _, err := sim.EnsureDockerNetwork(network); err != nil {
		return err
	}
	containerID, err := rdsEngineContainer(instanceID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := sim.DockerClient().ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect the engine of DB instance %s: %w", instanceID, err)
	}
	if settings := inspected.Container.NetworkSettings; settings != nil {
		if _, joined := settings.Networks[network]; joined {
			return nil
		}
	}
	return sim.ConnectContainerToNetwork(containerID, network, aliases)
}

// rdsEngineOutput runs command in the instance's running engine and returns
// what it wrote to standard output.
func rdsEngineOutput(instanceID string, command []string) (string, error) {
	containerID, err := rdsEngineContainer(instanceID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	release, err := sim.HoldThawed(ctx, containerID)
	if err != nil {
		return "", err
	}
	defer release()
	docker := sim.DockerClient()
	created, err := docker.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", fmt.Errorf("create engine command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attach engine command: %w", err)
	}
	var stdout, stderr bytes.Buffer
	_, copyErr := stdcopy.StdCopy(&stdout, &stderr, attached.Reader)
	attached.Close()
	if copyErr != nil {
		return "", fmt.Errorf("read engine command output: %w", copyErr)
	}
	inspected, err := docker.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect engine command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return "", fmt.Errorf("engine command exited %d: %s", inspected.ExitCode, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	return stdout.String(), nil
}

// rdsInstanceSQL runs statements in the instance's engine as its
// administrator: PostgreSQL's master user, or MySQL's and MariaDB's root.
func rdsInstanceSQL(instance RDSInstance, engine dbengine.Engine, statements string) (string, error) {
	if engine.Family == dbengine.Postgres {
		return rdsEngineOutput(instance.DBInstanceIdentifier, []string{engine.Client, "-v", "ON_ERROR_STOP=1", "-At", "-F", "|",
			"-U", instance.MasterUsername, "-d", rdsDatabaseName(instance), "-c", statements})
	}
	secret := instance.BackendMasterUserSecret
	if len(secret) == 0 {
		secret = instance.MasterUserSecret
	}
	_, password, ok := kmsDecryptBytes(secret)
	if !ok {
		return "", fmt.Errorf("decrypt the master-user credential")
	}
	return rdsEngineOutput(instance.DBInstanceIdentifier, []string{engine.Client, "--user=root", "--password=" + string(password),
		"--batch", "--vertical", "--execute=" + statements})
}

// rdsPrepareReplicationSource readies a source's running engine to serve a
// new replica: it joins the source's replication network, the root of its
// chain sets the replication user's password to the one it records, which a
// restored or promoted instance's data may hold another of, and a PostgreSQL
// source admits that user's replication connections and keeps a slot for the
// replica.
func rdsPrepareReplicationSource(source, replica RDSInstance, engine dbengine.Engine) error {
	plane, served := rdsLoadDataPlane(source.DBInstanceIdentifier)
	if !served {
		return fmt.Errorf("source DB instance %s serves no engine", source.DBInstanceIdentifier)
	}
	if err := plane.engine.Ensure(); err != nil {
		return fmt.Errorf("start the source engine: %w", err)
	}
	if err := rdsJoinReplicationNetwork(source.DBInstanceIdentifier, rdsReplicationNetwork(source), []string{rdsReplicationSourceHost}); err != nil {
		return fmt.Errorf("attach the source engine to its replication network: %w", err)
	}
	password, err := rdsReplicationPassword(source)
	if err != nil {
		return err
	}
	root := rdsReplicationRoot(source)
	if engine.Family == dbengine.Postgres {
		if root.DBInstanceIdentifier == source.DBInstanceIdentifier {
			role := dbengine.QuoteIdentifier(rdsReplicationUser)
			createRole := "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = " + dbengine.QuoteLiteral(rdsReplicationUser) +
				") THEN CREATE ROLE " + role + "; END IF; END $$; ALTER ROLE " + role + " WITH LOGIN REPLICATION PASSWORD " +
				dbengine.QuoteLiteral(password)
			if _, err := rdsInstanceSQL(source, engine, createRole); err != nil {
				return fmt.Errorf("create the replication user: %w", err)
			}
			admit := `grep -qx "host replication ` + rdsReplicationUser + ` all scram-sha-256" "$PGDATA/pg_hba.conf" || ` +
				`echo "host replication ` + rdsReplicationUser + ` all scram-sha-256" >> "$PGDATA/pg_hba.conf"`
			if _, err := rdsEngineOutput(source.DBInstanceIdentifier, []string{"sh", "-c", admit}); err != nil {
				return fmt.Errorf("admit replication connections: %w", err)
			}
			if _, err := rdsInstanceSQL(source, engine, "SELECT pg_reload_conf()"); err != nil {
				return fmt.Errorf("reload the source's configuration: %w", err)
			}
		}
		slot := dbengine.QuoteLiteral(rdsReplicationSlot(replica))
		if _, err := rdsInstanceSQL(source, engine, "SELECT pg_create_physical_replication_slot("+slot+", true) "+
			"WHERE NOT EXISTS (SELECT FROM pg_replication_slots WHERE slot_name = "+slot+")"); err != nil {
			return fmt.Errorf("create the replication slot: %w", err)
		}
		return nil
	}
	if root.DBInstanceIdentifier != source.DBInstanceIdentifier {
		return nil
	}
	user := dbengine.QuoteMySQLLiteral(rdsReplicationUser) + "@'%'"
	identified := " IDENTIFIED BY " + dbengine.QuoteMySQLLiteral(password)
	statements := "CREATE USER IF NOT EXISTS " + user + identified + "; ALTER USER " + user + identified +
		"; GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO " + user
	if _, err := rdsInstanceSQL(source, engine, statements); err != nil {
		return fmt.Errorf("create the replication user: %w", err)
	}
	return nil
}

// rdsMySQLReplicaVolumeScript and rdsPostgresReplicaVolumeScript turn a
// capture of the source's volume into a replica's: a MySQL-family replica
// takes a server UUID of its own and prints the source's newest binary log
// file, from which it replicates; a PostgreSQL replica drops the source's
// replication slots and starts as a standby of the source.
const rdsMySQLReplicaVolumeScript = `set -e
cd "$DATA"
rm -f auto.cnf
last=$(tail -n 1 binlog.index | sed 's|^\./||')
echo "binlog $last"
base64 -w 76 "$last"
`

const rdsPostgresReplicaVolumeScript = `set -e
cd "$DATA"
rm -f postmaster.pid standby.signal
rm -rf pg_replslot/*
touch standby.signal
printf "primary_conninfo = '%s'\nprimary_slot_name = '%s'\n" "$CONNINFO" "$SLOT" >> postgresql.auto.conf
chown "$(stat -c %u:%g PG_VERSION)" standby.signal postgresql.auto.conf
`

// rdsSeedReadReplica captures the source's volume into the replica's and
// prepares it to replicate the source.
func rdsSeedReadReplica(replica RDSInstance) (RDSInstance, error) {
	source, ok := rdsInstances.Get(replica.ReadReplicaSource)
	if !ok {
		return replica, fmt.Errorf("source DB instance %s no longer exists", replica.ReadReplicaSource)
	}
	engine, ok := rdsLoggingEngine(replica.Engine, replica.EngineVersion)
	if !ok {
		return replica, fmt.Errorf("the simulator runs no %s %s engine", replica.Engine, replica.EngineVersion)
	}
	if err := rdsPrepareReplicationSource(source, replica, engine); err != nil {
		return replica, err
	}
	volume := rdsInstanceVolume(replica.DBInstanceIdentifier)
	// A seed an earlier process cut short starts again on an empty volume.
	rdsRemoveEngineContainers("Amazon RDS "+replica.DBInstanceIdentifier, map[string]string{"sockerless-rds-instance": replica.DBInstanceIdentifier})
	sim.RemoveVolumeSettled(volume, "rds")
	if err := sim.CaptureVolume(context.Background(), rdsInstanceVolume(source.DBInstanceIdentifier), volume, "rds"); err != nil {
		return replica, fmt.Errorf("capture the source volume: %w", err)
	}
	binds := []string{volume + ":" + engine.DataPath}
	label := replica.DBInstanceIdentifier
	if engine.Family == dbengine.Postgres {
		password, err := rdsReplicationPassword(source)
		if err != nil {
			return replica, err
		}
		conninfo := fmt.Sprintf("host=%s port=%d user=%s password=%s application_name=%s",
			rdsReplicationSourceHost, engine.Port, rdsReplicationUser, password, replica.DBInstanceIdentifier)
		_, err = rdsRunVolumeHelper(engine, rdsPostgresReplicaVolumeScript,
			map[string]string{"CONNINFO": conninfo, "SLOT": rdsReplicationSlot(replica)}, rdsHelperSandbox, binds, label)
		return replica, err
	}
	listing, err := rdsRunVolumeHelper(engine, rdsMySQLReplicaVolumeScript, nil, rdsHelperSandbox, binds, label)
	if err != nil {
		return replica, err
	}
	files, err := rdsReadBinaryLogListing(listing)
	if err != nil {
		return replica, err
	}
	last := files[len(files)-1]
	replica.ReplicaSourceLogFile = last.name
	replica.ReplicaSourceLogPos = rdsBinlogWholeTransactionsEnd(last.data, len(rdsBinlogMagic))
	return replica, nil
}

// rdsConfigureReplication points a MySQL-family replica's applier at its
// source the first time its engine runs, at the position its capture holds.
// The engine keeps the configuration and resumes it on every later start.
func rdsConfigureReplication(plane *rdsDataPlane) error {
	instance := plane.current()
	if instance.ReplicaSourceLogFile == "" {
		return nil
	}
	source, ok := rdsInstances.Get(instance.ReadReplicaSource)
	if !ok {
		return fmt.Errorf("source DB instance %s no longer exists", instance.ReadReplicaSource)
	}
	password, err := rdsReplicationPassword(source)
	if err != nil {
		return err
	}
	var statements string
	if plane.engine.Engine.Client == dbengine.MariaDB114.Client {
		statements = fmt.Sprintf("STOP SLAVE; RESET SLAVE ALL; CHANGE MASTER TO MASTER_HOST=%s, MASTER_PORT=%d, MASTER_USER=%s, "+
			"MASTER_PASSWORD=%s, MASTER_LOG_FILE=%s, MASTER_LOG_POS=%d, MASTER_USE_GTID=no; START SLAVE",
			dbengine.QuoteMySQLLiteral(rdsReplicationSourceHost), plane.engine.Engine.Port, dbengine.QuoteMySQLLiteral(rdsReplicationUser),
			dbengine.QuoteMySQLLiteral(password), dbengine.QuoteMySQLLiteral(instance.ReplicaSourceLogFile), instance.ReplicaSourceLogPos)
	} else {
		statements = fmt.Sprintf("STOP REPLICA; RESET REPLICA ALL; CHANGE REPLICATION SOURCE TO SOURCE_HOST=%s, SOURCE_PORT=%d, "+
			"SOURCE_USER=%s, SOURCE_PASSWORD=%s, SOURCE_LOG_FILE=%s, SOURCE_LOG_POS=%d, GET_SOURCE_PUBLIC_KEY=1; START REPLICA",
			dbengine.QuoteMySQLLiteral(rdsReplicationSourceHost), plane.engine.Engine.Port, dbengine.QuoteMySQLLiteral(rdsReplicationUser),
			dbengine.QuoteMySQLLiteral(password), dbengine.QuoteMySQLLiteral(instance.ReplicaSourceLogFile), instance.ReplicaSourceLogPos)
	}
	if _, err := rdsInstanceSQL(instance, plane.engine.Engine, statements); err != nil {
		return fmt.Errorf("start replicating the source: %w", err)
	}
	instance.ReplicaSourceLogFile, instance.ReplicaSourceLogPos = "", 0
	plane.record(instance)
	rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) {
		if stored.DbiResourceId == instance.DbiResourceId {
			stored.ReplicaSourceLogFile, stored.ReplicaSourceLogPos = "", 0
		}
	})
	return nil
}

// replicaReady joins a replica's engine to its source's replication network
// and starts its replication, in place of the account reconciliation a
// writable engine runs: the replica takes every account change from its
// source.
func (plane *rdsDataPlane) replicaReady() error {
	instance := plane.current()
	source, ok := rdsInstances.Get(instance.ReadReplicaSource)
	if !ok {
		return fmt.Errorf("source DB instance %s no longer exists", instance.ReadReplicaSource)
	}
	if err := rdsJoinReplicationNetwork(instance.DBInstanceIdentifier, rdsReplicationNetwork(source), nil); err != nil {
		return fmt.Errorf("attach the replica engine to its source's network: %w", err)
	}
	return rdsConfigureReplication(plane)
}

// sourceReady attaches a source's engine, started again, to the network its
// replicas reach it on.
func (plane *rdsDataPlane) sourceReady() error {
	instance := plane.current()
	if stored, ok := rdsInstances.Get(instance.DBInstanceIdentifier); !ok || len(rdsReadReplicasOf(stored)) == 0 {
		return nil
	}
	return rdsJoinReplicationNetwork(instance.DBInstanceIdentifier, rdsReplicationNetwork(instance), []string{rdsReplicationSourceHost})
}

// rdsReplicationState is what a replica's engine reports of its replication.
type rdsReplicationState struct {
	replicating bool
	// lag is ReplicaLag in seconds, -1 while replication is not running.
	lag     float64
	message string
}

// rdsObserveReplication reads the replica engine's own replication status.
func rdsObserveReplication(instance RDSInstance, engine dbengine.Engine) (rdsReplicationState, error) {
	if engine.Family == dbengine.Postgres {
		output, err := rdsInstanceSQL(instance, engine, "SELECT coalesce((SELECT status FROM pg_stat_wal_receiver), ''), "+
			"CASE WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0 "+
			"ELSE coalesce(extract(epoch FROM now() - pg_last_xact_replay_timestamp()), 0) END")
		if err != nil {
			return rdsReplicationState{}, err
		}
		status, lag, _ := strings.Cut(strings.TrimSpace(output), "|")
		if status != "streaming" {
			return rdsReplicationState{lag: -1, message: "The replica is not streaming from its source."}, nil
		}
		seconds, err := strconv.ParseFloat(lag, 64)
		if err != nil {
			return rdsReplicationState{}, fmt.Errorf("read the replica lag %q: %w", lag, err)
		}
		return rdsReplicationState{replicating: true, lag: seconds}, nil
	}
	statement, io, sql, behind := "SHOW REPLICA STATUS", "Replica_IO_Running", "Replica_SQL_Running", "Seconds_Behind_Source"
	if engine.Client == dbengine.MariaDB114.Client {
		statement, io, sql, behind = "SHOW SLAVE STATUS", "Slave_IO_Running", "Slave_SQL_Running", "Seconds_Behind_Master"
	}
	output, err := rdsInstanceSQL(instance, engine, statement)
	if err != nil {
		return rdsReplicationState{}, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		if name, value, found := strings.Cut(strings.TrimSpace(line), ": "); found {
			fields[name] = strings.TrimSpace(value)
		}
	}
	for _, failure := range []string{fields["Last_IO_Error"], fields["Last_SQL_Error"]} {
		if failure != "" {
			return rdsReplicationState{lag: -1, message: failure}, nil
		}
	}
	if fields[io] != "Yes" || fields[sql] != "Yes" {
		return rdsReplicationState{lag: -1, message: "Replication is not running."}, nil
	}
	seconds, err := strconv.ParseFloat(fields[behind], 64)
	if err != nil {
		return rdsReplicationState{lag: -1}, nil
	}
	return rdsReplicationState{replicating: true, lag: seconds}, nil
}

// rdsReplicaMonitor publishes a replica's ReplicaLag and replication status
// each minute while its data plane runs.
type rdsReplicaMonitor struct {
	plane   *rdsDataPlane
	mu      sync.Mutex
	stopped bool
	timer   *bg.Timer
	// publishing is held across one publication, so stop returns only once
	// none is under way.
	publishing sync.Mutex
}

func (m *rdsReplicaMonitor) stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.stopped = true
	if m.timer != nil {
		m.timer.Stop()
	}
	m.mu.Unlock()
	m.publishing.Lock()
	defer m.publishing.Unlock()
}

// publish publishes the replica's state and arms the next publication.
func (m *rdsReplicaMonitor) publish() {
	m.publishing.Lock()
	m.mu.Lock()
	stopped := m.stopped
	m.mu.Unlock()
	if !stopped {
		rdsPublishReplicaState(m.plane)
	}
	m.publishing.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stopped {
		m.timer = bg.AfterFunc(rdsReplicaLagPeriod, m.publish)
	}
}

// rdsMonitorReplica starts publishing the replica's metrics each minute, the
// first at once when now is set.
func rdsMonitorReplica(instanceID string, now bool) {
	plane, served := rdsLoadDataPlane(instanceID)
	if !served {
		return
	}
	monitor := &rdsReplicaMonitor{plane: plane}
	plane.mu.Lock()
	previous := plane.replication
	plane.replication = monitor
	plane.mu.Unlock()
	previous.stop()
	if now {
		bg.Go(monitor.publish)
		return
	}
	monitor.mu.Lock()
	monitor.timer = bg.AfterFunc(rdsReplicaLagPeriod, monitor.publish)
	monitor.mu.Unlock()
}

func rdsPublishReplicaState(plane *rdsDataPlane) {
	instance, ok := rdsInstances.Get(plane.current().DBInstanceIdentifier)
	if !ok || !rdsIsReadReplica(instance) || instance.DBInstanceStatus != "available" {
		return
	}
	state := rdsReplicationState{lag: -1, message: "The replica engine is not running."}
	if plane.engine.Running() {
		observed, err := rdsObserveReplication(instance, plane.engine.Engine)
		if err != nil {
			observed = rdsReplicationState{lag: -1, message: err.Error()}
		}
		state = observed
	}
	rdsRecordReplicationState(instance, state)
}

func rdsRecordReplicationState(instance RDSInstance, state rdsReplicationState) {
	cwStoreDatum(CWMetricDatum{
		Namespace:  "AWS/RDS",
		MetricName: "ReplicaLag",
		Dimensions: []CWDimension{{Name: "DBInstanceIdentifier", Value: instance.DBInstanceIdentifier}},
		Value:      state.lag,
		Unit:       "Seconds",
		Timestamp:  float64(time.Now().UTC().Unix()),
	})
	status := "replicating"
	if !state.replicating {
		status = "error"
	}
	rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) {
		if stored.DbiResourceId == instance.DbiResourceId {
			stored.ReplicationStatus, stored.ReplicationMessage = status, state.message
		}
	})
}

// rdsAwaitReplicating waits until a new replica's engine reports its
// replication running, and publishes that first observation.
func rdsAwaitReplicating(instance RDSInstance, engine dbengine.Engine) error {
	deadline := time.Now().Add(rdsReplicationStartBudget)
	for {
		state, err := rdsObserveReplication(instance, engine)
		if err == nil && state.replicating {
			rdsRecordReplicationState(instance, state)
			return nil
		}
		if !time.Now().Before(deadline) {
			if err == nil {
				err = errors.New(state.message)
			}
			return fmt.Errorf("replication did not start: %w", err)
		}
		if !rdsCreatingInstance(instance.DBInstanceIdentifier, instance.DbiResourceId) {
			return fmt.Errorf("the replica is no longer being created")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// rdsProvisionReadReplica seeds a creating replica's volume from its source,
// starts its engine replicating, and lands it available, or failed when its
// replication does not start. A replica deleted while it was seeded goes
// once the seed ends.
func rdsProvisionReadReplica(id string) {
	replica, ok := rdsInstances.Get(id)
	if !ok || replica.DBInstanceStatus != "creating" {
		return
	}
	resourceID := replica.DbiResourceId
	seeded, err := rdsSeedReadReplica(replica)
	if err == nil {
		rdsInstances.Update(id, func(stored *RDSInstance) {
			if stored.DbiResourceId == resourceID {
				stored.ReplicaSourceLogFile, stored.ReplicaSourceLogPos = seeded.ReplicaSourceLogFile, seeded.ReplicaSourceLogPos
			}
		})
		replica = seeded
	}
	if err == nil && rdsCreatingInstance(id, resourceID) {
		err = rdsStartInstanceEngine(&replica)
	}
	if err == nil && rdsCreatingInstance(id, resourceID) {
		plane, _ := rdsLoadDataPlane(id)
		if err = plane.engine.Ensure(); err == nil {
			err = rdsAwaitReplicating(replica, plane.engine.Engine)
		}
	}
	if err != nil {
		log.Printf("Amazon RDS %s: create read replica: %v", id, err)
	}
	available := false
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId != resourceID || stored.DBInstanceStatus != "creating" {
			return
		}
		if err != nil {
			stored.DBInstanceStatus = "failed"
			stored.ReplicationStatus, stored.ReplicationMessage = "error", err.Error()
			return
		}
		stored.Endpoint, stored.Port = replica.Endpoint, replica.Port
		stored.DBInstanceStatus = "available"
		stored.ReplicationStatus, stored.ReplicationMessage = "replicating", ""
		available = true
	})
	rdsFinishInstanceDeletion(id, resourceID)
	if available {
		rdsMonitorReplica(id, false)
	}
}

// rdsPromoteReadReplica ends a modifying replica's replication and reopens it
// as a standalone instance that accepts writes, with the backup settings its
// promotion recorded.
func rdsPromoteReadReplica(id string) {
	replica, ok := rdsInstances.Get(id)
	if !ok || replica.DBInstanceStatus != "modifying" || !rdsIsReadReplica(replica) {
		return
	}
	engine, _ := rdsLoggingEngine(replica.Engine, replica.EngineVersion)
	err := rdsEndReplication(replica, engine)
	if err != nil {
		log.Printf("Amazon RDS %s: promote read replica: %v", id, err)
	}
	if source, found := rdsInstances.Get(replica.ReadReplicaSource); found {
		rdsDropReplicationSlot(source, replica, engine)
		rdsInstances.Update(source.DBInstanceIdentifier, func(stored *RDSInstance) {
			stored.ReadReplicas = rdsRemoveString(stored.ReadReplicas, id)
		})
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		stored.ReadReplicaSource = ""
		stored.ReplicationStatus, stored.ReplicationMessage = "", ""
		stored.ReplicaSourceLogFile, stored.ReplicaSourceLogPos = "", 0
	})
	promoted, _ := rdsInstances.Get(id)
	status := "available"
	if err == nil {
		err = rdsStopDataPlane(id, false)
	}
	if err == nil && len(promoted.MasterUserSecret) > 0 {
		err = rdsStartInstanceEngine(&promoted)
		if plane, served := rdsLoadDataPlane(id); err == nil && served {
			err = plane.engine.Ensure()
		}
	}
	if err != nil {
		log.Printf("Amazon RDS %s: reopen the promoted replica: %v", id, err)
		status = "failed"
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId == promoted.DbiResourceId && stored.DBInstanceStatus == "modifying" {
			stored.Endpoint, stored.Port = promoted.Endpoint, promoted.Port
			stored.DBInstanceStatus = status
		}
	})
	rdsTakeFirstInstanceBackup(id)
}

// rdsEndReplication stops a replica's engine replicating and lets it write:
// MySQL and MariaDB discard the replica's applier configuration and turn
// read_only off, and PostgreSQL promotes the standby.
func rdsEndReplication(replica RDSInstance, engine dbengine.Engine) error {
	plane, served := rdsLoadDataPlane(replica.DBInstanceIdentifier)
	if !served {
		return nil
	}
	if err := plane.engine.Ensure(); err != nil {
		return err
	}
	var statement string
	switch engine.Client {
	case dbengine.Postgres16.Client:
		statement = "SELECT pg_promote(true, 60) WHERE pg_is_in_recovery()"
	case dbengine.MariaDB114.Client:
		statement = "STOP SLAVE; RESET SLAVE ALL; SET GLOBAL read_only = OFF"
	default:
		statement = "STOP REPLICA; RESET REPLICA ALL; SET GLOBAL read_only = OFF"
	}
	_, err := rdsInstanceSQL(replica, engine, statement)
	return err
}

// rdsDropReplicationSlot drops the slot a PostgreSQL source kept for a
// replica that no longer replicates, so the source stops retaining its log.
func rdsDropReplicationSlot(source, replica RDSInstance, engine dbengine.Engine) {
	if engine.Family != dbengine.Postgres {
		return
	}
	if plane, served := rdsLoadDataPlane(source.DBInstanceIdentifier); !served || !plane.engine.Running() {
		return
	}
	slot := dbengine.QuoteLiteral(rdsReplicationSlot(replica))
	if _, err := rdsInstanceSQL(source, engine, "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = "+slot); err != nil {
		log.Printf("Amazon RDS %s: drop the replication slot of %s: %v", source.DBInstanceIdentifier, replica.DBInstanceIdentifier, err)
	}
}

// rdsDetachDeletedInstance unlinks a deleted instance from its replication:
// a deleted replica leaves its source's replica list and slot, and the
// replicas of a deleted source are promoted to standalone instances, after
// which the source's replication network goes.
func rdsDetachDeletedInstance(instance RDSInstance) {
	if rdsIsReadReplica(instance) {
		if source, ok := rdsInstances.Get(instance.ReadReplicaSource); ok {
			engine, _ := rdsLoggingEngine(instance.Engine, instance.EngineVersion)
			rdsDropReplicationSlot(source, instance, engine)
			rdsInstances.Update(source.DBInstanceIdentifier, func(stored *RDSInstance) {
				stored.ReadReplicas = rdsRemoveString(stored.ReadReplicas, instance.DBInstanceIdentifier)
			})
		}
	}
	for _, replica := range rdsReadReplicasOf(instance) {
		id := replica.DBInstanceIdentifier
		promoting := false
		rdsInstances.Update(id, func(stored *RDSInstance) {
			if stored.DBInstanceStatus == "available" {
				stored.DBInstanceStatus = "modifying"
				stored.BackupRetentionPeriod = 1
				promoting = true
			}
		})
		if promoting {
			rdsPromoteReadReplica(id)
		}
	}
	if sim.RequireContainerRuntime("removing an Amazon RDS replication network") == nil {
		if err := sim.RemoveDockerNetwork(rdsReplicationNetwork(instance)); err != nil {
			log.Printf("Amazon RDS %s: remove the replication network: %v", instance.DBInstanceIdentifier, err)
		}
	}
}

func handleRDSPromoteReadReplica(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", fmt.Sprintf("DBInstance %s not found.", id), http.StatusNotFound, requestID)
		return
	}
	if rdsRefuseUnavailableInstance(w, r, inst) {
		return
	}
	if !rdsIsReadReplica(inst) {
		rdsErrorXML(w, "InvalidDBInstanceState",
			fmt.Sprintf("DB Instance %s is not a read replica.", id), http.StatusBadRequest, requestID)
		return
	}
	retention, window, problem := rdsInstanceBackupSettings(r, 1, inst.PreferredBackupWindow)
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, requestID)
		return
	}
	if len(inst.MasterUserSecret) == 0 {
		// An engine the simulator runs no data plane for has no replication
		// to end.
		rdsInstances.Update(id, func(stored *RDSInstance) {
			stored.ReadReplicaSource, stored.BackupRetentionPeriod, stored.PreferredBackupWindow = "", retention, window
		})
		rdsInstances.Update(inst.ReadReplicaSource, func(stored *RDSInstance) {
			stored.ReadReplicas = rdsRemoveString(stored.ReadReplicas, id)
		})
		updated, _ := rdsInstances.Get(id)
		rdsXMLResponse(w, "PromoteReadReplica", renderRDSInstance(updated), requestID)
		return
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		stored.DBInstanceStatus = "modifying"
		stored.BackupRetentionPeriod, stored.PreferredBackupWindow = retention, window
	})
	updated, _ := rdsInstances.Get(id)
	bg.Go(func() { rdsPromoteReadReplica(id) })
	rdsXMLResponse(w, "PromoteReadReplica", renderRDSInstance(updated), requestID)
}

// renderRDSReplicaStatusInfos renders a replica's read replication status.
func renderRDSReplicaStatusInfos(instance RDSInstance) string {
	if !rdsIsReadReplica(instance) || instance.ReplicationStatus == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<StatusInfos><DBInstanceStatusInfo><StatusType>read replication</StatusType>")
	fmt.Fprintf(&b, "<Normal>%t</Normal>", instance.ReplicationStatus == "replicating")
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(instance.ReplicationStatus))
	if instance.ReplicationMessage != "" {
		fmt.Fprintf(&b, "<Message>%s</Message>", xmlEscape(instance.ReplicationMessage))
	}
	b.WriteString("</DBInstanceStatusInfo></StatusInfos>")
	return b.String()
}

func handleRDSCreateReadReplica(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	fail := func(code, message string, status int) { rdsErrorXML(w, code, message, status, requestID) }
	id := r.FormValue("DBInstanceIdentifier")
	srcID := r.FormValue("SourceDBInstanceIdentifier")
	if id == "" || srcID == "" {
		fail("MissingParameter", "DBInstanceIdentifier and SourceDBInstanceIdentifier are required", http.StatusBadRequest)
		return
	}
	src, ok := findRDSByARN(srcID)
	if !ok {
		fail("DBInstanceNotFound", fmt.Sprintf("DBInstance %s not found.", srcID), http.StatusNotFound)
		return
	}
	if rdsRefuseUnavailableInstance(w, r, src) {
		return
	}
	if _, exists := rdsInstances.Get(id); exists {
		fail("DBInstanceAlreadyExists", fmt.Sprintf("DBInstance %s already exists.", id), http.StatusBadRequest)
		return
	}
	if src.DBClusterIdentifier != "" {
		fail("InvalidDBInstanceState", fmt.Sprintf("DB instance %s is a member of DB cluster %s; add an Aurora Replica to the cluster instead.",
			src.DBInstanceIdentifier, src.DBClusterIdentifier), http.StatusBadRequest)
		return
	}
	served := len(src.MasterUserSecret) > 0 && rdsVersionedEngine(src.Engine)
	if served && src.DBInstanceStatus != "available" {
		fail("InvalidDBInstanceState", fmt.Sprintf("Instance %s is not in available state.", src.DBInstanceIdentifier), http.StatusBadRequest)
		return
	}
	if served && src.BackupRetentionPeriod == 0 {
		fail("InvalidDBInstanceState", "Automated backups are not enabled for this database instance. To enable automated backups, "+
			"use ModifyDBInstance to set the backup retention period to a non-zero value.", http.StatusBadRequest)
		return
	}
	paramGroup, code, message := rdsInstanceParameterGroup(r.FormValue("DBParameterGroupName"), src.Engine, src.EngineVersion)
	if code != "" {
		fail(code, message, rdsParameterGroupErrorStatus(code))
		return
	}
	class := r.FormValue("DBInstanceClass")
	if class == "" {
		class = src.DBInstanceClass
	}
	az := awsRegion() + "a"
	if v := r.FormValue("AvailabilityZone"); v != "" {
		az = v
	}
	port := src.Port
	if v := atoiOrZero(r.FormValue("Port")); v > 0 {
		port = v
	}
	iamAuthentication := src.EnableIAMDatabaseAuthentication
	if requested := rdsRequestedBool(r, "EnableIAMDatabaseAuthentication"); requested != nil {
		iamAuthentication = *requested
	}
	replica := RDSInstance{
		PreferredBackupWindow:           src.PreferredBackupWindow,
		PreferredMaintenanceWindow:      rdsDefaultMaintenanceWindow(src.PreferredBackupWindow),
		DBInstanceIdentifier:            id,
		DbiResourceId:                   rdsResourceID(),
		DBInstanceClass:                 class,
		Engine:                          src.Engine,
		EngineVersion:                   src.EngineVersion,
		DBInstanceStatus:                "available",
		MasterUsername:                  src.MasterUsername,
		DBName:                          src.DBName,
		AllocatedStorage:                src.AllocatedStorage,
		Endpoint:                        fmt.Sprintf("%s.%s.rds.amazonaws.com", id, awsRegion()),
		Port:                            port,
		AvailabilityZone:                az,
		InstanceCreateTime:              time.Now().UTC().Format(time.RFC3339),
		ARN:                             rdsInstanceARN(id),
		ReadReplicaSource:               src.DBInstanceIdentifier,
		Tags:                            parseAWSQueryTagMap(r, "Tags.Tag"),
		BackupTarget:                    rdsRequestedBackupTarget(r),
		DBParameterGroupName:            paramGroup,
		EnableIAMDatabaseAuthentication: iamAuthentication,
		DeletionProtection:              strings.EqualFold(r.FormValue("DeletionProtection"), "true"),
		AutoMinorVersionUpgrade:         rdsRequestedBool(r, "AutoMinorVersionUpgrade"),
	}
	if served {
		replica.DBInstanceStatus = "creating"
		replica.MasterUserSecret = append([]byte(nil), src.MasterUserSecret...)
		replica.BackendMasterUserSecret = append([]byte(nil), src.BackendMasterUserSecret...)
	}
	rdsInstances.Put(id, replica)
	rdsInstances.Update(src.DBInstanceIdentifier, func(i *RDSInstance) {
		i.ReadReplicas = rdsAppendUnique(i.ReadReplicas, id)
	})
	if served {
		bg.Go(func() { rdsProvisionReadReplica(id) })
	}
	rdsXMLResponse(w, "CreateDBInstanceReadReplica", renderRDSInstance(replica), requestID)
}

// rdsFollowSourceMasterPassword records a source's new master-user password
// on its replicas, whose engines take the password change through
// replication.
func rdsFollowSourceMasterPassword(source RDSInstance) {
	for _, replica := range rdsReadReplicasOf(source) {
		rdsInstances.Update(replica.DBInstanceIdentifier, func(stored *RDSInstance) {
			stored.MasterUserSecret = append([]byte(nil), source.MasterUserSecret...)
			stored.BackendMasterUserSecret = append([]byte(nil), source.BackendMasterUserSecret...)
		})
		updated, _ := rdsInstances.Get(replica.DBInstanceIdentifier)
		if plane, served := rdsLoadDataPlane(replica.DBInstanceIdentifier); served {
			plane.record(updated)
		}
		rdsFollowSourceMasterPassword(updated)
	}
}
