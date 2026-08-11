package rds_instance_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

func TestRDSInstanceTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.True(t, strings.HasPrefix(outputs.must(t, "rds_instance_arn"), "arn:aws:rds:us-east-1:"),
		"RDS instance ARN must include the rds-region prefix")
	require.Contains(t, outputs.must(t, "rds_instance_arn"), ":db:tf-rds-db",
		"RDS instance ARN must end with :db:<identifier>")
	require.Equal(t, "postgres", outputs.must(t, "rds_instance_engine"),
		"RDS engine must round-trip through terraform-provider-aws refresh")
	port, err := strconv.Atoi(outputs.must(t, "rds_instance_port"))
	require.NoError(t, err)
	require.Positive(t, port, "RDS endpoint port must round-trip through provider refresh")
	require.Equal(t, "terraform", outputs.must(t, "rds_instance_tags_env"),
		"RDS tags must round-trip through ListTagsForResource")

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
