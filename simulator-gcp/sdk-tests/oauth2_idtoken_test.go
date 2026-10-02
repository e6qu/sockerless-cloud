package gcp_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

// writeServiceAccountJSON stages the service-account credentials file a Cloud
// Run / Cloud Functions backend runs with (GOOGLE_APPLICATION_CREDENTIALS) the
// way an operator provisions it: a service account created through the real
// IAM API and a key minted with CreateServiceAccountKey, whose decoded
// privateKeyData IS the credential file. The token endpoint verifies
// assertions against the account's registered keys, so a self-generated
// keypair would be rejected — only the minted file works, exactly as against
// Google. The file's `token_uri` is repointed at the simulator's token
// endpoint: the coordinate, the only thing that changes. The
// google.golang.org/api/idtoken service-account flow signs a JWT-bearer
// assertion with the minted key, POSTs it to `token_uri`, and reads back the
// `id_token`.
func writeServiceAccountJSON(t *testing.T) (path, email string) {
	t.Helper()
	svc := iamService(t)
	sa, err := svc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      uniqueName("sockerless-runner"),
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Sockerless runner"},
		}).Do()
	require.NoError(t, err)
	key, err := svc.Projects.ServiceAccounts.Keys.Create(sa.Name,
		&iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)

	raw, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	require.NoError(t, err)
	var keyFile map[string]any
	require.NoError(t, json.Unmarshal(raw, &keyFile))
	keyFile["token_uri"] = baseURL + "/token"
	body, err := json.Marshal(keyFile)
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "sa-*.json")
	require.NoError(t, err)
	_, err = f.Write(body)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name(), sa.Email
}

// TestOAuth2IDToken_InvokeBearerAccepted reproduces the minting half of the
// Cloud Run backend's service-invoke path: with a service-account credential
// whose token_uri is the simulator, google.golang.org/api/idtoken runs the
// service-account JWT-bearer flow with a target_audience, and the token
// endpoint answers with an RS256 id_token whose single `aud` is that audience —
// the service URL the backend presents it to.
func TestOAuth2IDToken_InvokeBearerAccepted(t *testing.T) {
	saPath, email := writeServiceAccountJSON(t)
	const audience = "https://target-service-abcdefghij-us-central1.a.run.app"

	ts, err := idtoken.NewTokenSource(ctx, audience, option.WithCredentialsFile(saPath))
	require.NoError(t, err)
	tok, err := ts.Token()
	require.NoError(t, err)
	require.NotEmpty(t, tok.AccessToken)

	parts := strings.Split(tok.AccessToken, ".")
	require.Len(t, parts, 3, "id_token must be a 3-segment JWT")
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var hdr struct {
		Alg string `json:"alg"`
	}
	require.NoError(t, json.Unmarshal(header, &hdr))
	require.Equal(t, "RS256", hdr.Alg, "invoke bearer must be RS256, not HS256")

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Equal(t, audience, claims["aud"], "the id_token's audience is the requested target_audience")
	assert.Equal(t, email, claims["email"])

	// An ID token is not an OAuth access token: a Google API refuses it.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/v2/projects/test-project/locations/us-central1/services", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := rawClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
