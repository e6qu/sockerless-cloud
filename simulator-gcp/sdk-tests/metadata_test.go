package gcp_sdk_test

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

// GCE compute metadata server.
//
// Workloads on Cloud Run, Cloud Run Jobs, and Cloud Functions read
// metadata.google.internal/computeMetadata/v1/... at runtime. The sim
// must serve those routes for any test that exercises a workload's
// SDK-level metadata access. These tests hit the routes directly via
// the sim's main listener.

func metadataReq(t *testing.T, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestMetadata_ProjectID(t *testing.T) {
	resp := metadataReq(t, "/computeMetadata/v1/project/project-id")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "Google", resp.Header.Get("Metadata-Flavor"))
	body, _ := io.ReadAll(resp.Body)
	assert.NotEmpty(t, string(body))
}

func TestMetadata_RequiresFlavorHeader(t *testing.T) {
	req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+"/computeMetadata/v1/project/project-id", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestMetadata_InstanceZone(t *testing.T) {
	resp := metadataReq(t, "/computeMetadata/v1/instance/zone")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "projects/")
	assert.Contains(t, string(body), "/zones/")
}

func TestMetadata_DefaultServiceAccountToken(t *testing.T) {
	resp := metadataReq(t, "/computeMetadata/v1/instance/service-accounts/default/token")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tok))
	assert.NotEmpty(t, tok.AccessToken)
	assert.Equal(t, "Bearer", tok.TokenType)
	assert.Greater(t, tok.ExpiresIn, 0)
}

func TestMetadata_DefaultServiceAccountIDToken(t *testing.T) {
	resp := metadataReq(t, "/computeMetadata/v1/instance/service-accounts/default/identity?audience=https://example.com")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	jwt := strings.TrimSpace(string(body))
	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3, "expected JWT shape header.payload.sig, got %q", jwt)

	// Real Google identity tokens are RS256; the sim signs with the same key
	// its data-plane bearer middleware verifies against, so a workload can
	// present this token when invoking a sibling service.
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var hdr struct {
		Alg string `json:"alg"`
	}
	require.NoError(t, json.Unmarshal(header, &hdr))
	assert.Equal(t, "RS256", hdr.Alg)

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Contains(t, claims["aud"], "https://example.com", "identity token must carry the caller-requested audience")

	// The token must be accepted as a data-plane bearer: this is exactly the
	// path a Cloud Run / Cloud Functions workload takes when POSTing to a
	// sibling service URL (the invoke bootstrap 401'd here when the metadata
	// server still handed out an HS256 token the middleware rejected).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/v2/projects/test-project/locations/us-central1/services", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+jwt)
	dpResp, err := rawClient.Do(req)
	require.NoError(t, err)
	defer dpResp.Body.Close()
	assert.Equal(t, http.StatusOK, dpResp.StatusCode,
		"identity token must be accepted by the data-plane bearer middleware")
}

func TestMetadata_IDTokenRequiresAudience(t *testing.T) {
	resp := metadataReq(t, "/computeMetadata/v1/instance/service-accounts/default/identity")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestMetadata_DefaultServiceAccountEmail(t *testing.T) {
	resp := metadataReq(t, "/computeMetadata/v1/instance/service-accounts/default/email")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "iam.gserviceaccount.com")
}
