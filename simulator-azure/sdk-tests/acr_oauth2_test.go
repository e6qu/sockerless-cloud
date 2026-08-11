package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACR_OAuth2TokenService verifies ACR's registry-token auth endpoints:
// POST /oauth2/exchange (Entra access token -> ACR refresh token) and
// POST /oauth2/token (refresh token + scope -> scoped Bearer). Without these an
// ACR-shaped client that follows the Bearer challenge flow 404s before reaching
// the /v2/ data plane.
func TestACR_OAuth2TokenService(t *testing.T) {
	post := func(path string, form url.Values) (int, map[string]any) {
		req, err := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(form.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return resp.StatusCode, out
	}

	// exchange: access token -> refresh token
	code, out := post("/oauth2/exchange", url.Values{
		"grant_type":   {"access_token"},
		"service":      {"localhost"},
		"access_token": {"dummy-entra-token"},
	})
	require.Equal(t, http.StatusOK, code)
	refresh, _ := out["refresh_token"].(string)
	require.NotEmpty(t, refresh, "exchange must return a non-empty refresh_token")

	// token: refresh token + scope -> scoped Bearer access token
	code, out = post("/oauth2/token", url.Values{
		"grant_type":    {"refresh_token"},
		"service":       {"localhost"},
		"scope":         {"repository:myrepo:pull,push"},
		"refresh_token": {refresh},
	})
	require.Equal(t, http.StatusOK, code)
	access, _ := out["access_token"].(string)
	require.NotEmpty(t, access, "token must return a non-empty access_token")

	// ACR-shaped error envelope on a bad request.
	code, out = post("/oauth2/exchange", url.Values{"grant_type": {"access_token"}, "service": {"localhost"}})
	require.Equal(t, http.StatusBadRequest, code)
	assert.NotEmpty(t, out["error"], "error envelope must carry an error code")

	// The minted Bearer is accepted by the /v2/ data plane (auth is not enforced).
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v2/", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
}
