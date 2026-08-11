package aws_cli_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSTS_GetFederationToken_CLI mints credentials for a federated user.
func TestSTS_GetFederationToken_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("sts", "get-federation-token",
		"--name", "Bob",
		"--output", "json",
	))
	var res struct {
		Credentials struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
			SessionToken    string `json:"SessionToken"`
		} `json:"Credentials"`
		FederatedUser struct {
			Arn             string `json:"Arn"`
			FederatedUserId string `json:"FederatedUserId"`
		} `json:"FederatedUser"`
	}
	parseJSON(t, out, &res)
	assert.Contains(t, res.Credentials.AccessKeyId, "ASIA")
	assert.NotEmpty(t, res.Credentials.SecretAccessKey)
	assert.Contains(t, res.FederatedUser.Arn, "federated-user/Bob")
}

// TestSTS_AssumeRoleWithSAML_CLI assumes a role from a SAML assertion.
func TestSTS_AssumeRoleWithSAML_CLI(t *testing.T) {
	role := "cli-saml-role"
	runCLI(t, awsCLI("iam", "create-role",
		"--role-name", role,
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::000000000000:saml-provider/sim"},"Action":"sts:AssumeRoleWithSAML"}]}`,
	))
	t.Cleanup(func() { runCLI(t, awsCLI("iam", "delete-role", "--role-name", role)) })

	saml := base64.StdEncoding.EncodeToString([]byte("<saml-assertion/>"))
	out := runCLI(t, awsCLI("sts", "assume-role-with-saml",
		"--role-arn", "arn:aws:iam::000000000000:role/"+role,
		"--principal-arn", "arn:aws:iam::000000000000:saml-provider/sim",
		"--saml-assertion", saml,
		"--output", "json",
	))
	var res struct {
		Credentials struct {
			AccessKeyId string `json:"AccessKeyId"`
		} `json:"Credentials"`
		AssumedRoleUser struct {
			Arn string `json:"Arn"`
		} `json:"AssumedRoleUser"`
	}
	parseJSON(t, out, &res)
	assert.Contains(t, res.Credentials.AccessKeyId, "ASIA")
	assert.Contains(t, res.AssumedRoleUser.Arn, "assumed-role/"+role+"/")
}

// TestSTS_AssumeRoot_CLI mints credentials for a member account's root user.
func TestSTS_AssumeRoot_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("sts", "assume-root",
		"--target-principal", "123456789012",
		"--task-policy-arn", "arn=arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials",
		"--output", "json",
	))
	var res struct {
		Credentials struct {
			AccessKeyId string `json:"AccessKeyId"`
		} `json:"Credentials"`
		SourceIdentity string `json:"SourceIdentity"`
	}
	parseJSON(t, out, &res)
	assert.Contains(t, res.Credentials.AccessKeyId, "ASIA")
	assert.Equal(t, "123456789012", res.SourceIdentity)
}

// TestSTS_DecodeAuthorizationMessage_CLI round-trips a base64 JSON message.
func TestSTS_DecodeAuthorizationMessage_CLI(t *testing.T) {
	original := `{"allowed":false}`
	out := runCLI(t, awsCLI("sts", "decode-authorization-message",
		"--encoded-message", base64.StdEncoding.EncodeToString([]byte(original)),
		"--output", "json",
	))
	var res struct {
		DecodedMessage string `json:"DecodedMessage"`
	}
	parseJSON(t, out, &res)
	assert.Equal(t, original, res.DecodedMessage)
}

// TestSTS_GetAccessKeyInfo_CLI resolves an access key id to its account.
func TestSTS_GetAccessKeyInfo_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("sts", "get-access-key-info",
		"--access-key-id", "AKIAIOSFODNN7EXAMPLE",
		"--output", "json",
	))
	var res struct {
		Account string `json:"Account"`
	}
	parseJSON(t, out, &res)
	require.NotEmpty(t, res.Account)
}
