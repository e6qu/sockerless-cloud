package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cosmosItemsClient drives one account's data plane with signed requests,
// optionally carrying extra headers, as an SDK does.
type cosmosItemsClient struct {
	t       *testing.T
	srv     *sim.Server
	account string
	key     string
}

func newCosmosItemsClient(t *testing.T, account string) *cosmosItemsClient {
	t.Helper()
	srv := newMoveTestServer(t)
	keys := cosmosProvisionAccount(t, srv, account)
	return &cosmosItemsClient{t: t, srv: srv, account: account, key: keys.PrimaryMasterKey}
}

func (c *cosmosItemsClient) do(method, path, body string, headers map[string]string) (int, http.Header, []byte) {
	c.t.Helper()
	request := cosmosDataPlaneHTTPRequest(c.t, method, path, c.account, body)
	date := time.Now().UTC().Format(http.TimeFormat)
	request.Header.Set("x-ms-date", date)
	resourceType, resourceLink := cosmosClientResource(path)
	request.Header.Set("Authorization", cosmosAuthorizationHeader(c.t, c.key,
		cosmosStringToSign(method, resourceType, resourceLink, date)))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	c.srv.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Header(), recorder.Body.Bytes()
}

func (c *cosmosItemsClient) must(want int, method, path, body string, headers map[string]string) (http.Header, []byte) {
	c.t.Helper()
	status, header, out := c.do(method, path, body, headers)
	if status != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, out)
	}
	return header, out
}

// cosmosFixedClock pins the clock expiry and throughput are judged by.
func cosmosFixedClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	previous := cosmosNow
	cosmosNow = func() time.Time { return now }
	t.Cleanup(func() { cosmosNow = previous })
	return &now
}

var pkHeader = map[string]string{"x-ms-documentdb-partitionkey": `["p"]`}

func TestCosmosProvisionedThroughputAnswers429WithRetryAfter(t *testing.T) {
	c := newCosmosItemsClient(t, "throttlecosmos")
	now := cosmosFixedClock(t)
	c.must(http.StatusCreated, http.MethodPost, "/dbs", `{"id":"db"}`, nil)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, map[string]string{"x-ms-offer-throughput": "400"})

	var throttled http.Header
	written := 0
	for i := range 200 {
		status, header, body := c.do(http.MethodPost, "/dbs/db/colls/c/docs", fmt.Sprintf(`{"id":"d%d","pk":"p"}`, i), pkHeader)
		if status == http.StatusTooManyRequests {
			throttled = header
			var e map[string]any
			if err := json.Unmarshal(body, &e); err != nil || e["code"] != "TooManyRequests" {
				t.Fatalf("429 body: %s", body)
			}
			break
		}
		if status != http.StatusCreated {
			t.Fatalf("write %d: status %d: %s", i, status, body)
		}
		written++
	}
	if throttled == nil {
		t.Fatal("400 RU/s never throttled 200 writes in one second")
	}
	if written*5 < 400 {
		t.Fatalf("throttled after %d writes: the budget covers 400 RU of 5 RU writes", written)
	}
	if throttled.Get("x-ms-substatus") != "3200" {
		t.Fatalf("substatus: %q", throttled.Get("x-ms-substatus"))
	}
	wait, err := strconv.Atoi(throttled.Get("x-ms-retry-after-ms"))
	if err != nil || wait <= 0 {
		t.Fatalf("x-ms-retry-after-ms: %q", throttled.Get("x-ms-retry-after-ms"))
	}
	if status, _, _ := c.do(http.MethodGet, "/dbs/db/colls/c/docs/d0", "", pkHeader); status != http.StatusTooManyRequests {
		t.Fatalf("a read against the exhausted budget: status %d", status)
	}
	*now = now.Add(time.Duration(wait) * time.Millisecond)
	c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c/docs/d0", "", pkHeader)

	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls",
		`{"id":"shared","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	for i := range 200 {
		c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/shared/docs", fmt.Sprintf(`{"id":"d%d","pk":"p"}`, i), pkHeader)
	}
}

func TestCosmosTimeToLiveHidesThenDeletesExpiredItems(t *testing.T) {
	c := newCosmosItemsClient(t, "ttlcosmos")
	now := cosmosFixedClock(t)
	c.must(http.StatusCreated, http.MethodPost, "/dbs", `{"id":"db"}`, nil)
	c.must(http.StatusBadRequest, http.MethodPost, "/dbs/db/colls",
		`{"id":"bad","partitionKey":{"paths":["/pk"]},"defaultTtl":0}`, nil)
	_, body := c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"},"defaultTtl":10}`, nil)
	var coll map[string]any
	if err := json.Unmarshal(body, &coll); err != nil || coll["defaultTtl"] != float64(10) {
		t.Fatalf("created container: %s", body)
	}
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"short","pk":"p"}`, pkHeader)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"forever","pk":"p","ttl":-1}`, pkHeader)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"long","pk":"p","ttl":60}`, pkHeader)

	*now = now.Add(11 * time.Second)
	c.must(http.StatusNotFound, http.MethodGet, "/dbs/db/colls/c/docs/short", "", pkHeader)
	c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c/docs/forever", "", pkHeader)
	c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c/docs/long", "", pkHeader)
	_, body = c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c/docs", "", nil)
	var list struct{ Documents []map[string]any }
	if err := json.Unmarshal(body, &list); err != nil || len(list.Documents) != 2 {
		t.Fatalf("listing after expiry: %s", body)
	}
	if _, ok := cosmosDocs.Get(cosmosDocKeyPK("ttlcosmos", "db", "c", cosmosCanonPKValue("p"), "short")); !ok {
		t.Fatal("the expired item is hidden before the sweep deletes it")
	}
	if n := cosmosSweepExpired(*now); n != 1 {
		t.Fatalf("sweep deleted %d items, want 1", n)
	}
	if _, ok := cosmosDocs.Get(cosmosDocKeyPK("ttlcosmos", "db", "c", cosmosCanonPKValue("p"), "short")); ok {
		t.Fatal("the sweep left the expired item stored")
	}
	changes := cosmosChanges.Read(cosmosDataCollKey("ttlcosmos", "db", "c"), 0, 0)
	last := changes[len(changes)-1]
	if last.Previous == nil || last.Previous.ID != "short" || !last.Expired {
		t.Fatalf("the expiry is a delete the change log marks as time to live: %+v", last)
	}
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"short","pk":"p"}`, pkHeader)
}

func TestCosmosChangeFeedModes(t *testing.T) {
	c := newCosmosItemsClient(t, "feedcosmos")
	c.must(http.StatusCreated, http.MethodPost, "/dbs", `{"id":"db"}`, nil)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	allVersions := map[string]string{"A-IM": "Full-Fidelity Feed", "If-None-Match": "*"}
	c.must(http.StatusBadRequest, http.MethodGet, "/dbs/db/colls/c/docs", "", allVersions)

	status, body := moveARM(t, c.srv, http.MethodPut, cosmosTestAccountPath("feedcosmos"),
		`{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard","backupPolicy":{"type":"Continuous","continuousModeProperties":{"tier":"Continuous7Days"}}}}`)
	if status != http.StatusOK {
		t.Fatalf("enable continuous backup: %d: %s", status, body)
	}
	c.must(http.StatusBadRequest, http.MethodGet, "/dbs/db/colls/c/docs", "", map[string]string{"A-IM": "Full-Fidelity Feed"})
	header, _ := c.must(http.StatusNotModified, http.MethodGet, "/dbs/db/colls/c/docs", "", allVersions)
	start := header.Get("etag")

	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"a","pk":"p","v":1}`, pkHeader)
	c.must(http.StatusOK, http.MethodPut, "/dbs/db/colls/c/docs/a", `{"id":"a","pk":"p","v":2}`, pkHeader)
	c.must(http.StatusNoContent, http.MethodDelete, "/dbs/db/colls/c/docs/a", "", pkHeader)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"b","pk":"p"}`, pkHeader)

	_, body = c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c/docs", "",
		map[string]string{"A-IM": "Full-Fidelity Feed", "If-None-Match": start})
	var feed struct{ Documents []map[string]any }
	if err := json.Unmarshal(body, &feed); err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, entry := range feed.Documents {
		metadata := entry["metadata"].(map[string]any)
		ops = append(ops, fmt.Sprint(metadata["operationType"]))
	}
	if fmt.Sprint(ops) != "[create replace delete create]" {
		t.Fatalf("all versions and deletes: %v in %s", ops, body)
	}
	deleted := feed.Documents[2]
	if _, hasCurrent := deleted["current"]; hasCurrent {
		t.Fatalf("a delete carries no current item: %v", deleted)
	}
	metadata := deleted["metadata"].(map[string]any)
	if metadata["id"] != "a" || fmt.Sprint(metadata["partitionKey"]) != "map[pk:p]" || metadata["timeToLiveExpired"] != false {
		t.Fatalf("delete metadata: %v", metadata)
	}
	if feed.Documents[1]["current"].(map[string]any)["v"] != float64(2) {
		t.Fatalf("replace carries the new version: %v", feed.Documents[1])
	}

	_, body = c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c/docs", "", map[string]string{"A-IM": "Incremental feed"})
	var latest struct{ Documents []map[string]any }
	if err := json.Unmarshal(body, &latest); err != nil || len(latest.Documents) != 1 || latest.Documents[0]["id"] != "b" {
		t.Fatalf("latest version omits the deleted item: %s", body)
	}
}

func TestCosmosWritesToOneItemSerializeOnItsETag(t *testing.T) {
	c := newCosmosItemsClient(t, "etagcosmos")
	c.must(http.StatusCreated, http.MethodPost, "/dbs", `{"id":"db"}`, nil)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	c.must(http.StatusNotFound, http.MethodPut, "/dbs/db/colls/c/docs/missing", `{"id":"missing","pk":"p"}`, pkHeader)
	header, _ := c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"x","pk":"p","n":0}`, pkHeader)
	etag := header.Get("Etag")

	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			headers := map[string]string{"x-ms-documentdb-partitionkey": `["p"]`, "If-Match": etag}
			status, _, _ := c.do(http.MethodPut, "/dbs/db/colls/c/docs/x", fmt.Sprintf(`{"id":"x","pk":"p","n":%d}`, i+1), headers)
			mu.Lock()
			statuses[status]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if statuses[http.StatusOK] != 1 || statuses[http.StatusPreconditionFailed] != 7 {
		t.Fatalf("eight replaces conditioned on one ETag: %v", statuses)
	}

	c.must(http.StatusNoContent, http.MethodDelete, "/dbs/db/colls/c", "", nil)
	c.must(http.StatusNotFound, http.MethodGet, "/dbs/db/colls/c", "", nil)
}
