package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACR_OAuth2TokenService verifies a registry's token endpoints — POST
// /oauth2/exchange (Microsoft Entra access token → ACR refresh token) and POST
// /oauth2/token (refresh token + scope → scoped access token) — and that the
// access token they yield is the credential the /v2/ data plane accepts.
func TestACR_OAuth2TokenService(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acroauth2registry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	post := func(path string, form url.Values) (int, map[string]any) {
		req, err := http.NewRequest(http.MethodPost, fixture.endpoint()+path, strings.NewReader(form.Encode()))
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

	// exchange: a Microsoft Entra access token issued for the container
	// registry audience → ACR refresh token.
	entraToken, _, err := fetchSimAccessToken("https://containerregistry.azure.net/.default")
	require.NoError(t, err)
	code, out := post("/oauth2/exchange", url.Values{
		"grant_type":   {"access_token"},
		"service":      {fixture.loginServer},
		"access_token": {entraToken},
	})
	require.Equal(t, http.StatusOK, code)
	refresh, _ := out["refresh_token"].(string)
	require.NotEmpty(t, refresh, "exchange must return a non-empty refresh_token")

	// token: refresh token + scope → scoped access token
	code, out = post("/oauth2/token", url.Values{
		"grant_type":    {"refresh_token"},
		"service":       {fixture.loginServer},
		"scope":         {"repository:myrepo:pull,push"},
		"refresh_token": {refresh},
	})
	require.Equal(t, http.StatusOK, code)
	access, _ := out["access_token"].(string)
	require.NotEmpty(t, access, "token must return a non-empty access_token")

	// An unsupported grant is refused with the registry's error envelope.
	code, out = post("/oauth2/exchange", url.Values{
		"grant_type": {"client_credentials"},
		"service":    {fixture.loginServer},
	})
	require.Equal(t, http.StatusBadRequest, code)
	assert.NotEmpty(t, out["error"], "error envelope must carry an error code")

	// The minted access token authenticates the /v2/ data plane it was scoped
	// for, and the base endpoint that any token of this registry reaches.
	resp := acrDoRequest(t, http.MethodGet, fixture.endpoint()+"/v2/myrepo/tags/list", "Bearer "+access, "", nil)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
}
