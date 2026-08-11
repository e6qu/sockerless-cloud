package azure_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// federationTenantID is the tenant the simulator issues tokens for; azidentity
// builds the token endpoint as {authority}/{tenant}/oauth2/v2.0/token.
const federationTenantID = "11111111-1111-1111-1111-111111111111"

// federationAudience is the audience a workload-identity assertion carries when
// it is exchanged at Microsoft Entra — the standard token-exchange audience.
const federationAudience = "api://AzureADTokenExchange"

// externalIssuer is a minimal but real OpenID Connect issuer: it serves the
// discovery document and JSON Web Key Set Microsoft Entra fetches to verify a
// federated client assertion, and signs tokens with the matching key. It stands
// in for the external identity provider the console federates through.
type externalIssuer struct {
	server            *httptest.Server
	key               *rsa.PrivateKey
	keyID             string
	discoveryRequests atomic.Int64
	jwksRequests      atomic.Int64
}

func newExternalIssuer(t *testing.T) *externalIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := &externalIssuer{key: key, keyID: "federation-key"}

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

func (i *externalIssuer) mint(t *testing.T, subject, audience string, expiry time.Time) string {
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

// registerFederatedIdentity creates a user-assigned identity and a federated
// identity credential trusting the external issuer/subject/audience, and returns
// the identity's client ID — the federation an administrator sets up so an
// operator's identity-provider token can be exchanged for an Azure token.
func registerFederatedIdentity(t *testing.T, rg, issuerURL, subject string) string {
	t.Helper()
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	idClient, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	id, err := idClient.CreateOrUpdate(ctx, rg, "console-identity", armmsi.Identity{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)
	require.NotNil(t, id.Properties)
	clientID := *id.Properties.ClientID

	ficClient, err := armmsi.NewFederatedIdentityCredentialsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = ficClient.CreateOrUpdate(ctx, rg, "console-identity", "console-fic", armmsi.FederatedIdentityCredential{
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:    ptrStr(issuerURL),
			Subject:   ptrStr(subject),
			Audiences: []*string{ptrStr(federationAudience)},
		},
	}, nil)
	require.NoError(t, err)
	return clientID
}

// exchangeFederatedAssertion posts a workload-identity client assertion to the
// simulator's Microsoft Entra token endpoint exactly as the console does — a
// form POST with grant_type=client_credentials and a JWT-bearer client
// assertion — and returns the HTTP status and decoded body. azidentity cannot
// be pointed at the simulator's http authority (it requires https), and the
// console does not use azidentity: it federates with this same raw POST, so
// this exercises the real consumer's request shape.
func exchangeFederatedAssertion(t *testing.T, clientID, assertion string) (int, map[string]any) {
	t.Helper()
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {clientID},
		"scope":                 {"https://management.azure.com/.default"},
		"client_assertion_type": {federatedClientAssertionType},
		"client_assertion":      {assertion},
	}
	resp, err := http.Post(baseURL+"/"+federationTenantID+"/oauth2/v2.0/token",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// federatedClientAssertionType is the client_assertion_type Microsoft Entra
// requires for a JWT-bearer client assertion (RFC 7523).
const federatedClientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// TestFederation_ClientAssertion federates a signed OpenID Connect token into an
// Azure Resource Manager token, the way the console reaches Azure: Microsoft
// Entra verifies the client assertion against the identity's federated identity
// credential and issues an access token, exactly as against real Azure.
func TestFederation_ClientAssertion(t *testing.T) {
	issuer := newExternalIssuer(t)
	clientID := registerFederatedIdentity(t, "console-fed-rg", issuer.server.URL, "operator@example.test")
	assertion := issuer.mint(t, "operator@example.test", federationAudience, time.Now().Add(10*time.Minute))

	status, body := exchangeFederatedAssertion(t, clientID, assertion)
	require.Equal(t, http.StatusOK, status, "federation exchange must succeed: %v", body)
	assert.NotEmpty(t, body["access_token"], "federation must return an Azure Resource Manager access token")
	assert.Equal(t, "Bearer", body["token_type"])

	secondAssertion := issuer.mint(t, "operator@example.test", federationAudience, time.Now().Add(10*time.Minute))
	status, body = exchangeFederatedAssertion(t, clientID, secondAssertion)
	require.Equal(t, http.StatusOK, status, "repeated federation exchange must succeed: %v", body)
	assert.Equal(t, int64(1), issuer.discoveryRequests.Load(), "the issuer discovery document must be reused")
	assert.Equal(t, int64(1), issuer.jwksRequests.Load(), "the remote JSON Web Key Set must be reused while its keys remain current")
}

// TestFederation_RejectsUnregisteredIssuer proves an assertion from an issuer no
// federated identity credential trusts is refused.
func TestFederation_RejectsUnregisteredIssuer(t *testing.T) {
	registered := newExternalIssuer(t)
	clientID := registerFederatedIdentity(t, "console-fed-unreg-rg", registered.server.URL, "operator@example.test")

	// A different issuer that no credential trusts mints the assertion.
	stranger := newExternalIssuer(t)
	assertion := stranger.mint(t, "operator@example.test", federationAudience, time.Now().Add(10*time.Minute))

	status, body := exchangeFederatedAssertion(t, clientID, assertion)
	assert.GreaterOrEqual(t, status, http.StatusBadRequest, "an assertion from an untrusted issuer must be rejected: %v", body)
}

// TestFederation_RejectsExpiredAssertion proves an expired assertion is refused
// rather than exchanged.
func TestFederation_RejectsExpiredAssertion(t *testing.T) {
	issuer := newExternalIssuer(t)
	clientID := registerFederatedIdentity(t, "console-fed-exp-rg", issuer.server.URL, "operator@example.test")
	expired := issuer.mint(t, "operator@example.test", federationAudience, time.Now().Add(-time.Minute))

	status, body := exchangeFederatedAssertion(t, clientID, expired)
	assert.GreaterOrEqual(t, status, http.StatusBadRequest, "an expired assertion must be rejected: %v", body)
}
