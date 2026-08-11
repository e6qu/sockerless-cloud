package launchtemplate_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestLaunchTemplateTerraform exercises the EC2 Launch Template ops through
// terraform-provider-aws: aws_launch_template create +
// read-back, then destroy. This is the fck-nat aws_launch_template shape that
// previously failed at CreateLaunchTemplate (InvalidAction). A clean apply
// with no follow-up diff proves the launch-template data round-trips.
func TestLaunchTemplateTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.Contains(t, outputs.must(t, "launch_template_id"), "lt-")
	require.Equal(t, "ami-12345678", outputs.must(t, "image_id"),
		"aws_launch_template image_id must round-trip")
	require.Equal(t, "1", outputs.must(t, "latest_version"))

	env.Terraform(t, "destroy", "-auto-approve")
}

type tfOutputs map[string]struct {
	Value any `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	switch s := v.Value.(type) {
	case string:
		require.NotEmpty(t, s, "output %q is empty", key)
		return s
	case float64:
		return trimFloat(s)
	default:
		require.Failf(t, "unexpected output type", "output %q is %T", key, v.Value)
		return ""
	}
}

func trimFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
