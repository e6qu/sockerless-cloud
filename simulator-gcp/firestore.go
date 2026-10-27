package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// fsEnum is a Firestore enum value as its canonical uppercase name. The gax
// REST transport sends enum numbers (UseEnumNumbers) and the low-level REST
// client sends names; Firestore accepts both, and fsDecodeEnum normalizes
// either.
type fsEnum string

// fsDecodeEnum normalizes a Firestore enum JSON token, name or proto number,
// to its canonical name.
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

// FSDocument is a Firestore document in its typed-value JSON shape.

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

// fsWrite mirrors Firestore's Write: an update or delete with an optional
// updateMask, a standalone or trailing transform, inline updateTransforms, and
// a currentDocument precondition.
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

var (
	fsDocuments sim.Store[FSDocument]
	// fsDocumentChanges announces a document's name on every write of it,
	// which wakes the Listen streams.
	fsDocumentChanges = signals.New[string]()
)

func registerFirestore(srv *sim.Server) {
	fsDocuments = sim.Announcing(sim.MakeStore[FSDocument](srv.DB(), "firestore_documents"), fsDocumentChanges)

	fsTransactions = sim.MakeStore[fsTxn](srv.DB(), "firestore_transactions")

	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:beginTransaction", handleFSBeginTransaction)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:rollback", handleFSRollback)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:commit", handleFSCommit)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:batchGet", handleFSBatchGet)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:batchWrite", handleFSBatchWrite)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:runQuery", handleFSRunRootQuery)
	// The same custom methods on the documents root, which the catch-all below
	// cannot reach: it matches only paths that continue past the collection.
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:listCollectionIds",
		func(w http.ResponseWriter, r *http.Request) { fsHandleListCollectionIds(w, r, "") })
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:runAggregationQuery",
		func(w http.ResponseWriter, r *http.Request) { fsHandleRunAggregationQuery(w, r, "") })
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:partitionQuery",
		func(w http.ResponseWriter, r *http.Request) { fsHandlePartitionQuery(w, r, "") })
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents/{postPath...}", handleFSPostDocuments)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/documents/{docPath...}", handleFSGetOrList)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/documents/{docPath...}", handleFSPatchDocument)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/documents/{docPath...}", handleFSDeleteDocument)

	registerFirestoreAdmin(srv)
}

func fsDatabasePrefix(project, database string) string {
	return "projects/" + project + "/databases/" + database + "/documents"
}

// fsNow stamps a Firestore commit time at microsecond granularity, as
// Firestore does. A watching client treats a document whose updateTime equals
// the one it holds as unchanged, so millisecond granularity would hide the
// second of two writes inside one millisecond.
func fsNow() string {
	return time.Now().UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
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

// fsDocumentsUnder returns the documents keep accepts among those whose names
// begin with prefix. Documents are stored under their names, so the documents
// under one parent are one key range of the store and a query reads no others.
func fsDocumentsUnder(prefix string, keep func(FSDocument) bool) []FSDocument {
	docs := make([]FSDocument, 0)
	for _, entry := range fsDocuments.ListPrefix(prefix) {
		if keep(entry.Item) {
			docs = append(docs, entry.Item)
		}
	}
	return docs
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
	// collection to create a document in.
	if last := path[strings.LastIndex(path, "/")+1:]; strings.Contains(last, ":") {
		verb := last[strings.Index(last, ":")+1:]
		parent := strings.TrimSuffix(path, ":"+verb)
		if fsDocumentVerbHandled(w, r, parent, verb) {
			return
		}
		gcpMethodNotFound(w)
		return
	}
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	docID := r.URL.Query().Get("documentId")
	if docID == "" {
		docID = sim.NewUUID()
	}
	var req FSDocument
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid document body: %v", err)
		return
	}
	name := fsFullName(project, database, strings.Trim(path, "/")+"/"+docID)
	if _, ok := fsDocuments.Get(name); ok {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Document already exists: %s", name)
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Document not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, doc)
}

func handleFSListDocuments(w http.ResponseWriter, r *http.Request, collection string) {
	prefix := strings.TrimSuffix(collection, "/") + "/"
	docs := fsDocumentsUnder(prefix, func(d FSDocument) bool {
		rest := strings.TrimPrefix(d.Name, prefix)
		return rest != "" && !strings.Contains(rest, "/")
	})
	page, next, ok := paginateList(w, r, docs, gcpAsc(func(d FSDocument) string { return d.Name }))
	if !ok {
		return
	}
	resp := map[string]any{"documents": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// fsApplyUpdateMask merges incoming fields into existing per the updateMask.
// An absent mask (nil) replaces the document; a present mask, even an empty
// one, writes only the listed top-level paths (set if in the body, deleted if
// not) and keeps every other field. The SDK's transform-only Update sends an
// empty present mask and relies on the existing fields surviving for the
// transform to read.
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
			GCPError(w, e.httpStatus, e.message, e.status)
			return
		}
	}
	current, ok := fsDocuments.Get(name)
	if !ok {
		current = FSDocument{Name: name}
	}
	var req FSDocument
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid document body: %v", err)
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Document not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// fsWriteError is one write's error: HTTP status, canonical status, gRPC code
// (for batchWrite's per-write google.rpc.Status) and message.
type fsWriteError struct {
	httpStatus int
	status     string
	grpcCode   int
	message    string
}

// The gRPC canonical codes Firestore preconditions use.
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

// fsEvalPrecondition checks a Write.currentDocument precondition against the
// stored document, nil when it holds. Firestore REST maps ALREADY_EXISTS to
// 409, NOT_FOUND to 404 and FAILED_PRECONDITION to 400.
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

// fsApplyTransforms applies each FieldTransform in order to the already-updated
// fields, in place, and returns each transform's result value. now is the
// commit time setToServerValue: REQUEST_TIME writes.
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
			// An unset transform reports the field's current value.
			result = fields[t.FieldPath]
		}
		fields[t.FieldPath] = result
		results = append(results, result)
	}
	return results
}

// fsIncrement adds operand to current: a doubleValue if either is one,
// otherwise an integerValue. A missing or non-numeric current counts as 0.
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

// fsMaxMin returns the max (wantMax) or min of current and operand in the
// chosen operand's representation; a missing or non-numeric current yields
// operand.
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

// fsApplyWrite executes one Write: it checks the precondition, applies the
// update under its updateMask, the inline updateTransforms and the standalone
// or trailing transform, and persists. It returns the write result
// (updateTime, plus transformResults when a transform ran) or the error. A
// standalone transform also creates or updates its document.
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
		// An absent updateMask replaces; a present one, even empty, merges, which a
		// transform-only Update relies on.
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid commit body: %v", err)
		return
	}
	// A transactional commit consumes its token, success or failure; an unknown
	// or consumed token is rejected.
	if req.Transaction != "" {
		if _, ok := fsTransactions.Get(req.Transaction); !ok {
			GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
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
			GCPError(w, e.httpStatus, e.message, e.status)
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid batchWrite body: %v", err)
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

// fsStreamArray writes a server-streaming response (runQuery, batchGet) as a
// JSON array flushed element by element over chunked encoding, as Firestore
// streams over HTTP, and stops when the client disconnects. The complete body
// is still one valid JSON array.
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
			return // client disconnected
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid batchGet body: %v", err)
		return
	}
	readTime, ok := fsReadTimeForTxn(req.Transaction)
	if !ok {
		GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid runQuery body: %v", err)
		return
	}
	readTime, ok := fsReadTimeForTxn(req.Transaction)
	if !ok {
		GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
		return
	}
	q := req.StructuredQuery
	if len(q.From) == 0 || q.From[0].CollectionID == "" {
		GCPError(w, http.StatusBadRequest, "structuredQuery.from[0].collectionId is required", "INVALID_ARGUMENT")
		return
	}
	collection := strings.TrimSuffix(parent, "/") + "/" + q.From[0].CollectionID
	docs := fsDocumentsUnder(collection+"/", func(d FSDocument) bool {
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

	// offset and limit apply after the cursors.
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

// fsCompareToCursor compares a document's orderBy key with the cursor values
// (-1, 0, 1), over as many positions as the cursor supplies; `__name__`
// compares document names.
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

// fsCursorIndex finds a cursor's boundary in the ordered docs: the inclusive
// lower bound as a start, the exclusive upper bound as an end. before=true
// puts an equal document on or after the boundary (StartAt, EndBefore),
// before=false before it (StartAfter, EndAt).
func fsCursorIndex(docs []FSDocument, orderBy []fsOrderBy, cur *fsCursor) int {
	for i := range docs {
		cmp := fsCompareToCursor(docs[i], orderBy, cur)
		if (cur.Before && cmp >= 0) || (!cur.Before && cmp > 0) {
			return i
		}
	}
	return len(docs)
}

// fsProjectDocument applies a select projection, keeping name and timestamps;
// a nil projection keeps every field, and `__name__` alone yields a keys-only
// document.
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

// fsCompareValues orders two scalar Firestore values of one type: numbers
// numerically, strings and timestamps lexically, anything else by string form.
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

// fsValuesEqual applies Firestore's equality: integerValue and doubleValue
// unify by number (1 == 1.0), scalars compare structurally, and arrays and maps
// recursively.
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

// The Firestore v1 admin resources (databases, indexes, fields, backup
// schedules, backups, userCreds, operations) store the body the client sent
// and set only the output-only members. The methods the Discovery document
// marks long-running answer an operation persisted under the database's
// operations collection.

// fsResource is an admin resource: the client's body, every field kept, and
// its server-assigned name.
type fsResource struct {
	Name string         `json:"name"`
	Body map[string]any `json:"body"`
}

// fsAdminOp is a Firestore admin operation, created done with its response.
// Metadata appears only for IndexOperationMetadata and FieldOperationMetadata,
// which the admin SDK's registry resolves; an unregistered @type fails the
// client's Any unmarshal.
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

	// locations.list and .get are mounted once, by the Eventarc slice.

	// The per-database colon verbs arrive in one {dbAction} segment with the verb
	// attached.
	srv.HandleFunc("POST /v1/projects/{project}/databases", handleFSDatabasesCollection)
	// databases:clone and :restore arrive on the collection path with the verb
	// on the segment, so they take their own mount rather than a query
	// parameter the create path would misread as a database id.
	srv.HandleFunc("POST /v1/projects/{project}/{databasesVerb}", handleFSDatabasesVerb)
	registerFSDocumentVerbs(srv)
	srv.HandleFunc("GET /v1/projects/{project}/databases", handleFSListDatabases)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}", handleFSGetDatabase)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}", handleFSPatchDatabase)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}", handleFSDeleteDatabase)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{dbAction}", handleFSDatabaseVerb)

	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes", handleFSCreateIndex)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes", handleFSListIndexes)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}", handleFSGetIndex)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}", handleFSDeleteIndex)

	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields", handleFSListFields)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}", handleFSGetField)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}", handleFSPatchField)

	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/backupSchedules", handleFSCreateBackupSchedule)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/backupSchedules", handleFSListBackupSchedules)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/backupSchedules/{bs}", handleFSGetBackupSchedule)
	srv.HandleFunc("PATCH /v1/projects/{project}/databases/{database}/backupSchedules/{bs}", handleFSPatchBackupSchedule)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/backupSchedules/{bs}", handleFSDeleteBackupSchedule)

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/backups", handleFSListBackups)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/backups/{backup}", handleFSGetBackup)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/backups/{backup}", handleFSDeleteBackup)

	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/userCreds", handleFSUserCredsCollection)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/userCreds", handleFSListUserCreds)
	srv.HandleFunc("GET /v1/projects/{project}/databases/{database}/userCreds/{uc}", handleFSGetUserCreds)
	srv.HandleFunc("DELETE /v1/projects/{project}/databases/{database}/userCreds/{uc}", handleFSDeleteUserCreds)
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/userCreds/{ucAction}", handleFSUserCredsVerb)

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

// Enum names indexed by proto number, in Discovery declaration order. The gax
// REST transport sends numbers; Firestore reads back names, so ingest
// normalizes them.
var (
	fsDatabaseTypeEnum  = []string{"DATABASE_TYPE_UNSPECIFIED", "FIRESTORE_NATIVE", "DATASTORE_MODE"}
	fsIndexQueryScope   = []string{"QUERY_SCOPE_UNSPECIFIED", "COLLECTION", "COLLECTION_GROUP", "COLLECTION_RECURSIVE"}
	fsIndexApiScope     = []string{"ANY_API", "DATASTORE_MODE_API", "MONGODB_COMPATIBLE_API"}
	fsIndexState        = []string{"STATE_UNSPECIFIED", "CREATING", "READY", "NEEDS_REPAIR"}
	fsIndexFieldOrder   = []string{"ORDER_UNSPECIFIED", "ASCENDING", "DESCENDING"}
	fsIndexFieldArrayCf = []string{"ARRAY_CONFIG_UNSPECIFIED", "CONTAINS"}
)

// fsEnumName maps a numeric enum token to its name; a string or out-of-range
// number is returned unchanged.
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

// fsNewAdminOp records a completed Firestore admin operation under the
// database's operations collection, its response the resource as a
// protobuf.Any. metadata, when non-nil, carries an @type the admin SDK
// resolves (IndexOperationMetadata, FieldOperationMetadata) and only that
// schema's fields; database operations omit it.
func fsNewAdminOp(project, database string, resource map[string]any, typeName string, metadata map[string]any) fsAdminOp {
	opID := sim.NewUUID()
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
		"startTime": fsNow(),
		"endTime":   fsNow(),
	}
}

// fsFieldOpMetadata builds a FieldOperationMetadata Any for a completed field
// operation, carrying the schema's field/state/start-end-time fields.
func fsFieldOpMetadata(fieldName string) map[string]any {
	return map[string]any{
		"@type":     "type.googleapis.com/google.firestore.admin.v1.FieldOperationMetadata",
		"field":     fieldName,
		"state":     "SUCCESSFUL",
		"startTime": fsNow(),
		"endTime":   fsNow(),
	}
}

func fsDatabaseName(project, database string) string {
	return fmt.Sprintf("projects/%s/databases/%s", project, database)
}

func handleFSDatabasesCollection(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	dbID := r.URL.Query().Get("databaseId")
	if dbID == "" {
		GCPError(w, http.StatusBadRequest, "databaseId is required", "INVALID_ARGUMENT")
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid database body: %v", err)
		return
	}
	name := fsDatabaseName(project, dbID)
	if _, ok := fsDatabases.Get(name); ok {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Database already exists: %s", name)
		return
	}
	now := fsNow()
	body["name"] = name
	body["uid"] = sim.NewUUID()
	body["createTime"] = now
	body["updateTime"] = now
	fsDatabases.Put(name, fsResource{Name: name, Body: body})
	op := fsNewAdminOp(project, dbID, body, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleFSListDatabases(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := fmt.Sprintf("projects/%s/databases/", project)
	fsWriteBodies(w, fsDatabases, prefix, "databases")
}

func handleFSGetDatabase(w http.ResponseWriter, r *http.Request) {
	name := fsDatabaseName(sim.PathParam(r, "project"), sim.PathParam(r, "database"))
	d, ok := fsDatabases.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSPatchDatabase(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	name := fsDatabaseName(project, database)
	d, ok := fsDatabases.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid database body: %v", err)
		return
	}
	for k, v := range body {
		d.Body[k] = v
	}
	d.Body["name"] = name
	d.Body["updateTime"] = fsNow()
	fsDatabases.Put(name, d)
	op := fsNewAdminOp(project, database, d.Body, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleFSDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	name := fsDatabaseName(project, database)
	d, ok := fsDatabases.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
		return
	}
	fsDatabases.Delete(name)
	op := fsNewAdminOp(project, database, d.Body, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleFSDatabaseVerb serves the colon verbs captured in dbAction
// (exportDocuments, importDocuments, bulkDeleteDocuments, and the
// collection-level clone and restore), each answering an operation.
func handleFSDatabaseVerb(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	dbAction := sim.PathParam(r, "dbAction")
	dbID, verb, ok := strings.Cut(dbAction, ":")
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown database verb: %s", dbAction)
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	switch verb {
	case "exportDocuments", "importDocuments", "bulkDeleteDocuments":
		name := fsDatabaseName(project, dbID)
		if _, exists := fsDatabases.Get(name); !exists {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", name)
			return
		}
		op := fsNewAdminOp(project, dbID, map[string]any{"name": name}, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
		sim.WriteJSON(w, http.StatusOK, op)
	case "clone", "restore":
		// For clone and restore dbID is the literal "databases" segment; the new
		// database ID rides the body.
		newID, _ := body["databaseId"].(string)
		if newID == "" {
			newID = sim.NewUUID()
		}
		name := fsDatabaseName(project, newID)
		now := fsNow()
		db := map[string]any{"name": name, "uid": sim.NewUUID(), "createTime": now, "updateTime": now}
		fsDatabases.Put(name, fsResource{Name: name, Body: db})
		op := fsNewAdminOp(project, newID, db, "type.googleapis.com/google.firestore.admin.v1.Database", nil)
		sim.WriteJSON(w, http.StatusOK, op)
	default:
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown database verb: %s", verb)
	}
}

func handleFSCreateIndex(w http.ResponseWriter, r *http.Request) {
	project, database, cg := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg")
	body, err := fsReadBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid index body: %v", err)
		return
	}
	indexID := sim.NewUUID()
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Index not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteIndex(w http.ResponseWriter, r *http.Request) {
	project, database, cg, index := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg"), sim.PathParam(r, "index")
	name := fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/indexes/%s", project, database, cg, index)
	if !fsIndexes.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Index not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func fsFieldName(project, database, cg, field string) string {
	return fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/fields/%s", project, database, cg, field)
}

func handleFSListFields(w http.ResponseWriter, r *http.Request) {
	project, database, cg := sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg")
	prefix := fmt.Sprintf("projects/%s/databases/%s/collectionGroups/%s/fields/", project, database, cg)
	fsWriteBodies(w, fsFields, prefix, "fields")
}

func handleFSGetField(w http.ResponseWriter, r *http.Request) {
	name := fsFieldName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "cg"), sim.PathParam(r, "field"))
	d, ok := fsFields.Get(name)
	if !ok {
		// Firestore reports a field with default config even when it was never
		// written.
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid field body: %v", err)
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

func fsBackupScheduleName(project, database, bs string) string {
	return fmt.Sprintf("projects/%s/databases/%s/backupSchedules/%s", project, database, bs)
}

func handleFSCreateBackupSchedule(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	body, err := fsReadBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid backupSchedule body: %v", err)
		return
	}
	bsID := sim.NewUUID()
	name := fsBackupScheduleName(project, database, bsID)
	now := fsNow()
	body["name"] = name
	body["createTime"] = now
	body["updateTime"] = now
	fsBackupSchedules.Put(name, fsResource{Name: name, Body: body})
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleFSListBackupSchedules(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	prefix := fmt.Sprintf("projects/%s/databases/%s/backupSchedules/", project, database)
	fsWriteBodies(w, fsBackupSchedules, prefix, "backupSchedules")
}

func handleFSGetBackupSchedule(w http.ResponseWriter, r *http.Request) {
	name := fsBackupScheduleName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "bs"))
	d, ok := fsBackupSchedules.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup schedule not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSPatchBackupSchedule(w http.ResponseWriter, r *http.Request) {
	name := fsBackupScheduleName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "bs"))
	d, ok := fsBackupSchedules.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup schedule not found: %s", name)
		return
	}
	body, err := fsReadBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid backupSchedule body: %v", err)
		return
	}
	for k, v := range body {
		d.Body[k] = v
	}
	d.Body["name"] = name
	d.Body["updateTime"] = fsNow()
	fsBackupSchedules.Put(name, d)
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteBackupSchedule(w http.ResponseWriter, r *http.Request) {
	name := fsBackupScheduleName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "bs"))
	if !fsBackupSchedules.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup schedule not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func fsBackupName(project, location, backup string) string {
	return fmt.Sprintf("projects/%s/locations/%s/backups/%s", project, location, backup)
}

func handleFSListBackups(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/backups/", project, location)
	fsWriteBodies(w, fsBackups, prefix, "backups")
}

func handleFSGetBackup(w http.ResponseWriter, r *http.Request) {
	name := fsBackupName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "backup"))
	d, ok := fsBackups.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteBackup(w http.ResponseWriter, r *http.Request) {
	name := fsBackupName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "backup"))
	if !fsBackups.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func fsUserCredsName(project, database, uc string) string {
	return fmt.Sprintf("projects/%s/databases/%s/userCreds/%s", project, database, uc)
}

func handleFSUserCredsCollection(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	ucID := r.URL.Query().Get("userCredsId")
	if ucID == "" {
		ucID = sim.NewUUID()
	}
	body, err := fsReadBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid userCreds body: %v", err)
		return
	}
	name := fsUserCredsName(project, database, ucID)
	body["name"] = name
	body["state"] = "ENABLED"
	body["createTime"] = fsNow()
	body["securePassword"] = sim.NewUUID()
	fsUserCreds.Put(name, fsResource{Name: name, Body: body})
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleFSListUserCreds(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	prefix := fmt.Sprintf("projects/%s/databases/%s/userCreds/", project, database)
	fsWriteBodies(w, fsUserCreds, prefix, "userCreds")
}

func handleFSGetUserCreds(w http.ResponseWriter, r *http.Request) {
	name := fsUserCredsName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "uc"))
	d, ok := fsUserCreds.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "User creds not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

func handleFSDeleteUserCreds(w http.ResponseWriter, r *http.Request) {
	name := fsUserCredsName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "uc"))
	if !fsUserCreds.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "User creds not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleFSUserCredsVerb serves :enable, :disable and :resetPassword, each
// answering the UserCreds resource.
func handleFSUserCredsVerb(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	ucAction := sim.PathParam(r, "ucAction")
	ucID, verb, ok := strings.Cut(ucAction, ":")
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown userCreds verb: %s", ucAction)
		return
	}
	name := fsUserCredsName(project, database, ucID)
	d, ok := fsUserCreds.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "User creds not found: %s", name)
		return
	}
	switch verb {
	case "enable":
		d.Body["state"] = "ENABLED"
	case "disable":
		d.Body["state"] = "DISABLED"
	case "resetPassword":
		d.Body["securePassword"] = sim.NewUUID()
	default:
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown userCreds verb: %s", verb)
		return
	}
	fsUserCreds.Put(name, d)
	sim.WriteJSON(w, http.StatusOK, d.Body)
}

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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Operation not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleFSDeleteOperation(w http.ResponseWriter, r *http.Request) {
	name := fsOperationName(sim.PathParam(r, "project"), sim.PathParam(r, "database"), sim.PathParam(r, "operation"))
	if !fsAdminOps.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Operation not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleFSCancelOperation serves operations/{op}:cancel; cancelling a settled
// operation answers Empty.
func handleFSCancelOperation(w http.ResponseWriter, r *http.Request) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	opAction := sim.PathParam(r, "opAction")
	opID, verb, ok := strings.Cut(opAction, ":")
	if !ok || verb != "cancel" {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Unknown operation verb: %s", opAction)
		return
	}
	name := fsOperationName(project, database, opID)
	if _, ok := fsAdminOps.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Operation not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// fsWriteBodies answers a list of the resources of store named under prefix,
// in name order, under member.
func fsWriteBodies(w http.ResponseWriter, store sim.Store[fsResource], prefix, member string) {
	found := store.Filter(func(d fsResource) bool { return strings.HasPrefix(d.Name, prefix) })
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	out := make([]map[string]any, 0, len(found))
	for _, d := range found {
		out = append(out, d.Body)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{member: out})
}
