package apigatewaystage_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayStageTerraform applies a REST API stage with stage variables,
// X-Ray tracing, a cache cluster, CloudWatch Logs access logging and tags,
// plus method settings for every method, then checks a second plan is empty
// and destroys the stack.
func TestAPIGatewayStageTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	apiID := outputs["api_id"].Value
	require.Regexp(t, regexp.MustCompile(`^[a-z0-9]{10}$`), apiID)
	require.Equal(t, "arn:aws:apigateway:us-east-1::/restapis/"+apiID+"/stages/prod", outputs["stage_arn"].Value)
	require.Equal(t, "https://"+apiID+".execute-api.us-east-1.amazonaws.com/prod", outputs["invoke_url"].Value)

	env.Terraform(t, "plan", "-detailed-exitcode")
	env.Terraform(t, "destroy", "-auto-approve")
}
