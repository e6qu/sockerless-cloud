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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ociDigest computes the registry digest of content.
func ociDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ecrCreateRepository creates the repository a registry test addresses. Amazon
// ECR repositories are explicit resources — "With Amazon ECR, new repositories
// must be explicitly created before they can be used" (Amazon ECR User Guide,
// "Troubleshooting Amazon ECR error messages" § HTTP 404: "Repository Does Not
// Exist" error) — so a client creates one through the control plane before its
// first push, exactly as the documented push flow instructs.
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

// ecrRegistryCredential returns the Basic credential the Amazon ECR data plane
// authenticates, obtained the documented way: GetAuthorizationToken serves a
// base64 `AWS:<password>` string that is itself the Basic parameter — "you must
// provide an authorization token with every HTTP request … -H "Authorization:
// Basic $TOKEN"" (Amazon ECR User Guide, "Using HTTP API authentication").
func ecrRegistryCredential(t *testing.T) string {
	t.Helper()
	out, err := ecrClient().GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.AuthorizationData)
	require.NotNil(t, out.AuthorizationData[0].AuthorizationToken)
	return "Basic " + *out.AuthorizationData[0].AuthorizationToken
}

// ecrRegistryPassword returns the password half of the authorization token —
// what `aws ecr get-login-password` prints and `docker login --password-stdin`
// consumes.
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

// TestECR_OCIDataPlane covers the Docker Registry /v2/ data plane —
// a full chunked blob push + manifest push, then pull, against an ECR
// multi-segment repository.
func TestECR_OCIDataPlane(t *testing.T) {
	repo := "oci-test-repo/app"
	ecrCreateRepository(t, repo)

	// Base / version route.
	resp := ociDo(t, http.MethodGet, baseURL+"/v2/", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-API-Version"))
	resp.Body.Close()

	// Chunked blob upload: POST (init) → PATCH (chunk) → PUT (finalize).
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

	// Blob is now present.
	resp = ociDo(t, http.MethodHead, baseURL+"/v2/"+repo+"/blobs/"+digest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Push a manifest referencing the layer.
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":7,"digest":"%s"},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":%d,"digest":"%s"}]}`, digest, len(layer), digest))
	resp = ociDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1", manifest, "application/vnd.docker.distribution.manifest.v2+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	manifestDigest := resp.Header.Get("Docker-Content-Digest")
	assert.Equal(t, ociDigest(manifest), manifestDigest)
	resp.Body.Close()

	// Pull it back by tag.
	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, manifest, got)

	// And by digest.
	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/"+manifestDigest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Pull the layer blob back.
	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+digest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gotLayer, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, layer, gotLayer)

	// A digest that doesn't match the content is rejected.
	resp = ociDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	bad := resp.Header.Get("Location")
	resp.Body.Close()
	resp = ociDo(t, http.MethodPatch, baseURL+bad, layer, "application/octet-stream")
	resp.Body.Close()
	resp = ociDo(t, http.MethodPut, baseURL+bad+"?digest=sha256:0000000000000000000000000000000000000000000000000000000000000000", nil, "")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// TestECR_OCIManifestHeadMissing locks the push-client probe shape: a manifest
// HEAD for a tag that doesn't exist must return 404 (MANIFEST_UNKNOWN), never a
// generic 400 — an OCI client (go-containerregistry) treats 404 as "absent,
// proceed to upload" but aborts on 4xx that isn't 404. Every /v2/ response,
// including this 404, also carries Docker-Distribution-Api-Version (real
// registries set it on all responses, not just the base ping).
func TestECR_OCIManifestHeadMissing(t *testing.T) {
	repo := "shim/registry"
	ecrCreateRepository(t, repo)
	const apiVersionHeader = "Docker-Distribution-Api-Version"

	// Missing tag in a not-yet-populated repo → 404 with the registry header.
	resp := ociDo(t, http.MethodHead, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"manifest HEAD for a missing tag must be 404, not 400")
	assert.Equal(t, "registry/2.0", resp.Header.Get(apiVersionHeader),
		"the missing-manifest response must carry Docker-Distribution-Api-Version")
	resp.Body.Close()

	// GET for the same missing tag → 404 with the MANIFEST_UNKNOWN envelope.
	resp = ociDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), "MANIFEST_UNKNOWN")

	// Push the tag, then the same HEAD probe now reports it present (200).
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

// TestECR_RegistryDataPlaneAuthentication drives the Amazon ECR private-registry
// credential contract end to end through the SDK: GetAuthorizationToken mints
// the credential, the /v2/ data plane accepts it, and every other credential is
// refused with the Basic challenge Amazon ECR publishes.
//
//	Www-Authenticate: Basic realm="https://<registry>/",service="ecr.amazonaws.com"
//
// (observed against a real private registry in NVIDIA/enroot#59), and the
// plain-text `Not Authorized` body that go-containerregistry renders as
// `unexpected status code 401 Unauthorized: Not Authorized`
// (google/go-containerregistry#861).
func TestECR_RegistryDataPlaneAuthentication(t *testing.T) {
	repo := "ecr-auth/app"
	ecrCreateRepository(t, repo)
	credential := ecrRegistryCredential(t)

	// Every credential the registry does not accept is refused, on the base
	// endpoint and on a repository route alike.
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

	// A blob upload is refused before it can allocate an upload slot.
	resp := ociDoAuthorized(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "", "")
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Docker-Upload-UUID"),
		"a refused upload must not allocate an upload")

	// The credential GetAuthorizationToken issued reaches the whole data plane.
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

	// A second, independently issued token authenticates too — the credential
	// is the IAM principal's, not one shared secret.
	other := ecrRegistryCredential(t)
	require.NotEqual(t, credential, other, "each GetAuthorizationToken call mints its own token")
	resp = ociDoAuthorized(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "", other)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, manifest, got)

	// And the same pull without it is refused, so the pushed manifest is not
	// readable by an unauthenticated caller.
	resp = ociDoAuthorized(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "", "")
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
