package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cliOrgAccessDenied = "You don't have permissions to perform the requested operation."

// TestOrganizations_MemberAccountCallers_CLI calls `aws organizations` as a
// member account's administrator: describe-organization answers, the
// management account's commands are refused, and list-accounts answers while
// the account is a delegated administrator for a service.
func TestOrganizations_MemberAccountCallers_CLI(t *testing.T) {
	t.Parallel()
	member := cliMemberAccount(t, "cli-org-callers")
	session := cliMemberAdmin(t, member, "OrganizationAccountAccessRole")
	root := strings.TrimSpace(runCLI(t, awsCLI("organizations", "list-roots", "--query", "Roots[0].Id", "--output", "text")))

	management := strings.TrimSpace(runCLI(t, session.asSession(awsCLI("organizations", "describe-organization",
		"--query", "Organization.MasterAccountId", "--output", "text"))))
	assert.Equal(t, "123456789012", management)
	for _, args := range [][]string{
		{"organizations", "list-accounts"},
		{"organizations", "create-organizational-unit", "--parent-id", root, "--name", "cli-nested"},
		{"organizations", "enable-policy-type", "--root-id", root, "--policy-type", "TAG_POLICY"},
		{"organizations", "remove-account-from-organization", "--account-id", member},
	} {
		refused := runCLIExpectError(t, session.asSession(awsCLI(args...)))
		assert.Contains(t, refused, "AccessDeniedException", args[1])
		assert.Contains(t, refused, cliOrgAccessDenied, args[1])
	}

	runCLI(t, awsCLI("organizations", "register-delegated-administrator", "--account-id", member, "--service-principal", "config.amazonaws.com"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("organizations", "deregister-delegated-administrator", "--account-id", member, "--service-principal", "config.amazonaws.com"))
	})
	accounts := runCLI(t, session.asSession(awsCLI("organizations", "list-accounts", "--query", "Accounts[].Id", "--output", "text")))
	assert.Contains(t, strings.Fields(accounts), member, "a delegated administrator reads the organization")
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "remove-account-from-organization", "--account-id", member)),
		"CANNOT_REMOVE_DELEGATED_ADMINISTRATOR_FROM_ORG")
}

// TestOrganizations_RemovedAccountRunsStandalone_CLI removes a member account
// with `aws organizations remove-account-from-organization`: the service
// control policy that refused its user stops applying, and the user keeps
// working in the standalone account.
func TestOrganizations_RemovedAccountRunsStandalone_CLI(t *testing.T) {
	t.Parallel()
	member := cliMemberAccount(t, "cli-standalone")
	session := cliMemberAdmin(t, member, "OrganizationAccountAccessRole")
	runCLI(t, session.asSession(awsCLI("iam", "create-user", "--user-name", "cli-standalone-user")))
	runCLI(t, session.asSession(awsCLI("iam", "put-user-policy", "--user-name", "cli-standalone-user", "--policy-name", "all",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)))
	var key struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, runCLI(t, session.asSession(awsCLI("iam", "create-access-key", "--user-name", "cli-standalone-user", "--output", "json"))), &key)
	asUser := func(cmd *exec.Cmd) *exec.Cmd {
		cmd.Env = withCreds(cmd.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
		return cmd
	}

	policy := strings.TrimSpace(runCLI(t, awsCLI("organizations", "create-policy", "--name", "cli-deny-roles",
		"--description", "deny", "--type", "SERVICE_CONTROL_POLICY",
		"--content", `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"iam:ListRoles","Resource":"*"}]}`,
		"--query", "Policy.PolicySummary.Id", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("organizations", "delete-policy", "--policy-id", policy)) })
	runCLI(t, awsCLI("organizations", "attach-policy", "--policy-id", policy, "--target-id", member))
	refused := runCLIExpectError(t, asUser(awsCLI("iam", "list-roles")))
	assert.Contains(t, refused, "User: arn:aws:iam::"+member+":user/cli-standalone-user is not authorized to perform: iam:ListRoles with an explicit deny in a service control policy")

	runCLI(t, awsCLI("organizations", "remove-account-from-organization", "--account-id", member))
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "describe-account", "--account-id", member)), "AccountNotFoundException")
	listed := runCLI(t, awsCLI("organizations", "list-accounts", "--query", "Accounts[].Id", "--output", "text"))
	assert.NotContains(t, strings.Fields(listed), member)

	arn := strings.TrimSpace(runCLI(t, asUser(awsCLI("sts", "get-caller-identity", "--query", "Arn", "--output", "text"))))
	assert.Equal(t, "arn:aws:iam::"+member+":user/cli-standalone-user", arn)
	runCLI(t, asUser(awsCLI("iam", "list-roles")))
	assert.Contains(t, runCLIExpectError(t, asUser(awsCLI("organizations", "describe-organization"))), "AWSOrganizationsNotInUseException")
}

// TestOrganizations_MemberLeavesOrganization_CLI runs `aws organizations
// leave-organization` from a member account.
func TestOrganizations_MemberLeavesOrganization_CLI(t *testing.T) {
	t.Parallel()
	member := cliMemberAccount(t, "cli-leaver")
	session := cliMemberAdmin(t, member, "OrganizationAccountAccessRole")
	runCLI(t, session.asSession(awsCLI("organizations", "leave-organization")))
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "describe-account", "--account-id", member)), "AccountNotFoundException")
	roles := runCLI(t, session.asSession(awsCLI("iam", "list-roles", "--query", "Roles[].RoleName", "--output", "text")))
	require.Contains(t, strings.Fields(roles), "OrganizationAccountAccessRole", "the account's identities stay")
}
