package tutorial_test

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTutorialStack runs the AWS steps of docs/tutorial.md: init, apply, read
// the outputs, plan again with nothing to change, destroy.
func TestTutorialStack(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.NotNil(t, net.ParseIP(outputs["nat_gateway_public_ip"].Value), "the NAT gateway holds its Elastic IP")
	assert.Equal(t, "running", outputs["instance_state"].Value)
	privateIP := net.ParseIP(outputs["instance_private_ip"].Value)
	require.NotNil(t, privateIP)
	_, private, err := net.ParseCIDR("10.240.2.0/24")
	require.NoError(t, err)
	assert.True(t, private.Contains(privateIP), "the instance takes an address in its private subnet")
	assert.Equal(t, "tutorial-web", outputs["ecs_service"].Value)
	assert.Equal(t, "Hello from S3", outputs["s3_object_body"].Value)
	assert.Contains(t, outputs["dynamodb_table_arn"].Value, ":table/tutorial-orders")
	assert.Contains(t, outputs["queue_url"].Value, "/tutorial-orders")
	assert.JSONEq(t, `{"received":0,"greeting":"hello"}`, outputs["lambda_result"].Value,
		"the function ran the handler's code")
	assert.Equal(t, "Enabled", outputs["event_source_mapping_state"].Value)
	assert.Contains(t, outputs["alarm_arn"].Value, ":alarm:tutorial-orders-backlog")

	env.Terraform(t, "plan", "-detailed-exitcode")
	env.Terraform(t, "destroy", "-auto-approve")
}
