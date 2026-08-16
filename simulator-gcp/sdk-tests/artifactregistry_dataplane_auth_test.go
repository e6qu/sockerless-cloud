package gcp_sdk_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	artifactregistry "google.golang.org/api/artifactregistry/v1"
	iamadmin "google.golang.org/api/iam/v1"
	iamcredentials "google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

// The Artifact Registry Docker data plane, driven the way a container client
// drives it: an unauthenticated request, the Bearer challenge it answers with,
// the token service the challenge's realm names, and the retry that carries the
// token. Every credential these present is one the simulator's own Google
// surfaces issued — an OAuth 2.0 access token from the token endpoint, or a
// service-account key file from the IAM CreateServiceAccountKey API — so the
// exchange differs from the same exchange against
// `<location>-docker.pkg.dev` only in the endpoint coordinate.

// arNoAuthClient is an HTTP client that does NOT carry the shared
// simAuthTransport credential, so a test can send exactly the credential it
// means to (or none at all). The transport the suite installs on
// http.DefaultClient would otherwise attach an access token to every request,
// which is precisely what these tests are measuring the absence of.
var arNoAuthClient = &http.Client{Transport: http.DefaultTransport}

// arRawDo sends a request with an explicit Authorization header (or none) and
// returns the response together with its body.
func arRawDo(t *testing.T, method, url, authorization string, body []byte, contentType string) (*http.Response, string) {
	t.Helper()
	return arRawDoAtRegistry(t, method, url, authorization, body, contentType, "")
}

// arRawDoAtRegistry sends the same request addressed to a named Artifact
// Registry endpoint — the `LOCATION-docker.pkg.dev` coordinate a client
// resolving the real service dials — while the simulator's address carries it.
// An empty registry leaves the simulator's own coordinate in the `Host` header.
func arRawDoAtRegistry(t *testing.T, method, url, authorization string, body []byte, contentType, registry string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	require.NoError(t, err)
	if registry != "" {
		req.Host = registry
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := arNoAuthClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(data)
}

// arRegistryErrorOf decodes the Docker Registry v2 error envelope.
func arRegistryErrorOf(t *testing.T, body string) (string, string) {
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

// arChallengeParams parses the parameters out of a Docker Registry v2 Bearer
// challenge, the way a container client parses it to learn where to get a token
// and what to ask for.
var arChallengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

func arParseChallenge(t *testing.T, header string) map[string]string {
	t.Helper()
	require.True(t, strings.HasPrefix(header, "Bearer "), "challenge %q is not a Bearer challenge", header)
	params := map[string]string{}
	for _, match := range arChallengeParam.FindAllStringSubmatch(header, -1) {
		params[match[1]] = match[2]
	}
	return params
}

// arBasic renders an HTTP Basic credential.
func arBasic(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// arFollowChallenge runs the exchange a container client runs after a 401: it
// GETs the challenge's realm with the credential from its credential store,
// carrying the service and scope the challenge asked for, and returns the token
// the token service issues.
func arFollowChallenge(t *testing.T, challenge map[string]string, authorization string) string {
	t.Helper()
	realm := challenge["realm"]
	require.NotEmpty(t, realm, "challenge names no realm")
	url := realm + "?service=" + challenge["service"] + "&scope=" + challenge["scope"]

	resp, body := arRawDo(t, http.MethodGet, url, authorization, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "token service: %s", body)
	var issued struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &issued))
	require.NotEmpty(t, issued.Token)
	// Artifact Registry issues a twelve-hour registry token.
	assert.Equal(t, 43200, issued.ExpiresIn)
	return issued.Token
}

// arCreateRepository creates a repository through the Artifact Registry SDK,
// which is the only way one comes into existence.
func arCreateRepository(t *testing.T, project, location, repoID string) {
	t.Helper()
	service, err := artifactregistry.NewService(ctx,
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	parent := fmt.Sprintf("projects/%s/locations/%s", project, location)
	op, err := service.Projects.Locations.Repositories.
		Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).
		RepositoryId(repoID).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
}

// TestArtifactRegistry_DockerLoginTokenExchange drives the complete exchange a
// `docker login` + `docker push` + `docker pull` performs against
// `<location>-docker.pkg.dev`: the unauthenticated ping that draws the
// challenge, the token service the realm names, and a full blob + manifest
// round trip carried by the token it issues. Nothing here is simulator-aware —
// the same code against a real Artifact Registry differs only in the host it
// is pointed at and the source of the access token.
func TestArtifactRegistry_DockerLoginTokenExchange(t *testing.T) {
	arCreateRepository(t, "test-project", "us-central1", "login-repo")
	repo := "test-project/login-repo/app"

	// 1. The ping a client makes before it has any token. Artifact Registry
	//    answers 401 and names its token service.
	resp, body := arRawDo(t, http.MethodGet, baseURL+"/v2/", "", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
	require.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
	ping := arParseChallenge(t, resp.Header.Get("WWW-Authenticate"))
	assert.Equal(t, baseURL+"/v2/token", ping["realm"])
	code, message := arRegistryErrorOf(t, body)
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "not authenticated: No credential was supplied.", message)

	// 2. The credential the gcloud CLI credential helper puts in the client's
	//    credential store: the `oauth2accesstoken` username with an OAuth 2.0
	//    access token as the password.
	accessToken := strings.TrimPrefix(simBearerHeader(t), "Bearer ")
	credential := arBasic("oauth2accesstoken", accessToken)

	// 3. A write draws a challenge naming the pull,push pair, and the token
	//    service issues a token for it.
	resp, body = arRawDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", "", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
	pushChallenge := arParseChallenge(t, resp.Header.Get("WWW-Authenticate"))
	assert.Equal(t, "repository:"+repo+":pull,push", pushChallenge["scope"])
	assert.Equal(t, strings.TrimPrefix(baseURL, "http://"), pushChallenge["service"])
	registryToken := arFollowChallenge(t, pushChallenge, credential)
	bearer := "Bearer " + registryToken

	// 4. The ping again, now with the token — this is what makes a
	//    `docker login` report success.
	resp, body = arRawDo(t, http.MethodGet, baseURL+"/v2/", bearer, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	// 5. Push a blob monolithically and a manifest that references it.
	layer := []byte("artifact-registry-authenticated-layer")
	digest := arDigest(layer)
	resp, body = arRawDo(t, http.MethodPost,
		baseURL+"/v2/"+repo+"/blobs/uploads/?digest="+digest, bearer, layer, "application/octet-stream")
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)

	manifest := []byte(fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":%q,"size":%d},"layers":[{"digest":%q,"size":%d}]}`,
		digest, len(layer), digest, len(layer)))
	resp, body = arRawDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1",
		bearer, manifest, "application/vnd.oci.image.manifest.v1+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)

	// 6. Pull it back with the same token.
	resp, body = arRawDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", bearer, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.JSONEq(t, string(manifest), body)

	resp, body = arRawDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+digest, bearer, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, string(layer), body)

	resp, body = arRawDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/tags/list", bearer, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.JSONEq(t, `{"name":"`+repo+`","tags":["v1"]}`, body)

	// 7. The control plane registered the pushed image, so the push that the
	//    registry token authorized is visible through the Artifact Registry
	//    API — the two planes agree about what was written.
	service, err := artifactregistry.NewService(ctx,
		option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	images, err := service.Projects.Locations.Repositories.DockerImages.
		List("projects/test-project/locations/us-central1/repositories/login-repo").Do()
	require.NoError(t, err)
	require.Len(t, images.DockerImages, 1)
	assert.Equal(t, []string{"v1"}, images.DockerImages[0].Tags)
}

// TestArtifactRegistry_AcceptedCredentialForms covers every credential Artifact
// Registry documents for the Docker data plane, each verified against material
// this simulator's own Google APIs issued: a bare OAuth 2.0 access token as the
// Bearer (the form Google's own `curl -H "Authorization: Bearer $(gcloud auth
// print-access-token)"` example uses), the `oauth2accesstoken` Basic username
// the credential helpers produce, and the `_json_key` / `_json_key_base64`
// service-account key forms.
func TestArtifactRegistry_AcceptedCredentialForms(t *testing.T) {
	arCreateRepository(t, "test-project", "us-central1", "creds-repo")
	manifestURL := baseURL + "/v2/test-project/creds-repo/app/manifests/latest"

	accessToken := strings.TrimPrefix(simBearerHeader(t), "Bearer ")
	keyFile, _ := arServiceAccountKey(t, "ar-dataplane-creds")

	for _, tc := range []struct {
		name          string
		authorization string
	}{
		{"a bare OAuth 2.0 access token as the Bearer", "Bearer " + accessToken},
		{"the oauth2accesstoken Basic username", arBasic("oauth2accesstoken", accessToken)},
		{"the _json_key service-account key file", arBasic("_json_key", string(keyFile))},
		{"the _json_key_base64 service-account key file", arBasic("_json_key_base64", base64.StdEncoding.EncodeToString(keyFile))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The repository is reachable, so the request is served by the
			// registry: a missing manifest is MANIFEST_UNKNOWN, never a
			// credential refusal.
			resp, body := arRawDo(t, http.MethodGet, manifestURL, tc.authorization, nil, "")
			require.Equal(t, http.StatusNotFound, resp.StatusCode, body)
			code, _ := arRegistryErrorOf(t, body)
			assert.Equal(t, "MANIFEST_UNKNOWN", code)
		})
	}
}

// TestArtifactRegistry_RefusesAForgedServiceAccountKey is the negative beside
// the `_json_key` credential above. A service-account key file names its
// account and its key id in the clear — neither is a secret — so the only thing
// that makes the credential a credential is the private key inside it. A file
// whose private half was replaced with another account's must not authenticate.
func TestArtifactRegistry_RefusesAForgedServiceAccountKey(t *testing.T) {
	arCreateRepository(t, "test-project", "us-central1", "forged-repo")
	manifestURL := baseURL + "/v2/test-project/forged-repo/app/manifests/latest"

	genuine, genuineFields := arServiceAccountKey(t, "ar-dataplane-genuine")
	_, otherFields := arServiceAccountKey(t, "ar-dataplane-other")

	// The genuine file authenticates.
	resp, body := arRawDo(t, http.MethodGet, manifestURL, arBasic("_json_key", string(genuine)), nil, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, body)

	// The same file with another account's private key swapped in does not,
	// even though it still names a registered account and a registered key id.
	forged := map[string]any{}
	require.NoError(t, json.Unmarshal(genuine, &forged))
	forged["private_key"] = otherFields["private_key"]
	forgedFile, err := json.Marshal(forged)
	require.NoError(t, err)
	require.Equal(t, genuineFields["private_key_id"], forged["private_key_id"])

	resp, body = arRawDo(t, http.MethodGet, manifestURL, arBasic("_json_key", string(forgedFile)), nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
	assert.Empty(t, resp.Header.Get("WWW-Authenticate"))
	code, message := arRegistryErrorOf(t, body)
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "No valid credential was supplied.", message)
}

// TestArtifactRegistry_RefusesAnUnreachableRepository covers what happens when a
// credential authenticates but its principal cannot reach the repository the
// request addresses: Artifact Registry answers 403 DENIED naming the IAM
// permission and the repository resource, and says "(or it may not exist)"
// rather than telling the caller which of the two it is.
func TestArtifactRegistry_RefusesAnUnreachableRepository(t *testing.T) {
	accessToken := strings.TrimPrefix(simBearerHeader(t), "Bearer ")
	bearer := "Bearer " + accessToken

	for _, tc := range []struct {
		name       string
		method     string
		registry   string
		path       string
		permission string
		resource   string
	}{{
		name:       "pulling from another project",
		method:     http.MethodGet,
		registry:   "us-central1-docker.pkg.dev",
		path:       "/v2/unknown-project/unknown-repo/app/manifests/latest",
		permission: "artifactregistry.repositories.downloadArtifacts",
		resource:   "projects/unknown-project/locations/us-central1/repositories/unknown-repo",
	}, {
		name:       "pushing into a repository of this project that was never created",
		method:     http.MethodPost,
		registry:   "us-central1-docker.pkg.dev",
		path:       "/v2/test-project/never-created-repo/app/blobs/uploads/",
		permission: "artifactregistry.repositories.uploadArtifacts",
		resource:   "projects/test-project/locations/us-central1/repositories/never-created-repo",
	}, {
		// The location is the registry endpoint's, so the same path denied at
		// one location's registry names another location's resource at another.
		name:       "pulling the same path at another location's registry",
		method:     http.MethodGet,
		registry:   "europe-west1-docker.pkg.dev",
		path:       "/v2/unknown-project/unknown-repo/app/manifests/latest",
		permission: "artifactregistry.repositories.downloadArtifacts",
		resource:   "projects/unknown-project/locations/europe-west1/repositories/unknown-repo",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := arRawDoAtRegistry(t, tc.method, baseURL+tc.path, bearer, nil, "", tc.registry)
			require.Equal(t, http.StatusForbidden, resp.StatusCode, body)
			code, message := arRegistryErrorOf(t, body)
			assert.Equal(t, "DENIED", code)
			assert.Equal(t, fmt.Sprintf("Permission %q denied on resource %q (or it may not exist)",
				tc.permission, tc.resource), message)
		})
	}
}

// TestArtifactRegistry_RefusesARepositoryNoLocationHolds addresses the
// simulator at its own coordinate, which names no location — so a repository
// the control plane never created anywhere identifies no Artifact Registry
// resource, and the registry refuses without naming one at a location the
// request never supplied.
func TestArtifactRegistry_RefusesARepositoryNoLocationHolds(t *testing.T) {
	bearer := simBearerHeader(t)

	resp, body := arRawDo(t, http.MethodGet,
		baseURL+"/v2/unknown-project/unknown-repo/app/manifests/latest", bearer, nil, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, body)
	code, message := arRegistryErrorOf(t, body)
	assert.Equal(t, "NAME_UNKNOWN", code)
	assert.Equal(t, "repository name not known to registry", message)
	assert.NotContains(t, body, "us-central1",
		"the refusal must not name a location the request never supplied")
}

// TestArtifactRegistry_UnauthenticatedPullIsDeniedAtTheResource is the path a
// `docker pull` with no prior `docker login` takes: the client follows the
// challenge, the token service issues it a token without a credential, and the
// data plane refuses that identity at the repository. The refusal a user sees
// is therefore a `denied:` naming the permission, not an authentication error.
func TestArtifactRegistry_UnauthenticatedPullIsDeniedAtTheResource(t *testing.T) {
	arCreateRepository(t, "test-project", "us-central1", "anon-repo")
	repo := "test-project/anon-repo/app"

	resp, body := arRawDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/latest", "", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
	challenge := arParseChallenge(t, resp.Header.Get("WWW-Authenticate"))

	// No credential goes to the token service, and it issues a token anyway.
	anonymous := arFollowChallenge(t, challenge, "")

	resp, body = arRawDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/latest", "Bearer "+anonymous, nil, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode, body)
	code, message := arRegistryErrorOf(t, body)
	assert.Equal(t, "DENIED", code)
	assert.Equal(t,
		`Unauthenticated request. Unauthenticated requests do not have permission `+
			`"artifactregistry.repositories.downloadArtifacts" on resource `+
			`"projects/test-project/locations/us-central1/repositories/anon-repo" (or it may not exist)`,
		message)
}

// arServiceAccountKey creates a service account and a user-managed key for it
// through the real IAM API, and returns the decoded key file together with its
// fields. This is the credential a `_json_key` docker login presents.
func arServiceAccountKey(t *testing.T, accountID string) ([]byte, map[string]any) {
	t.Helper()
	service, err := iamadmin.NewService(ctx,
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	account, err := service.Projects.ServiceAccounts.Create("projects/test-project",
		&iamadmin.CreateServiceAccountRequest{AccountId: accountID}).Do()
	require.NoError(t, err)

	key, err := service.Projects.ServiceAccounts.Keys.Create(account.Name,
		&iamadmin.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, key.PrivateKeyData)

	keyFile, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	require.NoError(t, err)
	fields := map[string]any{}
	require.NoError(t, json.Unmarshal(keyFile, &fields))
	require.Equal(t, "service_account", fields["type"])
	return keyFile, fields
}

// TestArtifactRegistry_RefusesAnExpiredMintedCredential presents the data plane
// a credential that expired, obtained the way a client obtains one: the IAM
// Service Account Credentials API mints an access token for the service account
// with the shortest lifetime the request may ask for, and the token is
// presented once it has passed. Both carriers Artifact Registry accepts an
// access token through are covered, and both are refused with the flat 401 the
// live service sends for a credential it cannot accept — with no challenge
// header, which would only send the client back for the same rejected token.
func TestArtifactRegistry_RefusesAnExpiredMintedCredential(t *testing.T) {
	arCreateRepository(t, "test-project", "us-central1", "expired-cred-repo")

	iamSvc, err := iamadmin.NewService(ctx,
		option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	created, err := iamSvc.Projects.ServiceAccounts.Create("projects/test-project",
		&iamadmin.CreateServiceAccountRequest{AccountId: "expired-cred-sa"}).Do()
	require.NoError(t, err)

	credSvc, err := iamcredentials.NewService(ctx,
		option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	minted, err := credSvc.Projects.ServiceAccounts.GenerateAccessToken(created.Name,
		&iamcredentials.GenerateAccessTokenRequest{
			Scope:    []string{"https://www.googleapis.com/auth/cloud-platform"},
			Lifetime: "1s",
		}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, minted.AccessToken)

	expiry, err := time.Parse(time.RFC3339, minted.ExpireTime)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Second), expiry, 30*time.Second,
		"the mint must honour the one-second lifetime for this to be an expiry test")

	// The token is live right up to its expiry, so wait past it rather than
	// assume: a credential that was never valid would prove nothing here.
	time.Sleep(time.Until(expiry) + 2*time.Second)

	manifestURL := baseURL + "/v2/test-project/expired-cred-repo/app/manifests/latest"
	for _, tc := range []struct {
		name          string
		authorization string
	}{
		{"as a bearer", "Bearer " + minted.AccessToken},
		{"as the password of an oauth2accesstoken basic credential",
			arBasic("oauth2accesstoken", minted.AccessToken)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := arRawDo(t, http.MethodGet, manifestURL, tc.authorization, nil, "")
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
			assert.Empty(t, resp.Header.Get("WWW-Authenticate"))
			code, message := arRegistryErrorOf(t, body)
			assert.Equal(t, "UNAUTHORIZED", code)
			assert.Equal(t, "No valid credential was supplied.", message)
		})
	}
}
