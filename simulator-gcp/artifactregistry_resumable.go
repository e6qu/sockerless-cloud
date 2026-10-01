package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// arResumableUpload is one session of Google's resumable media upload
// protocol, keyed by its upload_id. It holds the request message the session
// began with and the bytes received so far; once the last byte arrives it
// holds the method's response, which a request for the session's status
// answers with from then on.
type arResumableUpload struct {
	Repo        string          `json:"repo"`
	Method      string          `json:"method"`
	Request     json.RawMessage `json:"request,omitempty"`
	ContentType string          `json:"contentType,omitempty"`
	Data        []byte          `json:"data,omitempty"`
	Response    json.RawMessage `json:"response,omitempty"`
}

var (
	arResumableUploads sim.Store[arResumableUpload]
	// arResumableWriters serializes the requests of one session, so a retried
	// chunk cannot race the one it repeats.
	arResumableWriters = sim.NewKeyedLocks()
)

// arUploadFinisher completes a media method once its bytes have all arrived,
// returning the method's response message.
type arUploadFinisher func(repo string, request, data []byte, contentType string) (any, error)

// arServeMediaUpload serves a media method on any of its paths. A request
// naming an upload_id belongs to a resumable session; one with
// uploadType=resumable on a media path begins a session; anything else is the
// simple protocol, which carries the request message and the bytes at once.
func arServeMediaUpload(w http.ResponseWriter, r *http.Request, repo, method string, resumable bool, finish arUploadFinisher) {
	defer r.Body.Close()
	query := r.URL.Query()
	mediaPath := strings.HasPrefix(r.URL.Path, "/upload/") || strings.HasPrefix(r.URL.Path, "/resumable/")
	if uploadID := query.Get("upload_id"); resumable && mediaPath && uploadID != "" {
		if r.Method == http.MethodDelete {
			arResumableCancel(w, repo, method, uploadID)
			return
		}
		arResumableChunk(w, r, repo, method, uploadID, finish)
		return
	}
	if r.Method != http.MethodPost {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s on the upload path addresses a resumable upload session and needs its upload_id", r.Method)
		return
	}
	if resumable && mediaPath && query.Get("uploadType") == "resumable" {
		arResumableStart(w, r, repo, method)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/resumable/") {
		GCPError(w, http.StatusBadRequest, "the resumable upload path takes uploadType=resumable", "INVALID_ARGUMENT")
		return
	}
	request, data, contentType, err := arReadMediaUpload(r)
	if err != nil {
		arWriteError(w, err)
		return
	}
	response, err := finish(repo, request, data, contentType)
	if err != nil {
		arWriteError(w, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

// arResumableStart begins a session: the body is the request message, and
// X-Upload-Content-Type names the media type of the bytes to come. The answer
// is 200 with the session URI in Location — the URI the request was sent to
// with the session's upload_id added — which every later request addresses.
func arResumableStart(w http.ResponseWriter, r *http.Request, repo, method string) {
	request, err := io.ReadAll(r.Body)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read the upload's request message: %v", err)
		return
	}
	if len(bytes.TrimSpace(request)) > 0 && !json.Valid(request) {
		GCPError(w, http.StatusBadRequest, "the upload's request message is not JSON", "INVALID_ARGUMENT")
		return
	}
	uploadID := sim.NewUUID()
	arResumableUploads.Put(uploadID, arResumableUpload{
		Repo:        repo,
		Method:      method,
		Request:     request,
		ContentType: r.Header.Get("X-Upload-Content-Type"),
	})
	query := r.URL.Query()
	query.Set("upload_id", uploadID)
	w.Header().Set("Location", requestScheme(r)+"://"+r.Host+r.URL.EscapedPath()+"?"+query.Encode())
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// arResumableChunk takes one request of a session. Content-Range places the
// chunk: `bytes FIRST-LAST/TOTAL` or `bytes FIRST-LAST/*` while the total is
// unknown, `bytes */TOTAL` to finish with no further bytes, and `bytes */*`
// with an empty body to ask how far the session got. Until the last byte
// arrives the answer is 308 Resume Incomplete with a Range header naming the
// bytes received — or, for a client that sends X-GUploader-No-308: yes, 200
// with X-Http-Status-Code-Override: 308 — and the last chunk answers with the
// method's own response.
func arResumableChunk(w http.ResponseWriter, r *http.Request, repo, method, uploadID string, finish arUploadFinisher) {
	defer arResumableWriters.Lock(uploadID)()
	session, ok := arResumableUploads.Get(uploadID)
	if !ok || session.Repo != repo || session.Method != method {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "resumable upload session %q not found", uploadID)
		return
	}
	reader, err := openStreamingBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusUnsupportedMediaType, "INVALID_ARGUMENT", "%v", err)
		return
	}
	defer reader.Close()
	chunk, err := io.ReadAll(reader)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read the upload chunk: %v", err)
		return
	}
	if session.Response != nil {
		sim.WriteJSON(w, http.StatusOK, session.Response)
		return
	}
	contentRange := r.Header.Get("Content-Range")
	if contentRange == "bytes */*" {
		if len(chunk) > 0 {
			GCPError(w, http.StatusBadRequest, "Content-Range bytes */* asks for the upload's status and carries no bytes", "INVALID_ARGUMENT")
			return
		}
		arResumeIncomplete(w, r, len(session.Data))
		return
	}
	start, end, total, err := parseGCSContentRange(contentRange, int64(len(chunk)))
	if err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return
	}
	if strings.HasPrefix(contentRange, "bytes */") {
		if len(chunk) > 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Content-Range %q names no bytes but the request carries %d", contentRange, len(chunk))
			return
		}
		start = int64(len(session.Data))
	} else if end-start+1 != int64(len(chunk)) {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Content-Range %q names %d bytes but the request carries %d", contentRange, end-start+1, len(chunk))
		return
	}
	if start > int64(len(session.Data)) {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"the chunk starts at byte %d but the upload has received %d bytes", start, len(session.Data))
		return
	}
	session.Data = append(session.Data[:start:start], chunk...)
	if session.ContentType == "" {
		session.ContentType = r.Header.Get("Content-Type")
	}
	received := int64(len(session.Data))
	if total < 0 || received < total {
		arResumableUploads.Put(uploadID, session)
		arResumeIncomplete(w, r, len(session.Data))
		return
	}
	response, err := finish(repo, session.Request, session.Data[:total], session.ContentType)
	if err != nil {
		arResumableUploads.Delete(uploadID)
		arWriteError(w, err)
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "encode the upload response: %v", err)
		return
	}
	session.Data, session.Response = nil, encoded
	arResumableUploads.Put(uploadID, session)
	sim.WriteJSON(w, http.StatusOK, session.Response)
}

// arResumeIncomplete answers 308 Resume Incomplete, naming the bytes received
// and naming none before the first arrives.
func arResumeIncomplete(w http.ResponseWriter, r *http.Request, received int) {
	if received > 0 {
		w.Header().Set("Range", "bytes=0-"+strconv.Itoa(received-1))
	}
	w.Header().Set("Content-Length", "0")
	if strings.EqualFold(r.Header.Get("X-GUploader-No-308"), "yes") {
		w.Header().Set("X-Http-Status-Code-Override", "308")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusPermanentRedirect)
}

// arResumableCancel cancels a session: the DELETE of a session URI answers
// 499 and forgets the bytes received, as Cloud Storage's does.
func arResumableCancel(w http.ResponseWriter, repo, method, uploadID string) {
	defer arResumableWriters.Lock(uploadID)()
	session, ok := arResumableUploads.Get(uploadID)
	if !ok || session.Repo != repo || session.Method != method {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "resumable upload session %q not found", uploadID)
		return
	}
	arResumableUploads.Delete(uploadID)
	w.WriteHeader(499)
}
