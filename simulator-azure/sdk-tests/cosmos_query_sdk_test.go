package azure_sdk_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cosmosDoc drives the Azure Cosmos DB data plane over raw HTTP in the wire
// shape azcosmos v1.4.2 emits: the `x-ms-documentdb-isquery` and
// `x-ms-documentdb-is-upsert` headers, the `application/query+json` body
// `{query, parameters}`, and the patch envelope `{operations:[{op,path,value}]}`.

func cosmosDoc(t *testing.T, method, account, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	var br io.Reader
	if body != "" {
		br = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, br)
	require.NoError(t, err)
	req.Host = cosmosDataPlaneHost(t, account)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cosmosSignDataPlane(t, req, cosmosAccountKey(t, account))
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
	req.Host = cosmosDataPlaneHost(t, account)
	req.Header.Set("Content-Type", "application/query+json")
	req.Header.Set("x-ms-documentdb-isquery", "true")
	cosmosSignDataPlane(t, req, cosmosAccountKey(t, account))
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

// TestCosmosQuery_RealGrammar drives the SQL engine: a parameterized WHERE with
// AND and a numeric range, ORDER BY, CONTAINS, and COUNT.
func TestCosmosQuery_RealGrammar(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmosquery")
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

	got := cosmosQuery(t, account, coll,
		"SELECT * FROM c WHERE c.team = @p AND c.score > @q",
		[]map[string]any{{"name": "@p", "value": "platform"}, {"name": "@q", "value": 5}})
	require.Len(t, got, 1, "want only bob (platform, score>5)")
	assert.Equal(t, "b", got[0]["id"])

	got = cosmosQuery(t, account, coll, "SELECT c.id FROM c ORDER BY c.score DESC", nil)
	require.Len(t, got, 3)
	assert.Equal(t, "b", got[0]["id"], "highest score first")
	assert.Equal(t, "a", got[2]["id"], "lowest score last")

	got = cosmosQuery(t, account, coll, "SELECT * FROM c WHERE CONTAINS(c.name, 'ar')", nil)
	require.Len(t, got, 1)
	assert.Equal(t, "c", got[0]["id"], "only carol contains 'ar'")

	got = cosmosQuery(t, account, coll, "SELECT VALUE COUNT(1) FROM c WHERE c.team = 'platform'", nil)
	require.Len(t, got, 1)
	assert.EqualValues(t, 2, got[0]["$1"])
}

// TestCosmosUpsertAndConflict: re-creating an existing id is 409 Conflict; an
// upsert (x-ms-documentdb-is-upsert) replaces it with 200.
func TestCosmosUpsertAndConflict(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmosupsert")
	docs := "/dbs/qdb/colls/items/docs"

	resp := cosmosDoc(t, "POST", account, docs, `{"id":"dup","v":1}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = cosmosDoc(t, "POST", account, docs, `{"id":"dup","v":2}`, nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode, "duplicate-id create must 409")
	resp.Body.Close()

	resp = cosmosDoc(t, "POST", account, docs, `{"id":"dup","v":3}`,
		map[string]string{"x-ms-documentdb-is-upsert": "true"})
	require.Equal(t, http.StatusOK, resp.StatusCode, "upsert of existing id returns 200")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"v":3`)

	resp = cosmosDoc(t, "GET", account, docs+"/dup", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var stored map[string]any
	require.NoError(t, json.Unmarshal(body, &stored))
	assert.EqualValues(t, 3, stored["v"])
}

// TestCosmosPatchIncrement applies an increment op through the azcosmos
// PatchOperations wire shape.
func TestCosmosPatchIncrement(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmospatch")
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
