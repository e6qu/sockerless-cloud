package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// ReplaceContainer replaces a container's time to live, and the new setting
// governs the items it already holds; the partition key stays as created.
func TestCosmosReplaceContainerChangesTimeToLive(t *testing.T) {
	c := newCosmosItemsClient(t, "replacecosmos")
	now := cosmosFixedClock(t)
	c.must(http.StatusCreated, http.MethodPost, "/dbs", `{"id":"db"}`, nil)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"item","pk":"p"}`, pkHeader)

	readTTL := func(body []byte) (any, bool) {
		t.Helper()
		var coll map[string]any
		if err := json.Unmarshal(body, &coll); err != nil {
			t.Fatalf("container body %s: %v", body, err)
		}
		pk, _ := coll["partitionKey"].(map[string]any)
		if paths, _ := pk["paths"].([]any); len(paths) != 1 || paths[0] != "/pk" {
			t.Fatalf("container partition key: %s", body)
		}
		ttl, present := coll["defaultTtl"]
		return ttl, present
	}

	_, body := c.must(http.StatusOK, http.MethodPut, "/dbs/db/colls/c",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"},"defaultTtl":10}`, nil)
	if ttl, _ := readTTL(body); ttl != float64(10) {
		t.Fatalf("replace answered defaultTtl %v: %s", ttl, body)
	}
	_, body = c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c", "", nil)
	if ttl, _ := readTTL(body); ttl != float64(10) {
		t.Fatalf("read after replace: defaultTtl %v: %s", ttl, body)
	}
	*now = now.Add(11 * time.Second)
	c.must(http.StatusNotFound, http.MethodGet, "/dbs/db/colls/c/docs/item", "", pkHeader)

	c.must(http.StatusCreated, http.MethodPost, "/dbs/db/colls/c/docs", `{"id":"kept","pk":"p"}`, pkHeader)
	_, body = c.must(http.StatusOK, http.MethodPut, "/dbs/db/colls/c",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	if ttl, present := readTTL(body); present {
		t.Fatalf("a replace without defaultTtl left it at %v: %s", ttl, body)
	}
	*now = now.Add(time.Hour)
	c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c/docs/kept", "", pkHeader)

	for _, refused := range []string{
		`{"id":"c","partitionKey":{"paths":["/other"],"kind":"Hash"}}`,
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"},"defaultTtl":0}`,
	} {
		_, body = c.must(http.StatusBadRequest, http.MethodPut, "/dbs/db/colls/c", refused, nil)
		var e map[string]any
		if err := json.Unmarshal(body, &e); err != nil || e["code"] != "BadRequest" {
			t.Fatalf("refused replace %s answered %s", refused, body)
		}
	}
	_, body = c.must(http.StatusOK, http.MethodGet, "/dbs/db/colls/c", "", nil)
	if ttl, present := readTTL(body); present {
		t.Fatalf("a refused replace changed defaultTtl to %v", ttl)
	}

	_, body = c.must(http.StatusNotFound, http.MethodPut, "/dbs/db/colls/missing",
		`{"id":"missing","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	var e map[string]any
	if err := json.Unmarshal(body, &e); err != nil || e["code"] != "NotFound" {
		t.Fatalf("replace of a missing container answered %s", body)
	}
	if _, ok := cosmosDataColls.Get(cosmosDataCollKey("replacecosmos", "db", "missing")); ok {
		t.Fatal("a replace of a missing container created it")
	}
}
