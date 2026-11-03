package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliMemberAccount creates a member account with `aws organizations
// create-account` and returns its ID.
func cliMemberAccount(t *testing.T, name string, extra ...string) string {
	t.Helper()
	args := append([]string{"organizations", "create-account", "--account-name", name, "--email", name + "@sim.invalid",
		"--query", "CreateAccountStatus.AccountId", "--output", "text"}, extra...)
	member := strings.TrimSpace(runCLI(t, awsCLI(args...)))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("organizations", "remove-account-from-organization", "--account-id", member))
	})
	return member
}

// cliMemberAdmin assumes the member account's access role as the management
// account's administrator.
func cliMemberAdmin(t *testing.T, member, roleName string) cliSessionCredentials {
	t.Helper()
	var session cliSessionCredentials
	parseJSON(t, runCLI(t, awsCLI("sts", "assume-role", "--role-arn", "arn:aws:iam::"+member+":role/"+roleName,
		"--role-session-name", "cli-admin", "--output", "json")), &session)
	return session
}

// TestOrganizations_AccountAccessRole_CLI reaches a member account through
// the role `aws organizations create-account` puts in it, and works in the
// member account's own IAM.
func TestOrganizations_AccountAccessRole_CLI(t *testing.T) {
	t.Parallel()
	member := cliMemberAccount(t, "cli-access-role", "--role-name", "CliOrgAdmin")
	session := cliMemberAdmin(t, member, "CliOrgAdmin")
	account := strings.TrimSpace(runCLI(t, session.asSession(awsCLI("sts", "get-caller-identity", "--query", "Account", "--output", "text"))))
	assert.Equal(t, member, account)

	arn := strings.TrimSpace(runCLI(t, session.asSession(awsCLI("iam", "create-role", "--role-name", "cli-member-role",
		"--assume-role-policy-document", cliAccountTrust, "--query", "Role.Arn", "--output", "text"))))
	assert.Equal(t, "arn:aws:iam::"+member+":role/cli-member-role", arn)
	assert.Contains(t, runCLIExpectError(t, awsCLI("iam", "get-role", "--role-name", "cli-member-role")), "NoSuchEntity",
		"the member account's role is not the management account's")
	roles := runCLI(t, session.asSession(awsCLI("iam", "list-roles", "--query", "Roles[].RoleName", "--output", "text")))
	assert.ElementsMatch(t, []string{"CliOrgAdmin", "cli-member-role"}, strings.Fields(roles))
	runCLI(t, session.asSession(awsCLI("iam", "delete-role", "--role-name", "cli-member-role")))
}

// TestOrganizations_ServiceControlPolicies_CLI attaches a deny SCP to a
// member account with `aws organizations attach-policy`, which refuses the
// account's principals and leaves the management account alone.
func TestOrganizations_ServiceControlPolicies_CLI(t *testing.T) {
	t.Parallel()
	member := cliMemberAccount(t, "cli-scp-member")
	session := cliMemberAdmin(t, member, "OrganizationAccountAccessRole")
	runCLI(t, session.asSession(awsCLI("iam", "list-users")))

	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "detach-policy", "--policy-id", "p-FullAWSAccess", "--target-id", member)),
		"ConstraintViolationException")
	policy := strings.TrimSpace(runCLI(t, awsCLI("organizations", "create-policy", "--name", "cli-deny-list-users",
		"--description", "deny", "--type", "SERVICE_CONTROL_POLICY",
		"--content", `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"iam:ListUsers","Resource":"*"}]}`,
		"--query", "Policy.PolicySummary.Id", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("organizations", "delete-policy", "--policy-id", policy)) })
	runCLI(t, awsCLI("organizations", "attach-policy", "--policy-id", policy, "--target-id", member))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("organizations", "detach-policy", "--policy-id", policy, "--target-id", member))
	})
	assert.Contains(t, runCLIExpectError(t, session.asSession(awsCLI("iam", "list-users"))), "AccessDenied")
	runCLI(t, session.asSession(awsCLI("iam", "list-roles")))
	runCLI(t, awsCLI("iam", "list-users"))
}

// TestSTS_SourceIdentity_CLI passes --source-identity to `aws sts
// assume-role`: the response returns it, a chained session keeps it, and a
// role whose trust policy does not allow sts:SetSourceIdentity refuses it.
func TestSTS_SourceIdentity_CLI(t *testing.T) {
	t.Parallel()
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":["sts:AssumeRole","sts:SetSourceIdentity"]}]}`
	roleArn := cliLimitsRole(t, "cli-source-identity", trust)
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", "cli-source-identity", "--policy-name", "chain",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:SetSourceIdentity"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-role-policy", "--role-name", "cli-source-identity", "--policy-name", "chain"))
	})
	untrusting := cliLimitsRole(t, "cli-source-untrusting", cliAccountTrust)

	assert.Contains(t, runCLIExpectError(t, awsCLI("sts", "assume-role", "--role-arn", untrusting, "--role-session-name", "si",
		"--source-identity", "alice")), "AccessDenied")
	assert.Contains(t, runCLIExpectError(t, awsCLI("sts", "assume-role", "--role-arn", roleArn, "--role-session-name", "si",
		"--source-identity", "al!ce")), "ValidationError")

	var assumed struct {
		cliSessionCredentials
		SourceIdentity string `json:"SourceIdentity"`
	}
	parseJSON(t, runCLI(t, awsCLI("sts", "assume-role", "--role-arn", roleArn, "--role-session-name", "si",
		"--source-identity", "alice", "--output", "json")), &assumed)
	require.Equal(t, "alice", assumed.SourceIdentity)
	chained := strings.TrimSpace(runCLI(t, assumed.asSession(awsCLI("sts", "assume-role", "--role-arn", roleArn,
		"--role-session-name", "chained", "--query", "SourceIdentity", "--output", "text"))))
	assert.Equal(t, "alice", chained)
	assert.Contains(t, runCLIExpectError(t, assumed.asSession(awsCLI("sts", "assume-role", "--role-arn", roleArn,
		"--role-session-name", "changed", "--source-identity", "bob"))), "AccessDenied")
}

// TestSTS_MemberRootPassword_CLI creates, reads and deletes a member account
// root user's password from AssumeRoot sessions with `aws iam
// create-login-profile`, `get-login-profile` and `delete-login-profile`.
func TestSTS_MemberRootPassword_CLI(t *testing.T) {
	t.Parallel()
	akid, secret, member := cliRootAccessPrincipal(t, "cli-root-password")
	root := func(task string) cliSessionCredentials {
		var session cliSessionCredentials
		parseJSON(t, runCLI(t, cliAs(akid, secret, "sts", "assume-root", "--target-principal", member,
			"--task-policy-arn", "arn=arn:aws:iam::aws:policy/root-task/"+task, "--output", "json")), &session)
		return session
	}
	audit := root("IAMAuditRootUserCredentials")
	assert.Contains(t, runCLIExpectError(t, audit.asSession(awsCLI("iam", "get-login-profile"))), "NoSuchEntity")
	user := strings.TrimSpace(runCLI(t, audit.asSession(awsCLI("iam", "get-user", "--query", "User.Arn", "--output", "text"))))
	assert.Equal(t, "arn:aws:iam::"+member+":root", user)
	assert.Equal(t, "", strings.TrimSpace(runCLI(t, audit.asSession(awsCLI("iam", "list-access-keys", "--query", "AccessKeyMetadata[].AccessKeyId", "--output", "text")))))
	runCLI(t, audit.asSession(awsCLI("iam", "list-mfa-devices")))
	runCLI(t, audit.asSession(awsCLI("iam", "list-signing-certificates")))

	runCLI(t, root("IAMCreateRootUserPassword").asSession(awsCLI("iam", "create-login-profile", "--password", "Root-Passw0rd!")))
	runCLI(t, audit.asSession(awsCLI("iam", "get-login-profile")))
	remove := root("IAMDeleteRootUserCredentials")
	runCLI(t, remove.asSession(awsCLI("iam", "delete-login-profile")))
	assert.Contains(t, runCLIExpectError(t, remove.asSession(awsCLI("iam", "get-login-profile"))), "NoSuchEntity")
}
