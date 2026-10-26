package azure_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// simGraphBearer is the bootstrap application's Microsoft Graph access token,
// acquired in TestMain with scope https://graph.microsoft.com/.default.
var simGraphBearer string

// graphAuthTransport presents the Graph token on every request the shared
// http.DefaultClient sends to a Microsoft Graph path on the simulator without
// an Authorization of its own, as a Graph client attaches the token it holds
// for https://graph.microsoft.com. The simulator serves Graph and Azure
// Resource Manager on one host, so the path tells them apart.
type graphAuthTransport struct {
	base   http.RoundTripper
	host   string
	bearer string
}

func (t *graphAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host && req.Header.Get("Authorization") == "" &&
		(strings.HasPrefix(req.URL.Path, "/v1.0/") || strings.HasPrefix(req.URL.Path, "/beta/")) {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", t.bearer)
	}
	return t.base.RoundTrip(req)
}

// graphRaw sends one Graph request with exactly the Authorization given,
// through a client that adds none.
func graphRaw(t *testing.T, method, path, authorization string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := (&http.Client{Transport: http.DefaultTransport}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var decoded map[string]any
	if resp.ContentLength != 0 {
		_ = json.NewDecoder(resp.Body).Decode(&decoded)
	}
	return resp.StatusCode, decoded
}

func graphErrorMessage(body map[string]any) (code, message string) {
	e, _ := body["error"].(map[string]any)
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	return code, message
}

// TestGraph_RefusesWithoutAGraphToken creates an application registration
// with no token and with a token for Azure Resource Manager; Microsoft Graph
// refuses both with InvalidAuthenticationToken, and serves the Graph token.
func TestGraph_RefusesWithoutAGraphToken(t *testing.T) {
	t.Parallel()
	app := map[string]any{"displayName": "graph-auth-sdk"}

	status, body := graphRaw(t, http.MethodPost, "/v1.0/applications", "", app)
	require.Equal(t, http.StatusUnauthorized, status, "%v", body)
	code, message := graphErrorMessage(body)
	assert.Equal(t, "InvalidAuthenticationToken", code)
	assert.Equal(t, "Access token is empty.", message)

	status, body = graphRaw(t, http.MethodPost, "/v1.0/applications", simARMBearer, app)
	require.Equal(t, http.StatusUnauthorized, status, "%v", body)
	code, message = graphErrorMessage(body)
	assert.Equal(t, "InvalidAuthenticationToken", code)
	assert.Equal(t, "Access token validation failure. Invalid audience.", message)

	status, body = graphRaw(t, http.MethodPost, "/v1.0/applications", "Bearer not-a-jwt", app)
	require.Equal(t, http.StatusUnauthorized, status, "%v", body)

	status, created := graphRaw(t, http.MethodPost, "/v1.0/applications", simGraphBearer, app)
	require.Equal(t, http.StatusCreated, status, "%v", created)
	id, _ := created["id"].(string)
	status, _ = graphRaw(t, http.MethodDelete, "/v1.0/applications/"+id, simGraphBearer, nil)
	require.Equal(t, http.StatusNoContent, status)
}

// TestGraph_MSALAcquiresAGraphTokenGraphAccepts acquires the Graph token
// through the real MSAL Go confidential client against the production
// authority string, differing from Azure only in coordinates, and creates an
// application registration with it.
func TestGraph_MSALAcquiresAGraphTokenGraphAccepts(t *testing.T) {
	t.Parallel()
	const authorityHost = "login.microsoftonline.com"
	certPath, keyPath := writeAuthorityTLSCert(t, t.TempDir(), authorityHost)
	addr := startTLSSimulator(t, certPath, keyPath, "SIM_RUNTIME=process")
	httpClient := authorityRoutedHTTPClient(t, certPath, authorityHost, addr)

	cred, err := confidential.NewCredFromSecret("test-client-secret")
	require.NoError(t, err)
	app, err := confidential.New("https://"+authorityHost+"/"+simTenantID, "test-client-id", cred,
		confidential.WithHTTPClient(httpClient))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := app.AcquireTokenByCredential(ctx, []string{"https://graph.microsoft.com/.default"})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+authorityHost+"/v1.0/applications", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+result.AccessToken)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
