package sns_delivery_status_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSNSDeliveryStatusTerraform sets an Amazon SNS topic's Lambda and Amazon
// SQS delivery status logging arguments through terraform-provider-aws, reads
// them back after a refresh, changes the success sample rate in place, and
// plans no drift after each apply.
func TestSNSDeliveryStatusTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")

	for _, rate := range []string{"100", "25"} {
		env.Terraform(t, "apply", "-auto-approve", "-var", "success_sample_rate="+rate)
		env.Terraform(t, "apply", "-auto-approve", "-refresh-only", "-var", "success_sample_rate="+rate)
		outputs := readOutputs(t, env)
		role := outputs["role_arn"]
		require.NotEmpty(t, role)
		assert.Equal(t, role, outputs["lambda_success_feedback_role_arn"])
		assert.Equal(t, role, outputs["lambda_failure_feedback_role_arn"])
		assert.Equal(t, role, outputs["sqs_failure_feedback_role_arn"])
		assert.Equal(t, rate, outputs["lambda_success_feedback_sample_rate"])
		assert.Equal(t, rate, outputs["sqs_success_feedback_sample_rate"])
		env.Terraform(t, "plan", "-detailed-exitcode", "-var", "success_sample_rate="+rate)
	}

	env.Terraform(t, "destroy", "-auto-approve", "-var", "success_sample_rate=25")
}

func readOutputs(t *testing.T, env *tfsim.Env) map[string]string {
	t.Helper()
	var raw map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &raw))
	out := map[string]string{}
	for key, value := range raw {
		out[key] = value.Value
	}
	return out
}
