package aws_sdk_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ociDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ecrCreateRepository creates the repository a registry test addresses: "With
// Amazon ECR, new repositories must be explicitly created before they can be
// used" (Amazon ECR User Guide, "Troubleshooting Amazon ECR error messages",
// HTTP 404: "Repository Does Not Exist" error).
func ecrCreateRepository(t *testing.T, repo string) {
	t.Helper()
	_, err := ecrClient().CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	if err != nil {
		var exists *ecrtypes.RepositoryAlreadyExistsException
		require.ErrorAs(t, err, &exists)
	}
}

// ecrRegistryCredential returns the Basic credential the data plane accepts:
// GetAuthorizationToken serves a base64 `AWS:<password>` that is itself the
// Basic parameter, "-H "Authorization: Basic $TOKEN"" (Amazon ECR User Guide,
// "Using HTTP API authentication").
func ecrRegistryCredential(t *testing.T) string {
	t.Helper()
	out, err := ecrClient().GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.AuthorizationData)
	require.NotNil(t, out.AuthorizationData[0].AuthorizationToken)
	return "Basic " + *out.AuthorizationData[0].AuthorizationToken
}

// ecrRegistryPassword returns the token's password half, which
// `aws ecr get-login-password` prints and `docker login --password-stdin`
// takes.
func ecrRegistryPassword(t *testing.T) string {
	t.Helper()
	out, err := ecrClient().GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.AuthorizationData)
	raw, err := base64.StdEncoding.DecodeString(*out.AuthorizationData[0].AuthorizationToken)
	require.NoError(t, err)
	user, password, ok := strings.Cut(string(raw), ":")
	require.True(t, ok, "authorization token must decode to user:password")
	require.Equal(t, "AWS", user)
	return password
}

func ociDo(t *testing.T, method, url string, body []byte, contentType string) *http.Response {
	t.Helper()
	return ociDoAuthorized(t, method, url, body, contentType, ecrRegistryCredential(t))
}

func ociDoAuthorized(t *testing.T, method, url string, body []byte, contentType, authorization string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestECR_OCIDataPlane pushes a chunked blob and a manifest to a multi-segment
// repository and pulls them back.
func TestECR_OCIDataPlane(t *testing.T) {
	t.Parallel()
	repo := "oci-test-repo/app"
	ecrCreateRepository(t, repo)

	// Captured from `public.ecr.aws/v2/` with a Bearer token from its token
	// service: 200, `content-length: 0`, an empty body and no `content-type`.
	// Docker Distribution answers `{}`, and Amazon ECR is not Docker Distribution.
	resp := ociDo(t, http.MethodGet, baseURL+"/v2/", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-API-Version"))
	pingBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Empty(t, pingBody)
	assert.Equal(t, "0", resp.Header.Get("Content-Length"))
	assert.Empty(t, resp.Header.Get("Content-Type"))

	// A chunked upload: POST to start, PATCH a chunk, PUT to finish.
	layer := []byte("the-image-layer-bytes-go-here")
	digest := ociDigest(layer)

	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.NotEmpty(t, loc)
	require.NotEmpty(t, resp.Header.Get("Docker-Upload-UUID"))
	resp.Body.Close()

	resp = ociDo(t, http.MethodPatch, baseURL+loc, layer, "application/octet-stream")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	resp = ociDo(t, http.MethodPut, baseURL+loc+"?digest="+digest, nil, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, digest, resp.Header.Get("Docker-Content-Digest"))
	resp.Body.Close()

	resp = ociDo(t, http.MethodHead, baseURL+"/v2/"+repo+"/blobs/"+digest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":7,"digest":"%s"},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":%d,"digest":"%s"}]}`, digest, len(layer), digest))
	resp = ociDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1", manifest, "application/vnd.docker.distribution.manifest.v2+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	manifestDigest := resp.Header.Get("Docker-Content-Digest")
	assert.Equal(t, ociDigest(manifest), manifestDigest)
	resp.Body.Close()

	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, manifest, got)

	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/"+manifestDigest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+digest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gotLayer, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, layer, gotLayer)

	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	bad := resp.Header.Get("Location")
	resp.Body.Close()
	resp = ociDo(t, http.MethodPatch, baseURL+bad, layer, "application/octet-stream")
	resp.Body.Close()
	resp = ociDo(t, http.MethodPut, baseURL+bad+"?digest=sha256:0000000000000000000000000000000000000000000000000000000000000000", nil, "")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// TestECR_OCIManifestHeadMissing expects a manifest HEAD for a missing tag to
// answer 404 (MANIFEST_UNKNOWN): go-containerregistry reads 404 as "absent,
// upload" and aborts on any other 4xx. Every /v2/ response, this 404 included,
// carries Docker-Distribution-Api-Version.
func TestECR_OCIManifestHeadMissing(t *testing.T) {
	t.Parallel()
	repo := uniquename.New("shim/registry")
	ecrCreateRepository(t, repo)
	const apiVersionHeader = "Docker-Distribution-Api-Version"

	resp := ociDo(t, http.MethodHead, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"manifest HEAD for a missing tag must be 404, not 400")
	assert.Equal(t, "registry/2.0", resp.Header.Get(apiVersionHeader),
		"the missing-manifest response must carry Docker-Distribution-Api-Version")
	resp.Body.Close()

	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), "MANIFEST_UNKNOWN")

	layer := []byte("head-probe-layer")
	digest := ociDigest(layer)
	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	resp = ociDo(t, http.MethodPatch, baseURL+loc, layer, "application/octet-stream")
	resp.Body.Close()
	resp = ociDo(t, http.MethodPut, baseURL+loc+"?digest="+digest, nil, "")
	resp.Body.Close()
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":7,"digest":"%s"},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":%d,"digest":"%s"}]}`, digest, len(layer), digest))
	resp = ociDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1", manifest, "application/vnd.docker.distribution.manifest.v2+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = ociDo(t, http.MethodHead, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "registry/2.0", resp.Header.Get(apiVersionHeader))
	resp.Body.Close()
}

// TestECR_RegistryDataPlaneAuthentication expects the data plane to accept the
// credential GetAuthorizationToken mints and refuse every other with Amazon
// ECR's Basic challenge
//
//	Www-Authenticate: Basic realm="https://<registry>/",service="ecr.amazonaws.com"
//
// (observed against a private registry in NVIDIA/enroot#59) and the plain-text
// `Not Authorized` body go-containerregistry renders as
// `unexpected status code 401 Unauthorized: Not Authorized`
// (google/go-containerregistry#861).
func TestECR_RegistryDataPlaneAuthentication(t *testing.T) {
	t.Parallel()
	repo := "ecr-auth/app"
	ecrCreateRepository(t, repo)
	credential := ecrRegistryCredential(t)

	for _, probe := range []struct {
		name          string
		authorization string
	}{
		{"no credential", ""},
		{"wrong password", "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:not-the-password"))},
		{"user other than AWS", "Basic " + base64.StdEncoding.EncodeToString([]byte("root:"+ecrRegistryPassword(t)))},
		{"bearer token", "Bearer " + ecrRegistryPassword(t)},
	} {
		for _, route := range []string{"/v2/", "/v2/" + repo + "/manifests/v1", "/v2/" + repo + "/tags/list"} {
			resp := ociDoAuthorized(t, http.MethodGet, baseURL+route, nil, "", probe.authorization)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
				"%s must be refused at %s", probe.name, route)
			assert.Equal(t,
				`Basic realm="`+baseURL+`/",service="ecr.amazonaws.com"`,
				resp.Header.Get("Www-Authenticate"),
				"the refusal must carry Amazon ECR's Basic challenge")
			assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
			assert.Equal(t, "Not Authorized", strings.TrimSpace(string(body)))
		}
	}

	// A blob upload is refused before it allocates an upload slot.
	resp := ociDoAuthorized(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "", "")
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Docker-Upload-UUID"),
		"a refused upload must not allocate an upload")

	resp = ociDoAuthorized(t, http.MethodGet, baseURL+"/v2/", nil, "", credential)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	layer := []byte("ecr-authenticated-layer")
	digest := ociDigest(layer)
	resp = ociDoAuthorized(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "", credential)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	resp = ociDoAuthorized(t, http.MethodPatch, baseURL+loc, layer, "application/octet-stream", credential)
	resp.Body.Close()
	resp = ociDoAuthorized(t, http.MethodPut, baseURL+loc+"?digest="+digest, nil, "", credential)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":%d,"digest":"%s"},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":%d,"digest":"%s"}]}`,
		len(layer), digest, len(layer), digest))
	resp = ociDoAuthorized(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1", manifest,
		"application/vnd.docker.distribution.manifest.v2+json", credential)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// An independently issued token authenticates too: the credential is the IAM
	// principal's, not one shared secret.
	other := ecrRegistryCredential(t)
	require.NotEqual(t, credential, other, "each GetAuthorizationToken call mints its own token")
	resp = ociDoAuthorized(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "", other)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, manifest, got)

	// Without it the same pull is refused.
	resp = ociDoAuthorized(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "", "")
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
