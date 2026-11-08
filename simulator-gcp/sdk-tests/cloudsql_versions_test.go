package gcp_sdk_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// The engine images Cloud SQL runs for the versions these tests create, as
// the simulator's version table pins them.
const (
	cloudSQLPostgres15Image = "mirror.gcr.io/library/postgres:15.19-alpine"
	cloudSQLPostgres16Image = "mirror.gcr.io/library/postgres:16.15-alpine"
	cloudSQLMySQL80Image    = "mirror.gcr.io/library/mysql:8.0.46"
	cloudSQLMySQL84Image    = "mirror.gcr.io/library/mysql:8.4.13"
)

// sqlDataPlaneAddress reads the instance's PRIMARY address, which must be a
// loopback address the data plane serves.
func sqlDataPlaneAddress(t *testing.T, svc *sqladmin.Service, project, instance string) string {
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

// A POSTGRES_15 instance runs PostgreSQL 15, not another release, and reports
// the minor it runs in databaseInstalledVersion. instances.patch with
// POSTGRES_16 upgrades it in place: the data moves to PostgreSQL 16, the
// instance keeps its address, and Cloud SQL's labelled pre- and post-upgrade
// backups appear among its backup runs. A downgrade is refused.
func TestCloudSQL_PostgresVersionRunsItsReleaseAndUpgradesInPlace(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const (
		project      = "test-project"
		instanceName = "version-pg"
		rootPassword = "RootPassword-123!"
	)

	insertOp, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		RootPassword:    rootPassword,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })
	waitSQLOperationDone(t, svc, project, insertOp.Name)

	inst, err := svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_15_19", inst.DatabaseInstalledVersion)
	address := sqlDataPlaneAddress(t, svc, project, instanceName)

	connect := func() *pgx.Conn {
		config, parseErr := pgx.ParseConfig(fmt.Sprintf("postgres://postgres@%s:5432/postgres?sslmode=prefer", address))
		require.NoError(t, parseErr)
		config.Password = rootPassword
		conn, connectErr := pgx.ConnectConfig(testContext, config)
		require.NoError(t, connectErr)
		return conn
	}
	serverVersion := func(conn *pgx.Conn) string {
		var version string
		require.NoError(t, conn.QueryRow(testContext, `SHOW server_version`).Scan(&version))
		return version
	}

	conn := connect()
	assert.Equal(t, "15.19", serverVersion(conn), "POSTGRES_15 runs PostgreSQL 15")
	_, err = conn.Exec(testContext, `CREATE TABLE ledger (entry text); INSERT INTO ledger VALUES ('written-on-15')`)
	require.NoError(t, err)
	require.NoError(t, conn.Close(testContext))

	upgradeOp, err := svc.Instances.Patch(project, instanceName, &sqladmin.DatabaseInstance{DatabaseVersion: "POSTGRES_16"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "MAJOR_VERSION_UPGRADE", upgradeOp.OperationType)
	waitSQLOperationDone(t, svc, project, upgradeOp.Name)

	inst, err = svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_16", inst.DatabaseVersion)
	assert.Equal(t, "POSTGRES_16_15", inst.DatabaseInstalledVersion)
	assert.Equal(t, address, inst.IpAddresses[0].IpAddress, "an in-place upgrade keeps the instance's address")

	conn = connect()
	defer func() { _ = conn.Close(testContext) }()
	assert.Equal(t, "16.15", serverVersion(conn), "the upgraded instance runs PostgreSQL 16")
	var entry string
	require.NoError(t, conn.QueryRow(testContext, `SELECT entry FROM ledger`).Scan(&entry))
	assert.Equal(t, "written-on-15", entry, "the upgrade carries the data to the new major version")

	runs, err := svc.BackupRuns.List(project, instanceName).Do()
	require.NoError(t, err)
	descriptions := map[string]string{}
	for _, run := range runs.Items {
		descriptions[run.Description] = run.DatabaseVersion
		assert.Equal(t, "SUCCESSFUL", run.Status)
	}
	assert.Equal(t, map[string]string{
		"Pre-upgrade backup, POSTGRES_15 to POSTGRES_16.":    "POSTGRES_15",
		"Post-upgrade backup, POSTGRES_16 from POSTGRES_15.": "POSTGRES_16",
	}, descriptions)

	_, err = svc.Instances.Patch(project, instanceName, &sqladmin.DatabaseInstance{DatabaseVersion: "POSTGRES_15"}).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code, "Cloud SQL upgrades and never downgrades")
}

// A MYSQL_8_4 instance runs MySQL 8.4 and a MYSQL_8_0_31 instance the 8.0.31
// release its name carries, each reporting the version through the server
// itself; MySQL refuses to skip a major version.
func TestCloudSQL_MySQLVersionRunsItsRelease(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const (
		project      = "test-project"
		instanceName = "version-mysql"
		rootPassword = "RootPassword-123!"
	)

	insertOp, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "MYSQL_8_4",
		RootPassword:    rootPassword,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })
	waitSQLOperationDone(t, svc, project, insertOp.Name)

	inst, err := svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "MYSQL_8_4_13", inst.DatabaseInstalledVersion)
	address := sqlDataPlaneAddress(t, svc, project, instanceName)

	config := mysql.NewConfig()
	config.User = "root"
	config.Passwd = rootPassword
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(address, "3306")
	config.AllowCleartextPasswords = true
	connector, err := mysql.NewConnector(config)
	require.NoError(t, err)
	db := sql.OpenDB(connector)
	defer db.Close()
	var version string
	require.NoError(t, db.QueryRowContext(testContext, `SELECT VERSION()`).Scan(&version))
	assert.Equal(t, "8.4.13", version, "MYSQL_8_4 runs MySQL 8.4")

	_, err = svc.Instances.Patch(project, instanceName, &sqladmin.DatabaseInstance{DatabaseVersion: "MYSQL_5_7"}).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code, "Cloud SQL upgrades and never downgrades")

	skipping := "version-mysql-57"
	skipOp, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name: skipping, Region: "us-central1", DatabaseVersion: "MYSQL_5_7", RootPassword: rootPassword,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, skipping).Do() })
	waitSQLOperationDone(t, svc, project, skipOp.Name)
	_, err = svc.Instances.Patch(project, skipping, &sqladmin.DatabaseInstance{DatabaseVersion: "MYSQL_8_4"}).Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code, "MySQL upgrades one major version at a time")
	assert.Contains(t, apiErr.Message, "MYSQL_5_7")
}
