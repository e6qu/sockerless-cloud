package ecsexpress_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestECSExpressModeTerraform exercises ECS Express Mode (the Express Gateway service API)
// (aws_ecs_express_gateway_service) through terraform-provider-aws:
// CreateExpressGatewayService on apply, DescribeExpressGatewayService on refresh,
// DeleteExpressGatewayService on destroy. The resource provisions the managed
// bundle (Fargate service + ALB + ACM + auto-scaling); apply asserts the
// computed service ARN and HTTPS ingress endpoint round-trip into TF state, and
// destroy tears it down cleanly.
func TestECSExpressModeTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.Contains(t, outputs.must(t, "express_service_arn"), "express-gateway-service",
		"service ARN must use the express-gateway-service resource type")
	require.Equal(t, "tf-express-web", outputs.must(t, "express_service_name"),
		"service name must round-trip through provider refresh")

	endpoint := firstIngressEndpoint(t, env)
	require.True(t, strings.HasPrefix(endpoint, "https://"),
		"ingress endpoint must be an HTTPS ALB URL, got %q", endpoint)

	// Idempotency: a second plan must report no drift.
	env.Terraform(t, "plan", "-detailed-exitcode")

	env.Terraform(t, "destroy", "-auto-approve")
}

// firstIngressEndpoint reads the computed ingress_paths list output and returns
// the first endpoint.
func firstIngressEndpoint(t *testing.T, env *tfsim.Env) string {
	t.Helper()
	var outputs map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	var paths []struct {
		AccessType string `json:"access_type"`
		Endpoint   string `json:"endpoint"`
	}
	require.NoError(t, json.Unmarshal(outputs["express_ingress_paths"].Value, &paths))
	require.NotEmpty(t, paths, "express_ingress_paths must have at least one entry")
	require.Equal(t, "PUBLIC", paths[0].AccessType, "default access type must be PUBLIC")
	return paths[0].Endpoint
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
