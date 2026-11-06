package cognito_user_pool_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCognitoUserPoolTerraform creates an Amazon Cognito user pool with a
// password policy and a custom attribute, an app client for an Application
// Load Balancer's authenticate action, a prefix domain and a user with a
// permanent password through terraform-provider-aws; a second plan finds
// nothing to change.
func TestCognitoUserPoolTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	poolID := outputs["pool_id"].Value
	assert.True(t, strings.HasPrefix(poolID, "us-east-1_"), poolID)
	assert.Equal(t, "arn:aws:cognito-idp:us-east-1:123456789012:userpool/"+poolID, outputs["pool_arn"].Value)
	assert.Equal(t, "tf-login", outputs["pool_domain"].Value)
	assert.NotEmpty(t, outputs["client_id"].Value)
	assert.NotEmpty(t, outputs["client_secret"].Value, "the client generated a secret")
	assert.Equal(t, "CONFIRMED", outputs["user_status"].Value, "a user created with a password has it permanently")
	assert.NotEmpty(t, outputs["user_sub"].Value)

	env.Terraform(t, "plan", "-detailed-exitcode")
	env.Terraform(t, "destroy", "-auto-approve")
}
