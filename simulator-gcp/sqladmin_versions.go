package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// sqlEngineVersion is a databaseVersion the simulator runs an engine for: the
// community release of that version, and the version with its minor that
// databaseInstalledVersion reports.
type sqlEngineVersion struct {
	DatabaseVersion  string
	InstalledVersion string
	Image            string
}

// sqlDefaultDatabaseVersion is the version instances.insert creates when the
// request names none.
const sqlDefaultDatabaseVersion = "POSTGRES_15"

// sqlEngineVersions lists each databaseVersion of the vendored enum that has
// a community image, at the newest release of the version, by its release tag
// as Amazon RDS's version table names its images; a MySQL 8.0
// version that names its minor runs that minor. MYSQL_5_1, POSTGRES_19 and
// POSTGRES_20 have no release image and run no engine.
// scripts/cloudsql-version-images-for.sh reads this table one row per line.
var sqlEngineVersions = []sqlEngineVersion{
	{"MYSQL_5_5", "MYSQL_5_5_62", "mirror.gcr.io/library/mysql:5.5.62"},
	{"MYSQL_5_6", "MYSQL_5_6_51", "mirror.gcr.io/library/mysql:5.6.51"},
	{"MYSQL_5_7", "MYSQL_5_7_44", "mirror.gcr.io/library/mysql:5.7.44"},
	{"MYSQL_8_0", "MYSQL_8_0_46", "mirror.gcr.io/library/mysql:8.0.46"},
	{"MYSQL_8_0_18", "MYSQL_8_0_18", "mirror.gcr.io/library/mysql:8.0.18"},
	{"MYSQL_8_0_26", "MYSQL_8_0_26", "mirror.gcr.io/library/mysql:8.0.26"},
	{"MYSQL_8_0_27", "MYSQL_8_0_27", "mirror.gcr.io/library/mysql:8.0.27"},
	{"MYSQL_8_0_28", "MYSQL_8_0_28", "mirror.gcr.io/library/mysql:8.0.28"},
	{"MYSQL_8_0_29", "MYSQL_8_0_29", "mirror.gcr.io/library/mysql:8.0.29"},
	{"MYSQL_8_0_30", "MYSQL_8_0_30", "mirror.gcr.io/library/mysql:8.0.30"},
	{"MYSQL_8_0_31", "MYSQL_8_0_31", "mirror.gcr.io/library/mysql:8.0.31"},
	{"MYSQL_8_0_32", "MYSQL_8_0_32", "mirror.gcr.io/library/mysql:8.0.32"},
	{"MYSQL_8_0_33", "MYSQL_8_0_33", "mirror.gcr.io/library/mysql:8.0.33"},
	{"MYSQL_8_0_34", "MYSQL_8_0_34", "mirror.gcr.io/library/mysql:8.0.34"},
	{"MYSQL_8_0_35", "MYSQL_8_0_35", "mirror.gcr.io/library/mysql:8.0.35"},
	{"MYSQL_8_0_36", "MYSQL_8_0_36", "mirror.gcr.io/library/mysql:8.0.36"},
	{"MYSQL_8_0_37", "MYSQL_8_0_37", "mirror.gcr.io/library/mysql:8.0.37"},
	{"MYSQL_8_0_39", "MYSQL_8_0_39", "mirror.gcr.io/library/mysql:8.0.39"},
	{"MYSQL_8_0_40", "MYSQL_8_0_40", "mirror.gcr.io/library/mysql:8.0.40"},
	{"MYSQL_8_0_41", "MYSQL_8_0_41", "mirror.gcr.io/library/mysql:8.0.41"},
	{"MYSQL_8_0_42", "MYSQL_8_0_42", "mirror.gcr.io/library/mysql:8.0.42"},
	{"MYSQL_8_0_43", "MYSQL_8_0_43", "mirror.gcr.io/library/mysql:8.0.43"},
	{"MYSQL_8_0_44", "MYSQL_8_0_44", "mirror.gcr.io/library/mysql:8.0.44"},
	{"MYSQL_8_0_45", "MYSQL_8_0_45", "mirror.gcr.io/library/mysql:8.0.45"},
	{"MYSQL_8_0_46", "MYSQL_8_0_46", "mirror.gcr.io/library/mysql:8.0.46"},
	{"MYSQL_8_4", "MYSQL_8_4_13", "mirror.gcr.io/library/mysql:8.4.13"},
	{"MYSQL_9_7", "MYSQL_9_7_4", "mirror.gcr.io/library/mysql:9.7.4"},
	{"POSTGRES_9_6", "POSTGRES_9_6_24", "mirror.gcr.io/library/postgres:9.6.24-alpine"},
	{"POSTGRES_10", "POSTGRES_10_23", "mirror.gcr.io/library/postgres:10.23-alpine"},
	{"POSTGRES_11", "POSTGRES_11_22", "mirror.gcr.io/library/postgres:11.22-alpine"},
	{"POSTGRES_12", "POSTGRES_12_22", "mirror.gcr.io/library/postgres:12.22-alpine"},
	{"POSTGRES_13", "POSTGRES_13_23", "mirror.gcr.io/library/postgres:13.23-alpine"},
	{"POSTGRES_14", "POSTGRES_14_24", "mirror.gcr.io/library/postgres:14.24-alpine"},
	{"POSTGRES_15", "POSTGRES_15_19", "mirror.gcr.io/library/postgres:15.19-alpine"},
	{"POSTGRES_16", "POSTGRES_16_15", "mirror.gcr.io/library/postgres:16.15-alpine"},
	{"POSTGRES_17", "POSTGRES_17_11", "mirror.gcr.io/library/postgres:17.11-alpine"},
	{"POSTGRES_18", "POSTGRES_18_6", "mirror.gcr.io/library/postgres:18.6-alpine"},
}

func sqlFindEngineVersion(databaseVersion string) (sqlEngineVersion, bool) {
	for _, version := range sqlEngineVersions {
		if version.DatabaseVersion == databaseVersion {
			return version, true
		}
	}
	return sqlEngineVersion{}, false
}

// sqlVersionFamily is the engine a databaseVersion names, whether or not the
// simulator runs it.
func sqlVersionFamily(databaseVersion string) (dbengine.Family, bool) {
	switch {
	case strings.HasPrefix(databaseVersion, "POSTGRES_"):
		return dbengine.Postgres, true
	case strings.HasPrefix(databaseVersion, "MYSQL_"):
		return dbengine.MySQL, true
	}
	return "", false
}

// release is the numeric release the installed version names:
// POSTGRES_15_19 is [15 19], MYSQL_8_0_46 is [8 0 46].
func (v sqlEngineVersion) release() []int {
	_, rest, _ := strings.Cut(v.InstalledVersion, "_")
	var release []int
	for _, part := range strings.Split(rest, "_") {
		n, _ := strconv.Atoi(part)
		release = append(release, n)
	}
	return release
}

// major is the release series a version belongs to: PostgreSQL's first
// number from 10 on, and the first two numbers before 10 and for MySQL.
func (v sqlEngineVersion) major() []int {
	release := v.release()
	if strings.HasPrefix(v.DatabaseVersion, "POSTGRES_") && release[0] >= 10 {
		return release[:1]
	}
	return release[:2]
}

// atLeast reports whether the version's release is at or after release.
func (v sqlEngineVersion) atLeast(release ...int) bool {
	return sqlCompareReleases(v.release(), release) >= 0
}

func sqlCompareReleases(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// sqlEngine is the engine an instance on version runs.
func sqlEngine(version sqlEngineVersion) dbengine.Engine {
	family, _ := sqlVersionFamily(version.DatabaseVersion)
	if family == dbengine.Postgres {
		return dbengine.Postgres16.WithImage(version.Image)
	}
	engine := dbengine.MySQL80.WithImage(version.Image)
	switch {
	case version.atLeast(9):
		// MySQL 9 removed mysql_native_password.
		engine.Args = nil
	case version.atLeast(8, 4):
		// MySQL 8.4 dropped --default-authentication-plugin and loads
		// mysql_native_password only on request, which accounts created on
		// 8.0 still use after an upgrade.
		engine.Args = []string{"--mysql-native-password=ON"}
	case !version.atLeast(8):
		engine.Args = nil
	}
	return engine
}

// sqlNextMySQLMajor is the consecutive major version each Cloud SQL for MySQL
// major upgrades to: MySQL upgrades one major version at a time.
var sqlNextMySQLMajor = map[string]string{"5.5": "5.6", "5.6": "5.7", "5.7": "8.0", "8.0": "8.4", "8.4": "9.7"}

// sqlCheckVersionChange answers why an instance on current cannot move to
// target through instances.patch, or "" when it can: the target is a later
// version of the same engine and, for MySQL, the next major version at most.
func sqlCheckVersionChange(current, target string) string {
	currentEngine, currentMajor := sqlMajorVersion(current)
	targetEngine, targetMajor := sqlMajorVersion(target)
	if currentEngine != targetEngine {
		return fmt.Sprintf("The database version %s is not a version of the instance's engine, which runs %s.", target, current)
	}
	from, fromOK := sqlFindEngineVersion(current)
	to, toOK := sqlFindEngineVersion(target)
	if fromOK && toOK && sqlCompareReleases(to.release(), from.release()) < 0 {
		return fmt.Sprintf("The database version %s must be later than the current version %s.", target, current)
	}
	if currentEngine == "MYSQL" && currentMajor != targetMajor && sqlNextMySQLMajor[currentMajor] != targetMajor {
		return fmt.Sprintf("MySQL upgrades only from one major version to the next: upgrade %s to MySQL %s first.", current, sqlNextMySQLMajor[currentMajor])
	}
	return ""
}

// sqlUpgradeInstanceEngine moves an instance's engine from one version to the
// next as Cloud SQL's in-place upgrade does: the instance goes unavailable, a
// pre-upgrade backup captures its volume, the target release takes over the
// data, and a post-upgrade backup follows. MySQL's server upgrades its data
// directory in place, with mysql_upgrade before 8.0.16; PostgreSQL's data
// moves through pg_dumpall into a fresh cluster of the target release. A
// failed upgrade restores the pre-upgrade backup and the earlier version.
func sqlUpgradeInstanceEngine(project, name string, from, to sqlEngineVersion) error {
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, name))
	if !ok {
		return fmt.Errorf("instance %s no longer exists", name)
	}
	plane, served := sqlLoadDataPlane(project, name)
	if !served {
		return nil
	}
	family, _ := sqlVersionFamily(from.DatabaseVersion)
	volume := sqlInstanceVolume(project, name)
	var dump []byte
	if family == dbengine.Postgres && sim.VolumeExists(volume) {
		var err error
		if dump, err = sqlEngineRun(context.Background(), plane, []string{"pg_dumpall", "--username=postgres"}, nil, nil); err != nil {
			return fmt.Errorf("dump the %s cluster: %w", from.DatabaseVersion, err)
		}
	}
	sqlStopDataPlane(project, name, false)
	backup, err := sqlTakeUpgradeBackup(project, name, from.DatabaseVersion,
		fmt.Sprintf("Pre-upgrade backup, %s to %s.", from.DatabaseVersion, to.DatabaseVersion))
	if err != nil {
		return errors.Join(fmt.Errorf("take the pre-upgrade backup: %w", err), sqlServeDataPlane(inst, sqlBuildDataPlane(project, name, from)))
	}
	if dump != nil {
		sim.RemoveVolumeSettled(volume, "cloudsql")
	}
	upgraded := sqlBuildDataPlane(project, name, to)
	if err := sqlBringUpUpgradedEngine(upgraded, from, to, dump); err != nil {
		if stopErr := upgraded.Stop(); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		if restoreErr := sqlRestoreVolume(project, name, sqlBackupRunVolume(project, name, backup)); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
		return errors.Join(fmt.Errorf("upgrade to %s: %w", to.DatabaseVersion, err), sqlServeDataPlane(inst, sqlBuildDataPlane(project, name, from)))
	}
	if err := sqlServeDataPlane(inst, upgraded); err != nil {
		return err
	}
	_, err = sqlTakeUpgradeBackup(project, name, to.DatabaseVersion,
		fmt.Sprintf("Post-upgrade backup, %s from %s.", to.DatabaseVersion, from.DatabaseVersion))
	return err
}

// sqlBringUpUpgradedEngine starts the target release on the instance's volume
// and carries the data over before the declared users and databases are
// reconciled into it.
func sqlBringUpUpgradedEngine(plane *sqlDataPlane, from, to sqlEngineVersion, dump []byte) error {
	reconcile := plane.Ready
	plane.Ready = nil
	if err := plane.Ensure(); err != nil {
		return fmt.Errorf("start the %s engine: %w", to.DatabaseVersion, err)
	}
	if dump != nil {
		// pg_dumpall creates every role it dumps, the bootstrap superuser
		// the target cluster already holds among them.
		dump = bytes.Replace(dump, []byte("\nCREATE ROLE postgres;\n"), []byte("\n"), 1)
		if _, err := sqlEngineRun(context.Background(), plane,
			[]string{"psql", "--no-psqlrc", "--quiet", "--set=ON_ERROR_STOP=1", "--username=postgres", "--dbname=postgres"},
			nil, bytes.NewReader(dump)); err != nil {
			return fmt.Errorf("restore the %s cluster: %w", from.DatabaseVersion, err)
		}
	}
	plane.Ready = reconcile
	if family, _ := sqlVersionFamily(to.DatabaseVersion); family == dbengine.MySQL && !to.atLeast(8, 0, 16) {
		env, err := sqlMySQLEnv(plane)
		if err != nil {
			return err
		}
		if _, err := sqlEngineRun(context.Background(), plane, []string{"mysql_upgrade", "--user=root"}, env, nil); err != nil {
			return fmt.Errorf("run mysql_upgrade: %w", err)
		}
		// mysql_upgrade's changes to the system tables take effect at the
		// next server start.
		if err := plane.Stop(); err != nil {
			return err
		}
		return plane.Ensure()
	}
	return reconcile()
}

// sqlServeDataPlane publishes plane as the instance's data plane on its
// recorded address.
func sqlServeDataPlane(inst SQLInstance, plane *sqlDataPlane) error {
	sqlDataPlanes.Store(sqlInstanceKey(inst.Project, inst.Name), plane)
	listener, err := sqlRebindAddress(inst, plane.version)
	if err != nil {
		return err
	}
	plane.Serve(listener)
	return nil
}

// sqlTakeUpgradeBackup records an on-demand backup run labelled as Cloud
// SQL labels its upgrade backups and captures the instance's volume into it.
func sqlTakeUpgradeBackup(project, instance, databaseVersion, description string) (int64, error) {
	id := time.Now().UTC().UnixNano()
	now := nowTimestamp()
	run := SQLBackupRun{
		Kind: "sql#backupRun", ID: id, Instance: instance, Description: description,
		Status: "RUNNING", EnqueuedTime: now, StartTime: now, Type: "ON_DEMAND",
		DatabaseVersion: databaseVersion,
		SelfLink: "https://sqladmin.googleapis.com/v1/projects/" + project + "/instances/" + instance +
			"/backupRuns/" + strconv.FormatInt(id, 10),
	}
	sqlBackupRuns.Put(sqlBackupRunKey(project, instance, id), run)
	err := sqlCaptureVolume(project, instance, sqlBackupRunVolume(project, instance, id))
	sqlBackupRuns.Update(sqlBackupRunKey(project, instance, id), func(b *SQLBackupRun) {
		b.Status, b.EndTime = "SUCCESSFUL", nowTimestamp()
		if err != nil {
			b.Status = "FAILED"
		}
	})
	return id, err
}

// sqlEarlierEngineVersions are the versions whose engine an earlier simulator
// ran for every instance of an engine, whatever databaseVersion it recorded.
var sqlEarlierEngineVersions = map[dbengine.Family]string{dbengine.Postgres: "POSTGRES_16", dbengine.MySQL: "MYSQL_8_0"}

// sqlMigrateEngineVersions fills databaseInstalledVersion on the instances an
// earlier simulator recorded without it. An instance whose volume that
// simulator's engine wrote takes the version of that engine, so the release
// that starts on the volume is one that reads it.
func sqlMigrateEngineVersions() {
	for _, inst := range sqlInstances.List() {
		if inst.DatabaseInstalledVersion != "" {
			continue
		}
		version := inst.DatabaseVersion
		if family, ok := sqlEngineFamily(version); ok && sim.RequireContainerRuntime("the Cloud SQL data plane") == nil &&
			sim.VolumeExists(sqlInstanceVolume(inst.Project, inst.Name)) {
			version = sqlEarlierEngineVersions[family]
		}
		installed, ok := sqlFindEngineVersion(version)
		if !ok {
			continue
		}
		sqlInstances.Update(sqlInstanceKey(inst.Project, inst.Name), func(stored *SQLInstance) {
			stored.DatabaseVersion, stored.DatabaseInstalledVersion = installed.DatabaseVersion, installed.InstalledVersion
		})
	}
}

// sqlVersionUpgrade is the engine upgrade an instances.patch or
// instances.update admitted, with the operation that reports it.
type sqlVersionUpgrade struct {
	op       SQLOperation
	from, to sqlEngineVersion
}

// sqlPlanVersionChange checks the databaseVersion an instances.patch or
// instances.update carries. A version that moves the instance onto another
// release returns the upgrade, its operation already RUNNING; one that does
// not returns nil, and the caller records it.
func sqlPlanVersionChange(w http.ResponseWriter, current SQLInstance, target string) (*sqlVersionUpgrade, bool) {
	if target == "" || target == current.DatabaseVersion {
		return nil, true
	}
	if reason := sqlCheckVersionChange(current.DatabaseVersion, target); reason != "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", reason)
		return nil, false
	}
	from, fromOK := sqlFindEngineVersion(current.DatabaseVersion)
	to, toOK := sqlFindEngineVersion(target)
	if !fromOK && !toOK {
		return nil, true
	}
	if !fromOK || !toOK {
		missing := current.DatabaseVersion
		if fromOK {
			missing = target
		}
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED",
			"the simulator serves no upgrade from %s to %s: it runs no engine for %s", current.DatabaseVersion, target, missing)
		return nil, false
	}
	if from.InstalledVersion == to.InstalledVersion {
		return nil, true
	}
	if !sqlRequireRunning(w, current) {
		return nil, false
	}
	opType := "UPDATE"
	if sqlCompareReleases(from.major(), to.major()) != 0 {
		opType = "MAJOR_VERSION_UPGRADE"
	}
	op, ok := sqlBeginInstanceOperation(w, current.Project, current.Name, opType)
	if !ok {
		return nil, false
	}
	return &sqlVersionUpgrade{op: op, from: from, to: to}, true
}

// start runs the upgrade in the background and answers its operation.
func (u *sqlVersionUpgrade) start(w http.ResponseWriter) {
	project, name := u.op.TargetProject, u.op.TargetID
	bg.Go(func() {
		err := sqlUpgradeInstanceEngine(project, name, u.from, u.to)
		if err == nil {
			sqlInstances.Update(sqlInstanceKey(project, name), func(i *SQLInstance) {
				i.DatabaseVersion, i.DatabaseInstalledVersion = u.to.DatabaseVersion, u.to.InstalledVersion
			})
		}
		sqlSettleOperation(project, u.op.Name, err)
	})
	sim.WriteJSON(w, http.StatusOK, u.op)
}
