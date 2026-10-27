package idempotencyfidelity_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestIdempotencyFidelity applies resources whose read-back fields must match
// what terraform-provider-aws wrote, then plans again expecting no drift:
//   - aws_vpc_security_group_egress_rule ip_protocol="-1" (no from/to port)
//   - aws_vpc_security_group_ingress_rule referenced_security_group_id (bare id)
//   - aws_nat_gateway connectivity_type
//   - aws_lb (no minimum_load_balancer_capacity)
//   - aws_lb_listener HTTPS certificate_arn
//   - tags on aws_cloudwatch_log_group / aws_dynamodb_table / aws_ecr_repository
func TestIdempotencyFidelity(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	// -detailed-exitcode exits 2 on any drift.
	env.Terraform(t, "plan", "-detailed-exitcode")

	outputs := readOutputs(t, env)
	require.Contains(t, outputs.must(t, "nat_gateway_id"), "nat-")
	require.Contains(t, outputs.must(t, "listener_certificate_arn"), "arn:aws:acm:",
		"HTTPS listener certificate_arn must round-trip through state")

	// An NLB's dns_name is a hostname, never a host:port, or the Route 53 alias
	// to it is invalid.
	nlbDNS := outputs.must(t, "nlb_dns_name")
	require.NotContains(t, nlbDNS, ":", "NLB dns_name must be a hostname, not host:port")
	require.Regexp(t, `^fidelity-nlb-[0-9a-f]+\.elb\.us-east-1\.amazonaws\.com$`, nlbDNS,
		"NLB dns_name must be the AWS-shaped NLB hostname")
	require.NotEmpty(t, outputs.must(t, "nlb_zone_id"),
		"NLB zone_id (CanonicalHostedZoneId) is required for the Route53 alias target")

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
	require.Truef(t, ok, "output %q is %T, want string", key, v.Value)
	require.NotEmpty(t, strings.TrimSpace(s), "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
