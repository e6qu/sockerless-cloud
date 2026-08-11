package networkinterface_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestNetworkInterfaceTerraform exercises the standalone ENI ops
// through terraform-provider-aws: aws_network_interface (Create +
// ModifyNetworkInterfaceAttribute for source_dest_check) + an instance +
// aws_network_interface_attachment (Attach), then destroy (Detach + Delete).
// This is the fck-nat NAT-instance shape that previously failed at the ENI.
func TestNetworkInterfaceTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.Contains(t, outputs.must(t, "eni_id"), "eni-")
	require.Equal(t, "false", outputs.must(t, "source_dest_check"),
		"aws_network_interface source_dest_check=false must round-trip")

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
	case bool:
		if s {
			return "true"
		}
		return "false"
	default:
		require.Failf(t, "unexpected output type", "output %q is %T", key, v.Value)
		return ""
	}
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
