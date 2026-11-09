package cloudwatch_anomaly_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatchAnomalyTerraform creates a CloudWatch Logs anomaly detector,
// an alarm on an anomaly detection band and a Contributor Insights rule
// through terraform-provider-aws, widens the band in place, and plans no
// drift after each apply.
func TestCloudWatchAnomalyTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")

	for _, width := range []string{"2", "3"} {
		env.Terraform(t, "apply", "-auto-approve", "-var", "band_width="+width)
		env.Terraform(t, "apply", "-auto-approve", "-refresh-only", "-var", "band_width="+width)
		var raw map[string]struct {
			Value string `json:"value"`
		}
		require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &raw))
		assert.True(t, strings.HasPrefix(raw["detector_arn"].Value, "arn:aws:logs:us-east-1:123456789012:anomaly-detector:"))
		assert.Equal(t, "ad1", raw["alarm_threshold_metric_id"].Value)
		assert.Equal(t, "arn:aws:cloudwatch:us-east-1:123456789012:insight-rule/tf-top-talkers", raw["rule_arn"].Value)
		env.Terraform(t, "plan", "-detailed-exitcode", "-var", "band_width="+width)
	}

	env.Terraform(t, "destroy", "-auto-approve", "-var", "band_width=3")
}
