package aws_sdk_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/testutil/samlidp"
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

// TestSTS_AssumeRoleWithSAML federates a signed SAML response into temporary
// credentials: the provider is registered from its metadata, the role trusts
// it, and the response names the role, the provider and the session. Every
// field AWS STS reports comes from the assertion.
func TestSTS_AssumeRoleWithSAML(t *testing.T) {
	idp, err := samlidp.New("https://idp.example.test/saml")
	require.NoError(t, err)
	admin := iamClient()
	provider, err := admin.CreateSAMLProvider(ctx, &iam.CreateSAMLProviderInput{
		Name: aws.String("sts-saml-idp"), SAMLMetadataDocument: aws.String(idp.Metadata()),
	})
	require.NoError(t, err)
	providerArn := aws.ToString(provider.SAMLProviderArn)
	t.Cleanup(func() {
		_, _ = admin.DeleteSAMLProvider(ctx, &iam.DeleteSAMLProviderInput{SAMLProviderArn: aws.String(providerArn)})
	})
	role, err := admin.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String("sts-saml-role"),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
			"Principal":{"Federated":"` + providerArn + `"},"Action":"sts:AssumeRoleWithSAML",
			"Condition":{"StringEquals":{"SAML:aud":"https://signin.aws.amazon.com/saml"}}}]}`),
	})
	require.NoError(t, err)
	roleArn := aws.ToString(role.Role.Arn)
	t.Cleanup(func() { _, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String("sts-saml-role")}) })

	issue := func(t *testing.T, signer *samlidp.IdP, a samlidp.Assertion) string {
		t.Helper()
		encoded, err := signer.Response(a)
		require.NoError(t, err)
		return encoded
	}
	offering := func(roles ...string) samlidp.Assertion {
		return samlidp.Assertion{Subject: "alice@example.test", Attributes: map[string][]string{
			samlidp.RoleAttribute:            roles,
			samlidp.RoleSessionNameAttribute: {"alice"},
		}}
	}
	assume := func(assertion string) (*sts.AssumeRoleWithSAMLOutput, error) {
		return stsClient().AssumeRoleWithSAML(ctx, &sts.AssumeRoleWithSAMLInput{
			RoleArn: aws.String(roleArn), PrincipalArn: aws.String(providerArn), SAMLAssertion: aws.String(assertion),
		})
	}

	out, err := assume(issue(t, idp, offering(roleArn+","+providerArn)))
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(out.Credentials.AccessKeyId), "ASIA")
	assert.Contains(t, aws.ToString(out.AssumedRoleUser.Arn), "assumed-role/sts-saml-role/alice")
	assert.Equal(t, "alice@example.test", aws.ToString(out.Subject))
	assert.Equal(t, "persistent", aws.ToString(out.SubjectType))
	assert.Equal(t, idp.EntityID, aws.ToString(out.Issuer))
	assert.Equal(t, samlidp.AWSAudience, aws.ToString(out.Audience))
	assert.Len(t, aws.ToString(out.NameQualifier), 28)

	code := func(err error) string {
		var apiErr smithy.APIError
		require.ErrorAs(t, err, &apiErr)
		return apiErr.ErrorCode()
	}

	valid := issue(t, idp, offering(roleArn+","+providerArn))
	raw, err := base64.StdEncoding.DecodeString(valid)
	require.NoError(t, err)
	tampered := base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), "alice@example.test", "mallory@example.test", 1)))
	_, err = assume(tampered)
	assert.Equal(t, "InvalidIdentityToken", code(err), "an assertion altered after signing is refused")

	// Signature wrapping: an unsigned assertion naming someone else, placed
	// ahead of the signed one, must not be the one read.
	signedStart := strings.Index(string(raw), "<saml:Assertion")
	require.GreaterOrEqual(t, signedStart, 0)
	forged := strings.Replace(string(raw[signedStart:]), "alice@example.test", "mallory@example.test", 1)
	if cut := strings.Index(forged, "<ds:Signature"); cut >= 0 {
		end := strings.Index(forged, "</ds:Signature>") + len("</ds:Signature>")
		forged = forged[:cut] + forged[end:]
	}
	wrapped := string(raw[:signedStart]) + forged + string(raw[signedStart:])
	_, err = assume(base64.StdEncoding.EncodeToString([]byte(wrapped)))
	assert.Equal(t, "InvalidIdentityToken", code(err), "an unsigned assertion beside a signed one is refused")

	impostor, err := samlidp.New(idp.EntityID)
	require.NoError(t, err)
	_, err = assume(issue(t, impostor, offering(roleArn+","+providerArn)))
	assert.Equal(t, "InvalidIdentityToken", code(err), "a key the provider's metadata does not hold is refused")

	_, err = assume(issue(t, idp, offering("arn:aws:iam::123456789012:role/another,"+providerArn)))
	assert.Equal(t, "AccessDenied", code(err), "an assertion that does not offer the role is refused")

	expired := offering(roleArn + "," + providerArn)
	expired.NotBefore = time.Now().Add(-time.Hour)
	expired.NotOnOrAfter = time.Now().Add(-time.Minute)
	_, err = assume(issue(t, idp, expired))
	assert.Equal(t, "ExpiredTokenException", code(err), "an assertion past its validity is refused as expired")
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
