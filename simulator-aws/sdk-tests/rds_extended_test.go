package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDS_ClusterSnapshotAndParamGroup exercises the DB cluster
// snapshot family (CreateDBClusterSnapshot → DescribeDBClusterSnapshots
// → DeleteDBClusterSnapshot) and the DB cluster parameter group family
// (CreateDBClusterParameterGroup → DescribeDBClusterParameterGroups →
// DeleteDBClusterParameterGroup), including tag round-trips via
// ListTagsForResource for each new resource ARN type.
func TestRDS_ClusterSnapshotAndParamGroup(t *testing.T) {
	c := rdsClient()

	clusterID := "ext-aurora-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier:   aws.String(clusterID),
		Engine:                aws.String("aurora-mysql"),
		MasterUsername:        aws.String("admin"),
		MasterUserPassword:    aws.String("password123!"),
		BackupRetentionPeriod: aws.Int32(3),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	// --- DB cluster snapshot ---
	snapID := "ext-cluster-snap"
	csOut, err := c.CreateDBClusterSnapshot(ctx, &rds.CreateDBClusterSnapshotInput{
		DBClusterSnapshotIdentifier: aws.String(snapID),
		DBClusterIdentifier:         aws.String(clusterID),
		Tags: []rdstypes.Tag{
			{Key: aws.String("team"), Value: aws.String("db")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, csOut.DBClusterSnapshot)
	assert.Equal(t, snapID, aws.ToString(csOut.DBClusterSnapshot.DBClusterSnapshotIdentifier))
	assert.Equal(t, clusterID, aws.ToString(csOut.DBClusterSnapshot.DBClusterIdentifier))
	assert.Equal(t, "available", aws.ToString(csOut.DBClusterSnapshot.Status))
	assert.Equal(t, "aurora-mysql", aws.ToString(csOut.DBClusterSnapshot.Engine))
	csArn := aws.ToString(csOut.DBClusterSnapshot.DBClusterSnapshotArn)
	require.NotEmpty(t, csArn)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
			DBClusterSnapshotIdentifier: aws.String(snapID),
		})
	})

	descCS, err := c.DescribeDBClusterSnapshots(ctx, &rds.DescribeDBClusterSnapshotsInput{
		DBClusterSnapshotIdentifier: aws.String(snapID),
	})
	require.NoError(t, err)
	require.Len(t, descCS.DBClusterSnapshots, 1)
	assert.Equal(t, "manual", aws.ToString(descCS.DBClusterSnapshots[0].SnapshotType))

	// Tag round-trip on the cluster-snapshot ARN.
	tagsCS, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{
		ResourceName: aws.String(csArn),
	})
	require.NoError(t, err)
	require.Len(t, tagsCS.TagList, 1)
	assert.Equal(t, "team", aws.ToString(tagsCS.TagList[0].Key))

	delCS, err := c.DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
		DBClusterSnapshotIdentifier: aws.String(snapID),
	})
	require.NoError(t, err)
	assert.Equal(t, "deleted", aws.ToString(delCS.DBClusterSnapshot.Status))

	// --- DB cluster parameter group ---
	pgName := "ext-cluster-pg"
	cpgOut, err := c.CreateDBClusterParameterGroup(ctx, &rds.CreateDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(pgName),
		DBParameterGroupFamily:      aws.String("aurora-mysql8.0"),
		Description:                 aws.String("ext cluster pg"),
		Tags: []rdstypes.Tag{
			{Key: aws.String("env"), Value: aws.String("test")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, cpgOut.DBClusterParameterGroup)
	assert.Equal(t, pgName, aws.ToString(cpgOut.DBClusterParameterGroup.DBClusterParameterGroupName))
	assert.Equal(t, "aurora-mysql8.0", aws.ToString(cpgOut.DBClusterParameterGroup.DBParameterGroupFamily))
	cpgArn := aws.ToString(cpgOut.DBClusterParameterGroup.DBClusterParameterGroupArn)
	require.NotEmpty(t, cpgArn)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterParameterGroup(ctx, &rds.DeleteDBClusterParameterGroupInput{
			DBClusterParameterGroupName: aws.String(pgName),
		})
	})

	descCPG, err := c.DescribeDBClusterParameterGroups(ctx, &rds.DescribeDBClusterParameterGroupsInput{
		DBClusterParameterGroupName: aws.String(pgName),
	})
	require.NoError(t, err)
	require.Len(t, descCPG.DBClusterParameterGroups, 1)

	tagsCPG, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{
		ResourceName: aws.String(cpgArn),
	})
	require.NoError(t, err)
	require.Len(t, tagsCPG.TagList, 1)

	_, err = c.DeleteDBClusterParameterGroup(ctx, &rds.DeleteDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(pgName),
	})
	require.NoError(t, err)
}

// TestRDS_OptionGroupLifecycle exercises CreateOptionGroup →
// DescribeOptionGroups → tag round-trip → DeleteOptionGroup.
func TestRDS_OptionGroupLifecycle(t *testing.T) {
	c := rdsClient()
	name := "ext-option-group"

	createOut, err := c.CreateOptionGroup(ctx, &rds.CreateOptionGroupInput{
		OptionGroupName:        aws.String(name),
		EngineName:             aws.String("mysql"),
		MajorEngineVersion:     aws.String("8.0"),
		OptionGroupDescription: aws.String("ext option group"),
		Tags: []rdstypes.Tag{
			{Key: aws.String("env"), Value: aws.String("test")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.OptionGroup)
	assert.Equal(t, name, aws.ToString(createOut.OptionGroup.OptionGroupName))
	assert.Equal(t, "mysql", aws.ToString(createOut.OptionGroup.EngineName))
	assert.Equal(t, "8.0", aws.ToString(createOut.OptionGroup.MajorEngineVersion))
	ogArn := aws.ToString(createOut.OptionGroup.OptionGroupArn)
	require.NotEmpty(t, ogArn)
	t.Cleanup(func() {
		_, _ = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{
			OptionGroupName: aws.String(name),
		})
	})

	descOut, err := c.DescribeOptionGroups(ctx, &rds.DescribeOptionGroupsInput{
		OptionGroupName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, descOut.OptionGroupsList, 1)
	assert.Equal(t, name, aws.ToString(descOut.OptionGroupsList[0].OptionGroupName))

	tags, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{
		ResourceName: aws.String(ogArn),
	})
	require.NoError(t, err)
	require.Len(t, tags.TagList, 1)
	assert.Equal(t, "env", aws.ToString(tags.TagList[0].Key))

	_, err = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{
		OptionGroupName: aws.String(name),
	})
	require.NoError(t, err)
}

// TestRDS_ReadReplicaAndCopySnapshot exercises CreateDBInstanceReadReplica
// (with source/replica back-linking) and CopyDBSnapshot.
func TestRDS_ReadReplicaAndCopySnapshot(t *testing.T) {
	c := rdsClient()

	srcID := "ext-replica-src"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(srcID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("mysql"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(srcID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	replicaID := "ext-replica"
	repOut, err := c.CreateDBInstanceReadReplica(ctx, &rds.CreateDBInstanceReadReplicaInput{
		DBInstanceIdentifier:       aws.String(replicaID),
		SourceDBInstanceIdentifier: aws.String(srcID),
	})
	require.NoError(t, err)
	require.NotNil(t, repOut.DBInstance)
	assert.Equal(t, replicaID, aws.ToString(repOut.DBInstance.DBInstanceIdentifier))
	assert.Equal(t, srcID, aws.ToString(repOut.DBInstance.ReadReplicaSourceDBInstanceIdentifier))
	assert.Equal(t, "mysql", aws.ToString(repOut.DBInstance.Engine))
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(replicaID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	// Source now lists the replica.
	descSrc, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(srcID),
	})
	require.NoError(t, err)
	require.Len(t, descSrc.DBInstances, 1)
	assert.Contains(t, descSrc.DBInstances[0].ReadReplicaDBInstanceIdentifiers, replicaID)

	// --- CopyDBSnapshot ---
	srcSnap := "ext-copy-src-snap"
	_, err = c.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(srcSnap),
		DBInstanceIdentifier: aws.String(srcID),
		Tags: []rdstypes.Tag{
			{Key: aws.String("src"), Value: aws.String("yes")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String(srcSnap),
		})
	})

	copyDst := "ext-copy-dst-snap"
	copyOut, err := c.CopyDBSnapshot(ctx, &rds.CopyDBSnapshotInput{
		SourceDBSnapshotIdentifier: aws.String(srcSnap),
		TargetDBSnapshotIdentifier: aws.String(copyDst),
		CopyTags:                   aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, copyOut.DBSnapshot)
	assert.Equal(t, copyDst, aws.ToString(copyOut.DBSnapshot.DBSnapshotIdentifier))
	assert.Equal(t, "available", aws.ToString(copyOut.DBSnapshot.Status))
	assert.NotEmpty(t, aws.ToString(copyOut.DBSnapshot.SourceDBSnapshotIdentifier))
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String(copyDst),
		})
	})

	// CopyTags carried the source tag onto the copy.
	tags, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{
		ResourceName: aws.String(aws.ToString(copyOut.DBSnapshot.DBSnapshotArn)),
	})
	require.NoError(t, err)
	keys := map[string]bool{}
	for _, tg := range tags.TagList {
		keys[aws.ToString(tg.Key)] = true
	}
	assert.True(t, keys["src"], "CopyTags should carry source tags to the copy")
}

// TestRDS_EventsAndEngineMetadata exercises DescribeEvents,
// DescribeEventCategories, DescribeDBEngineVersions, and
// DescribeOrderableDBInstanceOptions.
func TestRDS_EventsAndEngineMetadata(t *testing.T) {
	c := rdsClient()

	cats, err := c.DescribeEventCategories(ctx, &rds.DescribeEventCategoriesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, cats.EventCategoriesMapList)
	foundInstance := false
	for _, m := range cats.EventCategoriesMapList {
		if aws.ToString(m.SourceType) == "db-instance" {
			foundInstance = true
			assert.NotEmpty(t, m.EventCategories)
		}
	}
	assert.True(t, foundInstance, "db-instance source type must be present")

	// Filtered DescribeEventCategories.
	catsFiltered, err := c.DescribeEventCategories(ctx, &rds.DescribeEventCategoriesInput{
		SourceType: aws.String("db-cluster"),
	})
	require.NoError(t, err)
	require.Len(t, catsFiltered.EventCategoriesMapList, 1)
	assert.Equal(t, "db-cluster", aws.ToString(catsFiltered.EventCategoriesMapList[0].SourceType))

	// DescribeEvents: empty list for an unknown source is valid.
	events, err := c.DescribeEvents(ctx, &rds.DescribeEventsInput{})
	require.NoError(t, err)
	assert.NotNil(t, events) // Events may be empty; shape must parse.

	// DescribeEvents for a known instance surfaces a creation event.
	evInst := "ext-events-inst"
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(evInst),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(evInst),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})
	evOut, err := c.DescribeEvents(ctx, &rds.DescribeEventsInput{
		SourceIdentifier: aws.String(evInst),
		SourceType:       rdstypes.SourceTypeDbInstance,
	})
	require.NoError(t, err)
	require.Len(t, evOut.Events, 1)
	assert.Equal(t, evInst, aws.ToString(evOut.Events[0].SourceIdentifier))

	// DescribeDBEngineVersions.
	versions, err := c.DescribeDBEngineVersions(ctx, &rds.DescribeDBEngineVersionsInput{
		Engine: aws.String("postgres"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, versions.DBEngineVersions)
	for _, v := range versions.DBEngineVersions {
		assert.Equal(t, "postgres", aws.ToString(v.Engine))
		assert.NotEmpty(t, aws.ToString(v.EngineVersion))
		assert.NotEmpty(t, aws.ToString(v.DBParameterGroupFamily))
	}

	// DescribeOrderableDBInstanceOptions.
	orderable, err := c.DescribeOrderableDBInstanceOptions(ctx, &rds.DescribeOrderableDBInstanceOptionsInput{
		Engine:          aws.String("mysql"),
		DBInstanceClass: aws.String("db.t3.micro"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, orderable.OrderableDBInstanceOptions)
	assert.Equal(t, "mysql", aws.ToString(orderable.OrderableDBInstanceOptions[0].Engine))
	assert.Equal(t, "db.t3.micro", aws.ToString(orderable.OrderableDBInstanceOptions[0].DBInstanceClass))
}
