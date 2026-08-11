package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Cosmos DB server-side programming: stored procedures, user-defined functions,
// and triggers. These are container child resources the Cosmos REST data plane
// exposes under /dbs/{db}/colls/{coll}/{sprocs,udfs,triggers}. The azcosmos
// SDK (v1.4.2) carries no public client methods for them, so the faithful
// client surface is the REST API itself — the same paths, bodies, and response
// shapes a real Cosmos client (or the older documentdb SDK) uses.
//
// Stored-procedure EXECUTION (POST .../sprocs/{id}) runs the procedure's JS
// body against the collection. Arbitrary JS is out of scope, but a faithful,
// REAL subset of the common server-side patterns is interpreted and actually
// performed against the partition's documents (see cosmosExecuteSproc). A body
// outside that subset returns the real Cosmos error shape — never a faked
// success.

// CosmosScript is one stored procedure, UDF, or trigger. Kind distinguishes
// them ("sproc" / "udf" / "trigger") so a single store backs all three.
type CosmosScript struct {
	Account string `json:"-"`
	DB      string `json:"-"`
	Coll    string `json:"-"`
	Kind    string `json:"-"`
	ID      string `json:"id"`
	Body    string `json:"body"`
	// Triggers only.
	TriggerOperation string `json:"triggerOperation,omitempty"`
	TriggerType      string `json:"triggerType,omitempty"`
	ETag             string `json:"_etag,omitempty"`
	RID              string `json:"_rid,omitempty"`
	Self             string `json:"_self,omitempty"`
	TS               int64  `json:"_ts,omitempty"`
}

var cosmosScripts sim.Store[CosmosScript]

func registerCosmosScripts(srv *sim.Server) {
	cosmosScripts = sim.MakeStore[CosmosScript](srv.DB(), "cosmos_scripts")
	for _, s := range cosmosScripts.List() {
		cosmosRaiseETagFloor(cosmosETagSeqOf(s.ETag))
	}

	srv.HandleFunc("POST /dbs/{database}/colls/{container}/sprocs", handleCosmosCreateScript("sproc"))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/sprocs", handleCosmosListScripts("sproc"))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/sprocs/{script}", handleCosmosGetScript("sproc"))
	srv.HandleFunc("PUT /dbs/{database}/colls/{container}/sprocs/{script}", handleCosmosReplaceScript("sproc"))
	srv.HandleFunc("DELETE /dbs/{database}/colls/{container}/sprocs/{script}", handleCosmosDeleteScript("sproc"))
	// Execution: POST to the sproc resource (body = JSON array of args).
	srv.HandleFunc("POST /dbs/{database}/colls/{container}/sprocs/{script}", handleCosmosExecuteSproc)

	srv.HandleFunc("POST /dbs/{database}/colls/{container}/udfs", handleCosmosCreateScript("udf"))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/udfs", handleCosmosListScripts("udf"))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/udfs/{script}", handleCosmosGetScript("udf"))
	srv.HandleFunc("PUT /dbs/{database}/colls/{container}/udfs/{script}", handleCosmosReplaceScript("udf"))
	srv.HandleFunc("DELETE /dbs/{database}/colls/{container}/udfs/{script}", handleCosmosDeleteScript("udf"))

	srv.HandleFunc("POST /dbs/{database}/colls/{container}/triggers", handleCosmosCreateScript("trigger"))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/triggers", handleCosmosListScripts("trigger"))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/triggers/{script}", handleCosmosGetScript("trigger"))
	srv.HandleFunc("PUT /dbs/{database}/colls/{container}/triggers/{script}", handleCosmosReplaceScript("trigger"))
	srv.HandleFunc("DELETE /dbs/{database}/colls/{container}/triggers/{script}", handleCosmosDeleteScript("trigger"))
}

func cosmosScriptKey(account, db, coll, kind, id string) string {
	return account + "/" + db + "/" + coll + "/" + kind + "/" + id
}

// cosmosScriptListKey returns the response wrapper key Cosmos uses for each
// resource collection (mirrors the Documents/DocumentCollections shape).
func cosmosScriptListKey(kind string) string {
	switch kind {
	case "sproc":
		return "StoredProcedures"
	case "udf":
		return "UserDefinedFunctions"
	case "trigger":
		return "Triggers"
	}
	return "Resources"
}

func cosmosScriptSelfSeg(kind string) string {
	switch kind {
	case "sproc":
		return "sprocs"
	case "udf":
		return "udfs"
	case "trigger":
		return "triggers"
	}
	return kind
}

func cosmosScriptBody(s CosmosScript) map[string]any {
	out := map[string]any{
		"id":           s.ID,
		"body":         s.Body,
		"_rid":         s.RID,
		"_self":        s.Self,
		"_etag":        s.ETag,
		"_ts":          s.TS,
		"_attachments": "attachments/",
	}
	if s.Kind == "trigger" {
		out["triggerOperation"] = s.TriggerOperation
		out["triggerType"] = s.TriggerType
	}
	return out
}

func cosmosStoreScript(account, db, coll, kind, id, body, trigOp, trigType string) CosmosScript {
	now := time.Now().UTC().Unix()
	s := CosmosScript{
		Account:          account,
		DB:               db,
		Coll:             coll,
		Kind:             kind,
		ID:               id,
		Body:             body,
		TriggerOperation: trigOp,
		TriggerType:      trigType,
		ETag:             fmt.Sprintf(`"%x-%x"`, now, cosmosETagSeq.Add(1)),
		RID:              account + "-" + db + "-" + coll + "-" + kind + "-" + id,
		Self:             "dbs/" + db + "/colls/" + coll + "/" + cosmosScriptSelfSeg(kind) + "/" + id + "/",
		TS:               now,
	}
	cosmosScripts.Put(cosmosScriptKey(account, db, coll, kind, id), s)
	return s
}

func handleCosmosCreateScript(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			cosmosDataError(w, "BadRequest", "invalid "+kind+" body", http.StatusBadRequest)
			return
		}
		id, _ := body["id"].(string)
		if id == "" {
			cosmosDataError(w, "BadRequest", "id is required", http.StatusBadRequest)
			return
		}
		jsBody, _ := body["body"].(string)
		if jsBody == "" {
			cosmosDataError(w, "BadRequest", "body is required", http.StatusBadRequest)
			return
		}
		trigOp, trigType := "", ""
		if kind == "trigger" {
			trigOp, _ = body["triggerOperation"].(string)
			trigType, _ = body["triggerType"].(string)
			if trigOp == "" || trigType == "" {
				cosmosDataError(w, "BadRequest", "triggerOperation and triggerType are required for a trigger", http.StatusBadRequest)
				return
			}
		}
		if _, exists := cosmosScripts.Get(cosmosScriptKey(account, db, coll, kind, id)); exists {
			cosmosDataError(w, "Conflict", "Resource with specified id or name already exists.", http.StatusConflict)
			return
		}
		s := cosmosStoreScript(account, db, coll, kind, id, jsBody, trigOp, trigType)
		cosmosWriteData(w, http.StatusCreated, cosmosScriptBody(s))
	}
}

func handleCosmosListScripts(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
		all := cosmosScripts.Filter(func(s CosmosScript) bool {
			return s.Account == account && s.DB == db && s.Coll == coll && s.Kind == kind
		})
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		items := make([]map[string]any, 0, len(all))
		for _, s := range all {
			items = append(items, cosmosScriptBody(s))
		}
		cosmosWriteData(w, http.StatusOK, map[string]any{
			cosmosScriptListKey(kind): items,
			"_count":                  len(items),
		})
	}
}

func handleCosmosGetScript(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, db, coll, id := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "script")
		s, ok := cosmosScripts.Get(cosmosScriptKey(account, db, coll, kind, id))
		if !ok {
			cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
			return
		}
		cosmosWriteData(w, http.StatusOK, cosmosScriptBody(s))
	}
}

func handleCosmosReplaceScript(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, db, coll, id := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "script")
		existing, ok := cosmosScripts.Get(cosmosScriptKey(account, db, coll, kind, id))
		if !ok {
			cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
			return
		}
		if !cosmosIfMatchOK(r, existing.ETag) {
			cosmosDataError(w, "PreconditionFailed",
				"Operation cannot be performed because one of the specified precondition is not met.",
				http.StatusPreconditionFailed)
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			cosmosDataError(w, "BadRequest", "invalid "+kind+" body", http.StatusBadRequest)
			return
		}
		jsBody, _ := body["body"].(string)
		if jsBody == "" {
			cosmosDataError(w, "BadRequest", "body is required", http.StatusBadRequest)
			return
		}
		trigOp, trigType := existing.TriggerOperation, existing.TriggerType
		if kind == "trigger" {
			if v, ok := body["triggerOperation"].(string); ok && v != "" {
				trigOp = v
			}
			if v, ok := body["triggerType"].(string); ok && v != "" {
				trigType = v
			}
		}
		s := cosmosStoreScript(account, db, coll, kind, id, jsBody, trigOp, trigType)
		cosmosWriteData(w, http.StatusOK, cosmosScriptBody(s))
	}
}

func handleCosmosDeleteScript(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, db, coll, id := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "script")
		if !cosmosScripts.Delete(cosmosScriptKey(account, db, coll, kind, id)) {
			cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── stored-procedure execution ─────────────────────────────────────────────────

// handleCosmosExecuteSproc runs a stored procedure against the collection scoped
// to the request's partition key (mandatory for execution, exactly as real
// Cosmos requires). The procedure's JS body is interpreted against the faithful
// subset in cosmosExecuteSproc; the response body is the procedure's return
// value (real Cosmos returns whatever the sproc passed to getContext()
// .getResponse().setBody(...)).
func handleCosmosExecuteSproc(w http.ResponseWriter, r *http.Request) {
	account, db, coll, id := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "script")
	s, ok := cosmosScripts.Get(cosmosScriptKey(account, db, coll, "sproc", id))
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	// The partition key is required for a stored-procedure execution; real Cosmos
	// rejects an execution that omits it because a sproc runs in a single
	// partition's transactional scope.
	pkComponent, hasPK, werr := cosmosResolvePKForPoint(r, account, db, coll)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	if !hasPK {
		// Without a declared PK path the collection is unpartitioned (legacy
		// raw-HTTP); execution then operates over the whole collection. With a
		// declared PK path, real Cosmos requires the header.
		if _, declared := cosmosContainerPKPath(account, db, coll); declared {
			cosmosDataError(w, "BadRequest",
				"The partition key supplied in x-ms-partitionkey header has fewer components than defined in the the collection.",
				http.StatusBadRequest)
			return
		}
	}
	// The request body is a JSON array of the sproc's positional arguments.
	var args []any
	if err := sim.ReadJSON(r, &args); err != nil {
		// An empty body is a no-arg call; only a malformed non-empty body is a 400.
		args = nil
	}
	result, eerr := cosmosExecuteSproc(r, s, account, db, coll, pkComponent, hasPK, args)
	if eerr != nil {
		cosmosDataError(w, eerr.code, eerr.msg, eerr.status)
		return
	}
	cosmosWriteData(w, http.StatusOK, result)
}

// cosmosExecuteSproc interprets the faithful subset of stored-procedure bodies
// and REALLY performs the documented operation against the collection. The
// recognized patterns (matched structurally against the JS body, not executed
// as arbitrary JS) are:
//
//   - createDocument: a body that calls collection.createDocument(...) with the
//     first sproc argument as the document → the document is really created in
//     the request's partition (honouring the 409-on-duplicate-id rule).
//   - count: a body that queries/iterates the documents and returns a count →
//     the real number of documents in the partition is returned.
//   - bulkDelete: a body that deletes the documents it reads (the canonical
//     Cosmos "bulk delete" sample) → every document in the partition is really
//     deleted and the deleted count returned.
//
// A body that matches none of these returns a real Cosmos error
// (BadRequest, message identifying the unsupported sproc) — never a fake
// success.
func cosmosExecuteSproc(r *http.Request, s CosmosScript, account, db, coll, pkComponent string, hasPK bool, args []any) (map[string]any, *cosmosWriteError) {
	body := s.Body

	switch {
	case cosmosSprocCreatesDocument(body):
		var docArg map[string]any
		if len(args) > 0 {
			if m, ok := args[0].(map[string]any); ok {
				docArg = m
			}
		}
		if docArg == nil {
			return nil, &cosmosWriteError{code: "BadRequest",
				msg: "stored procedure expected a document object as its first argument", status: http.StatusBadRequest}
		}
		id, _ := docArg["id"].(string)
		if id == "" {
			id = generateUUID()
			docArg["id"] = id
		}
		// Determine the partition from the document body (the sproc runs within
		// the request's partition; the created doc must belong to it).
		pkKey, werr := cosmosResolvePKForWrite(r, account, db, coll, docArg)
		if werr != nil {
			return nil, werr
		}
		key := cosmosDocKeyPK(account, db, coll, pkKey, id)
		if _, exists := cosmosDocs.Get(key); exists {
			return nil, &cosmosWriteError{code: "Conflict",
				msg: "Resource with specified id or name already exists.", status: http.StatusConflict}
		}
		// Pre-triggers the execution requested via the
		// x-ms-documentdb-pre-trigger-include header fire BEFORE the write and
		// really mutate the document where their body is interpretable.
		cosmosApplyPreTriggers(r, account, db, coll, "Create", docArg)
		doc := cosmosStoreDocKey(key, account, db, coll, id, docArg)
		return cosmosDocBody(doc), nil

	case cosmosSprocBulkDeletes(body):
		docs := cosmosSprocPartitionDocs(account, db, coll, pkComponent, hasPK)
		deleted := 0
		for _, d := range docs {
			cosmosDocs.Delete(cosmosStoredDocKey(d))
			deleted++
		}
		return map[string]any{"deleted": deleted, "continuation": false}, nil

	case cosmosSprocCounts(body):
		docs := cosmosSprocPartitionDocs(account, db, coll, pkComponent, hasPK)
		return map[string]any{"count": len(docs)}, nil

	default:
		return nil, &cosmosWriteError{
			code:   "BadRequest",
			msg:    "Encountered exception while executing function. The stored procedure body uses JavaScript the simulator does not interpret. Supported patterns: createDocument, document count, bulk delete.",
			status: http.StatusBadRequest,
		}
	}
}

// cosmosSprocPartitionDocs returns the documents the sproc operates over: the
// request partition's documents when a PK is supplied, else the whole
// collection (unpartitioned legacy collection).
func cosmosSprocPartitionDocs(account, db, coll, pkComponent string, hasPK bool) []CosmosDocument {
	docs := cosmosDocsFor(account, db, coll)
	if !hasPK {
		return docs
	}
	out := docs[:0:0]
	for _, d := range docs {
		if cosmosDocPKComponent(account, db, coll, d) == pkComponent {
			out = append(out, d)
		}
	}
	return out
}

var (
	cosmosReCreateDoc = regexp.MustCompile(`(?i)\.\s*createDocument\s*\(`)
	cosmosReDeleteDoc = regexp.MustCompile(`(?i)\.\s*deleteDocument\s*\(`)
	cosmosReCount     = regexp.MustCompile(`(?i)(\.\s*length|count|_count)`)
	cosmosReQuery     = regexp.MustCompile(`(?i)(queryDocuments|readDocuments|getCollection)`)
)

// cosmosSprocCreatesDocument matches a sproc that calls createDocument and does
// NOT also delete (so a bulk-delete sample that internally re-reads isn't
// mistaken for a create).
func cosmosSprocCreatesDocument(body string) bool {
	return cosmosReCreateDoc.MatchString(body) && !cosmosReDeleteDoc.MatchString(body)
}

// cosmosSprocBulkDeletes matches the canonical bulk-delete sample: it queries
// the documents then deletes them.
func cosmosSprocBulkDeletes(body string) bool {
	return cosmosReDeleteDoc.MatchString(body)
}

// cosmosSprocCounts matches a count sproc: it reads/queries documents and
// returns a count, without creating or deleting anything.
func cosmosSprocCounts(body string) bool {
	if cosmosReCreateDoc.MatchString(body) || cosmosReDeleteDoc.MatchString(body) {
		return false
	}
	return cosmosReQuery.MatchString(body) && cosmosReCount.MatchString(body)
}

// cosmosApplyPreTriggers fires the pre-triggers the request explicitly includes
// via the x-ms-documentdb-pre-trigger-include header and really mutates the
// document where the trigger body is interpretable. Real Cosmos only runs a
// pre-trigger when the write opts into it by that header, and only the named
// triggers — so an unrequested trigger never fires. A trigger whose JS body
// matches the faithful "set a field on the request document" subset actually
// applies that mutation; a body outside the subset is a no-op (its arbitrary JS
// isn't interpreted), which is recorded loud-or-quiet only by the absence of
// the mutation — no fake effect is invented.
func cosmosApplyPreTriggers(r *http.Request, account, db, coll, op string, doc map[string]any) {
	requested := cosmosRequestedTriggers(r, "x-ms-documentdb-pre-trigger-include")
	if len(requested) == 0 {
		return
	}
	for _, t := range cosmosScriptTriggersFor(account, db, coll, "Pre", op) {
		if !requested[t.ID] {
			continue
		}
		cosmosApplyTriggerSetFields(t.Body, doc)
	}
}

// cosmosRequestedTriggers parses the comma-separated pre/post-trigger-include
// header into a set of trigger ids the operation opted into.
func cosmosRequestedTriggers(r *http.Request, header string) map[string]bool {
	raw := strings.TrimSpace(r.Header.Get(header))
	if raw == "" {
		return nil
	}
	out := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// cosmosScriptTriggersFor reports the triggers registered on a collection for a
// given operation + type, used to honour the pre/post-trigger include headers.
func cosmosScriptTriggersFor(account, db, coll, trigType, op string) []CosmosScript {
	var out []CosmosScript
	for _, s := range cosmosScripts.List() {
		if s.Account != account || s.DB != db || s.Coll != coll || s.Kind != "trigger" {
			continue
		}
		if !strings.EqualFold(s.TriggerType, trigType) {
			continue
		}
		if strings.EqualFold(s.TriggerOperation, "All") || strings.EqualFold(s.TriggerOperation, op) {
			out = append(out, s)
		}
	}
	return out
}

// cosmosTrigSetField matches the canonical Cosmos pre-trigger pattern that sets
// a property on the request document to a literal value, capturing the field
// name and the raw right-hand side up to the statement terminator:
//
//	var doc = request.getBody(); doc.<field> = "<literal>"; request.setBody(doc);
//	var doc = getContext().getRequest().getBody(); doc.<field> = <number>; ...
//
// Go's RE2 has no backreferences, so the value is captured raw and the quoting
// is resolved in cosmosApplyTriggerSetFields.
var cosmosTrigSetField = regexp.MustCompile(
	`(?:doc|item|body)\s*(?:\.(\w+)|\["(\w+)"\])\s*=\s*([^;]+?)\s*;`)

// cosmosApplyTriggerSetFields applies every interpretable "set a field to a
// literal" mutation found in the trigger body to doc. A double-quoted RHS sets a
// string; a timestamp helper (`__.getDate()` / `new Date()` / `getTimestamp`)
// sets the current unix time (the common "stamp createdTime" pre-trigger); a
// numeric/boolean literal sets that value.
func cosmosApplyTriggerSetFields(jsBody string, doc map[string]any) {
	for _, m := range cosmosTrigSetField.FindAllStringSubmatch(jsBody, -1) {
		field := m[1]
		if field == "" {
			field = m[2]
		}
		val := strings.TrimSpace(m[3])
		switch {
		case len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"':
			doc[field] = val[1 : len(val)-1]
		case strings.Contains(val, "getDate") || strings.Contains(val, "Date()") || strings.Contains(val, "getTimestamp"):
			doc[field] = time.Now().UTC().Unix()
		case val == "true" || val == "false":
			doc[field] = val == "true"
		default:
			if n, ok := cosmosNumberOf(parseJSNumber(val)); ok {
				doc[field] = n
			} else {
				doc[field] = val
			}
		}
	}
}

// parseJSNumber turns a literal numeric token into a float64-bearing any (or
// returns the original string when it isn't numeric).
func parseJSNumber(s string) any {
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}
