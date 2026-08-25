package gcp_sdk_test

import (
	"context"
	"database/sql"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// The MySQL half of the Cloud SQL data plane, proven the same way as the
// PostgreSQL half: a MYSQL_8_0 instance serves a real MySQL engine at its
// PRIMARY address on MySQL's own port, the declared user is a real account
// inside it, a backup captures the data, and instances.restoreBackup returns
// to it — all through the stock go-sql-driver client.
func TestCloudSQL_MySQLBackupCapturesDataAndRestoreReturnsToIt(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const (
		project      = "test-project"
		instanceName = "backup-data-mysql"
		rootPassword = "RootPassword-123!"
		appUser      = "appuser"
		appPassword  = "AppPassword-123!"
		appDatabase  = "appdb"
	)

	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "MYSQL_8_0",
		RootPassword:    rootPassword,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	inst, err := svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, inst.IpAddresses)
	address := inst.IpAddresses[0].IpAddress
	if ip := net.ParseIP(address); ip == nil || !ip.IsLoopback() {
		if runtime.GOOS == "linux" {
			t.Fatalf("instance reports %s: on Linux the data plane must install", address)
		}
		t.Skipf("host cannot provide a loopback alias at MySQL's port (address %s, GOOS %s)", address, runtime.GOOS)
	}

	_, err = svc.Databases.Insert(project, instanceName, &sqladmin.Database{Name: appDatabase}).Do()
	require.NoError(t, err)
	_, err = svc.Users.Insert(project, instanceName, &sqladmin.User{
		Name:     appUser,
		Host:     "%",
		Password: appPassword,
	}).Do()
	require.NoError(t, err)

	open := func() *sql.DB {
		config := mysql.NewConfig()
		config.User = appUser
		config.Passwd = appPassword
		config.Net = "tcp"
		config.Addr = net.JoinHostPort(address, "3306")
		config.DBName = appDatabase
		config.AllowCleartextPasswords = true
		connector, connectorErr := mysql.NewConnector(config)
		require.NoError(t, connectorErr)
		db := sql.OpenDB(connector)
		require.Eventually(t, func() bool {
			return db.PingContext(testContext) == nil
		}, 5*time.Minute, 2*time.Second, "the data plane must accept the stock driver")
		return db
	}

	source := open()
	var sessionUser string
	require.NoError(t, source.QueryRowContext(testContext, `SELECT CURRENT_USER()`).Scan(&sessionUser))
	assert.Contains(t, sessionUser, appUser,
		"the session must run as the user the client named, not a shared superuser")
	_, err = source.ExecContext(testContext, `CREATE TABLE ledger (entry VARCHAR(64))`)
	require.NoError(t, err)
	_, err = source.ExecContext(testContext, `INSERT INTO ledger VALUES ('before-backup')`)
	require.NoError(t, err)

	backupOp, err := svc.BackupRuns.Insert(project, instanceName, &sqladmin.BackupRun{}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, backupOp.Name)
	runs, err := svc.BackupRuns.List(project, instanceName).Do()
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	require.Equal(t, "SUCCESSFUL", runs.Items[0].Status)

	_, err = source.ExecContext(testContext, `INSERT INTO ledger VALUES ('after-backup')`)
	require.NoError(t, err)
	require.NoError(t, source.Close())

	restoreOp, err := svc.Instances.RestoreBackup(project, instanceName, &sqladmin.InstancesRestoreBackupRequest{
		RestoreBackupContext: &sqladmin.RestoreBackupContext{BackupRunId: runs.Items[0].Id},
	}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, restoreOp.Name)

	restored := open()
	defer restored.Close()
	rows, err := restored.QueryContext(testContext, `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err, "the restored engine must serve the captured schema")
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	require.NoError(t, rows.Close())
	assert.Equal(t, []string{"before-backup"}, entries,
		"the restore must return to the backup: rows from before it, and none from after it")
}
