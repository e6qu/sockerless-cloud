package aws_cli_test

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliECEndpoint struct {
	Address string `json:"Address"`
	Port    int    `json:"Port"`
}

func (e cliECEndpoint) address() string { return net.JoinHostPort(e.Address, strconv.Itoa(e.Port)) }

type cliECCluster struct {
	CacheClusterId        string         `json:"CacheClusterId"`
	CacheClusterStatus    string         `json:"CacheClusterStatus"`
	EngineVersion         string         `json:"EngineVersion"`
	ConfigurationEndpoint *cliECEndpoint `json:"ConfigurationEndpoint"`
	CacheNodes            []struct {
		CacheNodeId     string        `json:"CacheNodeId"`
		CacheNodeStatus string        `json:"CacheNodeStatus"`
		Endpoint        cliECEndpoint `json:"Endpoint"`
	} `json:"CacheNodes"`
}

type cliECReplGroup struct {
	Status                string         `json:"Status"`
	MemberClusters        []string       `json:"MemberClusters"`
	ConfigurationEndpoint *cliECEndpoint `json:"ConfigurationEndpoint"`
	NodeGroups            []struct {
		NodeGroupId      string         `json:"NodeGroupId"`
		Slots            string         `json:"Slots"`
		PrimaryEndpoint  *cliECEndpoint `json:"PrimaryEndpoint"`
		ReaderEndpoint   *cliECEndpoint `json:"ReaderEndpoint"`
		NodeGroupMembers []struct {
			CacheClusterId string         `json:"CacheClusterId"`
			CurrentRole    string         `json:"CurrentRole"`
			ReadEndpoint   *cliECEndpoint `json:"ReadEndpoint"`
		} `json:"NodeGroupMembers"`
	} `json:"NodeGroups"`
}

type cliECServerlessCache struct {
	Status         string        `json:"Status"`
	Endpoint       cliECEndpoint `json:"Endpoint"`
	ReaderEndpoint cliECEndpoint `json:"ReaderEndpoint"`
}

func ecCLIAwaitCluster(t *testing.T, id string) cliECCluster {
	t.Helper()
	runCLI(t, awsCLI("elasticache", "wait", "cache-cluster-available", "--cache-cluster-id", id))
	var described struct {
		CacheClusters []cliECCluster `json:"CacheClusters"`
	}
	parseJSON(t, runCLI(t, awsCLI("elasticache", "describe-cache-clusters", "--cache-cluster-id", id, "--show-cache-node-info")), &described)
	require.Len(t, described.CacheClusters, 1)
	return described.CacheClusters[0]
}

func ecCLIAwaitReplGroup(t *testing.T, id string) cliECReplGroup {
	t.Helper()
	runCLI(t, awsCLI("elasticache", "wait", "replication-group-available", "--replication-group-id", id))
	var described struct {
		ReplicationGroups []cliECReplGroup `json:"ReplicationGroups"`
	}
	parseJSON(t, runCLI(t, awsCLI("elasticache", "describe-replication-groups", "--replication-group-id", id)), &described)
	require.Len(t, described.ReplicationGroups, 1)
	return described.ReplicationGroups[0]
}

// ecCLIAwaitServerless polls the serverless cache's Status: the CLI has no
// waiter for a serverless cache.
func ecCLIAwaitServerless(t *testing.T, name string) cliECServerlessCache {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var described struct {
			ServerlessCaches []cliECServerlessCache `json:"ServerlessCaches"`
		}
		parseJSON(t, runCLI(t, awsCLI("elasticache", "describe-serverless-caches", "--serverless-cache-name", name)), &described)
		require.Len(t, described.ServerlessCaches, 1)
		cache := described.ServerlessCaches[0]
		if cache.Status == "available" {
			return cache
		}
		require.Equal(t, "creating", cache.Status, "serverless cache %s", name)
		require.True(t, time.Now().Before(deadline), "serverless cache %s did not become available", name)
		time.Sleep(time.Second)
	}
}

// ElastiCache presents a certificate a public root signs; the client checks
// encryption, not the chain.
func ecCLIClientTLS() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // see above
}

func ecCLIRedis(t *testing.T, options *goredis.Options) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(options)
	t.Cleanup(func() { client.Close() })
	return client
}

func ecCLIMemcached(t *testing.T, address, request string) string {
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

// A Memcached cluster and a cluster-mode replication group serve at the
// endpoints the CLI reports.
func TestElastiCacheCLI_MemcachedAndShardedGroupServe(t *testing.T) {
	t.Parallel()
	memcached := "cli-engine-memcached"
	runCLI(t, awsCLI("elasticache", "create-cache-cluster",
		"--cache-cluster-id", memcached,
		"--cache-node-type", "cache.t3.micro",
		"--engine", "memcached",
		"--num-cache-nodes", "2"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-cluster", "--cache-cluster-id", memcached).Run()
	})
	sharded := "cli-engine-sharded"
	const token = "cli-engine-shard-token-0001"
	runCLI(t, awsCLI("elasticache", "create-replication-group",
		"--replication-group-id", sharded,
		"--replication-group-description", "cli sharded",
		"--engine", "valkey",
		"--cache-node-type", "cache.t3.micro",
		"--num-node-groups", "2",
		"--replicas-per-node-group", "0",
		"--transit-encryption-enabled",
		"--auth-token", token))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-replication-group", "--replication-group-id", sharded).Run()
	})

	cluster := ecCLIAwaitCluster(t, memcached)
	require.NotNil(t, cluster.ConfigurationEndpoint)
	assert.Equal(t, 11211, cluster.ConfigurationEndpoint.Port)
	require.Len(t, cluster.CacheNodes, 2)
	first := cluster.CacheNodes[0].Endpoint.address()
	assert.Equal(t, "STORED", ecCLIMemcached(t, first, "set cli 0 0 3\r\nyes\r\n"))
	assert.Equal(t, "VALUE cli 0 3", ecCLIMemcached(t, first, "get cli\r\n"))
	assert.Equal(t, "END", ecCLIMemcached(t, cluster.CacheNodes[1].Endpoint.address(), "get cli\r\n"))

	group := ecCLIAwaitReplGroup(t, sharded)
	require.NotNil(t, group.ConfigurationEndpoint)
	require.Len(t, group.NodeGroups, 2)
	assert.Equal(t, "0-8191", group.NodeGroups[0].Slots)
	client := goredis.NewClusterClient(&goredis.ClusterOptions{
		Addrs: []string{group.ConfigurationEndpoint.address()}, Password: token, TLSConfig: ecCLIClientTLS(),
	})
	t.Cleanup(func() { client.Close() })
	for i := 0; i < 20; i++ {
		require.NoError(t, client.Set(t.Context(), fmt.Sprintf("cli-sharded-%d", i), i, 0).Err())
	}
	for i := 0; i < 20; i++ {
		value, err := client.Get(t.Context(), fmt.Sprintf("cli-sharded-%d", i)).Int()
		require.NoError(t, err)
		assert.Equal(t, i, value)
	}

	runCLI(t, awsCLI("elasticache", "delete-replication-group", "--replication-group-id", sharded))
	runCLI(t, awsCLI("elasticache", "wait", "replication-group-deleted", "--replication-group-id", sharded))
	runCLI(t, awsCLI("elasticache", "delete-cache-cluster", "--cache-cluster-id", memcached))
	runCLI(t, awsCLI("elasticache", "wait", "cache-cluster-deleted", "--cache-cluster-id", memcached))
}
