package acmvalidation_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestACMCertificateValidationTerraform exercises the real consumer flow that
// hit a known shape: a DNS-validated cert with a wildcard SAN, the
// Route53 _acm-challenge records built from domain_validation_options, and
// aws_acm_certificate_validation waiting for ISSUED. Before the fix this hung
// to the validation timeout or failed the record-name consistency
// check on the literal '*'.
func TestACMCertificateValidationTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.Contains(t, outputs.must(t, "certificate_arn"), "arn:aws:acm:us-east-1:")
	// aws_acm_certificate_validation only finishes creating (and exports an id)
	// once DescribeCertificate reports ISSUED — so a non-empty validation_id is
	// the end-to-end proof. The apply also completing (not timing out)
	// confirms it.
	require.NotEmpty(t, outputs.must(t, "validation_id"))
	// The wildcard SAN's record name is de-wildcarded (base domain).
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
