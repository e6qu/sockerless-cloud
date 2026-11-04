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
// service control policy, and the organizational unit it asks for by AWS
// Organizations, which leaves that to the management account. Destroying
// aws_organizations_account removes the account from the organization, and
// the provider keeps working in it as a standalone account.
func TestIAMMultiAccountTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	var outputs map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	value := func(name string) string {
		t.Helper()
		var s string
		require.NoError(t, json.Unmarshal(outputs[name].Value, &s), name)
		return s
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	member := value("member_account_id")
	require.Regexp(t, `^\d{12}$`, member)
	root := value("organization_root_id")
	require.Regexp(t, `^r-`, root)

	inMember := env.In("member")
	inMember.Terraform(t, "init")
	vars := func(extra ...string) []string {
		return append([]string{"-auto-approve", "-var", "member_account_id=" + member}, extra...)
	}
	inMember.Terraform(t, append([]string{"apply"}, vars()...)...)
	require.NoError(t, json.Unmarshal(inMember.Terraform(t, "output", "-json"), &outputs))
	assert.Equal(t, "arn:aws:iam::"+member+":role/tf-member-role", value("role_arn"),
		"the role belongs to the member account")
	assert.Equal(t, "123456789012", value("management_account_id"))
	assert.JSONEq(t, "0", string(outputs["listed_accounts"].Value), "a member account lists no accounts")

	refused := string(inMember.TerraformFails(t, append([]string{"apply"}, vars("-var", "create_queue=true")...)...))
	assert.Contains(t, refused, "AccessDenied", "the service control policy refuses the queue")
	assert.Contains(t, refused, "with an explicit deny in a service control policy")

	refused = string(inMember.TerraformFails(t, append([]string{"apply"}, vars("-var", "organization_root_id="+root)...)...))
	assert.Contains(t, refused, "AccessDeniedException", "a member account creates no organizational unit")
	assert.Contains(t, refused, "You don't have permissions to perform the requested operation.")

	env.Terraform(t, "destroy", "-auto-approve")
	standalone := func(extra ...string) []string {
		return vars(append([]string{"-var", "in_organization=false"}, extra...)...)
	}
	inMember.Terraform(t, append([]string{"apply"}, standalone("-var", "create_queue=true")...)...)
	require.NoError(t, json.Unmarshal(inMember.Terraform(t, "output", "-json"), &outputs))
	assert.Equal(t, "arn:aws:iam::"+member+":role/tf-member-role", value("role_arn"),
		"the removed account keeps its role")
	inMember.Terraform(t, append([]string{"destroy"}, standalone()...)...)
}
