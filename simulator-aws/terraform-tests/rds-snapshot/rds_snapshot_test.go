package rds_snapshot_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

func TestRDSSnapshotTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.True(t, strings.HasPrefix(outputs.must(t, "rds_snapshot_arn"), "arn:aws:rds:us-east-1:"),
		"RDS snapshot ARN must include the rds-region prefix")
	require.Contains(t, outputs.must(t, "rds_snapshot_arn"), ":snapshot:tf-rds-snapshot",
		"RDS snapshot ARN must end with :snapshot:<identifier>")
	require.Equal(t, "available", outputs.must(t, "rds_snapshot_status"),
		"RDS snapshot status must round-trip through terraform-provider-aws refresh")
	require.Equal(t, "terraform", outputs.must(t, "rds_snapshot_tags_env"),
		"RDS snapshot tags must round-trip through ListTagsForResource")

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
