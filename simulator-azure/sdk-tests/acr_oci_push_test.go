package azure_sdk_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acrDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func acrDo(t *testing.T, method, url string, body []byte, contentType string, authorization string) *http.Response {
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

// TestACR_OCIChunkedPush covers POST blob-upload start followed by the chunked
// PATCH/PUT upload and a manifest push + pull round-trip, against a
// multi-segment repository — the whole exchange authenticated with the admin
// credential a `docker login` presents.
func TestACR_OCIChunkedPush(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acrocipushregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})
	auth := fixture.basic(fixture.passwords[0])
	repo := "shim/registry/app"

	resp := acrDo(t, http.MethodGet, fixture.endpoint()+"/v2/", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-API-Version"))
	resp.Body.Close()

	layer := []byte("azure-acr-layer-bytes-content")
	digest := acrDigest(layer)

	resp = acrDo(t, http.MethodPost, fixture.endpoint()+"/v2/"+repo+"/blobs/uploads/", nil, "", auth)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.NotEmpty(t, loc)
	resp.Body.Close()

	resp = acrDo(t, http.MethodPatch, fixture.endpoint()+loc, layer, "application/octet-stream", auth)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	resp = acrDo(t, http.MethodPut, fixture.endpoint()+loc+"?digest="+digest, nil, "", auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"%s","size":%d},"layers":[{"digest":"%s","size":%d}]}`, digest, len(layer), digest, len(layer)))
	resp = acrDo(t, http.MethodPut, fixture.endpoint()+"/v2/"+repo+"/manifests/v1", manifest, "application/vnd.docker.distribution.manifest.v2+json", auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = acrDo(t, http.MethodGet, fixture.endpoint()+"/v2/"+repo+"/manifests/v1", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, manifest, got)

	resp = acrDo(t, http.MethodGet, fixture.endpoint()+"/v2/"+repo+"/blobs/"+digest, nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gotLayer, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, layer, gotLayer)
}
