package iam_multi_account_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAMMultiAccountTerraform creates a member account with
// aws_organizations_account and attaches it a service control policy that
// denies sqs:CreateQueue. A second configuration's provider assumes the
// account's OrganizationAccountAccessRole and creates an IAM role that
// belongs to the member account; the queue it asks for is refused by the
// service control policy.
func TestIAMMultiAccountTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	member := outputs["member_account_id"].Value
	require.Regexp(t, `^\d{12}$`, member)

	inMember := env.In("member")
	inMember.Terraform(t, "init")
	vars := func(extra ...string) []string {
		return append([]string{"-auto-approve", "-var", "member_account_id=" + member}, extra...)
	}
	inMember.Terraform(t, append([]string{"apply"}, vars()...)...)
	require.NoError(t, json.Unmarshal(inMember.Terraform(t, "output", "-json"), &outputs))
	assert.Equal(t, "arn:aws:iam::"+member+":role/tf-member-role", outputs["role_arn"].Value,
		"the role belongs to the member account")

	refused := string(inMember.TerraformFails(t, append([]string{"apply"}, vars("-var", "create_queue=true")...)...))
	assert.Contains(t, refused, "AccessDenied", "the service control policy refuses the queue")

	inMember.Terraform(t, append([]string{"destroy"}, vars()...)...)
	env.Terraform(t, "destroy", "-auto-approve")
}
