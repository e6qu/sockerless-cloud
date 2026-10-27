package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// azcosmos v1.4.2 has no client methods for stored procedures, UDFs and
// triggers, so these tests drive the REST data plane at the account's
// advertised documentEndpoint host, and assert the effect a stored procedure
// has on the collection.
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

func cosmosScript(t *testing.T, method, account, path, body string, headers map[string]string) *http.Response {
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

func cosmosScriptDecode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// TestCosmosScripts_CRUD exercises create, get, list, replace and delete on
// stored procedures, UDFs and triggers.
func TestCosmosScripts_CRUD(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmosscripts")
	base := "/dbs/sdb/colls/people"

	resp := cosmosScript(t, "POST", account, base+"/sprocs",
		`{"id":"sp1","body":"function(){ var c = getContext().getCollection(); }"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := cosmosScriptDecode(t, resp)
	assert.Equal(t, "sp1", created["id"])
	assert.NotEmpty(t, created["_etag"])
	assert.NotEmpty(t, created["_self"])

	resp = cosmosScript(t, "POST", account, base+"/sprocs", `{"id":"sp1","body":"function(){}"}`, nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()

	resp = cosmosScript(t, "GET", account, base+"/sprocs/sp1", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// The list wraps them in StoredProcedures.
	resp = cosmosScript(t, "GET", account, base+"/sprocs", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	list := cosmosScriptDecode(t, resp)
	sprocs, _ := list["StoredProcedures"].([]any)
	require.Len(t, sprocs, 1)
	assert.EqualValues(t, 1, list["_count"])

	resp = cosmosScript(t, "PUT", account, base+"/sprocs/sp1",
		`{"id":"sp1","body":"function(){ /* v2 */ }"}`, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	replaced := cosmosScriptDecode(t, resp)
	assert.Contains(t, replaced["body"], "v2")

	resp = cosmosScript(t, "DELETE", account, base+"/sprocs/sp1", "", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "GET", account, base+"/sprocs/sp1", "", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// The UDF list wraps them in UserDefinedFunctions.
	resp = cosmosScript(t, "POST", account, base+"/udfs",
		`{"id":"tax","body":"function(p){ return p * 1.1; }"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "GET", account, base+"/udfs", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	udfList := cosmosScriptDecode(t, resp)
	udfs, _ := udfList["UserDefinedFunctions"].([]any)
	require.Len(t, udfs, 1)

	// A trigger requires triggerOperation and triggerType; the list wraps them in
	// Triggers.
	resp = cosmosScript(t, "POST", account, base+"/triggers",
		`{"id":"stamp","triggerType":"Pre","triggerOperation":"Create","body":"function(){ var doc = request.getBody(); doc.status = \"new\"; request.setBody(doc); }"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	tg := cosmosScriptDecode(t, resp)
	assert.Equal(t, "Pre", tg["triggerType"])
	assert.Equal(t, "Create", tg["triggerOperation"])

	resp = cosmosScript(t, "POST", account, base+"/triggers", `{"id":"bad","body":"function(){}"}`, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// TestCosmosSproc_ExecuteCreatesDocument: a createDocument stored procedure
// creates the document, which a point read then finds.
func TestCosmosSproc_ExecuteCreatesDocument(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmossprocexec")
	base := "/dbs/xdb/colls/items"

	// A partition key makes the execution partition-scoped.
	resp := cosmosScript(t, "POST", account, "/dbs/xdb/colls",
		`{"id":"items","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, resp.StatusCode)
	resp.Body.Close()

	sproc := `{"id":"createItem","body":"function(doc){ var c = getContext().getCollection(); c.createDocument(c.getSelfLink(), doc, function(err, created){ getContext().getResponse().setBody(created); }); }"}`
	resp = cosmosScript(t, "POST", account, base+"/sprocs", sproc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// The partition key header, and the document as the first argument.
	pkHeader := map[string]string{"x-ms-documentdb-partitionkey": `["p1"]`}
	resp = cosmosScript(t, "POST", account, base+"/sprocs/createItem",
		`[{"id":"made","pk":"p1","name":"from-sproc"}]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode, "sproc execution succeeds")
	out := cosmosScriptDecode(t, resp)
	assert.Equal(t, "made", out["id"], "execution returns the created document")

	resp = cosmosScript(t, "GET", account, base+"/docs/made", "", pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode, "sproc-created doc is really stored")
	got := cosmosScriptDecode(t, resp)
	assert.Equal(t, "from-sproc", got["name"])
}

// TestCosmosSproc_ExecuteCountAndBulkDelete: a count procedure returns the
// partition's document count, and a bulk-delete procedure removes them.
func TestCosmosSproc_ExecuteCountAndBulkDelete(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmossproccount")
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

	countSproc := `{"id":"countDocs","body":"function(){ var c = getContext().getCollection(); c.queryDocuments(c.getSelfLink(), 'SELECT * FROM c', function(err, docs){ getContext().getResponse().setBody(docs.length); }); }"}`
	resp = cosmosScript(t, "POST", account, base+"/sprocs", countSproc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "POST", account, base+"/sprocs/countDocs", `[]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cnt := cosmosScriptDecode(t, resp)
	assert.EqualValues(t, 3, cnt["count"], "count sproc returns the real document count")

	bulkSproc := `{"id":"bulkDelete","body":"function(){ var c = getContext().getCollection(); c.queryDocuments(c.getSelfLink(), 'SELECT * FROM c', function(err, docs){ for (var i=0;i<docs.length;i++){ c.deleteDocument(docs[i]._self, function(){}); } }); }"}`
	resp = cosmosScript(t, "POST", account, base+"/sprocs", bulkSproc, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "POST", account, base+"/sprocs/bulkDelete", `[]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	del := cosmosScriptDecode(t, resp)
	assert.EqualValues(t, 3, del["deleted"], "bulk-delete reports the deleted count")

	resp = cosmosScript(t, "POST", account, base+"/sprocs/countDocs", `[]`, pkHeader)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cnt = cosmosScriptDecode(t, resp)
	assert.EqualValues(t, 0, cnt["count"], "partition is empty after bulk delete")
}

// TestCosmosSproc_UninterpretableBodyFailsLoud: a procedure outside the
// supported subset returns an Azure Cosmos DB error, never a success.
func TestCosmosSproc_UninterpretableBodyFailsLoud(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmossprocfail")
	base := "/dbs/fdb/colls/c"

	resp := cosmosScript(t, "POST", account, "/dbs/fdb/colls",
		`{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, resp.StatusCode)
	resp.Body.Close()

	// Arbitrary JavaScript the simulator does not interpret.
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

// TestCosmosTrigger_PreTriggerMutatesDocument: a requested pre-trigger sets a
// field on the document a procedure creates.
func TestCosmosTrigger_PreTriggerMutatesDocument(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmostrigger")
	base := "/dbs/tdb/colls/orders"

	resp := cosmosScript(t, "POST", account, "/dbs/tdb/colls",
		`{"id":"orders","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, resp.StatusCode)
	resp.Body.Close()

	trig := `{"id":"setStatus","triggerType":"Pre","triggerOperation":"Create","body":"function(){ var doc = request.getBody(); doc.status = \"confirmed\"; request.setBody(doc); }"}`
	resp = cosmosScript(t, "POST", account, base+"/triggers", trig, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// The execution opts into the pre-trigger.
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

	resp = cosmosScript(t, "GET", account, base+"/docs/o1", "",
		map[string]string{"x-ms-documentdb-partitionkey": `["p"]`})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := cosmosScriptDecode(t, resp)
	assert.Equal(t, "confirmed", got["status"], "pre-trigger really set the field")
}
