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
// metric extraction policies, a log group-level index policy, a field index
// policy selecting a data source, and an Amazon EventBridge rule targeting a
// log group a CloudWatch Logs resource policy opens to it, through
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
	assert.Equal(t, `DataSourceName = "tfapp" AND DataSourceType = "orders"`, raw["data_source_selection"].Value)
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:/aws/events/tf-logs-policies", raw["log_group_target"].Value)

	env.Terraform(t, "plan", "-detailed-exitcode")
	env.Terraform(t, "destroy", "-auto-approve")
}
