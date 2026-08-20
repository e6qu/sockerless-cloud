package gcp_sdk_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func arDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func arDo(t *testing.T, method, url string, body []byte, contentType string) *http.Response {
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
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestArtifactRegistry_OCIMonolithicPush covers the blob upload Artifact
// Registry serves and refuses the one it does not.
//
// Google is explicit: "Artifact Registry doesn't support Docker chunked
// uploads. … You must use monolithic uploads when you push container images to
// Artifact Registry" ("Support for the Docker Registry API"). The line is
// between one write into an upload session and several, not between PATCH and
// PUT: a container engine's `docker push` opens with POST, sends the whole blob
// in a single PATCH and finalizes with PUT, and that push succeeds against the
// live service. Refusing the first write broke a real `docker push` in CI while
// passing on a host whose engine sent the blob on the PUT instead.
//
// The requests carry the access token the suite's shared transport attaches, so
// the whole exchange is authenticated the way Artifact Registry requires; the
// repository is created through the control plane first because the data plane
// refuses a repository that does not exist.
func TestArtifactRegistry_OCIMonolithicPush(t *testing.T) {
	arCreateRepository(t, "test-project", "us-central1", "my-repo")
	repo := "test-project/my-repo/app"

	resp := arDo(t, http.MethodGet, baseURL+"/v2/", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	layer := []byte("gcp-artifact-registry-layer-bytes")
	digest := arDigest(layer)

	resp = arDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.NotEmpty(t, loc)
	resp.Body.Close()

	// The whole blob in one write, which is what an engine sends.
	resp = arDo(t, http.MethodPatch, baseURL+loc, layer, "application/octet-stream")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// A second write into the same session is the chunking Artifact Registry
	// does not support, and it is refused.
	resp = arDo(t, http.MethodPatch, baseURL+loc, []byte("a-second-chunk"), "application/octet-stream")
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	refusal, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Contains(t, string(refusal), "UNSUPPORTED")
	assert.Contains(t, string(refusal), "monolithic")

	// The refused chunk left nothing behind: the blob the session finalizes is
	// the one write it accepted.
	resp = arDo(t, http.MethodPut, baseURL+loc+"?digest="+digest, nil, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"%s","size":%d},"layers":[{"digest":"%s","size":%d}]}`, digest, len(layer), digest, len(layer)))
	resp = arDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1", manifest, "application/vnd.oci.image.manifest.v1+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = arDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, manifest, got)

	resp = arDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+digest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gotLayer, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, layer, gotLayer)
}
