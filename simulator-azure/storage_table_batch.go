package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// Azure Table Storage transactional batch (`POST /$batch`).
//
// aztables' TransactionalBatch posts a `multipart/mixed` body whose single part
// is itself a `multipart/mixed` change-set; each change-set part is a complete
// HTTP request (Insert / Merge / Update / Delete on an entity). The whole set is
// all-or-nothing: we replay each op against the existing entity handlers via an
// in-memory ResponseRecorder, and only commit if every op succeeds. The response
// mirrors the request structure — an outer multipart/mixed wrapping a change-set
// of per-op HTTP responses.
//
// Reference: https://learn.microsoft.com/rest/api/storageservices/performing-entity-group-transactions

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

	// Replay each op against the existing entity handlers, recording the
	// response. On any failure (>=400) the whole batch is rolled back: we run
	// the ops against a copy of the entity store and only commit on success.
	snapshot := snapshotTableEntities(account)
	results := make([]tableBatchOpResult, 0, len(ops))
	failed := false
	var failIdx int
	for i, op := range ops {
		rec := &batchRecorder{header: http.Header{}, code: http.StatusOK}
		req, err := http.NewRequest(op.method, op.url, bytes.NewReader(op.body))
		if err != nil {
			writeTableODataError(w, "InvalidInput", "invalid batch sub-request: "+err.Error(), http.StatusBadRequest)
			return
		}
		for k, vs := range op.headers {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		dispatchTableBatchOp(rec, req, account)
		results = append(results, tableBatchOpResult{status: rec.code, headers: rec.header, body: rec.buf.Bytes()})
		if rec.code >= 400 {
			failed = true
			failIdx = i
			break
		}
	}

	if failed {
		// Roll back any entity mutations the partially-applied ops made.
		restoreTableEntities(account, snapshot)
		// Real Tables returns the failing op's error as the batch response with a
		// Content-ID pointing at the failed change. We surface the failing op's
		// status + body directly (the SDK reads it as the transaction error).
		fr := results[failIdx]
		writeTableBatchSingleError(w, changesetBoundary, fr.status, fr.body)
		return
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
		changesetBoundary = "changesetresponse_" + generateUUID()
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

// dispatchTableBatchOp routes a parsed batch op to the matching entity handler.
// The op URL carries the full data-plane path; we strip the host and dispatch on
// the path shape, reusing the same handlers the non-batch routes use.
func dispatchTableBatchOp(w http.ResponseWriter, req *http.Request, account string) {
	path := strings.TrimPrefix(req.URL.Path, "/")

	if i := strings.Index(path, "(PartitionKey="); i > 0 {
		table := path[:i]
		rest := strings.TrimSuffix(path[i+1:], ")")
		pk, rk := parsePKRK(rest)
		switch req.Method {
		case http.MethodGet:
			handleEntityGet(w, req, account, table, pk, rk)
		case http.MethodPut:
			handleEntityUpsert(w, req, account, table, pk, rk, false)
		case http.MethodPatch, "MERGE":
			handleEntityUpsert(w, req, account, table, pk, rk, true)
		case http.MethodDelete:
			handleEntityDelete(w, req, account, table, pk, rk)
		default:
			writeTableODataError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}
	if !strings.Contains(path, "/") && path != "" {
		switch req.Method {
		case http.MethodPost:
			handleEntityInsert(w, req, account, path)
		case http.MethodGet:
			handleEntityQuery(w, req, account, path)
		default:
			writeTableODataError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}
	writeTableODataError(w, "InvalidUri", "Unrecognized batch sub-request path", http.StatusBadRequest)
}

// ── entity snapshot for rollback ─────────────────────────────────────────────

func snapshotTableEntities(account string) []TableEntity {
	prefix := account + "/"
	return tableEntities.Filter(func(e TableEntity) bool {
		return strings.HasPrefix(tableEntityKey(e.Account, e.Table, e.PartitionKey, e.RowKey), prefix)
	})
}

func restoreTableEntities(account string, snapshot []TableEntity) {
	// Delete everything for the account, then re-insert the snapshot — restores
	// inserts (removed), deletes (re-added), and mutations (reverted).
	prefix := account + "/"
	for _, e := range tableEntities.Filter(func(e TableEntity) bool {
		return strings.HasPrefix(tableEntityKey(e.Account, e.Table, e.PartitionKey, e.RowKey), prefix)
	}) {
		tableEntities.Delete(tableEntityKey(e.Account, e.Table, e.PartitionKey, e.RowKey))
	}
	for _, e := range snapshot {
		tableEntities.Put(tableEntityKey(e.Account, e.Table, e.PartitionKey, e.RowKey), e)
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
	batchBoundary := "batchresponse_" + generateUUID()
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

// writeTableBatchSingleError encodes a failed transaction: the batch response
// carries the single failing op's error (the aztables SDK surfaces this as the
// transaction error). The whole batch was already rolled back by the caller.
func writeTableBatchSingleError(w http.ResponseWriter, changesetBoundary string, status int, body []byte) {
	batchBoundary := "batchresponse_" + generateUUID()
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
