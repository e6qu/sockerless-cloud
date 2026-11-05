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
