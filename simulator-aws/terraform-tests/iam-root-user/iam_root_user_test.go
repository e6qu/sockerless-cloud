package iam_root_user_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAMRootUserTerraform signs the provider with the management account's
// root user key: aws_caller_identity reads the account's root ARN, and the
// IAM user the root user creates belongs to its account.
func TestIAMRootUserTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	account := outputs["account_id"].Value
	require.Regexp(t, `^\d{12}$`, account)
	assert.Equal(t, "arn:aws:iam::"+account+":root", outputs["caller_arn"].Value)
	assert.Equal(t, "arn:aws:iam::"+account+":user/tf-made-by-root", outputs["user_arn"].Value)
	env.Terraform(t, "destroy", "-auto-approve")
}
