package aws_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webIdentityIssuer is an OpenID Connect issuer: it serves the discovery
// document and JSON Web Key Set AWS STS fetches to verify a token, and signs
// tokens with the matching key, standing in for the identity provider the
// console federates through.
type webIdentityIssuer struct {
	server            *httptest.Server
	key               *rsa.PrivateKey
	keyID             string
	discoveryRequests atomic.Int32
	jwksRequests      atomic.Int32
}

func newWebIdentityIssuer(t *testing.T) *webIdentityIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := &webIdentityIssuer{key: key, keyID: "web-identity-key"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		issuer.discoveryRequests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer.server.URL,
			"jwks_uri":                              issuer.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		issuer.jwksRequests.Add(1)
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     issuer.keyID,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}})
	})
	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (i *webIdentityIssuer) mint(t *testing.T, subject, audience string, expiry time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.keyID),
	)
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   i.server.URL,
		Subject:  subject,
		Audience: jwt.Audience{audience},
		IssuedAt: jwt.NewNumericDate(time.Now()),
		Expiry:   jwt.NewNumericDate(expiry),
	}).Serialize()
	require.NoError(t, err)
	return raw
}

// registerWebIdentityRole registers the OpenID Connect provider and a role
// trusting it, as an administrator sets up console federation, and returns
// the role ARN.
func registerWebIdentityRole(t *testing.T, issuerURL, audience string) string {
	t.Helper()
	c := iamClient()
	provider, err := c.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
		Url:            aws.String(issuerURL),
		ClientIDList:   []string{audience},
		ThumbprintList: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	require.NoError(t, err)
	providerArn := aws.ToString(provider.OpenIDConnectProviderArn)
	t.Cleanup(func() {
		_, _ = c.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(providerArn)})
	})

	// Tests federate concurrently, each through its own role.
	role := uniquename.New("console-federation-role")
	_, err = c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"` + providerArn + `"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)}) })
	return "arn:aws:iam::000000000000:role/" + role
}

// TestSTS_AssumeRoleWithWebIdentity expects AWS STS to verify a signed OpenID
// Connect token against the registered provider and return credentials whose
// subject is the token's, as the console reaches AWS.
func TestSTS_AssumeRoleWithWebIdentity(t *testing.T) {
	t.Parallel()
	issuer := newWebIdentityIssuer(t)
	const audience = "sockerless-console"
	roleArn := registerWebIdentityRole(t, issuer.server.URL, audience)
	token := issuer.mint(t, "operator@example.test", audience, time.Now().Add(10*time.Minute))

	out, err := stsClient().AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          aws.String(roleArn),
		RoleSessionName:  aws.String("console"),
		WebIdentityToken: aws.String(token),
	})
	require.NoError(t, err)
	require.NotNil(t, out.Credentials)
	assert.Contains(t, aws.ToString(out.Credentials.AccessKeyId), "ASIA")
	assert.Equal(t, "operator@example.test", aws.ToString(out.SubjectFromWebIdentityToken),
		"the subject must come from the verified token, not a fixed placeholder")

	secondToken := issuer.mint(t, "second-operator@example.test", audience, time.Now().Add(10*time.Minute))
	second, err := stsClient().AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          aws.String(roleArn),
		RoleSessionName:  aws.String("second-console"),
		WebIdentityToken: aws.String(secondToken),
	})
	require.NoError(t, err)
	assert.Equal(t, "second-operator@example.test", aws.ToString(second.SubjectFromWebIdentityToken),
		"every assertion must still be verified independently")
	assert.Equal(t, int32(1), issuer.discoveryRequests.Load(),
		"repeated exchanges for one issuer must reuse OpenID Connect discovery")
	assert.Equal(t, int32(1), issuer.jwksRequests.Load(),
		"repeated exchanges for one issuer and signing key must reuse its JSON Web Key Set")
}

// TestSTS_AssumeRoleWithWebIdentity_RejectsUnregisteredIssuer expects a token
// from an issuer without a registered provider to be refused.
func TestSTS_AssumeRoleWithWebIdentity_RejectsUnregisteredIssuer(t *testing.T) {
	t.Parallel()
	issuer := newWebIdentityIssuer(t)
	const audience = "sockerless-console"
	// A role that trusts nothing in particular; the issuer has no provider.
	c := iamClient()
	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("unregistered-role"),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"x"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String("unregistered-role")}) })
	token := issuer.mint(t, "operator@example.test", audience, time.Now().Add(10*time.Minute))

	_, err = stsClient().AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          aws.String("arn:aws:iam::000000000000:role/unregistered-role"),
		RoleSessionName:  aws.String("console"),
		WebIdentityToken: aws.String(token),
	})
	var invalid *ststypes.InvalidIdentityTokenException
	assert.ErrorAs(t, err, &invalid, "an unregistered issuer must be rejected as an invalid identity token")
}

// TestSTS_AssumeRoleWithWebIdentity_RejectsExpiredToken expects an expired
// token to be refused.
func TestSTS_AssumeRoleWithWebIdentity_RejectsExpiredToken(t *testing.T) {
	t.Parallel()
	issuer := newWebIdentityIssuer(t)
	const audience = "sockerless-console"
	roleArn := registerWebIdentityRole(t, issuer.server.URL, audience)
	expired := issuer.mint(t, "operator@example.test", audience, time.Now().Add(-time.Minute))

	_, err := stsClient().AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          aws.String(roleArn),
		RoleSessionName:  aws.String("console"),
		WebIdentityToken: aws.String(expired),
	})
	var invalid *ststypes.InvalidIdentityTokenException
	assert.ErrorAs(t, err, &invalid, "an expired token must be rejected")
}

// TestSTS_AssumeRoleWithWebIdentity_EnforcesTheTrustPolicy expects the role's
// condition on the token's subject to admit one operator and refuse another,
// and a role trusting another provider to refuse this provider's token.
func TestSTS_AssumeRoleWithWebIdentity_EnforcesTheTrustPolicy(t *testing.T) {
	t.Parallel()
	issuer := newWebIdentityIssuer(t)
	const audience = "sockerless-console"
	c := iamClient()
	provider, err := c.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
		Url:            aws.String(issuer.server.URL),
		ClientIDList:   []string{audience},
		ThumbprintList: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	require.NoError(t, err)
	providerArn := aws.ToString(provider.OpenIDConnectProviderArn)
	t.Cleanup(func() {
		_, _ = c.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(providerArn)})
	})
	providerName := strings.TrimPrefix(strings.TrimPrefix(issuer.server.URL, "https://"), "http://")

	createRole := func(name, policy string) string {
		out, err := c.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(policy)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}) })
		return aws.ToString(out.Role.Arn)
	}
	scoped := createRole("web-identity-one-operator", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"`+providerArn+`"},"Action":"sts:AssumeRoleWithWebIdentity",
		"Condition":{"StringEquals":{"`+providerName+`:sub":"operator@example.test","`+providerName+`:aud":"`+audience+`"}}}]}`)
	otherProvider := createRole("web-identity-other-provider", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"arn:aws:iam::123456789012:oidc-provider/elsewhere.example.test"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`)

	assume := func(roleArn, subject string) error {
		_, err := stsClient().AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
			RoleArn:          aws.String(roleArn),
			RoleSessionName:  aws.String("console"),
			WebIdentityToken: aws.String(issuer.mint(t, subject, audience, time.Now().Add(10*time.Minute))),
		})
		return err
	}
	require.NoError(t, assume(scoped, "operator@example.test"), "the subject the trust policy names may federate")

	var denied smithy.APIError
	err = assume(scoped, "intruder@example.test")
	require.ErrorAs(t, err, &denied, "another subject is refused by the trust policy's condition")
	assert.Equal(t, "AccessDenied", denied.ErrorCode())

	err = assume(otherProvider, "operator@example.test")
	require.ErrorAs(t, err, &denied, "a role that trusts another provider refuses this one")
	assert.Equal(t, "AccessDenied", denied.ErrorCode())
}
