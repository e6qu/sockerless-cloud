package aws_sdk_test

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/smithy-go"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecEngineWait bounds the wait for an engine: its first start pulls the engine
// image.
const ecEngineWait = 10 * time.Minute

// The waiters poll each second; an engine comes up in seconds.
const (
	ecWaiterMinDelay = time.Second
	ecWaiterMaxDelay = 2 * time.Second
)

func ecAwaitCluster(t *testing.T, c *elasticache.Client, id string) ectypes.CacheCluster {
	t.Helper()
	waiter := elasticache.NewCacheClusterAvailableWaiter(c, func(o *elasticache.CacheClusterAvailableWaiterOptions) {
		o.MinDelay, o.MaxDelay = ecWaiterMinDelay, ecWaiterMaxDelay
	})
	out, err := waiter.WaitForOutput(t.Context(), &elasticache.DescribeCacheClustersInput{
		CacheClusterId: aws.String(id), ShowCacheNodeInfo: aws.Bool(true),
	}, ecEngineWait)
	require.NoError(t, err, "cache cluster %s did not become available", id)
	require.Len(t, out.CacheClusters, 1)
	return out.CacheClusters[0]
}

func ecAwaitClusterDeleted(t *testing.T, c *elasticache.Client, id string) {
	t.Helper()
	waiter := elasticache.NewCacheClusterDeletedWaiter(c, func(o *elasticache.CacheClusterDeletedWaiterOptions) {
		o.MinDelay, o.MaxDelay = ecWaiterMinDelay, ecWaiterMaxDelay
	})
	require.NoError(t, waiter.Wait(t.Context(), &elasticache.DescribeCacheClustersInput{CacheClusterId: aws.String(id)}, ecEngineWait))
}

func ecAwaitReplGroup(t *testing.T, c *elasticache.Client, id string) ectypes.ReplicationGroup {
	t.Helper()
	waiter := elasticache.NewReplicationGroupAvailableWaiter(c, func(o *elasticache.ReplicationGroupAvailableWaiterOptions) {
		o.MinDelay, o.MaxDelay = ecWaiterMinDelay, ecWaiterMaxDelay
	})
	out, err := waiter.WaitForOutput(t.Context(), &elasticache.DescribeReplicationGroupsInput{ReplicationGroupId: aws.String(id)}, ecEngineWait)
	require.NoError(t, err, "replication group %s did not become available", id)
	require.Len(t, out.ReplicationGroups, 1)
	return out.ReplicationGroups[0]
}

func ecAwaitReplGroupDeleted(t *testing.T, c *elasticache.Client, id string) {
	t.Helper()
	waiter := elasticache.NewReplicationGroupDeletedWaiter(c, func(o *elasticache.ReplicationGroupDeletedWaiterOptions) {
		o.MinDelay, o.MaxDelay = ecWaiterMinDelay, ecWaiterMaxDelay
	})
	require.NoError(t, waiter.Wait(t.Context(), &elasticache.DescribeReplicationGroupsInput{ReplicationGroupId: aws.String(id)}, ecEngineWait))
}

// ecAwaitServerless polls the serverless cache's Status, all ElastiCache offers
// to wait on: the SDK has no waiter for it.
func ecAwaitServerless(t *testing.T, c *elasticache.Client, name string) ectypes.ServerlessCache {
	t.Helper()
	deadline := time.Now().Add(ecEngineWait)
	for {
		out, err := c.DescribeServerlessCaches(t.Context(), &elasticache.DescribeServerlessCachesInput{ServerlessCacheName: aws.String(name)})
		require.NoError(t, err)
		require.Len(t, out.ServerlessCaches, 1)
		got := out.ServerlessCaches[0]
		status := aws.ToString(got.Status)
		if status == "available" {
			return got
		}
		require.Equal(t, "creating", status, "serverless cache %s", name)
		require.True(t, time.Now().Before(deadline), "serverless cache %s did not become available", name)
		time.Sleep(time.Second)
	}
}

func ecAwaitServerlessDeleted(t *testing.T, c *elasticache.Client, name string) {
	t.Helper()
	deadline := time.Now().Add(ecEngineWait)
	for {
		_, err := c.DescribeServerlessCaches(t.Context(), &elasticache.DescribeServerlessCachesInput{ServerlessCacheName: aws.String(name)})
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ServerlessCacheNotFoundFault" {
			return
		}
		require.NoError(t, err)
		require.True(t, time.Now().Before(deadline), "serverless cache %s was not deleted", name)
		time.Sleep(time.Second)
	}
}

func ecAddress(endpoint *ectypes.Endpoint) string {
	return net.JoinHostPort(aws.ToString(endpoint.Address), strconv.Itoa(int(aws.ToInt32(endpoint.Port))))
}

// ElastiCache presents a certificate a public root signs; the client checks
// encryption, not the chain.
func ecClientTLS() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // see above
}

func redisClient(t *testing.T, options *goredis.Options) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(options)
	t.Cleanup(func() { client.Close() })
	return client
}

func memcachedCommand(t *testing.T, address, request string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(30*time.Second)))
	_, err = conn.Write([]byte(request))
	require.NoError(t, err)
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	return strings.TrimSpace(line)
}

// A Redis OSS cache cluster requires its AUTH token over TLS on the port it
// names, and keeps what a client writes.
func TestElastiCache_RedisClusterServesItsEndpoint(t *testing.T) {
	t.Parallel()
	c := ecClient()
	id := "sdk-engine-redis"
	const token = "sdk-engine-auth-token-0001"
	created, err := c.CreateCacheCluster(t.Context(), &elasticache.CreateCacheClusterInput{
		CacheClusterId:           aws.String(id),
		CacheNodeType:            aws.String("cache.t3.micro"),
		Engine:                   aws.String("redis"),
		EngineVersion:            aws.String("7.1"),
		NumCacheNodes:            aws.Int32(1),
		Port:                     aws.Int32(6390),
		TransitEncryptionEnabled: aws.Bool(true),
		AuthToken:                aws.String(token),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(t.Context(), &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(id)})
	})
	assert.Equal(t, "creating", aws.ToString(created.CacheCluster.CacheClusterStatus))

	cluster := ecAwaitCluster(t, c, id)
	assert.Nil(t, cluster.ConfigurationEndpoint, "a Redis OSS cluster has no configuration endpoint")
	assert.True(t, aws.ToBool(cluster.AuthTokenEnabled))
	assert.True(t, aws.ToBool(cluster.TransitEncryptionEnabled))
	require.Len(t, cluster.CacheNodes, 1)
	node := cluster.CacheNodes[0]
	assert.Equal(t, "0001", aws.ToString(node.CacheNodeId))
	assert.Equal(t, "available", aws.ToString(node.CacheNodeStatus))
	require.NotNil(t, node.Endpoint)
	assert.Equal(t, int32(6390), aws.ToInt32(node.Endpoint.Port))

	client := redisClient(t, &goredis.Options{Addr: ecAddress(node.Endpoint), Password: token, TLSConfig: ecClientTLS()})
	require.NoError(t, client.Set(t.Context(), "greeting", "hello from elasticache", 0).Err())
	value, err := client.Get(t.Context(), "greeting").Result()
	require.NoError(t, err)
	assert.Equal(t, "hello from elasticache", value)

	wrong := redisClient(t, &goredis.Options{Addr: ecAddress(node.Endpoint), Password: "not-the-auth-token", TLSConfig: ecClientTLS(), MaxRetries: -1})
	assert.Error(t, wrong.Ping(t.Context()).Err(), "the engine refuses a wrong AUTH token")
	plaintext := redisClient(t, &goredis.Options{Addr: ecAddress(node.Endpoint), Password: token, MaxRetries: -1,
		DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second})
	assert.Error(t, plaintext.Ping(t.Context()).Err(), "an endpoint that requires TLS speaks no plaintext")

	_, err = c.RebootCacheCluster(t.Context(), &elasticache.RebootCacheClusterInput{
		CacheClusterId: aws.String(id), CacheNodeIdsToReboot: []string{"0001"},
	})
	require.NoError(t, err)
	ecAwaitCluster(t, c, id)
	require.NoError(t, client.Ping(t.Context()).Err(), "the node answers again after its reboot")

	_, err = c.DeleteCacheCluster(t.Context(), &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(id)})
	require.NoError(t, err)
	ecAwaitClusterDeleted(t, c, id)
}

// A Memcached cluster serves each node at an endpoint of its own and every
// node through its configuration endpoint; a modification adds a node.
func TestElastiCache_MemcachedClusterServesItsNodes(t *testing.T) {
	t.Parallel()
	c := ecClient()
	id := "sdk-engine-memcached"
	_, err := c.CreateCacheCluster(t.Context(), &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(id),
		CacheNodeType:  aws.String("cache.t3.micro"),
		Engine:         aws.String("memcached"),
		NumCacheNodes:  aws.Int32(2),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(t.Context(), &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(id)})
	})
	cluster := ecAwaitCluster(t, c, id)
	assert.Equal(t, "1.6.22", aws.ToString(cluster.EngineVersion))
	require.NotNil(t, cluster.ConfigurationEndpoint)
	assert.Equal(t, int32(11211), aws.ToInt32(cluster.ConfigurationEndpoint.Port))
	require.Len(t, cluster.CacheNodes, 2)
	first := ecAddress(cluster.CacheNodes[0].Endpoint)
	second := ecAddress(cluster.CacheNodes[1].Endpoint)
	assert.NotEqual(t, first, second, "each node answers at an address of its own")

	assert.Equal(t, "STORED", memcachedCommand(t, first, "set greeting 0 0 5\r\nhello\r\n"))
	assert.Equal(t, "VALUE greeting 0 5", memcachedCommand(t, first, "get greeting\r\n"))
	assert.Equal(t, "END", memcachedCommand(t, second, "get greeting\r\n"), "nodes share no items")
	assert.True(t, strings.HasPrefix(memcachedCommand(t, ecAddress(cluster.ConfigurationEndpoint), "version\r\n"), "VERSION 1.6.22"))

	_, err = c.ModifyCacheCluster(t.Context(), &elasticache.ModifyCacheClusterInput{
		CacheClusterId: aws.String(id), NumCacheNodes: aws.Int32(3), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	cluster = ecAwaitCluster(t, c, id)
	require.Len(t, cluster.CacheNodes, 3)
	assert.Equal(t, "STORED", memcachedCommand(t, ecAddress(cluster.CacheNodes[2].Endpoint), "set added 0 0 1\r\nx\r\n"))
}

// A replication group without cluster mode runs a primary and a replica: a
// write through the primary endpoint reads back through the reader endpoint
// and through the replica's own, and a failover promotes the replica.
func TestElastiCache_ReplicationGroupReplicates(t *testing.T) {
	t.Parallel()
	c := ecClient()
	id := "sdk-engine-replicated"
	_, err := c.CreateReplicationGroup(t.Context(), &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId:          aws.String(id),
		ReplicationGroupDescription: aws.String("replicated over a real engine"),
		CacheNodeType:               aws.String("cache.t3.micro"),
		Engine:                      aws.String("valkey"),
		NumCacheClusters:            aws.Int32(2),
		AutomaticFailoverEnabled:    aws.Bool(true),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(t.Context(), &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
	})
	group := ecAwaitReplGroup(t, c, id)
	assert.Equal(t, []string{id + "-001", id + "-002"}, group.MemberClusters)
	assert.Nil(t, group.ConfigurationEndpoint, "a group without cluster mode has no configuration endpoint")
	require.Len(t, group.NodeGroups, 1)
	nodeGroup := group.NodeGroups[0]
	assert.Equal(t, "0001", aws.ToString(nodeGroup.NodeGroupId))
	require.NotNil(t, nodeGroup.PrimaryEndpoint)
	require.NotNil(t, nodeGroup.ReaderEndpoint)
	require.Len(t, nodeGroup.NodeGroupMembers, 2)
	assert.Equal(t, "primary", aws.ToString(nodeGroup.NodeGroupMembers[0].CurrentRole))
	assert.Equal(t, "replica", aws.ToString(nodeGroup.NodeGroupMembers[1].CurrentRole))

	primary := redisClient(t, &goredis.Options{Addr: ecAddress(nodeGroup.PrimaryEndpoint)})
	require.NoError(t, primary.Set(t.Context(), "replicated", "yes", 0).Err())
	acknowledged, err := primary.Wait(t.Context(), 1, 0).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), acknowledged, "the replica acknowledged the write")
	reader := redisClient(t, &goredis.Options{Addr: ecAddress(nodeGroup.ReaderEndpoint)})
	value, err := reader.Get(t.Context(), "replicated").Result()
	require.NoError(t, err)
	assert.Equal(t, "yes", value)
	replica := redisClient(t, &goredis.Options{Addr: ecAddress(nodeGroup.NodeGroupMembers[1].ReadEndpoint)})
	role, err := replica.Do(t.Context(), "ROLE").Slice()
	require.NoError(t, err)
	assert.Equal(t, "slave", fmt.Sprint(role[0]), "the second member replicates")

	members, err := c.DescribeCacheClusters(t.Context(), &elasticache.DescribeCacheClustersInput{
		CacheClusterId: aws.String(id + "-002"), ShowCacheNodeInfo: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, members.CacheClusters, 1)
	assert.Equal(t, id, aws.ToString(members.CacheClusters[0].ReplicationGroupId))
	assert.Equal(t, aws.ToString(nodeGroup.NodeGroupMembers[1].ReadEndpoint.Address), aws.ToString(members.CacheClusters[0].CacheNodes[0].Endpoint.Address))
	_, err = c.DeleteCacheCluster(t.Context(), &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(id + "-002")})
	assertAWSAPIErrorCode(t, err, "InvalidCacheClusterState")

	_, err = c.TestFailover(t.Context(), &elasticache.TestFailoverInput{ReplicationGroupId: aws.String(id), NodeGroupId: aws.String("0001")})
	require.NoError(t, err)
	group = ecAwaitReplGroup(t, c, id)
	nodeGroup = group.NodeGroups[0]
	assert.Equal(t, id+"-002", aws.ToString(nodeGroup.NodeGroupMembers[0].CacheClusterId), "the replica became the primary")
	promoted := redisClient(t, &goredis.Options{Addr: ecAddress(nodeGroup.PrimaryEndpoint)})
	value, err = promoted.Get(t.Context(), "replicated").Result()
	require.NoError(t, err)
	assert.Equal(t, "yes", value)
	require.NoError(t, promoted.Set(t.Context(), "after-failover", "written", 0).Err())

	_, err = c.IncreaseReplicaCount(t.Context(), &elasticache.IncreaseReplicaCountInput{
		ReplicationGroupId: aws.String(id), NewReplicaCount: aws.Int32(2), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	group = ecAwaitReplGroup(t, c, id)
	require.Len(t, group.MemberClusters, 3)
	added := group.NodeGroups[0].NodeGroupMembers[2]
	assert.Equal(t, id+"-003", aws.ToString(added.CacheClusterId))
	newest := redisClient(t, &goredis.Options{Addr: ecAddress(added.ReadEndpoint)})
	value, err = newest.Get(t.Context(), "after-failover").Result()
	require.NoError(t, err)
	assert.Equal(t, "written", value, "the added replica synchronised")

	_, err = c.DeleteReplicationGroup(t.Context(), &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
	require.NoError(t, err)
	ecAwaitReplGroupDeleted(t, c, id)
	_, err = c.DescribeCacheClusters(t.Context(), &elasticache.DescribeCacheClustersInput{CacheClusterId: aws.String(id + "-001")})
	assertAWSAPIErrorCode(t, err, "CacheClusterNotFound")
}

// A replication group with cluster mode runs its shards behind its
// configuration endpoint over TLS; a cluster client spreads keys over them,
// and a resharding keeps every key.
func TestElastiCache_ClusterModeReplicationGroupShards(t *testing.T) {
	t.Parallel()
	c := ecClient()
	id := "sdk-engine-sharded"
	const token = "sdk-engine-shard-token-001"
	_, err := c.CreateReplicationGroup(t.Context(), &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId:          aws.String(id),
		ReplicationGroupDescription: aws.String("sharded over a real engine"),
		CacheNodeType:               aws.String("cache.t3.micro"),
		Engine:                      aws.String("redis"),
		NumNodeGroups:               aws.Int32(2),
		ReplicasPerNodeGroup:        aws.Int32(1),
		AutomaticFailoverEnabled:    aws.Bool(true),
		TransitEncryptionEnabled:    aws.Bool(true),
		AuthToken:                   aws.String(token),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(t.Context(), &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
	})
	group := ecAwaitReplGroup(t, c, id)
	assert.True(t, aws.ToBool(group.ClusterEnabled))
	require.NotNil(t, group.ConfigurationEndpoint)
	assert.Equal(t, int32(6379), aws.ToInt32(group.ConfigurationEndpoint.Port))
	require.Len(t, group.NodeGroups, 2)
	assert.Equal(t, "0-8191", aws.ToString(group.NodeGroups[0].Slots))
	assert.Equal(t, "8192-16383", aws.ToString(group.NodeGroups[1].Slots))
	assert.Len(t, group.MemberClusters, 4)

	client := goredis.NewClusterClient(&goredis.ClusterOptions{
		Addrs: []string{ecAddress(group.ConfigurationEndpoint)}, Password: token, TLSConfig: ecClientTLS(),
	})
	t.Cleanup(func() { client.Close() })
	for i := 0; i < 40; i++ {
		require.NoError(t, client.Set(t.Context(), fmt.Sprintf("sharded-%d", i), i, 0).Err())
	}
	shards, err := client.ClusterShards(t.Context()).Result()
	require.NoError(t, err)
	assert.Len(t, shards, 2)

	_, err = c.ModifyReplicationGroupShardConfiguration(t.Context(), &elasticache.ModifyReplicationGroupShardConfigurationInput{
		ReplicationGroupId: aws.String(id), NodeGroupCount: aws.Int32(3), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	group = ecAwaitReplGroup(t, c, id)
	require.Len(t, group.NodeGroups, 3)
	assert.Len(t, group.MemberClusters, 6)
	resharded := goredis.NewClusterClient(&goredis.ClusterOptions{
		Addrs: []string{ecAddress(group.ConfigurationEndpoint)}, Password: token, TLSConfig: ecClientTLS(),
	})
	t.Cleanup(func() { resharded.Close() })
	for i := 0; i < 40; i++ {
		value, err := resharded.Get(t.Context(), fmt.Sprintf("sharded-%d", i)).Int()
		require.NoError(t, err)
		assert.Equal(t, i, value)
	}
}

// A serverless cache serves a cluster-mode endpoint over TLS, and its reader
// endpoint on the next port.
func TestElastiCache_ServerlessCacheServesItsEndpoint(t *testing.T) {
	t.Parallel()
	c := ecClient()
	name := "sdk-engine-serverless"
	created, err := c.CreateServerlessCache(t.Context(), &elasticache.CreateServerlessCacheInput{
		ServerlessCacheName: aws.String(name),
		Engine:              aws.String("valkey"),
		MajorEngineVersion:  aws.String("8"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCache(t.Context(), &elasticache.DeleteServerlessCacheInput{ServerlessCacheName: aws.String(name)})
	})
	assert.Equal(t, "creating", aws.ToString(created.ServerlessCache.Status))
	cache := ecAwaitServerless(t, c, name)
	assert.Equal(t, "8.0", aws.ToString(cache.FullEngineVersion))
	require.NotNil(t, cache.Endpoint)
	require.NotNil(t, cache.ReaderEndpoint)
	assert.Equal(t, int32(6379), aws.ToInt32(cache.Endpoint.Port))
	assert.Equal(t, int32(6380), aws.ToInt32(cache.ReaderEndpoint.Port))

	client := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(cache.Endpoint)}, TLSConfig: ecClientTLS()})
	t.Cleanup(func() { client.Close() })
	require.NoError(t, client.Set(t.Context(), "serverless", "answered", 0).Err())
	value, err := client.Get(t.Context(), "serverless").Result()
	require.NoError(t, err)
	assert.Equal(t, "answered", value)
	reader := redisClient(t, &goredis.Options{Addr: ecAddress(cache.ReaderEndpoint), TLSConfig: ecClientTLS()})
	value, err = reader.Get(t.Context(), "serverless").Result()
	require.NoError(t, err)
	assert.Equal(t, "answered", value)
}
