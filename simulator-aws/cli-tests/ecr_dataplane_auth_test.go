package aws_cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the Amazon ECR private-registry credential contract. The
// AWS CLI's own `aws ecr get-login-password` is the documented way to obtain
// the credential —
//
//	aws ecr get-login-password --region <region> |
//	    docker login --username AWS --password-stdin <account>.dkr.ecr.<region>.amazonaws.com
//
// (Amazon ECR User Guide, "Private registry authentication in Amazon ECR") —
// and the command prints exactly the password half a Docker client re-encodes
// as `AWS:<password>`. This drives the CLI for the credential and then presents
// it to the registry's /v2/ data plane the way that client does, along with the
// refusals: no credential, and the wrong password.
//
// `aws ecr get-authorization-token` covers the other half of the same
// documentation, which pipes the whole base64 token into
// `-H "Authorization: Basic $TOKEN"`.

// ecrCLIRegistryRequest sends one Docker Registry HTTP API v2 request to the
// simulator's registry coordinate with the given Authorization header.
func ecrCLIRegistryRequest(t *testing.T, method, path string, body []byte, contentType, authorization string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp, string(payload)
}

func TestECRCLI_RegistryDataPlaneCredentialContract(t *testing.T) {
	const repo = "ecr-cli-auth/app"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
	t.Cleanup(func() {
		runCLI(t, awsCLI("ecr", "delete-repository", "--repository-name", repo, "--force"))
	})

	// `aws ecr get-login-password` prints the password half of the token.
	password := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-login-password")))
	require.NotEmpty(t, password)
	assert.NotContains(t, password, ":", "get-login-password prints the password, not user:password")

	// `aws ecr get-authorization-token` serves the whole base64 token, and it
	// decodes to `AWS:<password>` — the two commands describe one credential.
	out := runCLI(t, awsCLI("ecr", "get-authorization-token"))
	var authorization struct {
		AuthorizationData []struct {
			AuthorizationToken string `json:"authorizationToken"`
			ExpiresAt          string `json:"expiresAt"`
			ProxyEndpoint      string `json:"proxyEndpoint"`
		} `json:"authorizationData"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &authorization))
	require.Len(t, authorization.AuthorizationData, 1)
	token := authorization.AuthorizationData[0].AuthorizationToken
	require.NotEmpty(t, token)
	assert.Contains(t, authorization.AuthorizationData[0].ProxyEndpoint, ".dkr.ecr.")
	decoded, err := base64.StdEncoding.DecodeString(token)
	require.NoError(t, err)
	user, tokenPassword, ok := strings.Cut(string(decoded), ":")
	require.True(t, ok)
	assert.Equal(t, "AWS", user)
	assert.NotEmpty(t, tokenPassword)

	dockerCredential := "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:"+password))

	// No credential: the registry answers Amazon ECR's Basic challenge.
	resp, body := ecrCLIRegistryRequest(t, http.MethodGet, "/v2/", nil, "", "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Basic realm="`+baseURL+`/",service="ecr.amazonaws.com"`,
		resp.Header.Get("Www-Authenticate"))
	assert.Equal(t, "Not Authorized", strings.TrimSpace(body))

	// The wrong password is refused the same way.
	wrong := "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:not-the-password"))
	resp, body = ecrCLIRegistryRequest(t, http.MethodGet, "/v2/"+repo+"/tags/list", nil, "", wrong)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Not Authorized", strings.TrimSpace(body))

	// Both credentials the CLI serves authenticate the whole data plane.
	for name, credential := range map[string]string{
		"get-login-password":      dockerCredential,
		"get-authorization-token": "Basic " + token,
	} {
		t.Run(name, func(t *testing.T) {
			resp, _ := ecrCLIRegistryRequest(t, http.MethodGet, "/v2/", nil, "", credential)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}

	// A push and a pull with the credential, then the control plane sees it.
	layer := []byte("ecr-cli-authenticated-layer")
	sum := sha256.Sum256(layer)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	resp, _ = ecrCLIRegistryRequest(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, "", dockerCredential)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	location := resp.Header.Get("Location")
	require.NotEmpty(t, location)

	resp, _ = ecrCLIRegistryRequest(t, http.MethodPatch, location, layer, "application/octet-stream", dockerCredential)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp, _ = ecrCLIRegistryRequest(t, http.MethodPut, location+"?digest="+digest, nil, "", dockerCredential)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":%d,"digest":"%s"},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":%d,"digest":"%s"}]}`,
		len(layer), digest, len(layer), digest))
	resp, _ = ecrCLIRegistryRequest(t, http.MethodPut, "/v2/"+repo+"/manifests/cli",
		manifest, "application/vnd.docker.distribution.manifest.v2+json", dockerCredential)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp, body = ecrCLIRegistryRequest(t, http.MethodGet, "/v2/"+repo+"/tags/list", nil, "", dockerCredential)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, `"cli"`)

	out = runCLI(t, awsCLI("ecr", "list-images", "--repository-name", repo))
	assert.Contains(t, out, "cli")
}
