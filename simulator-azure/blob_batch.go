package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
)

// Blob Batch. A batch is a `multipart/mixed` body whose parts each carry one
// complete HTTP request; the response is a `multipart/mixed` body whose parts
// each carry that sub-request's complete HTTP response, correlated by
// `Content-ID`. The sub-requests are really parsed and really dispatched through
// the simulator's own blob data-plane handler, so a batched delete deletes and a
// batched Set Blob Tier changes the tier — the batch is a transport, not a
// second implementation.
//
// Azure restricts a batch to Delete Blob and Set Blob Tier sub-requests, and so
// does this: any other operation is answered per-part with the same 400 the
// service returns, leaving the rest of the batch to run.

const blobBatchMaxSubRequests = 256

func handleBlobSubmitBatch(w http.ResponseWriter, r *http.Request, account, container string) {
	defer r.Body.Close()
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: Content-Type.",
			http.StatusBadRequest)
		return
	}

	reader := multipart.NewReader(r.Body, params["boundary"])
	type subRequest struct {
		contentID string
		raw       []byte
	}
	var subs []subRequest
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeStorageError(w, "InvalidInput",
				"One of the request inputs is not valid: "+err.Error(), http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			writeStorageError(w, "InvalidInput",
				"One of the request inputs is not valid: "+err.Error(), http.StatusBadRequest)
			return
		}
		subs = append(subs, subRequest{contentID: part.Header.Get("Content-ID"), raw: body})
	}
	if len(subs) == 0 {
		writeStorageError(w, "InvalidInput",
			"One of the request inputs is not valid: the batch contains no sub-requests.",
			http.StatusBadRequest)
		return
	}
	if len(subs) > blobBatchMaxSubRequests {
		writeStorageError(w, "InvalidInput",
			fmt.Sprintf("One of the request inputs is not valid: a batch may contain at most %d sub-requests.",
				blobBatchMaxSubRequests), http.StatusBadRequest)
		return
	}

	var out bytes.Buffer
	writer := multipart.NewWriter(&out)
	if err := writer.SetBoundary(params["boundary"]); err != nil {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: Content-Type.",
			http.StatusBadRequest)
		return
	}
	for _, sub := range subs {
		rec := runBlobBatchSubRequest(r, account, container, sub.raw)
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Type", "application/http")
		headers.Set("Content-Transfer-Encoding", "binary")
		if sub.contentID != "" {
			headers.Set("Content-ID", sub.contentID)
		}
		partWriter, err := writer.CreatePart(headers)
		if err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := partWriter.Write(rec); err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := writer.Close(); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "multipart/mixed; boundary="+params["boundary"])
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(out.Bytes())
}

// runBlobBatchSubRequest parses one sub-request, dispatches it through the blob
// data plane, and serializes the answer as an HTTP/1.1 response message.
func runBlobBatchSubRequest(parent *http.Request, account, container string, raw []byte) []byte {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return blobBatchErrorResponse(http.StatusBadRequest, "InvalidInput",
			"One of the request inputs is not valid: "+err.Error())
	}
	req.Host = parent.Host
	req.RequestURI = ""
	if req.Header.Get("x-ms-version") == "" {
		req.Header.Set("x-ms-version", parent.Header.Get("x-ms-version"))
	}

	path := strings.TrimPrefix(req.URL.Path, "/")
	// A sub-request path may be host-style (`/{container}/{blob}`) or
	// path-style (`/{account}/{container}/{blob}`); the account prefix is
	// stripped so both address the same blob.
	if strings.HasPrefix(path, account+"/") {
		path = strings.TrimPrefix(path, account+"/")
		req.URL.Path = "/" + path
	}
	segs := strings.SplitN(path, "/", 2)
	if len(segs) != 2 || segs[1] == "" {
		return blobBatchErrorResponse(http.StatusBadRequest, "InvalidInput",
			"One of the request inputs is not valid: a batch sub-request must address a blob.")
	}
	if container != "" && segs[0] != container {
		return blobBatchErrorResponse(http.StatusBadRequest, "InvalidInput",
			"One of the request inputs is not valid: every sub-request of a container batch must address that container.")
	}
	if !blobBatchOperationAllowed(req) {
		return blobBatchErrorResponse(http.StatusBadRequest, "InvalidInput",
			"One of the request inputs is not valid: a batch may only contain Delete Blob or Set Blob Tier sub-requests.")
	}

	rec := httptest.NewRecorder()
	handleBlobDataPlane(rec, req, account)
	return blobBatchSerializeResponse(rec)
}

// blobBatchOperationAllowed reports whether a sub-request names one of the two
// operations Azure permits inside a batch.
func blobBatchOperationAllowed(req *http.Request) bool {
	comp := req.URL.Query().Get("comp")
	switch req.Method {
	case http.MethodDelete:
		return comp == ""
	case http.MethodPut:
		return comp == "tier"
	}
	return false
}

// blobBatchSerializeResponse renders a recorded response as the HTTP/1.1
// response message a batch part carries.
func blobBatchSerializeResponse(rec *httptest.ResponseRecorder) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "HTTP/1.1 %d %s\r\n", rec.Code, http.StatusText(rec.Code))
	body := rec.Body.Bytes()
	for k, values := range rec.Header() {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
		}
	}
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	return buf.Bytes()
}

// blobBatchErrorResponse renders a storage error as a batch sub-response, for
// the failures that happen before a sub-request can be dispatched at all.
func blobBatchErrorResponse(status int, code, message string) []byte {
	rec := httptest.NewRecorder()
	writeStorageError(rec, code, message, status)
	return blobBatchSerializeResponse(rec)
}
