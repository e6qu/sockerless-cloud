package gcp_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Artifact Registry Docker data plane, reached with the credential the real
// gcloud CLI produces. `gcloud auth configure-docker` installs a credential
// helper that hands a container client an OAuth 2.0 access token under the
// `oauth2accesstoken` username; the documented equivalent a user runs by hand is
//
//	gcloud auth print-access-token | docker login -u oauth2accesstoken \
//	    --password-stdin https://LOCATION-docker.pkg.dev
//
// so the token this test takes from `gcloud auth print-access-token` is exactly
// the password a `docker login` would carry. Everything after that is the
// Docker Registry v2 exchange the container client performs.

var arCLIChallengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// arCLIRegistryDo sends a Docker Registry v2 request with an explicit
// Authorization header (or none), bypassing httpDo's automatic bearer.
func arCLIRegistryDo(t *testing.T, method, url, authorization string) (*http.Response, string) {
	t.Helper()
	return arCLIRegistryDoAt(t, method, url, authorization, "")
}

// arCLIRegistryDoAt sends the same request addressed to a named Artifact
// Registry endpoint — the `LOCATION-docker.pkg.dev` coordinate a client
// resolving the real service dials — while the simulator's address carries it.
func arCLIRegistryDoAt(t *testing.T, method, url, authorization, registry string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	if registry != "" {
		req.Host = registry
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := (&http.Client{Transport: http.DefaultTransport}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func arCLIRegistryError(t *testing.T, body string) (string, string) {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope), "body: %s", body)
	require.Len(t, envelope.Errors, 1, "body: %s", body)
	return envelope.Errors[0].Code, envelope.Errors[0].Message
}

// TestArtifactRegistryDataPlaneAuthWithGcloudPrintedToken drives the data plane
// with a credential minted by the real gcloud CLI, through the same challenge →
// token service → retry exchange a container client performs.
func TestArtifactRegistryDataPlaneAuthWithGcloudPrintedToken(t *testing.T) {
	runCLI(t, gcloudCLI("artifacts", "repositories", "create", "cli-auth-repo",
		"--location="+location,
		"--repository-format=docker",
		"--format=json",
	))

	// The credential a `docker login` would carry, produced by the real CLI.
	token := strings.TrimSpace(runCLI(t, gcloudCLI("auth", "print-access-token")))
	require.NotEmpty(t, token, "gcloud auth print-access-token returned nothing")
	credential := "Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:"+token))

	repo := project + "/cli-auth-repo/app"
	manifestURL := baseURL + "/v2/" + repo + "/manifests/latest"

	// 1. Unauthenticated: the Bearer challenge naming the token service.
	resp, body := arCLIRegistryDo(t, http.MethodGet, manifestURL, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
	challengeHeader := resp.Header.Get("WWW-Authenticate")
	require.NotEmpty(t, challengeHeader, "an uncredentialled request must draw a challenge")
	challenge := map[string]string{}
	for _, match := range arCLIChallengeParam.FindAllStringSubmatch(challengeHeader, -1) {
		challenge[match[1]] = match[2]
	}
	assert.Equal(t, baseURL+"/v2/token", challenge["realm"])
	assert.Equal(t, "repository:"+repo+":pull", challenge["scope"])
	code, message := arCLIRegistryError(t, body)
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "not authenticated: No valid credential was supplied.", message)

	// 2. The token service at the realm, with the gcloud-minted credential.
	resp, body = arCLIRegistryDo(t, http.MethodGet,
		challenge["realm"]+"?service="+challenge["service"]+"&scope="+challenge["scope"], credential)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var issued struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &issued))
	require.NotEmpty(t, issued.Token)
	assert.Equal(t, 43200, issued.ExpiresIn)

	// 3. The retry with the registry token reaches the repository: a missing
	//    manifest inside a repository the caller can reach is MANIFEST_UNKNOWN.
	resp, body = arCLIRegistryDo(t, http.MethodGet, manifestURL, "Bearer "+issued.Token)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, body)
	code, _ = arCLIRegistryError(t, body)
	assert.Equal(t, "MANIFEST_UNKNOWN", code)

	// 4. The same gcloud credential presented directly as the Bearer, the form
	//    Google's Docker Registry API documentation uses with
	//    `curl -H "Authorization: Bearer $(gcloud auth print-access-token)"`.
	resp, body = arCLIRegistryDo(t, http.MethodGet, manifestURL, "Bearer "+token)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, body)

	// 5. A credential the registry cannot accept is refused with no challenge.
	resp, body = arCLIRegistryDo(t, http.MethodGet, manifestURL, "Bearer not-a-real-token")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
	assert.Empty(t, resp.Header.Get("WWW-Authenticate"))
	code, message = arCLIRegistryError(t, body)
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "No valid credential was supplied.", message)

	// 6. The gcloud credential against a repository that was never created is
	//    denied at the resource, naming the IAM permission Artifact Registry
	//    checks for a pull. The location in the resource is the registry
	//    endpoint's, so the request addresses one — which is what the live
	//    service reports back for a repository that exists nowhere.
	resp, body = arCLIRegistryDoAt(t, http.MethodGet,
		baseURL+"/v2/"+project+"/absent-repo/app/manifests/latest", "Bearer "+token,
		location+"-docker.pkg.dev")
	require.Equal(t, http.StatusForbidden, resp.StatusCode, body)
	code, message = arCLIRegistryError(t, body)
	assert.Equal(t, "DENIED", code)
	assert.Equal(t,
		`Permission "artifactregistry.repositories.downloadArtifacts" denied on resource `+
			`"projects/`+project+`/locations/`+location+`/repositories/absent-repo" (or it may not exist)`,
		message)

	// 7. The same request at the simulator's own coordinate, which names no
	//    location: no location holds that repository, so no Artifact Registry
	//    resource is identified and none is invented to name in a refusal.
	resp, body = arCLIRegistryDo(t, http.MethodGet,
		baseURL+"/v2/"+project+"/absent-repo/app/manifests/latest", "Bearer "+token)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, body)
	code, message = arCLIRegistryError(t, body)
	assert.Equal(t, "NAME_UNKNOWN", code)
	assert.Equal(t, "repository name not known to registry", message)
	assert.NotContains(t, body, location,
		"the refusal must not name a location the request never supplied")
}
