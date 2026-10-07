package rds_restore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestRDSRestoreTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	client := seedSnapshot(t, env)
	vars := []string{"-var", "mariadb_restore_time=" + seedMariaDBSource(t, client).Format(time.RFC3339)}

	env.Terraform(t, "init")
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars...)...)

	outputs := readOutputs(t, env)
	require.True(t, strings.HasPrefix(outputs.must(t, "rds_restored_instance_arn"), "arn:aws:rds:us-east-1:"),
		"Restored RDS instance ARN must include the rds-region prefix")
	require.Contains(t, outputs.must(t, "rds_restored_instance_arn"), ":db:tf-rds-restored",
		"Restored RDS instance ARN must end with :db:<identifier>")
	require.Equal(t, "postgres", outputs.must(t, "rds_restored_instance_engine"),
		"Restored RDS engine must round-trip through terraform-provider-aws refresh")
	require.Equal(t, "terraform", outputs.must(t, "rds_restored_instance_tags_env"),
		"Restored RDS tags must round-trip through ListTagsForResource")
	require.Equal(t, "postgres", outputs.must(t, "rds_point_in_time_engine"),
		"An instance restored to a point in time runs its source's engine")
	require.Equal(t, "1", outputs.must(t, "rds_point_in_time_backup_retention_period"),
		"An instance restored to a point in time keeps its source's backup retention period")

	require.Equal(t, "mariadb", outputs.must(t, "rds_mariadb_point_in_time_engine"))
	require.Equal(t, []string{"before-restore-time"}, mariaDBLedger(t,
		net.JoinHostPort(outputs.must(t, "rds_mariadb_point_in_time_address"), outputs.must(t, "rds_mariadb_point_in_time_port"))),
		"an RDS for MariaDB instance restored to a time holds the rows its binary log dates before it")

	resourceID := outputs.must(t, "rds_point_in_time_resource_id")

	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars...)...)

	// delete_automated_backups = false retains the destroyed instance's
	// automated backup.
	ctx := context.Background()
	retained, err := client.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
		DbiResourceId: aws.String(resourceID),
	})
	require.NoError(t, err)
	require.Len(t, retained.DBInstanceAutomatedBackups, 1)
	require.Equal(t, "retained", aws.ToString(retained.DBInstanceAutomatedBackups[0].Status))
	_, err = client.DeleteDBInstanceAutomatedBackup(ctx, &rds.DeleteDBInstanceAutomatedBackupInput{DbiResourceId: aws.String(resourceID)})
	require.NoError(t, err)
}

func seedSnapshot(t *testing.T, env *tfsim.Env) *rds.Client {
	t.Helper()
	ctx := context.Background()
	client := rds.New(rds.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	_, err := client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("tf-rds-restore-source"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		EngineVersion:        aws.String("16.15"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	// Amazon RDS takes no snapshot of an instance it is still creating; the
	// instance turns available once its first automated backup is taken.
	require.NoError(t, rds.NewDBInstanceAvailableWaiter(client, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = 250 * time.Millisecond
		o.MaxDelay = 2 * time.Second
	}).Wait(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String("tf-rds-restore-source"),
	}, 3*time.Minute), "instance tf-rds-restore-source never became available")
	_, err = client.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBInstanceIdentifier: aws.String("tf-rds-restore-source"),
		DBSnapshotIdentifier: aws.String("tf-rds-snapshot-source"),
	})
	require.NoError(t, err)
	// Amazon RDS refuses a restore from a snapshot still being taken ("is
	// creating; it must be available to restore from"). The provider waits for
	// its own aws_db_snapshot; this one is seeded outside Terraform, so wait for
	// it here with the SDK's DBSnapshotAvailable waiter.
	err = rds.NewDBSnapshotAvailableWaiter(client, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay = 250 * time.Millisecond
		o.MaxDelay = 2 * time.Second
	}).Wait(ctx, &rds.DescribeDBSnapshotsInput{
		DBSnapshotIdentifier: aws.String("tf-rds-snapshot-source"),
	}, 2*time.Minute)
	require.NoError(t, err, "snapshot tf-rds-snapshot-source never became available to restore from")
	t.Cleanup(func() {
		_, err := client.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String("tf-rds-snapshot-source"),
		})
		require.NoError(t, err)
		_, err = client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String("tf-rds-restore-source"),
			SkipFinalSnapshot:    aws.Bool(true),
		})
		require.NoError(t, err)
	})
	return client
}

// seedMariaDBSource creates an RDS for MariaDB instance that keeps automated
// backups, commits a row, and commits another once the engine's clock passes
// the next whole second, which it returns: MariaDB's binary log dates
// transactions in whole seconds.
func seedMariaDBSource(t *testing.T, client *rds.Client) time.Time {
	t.Helper()
	ctx := context.Background()
	const instanceID = "tf-rds-mariadb-source"
	_, err := client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(instanceID),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("mariadb"),
		MasterUsername:        aws.String("dbadmin"),
		MasterUserPassword:    aws.String("MasterPassword-123!"),
		DBName:                aws.String("application"),
		AllocatedStorage:      aws.Int32(20),
		BackupRetentionPeriod: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := client.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
		})
		require.NoError(t, err)
	})
	described, err := rds.NewDBInstanceAvailableWaiter(client, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = 250 * time.Millisecond
		o.MaxDelay = 2 * time.Second
	}).WaitForOutput(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute)
	require.NoError(t, err, "instance %s never became available", instanceID)
	instance := described.DBInstances[0]
	db := openMariaDB(t, net.JoinHostPort(aws.ToString(instance.Endpoint.Address), fmt.Sprint(aws.ToInt32(instance.Endpoint.Port))))
	for _, statement := range []string{
		`CREATE TABLE ledger (entry varchar(64) NOT NULL)`,
		`INSERT INTO ledger VALUES ('before-restore-time')`,
	} {
		_, err := db.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	var next int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT FLOOR(UNIX_TIMESTAMP(NOW(6))) + 1`).Scan(&next))
	_, err = db.ExecContext(ctx, fmt.Sprintf(`DO SLEEP(GREATEST(0, %d - UNIX_TIMESTAMP(NOW(6))))`, next))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO ledger VALUES ('after-restore-time')`)
	require.NoError(t, err)
	return time.Unix(next, 0).UTC()
}

func openMariaDB(t *testing.T, address string) *sql.DB {
	t.Helper()
	config := mysql.Config{
		User: "dbadmin", Passwd: "MasterPassword-123!", Net: "tcp", DBName: "application",
		Addr: address, TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mariaDBLedger(t *testing.T, address string) []string {
	t.Helper()
	rows, err := openMariaDB(t, address).QueryContext(context.Background(), `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err)
	defer rows.Close()
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	require.NoError(t, rows.Err())
	return entries
}

type tfOutputs map[string]struct {
	Value any `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is not a string (got %T)", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
