package natgatewayregional_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestRegionalNatGatewayTerraform applies regional aws_nat_gateway resources
// in automatic and manual mode, changes the manual gateway's per-zone
// addresses in place, and plans each stage again to an empty diff.
func TestRegionalNatGatewayTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	env.Terraform(t, "plan", "-detailed-exitcode")

	out := readOutputs(t, env)
	autoID := out.must(t, "auto_id")
	require.Equal(t, "regional", out.must(t, "auto_mode"))
	require.Equal(t, "enabled", out.must(t, "auto_provision_zones"))
	require.Equal(t, "enabled", out.must(t, "auto_scaling_ips"))
	require.Equal(t, autoID, out.must(t, "auto_route_table_id"))
	require.Equal(t, "us-east-1a,us-east-1b", out.must(t, "auto_zones"),
		"an automatic regional gateway covers the zones of the VPC's subnets")

	eips := strings.Split(out.must(t, "eips"), ",")
	require.Len(t, eips, 3)
	require.Equal(t, "disabled", out.must(t, "manual_auto_provision_zones"))
	require.Equal(t, "us-east-1a="+eips[0]+",us-east-1b="+eips[1], out.must(t, "manual_addresses"))
	require.Equal(t, out.must(t, "manual_id"), out.must(t, "route_nat_gateway_id"),
		"the private route table's default route must name the regional gateway")

	manualID := out.must(t, "manual_id")
	env.Terraform(t, "apply", "-auto-approve", "-var", "second_zone=false")
	env.Terraform(t, "plan", "-detailed-exitcode", "-var", "second_zone=false")
	out = readOutputs(t, env)
	require.Equal(t, manualID, out.must(t, "manual_id"), "changing a manual gateway's addresses must update it in place")
	want := []string{"us-east-1a=" + eips[0], "us-east-1a=" + eips[2]}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	require.Equal(t, strings.Join(want, ","), out.must(t, "manual_addresses"))

	env.Terraform(t, "destroy", "-auto-approve", "-var", "second_zone=false")
}

type tfOutputs map[string]struct {
	Value any `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is %T", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
