package aws_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webIdentityIssuer is a minimal but real OpenID Connect issuer: it serves the
// discovery document and JSON Web Key Set the Security Token Service fetches to
// verify a web identity token, and signs tokens with the matching key. It
// stands in for the external identity provider the console federates through.
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

// registerWebIdentityRole registers the OpenID Connect provider and a role that
// trusts it, and returns the role ARN — the federation an administrator sets up
// so an operator's identity-provider token can be exchanged for credentials.
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

	role := "console-federation-role"
	_, err = c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"` + providerArn + `"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)}) })
	return "arn:aws:iam::000000000000:role/" + role
}

// TestSTS_AssumeRoleWithWebIdentity federates a signed OpenID Connect token into
// temporary credentials, the way the console reaches AWS: the Security Token
// Service verifies the token against the registered provider and returns
// credentials whose subject is the token's, exactly as against real AWS.
func TestSTS_AssumeRoleWithWebIdentity(t *testing.T) {
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

// TestSTS_AssumeRoleWithWebIdentity_RejectsUnregisteredIssuer proves a token
// from an issuer with no registered provider is refused.
func TestSTS_AssumeRoleWithWebIdentity_RejectsUnregisteredIssuer(t *testing.T) {
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

// TestSTS_AssumeRoleWithWebIdentity_RejectsExpiredToken proves an expired token
// is refused rather than exchanged.
func TestSTS_AssumeRoleWithWebIdentity_RejectsExpiredToken(t *testing.T) {
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
