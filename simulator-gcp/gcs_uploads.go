package main

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// registerGCSUploads mounts the simple, multipart and resumable uploads.
func registerGCSUploads(srv *sim.Server, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) {
	// A resumable session's chunks are PUTs to its session URI and its
	// cancellation a DELETE there, on whichever media path the session began.
	resumableChunk := func(w http.ResponseWriter, r *http.Request) {
		uploadID := r.URL.Query().Get("upload_id")
		if uploadID == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"%s on the upload path addresses a resumable upload session and needs its upload_id", r.Method)
			return
		}
		if r.Method == http.MethodDelete {
			handleGCSResumableCancel(w, uploadID)
			return
		}
		handleGCSResumableChunk(w, r, uploadID, buckets, objects)
	}

	srv.HandleFunc("PUT /upload/storage/v1/b/{bucket}/o", resumableChunk)

	srv.HandleFunc("DELETE /upload/storage/v1/b/{bucket}/o", resumableChunk)

	srv.HandleFunc("PUT /resumable/upload/storage/v1/b/{bucket}/o", resumableChunk)

	srv.HandleFunc("DELETE /resumable/upload/storage/v1/b/{bucket}/o", resumableChunk)

	// objects.insert on its media paths: /upload takes every protocol, and
	// /resumable/upload only begins and continues resumable sessions.
	uploadObject := func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := r.URL.Query().Get("name")
		uploadType := r.URL.Query().Get("uploadType")
		uploadID := r.URL.Query().Get("upload_id")

		if _, ok := buckets.Get(bucketName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		pre, err := parseGCSPreconditions(r.URL.Query(), false)
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}

		var body string
		var digests blobstore.Digests
		objAttrs := GCSObject{}

		ct := r.Header.Get("Content-Type")
		mediaType, _, _ := mime.ParseMediaType(ct)
		if mediaType == "" && strings.HasPrefix(strings.ToLower(ct), "multipart/related") {
			mediaType = "multipart/related"
		}

		defer r.Body.Close()
		if uploadID != "" {
			handleGCSResumableChunk(w, r, uploadID, buckets, objects)
			return
		}
		if uploadType != "resumable" && refuseNonResumableOnResumablePath(w, r) {
			return
		}

		// Resumable upload session initiation. Real GCS accepts the
		// metadata (including `name`) as JSON in the body and returns
		// 200 + a Location header containing an opaque session ID;
		// the SDK then PUTs chunks at that session URL with
		// Content-Range headers. Each PUT either returns 308 Resume
		// Incomplete (with a Range header naming the bytes received)
		// or 200 with the finalized object metadata.
		if uploadType == "resumable" {
			var meta gcsObjectResource
			// Resumable-init body is a JSON object resource. The Go SDK
			// does not gzip-encode this control-plane request; chunk
			// uploads at the session URL carry the streaming envelope.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
					"failed to read resumable metadata: %v", err)
				return
			}
			if len(body) > 0 {
				if err := json.Unmarshal(body, &meta); err != nil {
					GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"failed to parse resumable metadata: %v", err)
					return
				}
			}
			if objectName == "" {
				objectName = meta.Name
			}
			if objectName == "" {
				GCPError(w, http.StatusBadRequest,
					"name is required (in query or body)", "INVALID_ARGUMENT")
				return
			}
			objAttrs = meta.applyTo(objAttrs)
			objAttrs.CustomContexts = meta.Contexts.replacement(nil, "")
			if err := validateGCSObjectAttrs(objAttrs); err != nil {
				writeGCSPersistError(w, "init resumable object", err)
				return
			}
			sessionID := sim.NewUUID()
			gcsResumableSessions.Put(sessionID, gcsResumableSession{
				Bucket:        bucketName,
				Object:        objectName,
				Attrs:         objAttrs,
				Preconditions: pre,
			})
			w.Header().Set("Location", mediaUploadSessionURI(r, sessionID))
			w.WriteHeader(http.StatusOK)
			return
		}

		if mediaType == "multipart/related" {
			boundary := gcsMultipartBoundary(ct)
			// Multipart upload: first part is metadata JSON
			// (including `name` when not in query), second part is
			// data. Real GCS multipart/related bodies are not
			// content-encoded — gzip travels on the resumable
			// session chunk PUT instead (handled in
			// handleGCSResumableChunk via openStreamingBody).
			mr := multipart.NewReader(r.Body, boundary)
			metaPart, err := mr.NextPart()
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "failed to read metadata part: %v", err)
				return
			}
			metaBytes, err := io.ReadAll(metaPart)
			_ = metaPart.Close()
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"failed to read multipart metadata part: %v", err)
				return
			}
			if len(metaBytes) > 0 {
				var meta gcsObjectResource
				if jsonErr := json.Unmarshal(metaBytes, &meta); jsonErr != nil {
					GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"failed to parse multipart metadata: %v", jsonErr)
					return
				}
				if objectName == "" {
					objectName = meta.Name
				}
				objAttrs = meta.applyTo(objAttrs)
				objAttrs.CustomContexts = meta.Contexts.replacement(nil, "")
			}
			if objectName == "" {
				GCPError(w, http.StatusBadRequest,
					"name is required (in query or multipart metadata)", "INVALID_ARGUMENT")
				return
			}
			// Read data part
			dataPart, err := mr.NextPart()
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "failed to read data part: %v", err)
				return
			}
			if objAttrs.ContentType == "" {
				objAttrs.ContentType = dataPart.Header.Get("Content-Type")
			}
			body, digests, err = gcsBodies.WriteFrom(dataPart)
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "failed to read data: %v", err)
				return
			}
		} else {
			if objectName == "" {
				GCPError(w, http.StatusBadRequest,
					"name query parameter is required", "INVALID_ARGUMENT")
				return
			}
			// Simple upload (streaming-aware: handles gzip).
			rc, err := openStreamingBody(r)
			if err != nil {
				GCPErrorf(w, http.StatusUnsupportedMediaType, "INVALID_ARGUMENT", "%s", err.Error())
				return
			}
			body, digests, err = gcsBodies.WriteFrom(rc)
			_ = rc.Close()
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "failed to read body: %v", err)
				return
			}
			objAttrs.ContentType = ct
		}

		obj, err := persistGCSObject(objects, bucketName, objectName, body, digests, objAttrs, pre, gcsMirror)
		if err != nil {
			writeGCSPersistError(w, "write object", err)
			return
		}

		// Real GCS object responses include `kind` + `id` + `selfLink` +
		// `mediaLink` (https-hard-coded — GCS' JSON API is HTTPS-only).
		// terraform-provider-google's `google_storage_bucket_object`
		// reads `selfLink` into the resource's `self_link` attribute
		// on apply, so missing it means the attribute is empty
		// downstream.
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
	}

	srv.HandleFunc("POST /upload/storage/v1/b/{bucket}/o", uploadObject)

	srv.HandleFunc("POST /resumable/upload/storage/v1/b/{bucket}/o", uploadObject)
}

// handleGCSResumableChunk processes a chunk PUT during a resumable
// upload. The client sends `Content-Range: bytes <start>-<end>/<total>`
// (or `bytes <start>-<end>/*` if total unknown). When `end+1 == total`,
// the upload is complete: the session bytes get persisted as a real
// GCS object and the canonical 200 + object-metadata response goes back.
// Otherwise the sim returns 308 Resume Incomplete with a `Range` header
// naming the bytes received so the client knows where to resume.
func handleGCSResumableChunk(w http.ResponseWriter, r *http.Request, uploadID string, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) {
	sess, ok := gcsResumableSessions.Get(uploadID)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"resumable upload session %q not found", uploadID)
		return
	}
	if _, exists := buckets.Get(sess.Bucket); !exists {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"bucket %q not found", sess.Bucket)
		return
	}

	defer r.Body.Close()
	// Wrap the chunk body through openStreamingBody so a
	// Content-Encoding: gzip chunk decodes transparently — real GCS
	// resumable uploads can carry gzip-encoded chunks when the SDK
	// sets Object.ContentEncoding = "gzip" alongside the streamed
	// upload.
	chunkReader, err := openStreamingBody(r)
	if err != nil {
		GCPErrorf(w, http.StatusUnsupportedMediaType, "INVALID_ARGUMENT",
			"%s", err.Error())
		return
	}
	defer func() { _ = chunkReader.Close() }()

	release := gcsResumableWriters.Lock(uploadID)
	sess, ok = gcsResumableSessions.Get(uploadID)
	if !ok {
		release()
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"resumable upload session %q not found", uploadID)
		return
	}
	if sess.Response != nil {
		release()
		sim.WriteJSON(w, http.StatusOK, sess.Response)
		return
	}
	held, err := gcsStagedSize(sess.Body)
	if err != nil {
		release()
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the staged upload: %v", err)
		return
	}
	// The chunk streams to the staging payload unread, so its length is known
	// only once written.
	contentRange := r.Header.Get("Content-Range")
	start, total, status, err := resumableChunkPlacement(contentRange, -1, held)
	if err != nil {
		release()
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return
	}
	if status {
		release()
		resumeIncomplete(w, r, held)
		return
	}
	staged, received, err := gcsBodies.WriteAt(sess.Body, start, chunkReader)
	if err != nil {
		release()
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
			"failed to stage resumable chunk: %v", err)
		return
	}
	sess.Body = staged
	// A chunk without Content-Range is the whole object, however long its
	// body turned out to be.
	if contentRange == "" {
		total = received
	}
	if total < 0 || received < total {
		// The session record names the staged bytes, so the session
		// resumes with them even across a simulator restart.
		gcsResumableSessions.Put(uploadID, sess)
		release()
		resumeIncomplete(w, r, received)
		return
	}

	// The last chunk: the staged bytes, cut to the declared total, become
	// the object's generation, and the session answers every later request
	// with the object's metadata.
	defer release()
	fail := func() {
		gcsResumableSessions.Delete(uploadID)
		gcsReleaseBody(staged)
	}
	if received > total {
		if err := gcsBodies.Truncate(staged, total); err != nil {
			fail()
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "finalize resumable upload: %v", err)
			return
		}
	}
	digests, err := gcsBodies.Digest(staged)
	if err != nil {
		fail()
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "finalize resumable upload: %v", err)
		return
	}
	obj, err := persistGCSObject(objects, sess.Bucket, sess.Object, staged, digests, sess.Attrs, sess.Preconditions, gcsMirror)
	if err != nil {
		gcsResumableSessions.Delete(uploadID)
		writeGCSPersistError(w, "write resumable object", err)
		return
	}
	response, err := json.Marshal(gcsObjectMetadata(r, obj))
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "encode the object metadata: %v", err)
		return
	}
	gcsResumableSessions.Put(uploadID, gcsResumableSession{Bucket: sess.Bucket, Object: sess.Object, Response: response})
	sim.WriteJSON(w, http.StatusOK, json.RawMessage(response))
}
