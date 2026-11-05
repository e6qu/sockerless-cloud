package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrganizations_Handshakes_CLI invites a standalone account with `aws
// organizations invite-account-to-organization`; only the invited account
// accepts, which joins it, and the management account cancels another
// invitation the account then cannot answer.
func TestOrganizations_Handshakes_CLI(t *testing.T) {
	t.Parallel()
	member := cliMemberAccount(t, "cli-invitee")
	session := cliMemberAdmin(t, member, "OrganizationAccountAccessRole")
	runCLI(t, session.asSession(awsCLI("organizations", "leave-organization")))

	hid := strings.TrimSpace(runCLI(t, awsCLI("organizations", "invite-account-to-organization",
		"--target", "Id="+member+",Type=ACCOUNT", "--notes", "join us", "--query", "Handshake.Id", "--output", "text")))
	require.True(t, strings.HasPrefix(hid, "h-"), hid)
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "invite-account-to-organization",
		"--target", "Id="+member+",Type=ACCOUNT")), "DuplicateHandshakeException")

	received := runCLI(t, session.asSession(awsCLI("organizations", "list-handshakes-for-account",
		"--query", "Handshakes[].Id", "--output", "text")))
	assert.Equal(t, []string{hid}, strings.Fields(received))
	assert.NotContains(t, runCLI(t, awsCLI("organizations", "list-handshakes-for-account", "--output", "json")), hid,
		"the management account sent the invitation")
	assert.Contains(t, runCLI(t, awsCLI("organizations", "list-handshakes-for-organization",
		"--filter", "ActionType=INVITE", "--output", "json")), hid)
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "accept-handshake", "--handshake-id", hid)), cliOrgAccessDenied)
	assert.Contains(t, runCLIExpectError(t, session.asSession(awsCLI("organizations", "cancel-handshake", "--handshake-id", hid))), cliOrgAccessDenied)

	state := runCLI(t, session.asSession(awsCLI("organizations", "accept-handshake", "--handshake-id", hid,
		"--query", "Handshake.State", "--output", "text")))
	assert.Equal(t, "ACCEPTED", strings.TrimSpace(state))
	joined := runCLI(t, awsCLI("organizations", "describe-account", "--account-id", member, "--query", "Account.JoinedMethod", "--output", "text"))
	assert.Equal(t, "INVITED", strings.TrimSpace(joined))
	assert.Contains(t, runCLIExpectError(t, session.asSession(awsCLI("organizations", "decline-handshake", "--handshake-id", hid))),
		"InvalidHandshakeTransitionException")
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "invite-account-to-organization",
		"--target", "Id="+member+",Type=ACCOUNT")), "ALREADY_IN_AN_ORGANIZATION")

	runCLI(t, session.asSession(awsCLI("organizations", "leave-organization")))
	second := strings.TrimSpace(runCLI(t, awsCLI("organizations", "invite-account-to-organization",
		"--target", "Id=cli-invitee@sim.invalid,Type=EMAIL", "--query", "Handshake.Id", "--output", "text")))
	state = runCLI(t, awsCLI("organizations", "cancel-handshake", "--handshake-id", second, "--query", "Handshake.State", "--output", "text"))
	assert.Equal(t, "CANCELED", strings.TrimSpace(state))
	assert.Contains(t, runCLIExpectError(t, session.asSession(awsCLI("organizations", "accept-handshake", "--handshake-id", second))),
		"InvalidHandshakeTransitionException")

	third := strings.TrimSpace(runCLI(t, awsCLI("organizations", "invite-account-to-organization",
		"--target", "Id="+member+",Type=ACCOUNT", "--query", "Handshake.Id", "--output", "text")))
	state = runCLI(t, session.asSession(awsCLI("organizations", "decline-handshake", "--handshake-id", third,
		"--query", "Handshake.State", "--output", "text")))
	assert.Equal(t, "DECLINED", strings.TrimSpace(state))
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "describe-account", "--account-id", member)), "AccountNotFoundException")
}

// cliMigrateToAllFeatures takes the account's organization, which has
// consolidated billing alone, through the User Guide's standard migration
// with `aws organizations enable-all-features` and `accept-handshake`.
func cliMigrateToAllFeatures(t *testing.T) {
	t.Helper()
	text := func(cmd ...string) string {
		return strings.TrimSpace(runCLI(t, awsCLI(append(cmd, "--output", "text")...)))
	}
	invited := cliMemberAccount(t, "cli-approver")
	session := cliMemberAdmin(t, invited, "OrganizationAccountAccessRole")
	runCLI(t, session.asSession(awsCLI("organizations", "leave-organization")))
	invitation := text("organizations", "invite-account-to-organization", "--target", "Id="+invited+",Type=ACCOUNT", "--query", "Handshake.Id")
	runCLI(t, session.asSession(awsCLI("organizations", "accept-handshake", "--handshake-id", invitation)))

	root := text("organizations", "list-roots", "--query", "Roots[0].Id")
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "enable-policy-type", "--root-id", root,
		"--policy-type", "SERVICE_CONTROL_POLICY")), "PolicyTypeNotAvailableForOrganizationException")

	parent := text("organizations", "enable-all-features", "--query", "Handshake.Id")
	assert.Equal(t, "REQUESTED", text("organizations", "describe-handshake", "--handshake-id", parent, "--query", "Handshake.State"))
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "enable-all-features")), "ORGANIZATION_IS_ALREADY_PENDING_ALL_FEATURES_MIGRATION")
	assert.Contains(t, runCLIExpectError(t, awsCLI("organizations", "accept-handshake", "--handshake-id", parent)), "InvalidHandshakeTransitionException")

	approval := strings.TrimSpace(runCLI(t, session.asSession(awsCLI("organizations", "list-handshakes-for-account",
		"--filter", "ActionType=APPROVE_ALL_FEATURES", "--query", "Handshakes[].Id", "--output", "text"))))
	require.True(t, strings.HasPrefix(approval, "h-"), approval)
	assert.Equal(t, approval, text("organizations", "list-handshakes-for-organization",
		"--filter", "ParentHandshakeId="+parent, "--query", "Handshakes[].Id"))
	runCLI(t, session.asSession(awsCLI("organizations", "accept-handshake", "--handshake-id", approval)))
	assert.Equal(t, "OPEN", text("organizations", "describe-handshake", "--handshake-id", parent, "--query", "Handshake.State"))

	assert.Equal(t, "ACCEPTED", text("organizations", "accept-handshake", "--handshake-id", parent, "--query", "Handshake.State"))
	assert.Equal(t, "ALL", text("organizations", "describe-organization", "--query", "Organization.FeatureSet"))
	// The default organization the other tests read enables SCPs.
	runCLI(t, awsCLI("organizations", "enable-policy-type", "--root-id", root, "--policy-type", "SERVICE_CONTROL_POLICY"))
}

// TestOrganizations_HandshakeListingsPage_CLI lists the organization's
// invitations one handshake per request: the CLI follows each NextToken.
func TestOrganizations_HandshakeListingsPage_CLI(t *testing.T) {
	t.Parallel()
	sent := []string{}
	for range 3 {
		member := cliMemberAccount(t, "cli-paged")
		runCLI(t, cliMemberAdmin(t, member, "OrganizationAccountAccessRole").asSession(awsCLI("organizations", "leave-organization")))
		sent = append(sent, strings.TrimSpace(runCLI(t, awsCLI("organizations", "invite-account-to-organization",
			"--target", "Id="+member+",Type=ACCOUNT", "--query", "Handshake.Id", "--output", "text"))))
	}
	listed := strings.Fields(runCLI(t, awsCLI("organizations", "list-handshakes-for-organization", "--filter", "ActionType=INVITE",
		"--page-size", "1", "--query", "Handshakes[].Id", "--output", "text")))
	assert.Subset(t, listed, sent)
}
