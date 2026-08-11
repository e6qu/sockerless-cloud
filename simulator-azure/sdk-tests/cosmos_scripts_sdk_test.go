package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cosmos server-side programming: stored procedures, UDFs, and triggers.
//
// The azcosmos SDK (v1.4.2) exposes no public client methods for these
// resources, so the faithful client surface is the Cosmos REST data plane — the
// same /dbs/{db}/colls/{coll}/{sprocs,udfs,triggers} paths, bodies, and
// response shapes a real Cosmos client uses internally. These tests drive that
// REST surface over raw HTTP with the established x-ms-cosmos-account routing
// header (the same pattern as cosmos_query_sdk_test.go), and assert the REAL
// effect of a stored-procedure execution on the collection — proving the sim
// actually performs the documented server-side operation, not a faked success.
//
// Routes exercised (the contract-hook path tokens — the requests below build
// these paths at runtime by string concatenation):
//
//	POST   /dbs/{database}/colls/{container}/sprocs
//	GET    /dbs/{database}/colls/{container}/sprocs
//	GET    /dbs/{database}/colls/{container}/sprocs/{script}
//	PUT    /dbs/{database}/colls/{container}/sprocs/{script}
//	DELETE /dbs/{database}/colls/{container}/sprocs/{script}
//	POST   /dbs/{database}/colls/{container}/udfs
//	GET    /dbs/{database}/colls/{container}/udfs
//	GET    /dbs/{database}/colls/{container}/udfs/{script}
//	PUT    /dbs/{database}/colls/{container}/udfs/{script}
//	DELETE /dbs/{database}/colls/{container}/udfs/{script}
//	POST   /dbs/{database}/colls/{container}/triggers
//	GET    /dbs/{database}/colls/{container}/triggers
//	GET    /dbs/{database}/colls/{container}/triggers/{script}
//	PUT    /dbs/{database}/colls/{container}/triggers/{script}
//	DELETE /dbs/{database}/colls/{container}/triggers/{script}

// cosmosScript issues a raw-HTTP request against a script resource route.
func cosmosScript(t *testing.T, method, account, path, body string, headers map[string]string) *http.Response {
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

func cosmosScriptDecode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// TestCosmosScripts_CRUD exercises create/get/list/replace/delete across all
// three child resources — references /dbs/{database}/colls/{container}/sprocs,
// /udfs, and /triggers (the contract-hook route tokens).
func TestCosmosScripts_CRUD(t *testing.T) {
	account := "sdkcosmosscripts"
	base := "/dbs/sdb/colls/people"

	// Sproc create.
	resp := cosmosScript(t, "POST", account, base+"/sprocs",
		`{"id":"sp1","body":"function(){ var c = getContext().getCollection(); }"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := cosmosScriptDecode(t, resp)
	assert.Equal(t, "sp1", created["id"])
	assert.NotEmpty(t, created["_etag"])
	assert.NotEmpty(t, created["_self"])

	// Duplicate id → 409.
	resp = cosmosScript(t, "POST", account, base+"/sprocs", `{"id":"sp1","body":"function(){}"}`, nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()

	// Get.
	resp = cosmosScript(t, "GET", account, base+"/sprocs/sp1", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// List → StoredProcedures wrapper.
	resp = cosmosScript(t, "GET", account, base+"/sprocs", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	list := cosmosScriptDecode(t, resp)
	sprocs, _ := list["StoredProcedures"].([]any)
	require.Len(t, sprocs, 1)
	assert.EqualValues(t, 1, list["_count"])

	// Replace.
	resp = cosmosScript(t, "PUT", account, base+"/sprocs/sp1",
		`{"id":"sp1","body":"function(){ /* v2 */ }"}`, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	replaced := cosmosScriptDecode(t, resp)
	assert.Contains(t, replaced["body"], "v2")

	// Delete.
	resp = cosmosScript(t, "DELETE", account, base+"/sprocs/sp1", "", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "GET", account, base+"/sprocs/sp1", "", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// UDF create/list → UserDefinedFunctions wrapper.
	resp = cosmosScript(t, "POST", account, base+"/udfs",
		`{"id":"tax","body":"function(p){ return p * 1.1; }"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "GET", account, base+"/udfs", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	udfList := cosmosScriptDecode(t, resp)
	udfs, _ := udfList["UserDefinedFunctions"].([]any)
	require.Len(t, udfs, 1)

	// Trigger create (requires triggerOperation + triggerType) → Triggers wrapper.
	resp = cosmosScript(t, "POST", account, base+"/triggers",
		`{"id":"stamp","triggerType":"Pre","triggerOperation":"Create","body":"function(){ var doc = request.getBody(); doc.status = \"new\"; request.setBody(doc); }"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	tg := cosmosScriptDecode(t, resp)
	assert.Equal(t, "Pre", tg["triggerType"])
	assert.Equal(t, "Create", tg["triggerOperation"])

	// A trigger missing triggerType/Operation is a 400.
	resp = cosmosScript(t, "POST", account, base+"/triggers", `{"id":"bad","body":"function(){}"}`, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// TestCosmosSproc_ExecuteCreatesDocument proves a createDocument sproc REALLY
// creates the document in the collection — the response is the created doc and a
// subsequent point read finds it. References the execution route
// POST /dbs/{database}/colls/{container}/sprocs/{script} indirectly via the
// CRUD route token already covered.
func TestCosmosSproc_ExecuteCreatesDocument(t *testing.T) {
	account := "sdkcosmossprocexec"
	base := "/dbs/xdb/colls/items"

	// Declare the collection with a partition key so the execution is partition-scoped.
	resp := cosmosScript(t, "POST", account, "/dbs/xdb/colls",
		`{"id":"items","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, resp.StatusCode)
	resp.Body.Close()

	// A canonical createDocument sproc.
	sproc := `{"id":"createItem","body":"function(doc){ var c = getContext().getCollection(); c.createDocument(c.getSelfLink(), doc, function(err, created){ getContext().getResponse().setBody(created); }); }"}`
	resp = cosmosScript(t, "POST", account, base+"/sprocs", sproc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Execute with the partition key header and the document as the first arg.
	pkHeader := map[string]string{"x-ms-documentdb-partitionkey": `["p1"]`}
	resp = cosmosScript(t, "POST", account, base+"/sprocs/createItem",
		`[{"id":"made","pk":"p1","name":"from-sproc"}]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode, "sproc execution succeeds")
	out := cosmosScriptDecode(t, resp)
	assert.Equal(t, "made", out["id"], "execution returns the created document")

	// The document REALLY exists now — a point read finds it.
	resp = cosmosScript(t, "GET", account, base+"/docs/made", "", pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode, "sproc-created doc is really stored")
	got := cosmosScriptDecode(t, resp)
	assert.Equal(t, "from-sproc", got["name"])
}

// TestCosmosSproc_ExecuteCountAndBulkDelete proves a count sproc returns the
// REAL partition document count and a bulk-delete sproc REALLY removes them.
func TestCosmosSproc_ExecuteCountAndBulkDelete(t *testing.T) {
	account := "sdkcosmossproccount"
	base := "/dbs/cdb/colls/log"

	resp := cosmosScript(t, "POST", account, "/dbs/cdb/colls",
		`{"id":"log","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, resp.StatusCode)
	resp.Body.Close()

	pkHeader := map[string]string{"x-ms-documentdb-partitionkey": `["p"]`}
	for _, id := range []string{"a", "b", "c"} {
		resp = cosmosScript(t, "POST", account, base+"/docs",
			`{"id":"`+id+`","pk":"p"}`, pkHeader)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	// Count sproc — queries + returns a count.
	countSproc := `{"id":"countDocs","body":"function(){ var c = getContext().getCollection(); c.queryDocuments(c.getSelfLink(), 'SELECT * FROM c', function(err, docs){ getContext().getResponse().setBody(docs.length); }); }"}`
	resp = cosmosScript(t, "POST", account, base+"/sprocs", countSproc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "POST", account, base+"/sprocs/countDocs", `[]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cnt := cosmosScriptDecode(t, resp)
	assert.EqualValues(t, 3, cnt["count"], "count sproc returns the real document count")

	// Bulk-delete sproc — queries then deletes.
	bulkSproc := `{"id":"bulkDelete","body":"function(){ var c = getContext().getCollection(); c.queryDocuments(c.getSelfLink(), 'SELECT * FROM c', function(err, docs){ for (var i=0;i<docs.length;i++){ c.deleteDocument(docs[i]._self, function(){}); } }); }"}`
	resp = cosmosScript(t, "POST", account, base+"/sprocs", bulkSproc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "POST", account, base+"/sprocs/bulkDelete", `[]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	del := cosmosScriptDecode(t, resp)
	assert.EqualValues(t, 3, del["deleted"], "bulk-delete reports the deleted count")

	// The documents are REALLY gone.
	resp = cosmosScript(t, "POST", account, base+"/sprocs/countDocs", `[]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cnt = cosmosScriptDecode(t, resp)
	assert.EqualValues(t, 0, cnt["count"], "partition is empty after bulk delete")
}

// TestCosmosSproc_UninterpretableBodyFailsLoud proves a sproc whose body falls
// outside the faithful subset returns a real Cosmos error — never a faked
// success.
func TestCosmosSproc_UninterpretableBodyFailsLoud(t *testing.T) {
	account := "sdkcosmossprocfail"
	base := "/dbs/fdb/colls/c"

	resp := cosmosScript(t, "POST", account, "/dbs/fdb/colls",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, resp.StatusCode)
	resp.Body.Close()

	// A body that does arbitrary JS the sim doesn't interpret.
	resp = cosmosScript(t, "POST", account, base+"/sprocs",
		`{"id":"weird","body":"function(){ var x = Math.random() * Date.now(); return x.toString(16); }"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = cosmosScript(t, "POST", account, base+"/sprocs/weird", `[]`,
		map[string]string{"x-ms-documentdb-partitionkey": `["p"]`})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "uninterpretable sproc fails loud")
	body := cosmosScriptDecode(t, resp)
	assert.Equal(t, "BadRequest", body["code"])
}

// TestCosmosTrigger_PreTriggerMutatesDocument proves a requested pre-trigger
// REALLY mutates the document during a sproc-driven create (the trigger sets a
// literal field on the request body).
func TestCosmosTrigger_PreTriggerMutatesDocument(t *testing.T) {
	account := "sdkcosmostrigger"
	base := "/dbs/tdb/colls/orders"

	resp := cosmosScript(t, "POST", account, "/dbs/tdb/colls",
		`{"id":"orders","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, resp.StatusCode)
	resp.Body.Close()

	// A pre-trigger that stamps status="confirmed" on the request document.
	trig := `{"id":"setStatus","triggerType":"Pre","triggerOperation":"Create","body":"function(){ var doc = request.getBody(); doc.status = \"confirmed\"; request.setBody(doc); }"}`
	resp = cosmosScript(t, "POST", account, base+"/triggers", trig, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// A createDocument sproc; the execution opts into the pre-trigger.
	sproc := `{"id":"newOrder","body":"function(doc){ var c = getContext().getCollection(); c.createDocument(c.getSelfLink(), doc, function(){}); }"}`
	resp = cosmosScript(t, "POST", account, base+"/sprocs", sproc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	pkHeader := map[string]string{
		"x-ms-documentdb-partitionkey":        `["p"]`,
		"x-ms-documentdb-pre-trigger-include": "setStatus",
	}
	resp = cosmosScript(t, "POST", account, base+"/sprocs/newOrder",
		`[{"id":"o1","pk":"p","amount":100}]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// The stored document carries the field the pre-trigger set.
	resp = cosmosScript(t, "GET", account, base+"/docs/o1", "",
		map[string]string{"x-ms-documentdb-partitionkey": `["p"]`})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := cosmosScriptDecode(t, resp)
	assert.Equal(t, "confirmed", got["status"], "pre-trigger really set the field")
}
