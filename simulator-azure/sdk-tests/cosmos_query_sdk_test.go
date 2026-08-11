package azure_sdk_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Cosmos data plane uses Shared-Key auth + endpoint discovery the azcosmos
// SDK's globalEndpointManager performs against `*.documents.azure.com`; pointing
// that at the single-port sim isn't expressible as a plain coordinate. These
// tests therefore drive the data plane over raw HTTP using the EXACT wire shape
// azcosmos emits — the same `x-ms-documentdb-isquery` / `x-ms-documentdb-is-upsert`
// headers, the `application/query+json` body with `{query, parameters}`, and the
// patch `{operations:[{op,path,value}]}` envelope (verified against
// azure-sdk-for-go/sdk/data/azcosmos@v1.4.2). This matches the established
// pattern in cosmos_test.go / storage_dataplanes_test.go.

func cosmosDoc(t *testing.T, method, account, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	var br io.Reader
	if body != "" {
		br = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, br)
	require.NoError(t, err)
	req.Header.Set("x-ms-cosmos-account", account)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func cosmosQuery(t *testing.T, account, coll, query string, params []map[string]any) []map[string]any {
	t.Helper()
	body := map[string]any{"query": query}
	if params != nil {
		body["parameters"] = params
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", baseURL+"/dbs/qdb/colls/"+coll+"/docs", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("x-ms-cosmos-account", account)
	req.Header.Set("Content-Type", "application/query+json")
	req.Header.Set("x-ms-documentdb-isquery", "true")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "query %q", query)
	var out struct {
		Documents []map[string]any `json:"Documents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Documents
}

// TestCosmosQuery_RealGrammar drives the SQL-subset engine over the data plane:
// parameterized WHERE with AND + numeric range, ORDER BY, and CONTAINS — each of
// which previously fell through the equality-only matcher and returned the whole
// collection.
func TestCosmosQuery_RealGrammar(t *testing.T) {
	account := "sdkcosmosquery"
	coll := "people"

	seed := []string{
		`{"id":"a","team":"platform","score":5,"name":"alice"}`,
		`{"id":"b","team":"platform","score":10,"name":"bob"}`,
		`{"id":"c","team":"infra","score":7,"name":"carol"}`,
	}
	for _, doc := range seed {
		resp := cosmosDoc(t, "POST", account, "/dbs/qdb/colls/"+coll+"/docs", doc, nil)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	// WHERE c.team = @p AND c.score > @q.
	got := cosmosQuery(t, account, coll,
		"SELECT * FROM c WHERE c.team = @p AND c.score > @q",
		[]map[string]any{{"name": "@p", "value": "platform"}, {"name": "@q", "value": 5}})
	require.Len(t, got, 1, "want only bob (platform, score>5)")
	assert.Equal(t, "b", got[0]["id"])

	// ORDER BY c.score DESC.
	got = cosmosQuery(t, account, coll, "SELECT c.id FROM c ORDER BY c.score DESC", nil)
	require.Len(t, got, 3)
	assert.Equal(t, "b", got[0]["id"], "highest score first")
	assert.Equal(t, "a", got[2]["id"], "lowest score last")

	// CONTAINS.
	got = cosmosQuery(t, account, coll, "SELECT * FROM c WHERE CONTAINS(c.name, 'ar')", nil)
	require.Len(t, got, 1)
	assert.Equal(t, "c", got[0]["id"], "only carol contains 'ar'")

	// COUNT aggregate.
	got = cosmosQuery(t, account, coll, "SELECT VALUE COUNT(1) FROM c WHERE c.team = 'platform'", nil)
	require.Len(t, got, 1)
	assert.EqualValues(t, 2, got[0]["$1"])
}

// TestCosmosUpsertAndConflict pins the create/upsert semantics: a plain re-create
// of an existing id is a 409 Conflict; an upsert (x-ms-documentdb-is-upsert)
// replaces it and returns 200.
func TestCosmosUpsertAndConflict(t *testing.T) {
	account := "sdkcosmosupsert"
	docs := "/dbs/qdb/colls/items/docs"

	resp := cosmosDoc(t, "POST", account, docs, `{"id":"dup","v":1}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Plain create of the same id → 409.
	resp = cosmosDoc(t, "POST", account, docs, `{"id":"dup","v":2}`, nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode, "duplicate-id create must 409")
	resp.Body.Close()

	// Upsert of the same id → 200, replaces the document.
	resp = cosmosDoc(t, "POST", account, docs, `{"id":"dup","v":3}`,
		map[string]string{"x-ms-documentdb-is-upsert": "true"})
	require.Equal(t, http.StatusOK, resp.StatusCode, "upsert of existing id returns 200")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"v":3`)

	// Confirm the stored doc reflects the upsert.
	resp = cosmosDoc(t, "GET", account, docs+"/dup", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var stored map[string]any
	require.NoError(t, json.Unmarshal(body, &stored))
	assert.EqualValues(t, 3, stored["v"])
}

// TestCosmosPatchIncrement pins the Cosmos partial-document PATCH route
// "PATCH /dbs/{database}/colls/{container}/docs/{doc}" applying an increment op
// to a stored document (the azcosmos PatchOperations wire shape).
func TestCosmosPatchIncrement(t *testing.T) {
	account := "sdkcosmospatch"
	docs := "/dbs/qdb/colls/counters/docs"

	resp := cosmosDoc(t, "POST", account, docs, `{"id":"ctr","count":10,"label":"keep"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	patch := `{"operations":[{"op":"incr","path":"/count","value":5},{"op":"set","path":"/status","value":"active"}]}`
	resp = cosmosDoc(t, "PATCH", account, docs+"/ctr", patch, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "patch must succeed")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	assert.EqualValues(t, 15, got["count"], "incr +5 from 10")
	assert.Equal(t, "active", got["status"], "set op added the field")
	assert.Equal(t, "keep", got["label"], "untouched property preserved")
}
