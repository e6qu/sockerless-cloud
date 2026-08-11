package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rdsClient() *rds.Client {
	return rds.NewFromConfig(sdkConfig(), func(o *rds.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func ecClient() *elasticache.Client {
	return elasticache.NewFromConfig(sdkConfig(), func(o *elasticache.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestRDS_DBInstanceLifecycle(t *testing.T) {
	c := rdsClient()
	id := "test-pg-db"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(id),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		EngineVersion:        aws.String("15.4"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(id),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	desc, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(id),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBInstances, 1)
	got := desc.DBInstances[0]
	assert.Equal(t, "postgres", aws.ToString(got.Engine))
	assert.Equal(t, "available", aws.ToString(got.DBInstanceStatus))
	assert.NotNil(t, got.Endpoint)
	assert.Positive(t, aws.ToInt32(got.Endpoint.Port),
		"the PostgreSQL data-plane endpoint must expose a concrete port")

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id),
		DBInstanceClass:      aws.String("db.t3.small"),
	})
	require.NoError(t, err)

	desc2, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(id),
	})
	require.NoError(t, err)
	require.Len(t, desc2.DBInstances, 1)
	assert.Equal(t, "db.t3.small", aws.ToString(desc2.DBInstances[0].DBInstanceClass))

	_, err = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(id),
		SkipFinalSnapshot:    aws.Bool(true),
	})
	require.NoError(t, err)

	_, err = c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(id),
	})
	assertAWSAPIErrorCode(t, err, "DBInstanceNotFound")
}

func assertAWSAPIErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "expected smithy.APIError")
	assert.Equal(t, code, apiErr.ErrorCode())
}

func TestElastiCache_ClusterLifecycle(t *testing.T) {
	c := ecClient()
	id := "test-redis-cluster"
	_, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(id),
		CacheNodeType:  aws.String("cache.t3.micro"),
		Engine:         aws.String("redis"),
		EngineVersion:  aws.String("7.0"),
		NumCacheNodes:  aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{
			CacheClusterId: aws.String(id),
		})
	})

	desc, err := c.DescribeCacheClusters(ctx, &elasticache.DescribeCacheClustersInput{
		CacheClusterId: aws.String(id),
	})
	require.NoError(t, err)
	require.Len(t, desc.CacheClusters, 1)
	got := desc.CacheClusters[0]
	assert.Equal(t, "redis", aws.ToString(got.Engine))
	assert.Equal(t, "available", aws.ToString(got.CacheClusterStatus))
	require.NotNil(t, got.ConfigurationEndpoint)
	assert.Equal(t, int32(6379), aws.ToInt32(got.ConfigurationEndpoint.Port),
		"redis engine should map to port 6379")

	_, err = c.ModifyCacheCluster(ctx, &elasticache.ModifyCacheClusterInput{
		CacheClusterId: aws.String(id),
		CacheNodeType:  aws.String("cache.t3.small"),
	})
	require.NoError(t, err)

	desc2, err := c.DescribeCacheClusters(ctx, &elasticache.DescribeCacheClustersInput{
		CacheClusterId: aws.String(id),
	})
	require.NoError(t, err)
	require.Len(t, desc2.CacheClusters, 1)
	assert.Equal(t, "cache.t3.small", aws.ToString(desc2.CacheClusters[0].CacheNodeType))

	arn := "arn:aws:elasticache:us-east-1:123456789012:cluster:" + id
	_, err = c.AddTagsToResource(ctx, &elasticache.AddTagsToResourceInput{
		ResourceName: aws.String(arn),
		Tags: []ectypes.Tag{
			{Key: aws.String("env"), Value: aws.String("sdk")},
			{Key: aws.String("team"), Value: aws.String("runner")},
		},
	})
	require.NoError(t, err)
	_, err = c.RemoveTagsFromResource(ctx, &elasticache.RemoveTagsFromResourceInput{
		ResourceName: aws.String(arn),
		TagKeys:      []string{"team"},
	})
	require.NoError(t, err)
	tags, err := c.ListTagsForResource(ctx, &elasticache.ListTagsForResourceInput{
		ResourceName: aws.String(arn),
	})
	require.NoError(t, err)
	tagMap := map[string]string{}
	for _, tag := range tags.TagList {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, "sdk", tagMap["env"])
	assert.NotContains(t, tagMap, "team")

	_, err = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{
		CacheClusterId: aws.String(id),
	})
	require.NoError(t, err)
	_, err = c.DescribeCacheClusters(ctx, &elasticache.DescribeCacheClustersInput{
		CacheClusterId: aws.String(id),
	})
	assertAWSAPIErrorCode(t, err, "CacheClusterNotFound")
}

func TestElastiCache_ReplicationGroupAndReboot(t *testing.T) {
	c := ecClient()

	// RebootCacheCluster on a standalone cluster.
	clusterID := "test-reboot-cluster"
	_, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(clusterID),
		CacheNodeType:  aws.String("cache.t3.micro"),
		Engine:         aws.String("redis"),
		NumCacheNodes:  aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{
			CacheClusterId: aws.String(clusterID),
		})
	})
	reboot, err := c.RebootCacheCluster(ctx, &elasticache.RebootCacheClusterInput{
		CacheClusterId:       aws.String(clusterID),
		CacheNodeIdsToReboot: []string{"0001"},
	})
	require.NoError(t, err)
	require.NotNil(t, reboot.CacheCluster)
	assert.Equal(t, "rebooting cache cluster nodes", aws.ToString(reboot.CacheCluster.CacheClusterStatus))

	// Replication group lifecycle.
	rgID := "test-repl-group"
	create, err := c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId:          aws.String(rgID),
		ReplicationGroupDescription: aws.String("sdk repl group"),
		CacheNodeType:               aws.String("cache.t3.micro"),
		Engine:                      aws.String("redis"),
		NumCacheClusters:            aws.Int32(2),
		AutomaticFailoverEnabled:    aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, create.ReplicationGroup)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{
			ReplicationGroupId: aws.String(rgID),
		})
	})
	assert.Equal(t, rgID, aws.ToString(create.ReplicationGroup.ReplicationGroupId))
	assert.Equal(t, "available", aws.ToString(create.ReplicationGroup.Status))
	assert.Equal(t, ectypes.AutomaticFailoverStatusEnabled, create.ReplicationGroup.AutomaticFailover)
	require.Len(t, create.ReplicationGroup.MemberClusters, 2)

	desc, err := c.DescribeReplicationGroups(ctx, &elasticache.DescribeReplicationGroupsInput{
		ReplicationGroupId: aws.String(rgID),
	})
	require.NoError(t, err)
	require.Len(t, desc.ReplicationGroups, 1)
	assert.Equal(t, "sdk repl group", aws.ToString(desc.ReplicationGroups[0].Description))

	mod, err := c.ModifyReplicationGroup(ctx, &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId:          aws.String(rgID),
		ReplicationGroupDescription: aws.String("modified description"),
		SnapshotRetentionLimit:      aws.Int32(7),
	})
	require.NoError(t, err)
	assert.Equal(t, "modified description", aws.ToString(mod.ReplicationGroup.Description))
	assert.Equal(t, int32(7), aws.ToInt32(mod.ReplicationGroup.SnapshotRetentionLimit))

	// Tagging a replication group ARN.
	rgARN := "arn:aws:elasticache:us-east-1:123456789012:replicationgroup:" + rgID
	_, err = c.AddTagsToResource(ctx, &elasticache.AddTagsToResourceInput{
		ResourceName: aws.String(rgARN),
		Tags:         []ectypes.Tag{{Key: aws.String("tier"), Value: aws.String("cache")}},
	})
	require.NoError(t, err)
	rgTags, err := c.ListTagsForResource(ctx, &elasticache.ListTagsForResourceInput{
		ResourceName: aws.String(rgARN),
	})
	require.NoError(t, err)
	rgTagMap := map[string]string{}
	for _, tag := range rgTags.TagList {
		rgTagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, "cache", rgTagMap["tier"])

	_, err = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{
		ReplicationGroupId: aws.String(rgID),
	})
	require.NoError(t, err)
	_, err = c.DescribeReplicationGroups(ctx, &elasticache.DescribeReplicationGroupsInput{
		ReplicationGroupId: aws.String(rgID),
	})
	assertAWSAPIErrorCode(t, err, "ReplicationGroupNotFoundFault")
}

func TestElastiCache_SubnetAndParameterGroups(t *testing.T) {
	c := ecClient()

	// Cache subnet group lifecycle.
	sgName := "test-cache-subnet-group"
	csg, err := c.CreateCacheSubnetGroup(ctx, &elasticache.CreateCacheSubnetGroupInput{
		CacheSubnetGroupName:        aws.String(sgName),
		CacheSubnetGroupDescription: aws.String("sdk subnet group"),
		SubnetIds:                   []string{"subnet-aaaa1111", "subnet-bbbb2222"},
	})
	require.NoError(t, err)
	require.NotNil(t, csg.CacheSubnetGroup)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheSubnetGroup(ctx, &elasticache.DeleteCacheSubnetGroupInput{
			CacheSubnetGroupName: aws.String(sgName),
		})
	})
	assert.Equal(t, sgName, aws.ToString(csg.CacheSubnetGroup.CacheSubnetGroupName))
	require.Len(t, csg.CacheSubnetGroup.Subnets, 2)

	descSG, err := c.DescribeCacheSubnetGroups(ctx, &elasticache.DescribeCacheSubnetGroupsInput{
		CacheSubnetGroupName: aws.String(sgName),
	})
	require.NoError(t, err)
	require.Len(t, descSG.CacheSubnetGroups, 1)
	assert.Equal(t, "sdk subnet group", aws.ToString(descSG.CacheSubnetGroups[0].CacheSubnetGroupDescription))

	_, err = c.ModifyCacheSubnetGroup(ctx, &elasticache.ModifyCacheSubnetGroupInput{
		CacheSubnetGroupName:        aws.String(sgName),
		CacheSubnetGroupDescription: aws.String("modified subnet desc"),
	})
	require.NoError(t, err)
	descSG2, err := c.DescribeCacheSubnetGroups(ctx, &elasticache.DescribeCacheSubnetGroupsInput{
		CacheSubnetGroupName: aws.String(sgName),
	})
	require.NoError(t, err)
	require.Len(t, descSG2.CacheSubnetGroups, 1)
	assert.Equal(t, "modified subnet desc", aws.ToString(descSG2.CacheSubnetGroups[0].CacheSubnetGroupDescription))

	_, err = c.DeleteCacheSubnetGroup(ctx, &elasticache.DeleteCacheSubnetGroupInput{
		CacheSubnetGroupName: aws.String(sgName),
	})
	require.NoError(t, err)
	_, err = c.DescribeCacheSubnetGroups(ctx, &elasticache.DescribeCacheSubnetGroupsInput{
		CacheSubnetGroupName: aws.String(sgName),
	})
	assertAWSAPIErrorCode(t, err, "CacheSubnetGroupNotFoundFault")

	// Cache parameter group lifecycle.
	pgName := "test-cache-param-group"
	cpg, err := c.CreateCacheParameterGroup(ctx, &elasticache.CreateCacheParameterGroupInput{
		CacheParameterGroupName:   aws.String(pgName),
		CacheParameterGroupFamily: aws.String("redis7"),
		Description:               aws.String("sdk param group"),
	})
	require.NoError(t, err)
	require.NotNil(t, cpg.CacheParameterGroup)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheParameterGroup(ctx, &elasticache.DeleteCacheParameterGroupInput{
			CacheParameterGroupName: aws.String(pgName),
		})
	})
	assert.Equal(t, "redis7", aws.ToString(cpg.CacheParameterGroup.CacheParameterGroupFamily))

	descPG, err := c.DescribeCacheParameterGroups(ctx, &elasticache.DescribeCacheParameterGroupsInput{
		CacheParameterGroupName: aws.String(pgName),
	})
	require.NoError(t, err)
	require.Len(t, descPG.CacheParameterGroups, 1)
	assert.Equal(t, "sdk param group", aws.ToString(descPG.CacheParameterGroups[0].Description))

	_, err = c.DeleteCacheParameterGroup(ctx, &elasticache.DeleteCacheParameterGroupInput{
		CacheParameterGroupName: aws.String(pgName),
	})
	require.NoError(t, err)
	_, err = c.DescribeCacheParameterGroups(ctx, &elasticache.DescribeCacheParameterGroupsInput{
		CacheParameterGroupName: aws.String(pgName),
	})
	assertAWSAPIErrorCode(t, err, "CacheParameterGroupNotFoundFault")
}
