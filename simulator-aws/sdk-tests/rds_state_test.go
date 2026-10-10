package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForRDSInstanceStatus polls until the instance reports status: a
// lifecycle action answers its transitional status (StopDBInstance answers
// stopping) and settles once the engine has made the transition, and the SDK
// carries no waiter for a stopped instance.
func waitForRDSInstanceStatus(t *testing.T, c *rds.Client, ctx context.Context, id, status string) {
	t.Helper()
	require.Eventually(t, func() bool {
		desc, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
		return err == nil && len(desc.DBInstances) == 1 && aws.ToString(desc.DBInstances[0].DBInstanceStatus) == status
	}, 2*time.Minute, 100*time.Millisecond, "DB instance %s must reach %s", id, status)
}

// waitForRDSInstanceAvailable waits with the SDK's DBInstanceAvailable waiter
// for Amazon RDS to finish creating or starting the instance.
func waitForRDSInstanceAvailable(t *testing.T, c *rds.Client, ctx context.Context, id string) {
	t.Helper()
	require.NoError(t, rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)}, 3*time.Minute),
		"DB instance %s must become available", id)
}

// waitForRDSClusterStatus polls until the cluster reports status; the SDK
// carries no waiter for a stopped cluster.
func waitForRDSClusterStatus(t *testing.T, c *rds.Client, ctx context.Context, id, status string) {
	t.Helper()
	require.Eventually(t, func() bool {
		desc, err := c.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(id)})
		return err == nil && len(desc.DBClusters) == 1 && aws.ToString(desc.DBClusters[0].Status) == status
	}, 2*time.Minute, 100*time.Millisecond, "DB cluster %s must reach %s", id, status)
}

// TestRDS_InstanceClusterState exercises the instance/cluster state
// transition ops (Start/Stop instance, Start/Stop/Failover cluster) and
// PromoteReadReplica end-to-end through the AWS SDK for Go v2.
func TestRDS_InstanceClusterState(t *testing.T) {
	c := rdsClient()

	instID := "state-pg-db"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})
	waitForRDSInstanceAvailable(t, c, ctx, instID)

	stopOut, err := c.StopDBInstance(ctx, &rds.StopDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	require.NotNil(t, stopOut.DBInstance)
	assert.Equal(t, "stopping", aws.ToString(stopOut.DBInstance.DBInstanceStatus))
	waitForRDSInstanceStatus(t, c, ctx, instID, "stopped")

	startOut, err := c.StartDBInstance(ctx, &rds.StartDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	require.NotNil(t, startOut.DBInstance)
	assert.Equal(t, "starting", aws.ToString(startOut.DBInstance.DBInstanceStatus))
	waitForRDSInstanceAvailable(t, c, ctx, instID)

	_, err = c.StartDBInstance(ctx, &rds.StartDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
	})
	assertAWSAPIErrorCode(t, err, "InvalidDBInstanceState")

	replicaID := "state-pg-replica"
	_, err = c.CreateDBInstanceReadReplica(ctx, &rds.CreateDBInstanceReadReplicaInput{
		DBInstanceIdentifier:       aws.String(replicaID),
		SourceDBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(replicaID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})
	waitForRDSInstanceAvailable(t, c, ctx, replicaID)
	promOut, err := c.PromoteReadReplica(ctx, &rds.PromoteReadReplicaInput{
		DBInstanceIdentifier: aws.String(replicaID),
	})
	require.NoError(t, err)
	require.NotNil(t, promOut.DBInstance)
	assert.Equal(t, "modifying", aws.ToString(promOut.DBInstance.DBInstanceStatus))
	waitForRDSInstanceAvailable(t, c, ctx, replicaID)
	promoted, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(replicaID)})
	require.NoError(t, err)
	// After promotion the replica has no source.
	assert.Empty(t, aws.ToString(promoted.DBInstances[0].ReadReplicaSourceDBInstanceIdentifier))

	clusterID := "state-aurora-cluster"
	_, err = c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	memberID := "state-aurora-writer"
	member, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(memberID),
		DBClusterIdentifier:  aws.String(clusterID),
		DBInstanceClass:      aws.String("db.r6g.large"),
		Engine:               aws.String("aurora-mysql"),
	})
	require.NoError(t, err)
	assert.Equal(t, clusterID, aws.ToString(member.DBInstance.DBClusterIdentifier))
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(memberID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	stopCl, err := c.StopDBCluster(ctx, &rds.StopDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	require.NotNil(t, stopCl.DBCluster)
	assert.Equal(t, "stopping", aws.ToString(stopCl.DBCluster.Status))
	require.Len(t, stopCl.DBCluster.DBClusterMembers, 1)
	assert.Equal(t, memberID, aws.ToString(stopCl.DBCluster.DBClusterMembers[0].DBInstanceIdentifier))
	assert.True(t, aws.ToBool(stopCl.DBCluster.DBClusterMembers[0].IsClusterWriter))
	waitForRDSClusterStatus(t, c, ctx, clusterID, "stopped")
	waitForRDSInstanceStatus(t, c, ctx, memberID, "stopped")

	_, err = c.StopDBCluster(ctx, &rds.StopDBClusterInput{DBClusterIdentifier: aws.String(clusterID)})
	assertAWSAPIErrorCode(t, err, "InvalidDBClusterStateFault")

	startCl, err := c.StartDBCluster(ctx, &rds.StartDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	require.NotNil(t, startCl.DBCluster)
	assert.Equal(t, "starting", aws.ToString(startCl.DBCluster.Status))
	require.NoError(t, rds.NewDBClusterAvailableWaiter(c, func(o *rds.DBClusterAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(clusterID),
	}, 2*time.Minute))
	require.NoError(t, rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(memberID)}, 2*time.Minute))

	foCl, err := c.FailoverDBCluster(ctx, &rds.FailoverDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	require.NotNil(t, foCl.DBCluster)
	assert.Equal(t, clusterID, aws.ToString(foCl.DBCluster.DBClusterIdentifier))
}

// TestRDS_GlobalClusterLifecycle exercises the Aurora global cluster
// family: Create → Describe → Modify → Delete.
func TestRDS_GlobalClusterLifecycle(t *testing.T) {
	c := rdsClient()

	gid := "sdk-global-cluster"
	createOut, err := c.CreateGlobalCluster(ctx, &rds.CreateGlobalClusterInput{
		GlobalClusterIdentifier: aws.String(gid),
		Engine:                  aws.String("aurora-mysql"),
		EngineVersion:           aws.String("8.0.mysql_aurora.3.04.0"),
		DatabaseName:            aws.String("appdb"),
		StorageEncrypted:        aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.GlobalCluster)
	assert.Equal(t, gid, aws.ToString(createOut.GlobalCluster.GlobalClusterIdentifier))
	assert.Equal(t, "available", aws.ToString(createOut.GlobalCluster.Status))
	assert.Equal(t, "aurora-mysql", aws.ToString(createOut.GlobalCluster.Engine))
	assert.True(t, aws.ToBool(createOut.GlobalCluster.StorageEncrypted))
	require.NotEmpty(t, aws.ToString(createOut.GlobalCluster.GlobalClusterArn))
	t.Cleanup(func() {
		_, _ = c.DeleteGlobalCluster(ctx, &rds.DeleteGlobalClusterInput{
			GlobalClusterIdentifier: aws.String(gid),
		})
	})

	descOut, err := c.DescribeGlobalClusters(ctx, &rds.DescribeGlobalClustersInput{
		GlobalClusterIdentifier: aws.String(gid),
	})
	require.NoError(t, err)
	require.Len(t, descOut.GlobalClusters, 1)
	assert.Equal(t, "appdb", aws.ToString(descOut.GlobalClusters[0].DatabaseName))

	modOut, err := c.ModifyGlobalCluster(ctx, &rds.ModifyGlobalClusterInput{
		GlobalClusterIdentifier: aws.String(gid),
		DeletionProtection:      aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, modOut.GlobalCluster)
	assert.True(t, aws.ToBool(modOut.GlobalCluster.DeletionProtection))

	delOut, err := c.DeleteGlobalCluster(ctx, &rds.DeleteGlobalClusterInput{
		GlobalClusterIdentifier: aws.String(gid),
	})
	require.NoError(t, err)
	require.NotNil(t, delOut.GlobalCluster)
	assert.Equal(t, gid, aws.ToString(delOut.GlobalCluster.GlobalClusterIdentifier))
}

// TestRDS_EventSubscriptionLifecycle exercises the event subscription
// family: Create → Describe → Modify → Delete.
func TestRDS_EventSubscriptionLifecycle(t *testing.T) {
	c := rdsClient()

	name := "sdk-rds-events"
	topic := "arn:aws:sns:us-east-1:123456789012:rds-events"
	createOut, err := c.CreateEventSubscription(ctx, &rds.CreateEventSubscriptionInput{
		SubscriptionName: aws.String(name),
		SnsTopicArn:      aws.String(topic),
		SourceType:       aws.String("db-instance"),
		EventCategories:  []string{"availability", "failure"},
		Enabled:          aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.EventSubscription)
	assert.Equal(t, name, aws.ToString(createOut.EventSubscription.CustSubscriptionId))
	assert.Equal(t, topic, aws.ToString(createOut.EventSubscription.SnsTopicArn))
	assert.Equal(t, "active", aws.ToString(createOut.EventSubscription.Status))
	assert.True(t, aws.ToBool(createOut.EventSubscription.Enabled))
	assert.ElementsMatch(t, []string{"availability", "failure"}, createOut.EventSubscription.EventCategoriesList)
	t.Cleanup(func() {
		_, _ = c.DeleteEventSubscription(ctx, &rds.DeleteEventSubscriptionInput{
			SubscriptionName: aws.String(name),
		})
	})

	descOut, err := c.DescribeEventSubscriptions(ctx, &rds.DescribeEventSubscriptionsInput{
		SubscriptionName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, descOut.EventSubscriptionsList, 1)
	assert.Equal(t, "db-instance", aws.ToString(descOut.EventSubscriptionsList[0].SourceType))

	modOut, err := c.ModifyEventSubscription(ctx, &rds.ModifyEventSubscriptionInput{
		SubscriptionName: aws.String(name),
		Enabled:          aws.Bool(false),
	})
	require.NoError(t, err)
	require.NotNil(t, modOut.EventSubscription)
	assert.False(t, aws.ToBool(modOut.EventSubscription.Enabled))

	delOut, err := c.DeleteEventSubscription(ctx, &rds.DeleteEventSubscriptionInput{
		SubscriptionName: aws.String(name),
	})
	require.NoError(t, err)
	require.NotNil(t, delOut.EventSubscription)
	assert.Equal(t, name, aws.ToString(delOut.EventSubscription.CustSubscriptionId))
}

// TestRDS_ParameterDetailAndSnapshotAttributes exercises parameter
// detail ops (Describe/Modify/Reset DB parameters and DB cluster
// parameters) plus snapshot attribute sharing (Modify/Describe
// DBSnapshotAttribute).
func TestRDS_ParameterDetailAndSnapshotAttributes(t *testing.T) {
	c := rdsClient()

	pgName := "sdk-detail-pg"
	_, err := c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String(pgName),
		DBParameterGroupFamily: aws.String("postgres16"),
		Description:            aws.String("detail pg"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBParameterGroup(ctx, &rds.DeleteDBParameterGroupInput{
			DBParameterGroupName: aws.String(pgName),
		})
	})

	dpOut, err := c.DescribeDBParameters(ctx, &rds.DescribeDBParametersInput{
		DBParameterGroupName: aws.String(pgName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, dpOut.Parameters)

	_, err = c.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{
		DBParameterGroupName: aws.String(pgName),
		Parameters: []rdstypes.Parameter{
			{
				ParameterName:  aws.String("max_connections"),
				ParameterValue: aws.String("200"),
				ApplyMethod:    rdstypes.ApplyMethodPendingReboot,
			},
		},
	})
	require.NoError(t, err)

	dpAfter, err := c.DescribeDBParameters(ctx, &rds.DescribeDBParametersInput{
		DBParameterGroupName: aws.String(pgName),
		Source:               aws.String("user"),
	})
	require.NoError(t, err)
	var found bool
	for _, p := range dpAfter.Parameters {
		if aws.ToString(p.ParameterName) == "max_connections" {
			assert.Equal(t, "200", aws.ToString(p.ParameterValue))
			found = true
		}
	}
	assert.True(t, found, "expected modified max_connections in DescribeDBParameters")

	_, err = c.ResetDBParameterGroup(ctx, &rds.ResetDBParameterGroupInput{
		DBParameterGroupName: aws.String(pgName),
		ResetAllParameters:   aws.Bool(true),
	})
	require.NoError(t, err)

	cpgName := "sdk-detail-cluster-pg"
	_, err = c.CreateDBClusterParameterGroup(ctx, &rds.CreateDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(cpgName),
		DBParameterGroupFamily:      aws.String("aurora-mysql8.0"),
		Description:                 aws.String("detail cluster pg"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterParameterGroup(ctx, &rds.DeleteDBClusterParameterGroupInput{
			DBClusterParameterGroupName: aws.String(cpgName),
		})
	})

	dcpOut, err := c.DescribeDBClusterParameters(ctx, &rds.DescribeDBClusterParametersInput{
		DBClusterParameterGroupName: aws.String(cpgName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, dcpOut.Parameters)

	_, err = c.ModifyDBClusterParameterGroup(ctx, &rds.ModifyDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(cpgName),
		Parameters: []rdstypes.Parameter{
			{
				ParameterName:  aws.String("character_set_server"),
				ParameterValue: aws.String("latin1"),
				ApplyMethod:    rdstypes.ApplyMethodImmediate,
			},
		},
	})
	require.NoError(t, err)

	dcpAfter, err := c.DescribeDBClusterParameters(ctx, &rds.DescribeDBClusterParametersInput{
		DBClusterParameterGroupName: aws.String(cpgName),
		Source:                      aws.String("user"),
	})
	require.NoError(t, err)
	found = false
	for _, p := range dcpAfter.Parameters {
		if aws.ToString(p.ParameterName) == "character_set_server" {
			assert.Equal(t, "latin1", aws.ToString(p.ParameterValue))
			found = true
		}
	}
	assert.True(t, found, "expected modified character_set_server in DescribeDBClusterParameters")

	instID := "sdk-attr-db"
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})
	waitForRDSInstanceAvailable(t, c, ctx, instID)

	snapID := "sdk-attr-snap"
	_, err = c.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(snapID),
		DBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String(snapID),
		})
	})

	_, err = c.ModifyDBSnapshotAttribute(ctx, &rds.ModifyDBSnapshotAttributeInput{
		DBSnapshotIdentifier: aws.String(snapID),
		AttributeName:        aws.String("restore"),
		ValuesToAdd:          []string{"123456789012", "210987654321"},
	})
	require.NoError(t, err)

	attrOut, err := c.DescribeDBSnapshotAttributes(ctx, &rds.DescribeDBSnapshotAttributesInput{
		DBSnapshotIdentifier: aws.String(snapID),
	})
	require.NoError(t, err)
	require.NotNil(t, attrOut.DBSnapshotAttributesResult)
	require.Len(t, attrOut.DBSnapshotAttributesResult.DBSnapshotAttributes, 1)
	attr := attrOut.DBSnapshotAttributesResult.DBSnapshotAttributes[0]
	assert.Equal(t, "restore", aws.ToString(attr.AttributeName))
	assert.ElementsMatch(t, []string{"123456789012", "210987654321"}, attr.AttributeValues)

	// Remove one shared account.
	_, err = c.ModifyDBSnapshotAttribute(ctx, &rds.ModifyDBSnapshotAttributeInput{
		DBSnapshotIdentifier: aws.String(snapID),
		AttributeName:        aws.String("restore"),
		ValuesToRemove:       []string{"210987654321"},
	})
	require.NoError(t, err)
	attrOut2, err := c.DescribeDBSnapshotAttributes(ctx, &rds.DescribeDBSnapshotAttributesInput{
		DBSnapshotIdentifier: aws.String(snapID),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"123456789012"},
		attrOut2.DBSnapshotAttributesResult.DBSnapshotAttributes[0].AttributeValues)
}

// TestRDS_ClusterEndpointAndCopyClusterSnapshot exercises custom DB
// cluster endpoints (Create → Describe → Delete) and
// CopyDBClusterSnapshot.
func TestRDS_ClusterEndpointAndCopyClusterSnapshot(t *testing.T) {
	c := rdsClient()

	clusterID := "sdk-endpoint-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	epID := "sdk-custom-reader"
	createEp, err := c.CreateDBClusterEndpoint(ctx, &rds.CreateDBClusterEndpointInput{
		DBClusterEndpointIdentifier: aws.String(epID),
		DBClusterIdentifier:         aws.String(clusterID),
		EndpointType:                aws.String("READER"),
		StaticMembers:               []string{"member-1", "member-2"},
	})
	require.NoError(t, err)
	assert.Equal(t, epID, aws.ToString(createEp.DBClusterEndpointIdentifier))
	assert.Equal(t, clusterID, aws.ToString(createEp.DBClusterIdentifier))
	assert.Equal(t, "available", aws.ToString(createEp.Status))
	assert.Equal(t, "READER", aws.ToString(createEp.CustomEndpointType))
	assert.ElementsMatch(t, []string{"member-1", "member-2"}, createEp.StaticMembers)
	require.NotEmpty(t, aws.ToString(createEp.Endpoint))
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterEndpoint(ctx, &rds.DeleteDBClusterEndpointInput{
			DBClusterEndpointIdentifier: aws.String(epID),
		})
	})

	descEp, err := c.DescribeDBClusterEndpoints(ctx, &rds.DescribeDBClusterEndpointsInput{
		DBClusterEndpointIdentifier: aws.String(epID),
	})
	require.NoError(t, err)
	require.Len(t, descEp.DBClusterEndpoints, 1)
	assert.Equal(t, clusterID, aws.ToString(descEp.DBClusterEndpoints[0].DBClusterIdentifier))

	delEp, err := c.DeleteDBClusterEndpoint(ctx, &rds.DeleteDBClusterEndpointInput{
		DBClusterEndpointIdentifier: aws.String(epID),
	})
	require.NoError(t, err)
	assert.Equal(t, epID, aws.ToString(delEp.DBClusterEndpointIdentifier))

	srcSnap := "sdk-src-cluster-snap"
	_, err = c.CreateDBClusterSnapshot(ctx, &rds.CreateDBClusterSnapshotInput{
		DBClusterSnapshotIdentifier: aws.String(srcSnap),
		DBClusterIdentifier:         aws.String(clusterID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
			DBClusterSnapshotIdentifier: aws.String(srcSnap),
		})
	})
	waitForRDSClusterSnapshotAvailable(t, c, ctx, srcSnap)

	copySnap := "sdk-copy-cluster-snap"
	copyOut, err := c.CopyDBClusterSnapshot(ctx, &rds.CopyDBClusterSnapshotInput{
		SourceDBClusterSnapshotIdentifier: aws.String(srcSnap),
		TargetDBClusterSnapshotIdentifier: aws.String(copySnap),
		Tags: []rdstypes.Tag{
			{Key: aws.String("purpose"), Value: aws.String("copy")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, copyOut.DBClusterSnapshot)
	assert.Equal(t, copySnap, aws.ToString(copyOut.DBClusterSnapshot.DBClusterSnapshotIdentifier))
	assert.Equal(t, "copying", aws.ToString(copyOut.DBClusterSnapshot.Status))
	assert.Equal(t, clusterID, aws.ToString(copyOut.DBClusterSnapshot.DBClusterIdentifier))
	assert.Contains(t, aws.ToString(copyOut.DBClusterSnapshot.SourceDBClusterSnapshotArn), ":cluster-snapshot:"+srcSnap)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
			DBClusterSnapshotIdentifier: aws.String(copySnap),
		})
	})
	waitForRDSClusterSnapshotAvailable(t, c, ctx, copySnap)
}
