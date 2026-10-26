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

// Amazon RDS takes a snapshot from creating to available, or failed, and
// deletes it; the create answers "creating", so a client waits for
// "available".

// waitForRDSSnapshotAvailable waits for the snapshot's capture to settle;
// nothing, a restore in particular, may act on it before.
func waitForRDSSnapshotAvailable(t *testing.T, c *rds.Client, ctx context.Context, id string) {
	t.Helper()
	require.NoError(t, rds.NewDBSnapshotAvailableWaiter(c, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: aws.String(id)}, 90*time.Second),
		"snapshot %s must settle to available", id)
}

func TestRDS_Snapshot_Lifecycle(t *testing.T) {
	t.Parallel()
	c := rdsClient()
	ctx := context.Background()

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
	waitForRDSInstanceAvailable(t, c, ctx, "snap-src")

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

	// The restored instance inherits the snapshot's engine, version and storage.
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

	delOut, err := c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
		DBSnapshotIdentifier: aws.String("snap-1"),
	})
	require.NoError(t, err)
	assert.Equal(t, "deleted", aws.ToString(delOut.DBSnapshot.Status),
		"final state-machine transition before removal")

	// Describing the deleted snapshot answers the not-found fault, and the
	// instance's manual snapshots list empty beside the automated snapshot its
	// creation took.
	_, err = c.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{
		DBSnapshotIdentifier: aws.String("snap-1"),
	})
	assertAWSAPIErrorCode(t, err, "DBSnapshotNotFound")

	desc2, err := c.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{
		DBInstanceIdentifier: aws.String("snap-src"), SnapshotType: aws.String("manual"),
	})
	require.NoError(t, err)
	assert.Empty(t, desc2.DBSnapshots)
}

// TestRDS_RestoreFromSnapshot_PortFromEngine expects a restored MySQL snapshot
// on the engine's port, not 5432.
func TestRDS_RestoreFromSnapshot_PortFromEngine(t *testing.T) {
	t.Parallel()
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
	waitForRDSInstanceAvailable(t, c, ctx, "port-src")

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
	described, err := rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("port-restored")}, 3*time.Minute)
	require.NoError(t, err, "the restored instance never became available")
	require.NotNil(t, described.DBInstances[0].Endpoint)
	address := aws.ToString(described.DBInstances[0].Endpoint.Address)
	port := aws.ToInt32(described.DBInstances[0].Endpoint.Port)
	if strings.HasSuffix(address, ".rds.amazonaws.com") {
		// A nominal endpoint carries the engine's default port.
		assert.Equal(t, int32(3306), port,
			"MySQL snapshot must restore to port 3306, not a hardcoded 5432")
	} else {
		// A served endpoint's port is wherever the restored MySQL listens, so assert
		// it accepts a connection rather than a number.
		require.NotZero(t, port)
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(address, strconv.Itoa(int(port))), 5*time.Second)
		require.NoError(t, dialErr, "an available restored MySQL endpoint %s:%d must accept a connection", address, port)
		_ = conn.Close()
	}
}

// TestRDS_DeleteDBInstance_FinalSnapshotContract expects DeleteDBInstance to
// require exactly one of SkipFinalSnapshot and FinalDBSnapshotIdentifier, and a
// requested final snapshot to settle available and outlive the instance.
func TestRDS_DeleteDBInstance_FinalSnapshotContract(t *testing.T) {
	t.Parallel()
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
	waitForRDSInstanceAvailable(t, c, ctx, "final-snap-src")

	// Neither skipping nor naming a final snapshot is refused.
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
	// which settles through the same capture as CreateDBSnapshot. The instance is
	// deleting until then, and its identifier is free once it is gone.
	deleted, err := c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier:      aws.String("final-snap-src"),
		FinalDBSnapshotIdentifier: aws.String("final-snap-1"),
	})
	require.NoError(t, err)
	assert.Equal(t, "deleting", aws.ToString(deleted.DBInstance.DBInstanceStatus))
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String("final-snap-1"),
		})
	})
	waitForRDSSnapshotAvailable(t, c, ctx, "final-snap-1")
	require.NoError(t, rds.NewDBInstanceDeletedWaiter(c, func(o *rds.DBInstanceDeletedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("final-snap-src")}, 90*time.Second))

	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("final-snap-src"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("SnapshotPassword-123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err, "the deleted instance's identifier must be free once it is gone")
	require.NoError(t, rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("final-snap-src")}, 90*time.Second))
}

// TestRDS_DeleteDBCluster_FinalSnapshotContract expects DeleteDBCluster to
// enforce the same contract and keep the final cluster snapshot.
func TestRDS_DeleteDBCluster_FinalSnapshotContract(t *testing.T) {
	t.Parallel()
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

	deleted, err := c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier:       aws.String("final-snap-cluster"),
		FinalDBSnapshotIdentifier: aws.String("final-snap-cluster-1"),
	})
	require.NoError(t, err)
	assert.Equal(t, "deleting", aws.ToString(deleted.DBCluster.Status))
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
			DBClusterSnapshotIdentifier: aws.String("final-snap-cluster-1"),
		})
	})
	waitForRDSClusterSnapshotAvailable(t, c, ctx, "final-snap-cluster-1")
	require.NoError(t, rds.NewDBClusterDeletedWaiter(c, func(o *rds.DBClusterDeletedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String("final-snap-cluster")}, 90*time.Second))

	snaps, err := c.DescribeDBClusterSnapshots(ctx, &rds.DescribeDBClusterSnapshotsInput{
		DBClusterSnapshotIdentifier: aws.String("final-snap-cluster-1"),
	})
	require.NoError(t, err)
	require.Len(t, snaps.DBClusterSnapshots, 1)
	assert.Equal(t, "available", aws.ToString(snaps.DBClusterSnapshots[0].Status))
	assert.Equal(t, "final-snap-cluster", aws.ToString(snaps.DBClusterSnapshots[0].DBClusterIdentifier))
}
