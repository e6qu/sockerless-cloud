package aws_cli_test

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ecCLIRedisUser(t *testing.T, address, user, password string) *goredis.Client {
	t.Helper()
	return ecCLIRedis(t, &goredis.Options{Addr: address, Username: user, Password: password, TLSConfig: ecCLIClientTLS(), MaxRetries: -1})
}

// A user group the CLI attaches to a replication group runs as the engine's
// users, and moving the group to AUTH with ROTATE accepts clients with and
// without the token until SET requires it.
func TestElastiCacheCLI_UserGroupAndTokenRotation(t *testing.T) {
	t.Parallel()
	const (
		id          = "cli-rbac-group"
		groupID     = "cli-rbac-users"
		appPassword = "cli-rbac-app-password-01"
		token       = "cli-rbac-auth-token-0001"
	)
	runCLI(t, awsCLI("elasticache", "create-user", "--user-id", "cli-rbac-default", "--user-name", "default",
		"--engine", "redis", "--access-string", "off ~* +@all", "--passwords", "cli-rbac-default-password"))
	t.Cleanup(func() { _ = awsCLI("elasticache", "delete-user", "--user-id", "cli-rbac-default").Run() })
	runCLI(t, awsCLI("elasticache", "create-user", "--user-id", "cli-rbac-app", "--user-name", "cli-app",
		"--engine", "redis", "--access-string", "on ~app:* +@all", "--authentication-mode", "Type=password,Passwords="+appPassword))
	t.Cleanup(func() { _ = awsCLI("elasticache", "delete-user", "--user-id", "cli-rbac-app").Run() })
	assert.Contains(t, runCLIExpectError(t, awsCLI("elasticache", "create-user-group", "--user-group-id", groupID,
		"--engine", "redis", "--user-ids", "cli-rbac-app")), "DefaultUserRequired")
	runCLI(t, awsCLI("elasticache", "create-user-group", "--user-group-id", groupID,
		"--engine", "redis", "--user-ids", "cli-rbac-default", "cli-rbac-app"))
	t.Cleanup(func() { _ = awsCLI("elasticache", "delete-user-group", "--user-group-id", groupID).Run() })
	runCLI(t, awsCLI("elasticache", "create-replication-group", "--replication-group-id", id,
		"--replication-group-description", "cli user groups", "--engine", "redis", "--cache-node-type", "cache.t3.micro",
		"--num-cache-clusters", "1", "--transit-encryption-enabled", "--user-group-ids", groupID))
	t.Cleanup(func() { _ = awsCLI("elasticache", "delete-replication-group", "--replication-group-id", id).Run() })

	group := ecCLIAwaitReplGroup(t, id)
	primary := group.NodeGroups[0].PrimaryEndpoint.address()
	assert.Error(t, ecCLIRedisUser(t, primary, "", "").Ping(t.Context()).Err(), "the group's default user is off")
	app := ecCLIRedisUser(t, primary, "cli-app", appPassword)
	require.NoError(t, app.Set(t.Context(), "app:cli", "yes", 0).Err())
	err := app.Set(t.Context(), "elsewhere", "no", 0).Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOPERM")

	runCLI(t, awsCLI("elasticache", "modify-replication-group", "--replication-group-id", id, "--remove-user-groups",
		"--auth-token", token, "--auth-token-update-strategy", "ROTATE", "--apply-immediately"))
	ecCLIAwaitReplGroup(t, id)
	require.NoError(t, ecCLIRedisUser(t, primary, "", "").Ping(t.Context()).Err(), "the rotated-in token is optional")
	require.NoError(t, ecCLIRedisUser(t, primary, "", token).Ping(t.Context()).Err())
	runCLI(t, awsCLI("elasticache", "modify-replication-group", "--replication-group-id", id,
		"--auth-token", token, "--auth-token-update-strategy", "SET", "--apply-immediately"))
	ecCLIAwaitReplGroup(t, id)
	assert.Error(t, ecCLIRedisUser(t, primary, "", "").Ping(t.Context()).Err(), "SET requires the token")
	require.NoError(t, ecCLIRedisUser(t, primary, "", token).Ping(t.Context()).Err())

	runCLI(t, awsCLI("elasticache", "delete-replication-group", "--replication-group-id", id))
	runCLI(t, awsCLI("elasticache", "wait", "replication-group-deleted", "--replication-group-id", id))
}

// A Memcached cluster the CLI creates answers ElastiCache Auto Discovery, and
// a CLI engine upgrade restarts its nodes on the newer version.
func TestElastiCacheCLI_MemcachedAutoDiscoveryAndUpgrade(t *testing.T) {
	t.Parallel()
	id := "cli-memcached-discovery"
	runCLI(t, awsCLI("elasticache", "create-cache-cluster", "--cache-cluster-id", id, "--cache-node-type", "cache.t3.micro",
		"--engine", "memcached", "--engine-version", "1.6.17", "--num-cache-nodes", "2"))
	t.Cleanup(func() { _ = awsCLI("elasticache", "delete-cache-cluster", "--cache-cluster-id", id).Run() })
	cluster := ecCLIAwaitCluster(t, id)
	require.NotNil(t, cluster.ConfigurationEndpoint)

	conn, err := net.DialTimeout("tcp", cluster.ConfigurationEndpoint.address(), 10*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(30*time.Second)))
	_, err = conn.Write([]byte("config get cluster\r\n"))
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	header, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(header, "CONFIG cluster 0 "), "header %q", header)
	version, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "1\n", version)
	nodes, err := reader.ReadString('\n')
	require.NoError(t, err)
	for _, node := range cluster.CacheNodes {
		address := node.Endpoint.address()
		host, port, _ := net.SplitHostPort(address)
		assert.Contains(t, nodes, host+"|"+host+"|"+port)
	}

	runCLI(t, awsCLI("elasticache", "modify-cache-cluster", "--cache-cluster-id", id, "--engine-version", "1.6.22", "--apply-immediately"))
	cluster = ecCLIAwaitCluster(t, id)
	assert.Equal(t, "1.6.22", cluster.EngineVersion)
	assert.True(t, strings.HasPrefix(ecCLIMemcached(t, cluster.CacheNodes[0].Endpoint.address(), "version\r\n"), "VERSION 1.6.22"))
	assert.Contains(t, runCLIExpectError(t, awsCLI("elasticache", "modify-cache-cluster", "--cache-cluster-id", id,
		"--engine-version", "1.6.17", "--apply-immediately")), "InvalidParameterCombination")
}
