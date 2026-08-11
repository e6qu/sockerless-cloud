package azure_sdk_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blobURL constructs a data-plane URL pointing at the
// `{account}.blob.<sim-host>` subdomain. Tests use raw HTTP rather
// than the Azure Storage SDK because the SDK's data-plane client
// expects a real Storage account URL with valid SAS / Shared Key
// auth, and the sim's auth-passthrough makes the SDK's strict
// signature verifier reject the canonical config — out of scope
// for the first cut. The raw-HTTP layer here exercises the same
// wire shape the SDK would emit.
func blobURL(account, path string) string {
	// baseURL is "http://127.0.0.1:<port>"; the data-plane dispatch
	// uses the request Host header. Rewriting the Host header on
	// a localhost-routed HTTP request gives the same routing the
	// SDK would trigger when DNS resolves <account>.blob.<host>
	// to 127.0.0.1.
	return baseURL + path
}

func blobReq(t *testing.T, method, account, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var br io.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, blobURL(account, path), br)
	require.NoError(t, err)
	// Force the dispatch by setting the Host header to the
	// advertised `{account}.blob.<sim-host>` form. The sim's
	// WrapHandler reads r.Host, splits on ".blob.", and routes
	// to the data plane.
	req.Host = account + ".blob.localhost"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestBlobDataPlane_RoundTrip exercises container + blob CRUD
// through the `<account>.blob.<host>` subdomain dispatch — the ARM
// `storageAccounts` response advertises this exact endpoint URL, so
// the data plane it points at must serve it.
func TestBlobDataPlane_RoundTrip(t *testing.T) {
	account := "testaccount"
	container := "mycontainer"

	// CreateContainer.
	resp := blobReq(t, "PUT", account, "/"+container+"?restype=container", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"PUT container should return 201 Created")
	resp.Body.Close()

	// PutBlob.
	payload := []byte("hello blob")
	resp = blobReq(t, "PUT", account, "/"+container+"/myblob", payload, map[string]string{
		"x-ms-blob-type": "BlockBlob",
		"Content-Type":   "text/plain",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("ETag"))
	require.NotEmpty(t, resp.Header.Get("Content-MD5"))
	resp.Body.Close()

	// HeadBlob — metadata-only, no body.
	resp = blobReq(t, "HEAD", account, "/"+container+"/myblob", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "BlockBlob", resp.Header.Get("x-ms-blob-type"))
	require.Equal(t, fmt.Sprintf("%d", len(payload)), resp.Header.Get("Content-Length"))
	resp.Body.Close()

	// GetBlob — body should equal the original payload.
	resp = blobReq(t, "GET", account, "/"+container+"/myblob", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, payload, got,
		"Blob data should round-trip; getting back the original bytes")

	// ListBlobs (XML).
	resp = blobReq(t, "GET", account, "/"+container+"?restype=container&comp=list", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), "<Name>myblob</Name>",
		"ListBlobs XML should include the blob")

	// DeleteBlob + DeleteContainer.
	resp = blobReq(t, "DELETE", account, "/"+container+"/myblob", nil, nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
	resp = blobReq(t, "DELETE", account, "/"+container+"?restype=container", nil, nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
}

func TestBlobDataPlane_ListContainersProperties(t *testing.T) {
	account := "listpropsacct"
	container := "probe"

	resp := blobReq(t, "PUT", account, "/"+container+"?restype=container", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	createdETag := resp.Header.Get("ETag")
	createdLastModified := resp.Header.Get("Last-Modified")
	resp.Body.Close()
	require.NotEmpty(t, createdETag)
	require.NotEmpty(t, createdLastModified)

	resp = blobReq(t, "GET", account, "/"+container+"?restype=container", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, createdETag, resp.Header.Get("ETag"))
	assert.Equal(t, createdLastModified, resp.Header.Get("Last-Modified"))
	resp.Body.Close()

	resp = blobReq(t, "GET", account, "/?comp=list", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	type listContainersResponse struct {
		Containers []struct {
			Name       string `xml:"Name"`
			Properties struct {
				LastModified string `xml:"Last-Modified"`
				ETag         string `xml:"Etag"`
			} `xml:"Properties"`
		} `xml:"Containers>Container"`
	}
	var got listContainersResponse
	require.NoError(t, xml.Unmarshal(body, &got))
	require.Len(t, got.Containers, 1)
	require.Equal(t, container, got.Containers[0].Name)
	require.Equal(t, createdLastModified, got.Containers[0].Properties.LastModified)
	require.Equal(t, createdETag, got.Containers[0].Properties.ETag)
	_, err := time.Parse(http.TimeFormat, got.Containers[0].Properties.LastModified)
	require.NoError(t, err)
}

// TestBlobDataPlane_404OnMissingContainer locks in the
// "advertised-endpoint must be serviced" invariant: a GET against a
// missing container on a non-existent account returns a real 404
// from the Blob data-plane handler (not a mux fall-through to a
// generic 404 page).
func TestBlobDataPlane_404OnMissingContainer(t *testing.T) {
	resp := blobReq(t, "GET", "noaccount", "/nope?restype=container", nil, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"missing container should produce 404; body: %s", string(body))
	assert.True(t, strings.Contains(string(body), "ContainerNotFound") ||
		strings.Contains(string(body), "does not exist"),
		"error body should mention ContainerNotFound; got %s", string(body))
}

// kvReq points at the KV data-plane subdomain ({vault}.vault.<host>).
// The Bearer header is required to clear the sim's WWW-Authenticate
// challenge that real Azure KV issues on unauthenticated probes
// (the Azure SDK does a challenge-then-retry token dance). Raw HTTP
// tests skip the dance by pre-setting the header.
func kvReq(t *testing.T, method, vault, path string, body []byte) *http.Response {
	t.Helper()
	var br io.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, br)
	require.NoError(t, err)
	req.Host = vault + ".vault.localhost"
	req.Header.Set("Authorization", simARMBearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestKeyVault_KeysAndCertificates exercises the keys + certificates
// data-plane surfaces (CreateKey / GetKey / ListKeys /
// CreateCertificate / GetCertificate) on a vault registered through
// ARM. Locks the symmetric "ARM-creates → data-plane-serves" wiring
// the secrets handler already covered.
func TestKeyVault_KeysAndCertificates(t *testing.T) {
	// Vault must exist in the ARM control plane first.
	vault := "testvault"
	armPath := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg1/providers/Microsoft.KeyVault/vaults/" + vault
	armReq, err := http.NewRequest("PUT", baseURL+armPath, strings.NewReader(`{"location":"eastus","properties":{"tenantId":"t","sku":{"name":"standard"}}}`))
	require.NoError(t, err)
	armReq.Header.Set("Content-Type", "application/json")
	armResp, err := http.DefaultClient.Do(armReq)
	require.NoError(t, err)
	armResp.Body.Close()

	// Create a key.
	keyBody := `{"kty":"RSA","key_size":2048}`
	resp := kvReq(t, "POST", vault, "/keys/mykey/create", []byte(keyBody))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var keyResp map[string]any
	require.NoError(t, json.Unmarshal(body, &keyResp))
	_, hasTopKid := keyResp["kid"]
	assert.False(t, hasTopKid, "KeyBundle must not hoist kid to the top level")
	jwk, _ := keyResp["key"].(map[string]any)
	require.NotNil(t, jwk, "KeyBundle must carry the JSON Web Key under key")
	kid, _ := jwk["kid"].(string)
	assert.Contains(t, kid, "/keys/mykey/", "key.kid should embed the key path")

	// Get the key back.
	resp = kvReq(t, "GET", vault, "/keys/mykey", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// List keys.
	resp = kvReq(t, "GET", vault, "/keys", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"value"`)

	// Delete the key.
	resp = kvReq(t, "DELETE", vault, "/keys/mykey", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Create a certificate.
	certBody := `{"policy":{"x509_props":{"subject":"CN=test"}}}`
	resp = kvReq(t, "POST", vault, "/certificates/mycert/create", []byte(certBody))
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"id"`, "cert response should carry an id")
	assert.Contains(t, string(body), `"status"`, "cert create should return a CertificateOperation")

	resp = kvReq(t, "GET", vault, "/certificates/mycert/pending", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = kvReq(t, "GET", vault, "/certificates/mycert", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	resp = kvReq(t, "DELETE", vault, "/certificates/mycert", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// TestKeyVault_AuthChallenge asserts the WWW-Authenticate: Bearer
// challenge response on unauthenticated probes. The Azure SDK's KV
// clients do challenge-then-retry: first request has no
// Authorization, SDK parses WWW-Authenticate to discover the AAD
// issuer + resource scope, then fetches a token and retries. The
// sim must issue the 401 + challenge for any SDK consumer to work.
func TestKeyVault_AuthChallenge(t *testing.T) {
	vault := "challengevault"
	armPath := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg1/providers/Microsoft.KeyVault/vaults/" + vault
	armReq, err := http.NewRequest("PUT", baseURL+armPath,
		strings.NewReader(`{"location":"eastus","properties":{"tenantId":"t","sku":{"name":"standard"}}}`))
	require.NoError(t, err)
	armReq.Header.Set("Content-Type", "application/json")
	armReq.Header.Set("Authorization", simARMBearer)
	armResp, err := http.DefaultClient.Do(armReq)
	require.NoError(t, err)
	armResp.Body.Close()

	// Probe without Authorization — must get 401 + WWW-Authenticate.
	req, err := http.NewRequest("PUT", baseURL+"/secrets/probe?api-version=7.6",
		strings.NewReader(`{"value":"v"}`))
	require.NoError(t, err)
	req.Host = vault + ".vault.localhost"
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"unauthenticated KV probe must 401 (real Azure does)")
	challenge := resp.Header.Get("WWW-Authenticate")
	require.NotEmpty(t, challenge, "401 must carry WWW-Authenticate header")
	assert.Contains(t, challenge, "Bearer ", "challenge must use Bearer scheme")
	assert.Contains(t, challenge, `authorization=`, "challenge must announce authorization URL")
	assert.Contains(t, challenge, `resource="https://vault.azure.net"`,
		"challenge must announce the KV resource scope")

	// Same request with Authorization — must succeed (challenge cleared).
	req2, err := http.NewRequest("PUT", baseURL+"/secrets/probe?api-version=7.6",
		strings.NewReader(`{"value":"v"}`))
	require.NoError(t, err)
	req2.Host = vault + ".vault.localhost"
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", simARMBearer)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode,
		"authenticated KV request must succeed")
}
