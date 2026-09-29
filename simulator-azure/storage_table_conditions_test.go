package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// tableTestClient addresses one storage account's Table service with a
// Microsoft Entra token for Azure Storage, as a client using a managed
// identity does.
type tableTestClient struct {
	t       *testing.T
	srv     *sim.Server
	account string
	token   string
}

func newTableTestClient(t *testing.T, account string) *tableTestClient {
	t.Helper()
	srv := newMoveTestServer(t)
	token, err := mintAzureSimJWTForUser(EntraUser{OID: "table-test-oid", Sub: "table-test-sub"},
		"table-test-tenant", "https://storage.azure.com/", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("mint storage token: %v", err)
	}
	c := &tableTestClient{t: t, srv: srv, account: account, token: token}
	c.must(http.StatusCreated, http.MethodPost, "/Tables", `{"TableName":"t"}`, nil)
	return c
}

func (c *tableTestClient) do(method, path, body string, headers map[string]string) (int, http.Header, []byte) {
	c.t.Helper()
	host := c.account + ".table.localhost"
	request := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	request.Host = host
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("x-ms-version", "2019-02-02")
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	c.srv.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Header(), recorder.Body.Bytes()
}

func (c *tableTestClient) must(want int, method, path, body string, headers map[string]string) (http.Header, []byte) {
	c.t.Helper()
	status, header, out := c.do(method, path, body, headers)
	if status != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, out)
	}
	return header, out
}

func tableErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"odata.error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body: %v: %s", err, body)
	}
	return e.Error.Code
}

// tableBatchErrorCode reads the error a batch response carries for the
// operation that failed.
func tableBatchErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	if !strings.Contains(string(body), "HTTP/1.1 400") {
		t.Fatalf("batch refusal: %s", body)
	}
	return tableErrorCode(t, body[bytes.IndexByte(body, '{'):bytes.LastIndexByte(body, '}')+1])
}

const tableEntityPath = "/t(PartitionKey='p',RowKey='r')"

func TestTableEntityWritesHonourIfMatch(t *testing.T) {
	c := newTableTestClient(t, "tableifmatch")
	header, _ := c.must(http.StatusNoContent, http.MethodPost, "/t", `{"PartitionKey":"p","RowKey":"r","v":1}`, nil)
	first := header.Get("ETag")
	_, body := c.must(http.StatusConflict, http.MethodPost, "/t", `{"PartitionKey":"p","RowKey":"r","v":9}`, nil)
	if code := tableErrorCode(t, body); code != "EntityAlreadyExists" {
		t.Fatalf("insert over an entity: %s", code)
	}

	header, _ = c.must(http.StatusNoContent, http.MethodPut, tableEntityPath, `{"v":2}`, map[string]string{"If-Match": first})
	second := header.Get("ETag")
	_, body = c.must(http.StatusPreconditionFailed, http.MethodPut, tableEntityPath, `{"v":3}`, map[string]string{"If-Match": first})
	if code := tableErrorCode(t, body); code != "UpdateConditionNotSatisfied" {
		t.Fatalf("stale update: %s", code)
	}
	c.must(http.StatusPreconditionFailed, "MERGE", tableEntityPath, `{"w":1}`, map[string]string{"If-Match": first})
	c.must(http.StatusNoContent, "MERGE", tableEntityPath, `{"w":1}`, map[string]string{"If-Match": "*"})
	c.must(http.StatusNotFound, http.MethodPut, "/t(PartitionKey='p',RowKey='absent')", `{"v":1}`, map[string]string{"If-Match": "*"})
	c.must(http.StatusNoContent, http.MethodPut, "/t(PartitionKey='p',RowKey='upserted')", `{"v":1}`, nil)

	_, body = c.must(http.StatusBadRequest, http.MethodDelete, tableEntityPath, "", nil)
	if code := tableErrorCode(t, body); code != "MissingRequiredHeader" {
		t.Fatalf("delete without If-Match: %s", code)
	}
	c.must(http.StatusPreconditionFailed, http.MethodDelete, tableEntityPath, "", map[string]string{"If-Match": second})
	c.must(http.StatusNoContent, http.MethodDelete, tableEntityPath, "", map[string]string{"If-Match": "*"})
	c.must(http.StatusNotFound, http.MethodGet, tableEntityPath, "", nil)
}

func TestTableConcurrentConditionalUpdatesAdmitOne(t *testing.T) {
	c := newTableTestClient(t, "tableracers")
	header, _ := c.must(http.StatusNoContent, http.MethodPost, "/t", `{"PartitionKey":"p","RowKey":"r","n":0}`, nil)
	etag := header.Get("ETag")
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, _ := c.do(http.MethodPut, tableEntityPath, fmt.Sprintf(`{"n":%d}`, i+1), map[string]string{"If-Match": etag})
			mu.Lock()
			statuses[status]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if statuses[http.StatusNoContent] != 1 || statuses[http.StatusPreconditionFailed] != 7 {
		t.Fatalf("eight updates conditioned on one ETag: %v", statuses)
	}
}

// tableBatchBody builds the multipart/mixed change set aztables posts.
func tableBatchBody(host string, ops ...string) (string, string) {
	var cs bytes.Buffer
	for _, op := range ops {
		method, rest, _ := strings.Cut(op, " ")
		path, body, _ := strings.Cut(rest, " ")
		fmt.Fprintf(&cs, "--changeset_1\r\nContent-Type: application/http\r\nContent-Transfer-Encoding: binary\r\n\r\n")
		fmt.Fprintf(&cs, "%s http://%s%s HTTP/1.1\r\nContent-Type: application/json\r\n", method, host, path)
		if method != http.MethodPost {
			cs.WriteString("If-Match: *\r\n")
		}
		fmt.Fprintf(&cs, "\r\n%s\r\n", body)
	}
	cs.WriteString("--changeset_1--\r\n")
	var batch bytes.Buffer
	fmt.Fprintf(&batch, "--batch_1\r\nContent-Type: multipart/mixed; boundary=changeset_1\r\n\r\n%s--batch_1--\r\n", cs.String())
	return batch.String(), "multipart/mixed; boundary=batch_1"
}

func TestTableBatchIsAtomicPerPartitionAndLeavesOtherWritersAlone(t *testing.T) {
	c := newTableTestClient(t, "tablebatch")
	host := c.account + ".table.localhost"
	c.must(http.StatusNoContent, http.MethodPost, "/t", `{"PartitionKey":"p","RowKey":"existing","v":"kept"}`, nil)
	c.must(http.StatusNoContent, http.MethodPost, "/t", `{"PartitionKey":"q","RowKey":"other","v":1}`, nil)

	body, contentType := tableBatchBody(host,
		`POST /t {"PartitionKey":"p","RowKey":"new","v":1}`,
		`PUT /t(PartitionKey='p',RowKey='existing') {"v":"changed"}`,
		`POST /t {"PartitionKey":"p","RowKey":"existing","v":"dup-insert"}`,
	)
	_, out := c.must(http.StatusAccepted, http.MethodPost, "/$batch", body, map[string]string{"Content-Type": contentType})
	if code := tableBatchErrorCode(t, out); code != "InvalidDuplicateRow" {
		t.Fatalf("an entity twice in one batch: %s", code)
	}

	body, contentType = tableBatchBody(host,
		`POST /t {"PartitionKey":"p","RowKey":"new","v":1}`,
		`PUT /t(PartitionKey='q',RowKey='other') {"v":2}`,
	)
	_, out = c.must(http.StatusAccepted, http.MethodPost, "/$batch", body, map[string]string{"Content-Type": contentType})
	if code := tableBatchErrorCode(t, out); code != "CommandsInBatchActOnDifferentPartitions" {
		t.Fatalf("two partitions in one batch: %s", code)
	}

	body, contentType = tableBatchBody(host,
		`POST /t {"PartitionKey":"p","RowKey":"new","v":1}`,
		`PUT /t(PartitionKey='p',RowKey='existing') {"v":"changed"}`,
		`PUT /t(PartitionKey='p',RowKey='missing') {"v":"fails"}`,
	)
	_, out = c.must(http.StatusAccepted, http.MethodPost, "/$batch", body, map[string]string{"Content-Type": contentType})
	if !strings.Contains(string(out), "HTTP/1.1 404") {
		t.Fatalf("the failed operation's status is the batch's answer: %s", out)
	}
	c.must(http.StatusNotFound, http.MethodGet, "/t(PartitionKey='p',RowKey='new')", "", nil)
	_, got := c.must(http.StatusOK, http.MethodGet, "/t(PartitionKey='p',RowKey='existing')", "", nil)
	if !strings.Contains(string(got), `"kept"`) {
		t.Fatalf("the rolled-back update left: %s", got)
	}
	c.must(http.StatusOK, http.MethodGet, "/t(PartitionKey='q',RowKey='other')", "", nil)

	body, contentType = tableBatchBody(host,
		`POST /t {"PartitionKey":"p","RowKey":"new","v":1}`,
		`DELETE /t(PartitionKey='p',RowKey='existing') `,
	)
	c.must(http.StatusAccepted, http.MethodPost, "/$batch", body, map[string]string{"Content-Type": contentType})
	c.must(http.StatusOK, http.MethodGet, "/t(PartitionKey='p',RowKey='new')", "", nil)
	c.must(http.StatusNotFound, http.MethodGet, "/t(PartitionKey='p',RowKey='existing')", "", nil)
	if held := tablePartitionLocks.Held(); held != 0 {
		t.Fatalf("partition locks left held: %d", held)
	}
}
