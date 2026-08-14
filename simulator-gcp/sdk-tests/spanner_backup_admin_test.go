package gcp_sdk_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	spanneradmin "google.golang.org/api/spanner/v1"
)

// spannerAdminService builds the official Google Cloud Spanner REST client
// against the simulator — same client, same resource names, only the endpoint
// coordinate differs from a real project.
func spannerAdminService(t *testing.T, ctx context.Context) *spanneradmin.Service {
	t.Helper()
	svc, err := spanneradmin.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// spannerWriteRows commits rows into a table through the session data plane —
// a read-write transaction, INSERT DML, and Commit — so the rows exist in the
// database's real backing engine and not in any admin-side record.
func spannerWriteRows(t *testing.T, svc *spanneradmin.Service, database string, statements ...string) {
	t.Helper()
	session, err := svc.Projects.Instances.Databases.Sessions.Create(database, &spanneradmin.CreateSessionRequest{}).Do()
	require.NoError(t, err)
	defer func() {
		_, err := svc.Projects.Instances.Databases.Sessions.Delete(session.Name).Do()
		require.NoError(t, err)
	}()
	txn, err := svc.Projects.Instances.Databases.Sessions.BeginTransaction(session.Name, &spanneradmin.BeginTransactionRequest{
		Options: &spanneradmin.TransactionOptions{ReadWrite: &spanneradmin.ReadWrite{}},
	}).Do()
	require.NoError(t, err)
	for i, stmt := range statements {
		_, err = svc.Projects.Instances.Databases.Sessions.ExecuteSql(session.Name, &spanneradmin.ExecuteSqlRequest{
			Sql:         stmt,
			Seqno:       int64(i + 1),
			Transaction: &spanneradmin.TransactionSelector{Id: txn.Id},
		}).Do()
		require.NoError(t, err, "DML %q", stmt)
	}
	_, err = svc.Projects.Instances.Databases.Sessions.Commit(session.Name, &spanneradmin.CommitRequest{TransactionId: txn.Id}).Do()
	require.NoError(t, err)
}

// spannerQueryRows runs a query through the session data plane and returns the
// rows the SQL engine produced.
func spannerQueryRows(t *testing.T, svc *spanneradmin.Service, database, query string) [][]interface{} {
	t.Helper()
	session, err := svc.Projects.Instances.Databases.Sessions.Create(database, &spanneradmin.CreateSessionRequest{}).Do()
	require.NoError(t, err)
	defer func() {
		_, err := svc.Projects.Instances.Databases.Sessions.Delete(session.Name).Do()
		require.NoError(t, err)
	}()
	result, err := svc.Projects.Instances.Databases.Sessions.ExecuteSql(session.Name, &spanneradmin.ExecuteSqlRequest{Sql: query}).Do()
	require.NoError(t, err)
	return result.Rows
}

// TestSpanner_BackupRestoreCrossPlane is the decisive cross-plane test: a
// database created through the admin API is written to through the data plane,
// backed up, dropped, restored from a copy of that backup, and read again
// through the data plane. The restored database must produce the same rows,
// which is only possible if the backup captured the database's real bytes.
func TestSpanner_BackupRestoreCrossPlane(t *testing.T) {
	ctx := context.Background()
	svc := spannerAdminService(t, ctx)

	const (
		project  = "test-project"
		instance = "backup-cross"
	)
	instanceName := fmt.Sprintf("projects/%s/instances/%s", project, instance)
	databaseName := instanceName + "/databases/ledger"

	op, err := svc.Projects.Instances.Create("projects/"+project, &spanneradmin.CreateInstanceRequest{
		InstanceId: instance,
		Instance:   &spanneradmin.Instance{DisplayName: instance, NodeCount: 1},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	// The create request's extraStatements are the database's initial schema:
	// they must build tables the data plane can query immediately.
	createOp, err := svc.Projects.Instances.Databases.Create(instanceName, &spanneradmin.CreateDatabaseRequest{
		CreateStatement: "CREATE DATABASE `ledger`",
		ExtraStatements: []string{
			"CREATE TABLE Entries (EntryId STRING(36) NOT NULL, Amount INT64 NOT NULL, Memo STRING(MAX)) PRIMARY KEY (EntryId)",
			"CREATE INDEX EntriesByAmount ON Entries(Amount)",
		},
	}).Do()
	require.NoError(t, err)
	require.True(t, createOp.Done)
	require.Nil(t, createOp.Error, "CreateDatabase reported %v", createOp.Error)

	ddl, err := svc.Projects.Instances.Databases.GetDdl(databaseName).Do()
	require.NoError(t, err)
	require.Len(t, ddl.Statements, 2, "GetDatabaseDdl reports the schema the create statement built")
	assert.Contains(t, ddl.Statements[0], "CREATE TABLE Entries")

	spannerWriteRows(t, svc, databaseName,
		"INSERT INTO Entries (EntryId, Amount, Memo) VALUES ('e1', 100, 'opening')",
		"INSERT INTO Entries (EntryId, Amount, Memo) VALUES ('e2', 250, 'invoice')",
		"INSERT INTO Entries (EntryId, Amount, Memo) VALUES ('e3', 375, 'refund')",
	)
	before := spannerQueryRows(t, svc, databaseName, "SELECT EntryId, Amount, Memo FROM Entries ORDER BY EntryId")
	require.Len(t, before, 3, "the table extraStatements created holds the rows the data plane wrote")

	expire := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339Nano)
	backupOp, err := svc.Projects.Instances.Backups.Create(instanceName, &spanneradmin.Backup{
		Database:   databaseName,
		ExpireTime: expire,
	}).BackupId("ledger-daily").Do()
	require.NoError(t, err)
	require.True(t, backupOp.Done)
	require.Nil(t, backupOp.Error, "CreateBackup reported %v", backupOp.Error)

	backup, err := svc.Projects.Instances.Backups.Get(instanceName + "/backups/ledger-daily").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", backup.State)
	assert.Equal(t, databaseName, backup.Database)
	assert.Positive(t, backup.SizeBytes, "the backup recorded the size of the bytes it captured")

	backups, err := svc.Projects.Instances.Backups.List(instanceName).Do()
	require.NoError(t, err)
	require.Len(t, backups.Backups, 1)

	// The copy must carry the captured bytes, not just the metadata: the
	// restore below runs from the copy.
	copyOp, err := svc.Projects.Instances.Backups.Copy(instanceName, &spanneradmin.CopyBackupRequest{
		BackupId:     "ledger-daily-copy",
		SourceBackup: backup.Name,
		ExpireTime:   expire,
	}).Do()
	require.NoError(t, err)
	require.True(t, copyOp.Done)
	require.Nil(t, copyOp.Error, "CopyBackup reported %v", copyOp.Error)
	copied, err := svc.Projects.Instances.Backups.Get(instanceName + "/backups/ledger-daily-copy").Do()
	require.NoError(t, err)
	assert.Equal(t, backup.SizeBytes, copied.SizeBytes)

	newExpire := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339Nano)
	patched, err := svc.Projects.Instances.Backups.Patch(backup.Name, &spanneradmin.Backup{ExpireTime: newExpire}).
		UpdateMask("expireTime").Do()
	require.NoError(t, err)
	assert.Equal(t, newExpire, patched.ExpireTime)

	// Dropping the database really removes it: the data plane can no longer
	// open a session on it.
	_, err = svc.Projects.Instances.Databases.DropDatabase(databaseName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Databases.Sessions.Create(databaseName, &spanneradmin.CreateSessionRequest{}).Do()
	require.Error(t, err, "the dropped database no longer serves the data plane")

	restoreOp, err := svc.Projects.Instances.Databases.Restore(instanceName, &spanneradmin.RestoreDatabaseRequest{
		DatabaseId: "ledger",
		Backup:     copied.Name,
	}).Do()
	require.NoError(t, err)
	require.True(t, restoreOp.Done)
	require.Nil(t, restoreOp.Error, "RestoreDatabase reported %v", restoreOp.Error)

	restored, err := svc.Projects.Instances.Databases.Get(databaseName).Do()
	require.NoError(t, err)
	require.NotNil(t, restored.RestoreInfo)
	assert.Equal(t, "BACKUP", restored.RestoreInfo.SourceType)
	require.NotNil(t, restored.RestoreInfo.BackupInfo)
	assert.Equal(t, copied.Name, restored.RestoreInfo.BackupInfo.Backup)
	assert.Equal(t, databaseName, restored.RestoreInfo.BackupInfo.SourceDatabase)

	after := spannerQueryRows(t, svc, databaseName, "SELECT EntryId, Amount, Memo FROM Entries ORDER BY EntryId")
	require.Equal(t, before, after, "the restore brought back the exact rows the backup captured")

	// The restored schema is queryable as schema, not just as rows: the index
	// the create statement built came back with it, and new writes land in it.
	restoredDdl, err := svc.Projects.Instances.Databases.GetDdl(databaseName).Do()
	require.NoError(t, err)
	assert.Equal(t, ddl.Statements, restoredDdl.Statements)
	spannerWriteRows(t, svc, databaseName, "INSERT INTO Entries (EntryId, Amount, Memo) VALUES ('e4', 500, 'topup')")
	assert.Len(t, spannerQueryRows(t, svc, databaseName, "SELECT EntryId FROM Entries"), 4)

	// The operations the backup and restore recorded show up in the instance's
	// per-kind operation collections.
	backupOps, err := svc.Projects.Instances.BackupOperations.List(instanceName).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(backupOps.Operations), 2, "CreateBackup and CopyBackup are backup operations")
	databaseOps, err := svc.Projects.Instances.DatabaseOperations.List(instanceName).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, databaseOps.Operations, "CreateDatabase and RestoreDatabase are database operations")

	_, err = svc.Projects.Instances.Backups.Delete(backup.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Backups.Get(backup.Name).Do()
	require.Error(t, err, "the deleted backup is gone")
}

// TestSpanner_DatabaseAdminSurface covers the rest of the administrative
// surface against the same real state: drop protection, database roles derived
// from the database's own DDL, the IAM triple, backup schedules and the
// operations collections.
func TestSpanner_DatabaseAdminSurface(t *testing.T) {
	ctx := context.Background()
	svc := spannerAdminService(t, ctx)

	const (
		project  = "test-project"
		instance = "admin-surface"
	)
	instanceName := fmt.Sprintf("projects/%s/instances/%s", project, instance)
	databaseName := instanceName + "/databases/catalog"

	op, err := svc.Projects.Instances.Create("projects/"+project, &spanneradmin.CreateInstanceRequest{
		InstanceId: instance,
		Instance:   &spanneradmin.Instance{DisplayName: instance, NodeCount: 1},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	createOp, err := svc.Projects.Instances.Databases.Create(instanceName, &spanneradmin.CreateDatabaseRequest{
		CreateStatement: "CREATE DATABASE `catalog`",
		ExtraStatements: []string{
			"CREATE TABLE Products (Sku STRING(36) NOT NULL, Title STRING(MAX)) PRIMARY KEY (Sku)",
		},
	}).Do()
	require.NoError(t, err)
	require.Nil(t, createOp.Error)

	// A CreateDatabase whose initial schema cannot be applied leaves no
	// database behind — the operation reports the failure instead.
	badOp, err := svc.Projects.Instances.Databases.Create(instanceName, &spanneradmin.CreateDatabaseRequest{
		CreateStatement: "CREATE DATABASE `broken`",
		ExtraStatements: []string{"ALTER TABLE Products ALTER COLUMN Title STRING(50)"},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, badOp.Error, "an unapplicable initial schema fails the create operation")
	_, err = svc.Projects.Instances.Databases.Get(instanceName + "/databases/broken").Do()
	require.Error(t, err, "the failed create left no database behind")

	// UpdateDatabase's drop protection is enforced by DropDatabase.
	protectOp, err := svc.Projects.Instances.Databases.Patch(databaseName, &spanneradmin.Database{
		EnableDropProtection: true,
	}).UpdateMask("enableDropProtection").Do()
	require.NoError(t, err)
	require.True(t, protectOp.Done)
	protected, err := svc.Projects.Instances.Databases.Get(databaseName).Do()
	require.NoError(t, err)
	require.True(t, protected.EnableDropProtection)
	_, err = svc.Projects.Instances.Databases.DropDatabase(databaseName).Do()
	require.Error(t, err, "drop protection blocks DropDatabase")
	_, err = svc.Projects.Instances.Databases.Patch(databaseName, &spanneradmin.Database{
		EnableDropProtection: false,
	}).UpdateMask("enableDropProtection").Do()
	require.NoError(t, err)

	// Database roles come out of the database's own fine-grained access
	// control DDL, not from a parallel record.
	roles, err := svc.Projects.Instances.Databases.DatabaseRoles.List(databaseName).Do()
	require.NoError(t, err)
	require.Len(t, roles.DatabaseRoles, 1)
	assert.Equal(t, databaseName+"/databaseRoles/public", roles.DatabaseRoles[0].Name)

	roleOp, err := svc.Projects.Instances.Databases.UpdateDdl(databaseName, &spanneradmin.UpdateDatabaseDdlRequest{
		Statements: []string{"CREATE ROLE analyst"},
	}).Do()
	require.NoError(t, err)
	require.Nil(t, roleOp.Error, "CREATE ROLE reported %v", roleOp.Error)
	roles, err = svc.Projects.Instances.Databases.DatabaseRoles.List(databaseName).Do()
	require.NoError(t, err)
	require.Len(t, roles.DatabaseRoles, 2)
	assert.Equal(t, databaseName+"/databaseRoles/analyst", roles.DatabaseRoles[0].Name)

	permissions, err := svc.Projects.Instances.Databases.DatabaseRoles.TestIamPermissions(
		databaseName+"/databaseRoles/analyst",
		&spanneradmin.TestIamPermissionsRequest{Permissions: []string{"spanner.databases.select"}}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"spanner.databases.select"}, permissions.Permissions)

	dropRoleOp, err := svc.Projects.Instances.Databases.UpdateDdl(databaseName, &spanneradmin.UpdateDatabaseDdlRequest{
		Statements: []string{"DROP ROLE analyst"},
	}).Do()
	require.NoError(t, err)
	require.Nil(t, dropRoleOp.Error)
	roles, err = svc.Projects.Instances.Databases.DatabaseRoles.List(databaseName).Do()
	require.NoError(t, err)
	require.Len(t, roles.DatabaseRoles, 1, "DROP ROLE removed the role from the database")

	// Role DDL never disturbs the SQL schema.
	spannerWriteRows(t, svc, databaseName, "INSERT INTO Products (Sku, Title) VALUES ('sku-1', 'Widget')")
	require.Len(t, spannerQueryRows(t, svc, databaseName, "SELECT Sku FROM Products"), 1)

	// The IAM triple round-trips on the database and on the instance.
	for _, resource := range []string{databaseName, instanceName} {
		set, err := svc.Projects.Instances.Databases.SetIamPolicy(resource, &spanneradmin.SetIamPolicyRequest{
			Policy: &spanneradmin.Policy{Bindings: []*spanneradmin.Binding{{
				Role:    "roles/spanner.databaseReader",
				Members: []string{"user:reader@example.com"},
			}}},
		}).Do()
		require.NoError(t, err, "setIamPolicy on %s", resource)
		require.Len(t, set.Bindings, 1)
		got, err := svc.Projects.Instances.Databases.GetIamPolicy(resource, &spanneradmin.GetIamPolicyRequest{}).Do()
		require.NoError(t, err, "getIamPolicy on %s", resource)
		require.Len(t, got.Bindings, 1)
		assert.Equal(t, "roles/spanner.databaseReader", got.Bindings[0].Role)
	}

	// Backup schedules are real resources on the database.
	schedule, err := svc.Projects.Instances.Databases.BackupSchedules.Create(databaseName, &spanneradmin.BackupSchedule{
		RetentionDuration: "172800s",
		Spec:              &spanneradmin.BackupScheduleSpec{CronSpec: &spanneradmin.CrontabSpec{Text: "0 2 * * *"}},
		FullBackupSpec:    &spanneradmin.FullBackupSpec{},
	}).BackupScheduleId("nightly").Do()
	require.NoError(t, err)
	assert.Equal(t, databaseName+"/backupSchedules/nightly", schedule.Name)
	schedules, err := svc.Projects.Instances.Databases.BackupSchedules.List(databaseName).Do()
	require.NoError(t, err)
	require.Len(t, schedules.BackupSchedules, 1)
	updated, err := svc.Projects.Instances.Databases.BackupSchedules.Patch(schedule.Name, &spanneradmin.BackupSchedule{
		RetentionDuration: "86400s",
	}).UpdateMask("retentionDuration").Do()
	require.NoError(t, err)
	assert.Equal(t, "86400s", updated.RetentionDuration)
	_, err = svc.Projects.Instances.Databases.BackupSchedules.GetIamPolicy(schedule.Name, &spanneradmin.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)

	// An invalid crontab is rejected rather than stored.
	_, err = svc.Projects.Instances.Databases.BackupSchedules.Create(databaseName, &spanneradmin.BackupSchedule{
		RetentionDuration: "86400s",
		Spec:              &spanneradmin.BackupScheduleSpec{CronSpec: &spanneradmin.CrontabSpec{Text: "not a crontab"}},
	}).BackupScheduleId("broken").Do()
	require.Error(t, err)

	_, err = svc.Projects.Instances.Databases.BackupSchedules.Delete(schedule.Name).Do()
	require.NoError(t, err)

	// Operations: the database's own collection lists what the DDL updates and
	// the patch recorded, and each is individually readable, cancellable and
	// deletable.
	databaseOps, err := svc.Projects.Instances.Databases.Operations.List(databaseName + "/operations").Do()
	require.NoError(t, err)
	require.NotEmpty(t, databaseOps.Operations)
	first := databaseOps.Operations[0]
	fetched, err := svc.Projects.Instances.Databases.Operations.Get(first.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, first.Name, fetched.Name)
	_, err = svc.Projects.Instances.Databases.Operations.Cancel(first.Name).Do()
	require.NoError(t, err, "cancelling an operation that already completed succeeds")
	_, err = svc.Projects.Instances.Databases.Operations.Delete(first.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Databases.Operations.Get(first.Name).Do()
	require.Error(t, err, "the deleted operation is gone")

	instanceOps, err := svc.Projects.Instances.Operations.List(instanceName + "/operations").Do()
	require.NoError(t, err)
	assert.NotEmpty(t, instanceOps.Operations, "CreateInstance recorded an instance operation")
}

// TestSpanner_InstancePartitionsAndMove covers the instance-scoped surface:
// instance partitions and MoveInstance, both of which change instance state the
// subsequent reads report.
func TestSpanner_InstancePartitionsAndMove(t *testing.T) {
	ctx := context.Background()
	svc := spannerAdminService(t, ctx)

	const (
		project  = "test-project"
		instance = "partitioned"
	)
	instanceName := fmt.Sprintf("projects/%s/instances/%s", project, instance)

	op, err := svc.Projects.Instances.Create("projects/"+project, &spanneradmin.CreateInstanceRequest{
		InstanceId: instance,
		Instance: &spanneradmin.Instance{
			DisplayName: instance,
			NodeCount:   1,
			Config:      fmt.Sprintf("projects/%s/instanceConfigs/regional-us-central1", project),
		},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	partitionName := instanceName + "/instancePartitions/shard-a"
	createOp, err := svc.Projects.Instances.InstancePartitions.Create(instanceName, &spanneradmin.CreateInstancePartitionRequest{
		InstancePartitionId: "shard-a",
		InstancePartition: &spanneradmin.InstancePartition{
			Config:      fmt.Sprintf("projects/%s/instanceConfigs/regional-us-east1", project),
			DisplayName: "Shard A",
			NodeCount:   1,
		},
	}).Do()
	require.NoError(t, err)
	require.True(t, createOp.Done)
	require.Nil(t, createOp.Error)

	partition, err := svc.Projects.Instances.InstancePartitions.Get(partitionName).Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", partition.State)
	assert.Equal(t, int64(1), partition.NodeCount)

	patchOp, err := svc.Projects.Instances.InstancePartitions.Patch(partitionName, &spanneradmin.UpdateInstancePartitionRequest{
		FieldMask:         "nodeCount",
		InstancePartition: &spanneradmin.InstancePartition{NodeCount: 3},
	}).Do()
	require.NoError(t, err)
	require.Nil(t, patchOp.Error)
	partition, err = svc.Projects.Instances.InstancePartitions.Get(partitionName).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(3), partition.NodeCount)

	partitions, err := svc.Projects.Instances.InstancePartitions.List(instanceName).Do()
	require.NoError(t, err)
	require.Len(t, partitions.InstancePartitions, 1)

	partitionOps, err := svc.Projects.Instances.InstancePartitionOperations.List(instanceName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, partitionOps.Operations)
	scopedOps, err := svc.Projects.Instances.InstancePartitions.Operations.List(partitionName + "/operations").Do()
	require.NoError(t, err)
	require.NotEmpty(t, scopedOps.Operations)

	_, err = svc.Projects.Instances.InstancePartitions.Delete(partitionName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.InstancePartitions.Get(partitionName).Do()
	require.Error(t, err)

	// MoveInstance relocates the instance to the target configuration, which is
	// the configuration a subsequent read reports.
	target := fmt.Sprintf("projects/%s/instanceConfigs/regional-us-east4", project)
	moveOp, err := svc.Projects.Instances.Move(instanceName, &spanneradmin.MoveInstanceRequest{TargetConfig: target}).Do()
	require.NoError(t, err)
	require.True(t, moveOp.Done)
	require.Nil(t, moveOp.Error)
	moved, err := svc.Projects.Instances.Get(instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, target, moved.Config)

	permissions, err := svc.Projects.Instances.TestIamPermissions(instanceName, &spanneradmin.TestIamPermissionsRequest{
		Permissions: []string{"spanner.instances.get"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"spanner.instances.get"}, permissions.Permissions)

	_, err = svc.Projects.Instances.Delete(instanceName).Do()
	require.NoError(t, err)
}
