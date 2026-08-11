package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDS_RestoreFamily exercises the cluster/instance restore
// operations: a stored cluster snapshot restores into a new cluster, a
// point-in-time restore clones an existing cluster, an S3 restore
// creates a fresh cluster, and the instance point-in-time / S3 restores
// create new instances.
func TestRDS_RestoreFamily(t *testing.T) {
	c := rdsClient()

	srcCluster := "rext-src-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(srcCluster),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(srcCluster), SkipFinalSnapshot: aws.Bool(true)})
	})

	snapID := "rext-src-cluster-snap"
	_, err = c.CreateDBClusterSnapshot(ctx, &rds.CreateDBClusterSnapshotInput{
		DBClusterSnapshotIdentifier: aws.String(snapID),
		DBClusterIdentifier:         aws.String(srcCluster),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
			DBClusterSnapshotIdentifier: aws.String(snapID)})
	})

	// RestoreDBClusterFromSnapshot
	restored := "rext-restored-from-snap"
	rOut, err := c.RestoreDBClusterFromSnapshot(ctx, &rds.RestoreDBClusterFromSnapshotInput{
		DBClusterIdentifier: aws.String(restored),
		SnapshotIdentifier:  aws.String(snapID),
		Engine:              aws.String("aurora-mysql"),
	})
	require.NoError(t, err)
	require.NotNil(t, rOut.DBCluster)
	assert.Equal(t, restored, aws.ToString(rOut.DBCluster.DBClusterIdentifier))
	assert.Equal(t, "available", aws.ToString(rOut.DBCluster.Status))
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(restored), SkipFinalSnapshot: aws.Bool(true)})
	})

	// RestoreDBClusterToPointInTime
	pit := "rext-cluster-pit"
	pitOut, err := c.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
		DBClusterIdentifier:       aws.String(pit),
		SourceDBClusterIdentifier: aws.String(srcCluster),
		UseLatestRestorableTime:   aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, pitOut.DBCluster)
	assert.Equal(t, "aurora-mysql", aws.ToString(pitOut.DBCluster.Engine))
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(pit), SkipFinalSnapshot: aws.Bool(true)})
	})

	// RestoreDBClusterFromS3
	s3cluster := "rext-cluster-from-s3"
	s3Out, err := c.RestoreDBClusterFromS3(ctx, &rds.RestoreDBClusterFromS3Input{
		DBClusterIdentifier: aws.String(s3cluster),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
		SourceEngine:        aws.String("mysql"),
		SourceEngineVersion: aws.String("8.0"),
		S3BucketName:        aws.String("my-backup-bucket"),
		S3IngestionRoleArn:  aws.String("arn:aws:iam::123456789012:role/rds-s3"),
	})
	require.NoError(t, err)
	require.NotNil(t, s3Out.DBCluster)
	assert.Equal(t, s3cluster, aws.ToString(s3Out.DBCluster.DBClusterIdentifier))
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(s3cluster), SkipFinalSnapshot: aws.Bool(true)})
	})

	// Instance restores
	srcInst := "rext-src-instance"
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(srcInst),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("mysql"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(srcInst), SkipFinalSnapshot: aws.Bool(true)})
	})

	// RestoreDBInstanceToPointInTime
	instPIT := "rext-instance-pit"
	ipOut, err := c.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
		SourceDBInstanceIdentifier: aws.String(srcInst),
		TargetDBInstanceIdentifier: aws.String(instPIT),
		UseLatestRestorableTime:    aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, ipOut.DBInstance)
	assert.Equal(t, instPIT, aws.ToString(ipOut.DBInstance.DBInstanceIdentifier))
	assert.Equal(t, "mysql", aws.ToString(ipOut.DBInstance.Engine))
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instPIT), SkipFinalSnapshot: aws.Bool(true)})
	})

	// RestoreDBInstanceFromS3
	instS3 := "rext-instance-from-s3"
	isOut, err := c.RestoreDBInstanceFromS3(ctx, &rds.RestoreDBInstanceFromS3Input{
		DBInstanceIdentifier: aws.String(instS3),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("mysql"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
		SourceEngine:         aws.String("mysql"),
		SourceEngineVersion:  aws.String("8.0"),
		S3BucketName:         aws.String("my-backup-bucket"),
		S3IngestionRoleArn:   aws.String("arn:aws:iam::123456789012:role/rds-s3"),
	})
	require.NoError(t, err)
	require.NotNil(t, isOut.DBInstance)
	assert.Equal(t, instS3, aws.ToString(isOut.DBInstance.DBInstanceIdentifier))
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instS3), SkipFinalSnapshot: aws.Bool(true)})
	})
}

// TestRDS_ReservedInstances covers the reserved-instance catalog and
// purchase flow.
func TestRDS_ReservedInstances(t *testing.T) {
	c := rdsClient()

	offerings, err := c.DescribeReservedDBInstancesOfferings(ctx, &rds.DescribeReservedDBInstancesOfferingsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, offerings.ReservedDBInstancesOfferings)
	offeringID := aws.ToString(offerings.ReservedDBInstancesOfferings[0].ReservedDBInstancesOfferingId)
	require.NotEmpty(t, offeringID)

	pOut, err := c.PurchaseReservedDBInstancesOffering(ctx, &rds.PurchaseReservedDBInstancesOfferingInput{
		ReservedDBInstancesOfferingId: aws.String(offeringID),
		ReservedDBInstanceId:          aws.String("rext-my-reservation"),
		DBInstanceCount:               aws.Int32(2),
	})
	require.NoError(t, err)
	require.NotNil(t, pOut.ReservedDBInstance)
	assert.Equal(t, "rext-my-reservation", aws.ToString(pOut.ReservedDBInstance.ReservedDBInstanceId))
	assert.Equal(t, int32(2), aws.ToInt32(pOut.ReservedDBInstance.DBInstanceCount))
	assert.Equal(t, "active", aws.ToString(pOut.ReservedDBInstance.State))

	dOut, err := c.DescribeReservedDBInstances(ctx, &rds.DescribeReservedDBInstancesInput{
		ReservedDBInstanceId: aws.String("rext-my-reservation"),
	})
	require.NoError(t, err)
	require.Len(t, dOut.ReservedDBInstances, 1)
	assert.Equal(t, offeringID, aws.ToString(dOut.ReservedDBInstances[0].ReservedDBInstancesOfferingId))
}

// TestRDS_BlueGreenDeployment covers the blue/green deployment family
// including switchover role-swap.
func TestRDS_BlueGreenDeployment(t *testing.T) {
	c := rdsClient()

	source := "arn:aws:rds:us-east-1:123456789012:cluster:bg-source"
	cOut, err := c.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
		BlueGreenDeploymentName: aws.String("rext-bg"),
		Source:                  aws.String(source),
	})
	require.NoError(t, err)
	require.NotNil(t, cOut.BlueGreenDeployment)
	bgID := aws.ToString(cOut.BlueGreenDeployment.BlueGreenDeploymentIdentifier)
	require.NotEmpty(t, bgID)
	assert.Equal(t, source, aws.ToString(cOut.BlueGreenDeployment.Source))
	t.Cleanup(func() {
		_, _ = c.DeleteBlueGreenDeployment(ctx, &rds.DeleteBlueGreenDeploymentInput{
			BlueGreenDeploymentIdentifier: aws.String(bgID)})
	})

	dOut, err := c.DescribeBlueGreenDeployments(ctx, &rds.DescribeBlueGreenDeploymentsInput{
		BlueGreenDeploymentIdentifier: aws.String(bgID),
	})
	require.NoError(t, err)
	require.Len(t, dOut.BlueGreenDeployments, 1)
	target := aws.ToString(dOut.BlueGreenDeployments[0].Target)
	require.NotEmpty(t, target)

	sOut, err := c.SwitchoverBlueGreenDeployment(ctx, &rds.SwitchoverBlueGreenDeploymentInput{
		BlueGreenDeploymentIdentifier: aws.String(bgID),
	})
	require.NoError(t, err)
	require.NotNil(t, sOut.BlueGreenDeployment)
	assert.Equal(t, "SWITCHOVER_COMPLETED", aws.ToString(sOut.BlueGreenDeployment.Status))
	// Roles swapped: the old target is now the source.
	assert.Equal(t, target, aws.ToString(sOut.BlueGreenDeployment.Source))
}

// TestRDS_Integrations covers the zero-ETL integration CRUD family.
func TestRDS_Integrations(t *testing.T) {
	c := rdsClient()

	src := "arn:aws:rds:us-east-1:123456789012:cluster:int-source"
	tgt := "arn:aws:redshift-serverless:us-east-1:123456789012:namespace/ns-1"
	cOut, err := c.CreateIntegration(ctx, &rds.CreateIntegrationInput{
		IntegrationName: aws.String("rext-integration"),
		SourceArn:       aws.String(src),
		TargetArn:       aws.String(tgt),
		Tags:            []rdstypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	require.NotNil(t, cOut.IntegrationArn)
	arn := aws.ToString(cOut.IntegrationArn)
	require.NotEmpty(t, arn)
	assert.Equal(t, src, aws.ToString(cOut.SourceArn))
	t.Cleanup(func() {
		_, _ = c.DeleteIntegration(ctx, &rds.DeleteIntegrationInput{
			IntegrationIdentifier: aws.String(arn)})
	})

	dOut, err := c.DescribeIntegrations(ctx, &rds.DescribeIntegrationsInput{
		IntegrationIdentifier: aws.String(arn),
	})
	require.NoError(t, err)
	require.Len(t, dOut.Integrations, 1)
	assert.Equal(t, tgt, aws.ToString(dOut.Integrations[0].TargetArn))
	require.Len(t, dOut.Integrations[0].Tags, 1)

	mOut, err := c.ModifyIntegration(ctx, &rds.ModifyIntegrationInput{
		IntegrationIdentifier: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Equal(t, rdstypes.IntegrationStatusActive, mOut.Status)
}

// TestRDS_TenantDatabases covers the tenant-database CRUD family.
func TestRDS_TenantDatabases(t *testing.T) {
	c := rdsClient()

	instID := "rext-oracle-cdb"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.medium"),
		Engine:               aws.String("oracle-ee"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID), SkipFinalSnapshot: aws.Bool(true)})
	})

	cOut, err := c.CreateTenantDatabase(ctx, &rds.CreateTenantDatabaseInput{
		DBInstanceIdentifier: aws.String(instID),
		TenantDBName:         aws.String("tenant1"),
		MasterUsername:       aws.String("tadmin"),
		MasterUserPassword:   aws.String("password123!"),
	})
	require.NoError(t, err)
	require.NotNil(t, cOut.TenantDatabase)
	assert.Equal(t, "tenant1", aws.ToString(cOut.TenantDatabase.TenantDBName))
	assert.Equal(t, "available", aws.ToString(cOut.TenantDatabase.Status))
	t.Cleanup(func() {
		_, _ = c.DeleteTenantDatabase(ctx, &rds.DeleteTenantDatabaseInput{
			DBInstanceIdentifier: aws.String(instID), TenantDBName: aws.String("tenant1")})
	})

	dOut, err := c.DescribeTenantDatabases(ctx, &rds.DescribeTenantDatabasesInput{
		DBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	require.Len(t, dOut.TenantDatabases, 1)

	mOut, err := c.ModifyTenantDatabase(ctx, &rds.ModifyTenantDatabaseInput{
		DBInstanceIdentifier: aws.String(instID),
		TenantDBName:         aws.String("tenant1"),
		NewTenantDBName:      aws.String("tenant1renamed"),
	})
	require.NoError(t, err)
	assert.Equal(t, "tenant1renamed", aws.ToString(mOut.TenantDatabase.TenantDBName))
	t.Cleanup(func() {
		_, _ = c.DeleteTenantDatabase(ctx, &rds.DeleteTenantDatabaseInput{
			DBInstanceIdentifier: aws.String(instID), TenantDBName: aws.String("tenant1renamed")})
	})
}

// TestRDS_ShardGroups covers the Aurora Limitless shard-group family.
func TestRDS_ShardGroups(t *testing.T) {
	c := rdsClient()

	clusterID := "rext-limitless-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-postgresql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true)})
	})

	sgID := "rext-shard-group"
	cOut, err := c.CreateDBShardGroup(ctx, &rds.CreateDBShardGroupInput{
		DBShardGroupIdentifier: aws.String(sgID),
		DBClusterIdentifier:    aws.String(clusterID),
		MaxACU:                 aws.Float64(16),
		MinACU:                 aws.Float64(2),
	})
	require.NoError(t, err)
	assert.Equal(t, sgID, aws.ToString(cOut.DBShardGroupIdentifier))
	assert.Equal(t, float64(16), aws.ToFloat64(cOut.MaxACU))
	t.Cleanup(func() {
		_, _ = c.DeleteDBShardGroup(ctx, &rds.DeleteDBShardGroupInput{
			DBShardGroupIdentifier: aws.String(sgID)})
	})

	dOut, err := c.DescribeDBShardGroups(ctx, &rds.DescribeDBShardGroupsInput{
		DBShardGroupIdentifier: aws.String(sgID),
	})
	require.NoError(t, err)
	require.Len(t, dOut.DBShardGroups, 1)
	assert.Equal(t, clusterID, aws.ToString(dOut.DBShardGroups[0].DBClusterIdentifier))

	mOut, err := c.ModifyDBShardGroup(ctx, &rds.ModifyDBShardGroupInput{
		DBShardGroupIdentifier: aws.String(sgID),
		MaxACU:                 aws.Float64(32),
	})
	require.NoError(t, err)
	assert.Equal(t, float64(32), aws.ToFloat64(mOut.MaxACU))

	_, err = c.RebootDBShardGroup(ctx, &rds.RebootDBShardGroupInput{
		DBShardGroupIdentifier: aws.String(sgID),
	})
	require.NoError(t, err)
}

// TestRDS_ActivityStreams covers the per-cluster database activity
// stream toggle.
func TestRDS_ActivityStreams(t *testing.T) {
	c := rdsClient()
	arn := "arn:aws:rds:us-east-1:123456789012:cluster:das-cluster"

	stOut, err := c.StartActivityStream(ctx, &rds.StartActivityStreamInput{
		ResourceArn:      aws.String(arn),
		Mode:             rdstypes.ActivityStreamModeAsync,
		KmsKeyId:         aws.String("arn:aws:kms:us-east-1:123456789012:key/abc"),
		ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, rdstypes.ActivityStreamStatusStarting, stOut.Status)

	mOut, err := c.ModifyActivityStream(ctx, &rds.ModifyActivityStreamInput{
		ResourceArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.NotEmpty(t, mOut.Status)

	_, err = c.StopActivityStream(ctx, &rds.StopActivityStreamInput{
		ResourceArn: aws.String(arn),
	})
	require.NoError(t, err)
}

// TestRDS_Backtrack covers Aurora backtrack record + describe.
func TestRDS_Backtrack(t *testing.T) {
	c := rdsClient()
	clusterID := "rext-backtrack-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
		BacktrackWindow:     aws.Int64(3600),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true)})
	})

	btOut, err := c.BacktrackDBCluster(ctx, &rds.BacktrackDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		BacktrackTo:         aws.Time(time.Now().Add(-time.Hour)),
	})
	require.NoError(t, err)
	assert.Equal(t, clusterID, aws.ToString(btOut.DBClusterIdentifier))
	require.NotEmpty(t, aws.ToString(btOut.BacktrackIdentifier))

	dOut, err := c.DescribeDBClusterBacktracks(ctx, &rds.DescribeDBClusterBacktracksInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	require.Len(t, dOut.DBClusterBacktracks, 1)
	assert.Equal(t, "COMPLETED", aws.ToString(dOut.DBClusterBacktracks[0].Status))
}

// TestRDS_ExportTasks covers the snapshot-export task family.
func TestRDS_ExportTasks(t *testing.T) {
	c := rdsClient()
	src := "arn:aws:rds:us-east-1:123456789012:snapshot:exp-snap"
	taskID := "rext-export-task"
	sOut, err := c.StartExportTask(ctx, &rds.StartExportTaskInput{
		ExportTaskIdentifier: aws.String(taskID),
		SourceArn:            aws.String(src),
		S3BucketName:         aws.String("exports-bucket"),
		IamRoleArn:           aws.String("arn:aws:iam::123456789012:role/export"),
		KmsKeyId:             aws.String("arn:aws:kms:us-east-1:123456789012:key/exp"),
	})
	require.NoError(t, err)
	assert.Equal(t, taskID, aws.ToString(sOut.ExportTaskIdentifier))
	assert.Equal(t, "COMPLETE", aws.ToString(sOut.Status))

	dOut, err := c.DescribeExportTasks(ctx, &rds.DescribeExportTasksInput{
		ExportTaskIdentifier: aws.String(taskID),
	})
	require.NoError(t, err)
	require.Len(t, dOut.ExportTasks, 1)
	assert.Equal(t, "exports-bucket", aws.ToString(dOut.ExportTasks[0].S3Bucket))

	cOut, err := c.CancelExportTask(ctx, &rds.CancelExportTaskInput{
		ExportTaskIdentifier: aws.String(taskID),
	})
	require.NoError(t, err)
	assert.Equal(t, "CANCELING", aws.ToString(cOut.Status))
}

// TestRDS_ClusterOps covers the assorted cluster operations: reboot,
// reset cluster param group, modify cluster endpoint, global-cluster
// failover/remove, promote replica cluster, and HTTP-endpoint toggle.
func TestRDS_ClusterOps(t *testing.T) {
	c := rdsClient()

	clusterID := "rext-ops-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	clusterArn := ""
	if d, e := c.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)}); e == nil && len(d.DBClusters) == 1 {
		clusterArn = aws.ToString(d.DBClusters[0].DBClusterArn)
	}
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true)})
	})

	rbOut, err := c.RebootDBCluster(ctx, &rds.RebootDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	assert.Equal(t, clusterID, aws.ToString(rbOut.DBCluster.DBClusterIdentifier))

	// Reset cluster parameter group.
	pgName := "rext-cluster-pg"
	_, err = c.CreateDBClusterParameterGroup(ctx, &rds.CreateDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(pgName),
		DBParameterGroupFamily:      aws.String("aurora-mysql8.0"),
		Description:                 aws.String("rext"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterParameterGroup(ctx, &rds.DeleteDBClusterParameterGroupInput{
			DBClusterParameterGroupName: aws.String(pgName)})
	})
	resetOut, err := c.ResetDBClusterParameterGroup(ctx, &rds.ResetDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(pgName),
		ResetAllParameters:          aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, pgName, aws.ToString(resetOut.DBClusterParameterGroupName))

	// Modify cluster endpoint.
	epID := "rext-custom-ep"
	_, err = c.CreateDBClusterEndpoint(ctx, &rds.CreateDBClusterEndpointInput{
		DBClusterIdentifier:         aws.String(clusterID),
		DBClusterEndpointIdentifier: aws.String(epID),
		EndpointType:                aws.String("READER"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterEndpoint(ctx, &rds.DeleteDBClusterEndpointInput{
			DBClusterEndpointIdentifier: aws.String(epID)})
	})
	meOut, err := c.ModifyDBClusterEndpoint(ctx, &rds.ModifyDBClusterEndpointInput{
		DBClusterEndpointIdentifier: aws.String(epID),
		EndpointType:                aws.String("ANY"),
	})
	require.NoError(t, err)
	assert.Equal(t, epID, aws.ToString(meOut.DBClusterEndpointIdentifier))

	// PromoteReadReplicaDBCluster.
	prOut, err := c.PromoteReadReplicaDBCluster(ctx, &rds.PromoteReadReplicaDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	assert.Equal(t, clusterID, aws.ToString(prOut.DBCluster.DBClusterIdentifier))

	// Enable/Disable HTTP endpoint (Aurora Serverless v1 Data API).
	if clusterArn != "" {
		enOut, err := c.EnableHttpEndpoint(ctx, &rds.EnableHttpEndpointInput{
			ResourceArn: aws.String(clusterArn),
		})
		require.NoError(t, err)
		assert.True(t, aws.ToBool(enOut.HttpEndpointEnabled))
		diOut, err := c.DisableHttpEndpoint(ctx, &rds.DisableHttpEndpointInput{
			ResourceArn: aws.String(clusterArn),
		})
		require.NoError(t, err)
		assert.False(t, aws.ToBool(diOut.HttpEndpointEnabled))
	}

	// Global cluster failover/remove.
	gcID := "rext-global-cluster"
	_, err = c.CreateGlobalCluster(ctx, &rds.CreateGlobalClusterInput{
		GlobalClusterIdentifier:   aws.String(gcID),
		SourceDBClusterIdentifier: aws.String(clusterArn),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteGlobalCluster(ctx, &rds.DeleteGlobalClusterInput{
			GlobalClusterIdentifier: aws.String(gcID)})
	})
	if clusterArn != "" {
		fgOut, err := c.FailoverGlobalCluster(ctx, &rds.FailoverGlobalClusterInput{
			GlobalClusterIdentifier:   aws.String(gcID),
			TargetDbClusterIdentifier: aws.String(clusterArn),
		})
		require.NoError(t, err)
		require.NotNil(t, fgOut.GlobalCluster)

		rmOut, err := c.RemoveFromGlobalCluster(ctx, &rds.RemoveFromGlobalClusterInput{
			GlobalClusterIdentifier: aws.String(gcID),
			DbClusterIdentifier:     aws.String(clusterArn),
		})
		require.NoError(t, err)
		require.NotNil(t, rmOut.GlobalCluster)
		assert.Empty(t, rmOut.GlobalCluster.GlobalClusterMembers)
	}
}

// TestRDS_SnapshotAttributesAndModify covers cluster-snapshot
// attributes and ModifyDBSnapshot.
func TestRDS_SnapshotAttributesAndModify(t *testing.T) {
	c := rdsClient()

	clusterID := "rext-snapattr-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true)})
	})
	csID := "rext-snapattr-snap"
	_, err = c.CreateDBClusterSnapshot(ctx, &rds.CreateDBClusterSnapshotInput{
		DBClusterSnapshotIdentifier: aws.String(csID),
		DBClusterIdentifier:         aws.String(clusterID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
			DBClusterSnapshotIdentifier: aws.String(csID)})
	})

	_, err = c.ModifyDBClusterSnapshotAttribute(ctx, &rds.ModifyDBClusterSnapshotAttributeInput{
		DBClusterSnapshotIdentifier: aws.String(csID),
		AttributeName:               aws.String("restore"),
		ValuesToAdd:                 []string{"123456789012", "all"},
	})
	require.NoError(t, err)

	daOut, err := c.DescribeDBClusterSnapshotAttributes(ctx, &rds.DescribeDBClusterSnapshotAttributesInput{
		DBClusterSnapshotIdentifier: aws.String(csID),
	})
	require.NoError(t, err)
	require.NotNil(t, daOut.DBClusterSnapshotAttributesResult)
	require.Len(t, daOut.DBClusterSnapshotAttributesResult.DBClusterSnapshotAttributes, 1)
	assert.ElementsMatch(t, []string{"123456789012", "all"},
		daOut.DBClusterSnapshotAttributesResult.DBClusterSnapshotAttributes[0].AttributeValues)

	// ModifyDBSnapshot needs an instance snapshot.
	instID := "rext-snapmod-instance"
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("mysql"),
		EngineVersion:        aws.String("8.0.39"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID), SkipFinalSnapshot: aws.Bool(true)})
	})
	snapID := "rext-snapmod-snap"
	_, err = c.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(snapID),
		DBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String(snapID)})
	})
	msOut, err := c.ModifyDBSnapshot(ctx, &rds.ModifyDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(snapID),
		EngineVersion:        aws.String("8.0.40"),
	})
	require.NoError(t, err)
	assert.Equal(t, "8.0.40", aws.ToString(msOut.DBSnapshot.EngineVersion))
}

// TestRDS_StaticDescribes covers the catalog/default describe ops.
func TestRDS_StaticDescribes(t *testing.T) {
	c := rdsClient()

	ogo, err := c.DescribeOptionGroupOptions(ctx, &rds.DescribeOptionGroupOptionsInput{
		EngineName: aws.String("mysql"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, ogo.OptionGroupOptions)
	assert.Equal(t, "mysql", aws.ToString(ogo.OptionGroupOptions[0].EngineName))

	edp, err := c.DescribeEngineDefaultParameters(ctx, &rds.DescribeEngineDefaultParametersInput{
		DBParameterGroupFamily: aws.String("mysql8.0"),
	})
	require.NoError(t, err)
	require.NotNil(t, edp.EngineDefaults)
	assert.Equal(t, "mysql8.0", aws.ToString(edp.EngineDefaults.DBParameterGroupFamily))
	require.NotEmpty(t, edp.EngineDefaults.Parameters)

	edcp, err := c.DescribeEngineDefaultClusterParameters(ctx, &rds.DescribeEngineDefaultClusterParametersInput{
		DBParameterGroupFamily: aws.String("aurora-mysql8.0"),
	})
	require.NoError(t, err)
	require.NotNil(t, edcp.EngineDefaults)
	require.NotEmpty(t, edcp.EngineDefaults.Parameters)

	sr, err := c.DescribeSourceRegions(ctx, &rds.DescribeSourceRegionsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, sr.SourceRegions)

	mev, err := c.DescribeDBMajorEngineVersions(ctx, &rds.DescribeDBMajorEngineVersionsInput{
		Engine: aws.String("mysql"),
	})
	require.NoError(t, err)
	require.Len(t, mev.DBMajorEngineVersions, 1)
	assert.Equal(t, "8.0", aws.ToString(mev.DBMajorEngineVersions[0].MajorEngineVersion))
	require.NotEmpty(t, mev.DBMajorEngineVersions[0].SupportedEngineLifecycles)
}
