package gcp_sdk_test

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// A Cloud SQL backup carries the data, and a restore returns to it.
//
// This is the property that separates a backup from a metadata row: rows
// written before the backup exist after instances.restoreBackup, and rows
// written after it do not. Both halves are asserted through a stock
// PostgreSQL driver against the instance's real data plane — the address in
// ipAddresses is a listener the simulator owns at PostgreSQL's own port
// (the Cloud SQL Admin API carries no port), the engine is a real
// PostgreSQL on a named volume, and the restore stops the engine, clones
// the backup volume back over the instance's, and boots on it.
//
// The declared user is real inside the engine too: the session runs as the
// user the client named, not as a shared superuser.
func TestCloudSQL_BackupCapturesDataAndRestoreReturnsToIt(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const (
		project      = "test-project"
		instanceName = "backup-data-pg"
		rootPassword = "RootPassword-123!"
		appUser      = "appuser"
		appPassword  = "AppPassword-123!"
		appDatabase  = "appdb"
	)

	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		RootPassword:    rootPassword,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	inst, err := svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, inst.IpAddresses)
	address := inst.IpAddresses[0].IpAddress
	if ip := net.ParseIP(address); ip == nil || !ip.IsLoopback() {
		// The data plane needs the engine's conventional port on a
		// per-instance loopback address. Linux provides loopback aliases
		// natively, so a modeled address there is a defect; macOS cannot
		// provide one without root — a host-kernel capability no test can
		// install.
		if runtime.GOOS == "linux" {
			t.Fatalf("instance reports %s: on Linux the data plane must install", address)
		}
		t.Skipf("host cannot provide a loopback alias at PostgreSQL's port (address %s, GOOS %s)", address, runtime.GOOS)
	}

	// The declared database and user become real inside the engine.
	_, err = svc.Databases.Insert(project, instanceName, &sqladmin.Database{Name: appDatabase}).Do()
	require.NoError(t, err)
	_, err = svc.Users.Insert(project, instanceName, &sqladmin.User{
		Name:     appUser,
		Password: appPassword,
	}).Do()
	require.NoError(t, err)

	connect := func() *pgx.Conn {
		config, parseErr := pgx.ParseConfig(fmt.Sprintf(
			"postgres://%s@%s:5432/%s?sslmode=prefer", appUser, address, appDatabase))
		require.NoError(t, parseErr)
		config.Password = appPassword
		var conn *pgx.Conn
		require.Eventually(t, func() bool {
			var connectErr error
			conn, connectErr = pgx.ConnectConfig(testContext, config)
			return connectErr == nil
		}, 3*time.Minute, 2*time.Second, "the data plane must accept the stock driver")
		return conn
	}

	source := connect()
	var sessionUser string
	require.NoError(t, source.QueryRow(testContext, `SELECT current_user`).Scan(&sessionUser))
	assert.Equal(t, appUser, sessionUser,
		"the session must run as the user the client named, not a shared superuser")
	_, err = source.Exec(testContext, `CREATE TABLE ledger (entry text)`)
	require.NoError(t, err)
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('before-backup')`)
	require.NoError(t, err)

	// Back up, and wait for the operation the way a real client does: the
	// BACKUP_VOLUME operation runs until the capture settles.
	backupOp, err := svc.BackupRuns.Insert(project, instanceName, &sqladmin.BackupRun{}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, backupOp.Name)

	runs, err := svc.BackupRuns.List(project, instanceName).Do()
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	require.Equal(t, "SUCCESSFUL", runs.Items[0].Status, "the capture must have settled successfully")
	backupRunID := runs.Items[0].Id

	// Rows written after the backup are the half a restore must NOT have.
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('after-backup')`)
	require.NoError(t, err)
	require.NoError(t, source.Close(testContext))

	restoreOp, err := svc.Instances.RestoreBackup(project, instanceName, &sqladmin.InstancesRestoreBackupRequest{
		RestoreBackupContext: &sqladmin.RestoreBackupContext{BackupRunId: backupRunID},
	}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, restoreOp.Name)

	restored := connect()
	defer func() { _ = restored.Close(context.Background()) }()
	rows, err := restored.Query(testContext, `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err, "the restored engine must serve the captured schema")
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	rows.Close()
	assert.Equal(t, []string{"before-backup"}, entries,
		"the restore must return to the backup: rows from before it, and none from after it")
}

// waitSQLOperationDone polls operations.get until the operation completes,
// failing on an operation that completed carrying an error — the same loop
// gcloud and terraform run against Cloud SQL.
func waitSQLOperationDone(t *testing.T, svc *sqladmin.Service, project, operation string) {
	t.Helper()
	require.Eventually(t, func() bool {
		op, err := svc.Operations.Get(project, operation).Do()
		if err != nil {
			return false
		}
		if op.Status != "DONE" {
			return false
		}
		require.Nil(t, op.Error, "operation %s must not fail: %+v", operation, op.Error)
		return true
	}, 3*time.Minute, 500*time.Millisecond, "operation %s must settle", operation)
}
