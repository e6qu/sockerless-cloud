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

// TestArtifactRegistry_OCIChunkedPush covers the OCI Distribution
// chunked blob upload (POST → PATCH → PUT) that previously 405'd on PATCH, then
// a manifest push + pull round-trip.
func TestArtifactRegistry_OCIChunkedPush(t *testing.T) {
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

	// PATCH chunk — the operation that previously returned 405.
	resp = arDo(t, http.MethodPatch, baseURL+loc, layer, "application/octet-stream")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

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
