package acmvalidation_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestACMCertificateValidationTerraform applies a DNS-validated certificate
// with a wildcard SAN, the Route 53 _acm-challenge records built from
// domain_validation_options, and an aws_acm_certificate_validation that waits
// for ISSUED.
func TestACMCertificateValidationTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.Contains(t, outputs.must(t, "certificate_arn"), "arn:aws:acm:us-east-1:")
	// aws_acm_certificate_validation exports an id only once DescribeCertificate
	// reports ISSUED.
	require.NotEmpty(t, outputs.must(t, "validation_id"))
	// ACM names a wildcard SAN's validation record after its base domain.
	names := outputs.must(t, "validation_record_names")
	require.Contains(t, names, "_acm-challenge.devbox.example.test")
	require.NotContains(t, names, "*", "validation record name must not carry a literal '*'")

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
