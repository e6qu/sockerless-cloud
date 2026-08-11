package azure_sdk_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// TestAzureEntra_V2UserInfoEndpoint verifies v2.0 discovery advertises a
// working userinfo_endpoint so coreos/go-oidc's provider.UserInfo() — called by
// Pomerium after the token exchange — resolves. Verified with the real go-oidc
// client. UserInfo requires a valid bearer token (OIDC Core §5.3); there is no
// fallback identity.
func TestAzureEntra_V2UserInfoEndpoint(t *testing.T) {
	tenant := "edd-e2e-tenant"
	clientID := "pomerium-userinfo-client"
	v2Issuer := baseURL + "/" + tenant + "/v2.0"

	// Discovery advertises a sim-hosted userinfo_endpoint.
	doc := fetchAzureDiscovery(t, v2Issuer+"/.well-known/openid-configuration")
	assert.Equal(t, v2Issuer+"/userinfo", doc["userinfo_endpoint"], "v2.0 discovery must advertise userinfo_endpoint")

	provider, err := oidc.NewProvider(ctx, v2Issuer)
	require.NoError(t, err)

	tok := azureAuthCodeFlow(t, tenant, clientID)
	require.NotEmpty(t, tok.AccessToken)
	require.NotEmpty(t, tok.IDToken)
	idSub, _ := azureTokenPayload(t, tok.IDToken)["sub"].(string)
	require.NotEmpty(t, idSub)

	// provider.UserInfo — the exact call Pomerium makes (previously failed with
	// "user info endpoint is not supported by this provider").
	ui, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok.AccessToken}))
	require.NoError(t, err, "provider.UserInfo() must resolve against the v2.0 userinfo endpoint")
	assert.Equal(t, idSub, ui.Subject, "userinfo sub must match the id_token sub")
	var claims map[string]any
	require.NoError(t, ui.Claims(&claims))
	assert.NotEmpty(t, claims["name"], "userinfo must include the name claim")

	// No bearer token -> 401 (no fallback to a default identity).
	resp, err := http.Get(baseURL + "/" + tenant + "/v2.0/userinfo")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "UserInfo without a bearer token must be 401")

	// A garbage token -> 401 (signature verification, no fallback).
	badReq, _ := http.NewRequest(http.MethodGet, baseURL+"/"+tenant+"/v2.0/userinfo", nil)
	badReq.Header.Set("Authorization", "Bearer not.a.realjwt")
	badResp, err := http.DefaultClient.Do(badReq)
	require.NoError(t, err)
	defer badResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, badResp.StatusCode, "an unverifiable token must be 401")
}

// TestAzureEntra_V2DiscoveryIssuerMatchesFetchURL verifies the v2.0 OIDC
// discovery document advertises an issuer equal to the URL it was fetched from
// (RFC 8414 §3) so strict OIDC clients (coreos/go-oidc, Pomerium) can
// initialise. The v1 discovery keeps the real AAD sts.windows.net issuer that
// the Azure SDK expects.
func TestAzureEntra_V2DiscoveryIssuerMatchesFetchURL(t *testing.T) {
	tenant := "edd-e2e-tenant"

	// v2.0 discovery: issuer == the v2.0 base URL it was fetched from.
	v2Issuer := baseURL + "/" + tenant + "/v2.0"
	v2 := fetchAzureDiscovery(t, v2Issuer+"/.well-known/openid-configuration")
	assert.Equal(t, v2Issuer, v2["issuer"], "v2.0 discovery issuer must equal the fetch base URL (RFC 8414 §3)")

	// v1 discovery: keeps the real AAD issuer (azidentity compares against it).
	v1 := fetchAzureDiscovery(t, baseURL+"/"+tenant+"/.well-known/openid-configuration")
	assert.Equal(t, "https://sts.windows.net/"+tenant+"/", v1["issuer"], "v1 discovery issuer must stay sts.windows.net")

	// coreos/go-oidc — the exact client that failed for Pomerium. NewProvider
	// fetches the discovery and ENFORCES issuer-URL identity; this previously
	// failed with "issuer did not match" because the doc returned sts.windows.net.
	provider, err := oidc.NewProvider(ctx, v2Issuer)
	require.NoError(t, err, "coreos/go-oidc must initialise against the v2.0 issuer")

	// Drive the auth-code flow and verify the id_token through the provider —
	// this validates iss (against the discovery issuer), signature (via JWKS),
	// and aud, exactly as Pomerium does.
	clientID := "pomerium-client"
	idToken := azureAuthCodeIDToken(t, tenant, clientID)
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})
	verified, err := verifier.Verify(ctx, idToken)
	require.NoError(t, err, "the id_token must verify against the v2.0 provider (iss + signature + aud)")
	assert.Equal(t, v2Issuer, verified.Issuer, "id_token iss must equal the v2.0 issuer")

	// Regression guard: the ACCESS token keeps the v1 sts.windows.net issuer
	// (real ARM access tokens do, even from the v2.0 endpoint).
	access := azureAuthCodeAccessToken(t, tenant, clientID)
	assert.Equal(t, "https://sts.windows.net/"+tenant+"/", azureTokenPayload(t, access)["iss"])
}

func fetchAzureDiscovery(t *testing.T, discoveryURL string) map[string]any {
	t.Helper()
	resp, err := http.Get(discoveryURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var doc map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	return doc
}

// azureAuthCodeIDToken / azureAuthCodeAccessToken run the PKCE auth-code flow and
// return the id_token / access_token.
func azureAuthCodeFlow(t *testing.T, tenant, clientID string) azureTokenResponse {
	t.Helper()
	redirectURI := "http://127.0.0.1/callback"
	verifier := "ThisIsntRandomButItNeedsToBe43CharactersLong"
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])

	authURL := baseURL + "/" + tenant + "/oauth2/v2.0/authorize?" + url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid profile email"},
		"state":                 {"s"},
		"nonce":                 {"n"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	client := http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	authResp, err := client.Get(authURL)
	require.NoError(t, err)
	defer authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)
	cb, err := url.Parse(authResp.Header.Get("Location"))
	require.NoError(t, err)
	code := cb.Query().Get("code")
	require.NotEmpty(t, code)

	return requestAzureToken(t, tenant, "oauth2/v2.0/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
}

func azureAuthCodeIDToken(t *testing.T, tenant, clientID string) string {
	tok := azureAuthCodeFlow(t, tenant, clientID)
	require.NotEmpty(t, tok.IDToken)
	return tok.IDToken
}

func azureAuthCodeAccessToken(t *testing.T, tenant, clientID string) string {
	return azureAuthCodeFlow(t, tenant, clientID).AccessToken
}
