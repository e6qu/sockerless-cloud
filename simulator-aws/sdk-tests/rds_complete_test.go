package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDS_CustomEngineVersions covers the custom DB engine version
// (CEV) lifecycle: CreateCustomDBEngineVersion → ModifyCustomDBEngineVersion
// → DeleteCustomDBEngineVersion, all of which return a DBEngineVersion.
func TestRDS_CustomEngineVersions(t *testing.T) {
	c := rdsClient()
	engine := "custom-oracle-ee"
	version := "19.cev.cust.1"
	t.Cleanup(func() {
		_, _ = c.DeleteCustomDBEngineVersion(ctx, &rds.DeleteCustomDBEngineVersionInput{
			Engine:        aws.String(engine),
			EngineVersion: aws.String(version),
		})
	})

	created, err := c.CreateCustomDBEngineVersion(ctx, &rds.CreateCustomDBEngineVersionInput{
		Engine:        aws.String(engine),
		EngineVersion: aws.String(version),
		Description:   aws.String("custom oracle build"),
		Tags: []rdstypes.Tag{
			{Key: aws.String("team"), Value: aws.String("db")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, engine, aws.ToString(created.Engine))
	assert.Equal(t, version, aws.ToString(created.EngineVersion))
	assert.Equal(t, "available", aws.ToString(created.Status))
	require.NotEmpty(t, aws.ToString(created.DBEngineVersionArn))

	modified, err := c.ModifyCustomDBEngineVersion(ctx, &rds.ModifyCustomDBEngineVersionInput{
		Engine:        aws.String(engine),
		EngineVersion: aws.String(version),
		Status:        rdstypes.CustomEngineVersionStatusInactive,
		Description:   aws.String("deactivated"),
	})
	require.NoError(t, err)
	assert.Equal(t, "inactive", aws.ToString(modified.Status))
	assert.Equal(t, "deactivated", aws.ToString(modified.DBEngineVersionDescription))

	deleted, err := c.DeleteCustomDBEngineVersion(ctx, &rds.DeleteCustomDBEngineVersionInput{
		Engine:        aws.String(engine),
		EngineVersion: aws.String(version),
	})
	require.NoError(t, err)
	assert.Equal(t, version, aws.ToString(deleted.EngineVersion))
}

// TestRDS_DBRecommendations covers DescribeDBRecommendations (which
// surfaces a recommendation scoped to a DB instance ARN) and
// ModifyDBRecommendation (which flips the recommendation status).
func TestRDS_DBRecommendations(t *testing.T) {
	c := rdsClient()
	instID := "rec-pg-db"
	created, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		Engine:               aws.String("postgres"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
	})
	require.NoError(t, err)
	instArn := aws.ToString(created.DBInstance.DBInstanceArn)
	require.NotEmpty(t, instArn)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	desc, err := c.DescribeDBRecommendations(ctx, &rds.DescribeDBRecommendationsInput{
		Filters: []rdstypes.Filter{
			{Name: aws.String("db-instance-arn"), Values: []string{instArn}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, desc.DBRecommendations)
	rec := desc.DBRecommendations[0]
	assert.Equal(t, instArn, aws.ToString(rec.ResourceArn))
	assert.Equal(t, "active", aws.ToString(rec.Status))
	recID := aws.ToString(rec.RecommendationId)
	require.NotEmpty(t, recID)

	mod, err := c.ModifyDBRecommendation(ctx, &rds.ModifyDBRecommendationInput{
		RecommendationId: aws.String(recID),
		Status:           aws.String("dismissed"),
	})
	require.NoError(t, err)
	require.NotNil(t, mod.DBRecommendation)
	assert.Equal(t, "dismissed", aws.ToString(mod.DBRecommendation.Status))
}

// TestRDS_SnapshotTenantDatabases covers
// DescribeDBSnapshotTenantDatabases — it reads the tenant databases off
// the source instance of a stored snapshot.
func TestRDS_SnapshotTenantDatabases(t *testing.T) {
	c := rdsClient()
	instID := "stdb-oracle-db"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		Engine:               aws.String("oracle-ee"),
		DBInstanceClass:      aws.String("db.t3.medium"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	tenant := "TENANT1"
	_, err = c.CreateTenantDatabase(ctx, &rds.CreateTenantDatabaseInput{
		DBInstanceIdentifier: aws.String(instID),
		TenantDBName:         aws.String(tenant),
		MasterUsername:       aws.String("tadmin"),
		MasterUserPassword:   aws.String("password123!"),
	})
	require.NoError(t, err)

	snapID := "stdb-snap"
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

	out, err := c.DescribeDBSnapshotTenantDatabases(ctx, &rds.DescribeDBSnapshotTenantDatabasesInput{
		DBSnapshotIdentifier: aws.String(snapID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.DBSnapshotTenantDatabases)
	td := out.DBSnapshotTenantDatabases[0]
	assert.Equal(t, snapID, aws.ToString(td.DBSnapshotIdentifier))
	assert.Equal(t, tenant, aws.ToString(td.TenantDBName))
	assert.Equal(t, instID, aws.ToString(td.DBInstanceIdentifier))
}

// TestRDS_ServerlessV2PlatformVersions covers
// DescribeServerlessV2PlatformVersions, returning AWS's real default
// platform-version shape.
func TestRDS_ServerlessV2PlatformVersions(t *testing.T) {
	c := rdsClient()
	out, err := c.DescribeServerlessV2PlatformVersions(ctx, &rds.DescribeServerlessV2PlatformVersionsInput{
		Engine: aws.String("aurora-postgresql"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.ServerlessV2PlatformVersions)
	pv := out.ServerlessV2PlatformVersions[0]
	assert.Equal(t, "aurora-postgresql", aws.ToString(pv.Engine))
	assert.Equal(t, "enabled", aws.ToString(pv.Status))
	require.NotEmpty(t, aws.ToString(pv.ServerlessV2PlatformVersion))
}

// TestRDS_ValidDBInstanceModifications covers
// DescribeValidDBInstanceModifications — it returns the real storage
// envelopes (gp2 / io1) for an existing DB instance.
func TestRDS_ValidDBInstanceModifications(t *testing.T) {
	c := rdsClient()
	instID := "vmod-pg-db"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		Engine:               aws.String("postgres"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	out, err := c.DescribeValidDBInstanceModifications(ctx, &rds.DescribeValidDBInstanceModificationsInput{
		DBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	require.NotNil(t, out.ValidDBInstanceModificationsMessage)
	storage := out.ValidDBInstanceModificationsMessage.Storage
	require.NotEmpty(t, storage)
	assert.Equal(t, "gp2", aws.ToString(storage[0].StorageType))
	require.NotEmpty(t, storage[0].StorageSize)
	assert.Equal(t, int32(20), aws.ToInt32(storage[0].StorageSize[0].From))
}

// TestRDS_CurrentDBClusterCapacity covers ModifyCurrentDBClusterCapacity
// on a Serverless v1 cluster.
func TestRDS_CurrentDBClusterCapacity(t *testing.T) {
	c := rdsClient()
	clusterID := "cap-aurora-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		EngineMode:          aws.String("serverless"),
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

	out, err := c.ModifyCurrentDBClusterCapacity(ctx, &rds.ModifyCurrentDBClusterCapacityInput{
		DBClusterIdentifier: aws.String(clusterID),
		Capacity:            aws.Int32(8),
	})
	require.NoError(t, err)
	assert.Equal(t, clusterID, aws.ToString(out.DBClusterIdentifier))
	assert.Equal(t, int32(8), aws.ToInt32(out.CurrentCapacity))
}

// TestRDS_ModifyOptionGroup covers ModifyOptionGroup adding then
// removing an option on an existing option group.
func TestRDS_ModifyOptionGroup(t *testing.T) {
	c := rdsClient()
	ogName := "mog-options"
	_, err := c.CreateOptionGroup(ctx, &rds.CreateOptionGroupInput{
		OptionGroupName:        aws.String(ogName),
		EngineName:             aws.String("mysql"),
		MajorEngineVersion:     aws.String("8.0"),
		OptionGroupDescription: aws.String("mog test"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{
			OptionGroupName: aws.String(ogName),
		})
	})

	added, err := c.ModifyOptionGroup(ctx, &rds.ModifyOptionGroupInput{
		OptionGroupName:  aws.String(ogName),
		ApplyImmediately: aws.Bool(true),
		OptionsToInclude: []rdstypes.OptionConfiguration{
			{OptionName: aws.String("MARIADB_AUDIT_PLUGIN")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, added.OptionGroup)
	require.Len(t, added.OptionGroup.Options, 1)
	assert.Equal(t, "MARIADB_AUDIT_PLUGIN", aws.ToString(added.OptionGroup.Options[0].OptionName))

	removed, err := c.ModifyOptionGroup(ctx, &rds.ModifyOptionGroupInput{
		OptionGroupName:  aws.String(ogName),
		ApplyImmediately: aws.Bool(true),
		OptionsToRemove:  []string{"MARIADB_AUDIT_PLUGIN"},
	})
	require.NoError(t, err)
	require.NotNil(t, removed.OptionGroup)
	assert.Empty(t, removed.OptionGroup.Options)
}

// TestRDS_AutomatedBackupsReplication covers the
// Start/StopDBInstanceAutomatedBackupsReplication toggle keyed on the
// source DB instance ARN.
func TestRDS_AutomatedBackupsReplication(t *testing.T) {
	c := rdsClient()
	instID := "abr-pg-db"
	created, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		Engine:               aws.String("postgres"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
	})
	require.NoError(t, err)
	srcArn := aws.ToString(created.DBInstance.DBInstanceArn)
	require.NotEmpty(t, srcArn)
	t.Cleanup(func() {
		_, _ = c.StopDBInstanceAutomatedBackupsReplication(ctx, &rds.StopDBInstanceAutomatedBackupsReplicationInput{
			SourceDBInstanceArn: aws.String(srcArn),
		})
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	start, err := c.StartDBInstanceAutomatedBackupsReplication(ctx, &rds.StartDBInstanceAutomatedBackupsReplicationInput{
		SourceDBInstanceArn:   aws.String(srcArn),
		BackupRetentionPeriod: aws.Int32(14),
	})
	require.NoError(t, err)
	require.NotNil(t, start.DBInstanceAutomatedBackup)
	assert.Equal(t, "replicating", aws.ToString(start.DBInstanceAutomatedBackup.Status))
	assert.Equal(t, int32(14), aws.ToInt32(start.DBInstanceAutomatedBackup.BackupRetentionPeriod))
	assert.Equal(t, instID, aws.ToString(start.DBInstanceAutomatedBackup.DBInstanceIdentifier))

	stop, err := c.StopDBInstanceAutomatedBackupsReplication(ctx, &rds.StopDBInstanceAutomatedBackupsReplicationInput{
		SourceDBInstanceArn: aws.String(srcArn),
	})
	require.NoError(t, err)
	require.NotNil(t, stop.DBInstanceAutomatedBackup)
	assert.Equal(t, "stopped", aws.ToString(stop.DBInstanceAutomatedBackup.Status))
}

// TestRDS_SwitchoverReadReplica covers SwitchoverReadReplica swapping
// the primary/replica roles on an existing read-replica relationship.
func TestRDS_SwitchoverReadReplica(t *testing.T) {
	c := rdsClient()
	primaryID := "sw-primary-db"
	replicaID := "sw-replica-db"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(primaryID),
		Engine:               aws.String("postgres"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(replicaID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(primaryID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	_, err = c.CreateDBInstanceReadReplica(ctx, &rds.CreateDBInstanceReadReplicaInput{
		DBInstanceIdentifier:       aws.String(replicaID),
		SourceDBInstanceIdentifier: aws.String(primaryID),
	})
	require.NoError(t, err)

	out, err := c.SwitchoverReadReplica(ctx, &rds.SwitchoverReadReplicaInput{
		DBInstanceIdentifier: aws.String(replicaID),
	})
	require.NoError(t, err)
	require.NotNil(t, out.DBInstance)
	// The former replica is now a standalone primary (no source).
	assert.Empty(t, aws.ToString(out.DBInstance.ReadReplicaSourceDBInstanceIdentifier))

	// The former primary now points at the new primary as its source.
	desc, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(primaryID),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBInstances, 1)
	assert.Equal(t, replicaID, aws.ToString(desc.DBInstances[0].ReadReplicaSourceDBInstanceIdentifier))
}

// TestRDS_SwitchoverGlobalCluster covers SwitchoverGlobalCluster
// promoting a secondary regional cluster to writer.
func TestRDS_SwitchoverGlobalCluster(t *testing.T) {
	c := rdsClient()
	writerID := "swg-writer-cluster"
	secondaryID := "swg-secondary-cluster"
	globalID := "swg-global"

	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(writerID),
		Engine:              aws.String("aurora-postgresql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	secOut, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(secondaryID),
		Engine:              aws.String("aurora-postgresql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	secArn := aws.ToString(secOut.DBCluster.DBClusterArn)
	t.Cleanup(func() {
		_, _ = c.DeleteGlobalCluster(ctx, &rds.DeleteGlobalClusterInput{
			GlobalClusterIdentifier: aws.String(globalID),
		})
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(secondaryID),
			SkipFinalSnapshot:   aws.Bool(true),
		})
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(writerID),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	_, err = c.CreateGlobalCluster(ctx, &rds.CreateGlobalClusterInput{
		GlobalClusterIdentifier:   aws.String(globalID),
		SourceDBClusterIdentifier: secOut.DBCluster.DBClusterArn,
	})
	require.NoError(t, err)
	// Adopt the writer cluster as a second member so there is a target to
	// switch over to. Re-create the global cluster with both members via
	// ModifyGlobalCluster is unavailable; instead build the global cluster
	// from the writer and switch over to the secondary's ARN.
	out, err := c.SwitchoverGlobalCluster(ctx, &rds.SwitchoverGlobalClusterInput{
		GlobalClusterIdentifier:   aws.String(globalID),
		TargetDbClusterIdentifier: aws.String(secArn),
	})
	require.NoError(t, err)
	require.NotNil(t, out.GlobalCluster)
	assert.Equal(t, globalID, aws.ToString(out.GlobalCluster.GlobalClusterIdentifier))
	require.NotEmpty(t, out.GlobalCluster.GlobalClusterMembers)
	// The target is now the writer (first member, IsWriter=true).
	assert.True(t, aws.ToBool(out.GlobalCluster.GlobalClusterMembers[0].IsWriter))
}
