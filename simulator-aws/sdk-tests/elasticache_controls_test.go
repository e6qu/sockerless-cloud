package aws_sdk_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecAwaitUserActive polls the user's Status, all ElastiCache offers to wait
// on: the SDK has no waiter for a user.
func ecAwaitUserActive(t *testing.T, c *elasticache.Client, id string) {
	t.Helper()
	deadline := time.Now().Add(ecEngineWait)
	for {
		out, err := c.DescribeUsers(t.Context(), &elasticache.DescribeUsersInput{UserId: aws.String(id)})
		require.NoError(t, err)
		require.Len(t, out.Users, 1)
		if aws.ToString(out.Users[0].Status) == "active" {
			return
		}
		require.True(t, time.Now().Before(deadline), "user %s did not become active", id)
		time.Sleep(time.Second)
	}
}

// ecIAMAuthToken signs an ElastiCache IAM authentication token: a GET of
// http://<cache>/?Action=connect&User=<user> presigned for 15 minutes for the
// elasticache service, without its scheme.
func ecIAMAuthToken(t *testing.T, cache, user string) string {
	t.Helper()
	cfg := sdkConfig()
	credentials, err := cfg.Credentials.Retrieve(t.Context())
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodGet, "http://"+cache+"/", nil)
	require.NoError(t, err)
	query := request.URL.Query()
	query.Set("Action", "connect")
	query.Set("User", user)
	query.Set("X-Amz-Expires", "900")
	request.URL.RawQuery = query.Encode()
	empty := sha256.Sum256(nil)
	signed, _, err := v4.NewSigner().PresignHTTP(t.Context(), credentials, request, hex.EncodeToString(empty[:]), "elasticache", cfg.Region, time.Now())
	require.NoError(t, err)
	return strings.TrimPrefix(signed, "http://")
}

func ecRedisUser(t *testing.T, address, user, password string) *goredis.Client {
	t.Helper()
	return redisClient(t, &goredis.Options{Addr: address, Username: user, Password: password, TLSConfig: ecClientTLS(), MaxRetries: -1})
}

// The users of a user group attached to a replication group are the engine's
// users: the default user is the group's, an access string rules what a user
// may run, a password change and an appended rule reach the engine, and an
// IAM-enabled user connects with an IAM authentication token. Moving the
// group to AUTH rotates tokens side by side until SET keeps the newest, and
// DELETE moves it back to the user group.
func TestElastiCache_UserGroupsAndAuthTokensRunInTheEngine(t *testing.T) {
	t.Parallel()
	c := ecClient()
	const (
		id              = "sdk-rbac-group"
		groupID         = "sdk-rbac-users"
		defaultPassword = "sdk-rbac-default-password"
		appPassword     = "sdk-rbac-app-password-001"
		appRotated      = "sdk-rbac-app-password-002"
		firstToken      = "sdk-rbac-auth-token-0001"
		secondToken     = "sdk-rbac-auth-token-0002"
	)
	users := []*elasticache.CreateUserInput{
		{UserId: aws.String("sdk-rbac-default"), UserName: aws.String("default"), Engine: aws.String("redis"),
			AccessString: aws.String("off ~* +@all"), Passwords: []string{defaultPassword}},
		{UserId: aws.String("sdk-rbac-app"), UserName: aws.String("sdk-app"), Engine: aws.String("redis"),
			AccessString: aws.String("on ~app:* &* +@all"), Passwords: []string{appPassword}},
		{UserId: aws.String("sdk-rbac-iam"), UserName: aws.String("sdk-rbac-iam"), Engine: aws.String("redis"),
			AccessString: aws.String("on ~* -@all +@read"), AuthenticationMode: &ectypes.AuthenticationMode{Type: ectypes.InputAuthenticationTypeIam}},
	}
	for _, input := range users {
		created, err := c.CreateUser(t.Context(), input)
		require.NoError(t, err)
		userID := input.UserId
		t.Cleanup(func() { _, _ = c.DeleteUser(t.Context(), &elasticache.DeleteUserInput{UserId: userID}) })
		require.NotNil(t, created.Authentication)
	}
	_, err := c.CreateUserGroup(t.Context(), &elasticache.CreateUserGroupInput{
		UserGroupId: aws.String(groupID), Engine: aws.String("redis"), UserIds: []string{"sdk-rbac-app"},
	})
	assertAWSAPIErrorCode(t, err, "DefaultUserRequired")
	_, err = c.CreateUserGroup(t.Context(), &elasticache.CreateUserGroupInput{
		UserGroupId: aws.String(groupID), Engine: aws.String("redis"), UserIds: []string{"default", "sdk-rbac-default"},
	})
	assertAWSAPIErrorCode(t, err, "DuplicateUserName")
	_, err = c.CreateUserGroup(t.Context(), &elasticache.CreateUserGroupInput{
		UserGroupId: aws.String(groupID), Engine: aws.String("redis"), UserIds: []string{"sdk-rbac-default", "sdk-rbac-app", "sdk-rbac-iam"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteUserGroup(t.Context(), &elasticache.DeleteUserGroupInput{UserGroupId: aws.String(groupID)})
	})

	_, err = c.CreateReplicationGroup(t.Context(), &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId: aws.String(id), ReplicationGroupDescription: aws.String("user groups in the engine"),
		CacheNodeType: aws.String("cache.t3.micro"), Engine: aws.String("redis"), NumCacheClusters: aws.Int32(1),
		UserGroupIds: []string{groupID},
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
	_, err = c.CreateReplicationGroup(t.Context(), &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId: aws.String(id), ReplicationGroupDescription: aws.String("user groups in the engine"),
		CacheNodeType: aws.String("cache.t3.micro"), Engine: aws.String("redis"), NumCacheClusters: aws.Int32(1),
		TransitEncryptionEnabled: aws.Bool(true), UserGroupIds: []string{groupID},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(t.Context(), &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
	})
	group := ecAwaitReplGroup(t, c, id)
	assert.Equal(t, []string{groupID}, group.UserGroupIds)
	described, err := c.DescribeUserGroups(t.Context(), &elasticache.DescribeUserGroupsInput{UserGroupId: aws.String(groupID)})
	require.NoError(t, err)
	assert.Equal(t, []string{id}, described.UserGroups[0].ReplicationGroups)
	_, err = c.DeleteUserGroup(t.Context(), &elasticache.DeleteUserGroupInput{UserGroupId: aws.String(groupID)})
	assertAWSAPIErrorCode(t, err, "InvalidUserGroupState")
	primary := ecAddress(group.NodeGroups[0].PrimaryEndpoint)

	assert.Error(t, ecRedisUser(t, primary, "", "").Ping(t.Context()).Err(), "the group's default user is off")
	assert.Error(t, ecRedisUser(t, primary, "default", defaultPassword).Ping(t.Context()).Err(), "an off user does not authenticate")
	app := ecRedisUser(t, primary, "sdk-app", appPassword)
	require.NoError(t, app.Set(t.Context(), "app:greeting", "hello", 0).Err())
	err = app.Set(t.Context(), "other:greeting", "hello", 0).Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOPERM")

	iam := ecRedisUser(t, primary, "sdk-rbac-iam", ecIAMAuthToken(t, id, "sdk-rbac-iam"))
	value, err := iam.Get(t.Context(), "app:greeting").Result()
	require.NoError(t, err, "an IAM authentication token signs the IAM-enabled user in")
	assert.Equal(t, "hello", value)
	assert.Error(t, iam.Set(t.Context(), "app:greeting", "changed", 0).Err(), "the IAM-enabled user reads only")
	assert.Error(t, ecRedisUser(t, primary, "sdk-rbac-iam", ecIAMAuthToken(t, "another-cache", "sdk-rbac-iam")).Ping(t.Context()).Err(),
		"a token signed for another cache does not sign in")

	_, err = c.ModifyUser(t.Context(), &elasticache.ModifyUserInput{
		UserId: aws.String("sdk-rbac-app"), AppendAccessString: aws.String("~other:*"), Passwords: []string{appRotated},
	})
	require.NoError(t, err)
	ecAwaitUserActive(t, c, "sdk-rbac-app")
	assert.Error(t, ecRedisUser(t, primary, "sdk-app", appPassword).Ping(t.Context()).Err(), "the replaced password no longer signs in")
	rotated := ecRedisUser(t, primary, "sdk-app", appRotated)
	require.NoError(t, rotated.Set(t.Context(), "other:greeting", "hello", 0).Err(), "the appended rule reached the engine")

	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), RemoveUserGroups: aws.Bool(true), AuthToken: aws.String(firstToken),
		AuthTokenUpdateStrategy: ectypes.AuthTokenUpdateStrategyTypeRotate, ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	group = ecAwaitReplGroup(t, c, id)
	assert.Empty(t, group.UserGroupIds)
	assert.True(t, aws.ToBool(group.AuthTokenEnabled))
	require.NoError(t, ecRedisUser(t, primary, "", "").Ping(t.Context()).Err(), "a token ROTATE adds to none is optional")
	require.NoError(t, ecRedisUser(t, primary, "", firstToken).Ping(t.Context()).Err())

	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), AuthToken: aws.String(secondToken),
		AuthTokenUpdateStrategy: ectypes.AuthTokenUpdateStrategyTypeSet, ApplyImmediately: aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), AuthToken: aws.String(firstToken),
		AuthTokenUpdateStrategy: ectypes.AuthTokenUpdateStrategyTypeSet, ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	ecAwaitReplGroup(t, c, id)
	assert.Error(t, ecRedisUser(t, primary, "", "").Ping(t.Context()).Err(), "SET makes the token required")

	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), AuthToken: aws.String(secondToken),
		AuthTokenUpdateStrategy: ectypes.AuthTokenUpdateStrategyTypeRotate, ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	ecAwaitReplGroup(t, c, id)
	require.NoError(t, ecRedisUser(t, primary, "", firstToken).Ping(t.Context()).Err(), "ROTATE keeps the previous token")
	require.NoError(t, ecRedisUser(t, primary, "", secondToken).Ping(t.Context()).Err())
	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), AuthToken: aws.String(secondToken),
		AuthTokenUpdateStrategy: ectypes.AuthTokenUpdateStrategyTypeSet, ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	ecAwaitReplGroup(t, c, id)
	assert.Error(t, ecRedisUser(t, primary, "", firstToken).Ping(t.Context()).Err(), "SET drops the previous token")
	require.NoError(t, ecRedisUser(t, primary, "", secondToken).Ping(t.Context()).Err())

	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), AuthTokenUpdateStrategy: ectypes.AuthTokenUpdateStrategyTypeDelete, ApplyImmediately: aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), AuthTokenUpdateStrategy: ectypes.AuthTokenUpdateStrategyTypeDelete,
		UserGroupIdsToAdd: []string{groupID}, ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	group = ecAwaitReplGroup(t, c, id)
	assert.False(t, aws.ToBool(group.AuthTokenEnabled))
	assert.Error(t, ecRedisUser(t, primary, "", secondToken).Ping(t.Context()).Err(), "DELETE drops the token for the group's default user")
	require.NoError(t, ecRedisUser(t, primary, "sdk-app", appRotated).Ping(t.Context()).Err())

	_, err = c.DeleteReplicationGroup(t.Context(), &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
	require.NoError(t, err)
	ecAwaitReplGroupDeleted(t, c, id)
	_, err = c.DeleteUserGroup(t.Context(), &elasticache.DeleteUserGroupInput{UserGroupId: aws.String(groupID)})
	require.NoError(t, err)
}

func ecConfigGetCluster(t *testing.T, address string) (int, []string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(30*time.Second)))
	_, err = conn.Write([]byte("config get cluster\r\n"))
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	header, err := reader.ReadString('\n')
	require.NoError(t, err)
	fields := strings.Fields(header)
	require.Len(t, fields, 4, "header %q", header)
	require.Equal(t, []string{"CONFIG", "cluster", "0"}, fields[:3])
	size, err := strconv.Atoi(fields[3])
	require.NoError(t, err)
	body := make([]byte, size)
	_, err = io.ReadFull(reader, body)
	require.NoError(t, err)
	trailer, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "\r\n", trailer)
	end, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "END\r\n", end)
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	require.Len(t, lines, 2)
	version, err := strconv.Atoi(lines[0])
	require.NoError(t, err)
	return version, strings.Fields(lines[1])
}

func ecDiscoveryNodes(cluster ectypes.CacheCluster) []string {
	var nodes []string
	for _, node := range cluster.CacheNodes {
		address := aws.ToString(node.Endpoint.Address)
		nodes = append(nodes, fmt.Sprintf("%s|%s|%d", address, address, aws.ToInt32(node.Endpoint.Port)))
	}
	sort.Strings(nodes)
	return nodes
}

// A Memcached cluster answers ElastiCache Auto Discovery at its configuration
// endpoint and at each node, its configuration version counting the nodes
// added; a modification refuses while one runs, and an engine upgrade
// restarts every node, empty, on the newer version, which no downgrade undoes.
func TestElastiCache_MemcachedAutoDiscoveryAndUpgrade(t *testing.T) {
	t.Parallel()
	c := ecClient()
	id := "sdk-memcached-discovery"
	_, err := c.CreateCacheCluster(t.Context(), &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(id), CacheNodeType: aws.String("cache.t3.micro"), Engine: aws.String("memcached"),
		EngineVersion: aws.String("1.6.17"), NumCacheNodes: aws.Int32(2), Port: aws.Int32(11215),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(t.Context(), &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(id)})
	})
	cluster := ecAwaitCluster(t, c, id)
	version, nodes := ecConfigGetCluster(t, ecAddress(cluster.ConfigurationEndpoint))
	assert.Equal(t, 1, version)
	assert.Equal(t, ecDiscoveryNodes(cluster), nodes)
	version, nodes = ecConfigGetCluster(t, ecAddress(cluster.CacheNodes[1].Endpoint))
	assert.Equal(t, 1, version, "every node answers with the same configuration")
	assert.Equal(t, ecDiscoveryNodes(cluster), nodes)

	_, err = c.ModifyCacheCluster(t.Context(), &elasticache.ModifyCacheClusterInput{
		CacheClusterId: aws.String(id), NumCacheNodes: aws.Int32(3), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	_, err = c.ModifyCacheCluster(t.Context(), &elasticache.ModifyCacheClusterInput{
		CacheClusterId: aws.String(id), CacheNodeType: aws.String("cache.t3.small"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidCacheClusterState")
	_, err = c.DeleteCacheCluster(t.Context(), &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(id)})
	assertAWSAPIErrorCode(t, err, "InvalidCacheClusterState")
	cluster = ecAwaitCluster(t, c, id)
	version, nodes = ecConfigGetCluster(t, ecAddress(cluster.ConfigurationEndpoint))
	assert.Equal(t, 2, version, "adding a node advances the configuration version")
	assert.Len(t, nodes, 3)
	assert.Equal(t, ecDiscoveryNodes(cluster), nodes)

	first := ecAddress(cluster.CacheNodes[0].Endpoint)
	assert.Equal(t, "STORED", memcachedCommand(t, first, "set before-upgrade 0 0 1\r\nx\r\n"))
	_, err = c.ModifyCacheCluster(t.Context(), &elasticache.ModifyCacheClusterInput{
		CacheClusterId: aws.String(id), EngineVersion: aws.String("1.6.22"), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	cluster = ecAwaitCluster(t, c, id)
	assert.Equal(t, "1.6.22", aws.ToString(cluster.EngineVersion))
	assert.True(t, strings.HasPrefix(memcachedCommand(t, first, "version\r\n"), "VERSION 1.6.22"))
	assert.Equal(t, "END", memcachedCommand(t, first, "get before-upgrade\r\n"), "a Memcached upgrade starts with a cold cache")

	_, err = c.ModifyCacheCluster(t.Context(), &elasticache.ModifyCacheClusterInput{
		CacheClusterId: aws.String(id), EngineVersion: aws.String("1.6.17"), ApplyImmediately: aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
}

// A cluster-mode replication group upgrades its engine by restarting every
// node on the newer version's image, every shard keeping its keys, and
// refuses to downgrade.
func TestElastiCache_ClusterModeEngineUpgradeKeepsKeys(t *testing.T) {
	t.Parallel()
	c := ecClient()
	id := "sdk-engine-upgrade"
	_, err := c.CreateReplicationGroup(t.Context(), &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId: aws.String(id), ReplicationGroupDescription: aws.String("upgraded in place"),
		CacheNodeType: aws.String("cache.t3.micro"), Engine: aws.String("redis"), EngineVersion: aws.String("7.0"),
		NumNodeGroups: aws.Int32(2), ReplicasPerNodeGroup: aws.Int32(0),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(t.Context(), &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
	})
	group := ecAwaitReplGroup(t, c, id)
	client := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(group.ConfigurationEndpoint)}})
	t.Cleanup(func() { client.Close() })
	for i := 0; i < 30; i++ {
		require.NoError(t, client.Set(t.Context(), fmt.Sprintf("upgraded-%d", i), i, 0).Err())
	}
	runIDs := ecShardRunIDs(t, client)

	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), EngineVersion: aws.String("7.1"), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), ReplicationGroupDescription: aws.String("changed while upgrading"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidReplicationGroupState")
	group = ecAwaitReplGroup(t, c, id)
	described, err := c.DescribeCacheClusters(t.Context(), &elasticache.DescribeCacheClustersInput{CacheClusterId: aws.String(group.MemberClusters[0])})
	require.NoError(t, err)
	assert.Equal(t, "7.1", aws.ToString(described.CacheClusters[0].EngineVersion))
	upgraded := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(group.ConfigurationEndpoint)}})
	t.Cleanup(func() { upgraded.Close() })
	for i := 0; i < 30; i++ {
		value, err := upgraded.Get(t.Context(), fmt.Sprintf("upgraded-%d", i)).Int()
		require.NoError(t, err)
		assert.Equal(t, i, value)
	}
	for node, runID := range ecShardRunIDs(t, upgraded) {
		assert.NotEqual(t, runIDs[node], runID, "node %s restarted for the upgrade", node)
	}
	_, err = c.ModifyReplicationGroup(t.Context(), &elasticache.ModifyReplicationGroupInput{
		ReplicationGroupId: aws.String(id), EngineVersion: aws.String("7.0"), ApplyImmediately: aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
}

// The run ID each primary reports, by its address; a restarted server reports
// a new one.
func ecShardRunIDs(t *testing.T, client *goredis.ClusterClient) map[string]string {
	t.Helper()
	var mu sync.Mutex
	runIDs := map[string]string{}
	require.NoError(t, client.ForEachMaster(t.Context(), func(ctx context.Context, shard *goredis.Client) error {
		info, err := shard.Info(ctx, "server").Result()
		if err != nil {
			return err
		}
		for _, line := range strings.Split(info, "\r\n") {
			if runID, ok := strings.CutPrefix(line, "run_id:"); ok {
				mu.Lock()
				runIDs[shard.Options().Addr] = runID
				mu.Unlock()
			}
		}
		return nil
	}))
	require.Len(t, runIDs, 2)
	return runIDs
}
