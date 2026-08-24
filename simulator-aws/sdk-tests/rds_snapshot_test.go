package aws_sdk_test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDS_Snapshot_Lifecycle exercises the snapshot family
// — CreateDBSnapshot → DescribeDBSnapshots → RestoreDBInstanceFromDBSnapshot
// → DeleteDBSnapshot — and asserts the state machine documented in
// RDSSnapshot's docstring. Real RDS transitions a snapshot through
// creating → available → deleted (or failed), and so does the sim: the
// create answers "creating" and settles asynchronously once the instance's
// data is captured, so the test waits for "available" the way a client must.

// waitForRDSSnapshotAvailable polls until the snapshot's asynchronous capture
// settles — the create answers "creating", exactly as real RDS does, and
// nothing (restore in particular) may act on it before "available".
func waitForRDSSnapshotAvailable(t *testing.T, c *rds.Client, ctx context.Context, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		desc, err := c.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{
			DBSnapshotIdentifier: aws.String(id),
		})
		if err != nil || len(desc.DBSnapshots) != 1 {
			return false
		}
		return aws.ToString(desc.DBSnapshots[0].Status) == "available"
	}, 90*time.Second, 100*time.Millisecond, "snapshot %s must settle to available", id)
}

func TestRDS_Snapshot_Lifecycle(t *testing.T) {
	c := rdsClient()
	ctx := context.Background()

	// Source instance.
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("snap-src"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("SnapshotPassword-123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String("snap-src"),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	// CreateDBSnapshot.
	createOut, err := c.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String("snap-1"),
		DBInstanceIdentifier: aws.String("snap-src"),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.DBSnapshot)
	assert.Equal(t, "creating", aws.ToString(createOut.DBSnapshot.Status),
		"the create answers creating and settles asynchronously, as real RDS does")
	assert.Equal(t, "manual", aws.ToString(createOut.DBSnapshot.SnapshotType),
		"sim emits SnapshotType=manual for client-initiated snapshots")
	waitForRDSSnapshotAvailable(t, c, ctx, "snap-1")

	// DescribeDBSnapshots round-trips.
	desc, err := c.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{
		DBSnapshotIdentifier: aws.String("snap-1"),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBSnapshots, 1)
	assert.Equal(t, "snap-1", aws.ToString(desc.DBSnapshots[0].DBSnapshotIdentifier))
	assert.Equal(t, "available", aws.ToString(desc.DBSnapshots[0].Status))

	attrs, err := c.DescribeDBSnapshotAttributes(ctx, &rds.DescribeDBSnapshotAttributesInput{
		DBSnapshotIdentifier: aws.String("snap-1"),
	})
	require.NoError(t, err)
	require.NotNil(t, attrs.DBSnapshotAttributesResult)
	assert.Equal(t, "snap-1", aws.ToString(attrs.DBSnapshotAttributesResult.DBSnapshotIdentifier))

	// RestoreDBInstanceFromDBSnapshot creates a new instance from the
	// snapshot; the new instance inherits engine + version + storage.
	restoreOut, err := c.RestoreDBInstanceFromDBSnapshot(ctx, &rds.RestoreDBInstanceFromDBSnapshotInput{
		DBInstanceIdentifier: aws.String("snap-restored"),
		DBSnapshotIdentifier: aws.String("snap-1"),
		DBInstanceClass:      aws.String("db.t3.micro"),
	})
	require.NoError(t, err)
	require.NotNil(t, restoreOut.DBInstance)
	assert.Equal(t, "postgres", aws.ToString(restoreOut.DBInstance.Engine))
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String("snap-restored"),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	// DeleteDBSnapshot transitions to deleted + removes from store.
	delOut, err := c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
		DBSnapshotIdentifier: aws.String("snap-1"),
	})
	require.NoError(t, err)
	assert.Equal(t, "deleted", aws.ToString(delOut.DBSnapshot.Status),
		"final state-machine transition before removal")

	// Subsequent identifier-addressed Describe returns the real RDS
	// not-found fault; unfiltered list calls are the empty-list shape.
	_, err = c.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{
		DBSnapshotIdentifier: aws.String("snap-1"),
	})
	assertAWSAPIErrorCode(t, err, "DBSnapshotNotFound")

	desc2, err := c.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{})
	require.NoError(t, err)
	assert.Empty(t, desc2.DBSnapshots)
}

// TestRDS_RestoreFromSnapshot_PortFromEngine proves a snapshot restore derives
// the new instance's port from the source engine, not a hardcoded 5432. A MySQL
// snapshot must restore to port 3306.
func TestRDS_RestoreFromSnapshot_PortFromEngine(t *testing.T) {
	c := rdsClient()
	ctx := context.Background()

	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("port-src"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("mysql"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("SnapshotPassword-123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String("port-src"), SkipFinalSnapshot: aws.Bool(true),
		})
	})

	_, err = c.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String("port-snap"),
		DBInstanceIdentifier: aws.String("port-src"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{DBSnapshotIdentifier: aws.String("port-snap")})
	})
	waitForRDSSnapshotAvailable(t, c, ctx, "port-snap")

	restore, err := c.RestoreDBInstanceFromDBSnapshot(ctx, &rds.RestoreDBInstanceFromDBSnapshotInput{
		DBInstanceIdentifier: aws.String("port-restored"),
		DBSnapshotIdentifier: aws.String("port-snap"),
		DBInstanceClass:      aws.String("db.t3.micro"),
	})
	require.NoError(t, err)
	require.NotNil(t, restore.DBInstance)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String("port-restored"), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	assert.Equal(t, "mysql", aws.ToString(restore.DBInstance.Engine))
	require.NotNil(t, restore.DBInstance.Endpoint)
	address := aws.ToString(restore.DBInstance.Endpoint.Address)
	port := aws.ToInt32(restore.DBInstance.Endpoint.Port)
	if strings.HasSuffix(address, ".rds.amazonaws.com") {
		// Modeled tier: the endpoint is nominal, so the port must derive from
		// the engine — 3306 for MySQL, never a hardcoded 5432.
		assert.Equal(t, int32(3306), port,
			"MySQL snapshot must restore to port 3306, not a hardcoded 5432")
	} else {
		// Engine tier: the endpoint is REAL — the port is wherever the
		// restored MySQL actually listens, so the honest assertion is that it
		// accepts a connection, not that it equals a number.
		require.NotZero(t, port)
		require.Eventually(t, func() bool {
			conn, dialErr := net.DialTimeout("tcp",
				net.JoinHostPort(address, strconv.Itoa(int(port))), 5*time.Second)
			if dialErr != nil {
				return false
			}
			_ = conn.Close()
			return true
		}, 60*time.Second, time.Second, "restored MySQL endpoint %s:%d must accept a connection", address, port)
	}
}

// TestRDS_DeleteDBInstance_FinalSnapshotContract proves DeleteDBInstance
// enforces RDS's final-snapshot parameters — SkipFinalSnapshot and
// FinalDBSnapshotIdentifier are mutually exclusive, one of them is required —
// and that a requested final snapshot is a real snapshot: it settles to
// available and remains after the instance is gone.
func TestRDS_DeleteDBInstance_FinalSnapshotContract(t *testing.T) {
	c := rdsClient()
	ctx := context.Background()

	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("final-snap-src"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("SnapshotPassword-123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String("final-snap-src"),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	// Neither skipping nor naming a final snapshot is the contract violation
	// RDS rejects.
	_, err = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String("final-snap-src"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	// So is doing both.
	_, err = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier:      aws.String("final-snap-src"),
		SkipFinalSnapshot:         aws.Bool(true),
		FinalDBSnapshotIdentifier: aws.String("final-snap-1"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	// Naming the final snapshot deletes the instance and leaves the snapshot,
	// which settles through the same capture as CreateDBSnapshot.
	_, err = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier:      aws.String("final-snap-src"),
		FinalDBSnapshotIdentifier: aws.String("final-snap-1"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String("final-snap-1"),
		})
	})
	waitForRDSSnapshotAvailable(t, c, ctx, "final-snap-1")

	_, err = c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String("final-snap-src"),
	})
	assertAWSAPIErrorCode(t, err, "DBInstanceNotFound")
}

// TestRDS_DeleteDBCluster_FinalSnapshotContract proves DeleteDBCluster
// enforces the same parameter contract and records the final cluster
// snapshot.
func TestRDS_DeleteDBCluster_FinalSnapshotContract(t *testing.T) {
	c := rdsClient()
	ctx := context.Background()

	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String("final-snap-cluster"),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("SnapshotPassword-123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String("final-snap-cluster"),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	_, err = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier: aws.String("final-snap-cluster"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	_, err = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier:       aws.String("final-snap-cluster"),
		SkipFinalSnapshot:         aws.Bool(true),
		FinalDBSnapshotIdentifier: aws.String("final-snap-cluster-1"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	_, err = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier:       aws.String("final-snap-cluster"),
		FinalDBSnapshotIdentifier: aws.String("final-snap-cluster-1"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
			DBClusterSnapshotIdentifier: aws.String("final-snap-cluster-1"),
		})
	})

	snaps, err := c.DescribeDBClusterSnapshots(ctx, &rds.DescribeDBClusterSnapshotsInput{
		DBClusterSnapshotIdentifier: aws.String("final-snap-cluster-1"),
	})
	require.NoError(t, err)
	require.Len(t, snaps.DBClusterSnapshots, 1)
	assert.Equal(t, "available", aws.ToString(snaps.DBClusterSnapshots[0].Status))
	assert.Equal(t, "final-snap-cluster", aws.ToString(snaps.DBClusterSnapshots[0].DBClusterIdentifier))
}
