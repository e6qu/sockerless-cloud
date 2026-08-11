package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawClient is an http.Client that does NOT carry the simulator token source
// the shared http.DefaultClient does, so a test can observe how the data plane
// treats an unauthenticated or invalidly-authenticated request.
var rawClient = &http.Client{}

// TestDataPlaneAuth_RejectsMissingAndInvalidTokens proves the data-plane bearer
// middleware rejects a request with no token and a request with a token the
// simulator did not sign, in Google's UNAUTHENTICATED error shape, and admits a
// request bearing a real simulator-minted token.
func TestDataPlaneAuth_RejectsMissingAndInvalidTokens(t *testing.T) {
	// A real Google data-plane read: list Cloud Run services.
	url := baseURL + "/v2/projects/test-project/locations/us-central1/services"

	t.Run("missing token", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		require.NoError(t, err)
		resp, err := rawClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assertUnauthenticatedShape(t, resp)
	})

	t.Run("token not signed by the simulator", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		require.NoError(t, err)
		// A structurally valid-looking but forged JWT.
		req.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJmb3JnZWQifQ.c2ln")
		resp, err := rawClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assertUnauthenticatedShape(t, resp)
	})

	t.Run("real simulator-minted token", func(t *testing.T) {
		tok, err := simTokenFetcher{base: baseURL}.Token()
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		require.NoError(t, err)
		tok.SetAuthHeader(req)
		resp, err := rawClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// TestDataPlaneAuth_UnroutedPathsAnswerNotFoundAnonymously pins the ordering
// Google's own API frontend uses: a URI no method publishes is answered before
// the credential is examined, so it reads 404 without a token, while a real API
// path reads 401. Authenticating first would report every path the simulator
// does not serve as one that merely rejected the caller — the absence of a
// route and an authentication failure would be indistinguishable.
func TestDataPlaneAuth_UnroutedPathsAnswerNotFoundAnonymously(t *testing.T) {
	for _, path := range []string{
		"/api/sessions/me",
		"/api/definitely-not-a-real-path",
		"/v1/no-such-google-surface/test-project",
	} {
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		require.NoError(t, err)
		resp, err := rawClient.Do(req)
		require.NoError(t, err, path)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode,
			"%s publishes no method, so it is not found — not unauthenticated", path)
	}

	// A path a method does publish still answers 401 without a token, so the
	// route-first ordering has not disabled the credential gate.
	req, err := http.NewRequest(http.MethodGet,
		baseURL+"/v2/projects/test-project/locations/us-central1/services", nil)
	require.NoError(t, err)
	resp, err := rawClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assertUnauthenticatedShape(t, resp)
}

// TestDataPlaneAuth_ExemptEndpoints proves the surfaces a real Google client
// reaches without an OAuth2 token stay reachable without one: the health check,
// OpenID Connect discovery, and the JSON Web Key Set.
func TestDataPlaneAuth_ExemptEndpoints(t *testing.T) {
	for _, path := range []string{"/health", "/.well-known/openid-configuration", "/.well-known/jwks.json"} {
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		require.NoError(t, err)
		resp, err := rawClient.Do(req)
		require.NoError(t, err, path)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
	}

	// The JWKS advertises the RSA signing key the middleware verifies against.
	resp, err := rawClient.Get(baseURL + "/.well-known/jwks.json")
	require.NoError(t, err)
	defer resp.Body.Close()
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&jwks))
	require.Len(t, jwks.Keys, 1)
	assert.Equal(t, "RSA", jwks.Keys[0].Kty)
	assert.Equal(t, "RS256", jwks.Keys[0].Alg)
	assert.NotEmpty(t, jwks.Keys[0].Kid)
	assert.NotEmpty(t, jwks.Keys[0].N)
	assert.NotEmpty(t, jwks.Keys[0].E)
}

func assertUnauthenticatedShape(t *testing.T, resp *http.Response) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope), "body: %s", body)
	assert.Equal(t, 401, envelope.Error.Code)
	assert.Equal(t, "UNAUTHENTICATED", envelope.Error.Status)
	assert.NotEmpty(t, envelope.Error.Message)
}
