package gcp_sdk_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// sqlEngineAddress is the instance's PRIMARY address, which on Linux is the
// loopback address its data plane serves on.
func sqlEngineAddress(t *testing.T, svc *sqladmin.Service, project, instance string) string {
	t.Helper()
	inst, err := svc.Instances.Get(project, instance).Do()
	require.NoError(t, err)
	require.NotEmpty(t, inst.IpAddresses)
	address := inst.IpAddresses[0].IpAddress
	if ip := net.ParseIP(address); ip == nil || !ip.IsLoopback() {
		if runtime.GOOS == "linux" {
			t.Fatalf("instance reports %s: on Linux the data plane must install", address)
		}
		t.Skipf("host cannot provide a loopback alias at the engine's port (address %s, GOOS %s)", address, runtime.GOOS)
	}
	return address
}

func connectPostgres(ctx context.Context, t *testing.T, address, user, password, database string) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s:5432/%s?sslmode=prefer", user, address, database))
	require.NoError(t, err)
	config.Password = password
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err, "the data plane must accept the stock driver")
	return conn
}

func sqlWaitOperation(t *testing.T, svc *sqladmin.Service, project string, op *sqladmin.Operation) *sqladmin.Operation {
	t.Helper()
	waitSQLOperationDone(t, svc, project, op.Name)
	done, err := svc.Operations.Get(project, op.Name).Do()
	require.NoError(t, err)
	return done
}

// instances.restart stops the engine, which ends its sessions, and brings it up
// again before the RESTART operation is DONE; projects.instances.performDiskShrink
// measures the data the engine holds, lowers the provisioned storage and
// restarts the engine too.
func TestCloudSQL_RestartAndStorageShrinkRestartTheEngine(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const (
		project      = "test-project"
		instanceName = "lifecycle-restart-pg"
		rootPassword = "RootPassword-123!"
	)
	insertOp, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		RootPassword:    rootPassword,
		Settings:        &sqladmin.Settings{Tier: "db-custom-1-3840", DataDiskSizeGb: 20},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })
	waitSQLOperationDone(t, svc, project, insertOp.Name)
	address := sqlEngineAddress(t, svc, project, instanceName)

	startTime := func(conn *pgx.Conn) time.Time {
		var started time.Time
		require.NoError(t, conn.QueryRow(testContext, `SELECT pg_postmaster_start_time()`).Scan(&started))
		return started
	}
	before := connectPostgres(testContext, t, address, "postgres", rootPassword, "postgres")
	firstStart := startTime(before)

	restartOp, err := svc.Instances.Restart(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "RESTART", restartOp.OperationType)
	assert.Equal(t, "RUNNING", restartOp.Status, "the restart runs until the engine is back")
	done := sqlWaitOperation(t, svc, project, restartOp)
	require.Nil(t, done.Error)

	_, err = before.Exec(testContext, `SELECT 1`)
	require.Error(t, err, "the restart ends the sessions the engine held")
	after := connectPostgres(testContext, t, address, "postgres", rootPassword, "postgres")
	restarted := startTime(after)
	assert.True(t, restarted.After(firstStart), "the engine started again: %v, then %v", firstStart, restarted)
	require.NoError(t, after.Close(testContext))

	shrinkConfig, err := svc.Projects.Instances.GetDiskShrinkConfig(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "sql#getDiskShrinkConfig", shrinkConfig.Kind)
	assert.Equal(t, int64(10), shrinkConfig.MinimalTargetSizeGb,
		"a freshly initialised engine holds far less than the 10 GB Cloud SQL provisions at least")

	_, err = svc.Projects.Instances.PerformDiskShrink(project, instanceName, &sqladmin.PerformDiskShrinkContext{TargetSizeGb: 9}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "minimum of 10 GB", "a shrink below the minimal target size")
	_, err = svc.Projects.Instances.PerformDiskShrink(project, instanceName, &sqladmin.PerformDiskShrinkContext{TargetSizeGb: 25}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "must be smaller", "a shrink to a larger size")

	shrinkOp, err := svc.Projects.Instances.PerformDiskShrink(project, instanceName, &sqladmin.PerformDiskShrinkContext{TargetSizeGb: 12}).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", shrinkOp.Status)
	require.Nil(t, sqlWaitOperation(t, svc, project, shrinkOp).Error)
	inst, err := svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(12), inst.Settings.DataDiskSizeGb)

	shrunk := connectPostgres(testContext, t, address, "postgres", rootPassword, "postgres")
	assert.True(t, startTime(shrunk).After(restarted), "the instance restarts when the shrink completes")
	require.NoError(t, shrunk.Close(testContext))
}

func TestCloudSQL_StorageShrinkRefusesSharedCoreInstances(t *testing.T) {
	t.Parallel()
	svc := sqlAdminService(t)
	const project, instanceName = "test-project", "lifecycle-shared-core"
	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		Settings:        &sqladmin.Settings{Tier: "db-f1-micro", DataDiskSizeGb: 20},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	_, err = svc.Projects.Instances.GetDiskShrinkConfig(project, instanceName).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "shared-core", "getDiskShrinkConfig on db-f1-micro")
	_, err = svc.Projects.Instances.PerformDiskShrink(project, instanceName, &sqladmin.PerformDiskShrinkContext{TargetSizeGb: 15}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "shared-core", "performDiskShrink on db-f1-micro")
}

// instances.failover moves a REGIONAL instance's primary to its standby's
// zone, and only a REGIONAL one, at the settings version the caller read.
func TestCloudSQL_FailoverMovesTheInstanceToItsSecondaryZone(t *testing.T) {
	t.Parallel()
	svc := sqlAdminService(t)
	const project = "test-project"
	const regional, zonal = "lifecycle-ha", "lifecycle-zonal"
	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            regional,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		Settings: &sqladmin.Settings{
			Tier:               "db-custom-1-3840",
			AvailabilityType:   "REGIONAL",
			LocationPreference: &sqladmin.LocationPreference{Zone: "us-central1-a", SecondaryZone: "us-central1-c"},
		},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, regional).Do() })
	_, err = svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            zonal,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		Settings:        &sqladmin.Settings{Tier: "db-custom-1-3840"},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zonal).Do() })

	inst, err := svc.Instances.Get(project, regional).Do()
	require.NoError(t, err)
	assert.Equal(t, "us-central1-a", inst.GceZone)
	assert.Equal(t, "us-central1-c", inst.SecondaryGceZone)
	single, err := svc.Instances.Get(project, zonal).Do()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(single.GceZone, "us-central1-"), "Cloud SQL places a zonal instance in a zone of its region, not %q", single.GceZone)
	assert.Empty(t, single.SecondaryGceZone, "a zonal instance has no standby")

	_, err = svc.Instances.Failover(project, zonal, &sqladmin.InstancesFailoverRequest{
		FailoverContext: &sqladmin.FailoverContext{SettingsVersion: single.Settings.SettingsVersion},
	}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "high availability", "a failover of a zonal instance")
	_, err = svc.Instances.Failover(project, regional, &sqladmin.InstancesFailoverRequest{
		FailoverContext: &sqladmin.FailoverContext{SettingsVersion: inst.Settings.SettingsVersion + 7},
	}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "settings version", "a failover at a stale settings version")

	failover := func(settingsVersion int64) {
		op, err := svc.Instances.Failover(project, regional, &sqladmin.InstancesFailoverRequest{
			FailoverContext: &sqladmin.FailoverContext{SettingsVersion: settingsVersion},
		}).Do()
		require.NoError(t, err)
		assert.Equal(t, "FAILOVER", op.OperationType)
		require.Nil(t, sqlWaitOperation(t, svc, project, op).Error)
	}
	failover(inst.Settings.SettingsVersion)
	moved, err := svc.Instances.Get(project, regional).Do()
	require.NoError(t, err)
	assert.Equal(t, "us-central1-c", moved.GceZone, "the standby's zone serves after the failover")
	assert.Equal(t, "us-central1-a", moved.SecondaryGceZone, "the original zone holds the new standby")

	failover(moved.Settings.SettingsVersion)
	back, err := svc.Instances.Get(project, regional).Do()
	require.NoError(t, err)
	assert.Equal(t, "us-central1-a", back.GceZone, "a failback returns to the original zone")
	assert.Equal(t, "us-central1-c", back.SecondaryGceZone)
}

// instances.preCheckMajorVersionUpgrade reads every database of the engine: a
// view over pg_stat_activity is an ERROR finding until it is dropped.
func TestCloudSQL_PreCheckMajorVersionUpgradeReadsTheEngine(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const (
		project      = "test-project"
		instanceName = "lifecycle-precheck-pg"
		rootPassword = "RootPassword-123!"
	)
	insertOp, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		RootPassword:    rootPassword,
		Settings:        &sqladmin.Settings{Tier: "db-custom-1-3840"},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })
	waitSQLOperationDone(t, svc, project, insertOp.Name)
	address := sqlEngineAddress(t, svc, project, instanceName)

	precheck := func(target string) (*sqladmin.Operation, error) {
		return svc.Instances.PreCheckMajorVersionUpgrade(project, instanceName, &sqladmin.InstancesPreCheckMajorVersionUpgradeRequest{
			PreCheckMajorVersionUpgradeContext: &sqladmin.PreCheckMajorVersionUpgradeContext{TargetDatabaseVersion: target},
		}).Do()
	}
	_, err = precheck("POSTGRES_14")
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "later major version", "a precheck to an older version")
	_, err = precheck("MYSQL_8_0")
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "engine", "a precheck to another engine")

	conn := connectPostgres(testContext, t, address, "postgres", rootPassword, "postgres")
	defer conn.Close(testContext)
	_, err = conn.Exec(testContext, `CREATE VIEW my_stat_activity_view AS SELECT * FROM pg_stat_activity`)
	require.NoError(t, err)

	op, err := precheck("POSTGRES_16")
	require.NoError(t, err)
	assert.Equal(t, "PRE_CHECK_MAJOR_VERSION_UPGRADE", op.OperationType)
	done := sqlWaitOperation(t, svc, project, op)
	require.NotNil(t, done.PreCheckMajorVersionUpgradeContext)
	assert.Equal(t, "POSTGRES_16", done.PreCheckMajorVersionUpgradeContext.TargetDatabaseVersion)
	findings := done.PreCheckMajorVersionUpgradeContext.PreCheckResponse
	require.Len(t, findings, 1, "the view is the one finding: %+v", findings)
	assert.Equal(t, "ERROR", findings[0].MessageType)
	assert.Contains(t, findings[0].Message, "views that depend on functions returning data types of pg_stat_activity")
	assert.Contains(t, findings[0].Message, "(database: postgres, schema name: public, view name: my_stat_activity_view)")
	assert.NotEmpty(t, findings[0].ActionsRequired)

	_, err = conn.Exec(testContext, `DROP VIEW my_stat_activity_view`)
	require.NoError(t, err)
	op, err = precheck("POSTGRES_16")
	require.NoError(t, err)
	clean := sqlWaitOperation(t, svc, project, op)
	require.NotNil(t, clean.PreCheckMajorVersionUpgradeContext)
	assert.Empty(t, clean.PreCheckMajorVersionUpgradeContext.PreCheckResponse, "the instance is ready for the upgrade")
}

// instances.truncateLog empties a MySQL log table in the engine and refuses
// a PostgreSQL instance.
func TestCloudSQL_TruncateLogEmptiesTheMySQLLogTable(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const (
		project      = "test-project"
		instanceName = "lifecycle-truncate-mysql"
		postgresName = "lifecycle-truncate-pg"
		rootPassword = "RootPassword-123!"
	)
	insertOp, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "MYSQL_8_0",
		RootPassword:    rootPassword,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })
	waitSQLOperationDone(t, svc, project, insertOp.Name)
	_, err = svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name: postgresName, Region: "us-central1", DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, postgresName).Do() })

	truncate := func(instance, logType string) (*sqladmin.Operation, error) {
		return svc.Instances.TruncateLog(project, instance, &sqladmin.InstancesTruncateLogRequest{
			TruncateLogContext: &sqladmin.TruncateLogContext{LogType: logType},
		}).Do()
	}
	_, err = truncate(postgresName, "MYSQL_GENERAL_TABLE")
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "MySQL", "truncateLog on a PostgreSQL instance")
	_, err = truncate(instanceName, "MYSQL_ERROR_LOG")
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "logType", "truncateLog of an unknown log")

	address := sqlEngineAddress(t, svc, project, instanceName)
	config := mysql.NewConfig()
	config.User, config.Passwd = "root", rootPassword
	config.Net, config.Addr = "tcp", net.JoinHostPort(address, "3306")
	config.AllowCleartextPasswords = true
	connector, err := mysql.NewConnector(config)
	require.NoError(t, err)
	db := sql.OpenDB(connector)
	defer db.Close()
	require.NoError(t, db.PingContext(testContext))

	for _, logType := range []string{"MYSQL_GENERAL_TABLE", "MYSQL_SLOW_TABLE"} {
		op, err := truncate(instanceName, logType)
		require.NoError(t, err)
		assert.Equal(t, "TRUNCATE_LOG", op.OperationType)
		require.Nil(t, sqlWaitOperation(t, svc, project, op).Error, "TRUNCATE TABLE ran in the engine")
	}
	for _, table := range []string{"mysql.general_log", "mysql.slow_log"} {
		var rows int
		require.NoError(t, db.QueryRowContext(testContext, "SELECT COUNT(*) FROM "+table).Scan(&rows))
		assert.Zero(t, rows, "%s holds no rows after truncateLog", table)
	}
}

// The replication actions act on a read replica, demote on a stand-alone
// instance and a source representation instance, rescheduleMaintenance on
// announced maintenance: a stand-alone primary with none is refused each.
func TestCloudSQL_ReplicationActionsRefuseAStandalonePrimary(t *testing.T) {
	t.Parallel()
	svc := sqlAdminService(t)
	const project, instanceName = "test-project", "lifecycle-primary"
	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name: instanceName, Region: "us-central1", DatabaseVersion: "MYSQL_8_0",
		Settings: &sqladmin.Settings{Tier: "db-custom-1-3840"},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	notReplica := map[string]func() error{
		"startReplica": func() error { _, err := svc.Instances.StartReplica(project, instanceName).Do(); return err },
		"stopReplica":  func() error { _, err := svc.Instances.StopReplica(project, instanceName).Do(); return err },
		"promoteReplica": func() error {
			_, err := svc.Instances.PromoteReplica(project, instanceName).Do()
			return err
		},
		"switchover": func() error { _, err := svc.Instances.Switchover(project, instanceName).Do(); return err },
		"resetReplicaSize": func() error {
			_, err := svc.Projects.Instances.ResetReplicaSize(project, instanceName, &sqladmin.SqlInstancesResetReplicaSizeRequest{}).Do()
			return err
		},
		"startExternalSync": func() error {
			_, err := svc.Projects.Instances.StartExternalSync(project, instanceName, &sqladmin.SqlInstancesStartExternalSyncRequest{SyncMode: "ONLINE"}).Do()
			return err
		},
	}
	for action, call := range notReplica {
		requireGoogleAPIRefusal(t, call(), http.StatusBadRequest, "is not a read replica", action+" on a primary")
	}

	_, err = svc.Instances.Demote(project, instanceName, &sqladmin.InstancesDemoteRequest{
		DemoteContext: &sqladmin.DemoteContext{},
	}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "sourceRepresentativeInstanceName is required", "demote without a source")
	_, err = svc.Instances.Demote(project, instanceName, &sqladmin.InstancesDemoteRequest{
		DemoteContext: &sqladmin.DemoteContext{SourceRepresentativeInstanceName: instanceName},
	}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "source representation instance", "demote onto a Cloud SQL instance")
	_, err = svc.Instances.DemoteMaster(project, instanceName, &sqladmin.InstancesDemoteMasterRequest{
		DemoteMasterContext: &sqladmin.DemoteMasterContext{MasterInstanceName: "no-such-source"},
	}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "source representation instance", "demoteMaster onto a missing source")

	_, err = svc.Projects.Instances.RescheduleMaintenance(project, instanceName, &sqladmin.SqlInstancesRescheduleMaintenanceRequestBody{
		Reschedule: &sqladmin.Reschedule{RescheduleType: "SPECIFIC_TIME"},
	}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "scheduleTime", "a SPECIFIC_TIME reschedule without a time")
	_, err = svc.Projects.Instances.RescheduleMaintenance(project, instanceName, &sqladmin.SqlInstancesRescheduleMaintenanceRequestBody{
		Reschedule: &sqladmin.Reschedule{RescheduleType: "IMMEDIATE"},
	}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "no scheduled maintenance", "a reschedule with no maintenance scheduled")

	_, err = svc.Instances.Reencrypt(project, instanceName, &sqladmin.InstancesReencryptRequest{}).Do()
	requireGoogleAPIRefusal(t, err, http.StatusNotImplemented, "customer-managed Cloud KMS key", "reencrypt")

	_, err = svc.Instances.Restart(project, "no-such-instance").Do()
	requireGoogleAPIRefusal(t, err, http.StatusNotFound, "instance not found", "a restart of a missing instance")
}
