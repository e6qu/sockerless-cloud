package azure_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMSI_TokenEndpoint pins the IMDS-style metadata service. Real
// Azure exposes managed-identity tokens via two paths
// (`/metadata/identity/oauth2/token` for VMs, `/msi/token` via
// IDENTITY_ENDPOINT for App Service / Container Apps); both must
// return the standard token-payload shape so DefaultAzureCredential
// (Azure SDK + every SDK that wraps it) can mint tokens.
func TestMSI_TokenEndpoint_VMShape(t *testing.T) {
	resp, err := http.Get(baseURL + "/metadata/identity/oauth2/token?api-version=2018-02-01&resource=https%3A%2F%2Fmanagement.azure.com%2F")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))

	tok, _ := payload["access_token"].(string)
	require.NotEmpty(t, tok, "access_token must be present")
	// The MSI token is a real signed JWT (3 dot-separated segments), not a
	// synthetic string, so resource servers can verify it via the JWKS endpoint.
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3, "MSI access_token must be a 3-part JWT")
	claims := decodeJWTClaims(t, tok)
	assert.Equal(t, "https://management.azure.com/", claims["aud"], "JWT aud must equal the requested resource")
	assert.NotEmpty(t, claims["oid"], "JWT must carry the identity's oid")
	assert.Equal(t, claims["oid"], claims["sub"], "MSI token sub equals oid")
	assert.Equal(t, "Bearer", payload["token_type"])
	assert.Equal(t, "https://management.azure.com/", payload["resource"])
	assert.NotEmpty(t, payload["expires_on"])
	_, hasRefresh := payload["refresh_token"]
	assert.False(t, hasRefresh, "IdentityTokenResponse has no refresh_token member")
}

// decodeJWTClaims decodes the payload segment of a JWT into a claims map.
func decodeJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payloadJSON, &claims))
	return claims
}

func TestMSI_TokenEndpoint_AppServiceShape(t *testing.T) {
	resp, err := http.Get(baseURL + "/msi/token?api-version=2019-08-01&resource=https%3A%2F%2Fstorage.azure.com%2F")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))

	assert.NotEmpty(t, payload["access_token"])
	assert.Equal(t, "https://storage.azure.com/", payload["resource"])
}

func TestMSI_TokenEndpoint_RejectsMissingResource(t *testing.T) {
	resp, err := http.Get(baseURL + "/metadata/identity/oauth2/token?api-version=2018-02-01")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"real IMDS rejects requests without 'resource'; sim must too")
}
