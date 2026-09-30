package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func cosmosTestDecodeRID(t *testing.T, rid string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(rid, "-", "/"))
	if err != nil {
		t.Fatalf("_rid %q is not the service's base64: %v", rid, err)
	}
	return raw
}

func cosmosTestResource(t *testing.T, out []byte) (rid, self string) {
	t.Helper()
	var resource struct {
		RID  string `json:"_rid"`
		Self string `json:"_self"`
	}
	if err := json.Unmarshal(out, &resource); err != nil {
		t.Fatalf("decode resource: %v: %s", err, out)
	}
	return resource.RID, resource.Self
}

// getByRID reads a resource by its id path, signing the lower-cased id of the
// addressed resource the way a client addressing by id does.
func (c *cosmosItemsClient) getByRID(path, rid string, headers map[string]string) (int, []byte) {
	c.t.Helper()
	segments := strings.Split(strings.Trim(path, "/"), "/")
	request := cosmosDataPlaneHTTPRequest(c.t, http.MethodGet, path, c.account, "")
	date := time.Now().UTC().Format(http.TimeFormat)
	request.Header.Set("x-ms-date", date)
	request.Header.Set("Authorization", cosmosAuthorizationHeader(c.t, c.key,
		cosmosStringToSign(http.MethodGet, segments[len(segments)-2], strings.ToLower(rid), date)))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	c.srv.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

// Resource ids are the service's hierarchical binary ids: 4 bytes for a
// database, its database's followed by 4 for a container, and its container's
// followed by an 8-byte little-endian number for an item or script, the kind in
// the last byte's top bits. A replace keeps an item's id, names with hyphens
// get distinct ids, and a resource reads back by its id path.
func TestCosmosResourceIDsAreHierarchicalAndAddressable(t *testing.T) {
	c := newCosmosItemsClient(t, "ridaccount")
	_, out := c.must(http.StatusCreated, http.MethodPost, "/dbs", `{"id":"a-b"}`, nil)
	dbRID, dbSelf := cosmosTestResource(t, out)
	c.must(http.StatusCreated, http.MethodPost, "/dbs", `{"id":"a"}`, nil)
	if db := cosmosTestDecodeRID(t, dbRID); len(db) != 4 {
		t.Fatalf("database _rid %q is %d bytes, want 4", dbRID, len(db))
	}
	if dbSelf != "dbs/"+dbRID+"/" {
		t.Fatalf("database _self %q, want dbs/%s/", dbSelf, dbRID)
	}

	_, out = c.must(http.StatusCreated, http.MethodPost, "/dbs/a-b/colls", `{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	collRID, _ := cosmosTestResource(t, out)
	_, out = c.must(http.StatusCreated, http.MethodPost, "/dbs/a/colls", `{"id":"b-c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	otherRID, _ := cosmosTestResource(t, out)
	coll := cosmosTestDecodeRID(t, collRID)
	if len(coll) != 8 || !bytes.Equal(coll[:4], cosmosTestDecodeRID(t, dbRID)) {
		t.Fatalf("container _rid %x does not extend its database's %q", coll, dbRID)
	}
	if collRID == otherRID {
		t.Fatalf("containers a-b/c and a/b-c share _rid %q", collRID)
	}

	var itemRIDs []string
	for _, id := range []string{"one", "two"} {
		_, out = c.must(http.StatusCreated, http.MethodPost, "/dbs/a-b/colls/c/docs", `{"id":"`+id+`","pk":"p"}`, pkHeader)
		rid, self := cosmosTestResource(t, out)
		item := cosmosTestDecodeRID(t, rid)
		if len(item) != 16 || !bytes.Equal(item[:8], coll) || item[15]>>4 != 0 {
			t.Fatalf("item _rid %x is not a document id under container %x", item, coll)
		}
		if self != "dbs/"+dbRID+"/colls/"+collRID+"/docs/"+rid+"/" {
			t.Fatalf("item _self %q is not its id path", self)
		}
		itemRIDs = append(itemRIDs, rid)
	}
	if itemRIDs[0] == itemRIDs[1] {
		t.Fatalf("two items share _rid %q", itemRIDs[0])
	}
	_, out = c.must(http.StatusOK, http.MethodPut, "/dbs/a-b/colls/c/docs/one", `{"id":"one","pk":"p","v":2}`, pkHeader)
	if rid, _ := cosmosTestResource(t, out); rid != itemRIDs[0] {
		t.Fatalf("a replace moved the item's _rid from %q to %q", itemRIDs[0], rid)
	}

	_, out = c.must(http.StatusCreated, http.MethodPost, "/dbs/a-b/colls/c/sprocs", `{"id":"sp","body":"function(){}"}`, nil)
	sprocRID, _ := cosmosTestResource(t, out)
	if sproc := cosmosTestDecodeRID(t, sprocRID); len(sproc) != 16 || sproc[15]>>4 != cosmosRIDKindStoredProcedure {
		t.Fatalf("stored procedure _rid %x does not carry the stored procedure kind", sproc)
	}

	status, got := c.getByRID("/dbs/"+dbRID+"/colls/"+collRID+"/docs/"+itemRIDs[0], itemRIDs[0], pkHeader)
	if status != http.StatusOK || !strings.Contains(string(got), `"v":2`) {
		t.Fatalf("GET item by its id path: %d %s", status, got)
	}
	if status, got := c.getByRID("/dbs/"+dbRID+"/colls/"+collRID, collRID, nil); status != http.StatusOK || !strings.Contains(string(got), `"id":"c"`) {
		t.Fatalf("GET container by its id path: %d %s", status, got)
	}
	if status, got := c.getByRID("/dbs/"+dbRID+"/colls/"+collRID+"/sprocs/"+sprocRID, sprocRID, nil); status != http.StatusOK || !strings.Contains(string(got), `"id":"sp"`) {
		t.Fatalf("GET stored procedure by its id path: %d %s", status, got)
	}

	c.must(http.StatusNoContent, http.MethodDelete, "/dbs/a-b/colls/c", "", nil)
	if status, got := c.getByRID("/dbs/"+dbRID+"/colls/"+collRID, collRID, nil); status != http.StatusNotFound {
		t.Fatalf("GET a deleted container by its id path: %d %s", status, got)
	}
	_, out = c.must(http.StatusCreated, http.MethodPost, "/dbs/a-b/colls", `{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	if again, _ := cosmosTestResource(t, out); again == collRID {
		t.Fatalf("a container created again under its old name kept _rid %q", again)
	}
}
