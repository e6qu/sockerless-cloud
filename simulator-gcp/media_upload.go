package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Discovery method that takes media declares a simple path under /upload/
// and, when it takes the resumable protocol too, a resumable path under
// /resumable/upload/. The Go client begins a resumable session on the /upload
// path; apitools begins it on the /resumable/upload path. Both then address
// the session URI the start answered with.

// apiRequestError is a refusal with the status and code the API answers it with.
type apiRequestError struct {
	status  int
	code    string
	message string
}

func (e *apiRequestError) Error() string { return e.message }

func apiRefuse(status int, code, format string, args ...any) error {
	return &apiRequestError{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

func writeAPIError(w http.ResponseWriter, err error) {
	var refusal *apiRequestError
	if errors.As(err, &refusal) {
		GCPError(w, refusal.status, refusal.message, refusal.code)
		return
	}
	GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
}

func isMediaUploadPath(path string) bool {
	return strings.HasPrefix(path, "/upload/") || strings.HasPrefix(path, "/resumable/upload/")
}

// readMediaUpload reads a media method's request message and its bytes. The
// simple protocol carries both as a multipart/related body whose first part is
// the request message and whose second is the media (uploadType=multipart), or
// the bytes alone on the /upload path (uploadType=media). The method's plain
// path carries the message and no bytes.
func readMediaUpload(r *http.Request) (request, data []byte, contentType string, err error) {
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "multipart/related" {
		parts := multipart.NewReader(r.Body, params["boundary"])
		meta, err := parts.NextPart()
		if err != nil {
			return nil, nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "read the upload's request part: %v", err)
		}
		request, err := io.ReadAll(meta)
		if err != nil {
			return nil, nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "read the upload's request part: %v", err)
		}
		media, err := parts.NextPart()
		if err != nil {
			return nil, nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "read the upload's media part: %v", err)
		}
		data, err := io.ReadAll(media)
		if err != nil {
			return nil, nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "read the upload's media part: %v", err)
		}
		return request, data, media.Header.Get("Content-Type"), nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "read the upload body: %v", err)
	}
	if strings.HasPrefix(r.URL.Path, "/upload/") {
		return nil, body, r.Header.Get("Content-Type"), nil
	}
	return body, nil, "", nil
}

// mediaUploadSession is one session of Google's resumable media upload
// protocol, keyed by its upload_id. It holds the request message the session
// began with and the bytes received so far; once the last byte arrives it
// holds the method's response, which a request for the session's status
// answers with from then on. Owner and Method name the resource and the method
// the session was begun on, which every later request must address.
type mediaUploadSession struct {
	Owner       string          `json:"owner"`
	Method      string          `json:"method"`
	Request     json.RawMessage `json:"request,omitempty"`
	ContentType string          `json:"contentType,omitempty"`
	Data        []byte          `json:"data,omitempty"`
	Response    json.RawMessage `json:"response,omitempty"`
}

var (
	mediaUploadSessions sim.Store[mediaUploadSession]
	// mediaUploadWriters serializes the requests of one session, so a retried
	// chunk cannot race the one it repeats.
	mediaUploadWriters = sim.NewKeyedLocks()
)

func initMediaUploadSessions(srv *sim.Server) {
	mediaUploadSessions = sim.MakeStore[mediaUploadSession](srv.DB(), "media_upload_sessions")
}

// mediaUploadFinisher completes a media method once its bytes have all
// arrived, returning the method's response message.
type mediaUploadFinisher func(owner string, request, data []byte, contentType string) (any, error)

// serveMediaUpload serves a media method on any of its paths. A request naming
// an upload_id belongs to a resumable session; one with uploadType=resumable on
// a media path begins a session; anything else is the simple protocol, which
// carries the request message and the bytes at once.
func serveMediaUpload(w http.ResponseWriter, r *http.Request, owner, method string, resumable bool, finish mediaUploadFinisher) {
	defer r.Body.Close()
	query := r.URL.Query()
	mediaPath := isMediaUploadPath(r.URL.Path)
	if uploadID := query.Get("upload_id"); resumable && mediaPath && uploadID != "" {
		if r.Method == http.MethodDelete {
			mediaUploadCancel(w, owner, method, uploadID)
			return
		}
		mediaUploadChunk(w, r, owner, method, uploadID, finish)
		return
	}
	if r.Method != http.MethodPost {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s on the upload path addresses a resumable upload session and needs its upload_id", r.Method)
		return
	}
	if resumable && mediaPath && query.Get("uploadType") == "resumable" {
		mediaUploadStart(w, r, owner, method)
		return
	}
	if refuseNonResumableOnResumablePath(w, r) {
		return
	}
	request, data, contentType, err := readMediaUpload(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	response, err := finish(owner, request, data, contentType)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

// refuseNonResumableOnResumablePath answers 400 for a request on a
// /resumable/upload path that neither begins nor continues a session: the
// path exists only for the resumable protocol.
func refuseNonResumableOnResumablePath(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/resumable/") {
		return false
	}
	GCPError(w, http.StatusBadRequest, "the resumable upload path takes uploadType=resumable", "INVALID_ARGUMENT")
	return true
}

// mediaUploadSessionURI is the session URI a resumable start answers with in
// Location: the URI the request was sent to, with the session's upload_id
// added.
func mediaUploadSessionURI(r *http.Request, uploadID string) string {
	query := r.URL.Query()
	query.Set("upload_id", uploadID)
	return requestScheme(r) + "://" + r.Host + r.URL.EscapedPath() + "?" + query.Encode()
}

// mediaUploadStart begins a session: the body is the request message, and
// X-Upload-Content-Type names the media type of the bytes to come. The answer
// is 200 with the session URI in Location, which every later request
// addresses.
func mediaUploadStart(w http.ResponseWriter, r *http.Request, owner, method string) {
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
	mediaUploadSessions.Put(uploadID, mediaUploadSession{
		Owner:       owner,
		Method:      method,
		Request:     request,
		ContentType: r.Header.Get("X-Upload-Content-Type"),
	})
	w.Header().Set("Location", mediaUploadSessionURI(r, uploadID))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// resumableChunkPlacement reads a chunk's Content-Range against the bytes a
// session holds: `bytes FIRST-LAST/TOTAL` or `bytes FIRST-LAST/*` while the
// total is unknown, `bytes */TOTAL` to finish with no further bytes, and
// `bytes */*` with an empty body to ask how far the session got (status). It
// returns where the chunk starts and the declared total, -1 while unknown. A
// chunk that would leave a gap after the received bytes is refused. chunkLen
// is the chunk's length, or -1 when the caller streams it unread.
func resumableChunkPlacement(contentRange string, chunkLen, received int64) (start, total int64, status bool, err error) {
	if contentRange == "bytes */*" {
		if chunkLen > 0 {
			return 0, 0, false, errors.New("Content-Range bytes */* asks for the upload's status and carries no bytes")
		}
		return received, -1, true, nil
	}
	start, end, total, err := parseGCSContentRange(contentRange, chunkLen)
	if err != nil {
		return 0, 0, false, err
	}
	switch {
	case strings.HasPrefix(contentRange, "bytes */"):
		if chunkLen > 0 {
			return 0, 0, false, fmt.Errorf("Content-Range %q names no bytes but the request carries %d", contentRange, chunkLen)
		}
		start = received
	case chunkLen >= 0 && contentRange != "" && end-start+1 != chunkLen:
		return 0, 0, false, fmt.Errorf("Content-Range %q names %d bytes but the request carries %d", contentRange, end-start+1, chunkLen)
	}
	if start > received {
		return 0, 0, false, fmt.Errorf("the chunk starts at byte %d but the upload has received %d bytes", start, received)
	}
	return start, total, false, nil
}

// mediaUploadChunk takes one request of a session. Until the last byte arrives
// the answer is 308 Resume Incomplete naming the bytes received, and the last
// chunk answers with the method's own response.
func mediaUploadChunk(w http.ResponseWriter, r *http.Request, owner, method, uploadID string, finish mediaUploadFinisher) {
	defer mediaUploadWriters.Lock(uploadID)()
	session, ok := mediaUploadSessions.Get(uploadID)
	if !ok || session.Owner != owner || session.Method != method {
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
	start, total, status, err := resumableChunkPlacement(r.Header.Get("Content-Range"), int64(len(chunk)), int64(len(session.Data)))
	if err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return
	}
	if status {
		resumeIncomplete(w, r, int64(len(session.Data)))
		return
	}
	session.Data = append(session.Data[:start:start], chunk...)
	if session.ContentType == "" {
		session.ContentType = r.Header.Get("Content-Type")
	}
	received := int64(len(session.Data))
	if total < 0 || received < total {
		mediaUploadSessions.Put(uploadID, session)
		resumeIncomplete(w, r, received)
		return
	}
	response, err := finish(owner, session.Request, session.Data[:total], session.ContentType)
	if err != nil {
		mediaUploadSessions.Delete(uploadID)
		writeAPIError(w, err)
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "encode the upload response: %v", err)
		return
	}
	session.Data, session.Response = nil, encoded
	mediaUploadSessions.Put(uploadID, session)
	sim.WriteJSON(w, http.StatusOK, session.Response)
}

// resumeIncomplete answers 308 Resume Incomplete, naming the bytes received
// and naming none before the first arrives — or, for a client that sends
// X-GUploader-No-308: yes, 200 with X-Http-Status-Code-Override: 308.
func resumeIncomplete(w http.ResponseWriter, r *http.Request, received int64) {
	if received > 0 {
		w.Header().Set("Range", "bytes=0-"+strconv.FormatInt(received-1, 10))
	}
	w.Header().Set("Content-Length", "0")
	if strings.EqualFold(r.Header.Get("X-GUploader-No-308"), "yes") {
		w.Header().Set("X-Http-Status-Code-Override", "308")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusPermanentRedirect)
}

// mediaUploadCancel cancels a session: the DELETE of a session URI answers 499
// and forgets the bytes received, as Cloud Storage's does.
func mediaUploadCancel(w http.ResponseWriter, owner, method, uploadID string) {
	defer mediaUploadWriters.Lock(uploadID)()
	session, ok := mediaUploadSessions.Get(uploadID)
	if !ok || session.Owner != owner || session.Method != method {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "resumable upload session %q not found", uploadID)
		return
	}
	mediaUploadSessions.Delete(uploadID)
	w.WriteHeader(499)
}
