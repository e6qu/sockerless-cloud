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

// The Instance Metadata Service's token endpoint
// (/metadata/identity/oauth2/token) speaks for the requesting virtual
// machine's identity alone; the tokens it mints are read from inside a
// machine in compute_vm_identity_test.go. What a caller that is no machine
// gets is IMDS's error body: error and error_description.

func imdsTokenFromHost(t *testing.T, query string, withHeader bool) (*http.Response, map[string]string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/metadata/identity/oauth2/token?"+query, nil)
	require.NoError(t, err)
	if withHeader {
		req.Header.Set("Metadata", "true")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body map[string]string
	require.NoError(t, json.Unmarshal(raw, &body), "%s", raw)
	return resp, body
}

// A token request without the Metadata header is refused before anything else
// is looked at, which is what stops a browser being tricked into making one.
func TestMSI_TokenEndpointRequiresMetadataHeader(t *testing.T) {
	t.Parallel()
	resp, body := imdsTokenFromHost(t, "api-version=2018-02-01&resource=https%3A%2F%2Fmanagement.azure.com%2F", false)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "bad_request_102", body["error"])
	assert.Equal(t, "Required metadata header not specified", body["error_description"])
}

// A caller that is no virtual machine has no identity for the metadata
// service to speak for.
func TestMSI_TokenEndpointRefusesACallerThatIsNoMachine(t *testing.T) {
	t.Parallel()
	resp, body := imdsTokenFromHost(t, "api-version=2018-02-01&resource=https%3A%2F%2Fmanagement.azure.com%2F", true)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid_request", body["error"])
	assert.Equal(t, "Identity not found", body["error_description"])
	_, hasToken := body["access_token"]
	assert.False(t, hasToken)
}

func TestMSI_TokenEndpointRejectsMissingResource(t *testing.T) {
	t.Parallel()
	resp, body := imdsTokenFromHost(t, "api-version=2018-02-01", true)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "real IMDS rejects requests without 'resource'; sim must too")
	assert.Equal(t, "invalid_request", body["error"])
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
