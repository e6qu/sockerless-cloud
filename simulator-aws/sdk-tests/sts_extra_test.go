package aws_sdk_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSTS_GetFederationToken mints credentials for a named federated user and
// asserts the FederatedUser identity is returned alongside them.
func TestSTS_GetFederationToken(t *testing.T) {
	client := stsClient()
	out, err := client.GetFederationToken(ctx, &sts.GetFederationTokenInput{
		Name: aws.String("Bob"),
	})
	require.NoError(t, err)
	require.NotNil(t, out.Credentials)
	assert.Contains(t, aws.ToString(out.Credentials.AccessKeyId), "ASIA")
	assert.NotEmpty(t, aws.ToString(out.Credentials.SecretAccessKey))
	assert.NotEmpty(t, aws.ToString(out.Credentials.SessionToken))
	require.NotNil(t, out.FederatedUser)
	assert.Contains(t, aws.ToString(out.FederatedUser.Arn), "federated-user/Bob")
	assert.Contains(t, aws.ToString(out.FederatedUser.FederatedUserId), ":Bob")
}

// TestSTS_AssumeRoleWithSAML assumes a role from a SAML assertion and asserts
// the temporary credentials + AssumedRoleUser come back.
func TestSTS_AssumeRoleWithSAML(t *testing.T) {
	admin := iamClient()
	role := "sts-saml-role"
	_, err := admin.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::000000000000:saml-provider/sim"},"Action":"sts:AssumeRoleWithSAML"}]}`),
	})
	require.NoError(t, err)
	defer admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)})

	client := stsClient()
	out, err := client.AssumeRoleWithSAML(ctx, &sts.AssumeRoleWithSAMLInput{
		RoleArn:       aws.String("arn:aws:iam::000000000000:role/" + role),
		PrincipalArn:  aws.String("arn:aws:iam::000000000000:saml-provider/sim"),
		SAMLAssertion: aws.String(base64.StdEncoding.EncodeToString([]byte("<saml-assertion/>"))),
	})
	require.NoError(t, err)
	require.NotNil(t, out.Credentials)
	assert.Contains(t, aws.ToString(out.Credentials.AccessKeyId), "ASIA")
	require.NotNil(t, out.AssumedRoleUser)
	assert.Contains(t, aws.ToString(out.AssumedRoleUser.Arn), "assumed-role/"+role+"/")
}

// TestSTS_GetWebIdentityToken issues a signed web-identity JWT + its expiration.
func TestSTS_GetWebIdentityToken(t *testing.T) {
	client := stsClient()
	out, err := client.GetWebIdentityToken(ctx, &sts.GetWebIdentityTokenInput{
		Audience:         []string{"https://example.com"},
		SigningAlgorithm: aws.String("RS256"),
	})
	require.NoError(t, err)
	tok := aws.ToString(out.WebIdentityToken)
	assert.NotEmpty(t, tok)
	// A structurally valid JWT has three dot-separated segments.
	assert.Equal(t, 3, strings.Count(tok, ".")+1)
	require.NotNil(t, out.Expiration)
}

// TestSTS_GetDelegatedAccessToken trades a token in for temporary credentials
// and asserts the assumed principal is returned.
func TestSTS_GetDelegatedAccessToken(t *testing.T) {
	client := stsClient()
	out, err := client.GetDelegatedAccessToken(ctx, &sts.GetDelegatedAccessTokenInput{
		TradeInToken: aws.String("sim-trade-in-token"),
	})
	require.NoError(t, err)
	require.NotNil(t, out.Credentials)
	assert.Contains(t, aws.ToString(out.Credentials.AccessKeyId), "ASIA")
	assert.NotEmpty(t, aws.ToString(out.AssumedPrincipal))
}

// TestSTS_AssumeRoot mints credentials for a member account's root user.
func TestSTS_AssumeRoot(t *testing.T) {
	client := stsClient()
	out, err := client.AssumeRoot(ctx, &sts.AssumeRootInput{
		TargetPrincipal: aws.String("123456789012"),
		TaskPolicyArn:   &ststypes.PolicyDescriptorType{Arn: aws.String("arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials")},
	})
	require.NoError(t, err)
	require.NotNil(t, out.Credentials)
	assert.Contains(t, aws.ToString(out.Credentials.AccessKeyId), "ASIA")
	assert.Equal(t, "123456789012", aws.ToString(out.SourceIdentity))
}

// TestSTS_DecodeAuthorizationMessage decodes a base64 JSON authorization message
// and asserts the decoded message round-trips.
func TestSTS_DecodeAuthorizationMessage(t *testing.T) {
	client := stsClient()
	original := `{"allowed":false,"explicitDeny":true}`
	out, err := client.DecodeAuthorizationMessage(ctx, &sts.DecodeAuthorizationMessageInput{
		EncodedMessage: aws.String(base64.StdEncoding.EncodeToString([]byte(original))),
	})
	require.NoError(t, err)
	assert.Equal(t, original, aws.ToString(out.DecodedMessage))
}

// TestSTS_GetAccessKeyInfo resolves an access key id to its owning account.
func TestSTS_GetAccessKeyInfo(t *testing.T) {
	client := stsClient()
	out, err := client.GetAccessKeyInfo(ctx, &sts.GetAccessKeyInfoInput{
		AccessKeyId: aws.String("AKIAIOSFODNN7EXAMPLE"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(out.Account))
}
