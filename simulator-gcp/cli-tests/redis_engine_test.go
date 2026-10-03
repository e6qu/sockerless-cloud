package gcp_cli_test

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redisCommand sends one command over conn and returns its reply: a status,
// error or bulk reply as its text, an array as its elements one per line.
func redisCommand(t *testing.T, conn net.Conn, args ...string) string {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, arg := range args {
		b.WriteString("$" + strconv.Itoa(len(arg)) + "\r\n" + arg + "\r\n")
	}
	_, err := conn.Write([]byte(b.String()))
	require.NoError(t, err)
	return readRedisReply(t, bufio.NewReader(conn))
}

func readRedisReply(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	line = strings.TrimRight(line, "\r\n")
	switch {
	case strings.HasPrefix(line, "$"):
		size, err := strconv.Atoi(line[1:])
		require.NoError(t, err)
		if size < 0 {
			return ""
		}
		data := make([]byte, size+2)
		_, err = io.ReadFull(reader, data)
		require.NoError(t, err)
		return string(data[:size])
	case strings.HasPrefix(line, "*"):
		count, err := strconv.Atoi(line[1:])
		require.NoError(t, err)
		items := make([]string, 0, max(count, 0))
		for i := 0; i < count; i++ {
			items = append(items, readRedisReply(t, reader))
		}
		return strings.Join(items, "\n")
	}
	return line
}

// An instance created with in-transit encryption and RDB persistence serves
// TLS on port 6378 with the CA describe reports; a replica-count update and a
// failover run on the engine.
func TestMemorystoreRedisCLI_InstanceTLSPersistenceAndFailover(t *testing.T) {
	name := "cli-redis-tls"
	runCLI(t, gcloudCLI("redis", "instances", "create", name,
		"--region", location,
		"--size", "1",
		"--tier", "standard",
		"--redis-version", "redis_7_2",
		"--read-replicas-mode", "read-replicas-enabled",
		"--replica-count", "1",
		"--transit-encryption-mode", "server-authentication",
		"--persistence-mode", "rdb",
		"--rdb-snapshot-period", "1h",
		"--quiet",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "instances", "delete", name, "--region", location, "--quiet", "--format=json").Run()
	})

	var inst struct {
		Host              string `json:"host"`
		Port              int    `json:"port"`
		ReplicaCount      int    `json:"replicaCount"`
		PersistenceConfig struct {
			PersistenceMode   string `json:"persistenceMode"`
			RdbSnapshotPeriod string `json:"rdbSnapshotPeriod"`
		} `json:"persistenceConfig"`
		ServerCaCerts []struct {
			Cert string `json:"cert"`
		} `json:"serverCaCerts"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("redis", "instances", "describe", name, "--region", location, "--format=json")), &inst)
	require.Equal(t, 6378, inst.Port)
	require.Equal(t, "RDB", inst.PersistenceConfig.PersistenceMode)
	require.Equal(t, "ONE_HOUR", inst.PersistenceConfig.RdbSnapshotPeriod)
	require.Len(t, inst.ServerCaCerts, 1)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(inst.ServerCaCerts[0].Cert)))
	conn, err := tls.Dial("tcp", net.JoinHostPort(inst.Host, strconv.Itoa(inst.Port)), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	require.NoError(t, err, "the endpoint presents a certificate the reported CA signed")
	t.Cleanup(func() { conn.Close() })
	assert.Equal(t, "+OK", redisCommand(t, conn, "SET", "greeting", "hello"))

	runCLI(t, gcloudCLI("redis", "instances", "update", name, "--region", location, "--replica-count", "2", "--quiet", "--format=json"))
	parseJSON(t, runCLI(t, gcloudCLI("redis", "instances", "describe", name, "--region", location, "--format=json")), &inst)
	require.Equal(t, 2, inst.ReplicaCount)
	assert.Contains(t, redisCommand(t, conn, "INFO", "replication"), "connected_slaves:2")

	runCLI(t, gcloudCLI("redis", "instances", "failover", name, "--region", location,
		"--data-protection-mode", "limited-data-loss", "--quiet", "--format=json"))
	promoted, err := tls.Dial("tcp", net.JoinHostPort(inst.Host, strconv.Itoa(inst.Port)), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	require.NoError(t, err)
	t.Cleanup(func() { promoted.Close() })
	assert.Equal(t, "hello", redisCommand(t, promoted, "GET", "greeting"), "the promoted replica holds the dataset")
}

func TestMemorystoreRedisCLI_BasicTierFailoverRefused(t *testing.T) {
	name := "cli-redis-basic"
	runCLI(t, gcloudCLI("redis", "instances", "create", name,
		"--region", location, "--size", "1", "--tier", "basic", "--redis-version", "redis_7_2", "--quiet", "--format=json"))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "instances", "delete", name, "--region", location, "--quiet", "--format=json").Run()
	})
	out, err := gcloudCLI("redis", "instances", "failover", name, "--region", location, "--quiet", "--format=json").CombinedOutput()
	require.Error(t, err, "a Basic Tier instance has no replica to fail over to: %s", out)
	assert.Contains(t, string(out), "FAILED_PRECONDITION")
}

// A shard-count update reshards the running cluster.
func TestMemorystoreRedisCLI_ClusterReshard(t *testing.T) {
	name := "cli-redis-cluster"
	runCLI(t, gcloudCLI("redis", "clusters", "create", name,
		"--region", location,
		"--network", "projects/test-project/global/networks/default",
		"--shard-count", "1",
		"--replica-count", "0",
		"--persistence-mode", "aof",
		"--aof-append-fsync", "everysec",
		"--quiet",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "clusters", "delete", name, "--region", location, "--quiet", "--format=json").Run()
	})
	var cluster struct {
		ShardCount         int `json:"shardCount"`
		DiscoveryEndpoints []struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"discoveryEndpoints"`
		PersistenceConfig struct {
			Mode      string `json:"mode"`
			AofConfig struct {
				AppendFsync string `json:"appendFsync"`
			} `json:"aofConfig"`
		} `json:"persistenceConfig"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("redis", "clusters", "describe", name, "--region", location, "--format=json")), &cluster)
	require.Len(t, cluster.DiscoveryEndpoints, 1)
	assert.Equal(t, "AOF", cluster.PersistenceConfig.Mode)
	assert.Equal(t, "EVERYSEC", cluster.PersistenceConfig.AofConfig.AppendFsync)
	discovery := net.JoinHostPort(cluster.DiscoveryEndpoints[0].Address, strconv.Itoa(cluster.DiscoveryEndpoints[0].Port))
	conn, err := net.Dial("tcp", discovery)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	assert.Contains(t, redisCommand(t, conn, "CLUSTER", "INFO"), "cluster_size:1")
	assert.Equal(t, "appendonly\nyes", redisCommand(t, conn, "CONFIG", "GET", "appendonly"))

	runCLI(t, gcloudCLI("redis", "clusters", "update", name, "--region", location, "--shard-count", "2", "--quiet", "--format=json"))
	parseJSON(t, runCLI(t, gcloudCLI("redis", "clusters", "describe", name, "--region", location, "--format=json")), &cluster)
	require.Equal(t, 2, cluster.ShardCount)
	resharded, err := net.Dial("tcp", discovery)
	require.NoError(t, err)
	t.Cleanup(func() { resharded.Close() })
	assert.Contains(t, redisCommand(t, resharded, "CLUSTER", "INFO"), "cluster_size:2", "both shards serve slots")
}

// A cluster created with an ACL policy runs the policy's users on its engine,
// and an update of the policy's rules reaches the running cluster.
func TestMemorystoreRedisCLI_ClusterAclPolicy(t *testing.T) {
	policy := "cli-acl"
	policyName := "projects/" + project + "/locations/" + location + "/aclPolicies/" + policy
	runCLI(t, gcloudCLI("redis", "acl-policies", "create", policy, "--region", location,
		`--rules=[{"username":"app","rule":"on >cli-secret ~app-* +@all"}]`, "--quiet", "--format=json"))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "acl-policies", "delete", policy, "--region", location, "--quiet", "--format=json").Run()
	})
	name := "cli-redis-acl"
	runCLI(t, gcloudCLI("redis", "clusters", "create", name,
		"--region", location,
		"--network", "projects/test-project/global/networks/default",
		"--shard-count", "1",
		"--replica-count", "0",
		"--acl-policy", policyName,
		"--quiet",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "clusters", "delete", name, "--region", location, "--quiet", "--format=json").Run()
	})
	var cluster struct {
		AclPolicy     string `json:"aclPolicy"`
		AclPolicyInfo struct {
			AppliedAclPolicyRevisionNumber string `json:"appliedAclPolicyRevisionNumber"`
		} `json:"aclPolicyInfo"`
		DiscoveryEndpoints []struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"discoveryEndpoints"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("redis", "clusters", "describe", name, "--region", location, "--format=json")), &cluster)
	assert.Equal(t, policyName, cluster.AclPolicy)
	assert.Equal(t, "1", cluster.AclPolicyInfo.AppliedAclPolicyRevisionNumber)
	require.Len(t, cluster.DiscoveryEndpoints, 1)
	discovery := net.JoinHostPort(cluster.DiscoveryEndpoints[0].Address, strconv.Itoa(cluster.DiscoveryEndpoints[0].Port))
	conn, err := net.Dial("tcp", discovery)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	assert.Equal(t, "+OK", redisCommand(t, conn, "AUTH", "app", "cli-secret"))
	assert.Equal(t, "+OK", redisCommand(t, conn, "SET", "app-1", "one"))
	assert.Contains(t, redisCommand(t, conn, "SET", "other", "one"), "NOPERM")

	out, err := gcloudCLI("redis", "acl-policies", "delete", policy, "--region", location, "--quiet", "--format=json").CombinedOutput()
	require.Error(t, err, "an attached policy cannot be deleted: %s", out)
	assert.Contains(t, string(out), "FAILED_PRECONDITION")

	runCLI(t, gcloudCLI("redis", "acl-policies", "update", policy, "--region", location,
		`--rules=[{"username":"app","rule":"on >cli-secret ~app-* +@read +@connection"}]`, "--quiet", "--format=json"))
	parseJSON(t, runCLI(t, gcloudCLI("redis", "clusters", "describe", name, "--region", location, "--format=json")), &cluster)
	assert.Equal(t, "2", cluster.AclPolicyInfo.AppliedAclPolicyRevisionNumber)
	revised, err := net.Dial("tcp", discovery)
	require.NoError(t, err)
	t.Cleanup(func() { revised.Close() })
	assert.Equal(t, "+OK", redisCommand(t, revised, "AUTH", "app", "cli-secret"))
	assert.Equal(t, "one", redisCommand(t, revised, "GET", "app-1"))
	assert.Contains(t, redisCommand(t, revised, "SET", "app-1", "two"), "NOPERM", "the revised rule grants reads only")
}
