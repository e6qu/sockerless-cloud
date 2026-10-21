package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// A Multi-AZ DB instance deployment keeps a synchronous standby of the
// primary in another Availability Zone of the Region: an engine of its own on
// a capture of the primary's volume, replicating the primary over the
// replication network the primary shares with its read replicas. The
// PostgreSQL primary names the standby in synchronous_standby_names, so a
// commit returns once the standby has flushed it; the MySQL and MariaDB
// primaries wait for the standby's semisynchronous acknowledgement. No client
// reaches the standby. RebootDBInstance with ForceFailover promotes it: the
// primary stops, the standby applies everything it received and opens for
// writes, the endpoint serves its data from the standby's Availability Zone,
// and a new standby is built from the new primary in the old primary's zone.

const (
	// rdsStandbyApplicationName is the name the PostgreSQL standby streams
	// under, which the primary's synchronous_standby_names lists.
	rdsStandbyApplicationName = "rds_multi_az_standby"
	rdsStandbyProbeInterval   = 250 * time.Millisecond
)

// rdsStandby is the standby engine of one Multi-AZ instance.
type rdsStandby struct {
	engine *dbengine.Instance
	labels map[string]string
	name   string

	mu sync.Mutex
	// logFile and logPos are where a newly seeded MySQL-family standby starts
	// replicating, which its first start configures.
	logFile string
	logPos  int
}

var (
	// rdsStandbys holds each Multi-AZ instance's standby by DbiResourceId,
	// which a rename keeps.
	rdsStandbys sync.Map
	// rdsStandbyLocks serializes the building, removal and promotion of one
	// instance's standby.
	rdsStandbyLocks = sim.NewKeyedLocks()
)

func rdsStandbyVolume(instance RDSInstance) string {
	return rdsVolume("standby", instance.DbiResourceId)
}

func rdsStandbyLabels(instance RDSInstance) map[string]string {
	return map[string]string{"sockerless-rds-standby": instance.DbiResourceId}
}

// rdsStandbySlot names the physical replication slot a PostgreSQL primary
// keeps for its standby.
func rdsStandbySlot(instance RDSInstance) string {
	return rdsReplicationSlot(RDSInstance{DbiResourceId: instance.DbiResourceId + "-standby"})
}

// rdsRegionAvailabilityZones are the Availability Zones Amazon RDS places DB
// instances in.
func rdsRegionAvailabilityZones() []string {
	region := awsRegion()
	return []string{region + "a", region + "b", region + "c"}
}

// rdsStandbyAvailabilityZone is the zone a standby of a primary in primary
// runs in: another zone of the Region.
func rdsStandbyAvailabilityZone(primary string) string {
	for _, zone := range rdsRegionAvailabilityZones() {
		if zone != primary {
			return zone
		}
	}
	return ""
}

// rdsRunsEngine reports whether the simulator runs instance's engine, which a
// Multi-AZ deployment then keeps a standby of.
func rdsRunsEngine(instance RDSInstance) bool {
	return len(instance.MasterUserSecret) > 0 && rdsVersionedEngine(instance.Engine) && !rdsIsAurora(instance.Engine)
}

// rdsStandbyConcerns reports whether instance has a standby to build, keep or
// remove.
func rdsStandbyConcerns(instance RDSInstance) bool {
	if instance.MultiAZ || instance.AvailabilityConversion || instance.SecondaryAvailabilityZone != "" {
		return true
	}
	_, held := rdsStandbys.Load(instance.DbiResourceId)
	return held
}

// rdsStandbyEngine is the engine a standby of a primary running primary runs:
// a PostgreSQL standby archives the log it receives, so the archive a restore
// to a time reads holds it once the standby is promoted; a MySQL-family
// standby runs read-only with a server ID of its own, logs what it applies to
// its own binary log and acknowledges each transaction semisynchronously.
func rdsStandbyEngine(primary dbengine.Engine, instance RDSInstance) dbengine.Engine {
	standby := primary
	standby.Args = nil
	for _, arg := range primary.Args {
		if arg == "archive_mode=on" {
			arg = "archive_mode=always"
		}
		standby.Args = append(standby.Args, arg)
	}
	switch primary.Client {
	case dbengine.MariaDB114.Client:
		standby.Args = append(standby.Args, "--server-id="+rdsStandbyServerID(instance), "--read-only=ON",
			"--log-slave-updates=ON", "--rpl-semi-sync-slave-enabled=ON")
	case dbengine.MySQL80.Client:
		standby.Args = append(standby.Args, "--server-id="+rdsStandbyServerID(instance), "--read-only=ON",
			"--plugin-load-add=semisync_replica.so", "--rpl-semi-sync-replica-enabled=ON")
	}
	return standby
}

func rdsStandbyServerID(instance RDSInstance) string {
	return rdsReplicaServerID(RDSInstance{DbiResourceId: instance.DbiResourceId + "-standby"})
}

// rdsInstanceEngine is the engine a DB instance runs under its recorded
// parameters.
func rdsInstanceEngine(instance RDSInstance) (dbengine.Engine, bool) {
	engine, ok := rdsLoggingEngine(instance.Engine, instance.EngineVersion)
	if !ok {
		return engine, false
	}
	engine.Args = append(append([]string(nil), engine.Args...), rdsEngineParameterArgs(engine, instance)...)
	return engine, true
}

// rdsNewStandby makes the standby of instance on its standby volume, adopting
// the engine an earlier process left running there.
func rdsNewStandby(instance RDSInstance, engine dbengine.Engine) (*rdsStandby, error) {
	password, err := rdsInstanceMasterPassword(instance)
	if err != nil {
		return nil, err
	}
	environment := rdsEngineEnvironment(engine, instance.MasterUsername, password, rdsDatabaseName(instance))
	standby := &rdsStandby{labels: rdsStandbyLabels(instance), name: "the standby of DB instance " + instance.DBInstanceIdentifier}
	resourceID := instance.DbiResourceId
	standby.engine = &dbengine.Instance{
		Name:        "Amazon RDS " + instance.DBInstanceIdentifier + " standby",
		Engine:      engine,
		Volume:      rdsStandbyVolume(instance),
		Labels:      standby.labels,
		Sandbox:     SandboxFargate,
		Platform:    dbengine.FixedPlatform("linux/amd64"),
		Environment: func() (map[string]string, error) { return environment, nil },
		Ready:       func() error { return standby.ready(resourceID) },
	}
	if sim.RequireContainerRuntime("adopting an Amazon RDS standby") == nil {
		if err := standby.engine.Adopt(); err != nil {
			return nil, fmt.Errorf("adopt %s: %w", standby.name, err)
		}
	}
	return standby, nil
}

// rdsInstanceMasterPassword is the master-user password installed in the
// instance's engine.
func rdsInstanceMasterPassword(instance RDSInstance) (string, error) {
	secret := instance.BackendMasterUserSecret
	if len(secret) == 0 {
		secret = instance.MasterUserSecret
	}
	_, password, ok := kmsDecryptBytes(secret)
	if !ok {
		return "", fmt.Errorf("decrypt the master-user credential")
	}
	return string(password), nil
}

// rdsInstanceByResourceID is the instance whose DbiResourceId is resourceID.
func rdsInstanceByResourceID(resourceID string) (RDSInstance, bool) {
	for _, instance := range rdsInstances.Filter(func(i RDSInstance) bool { return i.DbiResourceId == resourceID }) {
		return instance, true
	}
	return RDSInstance{}, false
}

// ready joins the standby to its primary's replication network and points a
// newly seeded MySQL-family standby's applier at the primary.
func (s *rdsStandby) ready(resourceID string) error {
	instance, ok := rdsInstanceByResourceID(resourceID)
	if !ok {
		return fmt.Errorf("the DB instance of %s no longer exists", s.name)
	}
	if err := rdsJoinNetwork(s.labels, s.name, rdsReplicationNetwork(instance), nil); err != nil {
		return fmt.Errorf("attach %s to its primary's network: %w", s.name, err)
	}
	s.mu.Lock()
	file, position := s.logFile, s.logPos
	s.mu.Unlock()
	if file == "" {
		return nil
	}
	password, err := rdsReplicationPassword(instance)
	if err != nil {
		return err
	}
	if _, err := s.sql(instance, rdsReplicateSourceStatements(s.engine.Engine, password, file, position)); err != nil {
		return fmt.Errorf("start %s replicating: %w", s.name, err)
	}
	s.mu.Lock()
	s.logFile, s.logPos = "", 0
	s.mu.Unlock()
	return nil
}

func (s *rdsStandby) sql(instance RDSInstance, statements string) (string, error) {
	return rdsEngineSQL(s.labels, s.name, instance, s.engine.Engine, statements)
}

func (s *rdsStandby) query(instance RDSInstance) func(string) (string, error) {
	return func(statements string) (string, error) { return s.sql(instance, statements) }
}

// rdsAwait runs check every rdsStandbyProbeInterval until it reports done,
// fails, or rdsReplicationStartBudget passes.
func rdsAwait(what string, check func() (bool, error)) error {
	deadline := time.Now().Add(rdsReplicationStartBudget)
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s did not happen within %s", what, rdsReplicationStartBudget)
		}
		time.Sleep(rdsStandbyProbeInterval)
	}
}

// replicate starts the standby's engine and waits until it replicates its
// primary.
func (s *rdsStandby) replicate(instance RDSInstance) error {
	if err := s.engine.Ensure(); err != nil {
		return fmt.Errorf("start %s: %w", s.name, err)
	}
	var last rdsReplicationState
	err := rdsAwait(s.name+" replicating its primary", func() (bool, error) {
		state, err := rdsObserveReplication(s.engine.Engine, s.query(instance))
		if err != nil {
			return false, err
		}
		last = state
		return state.replicating, nil
	})
	if err != nil && last.message != "" {
		err = fmt.Errorf("%w: %s", err, last.message)
	}
	return err
}

// rdsReconcileStandby runs a standby for a Multi-AZ instance whose engine
// runs, removes the standby of an instance that is no longer Multi-AZ, and
// records the Availability Zone of the standby and the end of a conversion
// between Single-AZ and Multi-AZ. A stopped instance's standby stays as it is
// until the instance starts.
func rdsReconcileStandby(id string) error {
	instance, ok := rdsInstances.Get(id)
	if !ok {
		return nil
	}
	release := rdsStandbyLocks.Lock(instance.DbiResourceId)
	defer release()
	instance, ok = rdsInstances.Get(id)
	if !ok {
		return nil
	}
	if !rdsRunsEngine(instance) {
		zone := ""
		if instance.MultiAZ {
			zone = instance.SecondaryAvailabilityZone
			if zone == "" || zone == instance.AvailabilityZone {
				zone = rdsStandbyAvailabilityZone(instance.AvailabilityZone)
			}
		}
		rdsSettleStandby(instance, zone)
		return nil
	}
	if !instance.MultiAZ || rdsIsReadReplica(instance) || instance.BlueGreenDeploymentIdentifier != "" {
		err := rdsRemoveStandby(instance)
		rdsSettleStandby(instance, "")
		return err
	}
	plane, served := rdsLoadDataPlane(id)
	switch instance.DBInstanceStatus {
	case "deleting", "stopping", "stopped":
		return nil
	}
	if !served {
		return nil
	}
	if err := rdsProvideStandby(instance, plane); err != nil {
		return err
	}
	zone := instance.SecondaryAvailabilityZone
	if zone == "" || zone == instance.AvailabilityZone {
		zone = rdsStandbyAvailabilityZone(instance.AvailabilityZone)
	}
	rdsSettleStandby(instance, zone)
	return nil
}

// rdsSettleStandby records the standby's zone, and the end of a conversion
// the instance was going through.
func rdsSettleStandby(instance RDSInstance, zone string) {
	var finished *rdsEventKind
	rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) {
		if stored.DbiResourceId != instance.DbiResourceId {
			return
		}
		stored.SecondaryAvailabilityZone = zone
		if stored.AvailabilityConversion && stored.MultiAZ == instance.MultiAZ {
			stored.AvailabilityConversion = false
			finished = &rdsEventSingleAZConversionFinished
			if stored.MultiAZ {
				finished = &rdsEventMultiAZConversionFinished
			}
		}
	})
	if finished != nil {
		rdsRecordInstanceEvent(instance, *finished)
	}
}

// rdsProvideStandby brings the instance's standby up and makes the primary
// replicate to it synchronously. A standby whose engine is another version
// than the primary's, or whose data no longer replicates the primary, gives
// way to one seeded from a fresh capture of the primary's volume.
func rdsProvideStandby(instance RDSInstance, plane *rdsDataPlane) error {
	if err := plane.engine.Ensure(); err != nil {
		return fmt.Errorf("start the primary engine: %w", err)
	}
	engine := rdsStandbyEngine(plane.engine.Engine, instance)
	var standby *rdsStandby
	if held, ok := rdsStandbys.Load(instance.DbiResourceId); ok {
		standby, _ = held.(*rdsStandby)
		if standby != nil && standby.engine.Engine.Image != engine.Image {
			rdsDiscardStandbyEngine(instance, standby)
			standby = nil
		}
	}
	if standby == nil && sim.VolumeExists(rdsStandbyVolume(instance)) {
		adopted, err := rdsNewStandby(instance, engine)
		if err != nil {
			return err
		}
		if adopted.engine.Engine.Image == engine.Image {
			standby = adopted
			rdsStandbys.Store(instance.DbiResourceId, standby)
		}
	}
	if standby != nil {
		err := standby.replicate(instance)
		if err == nil {
			return rdsSynchronizeStandby(instance, plane, standby)
		}
		log.Printf("Amazon RDS %s: %v; building a new standby", instance.DBInstanceIdentifier, err)
		rdsDiscardStandbyEngine(instance, standby)
	}
	rdsRemoveEngineContainers("Amazon RDS "+instance.DBInstanceIdentifier+" standby", rdsStandbyLabels(instance))
	file, position, err := rdsSeedReplicaVolume(instance, plane.engine.Engine, rdsStandbyVolume(instance),
		rdsStandbyApplicationName, rdsStandbySlot(instance), instance.DBInstanceIdentifier+"-standby")
	if err != nil {
		return fmt.Errorf("seed the standby: %w", err)
	}
	if plugin := rdsSemiSyncReplicaLibrary(engine); plugin != "" {
		if _, err := rdsRunVolumeHelper(engine, rdsCopyEnginePluginScript, map[string]string{"PLUGIN": plugin}, rdsHelperSandbox,
			[]string{rdsStandbyVolume(instance) + ":" + engine.DataPath}, instance.DBInstanceIdentifier+"-standby"); err != nil {
			return fmt.Errorf("give the standby its semisynchronous replication plugin: %w", err)
		}
	}
	standby, err = rdsNewStandby(instance, engine)
	if err != nil {
		return err
	}
	standby.logFile, standby.logPos = file, position
	rdsStandbys.Store(instance.DbiResourceId, standby)
	if err := standby.replicate(instance); err != nil {
		return err
	}
	return rdsSynchronizeStandby(instance, plane, standby)
}

// rdsDiscardStandbyEngine stops and forgets a standby's engine and removes
// its volume.
func rdsDiscardStandbyEngine(instance RDSInstance, standby *rdsStandby) {
	rdsStandbys.CompareAndDelete(instance.DbiResourceId, standby)
	if err := standby.engine.Discard(); err != nil {
		log.Printf("Amazon RDS %s: discard the standby engine: %v", instance.DBInstanceIdentifier, err)
	}
	rdsRemoveEngineContainers("Amazon RDS "+instance.DBInstanceIdentifier+" standby", rdsStandbyLabels(instance))
	sim.RemoveVolumeSettled(rdsStandbyVolume(instance), "rds")
}

// rdsPostgresSynchronousStandbyScript sets a running PostgreSQL engine's
// synchronous_standby_names to $4, which an empty $4 resets, and returns once
// the engine has reloaded its configuration.
const rdsPostgresSynchronousStandbyScript = `set -e
cd "$1"
current=$(sed -n "s/^synchronous_standby_names = '\(.*\)'$/\1/p" postgresql.auto.conf)
[ "$current" = "$4" ] && exit 0
requested=$(psql -U "$2" -d "$3" -tAXc "SELECT now()")
if [ -n "$4" ]; then
	psql -U "$2" -d "$3" -tAXc "ALTER SYSTEM SET synchronous_standby_names = '$4'" > /dev/null
else
	psql -U "$2" -d "$3" -tAXc "ALTER SYSTEM RESET synchronous_standby_names" > /dev/null
fi
psql -U "$2" -d "$3" -tAXc "SELECT pg_reload_conf()" > /dev/null
until [ "$(psql -U "$2" -d "$3" -tAXc "SELECT pg_conf_load_time() >= '$requested'")" = t ]; do sleep 0.05; done
`

// rdsSetPostgresSynchronousStandby names the PostgreSQL primary's synchronous
// standby, or names none for an empty name.
func rdsSetPostgresSynchronousStandby(engine *dbengine.Instance, instance RDSInstance, name string) error {
	return engine.Exec([]string{"sh", "-c", rdsPostgresSynchronousStandbyScript, "sh", engine.Engine.DataPath,
		instance.MasterUsername, rdsDatabaseName(instance), name})
}

// suspendSynchronousStandby lets a PostgreSQL primary that starts commit
// without its standby, which reconnects only once the primary is on the
// replication network; rdsSynchronizeStandby names the standby again once it
// streams.
func (plane *rdsDataPlane) suspendSynchronousStandby() error {
	if plane.engine.Engine.Family != dbengine.Postgres {
		return nil
	}
	if err := rdsSetPostgresSynchronousStandby(plane.engine, plane.current(), ""); err != nil {
		return fmt.Errorf("suspend synchronous replication: %w", err)
	}
	return nil
}

// rdsCopyEnginePluginScript copies the engine image's own $PLUGIN library
// into the plugin directory the engine loads plugins from, which lives on the
// data volume beside AWSAuthenticationPlugin.
const rdsCopyEnginePluginScript = `set -e
dir=$(mysqld --no-defaults --verbose --help 2>/dev/null | awk '$1 == "plugin-dir" { print $2 }')
target="$DATA/` + rdsPluginDirName + `"
mkdir -p "$target"
cp "$dir/$PLUGIN" "$target/$PLUGIN.new"
chmod 644 "$target/$PLUGIN.new"
mv "$target/$PLUGIN.new" "$target/$PLUGIN"
`

// rdsSemiSyncReplicaLibrary is the library a MySQL standby loads its
// semisynchronous replica plugin from; MariaDB builds the plugin in.
func rdsSemiSyncReplicaLibrary(engine dbengine.Engine) string {
	if engine.Client == dbengine.MySQL80.Client {
		return "semisync_replica.so"
	}
	return ""
}

// rdsSemiSyncNames are the semisynchronous replication names of a MySQL or
// MariaDB engine: the source's plugin and its library, the variable that
// enables it, the status that counts its semisynchronous replicas, and the
// statement that restarts a replica's receiver.
func rdsSemiSyncNames(engine dbengine.Engine) (plugin, library, enabled, clients, restartReceiver string) {
	if engine.Client == dbengine.MariaDB114.Client {
		return "", "", "rpl_semi_sync_master_enabled", "Rpl_semi_sync_master_clients", "STOP SLAVE IO_THREAD; START SLAVE IO_THREAD"
	}
	return "rpl_semi_sync_source", "semisync_source.so", "rpl_semi_sync_source_enabled", "Rpl_semi_sync_source_clients",
		"STOP REPLICA IO_THREAD; START REPLICA IO_THREAD"
}

// rdsSemiSyncPluginInstalled reports whether a MySQL primary has its
// semisynchronous source plugin; MariaDB builds it in.
func rdsSemiSyncPluginInstalled(instance RDSInstance, engine dbengine.Engine) (bool, error) {
	plugin, _, _, _, _ := rdsSemiSyncNames(engine)
	if plugin == "" {
		return true, nil
	}
	output, err := rdsInstanceSQL(instance, engine,
		"SELECT COUNT(*) AS installed FROM information_schema.PLUGINS WHERE PLUGIN_NAME = "+dbengine.QuoteMySQLLiteral(plugin))
	if err != nil {
		return false, err
	}
	return rdsVerticalFields(output)["installed"] != "0", nil
}

// rdsSynchronizeStandby makes the primary wait for its standby on each
// commit and returns once it does.
func rdsSynchronizeStandby(instance RDSInstance, plane *rdsDataPlane, standby *rdsStandby) error {
	engine := plane.engine.Engine
	if engine.Family == dbengine.Postgres {
		if err := rdsSetPostgresSynchronousStandby(plane.engine, instance, rdsStandbyApplicationName); err != nil {
			return fmt.Errorf("name the synchronous standby: %w", err)
		}
		return rdsAwait("synchronous replication to "+standby.name, func() (bool, error) {
			output, err := rdsInstanceSQL(instance, engine, "SELECT count(*) FROM pg_stat_replication WHERE application_name = "+
				dbengine.QuoteLiteral(rdsStandbyApplicationName)+" AND sync_state = 'sync'")
			return strings.TrimSpace(output) == "1", err
		})
	}
	plugin, library, enabled, clients, restartReceiver := rdsSemiSyncNames(engine)
	installed, err := rdsSemiSyncPluginInstalled(instance, engine)
	if err != nil {
		return err
	}
	if !installed {
		if err := plane.engine.Exec([]string{"env", "DATA=" + engine.DataPath, "PLUGIN=" + library, "sh", "-c", rdsCopyEnginePluginScript}); err != nil {
			return fmt.Errorf("copy the semisynchronous source plugin: %w", err)
		}
		if _, err := rdsInstanceSQL(instance, engine, "INSTALL PLUGIN "+plugin+" SONAME "+dbengine.QuoteMySQLLiteral(library)); err != nil {
			return fmt.Errorf("install semisynchronous replication: %w", err)
		}
	}
	if _, err := rdsInstanceSQL(instance, engine, "SET GLOBAL "+enabled+" = ON"); err != nil {
		return fmt.Errorf("enable semisynchronous replication: %w", err)
	}
	// The primary's dump thread takes a replica as semisynchronous only when
	// the replica connects with the source side enabled.
	if _, err := standby.sql(instance, restartReceiver); err != nil {
		return fmt.Errorf("reconnect %s: %w", standby.name, err)
	}
	return rdsAwait("semisynchronous replication to "+standby.name, func() (bool, error) {
		output, err := rdsInstanceSQL(instance, engine, "SHOW GLOBAL STATUS LIKE "+dbengine.QuoteMySQLLiteral(clients))
		if err != nil {
			return false, err
		}
		count, _ := strconv.Atoi(rdsVerticalFields(output)["Value"])
		return count > 0, nil
	})
}

// rdsDesynchronizeStandby lets a running primary commit without its standby.
func rdsDesynchronizeStandby(instance RDSInstance) error {
	plane, served := rdsLoadDataPlane(instance.DBInstanceIdentifier)
	if !served || !plane.engine.Running() {
		return nil
	}
	engine := plane.engine.Engine
	if engine.Family == dbengine.Postgres {
		return rdsSetPostgresSynchronousStandby(plane.engine, instance, "")
	}
	installed, err := rdsSemiSyncPluginInstalled(instance, engine)
	if err != nil || !installed {
		return err
	}
	_, _, enabled, _, _ := rdsSemiSyncNames(engine)
	_, err = rdsInstanceSQL(instance, engine, "SET GLOBAL "+enabled+" = OFF")
	return err
}

// rdsRemoveStandby lets the primary commit without its standby, removes the
// standby's engine and volume, and drops the primary's slot for it.
func rdsRemoveStandby(instance RDSInstance) error {
	err := rdsDesynchronizeStandby(instance)
	if held, ok := rdsStandbys.Load(instance.DbiResourceId); ok {
		if standby, isStandby := held.(*rdsStandby); isStandby {
			rdsDiscardStandbyEngine(instance, standby)
		}
	}
	rdsRemoveEngineContainers("Amazon RDS "+instance.DBInstanceIdentifier+" standby", rdsStandbyLabels(instance))
	sim.RemoveVolumeSettled(rdsStandbyVolume(instance), "rds")
	if plane, served := rdsLoadDataPlane(instance.DBInstanceIdentifier); served {
		rdsDropReplicationSlotNamed(instance, plane.engine.Engine, rdsStandbySlot(instance))
	}
	return err
}

// rdsStopStandby stops a stopped instance's standby engine; its volume stays
// for the instance's next start.
func rdsStopStandby(instance RDSInstance) {
	release := rdsStandbyLocks.Lock(instance.DbiResourceId)
	defer release()
	if held, ok := rdsStandbys.Load(instance.DbiResourceId); ok {
		if standby, isStandby := held.(*rdsStandby); isStandby {
			if err := standby.engine.Close(); err != nil {
				log.Printf("Amazon RDS %s: stop the standby engine: %v", instance.DBInstanceIdentifier, err)
			}
		}
	}
}

// rdsDeleteStandby removes a deleted instance's standby.
func rdsDeleteStandby(instance RDSInstance) {
	release := rdsStandbyLocks.Lock(instance.DbiResourceId)
	defer release()
	if err := rdsRemoveStandby(instance); err != nil {
		log.Printf("Amazon RDS %s: remove the standby: %v", instance.DBInstanceIdentifier, err)
	}
}

// rdsReconcileStandbyInBackground reconciles the instance's standby after its
// primary engine started, logging a failure.
func rdsReconcileStandbyInBackground(id string) {
	bg.Go(func() {
		if err := rdsReconcileStandby(id); err != nil {
			log.Printf("Amazon RDS %s: Multi-AZ standby: %v", id, err)
		}
	})
}

// rdsBeginAvailabilityConversion starts converting the instance to Multi-AZ
// or to Single-AZ: an instance whose engine is serving stays modifying while
// its standby is built or removed.
func rdsBeginAvailabilityConversion(instance *RDSInstance, multiAZ bool) {
	instance.MultiAZ = multiAZ
	instance.AvailabilityConversion = true
	started := rdsEventSingleAZConversionStarted
	if multiAZ {
		started = rdsEventMultiAZConversionStarted
	}
	rdsRecordInstanceEvent(*instance, started)
	if _, served := rdsLoadDataPlane(instance.DBInstanceIdentifier); served && instance.DBInstanceStatus == "available" {
		instance.DBInstanceStatus = "modifying"
	}
}

// rdsConvertAvailability finishes a modifying instance's conversion and lands
// it available, or failed when its standby could not be built or removed.
func rdsConvertAvailability(id, resourceID string) {
	err := rdsReconcileStandby(id)
	status := "available"
	if err != nil {
		log.Printf("Amazon RDS %s: Multi-AZ conversion: %v", id, err)
		status = "failed"
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId == resourceID && stored.DBInstanceStatus == "modifying" && stored.Restart == nil {
			stored.DBInstanceStatus = status
		}
	})
}

// rdsRefuseFailover answers the error Amazon RDS answers a ForceFailover of
// an instance that is not Multi-AZ.
func rdsRefuseFailover(w http.ResponseWriter, r *http.Request, instance RDSInstance) bool {
	if instance.MultiAZ {
		return false
	}
	rdsErrorXML(w, "InvalidDBInstanceState",
		fmt.Sprintf("Cannot force a failover of DB instance %s because it is not a Multi-AZ DB instance.", instance.DBInstanceIdentifier),
		http.StatusBadRequest, sim.RequestID(r.Context()))
	return true
}

// rdsBeginFailover starts a RebootDBInstance with ForceFailover and returns
// the instance as the reboot leaves it. An instance whose engine the
// simulator does not run swaps its zones at once.
func rdsBeginFailover(instance RDSInstance) RDSInstance {
	id := instance.DBInstanceIdentifier
	if !rdsRunsEngine(instance) {
		rdsRecordInstanceEvent(instance, rdsEventFailoverStarted)
		rdsInstances.Update(id, func(stored *RDSInstance) {
			secondary := stored.SecondaryAvailabilityZone
			if secondary == "" {
				secondary = rdsStandbyAvailabilityZone(stored.AvailabilityZone)
			}
			stored.AvailabilityZone, stored.SecondaryAvailabilityZone = secondary, stored.AvailabilityZone
		})
		rdsRecordInstanceEvent(instance, rdsEventFailoverFinished)
		updated, _ := rdsInstances.Get(id)
		return updated
	}
	rdsInstances.Update(id, func(stored *RDSInstance) { stored.DBInstanceStatus = "rebooting" })
	resourceID := instance.DbiResourceId
	bg.Go(func() { rdsFailOver(id, resourceID) })
	updated, _ := rdsInstances.Get(id)
	return updated
}

// rdsFailOver promotes a rebooting Multi-AZ instance's standby, lands the
// instance available on it, and builds a new standby from it.
func rdsFailOver(id, resourceID string) {
	instance, ok := rdsInstances.Get(id)
	if !ok || instance.DbiResourceId != resourceID || instance.DBInstanceStatus != "rebooting" {
		return
	}
	release := rdsStandbyLocks.Lock(resourceID)
	rdsRecordInstanceEvent(instance, rdsEventFailoverStarted)
	err := rdsPromoteStandby(&instance)
	status := "available"
	if err != nil {
		log.Printf("Amazon RDS %s: Multi-AZ failover: %v", id, err)
		status = "failed"
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId != resourceID || stored.DBInstanceStatus != "rebooting" {
			return
		}
		stored.AvailabilityZone, stored.SecondaryAvailabilityZone = instance.AvailabilityZone, instance.SecondaryAvailabilityZone
		stored.Endpoint, stored.Port, stored.EngineParameters = instance.Endpoint, instance.Port, instance.EngineParameters
		stored.ServerCertificateValidTill = instance.ServerCertificateValidTill
		stored.DBInstanceStatus = status
	})
	release()
	if err != nil {
		return
	}
	rdsRecordInstanceEvent(instance, rdsEventFailoverFinished)
	rdsReconcileStandbyInBackground(id)
	rdsTakeFirstInstanceBackup(id)
}

// rdsPromoteStandby stops the primary, promotes the standby once it has
// applied everything it received, moves the standby's data onto the
// instance's volume, swaps the instance's zones and starts its engine on the
// promoted data. A promotion an earlier process left part done resumes from
// the standby's volume while it exists.
func rdsPromoteStandby(instance *RDSInstance) error {
	id := instance.DBInstanceIdentifier
	if err := rdsStopDataPlane(id, false); err != nil {
		return fmt.Errorf("stop the primary: %w", err)
	}
	if sim.RequireContainerRuntime("promoting an Amazon RDS standby") != nil || !sim.VolumeExists(rdsStandbyVolume(*instance)) {
		return errors.New("the instance has no standby to promote")
	}
	primary, ok := rdsInstanceEngine(*instance)
	if !ok {
		return fmt.Errorf("the simulator runs no %s %s engine", instance.Engine, instance.EngineVersion)
	}
	var standby *rdsStandby
	if held, found := rdsStandbys.Load(instance.DbiResourceId); found {
		standby, _ = held.(*rdsStandby)
	}
	if standby == nil {
		adopted, err := rdsNewStandby(*instance, rdsStandbyEngine(primary, *instance))
		if err != nil {
			return err
		}
		standby = adopted
	}
	if err := standby.engine.Ensure(); err != nil {
		return fmt.Errorf("start %s: %w", standby.name, err)
	}
	if err := rdsPromoteStandbyEngine(*instance, standby); err != nil {
		return err
	}
	rdsStandbys.CompareAndDelete(instance.DbiResourceId, standby)
	if err := standby.engine.Close(); err != nil {
		return fmt.Errorf("stop the promoted standby: %w", err)
	}
	if err := rdsMoveStandbyVolume(*instance); err != nil {
		return err
	}
	primaryZone := instance.AvailabilityZone
	if instance.SecondaryAvailabilityZone != "" {
		instance.AvailabilityZone = instance.SecondaryAvailabilityZone
	} else {
		instance.AvailabilityZone = rdsStandbyAvailabilityZone(primaryZone)
	}
	instance.SecondaryAvailabilityZone = primaryZone
	if err := rdsStartInstanceEngine(instance); err != nil {
		return fmt.Errorf("start the promoted engine: %w", err)
	}
	plane, served := rdsLoadDataPlane(id)
	if !served {
		return errors.New("the promoted engine serves no endpoint")
	}
	return plane.engine.Ensure()
}

// rdsPromoteStandbyEngine ends the standby's replication once it has applied
// everything it received: PostgreSQL promotes the hot standby, which replays
// the log it holds first, and MySQL and MariaDB stop the receiver, wait for
// the applier to reach the end of the relay log, and discard the replica
// configuration.
func rdsPromoteStandbyEngine(instance RDSInstance, standby *rdsStandby) error {
	engine := standby.engine.Engine
	if engine.Family == dbengine.Postgres {
		_, err := standby.sql(instance, "SELECT pg_promote(true, 60) WHERE pg_is_in_recovery()")
		if err != nil {
			return fmt.Errorf("promote %s: %w", standby.name, err)
		}
		return nil
	}
	stopReceiver, status, reset := "STOP REPLICA IO_THREAD", "SHOW REPLICA STATUS", "STOP REPLICA; RESET REPLICA ALL"
	received, read, applied, executed, applier := "Source_Log_File", "Read_Source_Log_Pos", "Relay_Source_Log_File", "Exec_Source_Log_Pos", "Replica_SQL_Running"
	if engine.Client == dbengine.MariaDB114.Client {
		stopReceiver, status, reset = "STOP SLAVE IO_THREAD", "SHOW SLAVE STATUS", "STOP SLAVE; RESET SLAVE ALL"
		received, read, applied, executed, applier = "Master_Log_File", "Read_Master_Log_Pos", "Relay_Master_Log_File", "Exec_Master_Log_Pos", "Slave_SQL_Running"
	}
	if _, err := standby.sql(instance, stopReceiver); err != nil {
		return fmt.Errorf("stop %s receiving: %w", standby.name, err)
	}
	err := rdsAwait(standby.name+" applying its relay log", func() (bool, error) {
		output, err := standby.sql(instance, status)
		if err != nil {
			return false, err
		}
		fields := rdsVerticalFields(output)
		if len(fields) == 0 {
			return true, nil
		}
		if failure := fields["Last_SQL_Error"]; failure != "" {
			return false, fmt.Errorf("the standby's applier failed: %s", failure)
		}
		if fields[applier] != "Yes" {
			return false, fmt.Errorf("the standby's applier is not running")
		}
		return fields[received] == fields[applied] && fields[read] == fields[executed], nil
	})
	if err != nil {
		return err
	}
	if _, err := standby.sql(instance, reset); err != nil {
		return fmt.Errorf("end %s's replication: %w", standby.name, err)
	}
	return nil
}

// rdsMoveStandbyVolume replaces the instance's volume with its promoted
// standby's.
func rdsMoveStandbyVolume(instance RDSInstance) error {
	id := instance.DBInstanceIdentifier
	release := rdsDataPlaneStops.Lock(id)
	defer release()
	rdsRemoveEngineContainers("Amazon RDS "+id, rdsEngineLabels(id))
	rdsRemoveEngineContainers("Amazon RDS "+id+" standby", rdsStandbyLabels(instance))
	volume := rdsInstanceVolume(id)
	sim.RemoveVolumeSettled(volume, "rds")
	if sim.VolumeExists(volume) {
		return fmt.Errorf("remove the former primary's volume %s", volume)
	}
	if _, err := sim.SnapshotVolume(context.Background(), rdsStandbyVolume(instance), volume); err != nil {
		return fmt.Errorf("move the promoted standby's data: %w", err)
	}
	sim.RemoveVolumeSettled(rdsStandbyVolume(instance), "rds")
	return nil
}
