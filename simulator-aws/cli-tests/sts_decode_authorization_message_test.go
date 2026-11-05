package aws_cli_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var encodedAuthorizationMessage = regexp.MustCompile(`Encoded authorization failure message: (\S+)`)

// TestSTS_DecodeAuthorizationMessage_CLI refuses a user `aws ec2
// create-volume`, decodes the message the refusal carried with `aws sts
// decode-authorization-message` as the account administrator, and expects a
// message AWS never issued to be invalid.
func TestSTS_DecodeAuthorizationMessage_CLI(t *testing.T) {
	t.Parallel()
	user := uniquename.New("decode-user")
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	var key struct {
		AccessKey struct{ AccessKeyId, SecretAccessKey string }
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json")), &key)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", key.AccessKey.AccessKeyId).Run()
	})

	cmd := awsCLI("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "1")
	cmd.Env = withCreds(cmd.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
	refused := runCLIExpectError(t, cmd)
	assert.Contains(t, refused, "UnauthorizedOperation")
	match := encodedAuthorizationMessage.FindStringSubmatch(refused)
	require.Len(t, match, 2, "the refusal carries an encoded authorization message: %s", refused)

	decoded := strings.TrimSpace(runCLI(t, awsCLI("sts", "decode-authorization-message",
		"--encoded-message", match[1], "--query", "DecodedMessage", "--output", "text")))
	var doc struct {
		Allowed string `json:"allowed"`
		Context struct {
			Action    string                `json:"action"`
			Principal struct{ Name string } `json:"principal"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(decoded), &doc), decoded)
	assert.Equal(t, "false", doc.Allowed)
	assert.Equal(t, "ec2:CreateVolume", doc.Context.Action)
	assert.Equal(t, user, doc.Context.Principal.Name)

	invalid := runCLIExpectError(t, awsCLI("sts", "decode-authorization-message", "--encoded-message", "bm90LWEtbWVzc2FnZQ"))
	assert.Contains(t, invalid, "InvalidAuthorizationMessageException")
}
