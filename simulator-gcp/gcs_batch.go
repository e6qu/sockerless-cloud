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

	"github.com/e6qu/sockerless-cloud/sim"
)

// gcsBatchLimit is the most calls one batch request may carry.
const gcsBatchLimit = 100

// registerGCSBatch serves the JSON API's batch endpoint: a multipart/mixed body
// whose every part is one whole JSON API request, answered by a
// multipart/mixed body whose every part is one whole response, matched to its
// call by Content-ID with "response-" before it. The batch's own headers, the
// credential among them, apply to every call a part does not override, and each
// call is served exactly as it would be sent alone.
// https://cloud.google.com/storage/docs/batch
func registerGCSBatch(srv *sim.Server) {
	srv.HandleFunc("POST /batch/storage/v1", func(w http.ResponseWriter, r *http.Request) {
		mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/mixed" || parameters["boundary"] == "" {
			GCPError(w, http.StatusBadRequest, "A batch request must be multipart/mixed with a boundary.", "INVALID_ARGUMENT")
			return
		}
		type call struct {
			contentID string
			request   *http.Request
		}
		var calls []call
		parts := multipart.NewReader(r.Body, parameters["boundary"])
		for {
			part, err := parts.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read batch part: %v", err)
				return
			}
			if len(calls) == gcsBatchLimit {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"A batch request cannot contain more than %d calls.", gcsBatchLimit)
				return
			}
			if kind, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type")); kind != "application/http" {
				GCPError(w, http.StatusBadRequest, "Every part of a batch request must be application/http.", "INVALID_ARGUMENT")
				return
			}
			inner, err := http.ReadRequest(bufio.NewReader(part))
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read batched request: %v", err)
				return
			}
			body, err := io.ReadAll(inner.Body)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read batched request body: %v", err)
				return
			}
			if strings.HasPrefix(inner.URL.Path, "/batch/") {
				GCPError(w, http.StatusBadRequest, "A batch request cannot contain another batch request.", "INVALID_ARGUMENT")
				return
			}
			calls = append(calls, call{contentID: part.Header.Get("Content-ID"), request: gcsBatchedRequest(r, inner, body)})
		}

		var answer bytes.Buffer
		writer := multipart.NewWriter(&answer)
		for _, c := range calls {
			recorder := httptest.NewRecorder()
			srv.ServeHTTP(recorder, c.request)
			header := textproto.MIMEHeader{"Content-Type": {"application/http"}}
			if c.contentID != "" {
				header.Set("Content-ID", gcsBatchResponseID(c.contentID))
			}
			part, err := writer.CreatePart(header)
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "write batch answer: %v", err)
				return
			}
			result := recorder.Result()
			fmt.Fprintf(part, "HTTP/1.1 %d %s\r\n", result.StatusCode, http.StatusText(result.StatusCode))
			_ = result.Header.Write(part)
			fmt.Fprintf(part, "Content-Length: %d\r\n\r\n", recorder.Body.Len())
			_, _ = part.Write(recorder.Body.Bytes())
		}
		if err := writer.Close(); err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "write batch answer: %v", err)
			return
		}
		w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(answer.Bytes())
	})
}

// gcsBatchedRequest builds the request one part of a batch stands for: its own
// method, path, headers and body, addressed to the batch's host, carrying the
// batch's headers where it states none of its own.
func gcsBatchedRequest(batch, inner *http.Request, body []byte) *http.Request {
	target := inner.URL.RequestURI()
	request := httptest.NewRequestWithContext(batch.Context(), inner.Method, target, bytes.NewReader(body))
	request.Host = batch.Host
	request.RemoteAddr = batch.RemoteAddr
	for name, values := range batch.Header {
		if name == "Content-Type" || name == "Content-Length" {
			continue
		}
		request.Header[name] = append([]string(nil), values...)
	}
	for name, values := range inner.Header {
		request.Header[name] = append([]string(nil), values...)
	}
	return request
}

// gcsBatchResponseID is the Content-ID of the answer to the call whose
// Content-ID was id: "<response-item>" for "<item>".
func gcsBatchResponseID(id string) string {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(id, "<"), ">")
	return "<response-" + trimmed + ">"
}
