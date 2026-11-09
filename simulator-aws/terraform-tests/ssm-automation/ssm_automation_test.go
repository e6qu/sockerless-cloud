package ssm_automation_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSMAutomationTerraform creates an Automation runbook with
// aws_ssm_document and registers it as a maintenance window's AUTOMATION task
// through terraform-provider-aws, reads both back after a refresh, and plans no
// drift.
func TestSSMAutomationTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	vars := []string{"-var", "parameter_name=/tf/automation/written"}
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars...)...)
	env.Terraform(t, append([]string{"apply", "-auto-approve", "-refresh-only"}, vars...)...)

	var raw map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &raw))
	assert.Equal(t, "Automation", raw["document_type"].Value)
	assert.Equal(t, "0.3", raw["schema_version"].Value)
	assert.Equal(t, "1", raw["document_version"].Value)
	assert.Equal(t, "AUTOMATION", raw["task_type"].Value)
	assert.Equal(t, "tf-automation-runbook", raw["task_arn"].Value)
	env.Terraform(t, append([]string{"plan", "-detailed-exitcode"}, vars...)...)

	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars...)...)
}
