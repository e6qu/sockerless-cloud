package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// fsEnum is a Firestore enum field value: the canonical uppercase name the
// handlers compare against. The gax REST transport the high-level client uses
// marshals enums with UseEnumNumbers (so it sends numbers); the low-level REST
// client sends the string name. Real Firestore accepts both, so the sim does
// too — fsDecodeEnum normalizes either form to the canonical name.
type fsEnum string

// fsDecodeEnum normalizes a Firestore enum JSON token (string name or numeric
// proto value) to its canonical uppercase name using the given number→name map.
func fsDecodeEnum(b []byte, names map[int]string) (fsEnum, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return "", nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return "", err
		}
		return fsEnum(strings.ToUpper(s)), nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return "", err
	}
	return fsEnum(names[n]), nil
}

var (
	fsServerValueNames = map[int]string{1: "REQUEST_TIME"}
	fsDirectionNames   = map[int]string{1: "ASCENDING", 2: "DESCENDING"}
	fsCompositeOpNames = map[int]string{1: "AND", 2: "OR"}
	fsUnaryOpNames     = map[int]string{2: "IS_NAN", 3: "IS_NULL", 4: "IS_NOT_NAN", 5: "IS_NOT_NULL"}
	fsFieldOpNames     = map[int]string{
		1: "LESS_THAN", 2: "LESS_THAN_OR_EQUAL", 3: "GREATER_THAN", 4: "GREATER_THAN_OR_EQUAL",
		5: "EQUAL", 6: "NOT_EQUAL", 7: "ARRAY_CONTAINS", 8: "IN", 9: "ARRAY_CONTAINS_ANY", 10: "NOT_IN",
	}
)

// Named enum wrappers so json.Unmarshal dispatches to the right number→name map.
type (
	fsServerValueEnum fsEnum
	fsDirectionEnum   fsEnum
	fsCompositeOpEnum fsEnum
	fsUnaryOpEnum     fsEnum
	fsFieldOpEnum     fsEnum
)

func (e *fsServerValueEnum) UnmarshalJSON(b []byte) error {
	v, err := fsDecodeEnum(b, fsServerValueNames)
	*e = fsServerValueEnum(v)
	return err
}
func (e *fsDirectionEnum) UnmarshalJSON(b []byte) error {
	v, err := fsDecodeEnum(b, fsDirectionNames)
	*e = fsDirectionEnum(v)
	return err
}
func (e *fsCompositeOpEnum) UnmarshalJSON(b []byte) error {
	v, err := fsDecodeEnum(b, fsCompositeOpNames)
	*e = fsCompositeOpEnum(v)
	return err
}
func (e *fsUnaryOpEnum) UnmarshalJSON(b []byte) error {
	v, err := fsDecodeEnum(b, fsUnaryOpNames)
	*e = fsUnaryOpEnum(v)
	return err
}
func (e *fsFieldOpEnum) UnmarshalJSON(b []byte) error {
	v, err := fsDecodeEnum(b, fsFieldOpNames)
	*e = fsFieldOpEnum(v)
	return err
}

// Firestore v1 REST document surface. This slice persists documents in
// Firestore's typed-value JSON shape and implements document CRUD,
// commit/batchGet/batchWrite, and structured equality queries.

type FSDocument struct {
	Name       string             `json:"name"`
	Fields     map[string]FSValue `json:"fields,omitempty"`
	CreateTime string             `json:"createTime,omitempty"`
	UpdateTime string             `json:"updateTime,omitempty"`
}

type FSValue struct {
	NullValue      any           `json:"nullValue,omitempty"`
	BooleanValue   *bool         `json:"booleanValue,omitempty"`
	IntegerValue   string        `json:"integerValue,omitempty"`
	DoubleValue    *float64      `json:"doubleValue,omitempty"`
	TimestampValue string        `json:"timestampValue,omitempty"`
	StringValue    string        `json:"stringValue,omitempty"`
	BytesValue     string        `json:"bytesValue,omitempty"`
	ReferenceValue string        `json:"referenceValue,omitempty"`
	GeoPointValue  *FSGeoPoint   `json:"geoPointValue,omitempty"`
	ArrayValue     *FSArrayValue `json:"arrayValue,omitempty"`
	MapValue       *FSMapValue   `json:"mapValue,omitempty"`
}

// FSGeoPoint mirrors Firestore's geoPointValue: a latitude/longitude pair.
type FSGeoPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type FSArrayValue struct {
	Values []FSValue `json:"values,omitempty"`
}

type FSMapValue struct {
	Fields map[string]FSValue `json:"fields,omitempty"`
}

// fsFieldTransform mirrors Firestore's FieldTransform: exactly one of the
// transform operators applies to fieldPath.
type fsFieldTransform struct {
	FieldPath             string            `json:"fieldPath"`
	SetToServerValue      fsServerValueEnum `json:"setToServerValue,omitempty"`
	Increment             *FSValue          `json:"increment,omitempty"`
	Maximum               *FSValue          `json:"maximum,omitempty"`
	Minimum               *FSValue          `json:"minimum,omitempty"`
	AppendMissingElements *FSArrayValue     `json:"appendMissingElements,omitempty"`
	RemoveAllFromArray    *FSArrayValue     `json:"removeAllFromArray,omitempty"`
}

// fsDocumentTransform mirrors Firestore's DocumentTransform (the Write.transform
// field): a target document plus an ordered list of field transforms.
type fsDocumentTransform struct {
	Document        string             `json:"document,omitempty"`
	FieldTransforms []fsFieldTransform `json:"fieldTransforms,omitempty"`
}

// fsPrecondition mirrors Firestore's Precondition (Write.currentDocument):
// either exists (presence assertion) or updateTime (optimistic-concurrency
// assertion). Exactly one is set when present.
type fsPrecondition struct {
	Exists     *bool  `json:"exists,omitempty"`
	UpdateTime string `json:"updateTime,omitempty"`
}

// fsWrite mirrors Firestore's Write: an update/delete plus optional updateMask,
// a standalone or trailing transform (DocumentTransform), inline
// updateTransforms applied after the update, and a currentDocument precondition.
type fsWrite struct {
	Update     *FSDocument `json:"update,omitempty"`
	UpdateMask *struct {
		FieldPaths []string `json:"fieldPaths"`
	} `json:"updateMask,omitempty"`
	Delete           string               `json:"delete,omitempty"`
	Transform        *fsDocumentTransform `json:"transform,omitempty"`
	UpdateTransforms []fsFieldTransform   `json:"updateTransforms,omitempty"`
	CurrentDocument  *fsPrecondition      `json:"currentDocument,omitempty"`
}

var fsDocuments sim.Store[FSDocument]

func registerFirestore(srv *sim.Server) {
	fsDocuments = sim.MakeStore[FSDocument](srv.DB(), "firestore_documents")

	fsTransactions = sim.MakeStore[fsTxn](srv.DB(), "firestore_transactions")

	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:beginTransaction", handleFSBeginTransaction)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:rollback", handleFSRollback)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:commit", handleFSCommit)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:batchGet", handleFSBatchGet)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:batchWrite", handleFSBatchWrite)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:runQuery", handleFSRunRootQuery)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents/{postPath...}", handleFSPostDocuments)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/documents/{docPath...}", handleFSGetOrList)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/documents/{docPath...}", handleFSPatchDocument)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/documents/{docPath...}", handleFSDeleteDocument)

	registerFirestoreAdmin(srv)
}

func fsDatabasePrefix(project, database string) string {
	return "projects/" + project + "/databases/" + database + "/documents"
}

func fsNow() string {
	return nowTimestamp()
}

func fsFullName(project, database, docPath string) string {
	docPath = strings.Trim(docPath, "/")
	if docPath == "" {
		return fsDatabasePrefix(project, database)
	}
	return fsDatabasePrefix(project, database) + "/" + docPath
}

func fsCollectionParent(name string) string {
	idx := strings.LastIndex(name, "/")
	if idx < 0 {
		return ""
	}
	return name[:idx]
}

func fsPutDocument(doc FSDocument) FSDocument {
	now := fsNow()
	if doc.CreateTime == "" {
		doc.CreateTime = now
	}
	doc.UpdateTime = now
	if doc.Fields == nil {
		doc.Fields = map[string]FSValue{}
	}
	fsDocuments.Put(doc.Name, doc)
	return doc
}

func handleFSPostDocuments(w http.ResponseWriter, r *http.Request) {
	path := sim.PathParam(r, "postPath")
	if strings.HasSuffix(path, ":runQuery") {
		handleFSRunQuery(w, r, strings.TrimSuffix(path, ":runQuery"))
		return
	}
	// A ":verb" on the last segment names an AIP-136 custom method, not a
	// collection to create a document in. runQuery above is the one the
	// simulator serves on a document path; the others Firestore documents
	// there — listCollectionIds, partitionQuery, runAggregationQuery — are
	// unrouted, and creating a document in a collection named after the method
	// would silently invent data.
	if last := path[strings.LastIndex(path, "/")+1:]; strings.Contains(last, ":") {
		gcpMethodNotFound(w)
		return
	}
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	docID := r.URL.Query().Get("documentId")
	if docID == "" {
		docID = generateUUID()
	}
	var req FSDocument
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid document body: %v", err)
		return
	}
	name := fsFullName(project, database, strings.Trim(path, "/")+"/"+docID)
	if _, ok := fsDocuments.Get(name); ok {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Document already exists: %s", name)
		return
	}
	req.Name = name
	sim.WriteJSON(w, http.StatusOK, fsPutDocument(req))
}

func handleFSGetOrList(w http.ResponseWriter, r *http.Request) {
	project, database, docPath := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "docPath")
	name := fsFullName(project, database, docPath)
	if strings.Count(strings.Trim(docPath, "/"), "/")%2 == 0 {
		handleFSListDocuments(w, r, name)
		return
	}
	doc, ok := fsDocuments.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Document not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, doc)
}

func handleFSListDocuments(w http.ResponseWriter, r *http.Request, collection string) {
	prefix := strings.TrimSuffix(collection, "/") + "/"
	docs := fsDocuments.Filter(func(d FSDocument) bool {
		if !strings.HasPrefix(d.Name, prefix) {
			return false
		}
		rest := strings.TrimPrefix(d.Name, prefix)
		return rest != "" && !strings.Contains(rest, "/")
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	page, next, ok := paginateList(w, r, docs)
	if !ok {
		return
	}
	resp := map[string]any{"documents": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// fsApplyUpdateMask merges incoming fields into existing per the Firestore
// updateMask. An ABSENT mask (mask == nil) replaces the document wholesale (Set
// without merge). A PRESENT mask (mask != nil, even with zero paths) writes only
// the listed top-level field paths (present in the body → set, absent → delete)
// and preserves every other existing field — which is what DocumentRef.Update,
// Set(..., MergeAll), and a transform-only Update (empty mask + updateTransforms)
// rely on. Absent vs present-but-empty is load-bearing: the SDK's transform-only
// Update sends an empty fields doc with a present empty mask, which must preserve
// the existing fields so the transform reads them — collapsing the two wipes the
// doc.
func fsApplyUpdateMask(existing, incoming map[string]FSValue, mask *[]string) map[string]FSValue {
	if mask == nil {
		return incoming
	}
	result := make(map[string]FSValue, len(existing)+len(incoming))
	for k, v := range existing {
		result[k] = v
	}
	for _, path := range *mask {
		if v, ok := incoming[path]; ok {
			result[path] = v
		} else {
			delete(result, path)
		}
	}
	return result
}

func handleFSPatchDocument(w http.ResponseWriter, r *http.Request) {
	project, database, docPath := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "docPath")
	name := fsFullName(project, database, docPath)
	// The REST patch endpoint carries the currentDocument precondition as query
	// params (currentDocument.exists / currentDocument.updateTime).
	if pre := fsPreconditionFromQuery(r); pre != nil {
		if e := fsEvalPrecondition(name, pre); e != nil {
			sim.GCPError(w, e.httpStatus, e.message, e.status)
			return
		}
	}
	current, ok := fsDocuments.Get(name)
	if !ok {
		current = FSDocument{Name: name}
	}
	var req FSDocument
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid document body: %v", err)
		return
	}
	// An absent updateMask.fieldPaths query param replaces wholesale; a present
	// one merges the listed paths.
	var mask *[]string
	if paths, ok := r.URL.Query()["updateMask.fieldPaths"]; ok {
		mask = &paths
	}
	current.Fields = fsApplyUpdateMask(current.Fields, req.Fields, mask)
	sim.WriteJSON(w, http.StatusOK, fsPutDocument(current))
}

func handleFSDeleteDocument(w http.ResponseWriter, r *http.Request) {
	name := fsFullName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "docPath"))
	if !fsDocuments.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Document not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// fsWriteError carries a gRPC-mapped error for a single write: the HTTP status,
// the canonical status string, the numeric gRPC code (for batchWrite's per-write
// google.rpc.Status), and the message.
type fsWriteError struct {
	httpStatus int
	status     string
	grpcCode   int
	message    string
}

// gRPC canonical codes used by the Firestore preconditions.
const (
	fsGRPCNotFound           = 5
	fsGRPCAlreadyExists      = 6
	fsGRPCFailedPrecondition = 9
)

// fsPreconditionFromQuery extracts a Precondition from the currentDocument.*
// query params the REST patch endpoint carries, or nil when none are present.
func fsPreconditionFromQuery(r *http.Request) *fsPrecondition {
	q := r.URL.Query()
	existsRaw := q.Get("currentDocument.exists")
	updateTime := q.Get("currentDocument.updateTime")
	if existsRaw == "" && updateTime == "" {
		return nil
	}
	pre := &fsPrecondition{UpdateTime: updateTime}
	if existsRaw != "" {
		if b, err := strconv.ParseBool(existsRaw); err == nil {
			pre.Exists = &b
		}
	}
	return pre
}

// fsEvalPrecondition evaluates a Write.currentDocument precondition against the
// stored document. A nil precondition (or a satisfied one) returns nil; a
// mismatch returns the gRPC-mapped error. Firestore REST maps ALREADY_EXISTS →
// 409, NOT_FOUND → 404, FAILED_PRECONDITION → 400.
func fsEvalPrecondition(name string, pre *fsPrecondition) *fsWriteError {
	if pre == nil {
		return nil
	}
	existing, ok := fsDocuments.Get(name)
	if pre.Exists != nil {
		if *pre.Exists && !ok {
			return &fsWriteError{http.StatusNotFound, "NOT_FOUND", fsGRPCNotFound,
				fmt.Sprintf("No document to update: %s", name)}
		}
		if !*pre.Exists && ok {
			return &fsWriteError{http.StatusConflict, "ALREADY_EXISTS", fsGRPCAlreadyExists,
				fmt.Sprintf("Document already exists: %s", name)}
		}
	}
	if pre.UpdateTime != "" {
		if !ok || existing.UpdateTime != pre.UpdateTime {
			return &fsWriteError{http.StatusBadRequest, "FAILED_PRECONDITION", fsGRPCFailedPrecondition,
				"the stored version does not match the required base version"}
		}
	}
	return nil
}

// fsApplyTransforms applies each FieldTransform against the (already
// update-applied) field map in declaration order, mutating fields in place and
// returning the resulting Value for each transform (the transformResults the
// write result carries, one per transform, in order). now is the commit time
// used for setToServerValue: REQUEST_TIME.
func fsApplyTransforms(fields map[string]FSValue, transforms []fsFieldTransform, now string) []FSValue {
	results := make([]FSValue, 0, len(transforms))
	for _, t := range transforms {
		var result FSValue
		switch {
		case fsEnum(t.SetToServerValue) == "REQUEST_TIME":
			result = FSValue{TimestampValue: now}
		case t.Increment != nil:
			result = fsIncrement(fields[t.FieldPath], *t.Increment)
		case t.Maximum != nil:
			result = fsMaxMin(fields[t.FieldPath], *t.Maximum, true)
		case t.Minimum != nil:
			result = fsMaxMin(fields[t.FieldPath], *t.Minimum, false)
		case t.AppendMissingElements != nil:
			result = fsArrayUnion(fields[t.FieldPath], t.AppendMissingElements)
		case t.RemoveAllFromArray != nil:
			result = fsArrayRemove(fields[t.FieldPath], t.RemoveAllFromArray)
		default:
			// Unknown/unset transform — record the field's current value.
			result = fields[t.FieldPath]
		}
		fields[t.FieldPath] = result
		results = append(results, result)
	}
	return results
}

// fsIncrement adds operand to current. If either operand is a doubleValue the
// result is a doubleValue; otherwise both are integers and the result is an
// integerValue. A missing/non-numeric current is treated as 0.
func fsIncrement(current, operand FSValue) FSValue {
	cur, _ := fsNumeric(current)
	op, _ := fsNumeric(operand)
	sum := cur + op
	if current.DoubleValue != nil || operand.DoubleValue != nil {
		v := sum
		return FSValue{DoubleValue: &v}
	}
	return FSValue{IntegerValue: strconv.FormatInt(int64(sum), 10)}
}

// fsMaxMin returns the per-type max (wantMax) or min of current and operand.
// A missing/non-numeric current yields operand. The result preserves the
// chosen operand's exact representation (integerValue vs doubleValue).
func fsMaxMin(current, operand FSValue, wantMax bool) FSValue {
	_, curNum := fsNumeric(current)
	if !curNum {
		return operand
	}
	cmp := fsCompareValues(current, operand)
	if (wantMax && cmp >= 0) || (!wantMax && cmp <= 0) {
		return current
	}
	return operand
}

// fsArrayUnion appends each element of add not already present in current's
// arrayValue (by fsValuesEqual), returning the resulting arrayValue.
func fsArrayUnion(current FSValue, add *FSArrayValue) FSValue {
	out := &FSArrayValue{}
	if current.ArrayValue != nil {
		out.Values = append(out.Values, current.ArrayValue.Values...)
	}
	for _, e := range add.Values {
		present := false
		for _, x := range out.Values {
			if fsValuesEqual(x, e) {
				present = true
				break
			}
		}
		if !present {
			out.Values = append(out.Values, e)
		}
	}
	return FSValue{ArrayValue: out}
}

// fsArrayRemove drops every element of current's arrayValue that equals any
// element of remove (by fsValuesEqual), returning the resulting arrayValue.
func fsArrayRemove(current FSValue, remove *FSArrayValue) FSValue {
	out := &FSArrayValue{Values: []FSValue{}}
	if current.ArrayValue == nil {
		return FSValue{ArrayValue: out}
	}
	for _, x := range current.ArrayValue.Values {
		drop := false
		for _, e := range remove.Values {
			if fsValuesEqual(x, e) {
				drop = true
				break
			}
		}
		if !drop {
			out.Values = append(out.Values, x)
		}
	}
	return FSValue{ArrayValue: out}
}

// fsApplyWrite executes a single Write against the store: enforces the
// precondition, applies the update (honoring updateMask), applies the inline
// updateTransforms and the standalone/trailing DocumentTransform, and persists.
// It returns the per-write result map (updateTime plus transformResults when any
// transform ran), or the gRPC-mapped error on a precondition/validation
// failure. A standalone transform (no Update) still creates/updates the targeted
// doc.
func fsApplyWrite(wr fsWrite) (map[string]any, *fsWriteError) {
	if wr.Delete != "" {
		if e := fsEvalPrecondition(wr.Delete, wr.CurrentDocument); e != nil {
			return nil, e
		}
		fsDocuments.Delete(wr.Delete)
		return map[string]any{"updateTime": fsNow()}, nil
	}

	name := ""
	if wr.Update != nil {
		name = wr.Update.Name
	} else if wr.Transform != nil {
		name = wr.Transform.Document
	}
	if name == "" {
		return nil, &fsWriteError{http.StatusBadRequest, "INVALID_ARGUMENT", 3,
			"write.update.name or write.transform.document is required"}
	}
	if e := fsEvalPrecondition(name, wr.CurrentDocument); e != nil {
		return nil, e
	}

	existing, _ := fsDocuments.Get(name)
	merged := FSDocument{Name: name, CreateTime: existing.CreateTime}
	if wr.Update != nil {
		// An absent updateMask replaces wholesale; a present one (even empty)
		// merges. A transform-only Update sends an empty present mask that must
		// preserve existing fields so the transform reads them.
		var mask *[]string
		if wr.UpdateMask != nil {
			fp := wr.UpdateMask.FieldPaths
			mask = &fp
		}
		merged.Fields = fsApplyUpdateMask(existing.Fields, wr.Update.Fields, mask)
	} else {
		// Standalone transform: start from the existing field set.
		merged.Fields = map[string]FSValue{}
		for k, v := range existing.Fields {
			merged.Fields[k] = v
		}
	}
	if merged.Fields == nil {
		merged.Fields = map[string]FSValue{}
	}

	now := fsNow()
	var transformResults []FSValue
	transformResults = append(transformResults, fsApplyTransforms(merged.Fields, wr.UpdateTransforms, now)...)
	if wr.Transform != nil {
		transformResults = append(transformResults, fsApplyTransforms(merged.Fields, wr.Transform.FieldTransforms, now)...)
	}

	stored := fsPutDocument(merged)
	result := map[string]any{"updateTime": stored.UpdateTime}
	if len(transformResults) > 0 {
		result["transformResults"] = transformResults
	}
	return result, nil
}

func handleFSCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Writes      []fsWrite `json:"writes"`
		Transaction string    `json:"transaction"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid commit body: %v", err)
		return
	}
	// A transactional commit consumes the transaction: an unknown or
	// already-consumed token is rejected, and committing always retires the
	// token (a transaction can be committed at most once, success or failure).
	if req.Transaction != "" {
		if _, ok := fsTransactions.Get(req.Transaction); !ok {
			sim.GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
			return
		}
		fsTransactions.Delete(req.Transaction)
	}
	writeResults := make([]map[string]any, 0, len(req.Writes))
	for _, wr := range req.Writes {
		res, e := fsApplyWrite(wr)
		if e != nil {
			// commit is atomic: the first failing write aborts the whole commit
			// with the gRPC-mapped HTTP error.
			sim.GCPError(w, e.httpStatus, e.message, e.status)
			return
		}
		writeResults = append(writeResults, res)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"writeResults": writeResults, "commitTime": fsNow()})
}

func handleFSBatchWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Writes []fsWrite `json:"writes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid batchWrite body: %v", err)
		return
	}
	// batchWrite is non-atomic: each write succeeds or fails independently and
	// the response always carries HTTP 200 with a per-write google.rpc.Status.
	status := make([]map[string]any, 0, len(req.Writes))
	results := make([]map[string]any, 0, len(req.Writes))
	for _, wr := range req.Writes {
		res, e := fsApplyWrite(wr)
		if e != nil {
			results = append(results, map[string]any{})
			status = append(status, map[string]any{"code": e.grpcCode, "message": e.message})
			continue
		}
		results = append(results, res)
		status = append(status, map[string]any{"code": 0})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"writeResults": results, "status": status})
}

// fsStreamArray writes a Firestore server-streaming response (runQuery /
// batchGet) as an incrementally-flushed JSON array — the faithful REST wire
// form of a gRPC server stream. No Content-Length is set, so the Go HTTP server
// uses chunked transfer-encoding; each element is written and flushed as its own
// chunk, exactly as real Firestore streams results over HTTP. Client
// cancellation is honored: if the caller disconnects mid-stream, emission stops.
// The full array is still valid JSON once received, so SDK/CLI clients that
// buffer the whole body parse it identically.
func fsStreamArray(w http.ResponseWriter, r *http.Request, elements []map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writeChunk := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	if !writeChunk([]byte("[")) {
		return
	}
	for i, el := range elements {
		select {
		case <-r.Context().Done():
			return // client disconnected mid-stream — stop emitting
		default:
		}
		b, err := json.Marshal(el)
		if err != nil {
			return
		}
		if i > 0 {
			b = append([]byte{','}, b...)
		}
		if !writeChunk(b) {
			return
		}
	}
	writeChunk([]byte("]"))
}

func handleFSBatchGet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Documents   []string `json:"documents"`
		Transaction string   `json:"transaction"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid batchGet body: %v", err)
		return
	}
	readTime, ok := fsReadTimeForTxn(req.Transaction)
	if !ok {
		sim.GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
		return
	}
	out := make([]map[string]any, 0, len(req.Documents))
	for _, name := range req.Documents {
		if doc, ok := fsDocuments.Get(name); ok {
			out = append(out, map[string]any{"found": doc, "readTime": readTime})
		} else {
			out = append(out, map[string]any{"missing": name, "readTime": readTime})
		}
	}
	fsStreamArray(w, r, out)
}

func handleFSRunRootQuery(w http.ResponseWriter, r *http.Request) {
	handleFSRunQuery(w, r, "")
}

func handleFSRunQuery(w http.ResponseWriter, r *http.Request, parentPath string) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	parent := fsFullName(project, database, strings.Trim(parentPath, "/"))
	var req struct {
		StructuredQuery fsStructuredQuery `json:"structuredQuery"`
		Transaction     string            `json:"transaction"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid runQuery body: %v", err)
		return
	}
	readTime, ok := fsReadTimeForTxn(req.Transaction)
	if !ok {
		sim.GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
		return
	}
	q := req.StructuredQuery
	if len(q.From) == 0 || q.From[0].CollectionID == "" {
		sim.GCPError(w, http.StatusBadRequest, "structuredQuery.from[0].collectionId is required", "INVALID_ARGUMENT")
		return
	}
	collection := strings.TrimSuffix(parent, "/") + "/" + q.From[0].CollectionID
	docs := fsDocuments.Filter(func(d FSDocument) bool {
		return fsCollectionParent(d.Name) == collection && fsWhereMatches(d, q.Where)
	})

	// Ordering: explicit orderBy fields (with direction) take precedence,
	// otherwise documents sort by name (Firestore's implicit __name__ order).
	sort.SliceStable(docs, func(i, j int) bool {
		for _, ob := range q.OrderBy {
			path := ob.Field.FieldPath
			if path == "" || path == "__name__" {
				continue
			}
			cmp := fsCompareValues(docs[i].Fields[path], docs[j].Fields[path])
			if cmp == 0 {
				continue
			}
			if fsEnum(ob.Direction) == "DESCENDING" {
				return cmp > 0
			}
			return cmp < 0
		}
		return docs[i].Name < docs[j].Name
	})

	// Cursors: applied against the ordered slice, before offset/limit. startAt
	// trims the leading edge, endAt trims the trailing edge, each positioned by
	// comparing the cursor values against the orderBy fields (honoring `before`).
	if q.StartAt != nil {
		docs = docs[fsCursorIndex(docs, q.OrderBy, q.StartAt):]
	}
	if q.EndAt != nil {
		docs = docs[:fsCursorIndex(docs, q.OrderBy, q.EndAt)]
	}

	// Honor offset + limit (the StructuredQuery cursor controls page size).
	if q.Offset > 0 {
		if q.Offset >= len(docs) {
			docs = nil
		} else {
			docs = docs[q.Offset:]
		}
	}
	if q.Limit != nil && *q.Limit >= 0 && *q.Limit < len(docs) {
		docs = docs[:*q.Limit]
	}

	out := make([]map[string]any, 0, len(docs)+1)
	for _, d := range docs {
		out = append(out, map[string]any{"document": fsProjectDocument(d, q.Select), "readTime": readTime})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"readTime": readTime, "done": true})
	}
	fsStreamArray(w, r, out)
}

// fsCompareToCursor compares a document's orderBy key against the cursor values,
// returning -1/0/1. Only as many orderBy positions as the cursor supplies are
// compared (a partial cursor is a prefix). The `__name__` order field compares
// by document name.
func fsCompareToCursor(d FSDocument, orderBy []fsOrderBy, cur *fsCursor) int {
	for i, cv := range cur.Values {
		if i >= len(orderBy) {
			break
		}
		ob := orderBy[i]
		var cmp int
		if ob.Field.FieldPath == "__name__" {
			cmp = strings.Compare(d.Name, cv.ReferenceValue)
		} else {
			cmp = fsCompareValues(d.Fields[ob.Field.FieldPath], cv)
		}
		if fsEnum(ob.Direction) == "DESCENDING" {
			cmp = -cmp
		}
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

// fsCursorIndex finds the boundary index in the ordered docs for a cursor. The
// same comparison serves both bounds: as a startAt it is the inclusive lower
// bound, and as an endAt it is the exclusive upper bound. before=true treats a
// document equal to the cursor as on/after the boundary (StartAt / EndBefore);
// before=false places it before the boundary (StartAfter / EndAt). docs are
// already ordered.
func fsCursorIndex(docs []FSDocument, orderBy []fsOrderBy, cur *fsCursor) int {
	for i := range docs {
		cmp := fsCompareToCursor(docs[i], orderBy, cur)
		if (cur.Before && cmp >= 0) || (!cur.Before && cmp > 0) {
			return i
		}
	}
	return len(docs)
}

// fsProjectDocument applies a StructuredQuery select projection: the returned
// document carries only the listed field paths (preserving name/timestamps). A
// nil projection returns the document unchanged; a projection of only
// `__name__` yields a keys-only document (empty Fields).
func fsProjectDocument(d FSDocument, sel *fsProjection) FSDocument {
	if sel == nil {
		return d
	}
	projected := FSDocument{
		Name:       d.Name,
		CreateTime: d.CreateTime,
		UpdateTime: d.UpdateTime,
		Fields:     map[string]FSValue{},
	}
	for _, f := range sel.Fields {
		if f.FieldPath == "__name__" {
			continue
		}
		if v, ok := d.Fields[f.FieldPath]; ok {
			projected.Fields[f.FieldPath] = v
		}
	}
	return projected
}

type fsOrderBy struct {
	Field struct {
		FieldPath string `json:"fieldPath"`
	} `json:"field"`
	Direction fsDirectionEnum `json:"direction"`
}

type fsCursor struct {
	Values []FSValue `json:"values,omitempty"`
	Before bool      `json:"before,omitempty"`
}

type fsProjection struct {
	Fields []struct {
		FieldPath string `json:"fieldPath"`
	} `json:"fields,omitempty"`
}

type fsStructuredQuery struct {
	From []struct {
		CollectionID string `json:"collectionId"`
	} `json:"from"`
	Where   *fsFilter     `json:"where,omitempty"`
	OrderBy []fsOrderBy   `json:"orderBy,omitempty"`
	StartAt *fsCursor     `json:"startAt,omitempty"`
	EndAt   *fsCursor     `json:"endAt,omitempty"`
	Select  *fsProjection `json:"select,omitempty"`
	Limit   *int          `json:"limit,omitempty"`
	Offset  int           `json:"offset,omitempty"`
}

type fsFilter struct {
	CompositeFilter *struct {
		Op      fsCompositeOpEnum `json:"op"`
		Filters []fsFilter        `json:"filters"`
	} `json:"compositeFilter,omitempty"`
	FieldFilter *struct {
		Field struct {
			FieldPath string `json:"fieldPath"`
		} `json:"field"`
		Op    fsFieldOpEnum `json:"op"`
		Value FSValue       `json:"value"`
	} `json:"fieldFilter,omitempty"`
	UnaryFilter *struct {
		Op    fsUnaryOpEnum `json:"op"`
		Field struct {
			FieldPath string `json:"fieldPath"`
		} `json:"field"`
	} `json:"unaryFilter,omitempty"`
}

// fsWhereMatches evaluates a Firestore structured-query filter (field, unary, or
// composite AND/OR) against a document. A nil filter matches every document.
func fsWhereMatches(d FSDocument, f *fsFilter) bool {
	if f == nil {
		return true
	}
	switch {
	case f.CompositeFilter != nil:
		isOr := fsEnum(f.CompositeFilter.Op) == "OR"
		for i := range f.CompositeFilter.Filters {
			m := fsWhereMatches(d, &f.CompositeFilter.Filters[i])
			if isOr && m {
				return true
			}
			if !isOr && !m {
				return false
			}
		}
		return !isOr
	case f.UnaryFilter != nil:
		got, ok := d.Fields[f.UnaryFilter.Field.FieldPath]
		switch fsEnum(f.UnaryFilter.Op) {
		case "IS_NULL":
			return ok && got.NullValue != nil
		case "IS_NOT_NULL":
			return ok && got.NullValue == nil
		default:
			return false
		}
	case f.FieldFilter != nil:
		got, ok := d.Fields[f.FieldFilter.Field.FieldPath]
		want := f.FieldFilter.Value
		switch fsEnum(f.FieldFilter.Op) {
		case "EQUAL":
			return ok && fsValuesEqual(got, want)
		case "NOT_EQUAL":
			return ok && !fsValuesEqual(got, want)
		case "LESS_THAN":
			return ok && fsCompareValues(got, want) < 0
		case "LESS_THAN_OR_EQUAL":
			return ok && fsCompareValues(got, want) <= 0
		case "GREATER_THAN":
			return ok && fsCompareValues(got, want) > 0
		case "GREATER_THAN_OR_EQUAL":
			return ok && fsCompareValues(got, want) >= 0
		case "IN":
			return ok && fsValueInArray(got, want)
		case "NOT_IN":
			return ok && !fsValueInArray(got, want)
		case "ARRAY_CONTAINS":
			return ok && fsArrayContains(got, want)
		case "ARRAY_CONTAINS_ANY":
			return ok && fsArrayContainsAny(got, want)
		default:
			return false
		}
	default:
		return true
	}
}

// fsValueInArray reports whether got equals any element of want's arrayValue.
func fsValueInArray(got, want FSValue) bool {
	if want.ArrayValue == nil {
		return false
	}
	for _, v := range want.ArrayValue.Values {
		if fsValuesEqual(got, v) {
			return true
		}
	}
	return false
}

// fsArrayContains reports whether got's arrayValue contains want.
func fsArrayContains(got, want FSValue) bool {
	if got.ArrayValue == nil {
		return false
	}
	for _, v := range got.ArrayValue.Values {
		if fsValuesEqual(v, want) {
			return true
		}
	}
	return false
}

// fsArrayContainsAny reports whether got's arrayValue contains any element of
// want's arrayValue.
func fsArrayContainsAny(got, want FSValue) bool {
	if want.ArrayValue == nil {
		return false
	}
	for _, v := range want.ArrayValue.Values {
		if fsArrayContains(got, v) {
			return true
		}
	}
	return false
}

// fsCompareValues orders two scalar Firestore values of the same type, returning
// -1, 0, or 1. Numeric values (integer/double) compare numerically; strings and
// timestamps compare lexically; unknown/mixed types fall back to string form.
func fsCompareValues(a, b FSValue) int {
	an, aIsNum := fsNumeric(a)
	bn, bIsNum := fsNumeric(b)
	if aIsNum && bIsNum {
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	}
	as, bs := fsScalarString(a), fsScalarString(b)
	return strings.Compare(as, bs)
}

func fsNumeric(v FSValue) (float64, bool) {
	if v.DoubleValue != nil {
		return *v.DoubleValue, true
	}
	if v.IntegerValue != "" {
		if n, err := strconv.ParseFloat(v.IntegerValue, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func fsScalarString(v FSValue) string {
	switch {
	case v.StringValue != "":
		return v.StringValue
	case v.TimestampValue != "":
		return v.TimestampValue
	case v.ReferenceValue != "":
		return v.ReferenceValue
	default:
		return fmt.Sprintf("%#v", v)
	}
}

// fsValuesEqual reports whether two Firestore values are equal by Firestore's
// own equality semantics: integerValue and doubleValue unify by numeric value
// (so 1 == 1.0), scalars compare structurally, and arrayValue/mapValue compare
// element-/field-wise recursively. This drives EQUAL/NOT_EQUAL/IN/NOT_IN and
// the ARRAY_CONTAINS family.
func fsValuesEqual(a, b FSValue) bool {
	an, aIsNum := fsNumeric(a)
	bn, bIsNum := fsNumeric(b)
	if aIsNum || bIsNum {
		return aIsNum && bIsNum && an == bn
	}
	switch {
	case a.NullValue != nil || b.NullValue != nil:
		return a.NullValue != nil && b.NullValue != nil
	case a.BooleanValue != nil || b.BooleanValue != nil:
		return a.BooleanValue != nil && b.BooleanValue != nil && *a.BooleanValue == *b.BooleanValue
	case a.StringValue != "" || b.StringValue != "":
		return a.StringValue == b.StringValue
	case a.TimestampValue != "" || b.TimestampValue != "":
		return a.TimestampValue == b.TimestampValue
	case a.BytesValue != "" || b.BytesValue != "":
		return a.BytesValue == b.BytesValue
	case a.ReferenceValue != "" || b.ReferenceValue != "":
		return a.ReferenceValue == b.ReferenceValue
	case a.ArrayValue != nil || b.ArrayValue != nil:
		return fsArraysEqual(a.ArrayValue, b.ArrayValue)
	case a.MapValue != nil || b.MapValue != nil:
		return fsMapsEqual(a.MapValue, b.MapValue)
	default:
		// Both values are empty/untyped — treat as equal.
		return true
	}
}

func fsArraysEqual(a, b *FSArrayValue) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if len(a.Values) != len(b.Values) {
		return false
	}
	for i := range a.Values {
		if !fsValuesEqual(a.Values[i], b.Values[i]) {
			return false
		}
	}
	return true
}

func fsMapsEqual(a, b *FSMapValue) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for k, av := range a.Fields {
		bv, ok := b.Fields[k]
		if !ok || !fsValuesEqual(av, bv) {
			return false
		}
	}
	return true
}

// ----------------------------------------------------------------------------
// Firestore v1 Admin API surface
//
// The admin resources (databases, collectionGroups/{cg}/indexes,
// collectionGroups/{cg}/fields, backupSchedules, locations/{loc}/backups,
// userCreds, operations) implement real CRUD. Each resource body is stored as
// the typed-value JSON the client sent (a fidelity-preserving map: every field
// the client supplies round-trips back, and the sim only sets the
// server-managed fields the Discovery schema marks output-only — name, state,
// createTime/updateTime, uid). Mutating operations the Discovery marks
// long-running (database create/patch/delete, the database export/import/
// bulkDelete/restore/clone verbs, index create, field patch) return a
// GoogleLongrunningOperation, persisted under the database-scoped operations
// collection so a subsequent operations.get returns the same record.

// fsResource is the stored shape for an admin resource: the client-supplied
// body plus the server-assigned resource name. Storing the whole body as a map
// preserves every field the SDK sent so the read-back is byte-faithful and
// never drops an unmodeled field.
type fsResource struct {
	Name string         `json:"name"`
	Body map[string]any `json:"body"`
}

// fsAdminOp is the stored shape of a Firestore admin long-running operation.
// The sim performs no asynchronous work, so every operation is created already
// done with its embedded response, matching what real Firestore returns once
// the underlying resource has settled. Metadata is omitted unless the operation
// has a concrete metadata message the admin SDK's proto registry can resolve
// (IndexOperationMetadata / FieldOperationMetadata) — emitting an unregistered
// @type there makes the client's Any unmarshal fail.
type fsAdminOp struct {
	Name     string         `json:"name"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Done     bool           `json:"done"`
	Response map[string]any `json:"response"`
}

var (
	fsDatabases       sim.Store[fsResource]
	fsIndexes         sim.Store[fsResource]
	fsFields          sim.Store[fsResource]
	fsBackupSchedules sim.Store[fsResource]
	fsBackups         sim.Store[fsResource]
	fsUserCreds       sim.Store[fsResource]
	fsAdminOps        sim.Store[fsAdminOp]
)

func registerFirestoreAdmin(srv *sim.Server) {
	fsDatabases = sim.MakeStore[fsResource](srv.DB(), "firestore_databases")
	fsIndexes = sim.MakeStore[fsResource](srv.DB(), "firestore_indexes")
	fsFields = sim.MakeStore[fsResource](srv.DB(), "firestore_fields")
	fsBackupSchedules = sim.MakeStore[fsResource](srv.DB(), "firestore_backup_schedules")
	fsBackups = sim.MakeStore[fsResource](srv.DB(), "firestore_backups")
	fsUserCreds = sim.MakeStore[fsResource](srv.DB(), "firestore_user_creds")
	fsAdminOps = sim.MakeStore[fsAdminOp](srv.DB(), "firestore_admin_operations")

	// Locations (locations.list / locations.get) are shared GCP surface mounted
	// once on the collapsed sim mux (by the eventarc slice); the firestore-v1
	// Discovery doc lists them too, so they count toward firestore coverage
	// without a duplicate registration here.

	// Databases (admin CRUD). The colon-verb forms (databases:clone /
	// databases:restore at the collection level, and the per-database
	// {dbAction} that captures "{db}:exportDocuments" etc.) fan in on a
	// single action parameter that also carries the ":verb" suffix.
	srv.HandleFunc("POST /v1/projects/{project}/databases", handleFSDatabasesCollection)
	srv.HandleFunc("GET /v1/projects/{project}/databases", handleFSListDatabases)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}", handleFSGetDatabase)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}", handleFSPatchDatabase)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}", handleFSDeleteDatabase)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{dbAction}", handleFSDatabaseVerb)

	// Indexes (collectionGroups/{cg}/indexes).
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes", handleFSCreateIndex)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes", handleFSListIndexes)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}", handleFSGetIndex)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}", handleFSDeleteIndex)

	// Fields (collectionGroups/{cg}/fields).
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields", handleFSListFields)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}", handleFSGetField)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}", handleFSPatchField)

	// Backup schedules (databases/{db}/backupSchedules).
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/backupSchedules", handleFSCreateBackupSchedule)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/backupSchedules", handleFSListBackupSchedules)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/backupSchedules/{bs}", handleFSGetBackupSchedule)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/backupSchedules/{bs}", handleFSPatchBackupSchedule)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/backupSchedules/{bs}", handleFSDeleteBackupSchedule)

	// Backups (locations/{loc}/backups).
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/backups", handleFSListBackups)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/backups/{backup}", handleFSGetBackup)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/backups/{backup}", handleFSDeleteBackup)

	// User creds (databases/{db}/userCreds).
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/userCreds", handleFSUserCredsCollection)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/userCreds", handleFSListUserCreds)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/userCreds/{uc}", handleFSGetUserCreds)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/userCreds/{uc}", handleFSDeleteUserCreds)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/userCreds/{ucAction}", handleFSUserCredsVerb)

	// Operations (databases/{db}/operations).
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/operations", handleFSListOperations)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/operations/{operation}", handleFSGetOperation)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/operations/{operation}", handleFSDeleteOperation)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/operations/{opAction}", handleFSCancelOperation)
}

// fsReadBody decodes the request body into a fidelity-preserving map. An empty
// body yields an empty (non-nil) map.
func fsReadBody(r *http.Request) (map[string]any, error) {
	body := map[string]any{}
	if r.Body == nil {
		return body, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if err.Error() == "EOF" {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	fsNormalizeAdminEnums(body)
	return body, nil
}

// Enum name tables for the admin enum fields, ordered so the proto wire number
// indexes the slice (matching the Discovery enum declaration order). The gax
// REST transport serializes enums with UseEnumNumbers, so the sim receives
// numbers; real Firestore returns the string names on read-back, and the
// Discovery schema declares these fields as strings — so the sim normalizes
// numeric enum tokens to their canonical name at ingest.
var (
	fsDatabaseTypeEnum  = []string{"DATABASE_TYPE_UNSPECIFIED", "FIRESTORE_NATIVE", "DATASTORE_MODE"}
	fsIndexQueryScope   = []string{"QUERY_SCOPE_UNSPECIFIED", "COLLECTION", "COLLECTION_GROUP", "COLLECTION_RECURSIVE"}
	fsIndexApiScope     = []string{"ANY_API", "DATASTORE_MODE_API", "MONGODB_COMPATIBLE_API"}
	fsIndexState        = []string{"STATE_UNSPECIFIED", "CREATING", "READY", "NEEDS_REPAIR"}
	fsIndexFieldOrder   = []string{"ORDER_UNSPECIFIED", "ASCENDING", "DESCENDING"}
	fsIndexFieldArrayCf = []string{"ARRAY_CONFIG_UNSPECIFIED", "CONTAINS"}
)

// fsEnumName converts a numeric enum token to its canonical name using names
// (indexed by proto number). A non-numeric value (already a string name) or an
// out-of-range number is returned unchanged.
func fsEnumName(v any, names []string) any {
	n, ok := v.(float64)
	if !ok {
		return v
	}
	i := int(n)
	if i < 0 || i >= len(names) {
		return v
	}
	return names[i]
}

func fsAsMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// fsNormalizeIndexEnums converts the enum fields of an Index map (queryScope,
// apiScope, state, and each field's order/arrayConfig) from wire numbers to
// names.
func fsNormalizeIndexEnums(idx map[string]any) {
	if v, ok := idx["queryScope"]; ok {
		idx["queryScope"] = fsEnumName(v, fsIndexQueryScope)
	}
	if v, ok := idx["apiScope"]; ok {
		idx["apiScope"] = fsEnumName(v, fsIndexApiScope)
	}
	if v, ok := idx["state"]; ok {
		idx["state"] = fsEnumName(v, fsIndexState)
	}
	fields, ok := idx["fields"].([]any)
	if !ok {
		return
	}
	for _, f := range fields {
		fm, ok := fsAsMap(f)
		if !ok {
			continue
		}
		if v, ok := fm["order"]; ok {
			fm["order"] = fsEnumName(v, fsIndexFieldOrder)
		}
		if v, ok := fm["arrayConfig"]; ok {
			fm["arrayConfig"] = fsEnumName(v, fsIndexFieldArrayCf)
		}
	}
}

// fsNormalizeAdminEnums normalizes every numeric enum token in an admin
// resource body (database/index/field shapes) to its canonical string name.
func fsNormalizeAdminEnums(body map[string]any) {
	if v, ok := body["type"]; ok {
		body["type"] = fsEnumName(v, fsDatabaseTypeEnum)
	}
	// Index-shaped body (createIndex payload).
	fsNormalizeIndexEnums(body)
	// Field-shaped body: indexConfig.indexes[] are full Index shapes.
	if ic, ok := fsAsMap(body["indexConfig"]); ok {
		if idxs, ok := ic["indexes"].([]any); ok {
			for _, i := range idxs {
				if im, ok := fsAsMap(i); ok {
					fsNormalizeIndexEnums(im)
				}
			}
		}
	}
}

// fsNewAdminOp creates a completed Firestore admin LRO for the given resource
// (already carrying its server-assigned name) and persists it under the
// database-scoped operations collection. The returned Operation embeds the
// resource as a protobuf.Any (with @type) — what the Firestore admin SDK polls
// for and unmarshals once done. metadata, when non-nil, must carry an @type
// naming a concrete OperationMetadata message the admin SDK's proto registry
// resolves (IndexOperationMetadata / FieldOperationMetadata) plus only that
// schema's fields; a nil metadata omits the field (database operations, whose
// metadata messages are not part of this client's registry).
func fsNewAdminOp(project, database string, resource map[string]any, typeName string, metadata map[string]any) fsAdminOp {
	opID := generateUUID()
	resp := map[string]any{}
	for k, v := range resource {
		resp[k] = v
	}
	resp["@type"] = typeName
	op := fsAdminOp{
		Name:     fmt.Sprintf("projects/%s/databases/%s/operations/%s", project, database, opID),
		Done:     true,
		Response: resp,
		Metadata: metadata,
	}
	fsAdminOps.Put(op.Name, op)
	return op
}

// fsIndexOpMetadata builds an IndexOperationMetadata Any for a completed index
// operation, carrying the schema's index/state/start-end-time fields.
func fsIndexOpMetadata(indexName string) map[string]any {
	return map[string]any{
		"@type":     "type.googleapis.com/google.firestore.admin.v1.IndexOperationMetadata",
		"index":     indexName,
		"state":     "SUCCESSFUL",
		"startTime": nowTimestamp(),
		"endTime":   nowTimestamp(),
	}
}

// fsFieldOpMetadata builds a FieldOperationMetadata Any for a completed field
// operation, carrying the schema's field/state/start-end-time fields.
func fsFieldOpMetadata(fieldName string) map[string]any {
	return map[string]any{
		"@type":     "type.googleapis.com/google.firestore.admin.v1.FieldOperationMetadata",
		"field":     fieldName,
		"state":     "SUCCESSFUL",
		"startTime": nowTimestamp(),
		"endTime":   nowTimestamp(),
	}
}

// --- Databases ---

func fsDatabaseName(project, database string) string {
	return fmt.Sprintf("projects/%s/databases/%s", project, database)
}

func handleFSDatabasesCollection(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	dbID := r.URL.Query().Get("databaseId")
	if dbID == "" {
		sim.GCPError(w, http.StatusBadRequest, "databaseId is required", "INVALID_ARGUMENT")
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid database body: %v", err)
		return
	}
	name := fsDatabaseName(project, dbID)
	if _, ok := fsDatabases.Get(name); ok {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Database already exists: %s", name)
		return
	}
	now := nowTimestamp()
	body["name"] = name
	body["uid"] = generateUUID()
	body["createTime"] = now
	body["updateTime"] = now
	fsDatabases.Put(name, fsResource{Name: name, Body: body})
	op := fsNewAdminOp(project, dbID, body, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleFSListDatabases(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := fmt.Sprintf("projects/%s/databases/", project)
	dbs := fsDatabases.Filter(func(d fsResource) bool { return strings.HasPrefix(d.Name, prefix) })
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Name < dbs[j].Name })
	out := make([]map[string]any, 0, len(dbs))
	for _, d := range dbs {
		out = append(out, d.Body)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"databases": out})
}

func handleFSGetDatabase(w http.ResponseWriter, r *http.Request) {
	name := fsDatabaseName(sim.PathParam(r, "project"), sim.PathParam(r, "database"))
	d, ok := fsDatabases.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSPatchDatabase(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	name := fsDatabaseName(project, database)
	d, ok := fsDatabases.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid database body: %v", err)
		return
	}
	for k, v := range body {
		d.Body[k] = v
	}
	d.Body["name"] = name
	d.Body["updateTime"] = nowTimestamp()
	fsDatabases.Put(name, d)
	op := fsNewAdminOp(project, database, d.Body, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleFSDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	name := fsDatabaseName(project, database)
	d, ok := fsDatabases.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
		return
	}
	fsDatabases.Delete(name)
	op := fsNewAdminOp(project, database, d.Body, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleFSDatabaseVerb fans in the per-database colon verbs that ride a single
// path segment ({db}:exportDocuments / :importDocuments / :bulkDeleteDocuments)
// plus the collection-level databases:clone / databases:restore — Go's mux
// cannot spell a ":verb" as its own pattern, so these arrive captured in
// dbAction. Each returns a long-running Operation.
func handleFSDatabaseVerb(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	dbAction := sim.PathParam(r, "dbAction")
	dbID, verb, ok := strings.Cut(dbAction, ":")
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown database verb: %s", dbAction)
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	switch verb {
	case "exportDocuments", "importDocuments", "bulkDeleteDocuments":
		name := fsDatabaseName(project, dbID)
		if _, exists := fsDatabases.Get(name); !exists {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
			return
		}
		op := fsNewAdminOp(project, dbID, map[string]any{"name": name}, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
		sim.WriteJSON(w, http.StatusOK, op)
	case "clone", "restore":
		// databases:clone / databases:restore — collection-level verbs (dbID
		// here is the literal "databases" segment, not a database id). The new
		// database id rides the request body (databaseId).
		newID, _ := body["databaseId"].(string)
		if newID == "" {
			newID = generateUUID()
		}
		name := fsDatabaseName(project, newID)
		now := nowTimestamp()
		db := map[string]any{"name": name, "uid": generateUUID(), "createTime": now, "updateTime": now}
		fsDatabases.Put(name, fsResource{Name: name, Body: db})
		op := fsNewAdminOp(project, newID, db, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
		sim.WriteJSON(w, http.StatusOK, op)
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown database verb: %s", verb)
	}
}

// --- Indexes ---

func handleFSCreateIndex(w http.ResponseWriter, r *http.Request) {
	project, database, cg := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg")
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid index body: %v", err)
		return
	}
	indexID := generateUUID()
	name := fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/indexes/%s", project, database, cg, indexID)
	body["name"] = name
	body["state"] = "READY"
	fsIndexes.Put(name, fsResource{Name: name, Body: body})
	op := fsNewAdminOp(project, database, body, "type.googleapis.com/google.firestore.admin.v1.Index", fsIndexOpMetadata(name))
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleFSListIndexes(w http.ResponseWriter, r *http.Request) {
	project, database, cg := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg")
	prefix := fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/indexes/", project, database, cg)
	idxs := fsIndexes.Filter(func(d fsResource) bool { return strings.HasPrefix(d.Name, prefix) })
	sort.Slice(idxs, func(i, j int) bool { return idxs[i].Name < idxs[j].Name })
	out := make([]map[string]any, 0, len(idxs))
	for _, d := range idxs {
		out = append(out, d.Body)
	}
	resp := map[string]any{"indexes": out}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleFSGetIndex(w http.ResponseWriter, r *http.Request) {
	project, database, cg, index := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg"), sim.PathParam(r, "index")
	name := fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/indexes/%s", project, database, cg, index)
	d, ok := fsIndexes.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Index not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteIndex(w http.ResponseWriter, r *http.Request) {
	project, database, cg, index := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg"), sim.PathParam(r, "index")
	name := fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/indexes/%s", project, database, cg, index)
	if !fsIndexes.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Index not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// --- Fields ---

func fsFieldName(project, database, cg, field string) string {
	return fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/fields/%s", project, database, cg, field)
}

func handleFSListFields(w http.ResponseWriter, r *http.Request) {
	project, database, cg := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg")
	prefix := fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/fields/", project, database, cg)
	fields := fsFields.Filter(func(d fsResource) bool { return strings.HasPrefix(d.Name, prefix) })
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	out := make([]map[string]any, 0, len(fields))
	for _, d := range fields {
		out = append(out, d.Body)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"fields": out})
}

func handleFSGetField(w http.ResponseWriter, r *http.Request) {
	name := fsFieldName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg"), sim.PathParam(r, "field"))
	d, ok := fsFields.Get(name)
	if !ok {
		// A field with default (empty) config is implicitly present in
		// Firestore; the SDK reads it back even when it was never written.
		// Echo the empty default rather than 404 so the read-back is faithful.
		sim.WriteJSON(w, http.StatusOK, map[string]any{"name": name})
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSPatchField(w http.ResponseWriter, r *http.Request) {
	project, database, cg, field := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg"), sim.PathParam(r, "field")
	name := fsFieldName(project, database, cg, field)
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid field body: %v", err)
		return
	}
	d, ok := fsFields.Get(name)
	if !ok {
		d = fsResource{Name: name, Body: map[string]any{}}
	}
	for k, v := range body {
		d.Body[k] = v
	}
	d.Body["name"] = name
	fsFields.Put(name, d)
	op := fsNewAdminOp(project, database, d.Body, "type.googleapis.com/google.firestore.admin.v1.Field", fsFieldOpMetadata(name))
	sim.WriteJSON(w, http.StatusOK, op)
}

// --- Backup schedules ---

func fsBackupScheduleName(project, database, bs string) string {
	return fmt.Sprintf("projects/%s/databases/%s/backupSchedules/%s", project, database, bs)
}

func handleFSCreateBackupSchedule(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid backupSchedule body: %v", err)
		return
	}
	bsID := generateUUID()
	name := fsBackupScheduleName(project, database, bsID)
	now := nowTimestamp()
	body["name"] = name
	body["createTime"] = now
	body["updateTime"] = now
	fsBackupSchedules.Put(name, fsResource{Name: name, Body: body})
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleFSListBackupSchedules(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	prefix := fmt.Sprintf("projects/%s/databases/%s/backupSchedules/", project, database)
	bss := fsBackupSchedules.Filter(func(d fsResource) bool { return strings.HasPrefix(d.Name, prefix) })
	sort.Slice(bss, func(i, j int) bool { return bss[i].Name < bss[j].Name })
	out := make([]map[string]any, 0, len(bss))
	for _, d := range bss {
		out = append(out, d.Body)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"backupSchedules": out})
}

func handleFSGetBackupSchedule(w http.ResponseWriter, r *http.Request) {
	name := fsBackupScheduleName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "bs"))
	d, ok := fsBackupSchedules.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup schedule not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSPatchBackupSchedule(w http.ResponseWriter, r *http.Request) {
	name := fsBackupScheduleName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "bs"))
	d, ok := fsBackupSchedules.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup schedule not found: %s", name)
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid backupSchedule body: %v", err)
		return
	}
	for k, v := range body {
		d.Body[k] = v
	}
	d.Body["name"] = name
	d.Body["updateTime"] = nowTimestamp()
	fsBackupSchedules.Put(name, d)
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteBackupSchedule(w http.ResponseWriter, r *http.Request) {
	name := fsBackupScheduleName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "bs"))
	if !fsBackupSchedules.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup schedule not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// --- Backups ---

func fsBackupName(project, location, backup string) string {
	return fmt.Sprintf("projects/%s/locations/%s/backups/%s", project, location, backup)
}

func handleFSListBackups(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/backups/", project, location)
	bks := fsBackups.Filter(func(d fsResource) bool { return strings.HasPrefix(d.Name, prefix) })
	sort.Slice(bks, func(i, j int) bool { return bks[i].Name < bks[j].Name })
	out := make([]map[string]any, 0, len(bks))
	for _, d := range bks {
		out = append(out, d.Body)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"backups": out})
}

func handleFSGetBackup(w http.ResponseWriter, r *http.Request) {
	name := fsBackupName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "backup"))
	d, ok := fsBackups.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteBackup(w http.ResponseWriter, r *http.Request) {
	name := fsBackupName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "backup"))
	if !fsBackups.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// --- User creds ---

func fsUserCredsName(project, database, uc string) string {
	return fmt.Sprintf("projects/%s/databases/%s/userCreds/%s", project, database, uc)
}

func handleFSUserCredsCollection(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	ucID := r.URL.Query().Get("userCredsId")
	if ucID == "" {
		ucID = generateUUID()
	}
	body, err := fsReadBody(r)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid userCreds body: %v", err)
		return
	}
	name := fsUserCredsName(project, database, ucID)
	body["name"] = name
	body["state"] = "ENABLED"
	body["createTime"] = nowTimestamp()
	body["securePassword"] = generateUUID()
	fsUserCreds.Put(name, fsResource{Name: name, Body: body})
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleFSListUserCreds(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	prefix := fmt.Sprintf("projects/%s/databases/%s/userCreds/", project, database)
	ucs := fsUserCreds.Filter(func(d fsResource) bool { return strings.HasPrefix(d.Name, prefix) })
	sort.Slice(ucs, func(i, j int) bool { return ucs[i].Name < ucs[j].Name })
	out := make([]map[string]any, 0, len(ucs))
	for _, d := range ucs {
		out = append(out, d.Body)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"userCreds": out})
}

func handleFSGetUserCreds(w http.ResponseWriter, r *http.Request) {
	name := fsUserCredsName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "uc"))
	d, ok := fsUserCreds.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "User creds not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteUserCreds(w http.ResponseWriter, r *http.Request) {
	name := fsUserCredsName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "uc"))
	if !fsUserCreds.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "User creds not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleFSUserCredsVerb fans in the userCreds :enable / :disable /
// :resetPassword colon verbs (ucAction captures "{uc}:verb"), each of which
// mutates and returns the UserCreds resource.
func handleFSUserCredsVerb(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	ucAction := sim.PathParam(r, "ucAction")
	ucID, verb, ok := strings.Cut(ucAction, ":")
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown userCreds verb: %s", ucAction)
		return
	}
	name := fsUserCredsName(project, database, ucID)
	d, ok := fsUserCreds.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "User creds not found: %s", name)
		return
	}
	switch verb {
	case "enable":
		d.Body["state"] = "ENABLED"
	case "disable":
		d.Body["state"] = "DISABLED"
	case "resetPassword":
		d.Body["securePassword"] = generateUUID()
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown userCreds verb: %s", verb)
		return
	}
	fsUserCreds.Put(name, d)
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

// --- Operations ---

func fsOperationName(project, database, op string) string {
	return fmt.Sprintf("projects/%s/databases/%s/operations/%s", project, database, op)
}

func handleFSListOperations(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	prefix := fmt.Sprintf("projects/%s/databases/%s/operations/", project, database)
	ops := fsAdminOps.Filter(func(o fsAdminOp) bool { return strings.HasPrefix(o.Name, prefix) })
	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operations": ops})
}

func handleFSGetOperation(w http.ResponseWriter, r *http.Request) {
	name := fsOperationName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "operation"))
	op, ok := fsAdminOps.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Operation not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleFSDeleteOperation(w http.ResponseWriter, r *http.Request) {
	name := fsOperationName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "operation"))
	if !fsAdminOps.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Operation not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleFSCancelOperation handles operations/{op}:cancel (opAction captures
// "{op}:cancel"). Cancelling a settled operation is a no-op that returns Empty.
func handleFSCancelOperation(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	opAction := sim.PathParam(r, "opAction")
	opID, verb, ok := strings.Cut(opAction, ":")
	if !ok || verb != "cancel" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown operation verb: %s", opAction)
		return
	}
	name := fsOperationName(project, database, opID)
	if _, ok := fsAdminOps.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Operation not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
