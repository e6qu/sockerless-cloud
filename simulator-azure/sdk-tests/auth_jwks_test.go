package azure_sdk_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureEntra_AuthorizationCodeFlow(t *testing.T) {
	tenant := "tenant-auth-code"
	clientID := "client-auth-code"
	redirectURI := "http://127.0.0.1/callback"
	state := "state-123"
	nonce := "nonce-456"
	verifier := "ThisIsntRandomButItNeedsToBe43CharactersLong"
	challengeDigest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeDigest[:])

	authURL := baseURL + "/" + tenant + "/oauth2/v2.0/authorize?" + url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid profile email offline_access https://graph.microsoft.com/User.Read"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	client := http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	authResp, err := client.Get(authURL)
	require.NoError(t, err)
	defer authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)
	location := authResp.Header.Get("Location")
	require.NotEmpty(t, location)
	callback, err := url.Parse(location)
	require.NoError(t, err)
	assert.Equal(t, redirectURI, callback.Scheme+"://"+callback.Host+callback.Path)
	assert.Equal(t, state, callback.Query().Get("state"))
	code := callback.Query().Get("code")
	require.NotEmpty(t, code)

	tokenBody := requestAzureToken(t, tenant, "oauth2/v2.0/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	require.NotEmpty(t, tokenBody.IDToken)
	assert.Equal(t, "openid profile email offline_access https://graph.microsoft.com/User.Read", tokenBody.Scope)
	require.NotEmpty(t, tokenBody.RefreshToken)

	accessPayload := azureTokenPayload(t, tokenBody.AccessToken)
	assert.Equal(t, "https://graph.microsoft.com", accessPayload["aud"])
	idPayload := azureTokenPayload(t, tokenBody.IDToken)
	assert.Equal(t, clientID, idPayload["aud"])
	assert.Equal(t, nonce, idPayload["nonce"])
	assert.Equal(t, "sockerless-test@example.com", idPayload["email"])

	refreshed := requestAzureToken(t, tenant, "oauth2/v2.0/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {tokenBody.RefreshToken},
	})
	require.NotEmpty(t, refreshed.IDToken)
	assert.Equal(t, tokenBody.RefreshToken, refreshed.RefreshToken)
	assert.Equal(t, tokenBody.Scope, refreshed.Scope)
	refreshedPayload := azureTokenPayload(t, refreshed.AccessToken)
	assert.Equal(t, "https://graph.microsoft.com", refreshedPayload["aud"])

	reuseResp, err := http.PostForm(baseURL+"/"+tenant+"/oauth2/v2.0/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer reuseResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, reuseResp.StatusCode)
	assertOAuthError(t, reuseResp, "invalid_grant")
}

func TestAzureEntra_AuthorizationCodeRequiresMatchingPKCE(t *testing.T) {
	tenant := "tenant-pkce"
	redirectURI := "http://127.0.0.1/callback"
	authURL := baseURL + "/" + tenant + "/oauth2/v2.0/authorize?" + url.Values{
		"client_id":             {"client-pkce"},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid"},
		"code_challenge":        {"expected-verifier"},
		"code_challenge_method": {"plain"},
	}.Encode()
	client := http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	authResp, err := client.Get(authURL)
	require.NoError(t, err)
	defer authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)
	callback, err := url.Parse(authResp.Header.Get("Location"))
	require.NoError(t, err)
	code := callback.Query().Get("code")
	require.NotEmpty(t, code)

	tokenResp, err := http.PostForm(baseURL+"/"+tenant+"/oauth2/v2.0/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"client-pkce"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {"wrong-verifier"},
	})
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, tokenResp.StatusCode)
	assertOAuthError(t, tokenResp, "invalid_grant")
}

func TestAzureEntra_DiscoveryAdvertisesServedAuthCodeEndpoints(t *testing.T) {
	tenant := "tenant-discovery"
	resp, err := http.Get(baseURL + "/" + tenant + "/.well-known/openid-configuration")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		AuthorizationEndpoint           string   `json:"authorization_endpoint"`
		TokenEndpoint                   string   `json:"token_endpoint"`
		JWKSURI                         string   `json:"jwks_uri"`
		ResponseTypesSupported          []string `json:"response_types_supported"`
		ResponseModesSupported          []string `json:"response_modes_supported"`
		GrantTypesSupported             []string `json:"grant_types_supported"`
		CodeChallengeMethodsSupported   []string `json:"code_challenge_methods_supported"`
		IDTokenSigningAlgsSupported     []string `json:"id_token_signing_alg_values_supported"`
		TokenEndpointAuthMethodsSupport []string `json:"token_endpoint_auth_methods_supported"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, baseURL+"/"+tenant+"/oauth2/v2.0/authorize", body.AuthorizationEndpoint)
	assert.Equal(t, baseURL+"/"+tenant+"/oauth2/v2.0/token", body.TokenEndpoint)
	assert.Equal(t, baseURL+"/"+tenant+"/discovery/v2.0/keys", body.JWKSURI)
	assert.Contains(t, body.ResponseTypesSupported, "code")
	assert.NotContains(t, body.ResponseTypesSupported, "code id_token")
	assert.ElementsMatch(t, []string{"query", "fragment", "form_post"}, body.ResponseModesSupported)
	assert.Contains(t, body.GrantTypesSupported, "authorization_code")
	assert.Contains(t, body.GrantTypesSupported, "client_credentials")
	assert.Contains(t, body.GrantTypesSupported, "refresh_token")
	assert.ElementsMatch(t, []string{"plain", "S256"}, body.CodeChallengeMethodsSupported)
	assert.Contains(t, body.IDTokenSigningAlgsSupported, "RS256")
	assert.Contains(t, body.TokenEndpointAuthMethodsSupport, "client_secret_post")
}

func TestAzureEntra_AuthorizeRejectsUnsupportedAndMissingFields(t *testing.T) {
	tenant := "tenant-authorize-errors"
	redirectURI := "http://127.0.0.1/callback"
	client := http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	tests := []struct {
		name string
		q    url.Values
		want string
	}{
		{
			name: "missing scope",
			q: url.Values{
				"client_id":     {"client"},
				"response_type": {"code"},
				"redirect_uri":  {redirectURI},
				"state":         {"scope-state"},
			},
			want: "invalid_request",
		},
		{
			name: "hybrid response type outside implemented slice",
			q: url.Values{
				"client_id":     {"client"},
				"response_type": {"code id_token"},
				"redirect_uri":  {redirectURI},
				"scope":         {"openid profile"},
				"state":         {"hybrid-state"},
			},
			want: "unsupported_response_type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := client.Get(baseURL + "/" + tenant + "/oauth2/v2.0/authorize?" + tt.q.Encode())
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusFound, resp.StatusCode)
			location, err := url.Parse(resp.Header.Get("Location"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, location.Query().Get("error"))
			assert.Equal(t, tt.q.Get("state"), location.Query().Get("state"))
		})
	}
}

func TestAzureEntra_TokenRejectsUnsupportedGrantType(t *testing.T) {
	resp, err := http.PostForm(baseURL+"/tenant-grant/oauth2/v2.0/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"client_id":  {"client"},
		"scope":      {"https://graph.microsoft.com/User.Read"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assertOAuthError(t, resp, "unsupported_grant_type")
}

func TestAzureEntra_TokenRS256VerifiesWithJWKS(t *testing.T) {
	tenant := "tenant-rs256"
	tokenBody := requestAzureToken(t, tenant, "oauth2/v2.0/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://management.azure.com/.default"},
	})

	jwksResp, err := http.Get(baseURL + "/" + tenant + "/discovery/v2.0/keys")
	require.NoError(t, err)
	defer jwksResp.Body.Close()
	require.Equal(t, http.StatusOK, jwksResp.StatusCode)
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	require.NoError(t, json.NewDecoder(jwksResp.Body).Decode(&jwks))
	require.Len(t, jwks.Keys, 1)
	key := jwks.Keys[0]
	assert.Equal(t, "sockerless-sim-key-1", key.Kid)
	assert.Equal(t, "RSA", key.Kty)
	assert.Equal(t, "RS256", key.Alg)
	assert.Equal(t, "sig", key.Use)
	require.NotEmpty(t, key.N)
	require.NotEmpty(t, key.E)

	parts := strings.Split(tokenBody.AccessToken, ".")
	require.Len(t, parts, 3)
	var header map[string]string
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(headerJSON, &header))
	assert.Equal(t, "RS256", header["alg"])
	assert.Equal(t, key.Kid, header["kid"])

	nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
	require.NoError(t, err)
	eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
	require.NoError(t, err)
	pub := rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(new(big.Int).SetBytes(eBytes).Int64()),
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	require.NoError(t, rsa.VerifyPKCS1v15(&pub, crypto.SHA256, digest[:], sig))

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))
	assert.Equal(t, tenant, payload["tid"])
	assert.Equal(t, "https://management.azure.com/", payload["aud"])
	assert.Equal(t, "https://sts.windows.net/"+tenant+"/", payload["iss"])
}

func TestAzureEntra_TokenAudienceFollowsScopeAndResource(t *testing.T) {
	tests := []struct {
		name     string
		tenant   string
		tokenURL string
		values   url.Values
		wantAud  string
	}{
		{
			name:     "v2 vault scope",
			tenant:   "tenant-vault",
			tokenURL: "oauth2/v2.0/token",
			values: url.Values{
				"grant_type":    {"client_credentials"},
				"client_id":     {"test-client-id"},
				"client_secret": {"test-client-secret"},
				"scope":         {"https://vault.azure.net/.default"},
			},
			wantAud: "https://vault.azure.net",
		},
		{
			name:     "v2 storage scope",
			tenant:   "tenant-storage",
			tokenURL: "oauth2/v2.0/token",
			values: url.Values{
				"grant_type":    {"client_credentials"},
				"client_id":     {"test-client-id"},
				"client_secret": {"test-client-secret"},
				"scope":         {"https://storage.azure.com/.default"},
			},
			wantAud: "https://storage.azure.com/",
		},
		{
			name:     "v1 service bus resource",
			tenant:   "tenant-servicebus",
			tokenURL: "oauth2/token",
			values: url.Values{
				"grant_type":    {"client_credentials"},
				"client_id":     {"test-client-id"},
				"client_secret": {"test-client-secret"},
				"resource":      {"https://servicebus.azure.net"},
			},
			wantAud: "https://servicebus.azure.net",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenBody := requestAzureToken(t, tt.tenant, tt.tokenURL, tt.values)
			payload := azureTokenPayload(t, tokenBody.AccessToken)
			assert.Equal(t, tt.tenant, payload["tid"])
			assert.Equal(t, tt.wantAud, payload["aud"])
		})
	}
}

type azureTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
}

func requestAzureToken(t *testing.T, tenant, tokenURL string, values url.Values) azureTokenResponse {
	t.Helper()
	tokenResp, err := http.PostForm(baseURL+"/"+tenant+"/"+tokenURL, values)
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	var tokenBody azureTokenResponse
	require.NoError(t, json.NewDecoder(tokenResp.Body).Decode(&tokenBody))
	require.Equal(t, "Bearer", tokenBody.TokenType)
	require.NotEmpty(t, tokenBody.AccessToken)
	return tokenBody
}

func assertOAuthError(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body), string(raw))
	assert.Equal(t, want, body["error"])
}

func azureTokenPayload(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))
	return payload
}
