package aws_cli_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliDBInstance struct {
	DBInstanceStatus      string `json:"DBInstanceStatus"`
	BackupRetentionPeriod int    `json:"BackupRetentionPeriod"`
	LatestRestorableTime  string `json:"LatestRestorableTime"`
	Endpoint              struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Endpoint"`
}

// cliAvailableDBInstance waits for the instance and describes it.
func cliAvailableDBInstance(t *testing.T, instanceID string) cliDBInstance {
	t.Helper()
	runCLI(t, awsCLI("rds", "wait", "db-instance-available", "--db-instance-identifier", instanceID))
	var described struct {
		DBInstances []cliDBInstance `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", instanceID)), &described)
	require.Len(t, described.DBInstances, 1)
	return described.DBInstances[0]
}

func cliCleanupDBInstance(t *testing.T, instanceID string) {
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance", "--db-instance-identifier", instanceID, "--skip-final-snapshot").Run()
	})
}

// aws rds restore-db-instance-to-point-in-time --restore-time returns an RDS
// for PostgreSQL instance to the rows committed by that time, which lies
// before the LatestRestorableTime describe-db-instances reports, and refuses
// a time before the instance's first automated snapshot.
func TestRDSCLI_InstanceRestoresToAPointInTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sourceID, restoredID := "cli-instance-pitr-source", "cli-instance-pitr-restored"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", sourceID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--backup-retention-period", "1"))
	cliCleanupDBInstance(t, sourceID)
	source := cliAvailableDBInstance(t, sourceID)
	conn := cliConnectPostgres(t, ctx, source.Endpoint.Address, source.Endpoint.Port)
	_, err := conn.Exec(ctx, `CREATE TABLE ledger (entry text NOT NULL)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO ledger VALUES ('before-restore-time')`)
	require.NoError(t, err)
	// RDS takes the restore time to the millisecond: restore to the engine's
	// next millisecond, and commit the later row once its clock has passed it.
	var restoreTo time.Time
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT date_trunc('milliseconds', clock_timestamp()) + interval '1 millisecond'`).Scan(&restoreTo))
	_, err = conn.Exec(ctx, fmt.Sprintf(`SELECT pg_sleep_until('%s')`, restoreTo.UTC().Format(time.RFC3339Nano)))
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO ledger VALUES ('after-restore-time')`)
	require.NoError(t, err)

	described := cliAvailableDBInstance(t, sourceID)
	assert.Equal(t, 1, described.BackupRetentionPeriod)
	require.NotEmpty(t, described.LatestRestorableTime, "an instance whose engine has served reports LatestRestorableTime")

	refused := runCLIExpectError(t, awsCLI("rds", "restore-db-instance-to-point-in-time",
		"--source-db-instance-identifier", sourceID,
		"--target-db-instance-identifier", "cli-instance-pitr-too-early",
		"--restore-time", restoreTo.Add(-time.Hour).UTC().Format(time.RFC3339)))
	assert.Contains(t, refused, "InvalidRestoreFault")

	out := runCLI(t, awsCLI("rds", "restore-db-instance-to-point-in-time",
		"--source-db-instance-identifier", sourceID,
		"--target-db-instance-identifier", restoredID,
		"--restore-time", restoreTo.UTC().Format("2006-01-02T15:04:05.000Z")))
	cliCleanupDBInstance(t, restoredID)
	var restored struct {
		DBInstance cliDBInstance `json:"DBInstance"`
	}
	parseJSON(t, out, &restored)
	assert.Equal(t, "creating", restored.DBInstance.DBInstanceStatus)
	target := cliAvailableDBInstance(t, restoredID)
	assert.Equal(t, []string{"before-restore-time"}, cliLedger(t, ctx, cliConnectPostgres(t, ctx, target.Endpoint.Address, target.Endpoint.Port)),
		"the restored instance holds the rows committed by the restore time and none after it")

	// Deleting the source with --no-delete-automated-backups retains its
	// automated backup, which restores the deleted instance by its resource ID.
	var sources struct {
		DBInstances []struct {
			DbiResourceId string `json:"DbiResourceId"`
		} `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", sourceID)), &sources)
	require.Len(t, sources.DBInstances, 1)
	resourceID := sources.DBInstances[0].DbiResourceId
	runCLI(t, awsCLI("rds", "delete-db-instance", "--db-instance-identifier", sourceID,
		"--skip-final-snapshot", "--no-delete-automated-backups"))
	runCLI(t, awsCLI("rds", "wait", "db-instance-deleted", "--db-instance-identifier", sourceID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance-automated-backup", "--dbi-resource-id", resourceID).Run()
	})
	retainedID := "cli-instance-pitr-retained"
	runCLI(t, awsCLI("rds", "restore-db-instance-to-point-in-time",
		"--source-dbi-resource-id", resourceID,
		"--target-db-instance-identifier", retainedID,
		"--use-latest-restorable-time"))
	cliCleanupDBInstance(t, retainedID)
	fromRetained := cliAvailableDBInstance(t, retainedID)
	assert.Equal(t, []string{"after-restore-time", "before-restore-time"},
		cliLedger(t, ctx, cliConnectPostgres(t, ctx, fromRetained.Endpoint.Address, fromRetained.Endpoint.Port)),
		"the retained automated backup holds every row committed before the deletion")
}

// aws rds create-db-instance with a backup retention period takes the
// instance's first automated backup though no client connects: the automated
// backup describe-db-instance-automated-backups lists turns active and the
// automated snapshot is available.
func TestRDSCLI_InstanceTakesItsFirstAutomatedBackupWithoutAClient(t *testing.T) {
	instanceID := "cli-instance-first-backup"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instanceID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--backup-retention-period", "1"))
	cliCleanupDBInstance(t, instanceID)

	// No waiter covers an automated backup, so read its status.
	require.Eventually(t, func() bool {
		var listed struct {
			DBInstanceAutomatedBackups []struct {
				Status string `json:"Status"`
			} `json:"DBInstanceAutomatedBackups"`
		}
		parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instance-automated-backups",
			"--db-instance-identifier", instanceID)), &listed)
		return len(listed.DBInstanceAutomatedBackups) == 1 && listed.DBInstanceAutomatedBackups[0].Status == "active"
	}, 3*time.Minute, 500*time.Millisecond, "Amazon RDS takes the first automated backup when it creates the instance")

	var snapshots struct {
		DBSnapshots []struct {
			DBSnapshotIdentifier string `json:"DBSnapshotIdentifier"`
			Status               string `json:"Status"`
		} `json:"DBSnapshots"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-snapshots",
		"--db-instance-identifier", instanceID, "--snapshot-type", "automated")), &snapshots)
	require.Len(t, snapshots.DBSnapshots, 1)
	assert.True(t, strings.HasPrefix(snapshots.DBSnapshots[0].DBSnapshotIdentifier, "rds:"+instanceID+"-"))
	assert.Equal(t, "available", snapshots.DBSnapshots[0].Status)
	assert.NotEmpty(t, cliAvailableDBInstance(t, instanceID).LatestRestorableTime)
}

// aws rds restore-db-instance-from-s3 imports a Percona XtraBackup from an
// Amazon S3 prefix the ingestion role reads into a new RDS for MySQL
// instance, which serves the backup's data to its master user.
func TestRDSCLI_InstanceRestoresFromS3XtraBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	backup := cliTakeXtraBackup(t, "cli-instance-xtrabackup-source",
		"CREATE DATABASE shop",
		"CREATE TABLE shop.orders (id INT PRIMARY KEY, item VARCHAR(32) NOT NULL)",
		"INSERT INTO shop.orders VALUES (1, 'kettle'), (2, 'teapot')")
	bucket := "cli-instance-s3-import"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("s3", "cp", backup, "s3://"+bucket+"/backups/shop.xbstream"))
	roleName := "cli-rds-instance-s3-import"
	var role struct {
		Role struct {
			Arn string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-role", "--role-name", roleName, "--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"rds.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)), &role)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-role-policy", "--role-name", roleName, "--policy-name", "read").Run()
		_ = awsCLI("iam", "delete-role", "--role-name", roleName).Run()
	})
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", roleName, "--policy-name", "read", "--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListBucket","s3:GetObject"],"Resource":["arn:aws:s3:::`+bucket+`","arn:aws:s3:::`+bucket+`/*"]}]}`))

	instanceID := "cli-instance-s3-import"
	out := runCLI(t, awsCLI("rds", "restore-db-instance-from-s3",
		"--db-instance-identifier", instanceID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--source-engine", "mysql",
		"--source-engine-version", "8.0.40",
		"--s3-bucket-name", bucket,
		"--s3-prefix", "backups/",
		"--s3-ingestion-role-arn", role.Role.Arn))
	cliCleanupDBInstance(t, instanceID)
	var restored struct {
		DBInstance cliDBInstance `json:"DBInstance"`
	}
	parseJSON(t, out, &restored)
	assert.Equal(t, "creating", restored.DBInstance.DBInstanceStatus)
	instance := cliAvailableDBInstance(t, instanceID)
	config := mysql.Config{
		User: cliRestoreUsername, Passwd: cliRestorePassword, Net: "tcp",
		Addr: fmt.Sprintf("%s:%d", instance.Endpoint.Address, instance.Endpoint.Port), DBName: cliRestoreDatabase,
		TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.QueryContext(ctx, `SELECT item FROM shop.orders ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var items []string
	for rows.Next() {
		var item string
		require.NoError(t, rows.Scan(&item))
		items = append(items, item)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"kettle", "teapot"}, items, "the instance serves the backup's data")
}
