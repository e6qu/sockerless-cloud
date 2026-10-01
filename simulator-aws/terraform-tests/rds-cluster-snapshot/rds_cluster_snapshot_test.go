package rds_cluster_snapshot_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// terraform-provider-aws takes an Aurora DB cluster snapshot, waits for it
// to settle, and restores a cluster from it, waiting for the restored cluster
// to become available.
func TestRDSClusterSnapshotTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.True(t, strings.HasPrefix(outputs.must(t, "snapshot_arn"), "arn:aws:rds:us-east-1:"))
	require.Contains(t, outputs.must(t, "snapshot_arn"), ":cluster-snapshot:tf-aurora-snapshot")
	require.Equal(t, "available", outputs.must(t, "snapshot_status"))
	require.Equal(t, "terraform", outputs.must(t, "snapshot_tags_env"))
	require.Contains(t, outputs.must(t, "restored_arn"), ":cluster:tf-aurora-snapshot-restored")
	require.Equal(t, "dbadmin", outputs.must(t, "restored_master_username"))
	require.Equal(t, "application", outputs.must(t, "restored_database_name"))

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
