package logs_transformer_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsTransformerTerraform creates an Amazon CloudWatch Logs transformer
// through terraform-provider-aws, reads it back after a refresh, changes a
// processor in place, and plans no drift after each apply.
func TestLogsTransformerTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")

	for _, environment := range []string{"staging", "production"} {
		env.Terraform(t, "apply", "-auto-approve", "-var", "environment="+environment)
		env.Terraform(t, "apply", "-auto-approve", "-refresh-only", "-var", "environment="+environment)
		outputs := readOutputs(t, env)
		assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:/tf/logs-transformer", outputs["log_group_arn"])
		assert.Equal(t, "6", outputs["processor_count"])
		assert.Equal(t, environment, outputs["environment"])
		assert.Equal(t, "dd/MMM/yyyy:HH:mm:ss Z", outputs["date_pattern"])
		env.Terraform(t, "plan", "-detailed-exitcode", "-var", "environment="+environment)
	}

	env.Terraform(t, "destroy", "-auto-approve", "-var", "environment=production")
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
