package azure_sdk_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storageDataplaneReq points at one of the four storage data-plane
// subdomains (`<account>.{blob,file,queue,table}.<host>`). Tests use
// raw HTTP rather than the Azure Storage SDK because the SDK's
// strict Shared Key signature verifier rejects sim auth-passthrough
// config; raw HTTP exercises the same wire shape.
func storageDataplaneReq(t *testing.T, method, account, service, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var br io.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, br)
	require.NoError(t, err)
	req.Host = account + "." + service + ".localhost"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestFilesDataPlane exercises Files data-plane share + file CRUD.
// Regression guard (Azure storage data planes that
// weren't serviced — the ARM response had advertised these URLs
// fixed Blob; this commit fixes the remaining 3).
func TestFilesDataPlane(t *testing.T) {
	account := "testacct"
	share := "myshare"

	resp := storageDataplaneReq(t, "PUT", account, "file", "/"+share+"?restype=share", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT share")
	resp.Body.Close()

	// Azure Files writes a file in two steps: Create File allocates the byte
	// range x-ms-content-length declares (the request carries no body), then
	// Upload Range fills it. Both are exercised here exactly as the azfile SDK
	// spells them.
	payload := []byte("hello files")
	resp = storageDataplaneReq(t, "PUT", account, "file", "/"+share+"/myfile.txt", nil, map[string]string{
		"Content-Type":        "text/plain",
		"x-ms-type":           "file",
		"x-ms-content-length": fmt.Sprintf("%d", len(payload)),
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT file (Create File)")
	resp.Body.Close()

	resp = storageDataplaneReq(t, "PUT", account, "file", "/"+share+"/myfile.txt?comp=range", payload, map[string]string{
		"x-ms-write": "update",
		"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1),
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT file?comp=range (Upload Range)")
	resp.Body.Close()

	resp = storageDataplaneReq(t, "HEAD", account, "file", "/"+share+"/myfile.txt", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, fmt.Sprintf("%d", len(payload)), resp.Header.Get("Content-Length"))
	resp.Body.Close()

	resp = storageDataplaneReq(t, "GET", account, "file", "/"+share+"/myfile.txt", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, payload, got, "GET file payload round-trip")

	// List files in share.
	resp = storageDataplaneReq(t, "GET", account, "file", "/"+share+"?restype=directory&comp=list", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	listBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(listBody), "myfile.txt")

	// Cleanup.
	resp = storageDataplaneReq(t, "DELETE", account, "file", "/"+share+"/myfile.txt", nil, nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
	resp = storageDataplaneReq(t, "DELETE", account, "file", "/"+share+"?restype=share", nil, nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
}

// TestQueuesDataPlane exercises queue + message enqueue/dequeue/ack.
func TestQueuesDataPlane(t *testing.T) {
	account := "testacct"
	queue := "myqueue"

	resp := storageDataplaneReq(t, "PUT", account, "queue", "/"+queue, nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT queue")
	resp.Body.Close()

	// Enqueue a message — body is XML <QueueMessage><MessageText>...</MessageText></QueueMessage>.
	xmlBody := []byte(`<QueueMessage><MessageText>aGVsbG8=</MessageText></QueueMessage>`)
	resp = storageDataplaneReq(t, "POST", account, "queue", "/"+queue+"/messages", xmlBody, map[string]string{
		"Content-Type": "application/xml",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "POST message")
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	type qmsgResp struct {
		XMLName  xml.Name `xml:"QueueMessagesList"`
		Messages []struct {
			MessageID string `xml:"MessageId"`
		} `xml:"QueueMessage"`
	}
	var msgs qmsgResp
	require.NoError(t, xml.Unmarshal(rb, &msgs))
	require.Len(t, msgs.Messages, 1)
	assert.NotEmpty(t, msgs.Messages[0].MessageID)

	// Peek without dequeue.
	resp = storageDataplaneReq(t, "GET", account, "queue", "/"+queue+"/messages?peekonly=true", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	peekBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(peekBody), "aGVsbG8=")

	// Dequeue (Get Messages).
	resp = storageDataplaneReq(t, "GET", account, "queue", "/"+queue+"/messages?visibilitytimeout=30", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	deqBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var deq qmsgResp
	type fullMsg struct {
		MessageID  string `xml:"MessageId"`
		PopReceipt string `xml:"PopReceipt"`
	}
	type fullList struct {
		XMLName  xml.Name  `xml:"QueueMessagesList"`
		Messages []fullMsg `xml:"QueueMessage"`
	}
	var full fullList
	require.NoError(t, xml.Unmarshal(deqBody, &full))
	require.Len(t, full.Messages, 1)
	assert.NotEmpty(t, full.Messages[0].PopReceipt)
	_ = deq

	// Delete via popreceipt.
	resp = storageDataplaneReq(t, "DELETE", account, "queue",
		fmt.Sprintf("/%s/messages/%s?popreceipt=%s", queue, full.Messages[0].MessageID, full.Messages[0].PopReceipt),
		nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	// Cleanup queue.
	resp = storageDataplaneReq(t, "DELETE", account, "queue", "/"+queue, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
}

// TestTablesDataPlane exercises Tables + entity CRUD.
func TestTablesDataPlane(t *testing.T) {
	account := "testacct"
	table := "MyTable"

	// Create table.
	resp := storageDataplaneReq(t, "POST", account, "table", "/Tables",
		[]byte(`{"TableName":"`+table+`"}`),
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Insert entity.
	resp = storageDataplaneReq(t, "POST", account, "table", "/"+table,
		[]byte(`{"PartitionKey":"p1","RowKey":"r1","Foo":"bar","Num":42}`),
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "POST entity (no-content prefer)")
	resp.Body.Close()

	// Get entity.
	resp = storageDataplaneReq(t, "GET", account, "table",
		"/"+table+"(PartitionKey='p1',RowKey='r1')", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	getBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(getBody), `"Foo":"bar"`)
	assert.Contains(t, string(getBody), `"Num":42`)

	// Query (full-table scan).
	resp = storageDataplaneReq(t, "GET", account, "table", "/"+table, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	queryBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(queryBody), `"value"`)
	assert.Contains(t, string(queryBody), `"Foo":"bar"`)

	// Delete entity.
	resp = storageDataplaneReq(t, "DELETE", account, "table",
		"/"+table+"(PartitionKey='p1',RowKey='r1')", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	// Delete table.
	resp = storageDataplaneReq(t, "DELETE", account, "table", "/Tables('"+table+"')", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	// Final get should 404 with the OData TableNotFound envelope real Azure
	// Tables returns (odata.error.code + x-ms-error-code header).
	resp = storageDataplaneReq(t, "GET", account, "table", "/Tables('"+table+"')", nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "TableNotFound", resp.Header.Get("x-ms-error-code"))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"odata.error"`, "table errors must use the OData envelope: %s", string(body))
	assert.Contains(t, string(body), "TableNotFound", "expected TableNotFound; got %s", string(body))
}

// pathStyleStorageReq issues a request via the Azurite-compatible
// path-style URL: `/{account}/...` (blob), `/queue/{account}/...`,
// `/table/{account}/...`, `/file/{account}/...`. No `.<service>.`
// subdomain. The Azure SDK + azurerm provider both default to this
// shape when the configured endpoint isn't `*.core.windows.net`.
//
// `x-ms-version` is always set — real Azure-Storage clients send it
// on every request, and the sim's path-style dispatcher uses it (and
// the other `x-ms-*` headers / `restype` / `comp` query params) as
// the protocol discriminator that keeps non-storage co-tenants
// (IMDS at `/metadata/...`, Monitor ingest at
// `/dataCollectionRules/...`) routed to their own handlers on the
// shared sim port.
func pathStyleStorageReq(t *testing.T, method, prefix, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var br io.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+prefix+path, br)
	require.NoError(t, err)
	// Carry the ARM bearer so a request that deliberately targets an ARM path
	// (the ARM-prefix-exclusion case) reaches the ARM handler; path-style
	// storage data-plane requests route by their own scheme and ignore it.
	req.Header.Set("Authorization", simARMBearer)
	if _, set := headers["x-ms-version"]; !set {
		req.Header.Set("x-ms-version", "2023-11-03")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestStorageDataPlane_PathStyleBlob verifies the Azurite-compatible
// bare-account form: `PUT /{account}/{container}?restype=container`.
// The dispatcher accepts any non-reserved first path segment as the
// account name (real Azurite is permissive — prior ARM registration
// is NOT required). Reserved segments (`subscriptions`, `providers`,
// `v1`, `v1.NN`, `storage`, etc.) fall through to the normal mux.
func TestStorageDataPlane_PathStyleBlob(t *testing.T) {
	container := "path-style-blob-container"
	// Account is `azurite-style-acct` — never registered via ARM.
	// Real Azurite consumers point `BlobEndpoint=http://localhost:.../<account>`
	// at the data plane directly without going through any control plane.
	account := "azurite-style-acct"

	// Create container via path-style.
	resp := pathStyleStorageReq(t, "PUT", "/"+account, "/"+container+"?restype=container", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT container path-style")
	resp.Body.Close()
	t.Cleanup(func() {
		r := pathStyleStorageReq(t, "DELETE", "/"+account, "/"+container+"?restype=container", nil, nil)
		r.Body.Close()
	})

	// PUT blob via path-style.
	payload := []byte("hello path-style blob")
	resp = pathStyleStorageReq(t, "PUT", "/"+account, "/"+container+"/myblob.txt", payload,
		map[string]string{"x-ms-blob-type": "BlockBlob"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT blob path-style")
	resp.Body.Close()

	// GET blob via path-style.
	resp = pathStyleStorageReq(t, "GET", "/"+account, "/"+container+"/myblob.txt", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET blob path-style")
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, payload, got, "blob payload round-trip")

	// Bonus: confirm host-based and path-style point at the SAME stored
	// blob — both forms retrieve the same bytes.
	resp = storageDataplaneReq(t, "GET", account, "blob", "/"+container+"/myblob.txt", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET blob host-style")
	hostGot, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, payload, hostGot, "host-style retrieval should match path-style PUT")
}

// TestStorageDataPlane_PathStyleFileQueueTable exercises the
// per-service prefix scheme for the non-blob services. `/file/...`,
// `/queue/...`, `/table/...` are sockerless-specific (Azurite uses
// per-port; sim runs on one port, so a path prefix discriminates).
// Like the blob path-style form, no prior ARM registration required.
func TestStorageDataPlane_PathStyleFileQueueTable(t *testing.T) {
	account := "azurite-style-acct"

	// Files.
	share := "path-style-file-share"
	resp := pathStyleStorageReq(t, "PUT", "/file/"+account, "/"+share+"?restype=share", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT share path-style")
	resp.Body.Close()
	t.Cleanup(func() {
		r := pathStyleStorageReq(t, "DELETE", "/file/"+account, "/"+share+"?restype=share", nil, nil)
		r.Body.Close()
	})

	// Queue.
	queue := "path-style-queue"
	resp = pathStyleStorageReq(t, "PUT", "/queue/"+account, "/"+queue, nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "PUT queue path-style")
	resp.Body.Close()
	t.Cleanup(func() {
		r := pathStyleStorageReq(t, "DELETE", "/queue/"+account, "/"+queue, nil, nil)
		r.Body.Close()
	})

	// Table.
	table := "PathStyleTable"
	resp = pathStyleStorageReq(t, "POST", "/table/"+account, "/Tables",
		[]byte(`{"TableName":"`+table+`"}`),
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "POST table path-style")
	resp.Body.Close()
	t.Cleanup(func() {
		r := pathStyleStorageReq(t, "DELETE", "/table/"+account, "/Tables('"+table+"')", nil, nil)
		r.Body.Close()
	})
}

// TestStorageDataPlane_PathStyleARMPrefixExclusion confirms reserved
// path prefixes (ARM `/subscriptions/`, `/providers/`, Docker SDK
// `/v1.44/`, GCP-shaped `/v1/`) do NOT dispatch through the
// path-style storage handler. The path-style dispatcher accepts any
// other first segment as an account name.
func TestStorageDataPlane_PathStyleARMPrefixExclusion(t *testing.T) {
	// ARM-shaped path — should fall through to ARM handler.
	resp := pathStyleStorageReq(t, "GET",
		"/subscriptions/00000000-0000-0000-0000-000000000001",
		"/resourceGroups/test-rg/providers/Microsoft.Web/sites?api-version=2025-03-01",
		nil, nil)
	defer resp.Body.Close()
	// Real ARM path resolves; it should NOT have been dispatched as
	// a storage data-plane request claiming the "subscriptions"
	// "account" doesn't have a container.
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"ARM path must reach the ARM handler (subscriptions is reserved)")
}
