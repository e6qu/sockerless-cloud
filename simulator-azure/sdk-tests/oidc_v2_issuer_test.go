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

// TestAzureEntra_V2UserInfoEndpoint: v2.0 discovery advertises a working
// userinfo_endpoint, which coreos/go-oidc's provider.UserInfo() calls after the
// token exchange. UserInfo requires a valid bearer token (OIDC Core §5.3).
func TestAzureEntra_V2UserInfoEndpoint(t *testing.T) {
	t.Parallel()
	tenant := "edd-e2e-tenant"
	clientID := "pomerium-userinfo-client"
	v2Issuer := baseURL + "/" + tenant + "/v2.0"

	doc := fetchAzureDiscovery(t, v2Issuer+"/.well-known/openid-configuration")
	assert.Equal(t, v2Issuer+"/userinfo", doc["userinfo_endpoint"], "v2.0 discovery must advertise userinfo_endpoint")

	provider, err := oidc.NewProvider(ctx, v2Issuer)
	require.NoError(t, err)

	tok := azureAuthCodeFlow(t, tenant, clientID)
	require.NotEmpty(t, tok.AccessToken)
	require.NotEmpty(t, tok.IDToken)
	idSub, _ := azureTokenPayload(t, tok.IDToken)["sub"].(string)
	require.NotEmpty(t, idSub)

	ui, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok.AccessToken}))
	require.NoError(t, err, "provider.UserInfo() must resolve against the v2.0 userinfo endpoint")
	assert.Equal(t, idSub, ui.Subject, "userinfo sub must match the id_token sub")
	var claims map[string]any
	require.NoError(t, ui.Claims(&claims))
	assert.NotEmpty(t, claims["name"], "userinfo must include the name claim")

	// No bearer token answers 401.
	resp, err := http.Get(baseURL + "/" + tenant + "/v2.0/userinfo")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "UserInfo without a bearer token must be 401")

	// A token that fails signature verification answers 401.
	badReq, _ := http.NewRequest(http.MethodGet, baseURL+"/"+tenant+"/v2.0/userinfo", nil)
	badReq.Header.Set("Authorization", "Bearer not.a.realjwt")
	badResp, err := http.DefaultClient.Do(badReq)
	require.NoError(t, err)
	defer badResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, badResp.StatusCode, "an unverifiable token must be 401")
}

// TestAzureEntra_V2DiscoveryIssuerMatchesFetchURL: v2.0 discovery advertises
// the URL it was fetched from as its issuer (RFC 8414 §3), which strict OIDC
// clients such as coreos/go-oidc enforce. v1 discovery keeps the
// sts.windows.net issuer the Azure SDK expects.
func TestAzureEntra_V2DiscoveryIssuerMatchesFetchURL(t *testing.T) {
	t.Parallel()
	tenant := "edd-e2e-tenant"

	v2Issuer := baseURL + "/" + tenant + "/v2.0"
	v2 := fetchAzureDiscovery(t, v2Issuer+"/.well-known/openid-configuration")
	assert.Equal(t, v2Issuer, v2["issuer"], "v2.0 discovery issuer must equal the fetch base URL (RFC 8414 §3)")

	// azidentity compares the v1 issuer against sts.windows.net.
	v1 := fetchAzureDiscovery(t, baseURL+"/"+tenant+"/.well-known/openid-configuration")
	assert.Equal(t, "https://sts.windows.net/"+tenant+"/", v1["issuer"], "v1 discovery issuer must stay sts.windows.net")

	// NewProvider enforces that the discovery issuer matches the URL.
	provider, err := oidc.NewProvider(ctx, v2Issuer)
	require.NoError(t, err, "coreos/go-oidc must initialise against the v2.0 issuer")

	// The auth-code flow's id_token verifies through the provider: iss against
	// the discovery issuer, the signature via JWKS, and aud.
	clientID := "pomerium-client"
	idToken := azureAuthCodeIDToken(t, tenant, clientID)
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})
	verified, err := verifier.Verify(ctx, idToken)
	require.NoError(t, err, "the id_token must verify against the v2.0 provider (iss + signature + aud)")
	assert.Equal(t, v2Issuer, verified.Issuer, "id_token iss must equal the v2.0 issuer")

	// Access tokens keep the v1 sts.windows.net issuer, even from the v2.0
	// endpoint, as ARM access tokens do.
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

// azureAuthCodeFlow runs the PKCE auth-code flow.
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
