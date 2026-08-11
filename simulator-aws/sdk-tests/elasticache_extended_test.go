package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCache_Snapshots covers CreateSnapshot, DescribeSnapshots,
// CopySnapshot, and DeleteSnapshot against a real cache cluster.
func TestElastiCache_Snapshots(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	clusterID := "snap-src-cluster"
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

	snapName := "snap-one"
	snap, err := c.CreateSnapshot(ctx, &elasticache.CreateSnapshotInput{
		SnapshotName:   aws.String(snapName),
		CacheClusterId: aws.String(clusterID),
	})
	require.NoError(t, err)
	require.NotNil(t, snap.Snapshot)
	assert.Equal(t, snapName, aws.ToString(snap.Snapshot.SnapshotName))
	assert.Equal(t, clusterID, aws.ToString(snap.Snapshot.CacheClusterId))
	assert.Equal(t, "manual", aws.ToString(snap.Snapshot.SnapshotSource))
	t.Cleanup(func() {
		_, _ = c.DeleteSnapshot(ctx, &elasticache.DeleteSnapshotInput{SnapshotName: aws.String(snapName)})
	})

	desc, err := c.DescribeSnapshots(ctx, &elasticache.DescribeSnapshotsInput{
		SnapshotName: aws.String(snapName),
	})
	require.NoError(t, err)
	require.Len(t, desc.Snapshots, 1)
	assert.Equal(t, "redis", aws.ToString(desc.Snapshots[0].Engine))

	copyName := "snap-copy"
	cp, err := c.CopySnapshot(ctx, &elasticache.CopySnapshotInput{
		SourceSnapshotName: aws.String(snapName),
		TargetSnapshotName: aws.String(copyName),
	})
	require.NoError(t, err)
	require.NotNil(t, cp.Snapshot)
	assert.Equal(t, copyName, aws.ToString(cp.Snapshot.SnapshotName))
	t.Cleanup(func() {
		_, _ = c.DeleteSnapshot(ctx, &elasticache.DeleteSnapshotInput{SnapshotName: aws.String(copyName)})
	})

	_, err = c.DeleteSnapshot(ctx, &elasticache.DeleteSnapshotInput{SnapshotName: aws.String(snapName)})
	require.NoError(t, err)
	_, err = c.DescribeSnapshots(ctx, &elasticache.DescribeSnapshotsInput{SnapshotName: aws.String(snapName)})
	assertAWSAPIErrorCode(t, err, "SnapshotNotFoundFault")
}

// TestElastiCache_UsersAndGroups covers CreateUser, DescribeUsers,
// ModifyUser, DeleteUser and the matching user-group operations.
func TestElastiCache_UsersAndGroups(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	userID := "rbac-user-1"
	user, err := c.CreateUser(ctx, &elasticache.CreateUserInput{
		UserId:       aws.String(userID),
		UserName:     aws.String("app-user"),
		Engine:       aws.String("redis"),
		AccessString: aws.String("on ~* +@all"),
		Passwords:    []string{"AVeryStrongPassword123"},
	})
	require.NoError(t, err)
	assert.Equal(t, userID, aws.ToString(user.UserId))
	assert.Equal(t, "active", aws.ToString(user.Status))
	require.NotNil(t, user.Authentication)
	assert.Equal(t, ectypes.AuthenticationTypePassword, user.Authentication.Type)
	t.Cleanup(func() {
		_, _ = c.DeleteUser(ctx, &elasticache.DeleteUserInput{UserId: aws.String(userID)})
	})

	du, err := c.DescribeUsers(ctx, &elasticache.DescribeUsersInput{UserId: aws.String(userID)})
	require.NoError(t, err)
	require.Len(t, du.Users, 1)
	assert.Equal(t, "app-user", aws.ToString(du.Users[0].UserName))

	mu, err := c.ModifyUser(ctx, &elasticache.ModifyUserInput{
		UserId:       aws.String(userID),
		AccessString: aws.String("on ~app:* +@read"),
	})
	require.NoError(t, err)
	assert.Equal(t, "on ~app:* +@read", aws.ToString(mu.AccessString))

	groupID := "rbac-group-1"
	grp, err := c.CreateUserGroup(ctx, &elasticache.CreateUserGroupInput{
		UserGroupId: aws.String(groupID),
		Engine:      aws.String("redis"),
		UserIds:     []string{userID},
	})
	require.NoError(t, err)
	assert.Equal(t, groupID, aws.ToString(grp.UserGroupId))
	assert.Contains(t, grp.UserIds, userID)
	t.Cleanup(func() {
		_, _ = c.DeleteUserGroup(ctx, &elasticache.DeleteUserGroupInput{UserGroupId: aws.String(groupID)})
	})

	dg, err := c.DescribeUserGroups(ctx, &elasticache.DescribeUserGroupsInput{UserGroupId: aws.String(groupID)})
	require.NoError(t, err)
	require.Len(t, dg.UserGroups, 1)
	assert.Contains(t, dg.UserGroups[0].UserIds, userID)

	mg, err := c.ModifyUserGroup(ctx, &elasticache.ModifyUserGroupInput{
		UserGroupId:     aws.String(groupID),
		UserIdsToRemove: []string{userID},
	})
	require.NoError(t, err)
	assert.NotContains(t, mg.UserIds, userID)

	_, err = c.DeleteUserGroup(ctx, &elasticache.DeleteUserGroupInput{UserGroupId: aws.String(groupID)})
	require.NoError(t, err)
	_, err = c.DescribeUserGroups(ctx, &elasticache.DescribeUserGroupsInput{UserGroupId: aws.String(groupID)})
	assertAWSAPIErrorCode(t, err, "UserGroupNotFoundFault")

	_, err = c.DeleteUser(ctx, &elasticache.DeleteUserInput{UserId: aws.String(userID)})
	require.NoError(t, err)
	_, err = c.DescribeUsers(ctx, &elasticache.DescribeUsersInput{UserId: aws.String(userID)})
	assertAWSAPIErrorCode(t, err, "UserNotFoundFault")
}

// TestElastiCache_ParameterDetail covers DescribeCacheParameters,
// ModifyCacheParameterGroup, ResetCacheParameterGroup, and
// DescribeEngineDefaultParameters.
func TestElastiCache_ParameterDetail(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	pgName := "param-detail-group"
	_, err := c.CreateCacheParameterGroup(ctx, &elasticache.CreateCacheParameterGroupInput{
		CacheParameterGroupName:   aws.String(pgName),
		CacheParameterGroupFamily: aws.String("redis7"),
		Description:               aws.String("param detail"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheParameterGroup(ctx, &elasticache.DeleteCacheParameterGroupInput{
			CacheParameterGroupName: aws.String(pgName),
		})
	})

	dp, err := c.DescribeCacheParameters(ctx, &elasticache.DescribeCacheParametersInput{
		CacheParameterGroupName: aws.String(pgName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, dp.Parameters)

	_, err = c.ModifyCacheParameterGroup(ctx, &elasticache.ModifyCacheParameterGroupInput{
		CacheParameterGroupName: aws.String(pgName),
		ParameterNameValues: []ectypes.ParameterNameValue{
			{ParameterName: aws.String("maxmemory-policy"), ParameterValue: aws.String("allkeys-lru")},
		},
	})
	require.NoError(t, err)

	dp2, err := c.DescribeCacheParameters(ctx, &elasticache.DescribeCacheParametersInput{
		CacheParameterGroupName: aws.String(pgName),
	})
	require.NoError(t, err)
	found := false
	for _, p := range dp2.Parameters {
		if aws.ToString(p.ParameterName) == "maxmemory-policy" {
			assert.Equal(t, "allkeys-lru", aws.ToString(p.ParameterValue))
			assert.Equal(t, "user", aws.ToString(p.Source))
			found = true
		}
	}
	assert.True(t, found, "modified parameter present")

	_, err = c.ResetCacheParameterGroup(ctx, &elasticache.ResetCacheParameterGroupInput{
		CacheParameterGroupName: aws.String(pgName),
		ResetAllParameters:      aws.Bool(true),
	})
	require.NoError(t, err)

	ed, err := c.DescribeEngineDefaultParameters(ctx, &elasticache.DescribeEngineDefaultParametersInput{
		CacheParameterGroupFamily: aws.String("redis7"),
	})
	require.NoError(t, err)
	require.NotNil(t, ed.EngineDefaults)
	assert.Equal(t, "redis7", aws.ToString(ed.EngineDefaults.CacheParameterGroupFamily))
	require.NotEmpty(t, ed.EngineDefaults.Parameters)
}

// TestElastiCache_DescribeReadOnly covers the read-only describe surfaces:
// events, engine versions, reserved nodes, offerings, service updates, and
// cache security groups.
func TestElastiCache_DescribeReadOnly(t *testing.T) {
	c := ecClient()
	ctx := t.Context()

	clusterID := "evt-cluster"
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

	ev, err := c.DescribeEvents(ctx, &elasticache.DescribeEventsInput{
		SourceType:       ectypes.SourceTypeCacheCluster,
		SourceIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, ev.Events)
	assert.Equal(t, clusterID, aws.ToString(ev.Events[0].SourceIdentifier))

	cev, err := c.DescribeCacheEngineVersions(ctx, &elasticache.DescribeCacheEngineVersionsInput{
		Engine: aws.String("redis"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, cev.CacheEngineVersions)
	assert.Equal(t, "redis", aws.ToString(cev.CacheEngineVersions[0].Engine))

	rn, err := c.DescribeReservedCacheNodes(ctx, &elasticache.DescribeReservedCacheNodesInput{})
	require.NoError(t, err)
	assert.Empty(t, rn.ReservedCacheNodes)

	off, err := c.DescribeReservedCacheNodesOfferings(ctx, &elasticache.DescribeReservedCacheNodesOfferingsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, off.ReservedCacheNodesOfferings)
	assert.NotEmpty(t, aws.ToString(off.ReservedCacheNodesOfferings[0].CacheNodeType))

	su, err := c.DescribeServiceUpdates(ctx, &elasticache.DescribeServiceUpdatesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, su.ServiceUpdates)
	assert.NotEmpty(t, aws.ToString(su.ServiceUpdates[0].ServiceUpdateName))

	sg, err := c.DescribeCacheSecurityGroups(ctx, &elasticache.DescribeCacheSecurityGroupsInput{})
	require.NoError(t, err)
	assert.Empty(t, sg.CacheSecurityGroups)
}
