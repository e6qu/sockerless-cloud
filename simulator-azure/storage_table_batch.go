package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Table Storage transactional batch (`POST /$batch`).
//
// aztables' TransactionalBatch posts a `multipart/mixed` body whose single part
// is itself a `multipart/mixed` change-set; each change-set part is a complete
// HTTP request (Insert / Merge / Update / Delete on an entity). Every operation
// must address one partition of one table, and an entity at most once. The set
// is all-or-nothing: the batch holds the partition's lock, runs each operation
// through the entity handlers, and on the first failure restores the entities
// it had changed. The response mirrors the request — an outer multipart/mixed
// wrapping a change-set of per-operation HTTP responses.
//
// Reference: https://learn.microsoft.com/rest/api/storageservices/performing-entity-group-transactions

// tableBatchMaxOps is the most operations one change set may hold.
const tableBatchMaxOps = 100

func handleTableBatch(w http.ResponseWriter, r *http.Request, account string) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeTableODataError(w, "InvalidInput", "batch request must be multipart/mixed", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeTableODataError(w, "InvalidInput", "failed to read batch body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ops, changesetBoundary, err := parseTableBatch(body, params["boundary"])
	if err != nil {
		writeTableODataError(w, "InvalidInput", err.Error(), http.StatusBadRequest)
		return
	}

	if len(ops) > tableBatchMaxOps {
		writeTableODataError(w, "InvalidInput", fmt.Sprintf("The batch request contains %d operations; a change set holds at most %d.", len(ops), tableBatchMaxOps), http.StatusBadRequest)
		return
	}
	partition := ""
	rows := map[string]bool{}
	for _, op := range ops {
		table, pk, rk, err := tableBatchTarget(op)
		if err != nil {
			writeTableODataError(w, "InvalidInput", err.Error(), http.StatusBadRequest)
			return
		}
		if key := tablePartitionKey(account, table, pk); partition == "" {
			partition = key
		} else if key != partition {
			tableBatchRefusal(w, changesetBoundary, "CommandsInBatchActOnDifferentPartitions",
				"All commands in a batch must operate on same entity group.")
			return
		}
		if rows[rk] {
			tableBatchRefusal(w, changesetBoundary, "InvalidDuplicateRow",
				"The batch request contains multiple changes with same row key. An entity can appear only once in a batch request.")
			return
		}
		rows[rk] = true
	}

	release := tablePartitionLocks.Lock(true, partition)
	defer release()
	txn := &tableTxn{before: map[string]*TableEntity{}}
	results := make([]tableBatchOpResult, 0, len(ops))
	for _, op := range ops {
		rec := &batchRecorder{header: http.Header{}, code: http.StatusOK}
		req, err := http.NewRequest(op.method, op.url, bytes.NewReader(op.body))
		if err != nil {
			txn.rollback()
			writeTableODataError(w, "InvalidInput", "invalid batch sub-request: "+err.Error(), http.StatusBadRequest)
			return
		}
		for k, vs := range op.headers {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		if !serveTableEntityOp(rec, req, account, txn) {
			writeTableODataError(rec, "InvalidUri", "Unrecognized batch sub-request path", http.StatusBadRequest)
		}
		result := tableBatchOpResult{status: rec.code, headers: rec.header, body: rec.buf.Bytes()}
		if rec.code >= 400 {
			txn.rollback()
			writeTableBatchSingleError(w, changesetBoundary, result.status, result.body)
			return
		}
		results = append(results, result)
	}

	writeTableBatchResponse(w, changesetBoundary, results)
}

type tableBatchOp struct {
	method  string
	url     string
	headers http.Header
	body    []byte
}

// parseTableBatch unwraps the outer multipart/mixed → the change-set
// multipart/mixed → each HTTP request part. Returns the ops and the change-set
// boundary (reused in the response).
func parseTableBatch(body []byte, outerBoundary string) ([]tableBatchOp, string, error) {
	if outerBoundary == "" {
		return nil, "", fmt.Errorf("batch missing multipart boundary")
	}
	outer := multipart.NewReader(bytes.NewReader(body), outerBoundary)
	var ops []tableBatchOp
	changesetBoundary := ""
	for {
		part, err := outer.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("malformed batch envelope: %v", err)
		}
		ct := part.Header.Get("Content-Type")
		mediaType, params, perr := mime.ParseMediaType(ct)
		partBytes, rerr := io.ReadAll(part)
		if rerr != nil {
			return nil, "", fmt.Errorf("truncated batch part: %v", rerr)
		}
		if perr == nil && strings.HasPrefix(mediaType, "multipart/") {
			changesetBoundary = params["boundary"]
			csOps, err := parseTableChangeset(partBytes, changesetBoundary)
			if err != nil {
				return nil, "", err
			}
			ops = append(ops, csOps...)
			continue
		}
		// A bare (non-changeset) operation part is also valid for read-only
		// batches; treat it as a single op.
		op, err := parseTableBatchRequest(partBytes)
		if err != nil {
			return nil, "", err
		}
		ops = append(ops, op)
	}
	if changesetBoundary == "" {
		// No nested changeset — synthesize a boundary for the response.
		changesetBoundary = "changesetresponse_" + sim.NewUUID()
	}
	return ops, changesetBoundary, nil
}

func parseTableChangeset(body []byte, boundary string) ([]tableBatchOp, error) {
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var ops []tableBatchOp
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("malformed batch change-set: %v", err)
		}
		partBytes, rerr := io.ReadAll(part)
		if rerr != nil {
			return nil, fmt.Errorf("truncated batch change-set part: %v", rerr)
		}
		op, err := parseTableBatchRequest(partBytes)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// parseTableBatchRequest parses one inner HTTP request body of the form
//
//	POST https://acct.table.host/Table HTTP/1.1
//	Content-Type: application/json
//	<blank>
//	{json body}
func parseTableBatchRequest(part []byte) (tableBatchOp, error) {
	// The part itself begins with the application/http headers; the actual HTTP
	// request follows after a blank line. Strip everything up to the request
	// line (the first token that looks like METHOD URL HTTP/1.1).
	reader := bufio.NewReader(bytes.NewReader(part))
	var requestLine string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return tableBatchOp{}, fmt.Errorf("batch sub-request has no request line")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "HTTP/1.1") {
			requestLine = trimmed
			break
		}
		// application/http part headers (Content-Type, Content-Transfer-Encoding)
		// precede the request line; skip them.
		if err != nil {
			return tableBatchOp{}, fmt.Errorf("batch sub-request has no request line")
		}
	}
	fields := strings.Fields(requestLine)
	if len(fields) < 2 {
		return tableBatchOp{}, fmt.Errorf("malformed batch request line: %q", requestLine)
	}
	method, rawURL := fields[0], fields[1]

	tp := textproto.NewReader(reader)
	mimeHeader, err := tp.ReadMIMEHeader()
	if err != nil && err != io.EOF {
		return tableBatchOp{}, fmt.Errorf("malformed batch sub-request headers: %v", err)
	}
	bodyBytes, rerr := io.ReadAll(tp.R)
	if rerr != nil {
		return tableBatchOp{}, fmt.Errorf("truncated batch sub-request body: %v", rerr)
	}
	return tableBatchOp{
		method:  method,
		url:     rawURL,
		headers: http.Header(mimeHeader),
		body:    bytes.TrimSpace(bodyBytes),
	}, nil
}

// serveTableEntityOp routes an entity operation — addressed by
// /{table}(PartitionKey='X',RowKey='Y') or by /{table} — to its handler, inside
// txn when a batch runs it. It reports false for a path that addresses no
// entity operation.
func serveTableEntityOp(w http.ResponseWriter, req *http.Request, account string, txn *tableTxn) bool {
	path := strings.TrimPrefix(req.URL.Path, "/")
	if i := strings.Index(path, "(PartitionKey="); i > 0 {
		table := path[:i]
		pk, rk := parsePKRK(strings.TrimSuffix(path[i+1:], ")"))
		switch req.Method {
		case http.MethodGet:
			handleEntityGet(w, req, account, table, pk, rk, txn)
		case http.MethodPut:
			handleEntityUpsert(w, req, account, table, pk, rk, false, txn)
		case http.MethodPatch, "MERGE":
			handleEntityUpsert(w, req, account, table, pk, rk, true, txn)
		case http.MethodDelete:
			handleEntityDelete(w, req, account, table, pk, rk, txn)
		default:
			writeTableODataError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return true
	}
	if !strings.Contains(path, "/") && path != "" {
		switch req.Method {
		case http.MethodPost:
			handleEntityInsert(w, req, account, path, txn)
		case http.MethodGet:
			handleEntityQuery(w, req, account, path)
		default:
			writeTableODataError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return true
	}
	return false
}

// tableBatchTarget is the table, partition and row a batch operation
// addresses: from its URL, or for an insert from its body.
func tableBatchTarget(op tableBatchOp) (table, pk, rk string, err error) {
	u, err := url.Parse(op.url)
	if err != nil {
		return "", "", "", err
	}
	path := strings.TrimPrefix(u.Path, "/")
	if i := strings.Index(path, "(PartitionKey="); i > 0 {
		pk, rk := parsePKRK(strings.TrimSuffix(path[i+1:], ")"))
		return path[:i], pk, rk, nil
	}
	var keys struct {
		PartitionKey string
		RowKey       string
	}
	if err := json.Unmarshal(op.body, &keys); err != nil {
		return "", "", "", fmt.Errorf("batch insert body: %v", err)
	}
	return path, keys.PartitionKey, keys.RowKey, nil
}

// tableTxn is an entity group transaction in flight. It holds its partition's
// lock and remembers each entity's state before the transaction first changed
// it, so a failed transaction restores exactly those entities and nothing a
// writer outside the partition did meanwhile.
type tableTxn struct {
	before map[string]*TableEntity
}

func (x *tableTxn) remember(key string) {
	if x == nil {
		return
	}
	if _, seen := x.before[key]; seen {
		return
	}
	if e, ok := tableEntities.Get(key); ok {
		x.before[key] = &e
		return
	}
	x.before[key] = nil
}

func (x *tableTxn) rollback() {
	for key, e := range x.before {
		if e == nil {
			tableEntities.Delete(key)
			continue
		}
		tableEntities.Put(key, *e)
	}
}

// ── in-memory response recorder ──────────────────────────────────────────────

type batchRecorder struct {
	header http.Header
	code   int
	buf    bytes.Buffer
	wrote  bool
}

func (r *batchRecorder) Header() http.Header { return r.header }
func (r *batchRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.code = code
		r.wrote = true
	}
}
func (r *batchRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.buf.Write(b)
}

// ── response encoding ────────────────────────────────────────────────────────

type tableBatchOpResult struct {
	status  int
	headers http.Header
	body    []byte
}

func httpStatusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return t
	}
	return "Status"
}

// writeTableBatchResponse encodes the per-op responses into the multipart/mixed
// batch response real Azure Tables returns: an outer batch boundary wrapping a
// change-set boundary, each part an `application/http` response.
func writeTableBatchResponse(w http.ResponseWriter, changesetBoundary string, results []tableBatchOpResult) {
	batchBoundary := "batchresponse_" + sim.NewUUID()
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "--%s\r\n", batchBoundary)
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", changesetBoundary)
	for i, res := range results {
		fmt.Fprintf(&buf, "--%s\r\n", changesetBoundary)
		buf.WriteString("Content-Type: application/http\r\n")
		buf.WriteString("Content-Transfer-Encoding: binary\r\n\r\n")
		fmt.Fprintf(&buf, "HTTP/1.1 %d %s\r\n", res.status, httpStatusText(res.status))
		writeBatchPartHeaders(&buf, res, i)
		// Content-Length frames the inner response body so http.ReadResponse in
		// the SDK doesn't block/EOF reading it.
		fmt.Fprintf(&buf, "Content-Length: %d\r\n", len(res.body))
		buf.WriteString("\r\n")
		if len(res.body) > 0 {
			buf.Write(res.body)
		}
		buf.WriteString("\r\n")
	}
	fmt.Fprintf(&buf, "--%s--\r\n", changesetBoundary)
	fmt.Fprintf(&buf, "--%s--\r\n", batchBoundary)

	w.Header().Set("Content-Type", "multipart/mixed; boundary="+batchBoundary)
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(buf.Bytes())
}

// tableBatchRefusal answers a change set the service refuses before running
// any of it, the way it answers a failed operation: inside the batch response.
func tableBatchRefusal(w http.ResponseWriter, changesetBoundary, code, message string) {
	rec := &batchRecorder{header: http.Header{}}
	writeTableODataError(rec, code, message, http.StatusBadRequest)
	writeTableBatchSingleError(w, changesetBoundary, http.StatusBadRequest, rec.buf.Bytes())
}

// writeTableBatchSingleError encodes a failed transaction: the batch response
// carries the single failing op's error (the aztables SDK surfaces this as the
// transaction error). The whole batch was already rolled back by the caller.
func writeTableBatchSingleError(w http.ResponseWriter, changesetBoundary string, status int, body []byte) {
	batchBoundary := "batchresponse_" + sim.NewUUID()
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--%s\r\n", batchBoundary)
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", changesetBoundary)
	fmt.Fprintf(&buf, "--%s\r\n", changesetBoundary)
	buf.WriteString("Content-Type: application/http\r\n")
	buf.WriteString("Content-Transfer-Encoding: binary\r\n\r\n")
	fmt.Fprintf(&buf, "HTTP/1.1 %d %s\r\n", status, httpStatusText(status))
	buf.WriteString("Content-Type: application/json;odata=minimalmetadata;streaming=true;charset=utf-8\r\n")
	fmt.Fprintf(&buf, "Content-Length: %d\r\n", len(body))
	buf.WriteString("\r\n")
	buf.Write(body)
	buf.WriteString("\r\n")
	fmt.Fprintf(&buf, "--%s--\r\n", changesetBoundary)
	fmt.Fprintf(&buf, "--%s--\r\n", batchBoundary)

	w.Header().Set("Content-Type", "multipart/mixed; boundary="+batchBoundary)
	// Real Tables returns 202 for the batch envelope even on a failed op; the
	// failing status lives inside the multipart part.
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(buf.Bytes())
}

func writeBatchPartHeaders(buf *bytes.Buffer, res tableBatchOpResult, contentID int) {
	if ct := res.headers.Get("Content-Type"); ct != "" {
		fmt.Fprintf(buf, "Content-Type: %s\r\n", ct)
	} else {
		buf.WriteString("Content-Type: application/json;odata=minimalmetadata;streaming=true;charset=utf-8\r\n")
	}
	if etag := res.headers.Get("ETag"); etag != "" {
		fmt.Fprintf(buf, "ETag: %s\r\n", etag)
	}
	fmt.Fprintf(buf, "Content-ID: %d\r\n", contentID)
}
