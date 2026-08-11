package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCache_Serverless covers CreateServerlessCache,
// DescribeServerlessCaches, ModifyServerlessCache, and
// DeleteServerlessCache.
func TestElastiCache_Serverless(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	name := "sl-cache-one"
	created, err := c.CreateServerlessCache(ctx, &elasticache.CreateServerlessCacheInput{
		ServerlessCacheName: aws.String(name),
		Engine:              aws.String("redis"),
		MajorEngineVersion:  aws.String("7"),
		Description:         aws.String("serverless test"),
		CacheUsageLimits: &ectypes.CacheUsageLimits{
			DataStorage: &ectypes.DataStorage{
				Maximum: aws.Int32(10),
				Unit:    ectypes.DataStorageUnitGb,
			},
			ECPUPerSecond: &ectypes.ECPUPerSecond{Maximum: aws.Int32(5000)},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created.ServerlessCache)
	assert.Equal(t, name, aws.ToString(created.ServerlessCache.ServerlessCacheName))
	assert.Equal(t, "available", aws.ToString(created.ServerlessCache.Status))
	assert.Equal(t, "redis", aws.ToString(created.ServerlessCache.Engine))
	require.NotNil(t, created.ServerlessCache.Endpoint)
	assert.Equal(t, int32(6379), aws.ToInt32(created.ServerlessCache.Endpoint.Port))
	require.NotNil(t, created.ServerlessCache.CacheUsageLimits)
	require.NotNil(t, created.ServerlessCache.CacheUsageLimits.DataStorage)
	assert.Equal(t, int32(10), aws.ToInt32(created.ServerlessCache.CacheUsageLimits.DataStorage.Maximum))
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCache(ctx, &elasticache.DeleteServerlessCacheInput{
			ServerlessCacheName: aws.String(name),
		})
	})

	desc, err := c.DescribeServerlessCaches(ctx, &elasticache.DescribeServerlessCachesInput{
		ServerlessCacheName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.ServerlessCaches, 1)
	assert.Equal(t, name, aws.ToString(desc.ServerlessCaches[0].ServerlessCacheName))

	mod, err := c.ModifyServerlessCache(ctx, &elasticache.ModifyServerlessCacheInput{
		ServerlessCacheName: aws.String(name),
		Description:         aws.String("modified desc"),
	})
	require.NoError(t, err)
	assert.Equal(t, "modified desc", aws.ToString(mod.ServerlessCache.Description))

	del, err := c.DeleteServerlessCache(ctx, &elasticache.DeleteServerlessCacheInput{
		ServerlessCacheName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, "deleting", aws.ToString(del.ServerlessCache.Status))

	_, err = c.DescribeServerlessCaches(ctx, &elasticache.DescribeServerlessCachesInput{
		ServerlessCacheName: aws.String(name),
	})
	assertAWSAPIErrorCode(t, err, "ServerlessCacheNotFoundFault")
}

// TestElastiCache_ServerlessSnapshots covers
// CreateServerlessCacheSnapshot, DescribeServerlessCacheSnapshots,
// CopyServerlessCacheSnapshot, ExportServerlessCacheSnapshot, and
// DeleteServerlessCacheSnapshot.
func TestElastiCache_ServerlessSnapshots(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	cacheName := "sl-snap-cache"
	_, err := c.CreateServerlessCache(ctx, &elasticache.CreateServerlessCacheInput{
		ServerlessCacheName: aws.String(cacheName),
		Engine:              aws.String("valkey"),
		MajorEngineVersion:  aws.String("8"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCache(ctx, &elasticache.DeleteServerlessCacheInput{
			ServerlessCacheName: aws.String(cacheName),
		})
	})

	snapName := "sl-snap-one"
	snap, err := c.CreateServerlessCacheSnapshot(ctx, &elasticache.CreateServerlessCacheSnapshotInput{
		ServerlessCacheSnapshotName: aws.String(snapName),
		ServerlessCacheName:         aws.String(cacheName),
	})
	require.NoError(t, err)
	require.NotNil(t, snap.ServerlessCacheSnapshot)
	assert.Equal(t, snapName, aws.ToString(snap.ServerlessCacheSnapshot.ServerlessCacheSnapshotName))
	assert.Equal(t, "manual", aws.ToString(snap.ServerlessCacheSnapshot.SnapshotType))
	require.NotNil(t, snap.ServerlessCacheSnapshot.ServerlessCacheConfiguration)
	assert.Equal(t, cacheName, aws.ToString(snap.ServerlessCacheSnapshot.ServerlessCacheConfiguration.ServerlessCacheName))
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCacheSnapshot(ctx, &elasticache.DeleteServerlessCacheSnapshotInput{
			ServerlessCacheSnapshotName: aws.String(snapName),
		})
	})

	descS, err := c.DescribeServerlessCacheSnapshots(ctx, &elasticache.DescribeServerlessCacheSnapshotsInput{
		ServerlessCacheSnapshotName: aws.String(snapName),
	})
	require.NoError(t, err)
	require.Len(t, descS.ServerlessCacheSnapshots, 1)

	copyName := "sl-snap-copy"
	cp, err := c.CopyServerlessCacheSnapshot(ctx, &elasticache.CopyServerlessCacheSnapshotInput{
		SourceServerlessCacheSnapshotName: aws.String(snapName),
		TargetServerlessCacheSnapshotName: aws.String(copyName),
	})
	require.NoError(t, err)
	assert.Equal(t, copyName, aws.ToString(cp.ServerlessCacheSnapshot.ServerlessCacheSnapshotName))
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCacheSnapshot(ctx, &elasticache.DeleteServerlessCacheSnapshotInput{
			ServerlessCacheSnapshotName: aws.String(copyName),
		})
	})

	exp, err := c.ExportServerlessCacheSnapshot(ctx, &elasticache.ExportServerlessCacheSnapshotInput{
		ServerlessCacheSnapshotName: aws.String(snapName),
		S3BucketName:                aws.String("my-export-bucket"),
	})
	require.NoError(t, err)
	assert.Equal(t, snapName, aws.ToString(exp.ServerlessCacheSnapshot.ServerlessCacheSnapshotName))

	_, err = c.DeleteServerlessCacheSnapshot(ctx, &elasticache.DeleteServerlessCacheSnapshotInput{
		ServerlessCacheSnapshotName: aws.String(snapName),
	})
	require.NoError(t, err)
}

// TestElastiCache_GlobalReplicationGroup covers the Global Datastore
// lifecycle: create from a primary replication group, describe, modify,
// failover, node-group increase/decrease, rebalance, disassociate, and
// delete.
func TestElastiCache_GlobalReplicationGroup(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	primary := "grg-primary"
	_, err := c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId:          aws.String(primary),
		ReplicationGroupDescription: aws.String("primary for global"),
		Engine:                      aws.String("redis"),
		CacheNodeType:               aws.String("cache.r7g.large"),
		NumCacheClusters:            aws.Int32(2),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{
			ReplicationGroupId: aws.String(primary),
		})
	})

	created, err := c.CreateGlobalReplicationGroup(ctx, &elasticache.CreateGlobalReplicationGroupInput{
		GlobalReplicationGroupIdSuffix:    aws.String("myglobal"),
		PrimaryReplicationGroupId:         aws.String(primary),
		GlobalReplicationGroupDescription: aws.String("global datastore"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.GlobalReplicationGroup)
	gid := aws.ToString(created.GlobalReplicationGroup.GlobalReplicationGroupId)
	require.NotEmpty(t, gid)
	require.Len(t, created.GlobalReplicationGroup.Members, 1)
	assert.Equal(t, "PRIMARY", aws.ToString(created.GlobalReplicationGroup.Members[0].Role))
	t.Cleanup(func() {
		_, _ = c.DeleteGlobalReplicationGroup(ctx, &elasticache.DeleteGlobalReplicationGroupInput{
			GlobalReplicationGroupId:      aws.String(gid),
			RetainPrimaryReplicationGroup: aws.Bool(true),
		})
	})

	desc, err := c.DescribeGlobalReplicationGroups(ctx, &elasticache.DescribeGlobalReplicationGroupsInput{
		GlobalReplicationGroupId: aws.String(gid),
	})
	require.NoError(t, err)
	require.Len(t, desc.GlobalReplicationGroups, 1)

	mod, err := c.ModifyGlobalReplicationGroup(ctx, &elasticache.ModifyGlobalReplicationGroupInput{
		GlobalReplicationGroupId:          aws.String(gid),
		ApplyImmediately:                  aws.Bool(true),
		GlobalReplicationGroupDescription: aws.String("updated global"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated global", aws.ToString(mod.GlobalReplicationGroup.GlobalReplicationGroupDescription))

	inc, err := c.IncreaseNodeGroupsInGlobalReplicationGroup(ctx, &elasticache.IncreaseNodeGroupsInGlobalReplicationGroupInput{
		GlobalReplicationGroupId: aws.String(gid),
		NodeGroupCount:           aws.Int32(2),
		ApplyImmediately:         aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Len(t, inc.GlobalReplicationGroup.GlobalNodeGroups, 2)

	dec, err := c.DecreaseNodeGroupsInGlobalReplicationGroup(ctx, &elasticache.DecreaseNodeGroupsInGlobalReplicationGroupInput{
		GlobalReplicationGroupId: aws.String(gid),
		NodeGroupCount:           aws.Int32(1),
		ApplyImmediately:         aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Len(t, dec.GlobalReplicationGroup.GlobalNodeGroups, 1)

	_, err = c.RebalanceSlotsInGlobalReplicationGroup(ctx, &elasticache.RebalanceSlotsInGlobalReplicationGroupInput{
		GlobalReplicationGroupId: aws.String(gid),
		ApplyImmediately:         aws.Bool(true),
	})
	require.NoError(t, err)

	fo, err := c.FailoverGlobalReplicationGroup(ctx, &elasticache.FailoverGlobalReplicationGroupInput{
		GlobalReplicationGroupId:  aws.String(gid),
		PrimaryRegion:             aws.String("us-east-1"),
		PrimaryReplicationGroupId: aws.String(primary),
	})
	require.NoError(t, err)
	require.NotNil(t, fo.GlobalReplicationGroup)

	dis, err := c.DisassociateGlobalReplicationGroup(ctx, &elasticache.DisassociateGlobalReplicationGroupInput{
		GlobalReplicationGroupId: aws.String(gid),
		ReplicationGroupId:       aws.String(primary),
		ReplicationGroupRegion:   aws.String("us-east-1"),
	})
	require.NoError(t, err)
	assert.Empty(t, dis.GlobalReplicationGroup.Members)
}

// TestElastiCache_CacheSecurityGroups covers CreateCacheSecurityGroup,
// AuthorizeCacheSecurityGroupIngress, RevokeCacheSecurityGroupIngress,
// and DeleteCacheSecurityGroup.
func TestElastiCache_CacheSecurityGroups(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	name := "csg-one"
	created, err := c.CreateCacheSecurityGroup(ctx, &elasticache.CreateCacheSecurityGroupInput{
		CacheSecurityGroupName: aws.String(name),
		Description:            aws.String("classic security group"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.CacheSecurityGroup)
	assert.Equal(t, name, aws.ToString(created.CacheSecurityGroup.CacheSecurityGroupName))
	t.Cleanup(func() {
		_, _ = c.DeleteCacheSecurityGroup(ctx, &elasticache.DeleteCacheSecurityGroupInput{
			CacheSecurityGroupName: aws.String(name),
		})
	})

	auth, err := c.AuthorizeCacheSecurityGroupIngress(ctx, &elasticache.AuthorizeCacheSecurityGroupIngressInput{
		CacheSecurityGroupName:  aws.String(name),
		EC2SecurityGroupName:    aws.String("ec2-sg-1"),
		EC2SecurityGroupOwnerId: aws.String("123456789012"),
	})
	require.NoError(t, err)
	require.Len(t, auth.CacheSecurityGroup.EC2SecurityGroups, 1)
	assert.Equal(t, "ec2-sg-1", aws.ToString(auth.CacheSecurityGroup.EC2SecurityGroups[0].EC2SecurityGroupName))

	rev, err := c.RevokeCacheSecurityGroupIngress(ctx, &elasticache.RevokeCacheSecurityGroupIngressInput{
		CacheSecurityGroupName:  aws.String(name),
		EC2SecurityGroupName:    aws.String("ec2-sg-1"),
		EC2SecurityGroupOwnerId: aws.String("123456789012"),
	})
	require.NoError(t, err)
	assert.Empty(t, rev.CacheSecurityGroup.EC2SecurityGroups)

	_, err = c.DeleteCacheSecurityGroup(ctx, &elasticache.DeleteCacheSecurityGroupInput{
		CacheSecurityGroupName: aws.String(name),
	})
	require.NoError(t, err)
}

// TestElastiCache_UpdateActions covers DescribeUpdateActions,
// BatchApplyUpdateAction, and BatchStopUpdateAction against a real
// cache cluster.
func TestElastiCache_UpdateActions(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	clusterID := "ua-cluster"
	_, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(clusterID),
		Engine:         aws.String("redis"),
		CacheNodeType:  aws.String("cache.t3.micro"),
		NumCacheNodes:  aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{
			CacheClusterId: aws.String(clusterID),
		})
	})

	svcUpdate := "elasticache-20240101-001"
	desc, err := c.DescribeUpdateActions(ctx, &elasticache.DescribeUpdateActionsInput{
		CacheClusterIds:   []string{clusterID},
		ServiceUpdateName: aws.String(svcUpdate),
	})
	require.NoError(t, err)
	require.Len(t, desc.UpdateActions, 1)
	assert.Equal(t, clusterID, aws.ToString(desc.UpdateActions[0].CacheClusterId))
	assert.Equal(t, svcUpdate, aws.ToString(desc.UpdateActions[0].ServiceUpdateName))

	applied, err := c.BatchApplyUpdateAction(ctx, &elasticache.BatchApplyUpdateActionInput{
		CacheClusterIds:   []string{clusterID},
		ServiceUpdateName: aws.String(svcUpdate),
	})
	require.NoError(t, err)
	require.Len(t, applied.ProcessedUpdateActions, 1)
	assert.Equal(t, clusterID, aws.ToString(applied.ProcessedUpdateActions[0].CacheClusterId))

	stopped, err := c.BatchStopUpdateAction(ctx, &elasticache.BatchStopUpdateActionInput{
		CacheClusterIds:   []string{clusterID},
		ServiceUpdateName: aws.String(svcUpdate),
	})
	require.NoError(t, err)
	require.Len(t, stopped.ProcessedUpdateActions, 1)
}

// TestElastiCache_ReplicaAndShardConfig covers IncreaseReplicaCount,
// DecreaseReplicaCount, ModifyReplicationGroupShardConfiguration,
// TestFailover, and the online-migration operations on an existing
// replication group.
func TestElastiCache_ReplicaAndShardConfig(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	rgID := "rsc-group"
	_, err := c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId:          aws.String(rgID),
		ReplicationGroupDescription: aws.String("replica/shard tests"),
		Engine:                      aws.String("redis"),
		CacheNodeType:               aws.String("cache.r7g.large"),
		NumCacheClusters:            aws.Int32(2),
		AutomaticFailoverEnabled:    aws.Bool(true),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{
			ReplicationGroupId: aws.String(rgID),
		})
	})

	inc, err := c.IncreaseReplicaCount(ctx, &elasticache.IncreaseReplicaCountInput{
		ReplicationGroupId: aws.String(rgID),
		NewReplicaCount:    aws.Int32(2),
		ApplyImmediately:   aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Len(t, inc.ReplicationGroup.MemberClusters, 3)

	dec, err := c.DecreaseReplicaCount(ctx, &elasticache.DecreaseReplicaCountInput{
		ReplicationGroupId: aws.String(rgID),
		NewReplicaCount:    aws.Int32(1),
		ApplyImmediately:   aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Len(t, dec.ReplicationGroup.MemberClusters, 2)

	shard, err := c.ModifyReplicationGroupShardConfiguration(ctx, &elasticache.ModifyReplicationGroupShardConfigurationInput{
		ReplicationGroupId: aws.String(rgID),
		NodeGroupCount:     aws.Int32(2),
		ApplyImmediately:   aws.Bool(true),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(shard.ReplicationGroup.ClusterEnabled))

	fo, err := c.TestFailover(ctx, &elasticache.TestFailoverInput{
		ReplicationGroupId: aws.String(rgID),
		NodeGroupId:        aws.String("0001"),
	})
	require.NoError(t, err)
	require.NotNil(t, fo.ReplicationGroup)

	sm, err := c.StartMigration(ctx, &elasticache.StartMigrationInput{
		ReplicationGroupId: aws.String(rgID),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{
			{Address: aws.String("10.0.0.1"), Port: aws.Int32(6379)},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, sm.ReplicationGroup)

	tm, err := c.TestMigration(ctx, &elasticache.TestMigrationInput{
		ReplicationGroupId: aws.String(rgID),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{
			{Address: aws.String("10.0.0.1"), Port: aws.Int32(6379)},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, tm.ReplicationGroup)

	cm, err := c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{
		ReplicationGroupId: aws.String(rgID),
	})
	require.NoError(t, err)
	assert.Equal(t, "available", aws.ToString(cm.ReplicationGroup.Status))
}

// TestElastiCache_NodeTypeModificationsAndReserved covers
// ListAllowedNodeTypeModifications and
// PurchaseReservedCacheNodesOffering.
func TestElastiCache_NodeTypeModificationsAndReserved(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	allowed, err := c.ListAllowedNodeTypeModifications(ctx, &elasticache.ListAllowedNodeTypeModificationsInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, allowed.ScaleUpModifications)
	assert.NotEmpty(t, allowed.ScaleDownModifications)

	purchased, err := c.PurchaseReservedCacheNodesOffering(ctx, &elasticache.PurchaseReservedCacheNodesOfferingInput{
		ReservedCacheNodesOfferingId: aws.String("649fd0c8-cf6d-47a0-bfa6-060f8e75e95f"),
		ReservedCacheNodeId:          aws.String("my-reservation"),
		CacheNodeCount:               aws.Int32(1),
	})
	require.NoError(t, err)
	require.NotNil(t, purchased.ReservedCacheNode)
	assert.Equal(t, "my-reservation", aws.ToString(purchased.ReservedCacheNode.ReservedCacheNodeId))
	assert.Equal(t, int32(1), aws.ToInt32(purchased.ReservedCacheNode.CacheNodeCount))
}
