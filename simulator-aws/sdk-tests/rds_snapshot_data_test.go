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

// TestRDS_SnapshotCapturesDataAndRestoreReturnsToIt expects rows written before
// the snapshot in the restored instance and rows written after it not,
// asserted through a stock PostgreSQL driver against both instances' data
// planes. How the engine copies the volume is not observable through the API,
// so the test does not depend on it.
func TestRDS_SnapshotCapturesDataAndRestoreReturnsToIt(t *testing.T) {
	t.Parallel()
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

	_, err := c.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
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

	connect := func(id string) *pgx.Conn {
		t.Helper()
		described, waitErr := rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).WaitForOutput(testContext, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)}, 3*time.Minute)
		require.NoError(t, waitErr, "DB instance %s never became available", id)
		endpoint := described.DBInstances[0].Endpoint
		config, parseErr := pgx.ParseConfig(fmt.Sprintf(
			"postgres://%s@%s:%d/%s?sslmode=require", username,
			aws.ToString(endpoint.Address), aws.ToInt32(endpoint.Port), database))
		require.NoError(t, parseErr)
		config.Password = password
		config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
		conn, connectErr := pgx.ConnectConfig(testContext, config)
		require.NoError(t, connectErr, "an available instance's data plane must accept the stock driver")
		return conn
	}

	source := connect(instanceID)
	_, err = source.Exec(testContext, `CREATE TABLE ledger (entry text)`)
	require.NoError(t, err)
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('before-snapshot')`)
	require.NoError(t, err)

	// The capture is asynchronous; wait for it to settle.
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
	require.NoError(t, rds.NewDBSnapshotAvailableWaiter(c, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(testContext, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: aws.String(snapshotID)}, 3*time.Minute),
		"the snapshot must settle once its capture completes")

	// A restore must not hold rows written after the snapshot.
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('after-snapshot')`)
	require.NoError(t, err)
	require.NoError(t, source.Close(testContext))

	_, err = c.RestoreDBInstanceFromDBSnapshot(testContext, &rds.RestoreDBInstanceFromDBSnapshotInput{
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

	restoredConn := connect(restoredID)
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
