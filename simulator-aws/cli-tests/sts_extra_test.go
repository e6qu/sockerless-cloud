package aws_cli_test

import (
	"encoding/base64"
	"github.com/e6qu/sockerless-cloud/testutil/samlidp"
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

func TestSTS_AssumeRoleWithSAML_CLI(t *testing.T) {
	idp, err := samlidp.New("https://idp.example.test/cli-saml")
	require.NoError(t, err)
	var provider struct {
		SAMLProviderArn string `json:"SAMLProviderArn"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-saml-provider",
		"--name", "cli-saml-idp", "--saml-metadata-document", idp.Metadata(), "--output", "json")), &provider)
	t.Cleanup(func() {
		runCLI(t, awsCLI("iam", "delete-saml-provider", "--saml-provider-arn", provider.SAMLProviderArn))
	})

	role := "cli-saml-role"
	var created struct {
		Role struct {
			Arn string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-role",
		"--role-name", role,
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"`+provider.SAMLProviderArn+`"},"Action":"sts:AssumeRoleWithSAML"}]}`,
		"--output", "json",
	)), &created)
	t.Cleanup(func() { runCLI(t, awsCLI("iam", "delete-role", "--role-name", role)) })

	assertion, err := idp.Response(samlidp.Assertion{Subject: "bob@example.test", Attributes: map[string][]string{
		samlidp.RoleAttribute:            {created.Role.Arn + "," + provider.SAMLProviderArn},
		samlidp.RoleSessionNameAttribute: {"bob"},
	}})
	require.NoError(t, err)
	out := runCLI(t, awsCLI("sts", "assume-role-with-saml",
		"--role-arn", created.Role.Arn,
		"--principal-arn", provider.SAMLProviderArn,
		"--saml-assertion", assertion,
		"--output", "json",
	))
	var res struct {
		Credentials struct {
			AccessKeyId string `json:"AccessKeyId"`
		} `json:"Credentials"`
		AssumedRoleUser struct {
			Arn string `json:"Arn"`
		} `json:"AssumedRoleUser"`
		Subject string `json:"Subject"`
		Issuer  string `json:"Issuer"`
	}
	parseJSON(t, out, &res)
	assert.Contains(t, res.Credentials.AccessKeyId, "ASIA")
	assert.Contains(t, res.AssumedRoleUser.Arn, "assumed-role/"+role+"/bob")
	assert.Equal(t, "bob@example.test", res.Subject)
	assert.Equal(t, idp.EntityID, res.Issuer)
}
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
