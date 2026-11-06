package aws_sdk_test

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecAwaitSnapshot polls the snapshot's SnapshotStatus, all ElastiCache offers
// to wait on: the SDK has no waiter for a snapshot.
func ecAwaitSnapshot(t *testing.T, c *elasticache.Client, name string) ectypes.Snapshot {
	t.Helper()
	deadline := time.Now().Add(ecEngineWait)
	for {
		out, err := c.DescribeSnapshots(t.Context(), &elasticache.DescribeSnapshotsInput{SnapshotName: aws.String(name)})
		require.NoError(t, err)
		require.Len(t, out.Snapshots, 1)
		got := out.Snapshots[0]
		status := aws.ToString(got.SnapshotStatus)
		if status == "available" {
			return got
		}
		require.Contains(t, []string{"creating", "restoring"}, status, "snapshot %s", name)
		require.True(t, time.Now().Before(deadline), "snapshot %s did not become available", name)
		time.Sleep(time.Second)
	}
}

func ecAwaitServerlessSnapshot(t *testing.T, c *elasticache.Client, name string) ectypes.ServerlessCacheSnapshot {
	t.Helper()
	deadline := time.Now().Add(ecEngineWait)
	for {
		out, err := c.DescribeServerlessCacheSnapshots(t.Context(), &elasticache.DescribeServerlessCacheSnapshotsInput{
			ServerlessCacheSnapshotName: aws.String(name),
		})
		require.NoError(t, err)
		require.Len(t, out.ServerlessCacheSnapshots, 1)
		got := out.ServerlessCacheSnapshots[0]
		if aws.ToString(got.Status) == "available" {
			return got
		}
		require.Equal(t, "creating", aws.ToString(got.Status), "serverless cache snapshot %s", name)
		require.True(t, time.Now().Before(deadline), "serverless cache snapshot %s did not become available", name)
		time.Sleep(time.Second)
	}
}

func ecRequireKeys(t *testing.T, client goredis.Cmdable, prefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		value, err := client.Get(t.Context(), fmt.Sprintf("%s-%d", prefix, i)).Result()
		require.NoError(t, err)
		assert.Equal(t, strconv.Itoa(i), value)
	}
}

// TestElastiCache_SnapshotRestore snapshots a Redis OSS cache cluster's data
// and restores it into a cache cluster, a replication group and a serverless
// cache, then exports a serverless snapshot to Amazon S3 and seeds a cache
// cluster from the exported RDB file.
func TestElastiCache_SnapshotRestore(t *testing.T) {
	t.Parallel()
	c := ecClient()
	ctx := t.Context()

	memcached := uniquename.New("snap-memcached")
	_, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(memcached), Engine: aws.String("memcached"),
		CacheNodeType: aws.String("cache.t3.micro"), NumCacheNodes: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(memcached)})
	})
	_, err = c.CreateSnapshot(ctx, &elasticache.CreateSnapshotInput{SnapshotName: aws.String(uniquename.New("snap-mc")), CacheClusterId: aws.String(memcached)})
	assertAWSAPIErrorCode(t, err, "SnapshotFeatureNotSupportedFault")

	sourceID := uniquename.New("snap-src")
	_, err = c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(sourceID), Engine: aws.String("redis"),
		CacheNodeType: aws.String("cache.t3.micro"), NumCacheNodes: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(sourceID)})
	})
	// The cluster stays creating until its node's container runs, which
	// outlasts the next request.
	snapName := uniquename.New("snap-data")
	_, err = c.CreateSnapshot(ctx, &elasticache.CreateSnapshotInput{SnapshotName: aws.String(snapName), CacheClusterId: aws.String(sourceID)})
	assertAWSAPIErrorCode(t, err, "InvalidCacheClusterState")

	source := redisClient(t, &goredis.Options{Addr: ecAddress(ecAwaitCluster(t, c, sourceID).CacheNodes[0].Endpoint)})
	for i := 0; i < 20; i++ {
		require.NoError(t, source.Set(ctx, fmt.Sprintf("snap-%d", i), i, 0).Err())
	}
	require.NoError(t, source.Set(ctx, "snap-expiring", "soon", time.Hour).Err())

	snap, err := c.CreateSnapshot(ctx, &elasticache.CreateSnapshotInput{SnapshotName: aws.String(snapName), CacheClusterId: aws.String(sourceID)})
	require.NoError(t, err)
	assert.Equal(t, "creating", aws.ToString(snap.Snapshot.SnapshotStatus))
	assert.Equal(t, "manual", aws.ToString(snap.Snapshot.SnapshotSource))
	t.Cleanup(func() {
		_, _ = c.DeleteSnapshot(ctx, &elasticache.DeleteSnapshotInput{SnapshotName: aws.String(snapName)})
	})
	taken := ecAwaitSnapshot(t, c, snapName)
	require.Len(t, taken.NodeSnapshots, 1)
	assert.Equal(t, sourceID, aws.ToString(taken.NodeSnapshots[0].CacheClusterId))
	assert.Equal(t, "0001", aws.ToString(taken.NodeSnapshots[0].NodeGroupId))
	require.NoError(t, source.Set(ctx, "after-the-snapshot", "unseen", 0).Err())

	_, err = c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(uniquename.New("snap-missing")), CacheNodeType: aws.String("cache.t3.micro"),
		NumCacheNodes: aws.Int32(1), SnapshotName: aws.String(uniquename.New("no-such-snapshot")),
	})
	assertAWSAPIErrorCode(t, err, "SnapshotNotFoundFault")

	restoredID := uniquename.New("snap-restored")
	_, err = c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(restoredID), CacheNodeType: aws.String("cache.t3.micro"),
		NumCacheNodes: aws.Int32(1), SnapshotName: aws.String(snapName),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(restoredID)})
	})
	restored := ecAwaitCluster(t, c, restoredID)
	assert.Equal(t, "redis", aws.ToString(restored.Engine), "the restore takes the snapshot's engine")
	restoredClient := redisClient(t, &goredis.Options{Addr: ecAddress(restored.CacheNodes[0].Endpoint)})
	ecRequireKeys(t, restoredClient, "snap", 20)
	ttl, err := restoredClient.TTL(ctx, "snap-expiring").Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0), "the key keeps its time to live")
	assert.Equal(t, goredis.Nil, restoredClient.Get(ctx, "after-the-snapshot").Err(), "a write after the snapshot is not in it")
	assert.Equal(t, "available", aws.ToString(ecAwaitSnapshot(t, c, snapName).SnapshotStatus))

	copyName := uniquename.New("snap-copy")
	_, err = c.CopySnapshot(ctx, &elasticache.CopySnapshotInput{SourceSnapshotName: aws.String(snapName), TargetSnapshotName: aws.String(copyName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteSnapshot(ctx, &elasticache.DeleteSnapshotInput{SnapshotName: aws.String(copyName)})
	})
	_, err = c.DeleteSnapshot(ctx, &elasticache.DeleteSnapshotInput{SnapshotName: aws.String(snapName)})
	require.NoError(t, err)
	_, err = c.DescribeSnapshots(ctx, &elasticache.DescribeSnapshotsInput{SnapshotName: aws.String(snapName)})
	assertAWSAPIErrorCode(t, err, "SnapshotNotFoundFault")

	groupID := uniquename.New("snap-group")
	_, err = c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId: aws.String(groupID), ReplicationGroupDescription: aws.String("restored from a copied snapshot"),
		CacheNodeType: aws.String("cache.t3.micro"), NumCacheClusters: aws.Int32(2), SnapshotName: aws.String(copyName),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(groupID)})
	})
	group := ecAwaitReplGroup(t, c, groupID)
	ecRequireKeys(t, redisClient(t, &goredis.Options{Addr: ecAddress(group.NodeGroups[0].PrimaryEndpoint)}), "snap", 20)
	ecRequireKeys(t, redisClient(t, &goredis.Options{Addr: ecAddress(group.NodeGroups[0].ReaderEndpoint)}), "snap", 20)

	copied, err := c.DescribeSnapshots(ctx, &elasticache.DescribeSnapshotsInput{SnapshotName: aws.String(copyName)})
	require.NoError(t, err)
	require.Len(t, copied.Snapshots, 1)
	cacheName := uniquename.New("snap-serverless")
	_, err = c.CreateServerlessCache(ctx, &elasticache.CreateServerlessCacheInput{
		ServerlessCacheName: aws.String(cacheName), Engine: aws.String("valkey"),
		SnapshotArnsToRestore: []string{aws.ToString(copied.Snapshots[0].ARN)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCache(ctx, &elasticache.DeleteServerlessCacheInput{ServerlessCacheName: aws.String(cacheName)})
	})
	cache := ecAwaitServerless(t, c, cacheName)
	serverless := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(cache.Endpoint)}, TLSConfig: ecClientTLS()})
	t.Cleanup(func() { serverless.Close() })
	ecRequireKeys(t, serverless, "snap", 20)
	require.NoError(t, serverless.Set(ctx, "serverless-only", "kept", 0).Err())

	slSnap := uniquename.New("sl-snap-data")
	created, err := c.CreateServerlessCacheSnapshot(ctx, &elasticache.CreateServerlessCacheSnapshotInput{
		ServerlessCacheSnapshotName: aws.String(slSnap), ServerlessCacheName: aws.String(cacheName),
	})
	require.NoError(t, err)
	assert.Equal(t, "creating", aws.ToString(created.ServerlessCacheSnapshot.Status))
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCacheSnapshot(ctx, &elasticache.DeleteServerlessCacheSnapshotInput{ServerlessCacheSnapshotName: aws.String(slSnap)})
	})
	slTaken := ecAwaitServerlessSnapshot(t, c, slSnap)
	size, err := strconv.Atoi(aws.ToString(slTaken.BytesUsedForCache))
	require.NoError(t, err)
	assert.Positive(t, size)

	bucket := uniquename.New("ec-export")
	s3c := s3Client()
	_, err = s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = c.ExportServerlessCacheSnapshot(ctx, &elasticache.ExportServerlessCacheSnapshotInput{
		ServerlessCacheSnapshotName: aws.String(slSnap), S3BucketName: aws.String(uniquename.New("no-such-bucket")),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.ExportServerlessCacheSnapshot(ctx, &elasticache.ExportServerlessCacheSnapshotInput{
		ServerlessCacheSnapshotName: aws.String(slSnap), S3BucketName: aws.String(bucket),
	})
	require.NoError(t, err)
	object := slSnap + "-0001.rdb"
	_, err = s3c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(object)})
	require.NoError(t, err, "the export writes the snapshot's RDB file")

	seededID := uniquename.New("snap-seeded")
	_, err = c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(seededID), Engine: aws.String("valkey"), CacheNodeType: aws.String("cache.t3.micro"),
		NumCacheNodes: aws.Int32(1), SnapshotArns: []string{"arn:aws:s3:::" + bucket + "/" + object},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(seededID)})
	})
	seeded := redisClient(t, &goredis.Options{Addr: ecAddress(ecAwaitCluster(t, c, seededID).CacheNodes[0].Endpoint)})
	ecRequireKeys(t, seeded, "snap", 20)
	value, err := seeded.Get(ctx, "serverless-only").Result()
	require.NoError(t, err)
	assert.Equal(t, "kept", value)

	finalName := uniquename.New("snap-final")
	_, err = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{
		CacheClusterId: aws.String(seededID), FinalSnapshotIdentifier: aws.String(finalName),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteSnapshot(ctx, &elasticache.DeleteSnapshotInput{SnapshotName: aws.String(finalName)})
	})
	final := ecAwaitSnapshot(t, c, finalName)
	assert.Equal(t, seededID, aws.ToString(final.CacheClusterId))
	assert.Equal(t, "valkey", aws.ToString(final.Engine))
	ecAwaitClusterDeleted(t, c, seededID)
}
