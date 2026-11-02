package elasticache_snapshot_test

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestElastiCacheSnapshotTerraform writes keys to a cache cluster, destroys it
// with final_snapshot_identifier, and restores the final snapshot through
// snapshot_name and snapshot_arns_to_restore: every restored engine holds the
// keys.
func TestElastiCacheSnapshotTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	apply := func(stage int, extra ...string) tfOutputs {
		t.Helper()
		env.Terraform(t, append([]string{"apply", "-auto-approve", "-var", fmt.Sprintf("stage=%d", stage)}, extra...)...)
		var outputs tfOutputs
		require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
		return outputs
	}

	outputs := apply(1)
	source := goredis.NewClient(&goredis.Options{Addr: net.JoinHostPort(outputs.must(t, "source_address"), outputs.must(t, "source_port"))})
	t.Cleanup(func() { source.Close() })
	for i := 0; i < 10; i++ {
		require.NoError(t, source.Set(t.Context(), fmt.Sprintf("terraform-%d", i), i, 0).Err())
	}
	snapshotARN := strings.Replace(outputs.must(t, "source_arn"), ":cluster:tf-ec-snapshot-source", ":snapshot:tf-ec-final", 1)
	require.NotEqual(t, outputs.must(t, "source_arn"), snapshotARN)

	apply(2)
	restoreVars := []string{"-var", "snapshot_arn=" + snapshotARN}
	outputs = apply(3, restoreVars...)

	requireKeys := func(client goredis.Cmdable, what string) {
		t.Helper()
		for i := 0; i < 10; i++ {
			value, err := client.Get(t.Context(), fmt.Sprintf("terraform-%d", i)).Result()
			require.NoError(t, err, "the %s holds the snapshot's keys", what)
			require.Equal(t, fmt.Sprint(i), value)
		}
	}
	restored := goredis.NewClient(&goredis.Options{Addr: net.JoinHostPort(outputs.must(t, "restored_address"), outputs.must(t, "restored_port"))})
	t.Cleanup(func() { restored.Close() })
	requireKeys(restored, "cache cluster restored through snapshot_name")
	group := goredis.NewClient(&goredis.Options{Addr: net.JoinHostPort(outputs.must(t, "group_primary_address"), outputs.must(t, "group_port"))})
	t.Cleanup(func() { group.Close() })
	requireKeys(group, "replication group restored through snapshot_name")
	// ElastiCache presents a certificate a public root signs; the client checks
	// encryption, not the chain.
	serverless := goredis.NewClusterClient(&goredis.ClusterOptions{
		Addrs:     []string{net.JoinHostPort(outputs.must(t, "serverless_address"), outputs.must(t, "serverless_port"))},
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec // see above
	})
	t.Cleanup(func() { serverless.Close() })
	requireKeys(serverless, "serverless cache restored through snapshot_arns_to_restore")

	env.Terraform(t, append([]string{"destroy", "-auto-approve", "-var", "stage=3"}, restoreVars...)...)
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
