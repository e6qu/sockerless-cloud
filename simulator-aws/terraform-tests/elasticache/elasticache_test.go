package elasticache_test

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestElastiCacheTerraform applies a cache cluster, a replication group and a
// serverless cache, and connects to the endpoints Terraform read back: each
// is an engine that keeps what a client writes, the replication group's
// reader endpoint reads what its primary endpoint took, and the serverless
// cache answers a cluster client over TLS.
func TestElastiCacheTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.True(t, strings.HasPrefix(outputs.must(t, "elasticache_cluster_arn"), "arn:aws:elasticache:us-east-1:"),
		"ElastiCache cluster ARN must include the elasticache-region prefix")
	require.Contains(t, outputs.must(t, "elasticache_cluster_arn"), ":cluster:tf-cache",
		"ElastiCache cluster ARN must end with :cluster:<identifier>")
	require.Equal(t, "redis", outputs.must(t, "elasticache_cluster_engine"),
		"ElastiCache engine must round-trip through terraform-provider-aws refresh")
	require.Equal(t, "6379", outputs.must(t, "elasticache_cluster_port"),
		"ElastiCache redis port must round-trip through provider refresh")
	require.Equal(t, "terraform", outputs.must(t, "elasticache_cluster_tags_env"),
		"ElastiCache tags must round-trip through ListTagsForResource")
	require.Equal(t, "6379", outputs.must(t, "elasticache_cluster_node_port"))

	cluster := goredis.NewClient(&goredis.Options{
		Addr: net.JoinHostPort(outputs.must(t, "elasticache_cluster_node_address"), outputs.must(t, "elasticache_cluster_node_port")),
	})
	t.Cleanup(func() { cluster.Close() })
	require.NoError(t, cluster.Set(t.Context(), "terraform", "cache cluster", 0).Err())
	value, err := cluster.Get(t.Context(), "terraform").Result()
	require.NoError(t, err)
	require.Equal(t, "cache cluster", value)

	require.Equal(t, "tf-group-001,tf-group-002", outputs.must(t, "replication_group_members"))
	port := outputs.must(t, "replication_group_port")
	require.Equal(t, "6385", port)
	require.NotEqual(t, outputs.must(t, "replication_group_primary_address"), outputs.must(t, "replication_group_reader_address"),
		"the primary and reader endpoints are addresses of their own")
	primary := goredis.NewClient(&goredis.Options{Addr: net.JoinHostPort(outputs.must(t, "replication_group_primary_address"), port)})
	t.Cleanup(func() { primary.Close() })
	require.NoError(t, primary.Set(t.Context(), "terraform", "replication group", 0).Err())
	acknowledged, err := primary.Wait(t.Context(), 1, 0).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), acknowledged)
	reader := goredis.NewClient(&goredis.Options{Addr: net.JoinHostPort(outputs.must(t, "replication_group_reader_address"), port)})
	t.Cleanup(func() { reader.Close() })
	value, err = reader.Get(t.Context(), "terraform").Result()
	require.NoError(t, err)
	require.Equal(t, "replication group", value)

	require.Equal(t, "6379", outputs.must(t, "serverless_endpoint_port"))
	// ElastiCache presents a certificate a public root signs; the client
	// checks encryption, not the chain.
	serverless := goredis.NewClusterClient(&goredis.ClusterOptions{
		Addrs:     []string{net.JoinHostPort(outputs.must(t, "serverless_endpoint_address"), outputs.must(t, "serverless_endpoint_port"))},
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // see above
	})
	t.Cleanup(func() { serverless.Close() })
	require.NoError(t, serverless.Set(t.Context(), "terraform", "serverless", 0).Err())
	value, err = serverless.Get(t.Context(), "terraform").Result()
	require.NoError(t, err)
	require.Equal(t, "serverless", value)

	env.Terraform(t, "destroy", "-auto-approve")
}

type tfOutputs map[string]struct {
	Value any `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is not a string (got %T)", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
