package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Amazon RDS snapshot carries the data, and a restore returns to it.
//
// This is the property that separates a snapshot from a metadata row: rows
// written before the snapshot exist in the restored instance, and rows
// written after it do not. Both halves are asserted through a stock
// PostgreSQL driver against the instances' real data planes — the restored
// engine boots on the captured volume, cloned copy-on-write where the
// engine's volume store supports block cloning and by full copy elsewhere,
// with this test indifferent to which, because the API is.
func TestRDS_SnapshotCapturesDataAndRestoreReturnsToIt(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := rdsClient()
	const (
		instanceID = "rds-snapshot-data-source"
		restoredID = "rds-snapshot-data-restored"
		snapshotID = "rds-snapshot-data-snap"
		username   = "dbadmin"
		password   = "MasterPassword-123!"
		database   = "application"
	)

	created, err := c.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String(username),
		MasterUserPassword:   aws.String(password),
		DBName:               aws.String(database),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
		})
	})

	connect := func(instance *rds.CreateDBInstanceOutput, restored *rds.RestoreDBInstanceFromDBSnapshotOutput) *pgx.Conn {
		var address string
		var port int32
		if instance != nil {
			address, port = aws.ToString(instance.DBInstance.Endpoint.Address), aws.ToInt32(instance.DBInstance.Endpoint.Port)
		} else {
			address, port = aws.ToString(restored.DBInstance.Endpoint.Address), aws.ToInt32(restored.DBInstance.Endpoint.Port)
		}
		config, parseErr := pgx.ParseConfig(fmt.Sprintf(
			"postgres://%s@%s:%d/%s?sslmode=require", username, address, port, database))
		require.NoError(t, parseErr)
		config.Password = password
		config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
		var conn *pgx.Conn
		require.Eventually(t, func() bool {
			var connectErr error
			conn, connectErr = pgx.ConnectConfig(testContext, config)
			return connectErr == nil
		}, 2*time.Minute, 2*time.Second, "the data plane must accept the stock driver")
		return conn
	}

	source := connect(created, nil)
	_, err = source.Exec(testContext, `CREATE TABLE ledger (entry text)`)
	require.NoError(t, err)
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('before-snapshot')`)
	require.NoError(t, err)

	// Snapshot, and wait for the capture to settle: the status is a real
	// state machine now, backed by the copy.
	_, err = c.CreateDBSnapshot(testContext, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(snapshotID),
		DBInstanceIdentifier: aws.String(instanceID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(context.Background(), &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String(snapshotID),
		})
	})
	require.Eventually(t, func() bool {
		described, describeErr := c.DescribeDBSnapshots(testContext, &rds.DescribeDBSnapshotsInput{
			DBSnapshotIdentifier: aws.String(snapshotID),
		})
		if describeErr != nil || len(described.DBSnapshots) != 1 {
			return false
		}
		status := aws.ToString(described.DBSnapshots[0].Status)
		require.NotEqual(t, "failed", status, "the capture must not fail")
		return status == "available"
	}, 3*time.Minute, time.Second, "the snapshot must settle once its capture completes")

	// Rows written after the snapshot are the half a restore must NOT have.
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('after-snapshot')`)
	require.NoError(t, err)
	require.NoError(t, source.Close(testContext))

	restored, err := c.RestoreDBInstanceFromDBSnapshot(testContext, &rds.RestoreDBInstanceFromDBSnapshotInput{
		DBInstanceIdentifier: aws.String(restoredID),
		DBSnapshotIdentifier: aws.String(snapshotID),
		DBInstanceClass:      aws.String("db.t3.micro"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(restoredID), SkipFinalSnapshot: aws.Bool(true),
		})
	})

	restoredConn := connect(nil, restored)
	defer func() { _ = restoredConn.Close(context.Background()) }()
	rows, err := restoredConn.Query(testContext, `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err, "the restored engine must serve the captured schema")
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	rows.Close()
	assert.Equal(t, []string{"before-snapshot"}, entries,
		"the restore must return to the snapshot: rows from before it, and none from after it")
}
