package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRDS_DBClusterLifecycle(t *testing.T) {
	t.Parallel()
	c := rdsClient()
	id := "test-aurora-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier:   aws.String(id),
		Engine:                aws.String("aurora-postgresql"),
		EngineVersion:         aws.String("16.6"),
		MasterUsername:        aws.String("admin"),
		MasterUserPassword:    aws.String("password123!"),
		DatabaseName:          aws.String("appdb"),
		BackupRetentionPeriod: aws.Int32(7),
		StorageEncrypted:      aws.Bool(true),
		Tags: []rdstypes.Tag{
			{Key: aws.String("env"), Value: aws.String("test")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(id),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	desc, err := c.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(id),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBClusters, 1)
	got := desc.DBClusters[0]
	assert.Equal(t, "aurora-postgresql", aws.ToString(got.Engine))
	assert.Equal(t, "available", aws.ToString(got.Status))
	assert.Equal(t, "appdb", aws.ToString(got.DatabaseName))
	assert.Equal(t, int32(5432), aws.ToInt32(got.Port))
	assert.True(t, aws.ToBool(got.StorageEncrypted))
	assert.Equal(t, int32(7), aws.ToInt32(got.BackupRetentionPeriod))
	assert.NotEmpty(t, aws.ToString(got.Endpoint))
	assert.NotEmpty(t, aws.ToString(got.ReaderEndpoint))
	clusterARN := aws.ToString(got.DBClusterArn)
	require.NotEmpty(t, clusterARN)

	tags, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{
		ResourceName: aws.String(clusterARN),
	})
	require.NoError(t, err)
	require.Len(t, tags.TagList, 1)
	assert.Equal(t, "env", aws.ToString(tags.TagList[0].Key))

	_, err = c.ModifyDBCluster(ctx, &rds.ModifyDBClusterInput{
		DBClusterIdentifier:   aws.String(id),
		BackupRetentionPeriod: aws.Int32(14),
		DeletionProtection:    aws.Bool(true),
		ApplyImmediately:      aws.Bool(true),
	})
	require.NoError(t, err)

	desc2, err := c.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(id),
	})
	require.NoError(t, err)
	require.Len(t, desc2.DBClusters, 1)
	assert.Equal(t, int32(14), aws.ToInt32(desc2.DBClusters[0].BackupRetentionPeriod))
	assert.True(t, aws.ToBool(desc2.DBClusters[0].DeletionProtection))

	_, err = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier: aws.String(id),
		SkipFinalSnapshot:   aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
	require.ErrorContains(t, err, "disable deletion protection", "Terraform's aws_rds_cluster matches this text to lift the protection")

	_, err = c.ModifyDBCluster(ctx, &rds.ModifyDBClusterInput{
		DBClusterIdentifier: aws.String(id),
		DeletionProtection:  aws.Bool(false),
		ApplyImmediately:    aws.Bool(true),
	})
	require.NoError(t, err)
	_, err = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier: aws.String(id),
		SkipFinalSnapshot:   aws.Bool(true),
	})
	require.NoError(t, err)

	_, err = c.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(id),
	})
	assertAWSAPIErrorCode(t, err, "DBClusterNotFoundFault")
}

func TestRDS_RebootDBInstance(t *testing.T) {
	t.Parallel()
	c := rdsClient()
	id := "test-reboot-db"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(id),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("mysql"),
		MasterUsername:        aws.String("admin"),
		MasterUserPassword:    aws.String("password123!"),
		AllocatedStorage:      aws.Int32(20),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(id),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})
	waitForRDSInstanceAvailable(t, c, ctx, id)

	out, err := c.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{
		DBInstanceIdentifier: aws.String(id),
	})
	require.NoError(t, err)
	require.NotNil(t, out.DBInstance)
	assert.Equal(t, id, aws.ToString(out.DBInstance.DBInstanceIdentifier))

	_, err = c.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{
		DBInstanceIdentifier: aws.String("no-such-db"),
	})
	assertAWSAPIErrorCode(t, err, "DBInstanceNotFound")
}

func TestRDS_DBSubnetGroupLifecycle(t *testing.T) {
	t.Parallel()
	c := rdsClient()
	name := "test-subnet-group"
	vpcID, subnets := createECSTestVPCSubnets(t, name, 2)
	otherVPC, otherSubnets := createECSTestVPCSubnets(t, name+"-other", 3)
	_, oneZone := createECSTestVPCSubnets(t, name+"-one-zone", 1)

	// A subnet group's subnets must exist, share a VPC and cover two Availability
	// Zones.
	for _, refusal := range []struct {
		subnets []string
		code    string
	}{
		{[]string{subnets[0], "subnet-0f1e2d3c4b5a69788"}, "InvalidSubnet"},
		{[]string{subnets[0], otherSubnets[1]}, "InvalidSubnet"},
		{oneZone, "DBSubnetGroupDoesNotCoverEnoughAZs"},
	} {
		_, err := c.CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{
			DBSubnetGroupName:        aws.String(name),
			DBSubnetGroupDescription: aws.String("sim subnet group"),
			SubnetIds:                refusal.subnets,
		})
		assertAWSAPIErrorCode(t, err, refusal.code)
	}

	created, err := c.CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{
		DBSubnetGroupName:        aws.String(name),
		DBSubnetGroupDescription: aws.String("sim subnet group"),
		SubnetIds:                subnets,
		Tags: []rdstypes.Tag{
			{Key: aws.String("team"), Value: aws.String("data")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, vpcID, aws.ToString(created.DBSubnetGroup.VpcId), "the group's VPC is its subnets' VPC")
	t.Cleanup(func() {
		_, _ = c.DeleteDBSubnetGroup(ctx, &rds.DeleteDBSubnetGroupInput{
			DBSubnetGroupName: aws.String(name),
		})
	})

	desc, err := c.DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{
		DBSubnetGroupName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBSubnetGroups, 1)
	got := desc.DBSubnetGroups[0]
	assert.Equal(t, "sim subnet group", aws.ToString(got.DBSubnetGroupDescription))
	assert.Equal(t, "Complete", aws.ToString(got.SubnetGroupStatus))
	require.Len(t, got.Subnets, 2)
	assert.Equal(t, subnets[0], aws.ToString(got.Subnets[0].SubnetIdentifier))
	require.NotNil(t, got.Subnets[0].SubnetAvailabilityZone)
	assert.Equal(t, "us-east-1a", aws.ToString(got.Subnets[0].SubnetAvailabilityZone.Name), "each subnet reports the Availability Zone Amazon EC2 holds it in")
	assert.Equal(t, "us-east-1b", aws.ToString(got.Subnets[1].SubnetAvailabilityZone.Name))
	groupARN := aws.ToString(got.DBSubnetGroupArn)
	require.NotEmpty(t, groupARN)

	tags, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{
		ResourceName: aws.String(groupARN),
	})
	require.NoError(t, err)
	require.Len(t, tags.TagList, 1)
	assert.Equal(t, "team", aws.ToString(tags.TagList[0].Key))

	_, err = c.ModifyDBSubnetGroup(ctx, &rds.ModifyDBSubnetGroupInput{
		DBSubnetGroupName:        aws.String(name),
		DBSubnetGroupDescription: aws.String("updated description"),
		SubnetIds:                otherSubnets,
	})
	require.NoError(t, err)

	desc2, err := c.DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{
		DBSubnetGroupName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc2.DBSubnetGroups, 1)
	assert.Equal(t, "updated description", aws.ToString(desc2.DBSubnetGroups[0].DBSubnetGroupDescription))
	require.Len(t, desc2.DBSubnetGroups[0].Subnets, 3)
	assert.Equal(t, otherVPC, aws.ToString(desc2.DBSubnetGroups[0].VpcId), "the modified group moves to its new subnets' VPC")

	_, err = c.DeleteDBSubnetGroup(ctx, &rds.DeleteDBSubnetGroupInput{
		DBSubnetGroupName: aws.String(name),
	})
	require.NoError(t, err)

	_, err = c.DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{
		DBSubnetGroupName: aws.String(name),
	})
	assertAWSAPIErrorCode(t, err, "DBSubnetGroupNotFoundFault")
}

func TestRDS_DBParameterGroupLifecycle(t *testing.T) {
	t.Parallel()
	c := rdsClient()
	name := "test-param-group"
	_, err := c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String(name),
		DBParameterGroupFamily: aws.String("postgres16"),
		Description:            aws.String("sim parameter group"),
		Tags: []rdstypes.Tag{
			{Key: aws.String("cost-center"), Value: aws.String("eng")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBParameterGroup(ctx, &rds.DeleteDBParameterGroupInput{
			DBParameterGroupName: aws.String(name),
		})
	})

	desc, err := c.DescribeDBParameterGroups(ctx, &rds.DescribeDBParameterGroupsInput{
		DBParameterGroupName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBParameterGroups, 1)
	got := desc.DBParameterGroups[0]
	assert.Equal(t, "postgres16", aws.ToString(got.DBParameterGroupFamily))
	assert.Equal(t, "sim parameter group", aws.ToString(got.Description))
	groupARN := aws.ToString(got.DBParameterGroupArn)
	require.NotEmpty(t, groupARN)

	tags, err := c.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{
		ResourceName: aws.String(groupARN),
	})
	require.NoError(t, err)
	require.Len(t, tags.TagList, 1)
	assert.Equal(t, "cost-center", aws.ToString(tags.TagList[0].Key))

	_, err = c.DeleteDBParameterGroup(ctx, &rds.DeleteDBParameterGroupInput{
		DBParameterGroupName: aws.String(name),
	})
	require.NoError(t, err)

	_, err = c.DescribeDBParameterGroups(ctx, &rds.DescribeDBParameterGroupsInput{
		DBParameterGroupName: aws.String(name),
	})
	assertAWSAPIErrorCode(t, err, "DBParameterGroupNotFound")
}
