package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func awsCLIAsRoot(args ...string) *exec.Cmd {
	cmd := awsCLI(args...)
	cmd.Env = withCreds(cmd.Env, "root", "root")
	return cmd
}

// TestIAM_RootUserSignsAsTheAccount_CLI signs the AWS CLI with the management
// account's root user key: `aws sts get-caller-identity` answers the account's
// root ARN, a root `get-session-token` lasts at most an hour, a root
// `assume-root` is refused, and `aws iam list-access-keys` lists the root
// user's own key.
func TestIAM_RootUserSignsAsTheAccount_CLI(t *testing.T) {
	t.Parallel()
	account := strings.TrimSpace(runCLI(t, awsCLI("sts", "get-caller-identity", "--query", "Account", "--output", "text")))

	arn := strings.TrimSpace(runCLI(t, awsCLIAsRoot("sts", "get-caller-identity", "--query", "Arn", "--output", "text")))
	assert.Equal(t, "arn:aws:iam::"+account+":root", arn)

	requested := time.Now()
	expiration := strings.TrimSpace(runCLI(t, awsCLIAsRoot("sts", "get-session-token", "--duration-seconds", "7200",
		"--query", "Credentials.Expiration", "--output", "text")))
	expires, err := time.Parse(time.RFC3339, expiration)
	require.NoError(t, err)
	assert.False(t, expires.After(requested.Add(time.Hour+time.Minute)), "a root user's session asked for two hours lasts one")

	refused := runCLIExpectError(t, awsCLIAsRoot("sts", "assume-root", "--target-principal", account,
		"--task-policy-arn", "arn=arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials"))
	assert.Contains(t, refused, "AccessDenied", "a root user cannot call AssumeRoot")

	keys := runCLI(t, awsCLIAsRoot("iam", "list-access-keys", "--query", "AccessKeyMetadata[].AccessKeyId", "--output", "text"))
	assert.Contains(t, strings.Fields(keys), "root")
}
