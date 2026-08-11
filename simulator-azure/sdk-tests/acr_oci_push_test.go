package azure_sdk_test

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

func acrDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func acrDo(t *testing.T, method, url string, body []byte, contentType string) *http.Response {
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

// TestACR_OCIChunkedPush covers POST blob-upload start (which
// previously 404'd) followed by the chunked PATCH/PUT upload and a manifest
// push + pull round-trip, against a multi-segment repository.
func TestACR_OCIChunkedPush(t *testing.T) {
	repo := "shim/registry/app"

	resp := acrDo(t, http.MethodGet, baseURL+"/v2/", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-API-Version"))
	resp.Body.Close()

	layer := []byte("azure-acr-layer-bytes-content")
	digest := acrDigest(layer)

	// POST init — previously 404 Not Found.
	resp = acrDo(t, http.MethodPost, baseURL+"/v2/"+repo+"/blobs/uploads/", nil, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.NotEmpty(t, loc)
	resp.Body.Close()

	resp = acrDo(t, http.MethodPatch, baseURL+loc, layer, "application/octet-stream")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	resp = acrDo(t, http.MethodPut, baseURL+loc+"?digest="+digest, nil, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"%s","size":%d},"layers":[{"digest":"%s","size":%d}]}`, digest, len(layer), digest, len(layer)))
	resp = acrDo(t, http.MethodPut, baseURL+"/v2/"+repo+"/manifests/v1", manifest, "application/vnd.docker.distribution.manifest.v2+json")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = acrDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/manifests/v1", nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, manifest, got)

	resp = acrDo(t, http.MethodGet, baseURL+"/v2/"+repo+"/blobs/"+digest, nil, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gotLayer, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, layer, gotLayer)
}
