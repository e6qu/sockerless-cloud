package logs_policies_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsPoliciesTerraform creates an Amazon CloudWatch Logs log group-level
// data protection policy, account-level data protection, field index and
// metric extraction policies, and a log group-level index policy through
// terraform-provider-aws, reads them back after a refresh, and plans no
// drift.
func TestLogsPoliciesTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	env.Terraform(t, "apply", "-auto-approve", "-refresh-only")

	var raw map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &raw))
	assert.Equal(t, "/tf/logs-policies/app", raw["data_protection_group"].Value)
	assert.Equal(t, "/tf/logs-policies/own-index", raw["index_policy_group"].Value)
	assert.Equal(t, `LogGroupNamePrefix IN ["/tf/logs-policies/"]`, raw["field_index_selection"].Value)

	env.Terraform(t, "plan", "-detailed-exitcode")
	env.Terraform(t, "destroy", "-auto-approve")
}
