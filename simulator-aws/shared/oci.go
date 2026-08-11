package simulator

import (
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// OCI Distribution data plane — the Docker Registry HTTP API v2 (`/v2/…`),
// shared verbatim across the AWS ECR, GCP Artifact Registry, and Azure ACR
// simulators. The protocol is identical across registries; cloud-specific
// behaviour (control-plane image-row registration, pull-through hydration,
// non-OCI `/v2/` routes) is injected via the hooks on OCIRegistry.

// OCIManifest is a stored image manifest, keyed by `repo:reference` (tag or
// digest).
type OCIManifest struct {
	ContentType string
	Digest      string
	Data        []byte
	Repo        string
	Ref         string
}

// OCIBlob is a stored content-addressed blob (image config or layer), keyed by
// `repo@digest`.
type OCIBlob struct {
	Digest      string
	ContentType string
	Data        []byte
}

// OCIUpload tracks an in-progress (possibly chunked) blob upload, keyed by its
// upload UUID.
type OCIUpload struct {
	UUID string
	Repo string
	Data []byte
}

// OCIRegistry serves the OCI Distribution data plane from a trio of stores.
type OCIRegistry struct {
	Manifests Store[OCIManifest]
	Blobs     Store[OCIBlob]
	Uploads   Store[OCIUpload]

	// manifestMu serializes the multi-key manifest mutations (a put writes the
	// tag + digest aliases; a delete removes both) so a concurrent push+delete
	// of the same manifest can't leave a dangling half-manifest.
	manifestMu sync.Mutex

	// OnManifestPut, if set, is invoked after a manifest is stored so the cloud
	// can register a control-plane image row.
	OnManifestPut func(repo, ref, contentType string, data []byte)
	// HydrateManifest, if set, is invoked on a manifest GET/HEAD miss to let the
	// cloud's pull-through cache populate the manifest (+ its blobs) via
	// PutBlob/PutManifest. Returns true if it populated the requested manifest.
	HydrateManifest func(reg *OCIRegistry, repo, ref string) bool
	// SkipPath, if set, returns true for `/v2/`-prefixed paths the surrounding
	// mux serves elsewhere (e.g. GCP's `/v2/projects/` control-plane routes);
	// those get a 404 here so the cloud handler can take them.
	SkipPath func(path string) bool
}

// Register mounts the data plane on the /v2/ subtree for every method, then
// dispatches by method internally. Registering one method-specific subtree per
// verb (rather than a bare `/v2/` or per-method `{wildcard...}` patterns)
// sidesteps three ServeMux pitfalls at once: the Go 1.22 rule that a
// `{name...}` wildcard must be the final segment; the method-pattern split that
// left blob-upload POST unrouted on ACR; and the conflict a bare all-method
// `/v2/` raises against a method-specific root pattern like awsJson's `POST /`
// (a method-specific subtree is strictly more specific, so it wins cleanly).
func (reg *OCIRegistry) Register(srv *Server) {
	h := http.HandlerFunc(reg.serve)
	// HEAD is intentionally omitted: Go's ServeMux routes HEAD to a GET
	// handler, and registering an explicit `HEAD /v2/` would conflict with any
	// sibling `GET /v2/<exact>` route (e.g. AWS API Gateway v2's /v2/apis) via
	// the GET-implies-HEAD rule. handleBlob/handleManifest serve HEAD inside.
	for _, method := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
		srv.Handle(method+" /v2/", h)
	}
}

func (reg *OCIRegistry) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// Real Docker Distribution / ECR registries return this header on EVERY
	// /v2/ response (not just the base ping) — including the missing-manifest
	// 404 that a push client probes first. Setting it here covers manifest,
	// blob, tags, and error responses; a strict client or fronting proxy can
	// otherwise reject a bare 404 that doesn't look like a registry response.
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	if path == "/v2/" || path == "/v2" {
		WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if reg.SkipPath != nil && reg.SkipPath(path) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, "/v2/")

	// Order matters: `/blobs/uploads/` must be matched before `/blobs/`.
	switch {
	case strings.Contains(rest, "/blobs/uploads/"):
		idx := strings.Index(rest, "/blobs/uploads/")
		reg.handleBlobUpload(w, r, rest[:idx], rest[idx+len("/blobs/uploads/"):])
	case strings.Contains(rest, "/blobs/"):
		idx := strings.Index(rest, "/blobs/")
		reg.handleBlob(w, r, rest[:idx], rest[idx+len("/blobs/"):])
	case strings.Contains(rest, "/manifests/"):
		idx := strings.Index(rest, "/manifests/")
		reg.handleManifest(w, r, rest[:idx], rest[idx+len("/manifests/"):])
	case strings.HasSuffix(rest, "/tags/list"):
		reg.handleTagsList(w, r, strings.TrimSuffix(rest, "/tags/list"))
	default:
		http.NotFound(w, r)
	}
}

// PutBlob stores a content-addressed blob (used by hydration hooks).
func (reg *OCIRegistry) PutBlob(repo, digest, contentType string, data []byte) {
	reg.Blobs.Put(repo+"@"+digest, OCIBlob{Digest: digest, ContentType: contentType, Data: data})
}

// PutManifest stores a manifest under both its tag/reference and its digest
// (used by hydration hooks).
func (reg *OCIRegistry) PutManifest(repo, ref, contentType string, data []byte) {
	digest := ociDigest(data)
	m := OCIManifest{ContentType: contentType, Digest: digest, Data: data, Repo: repo, Ref: ref}
	reg.Manifests.Put(repo+":"+ref, m)
	byDigest := m
	byDigest.Ref = digest
	reg.Manifests.Put(repo+":"+digest, byDigest)
}

func (reg *OCIRegistry) handleBlobUpload(w http.ResponseWriter, r *http.Request, repo, uploadID string) {
	switch r.Method {
	case http.MethodPost:
		// POST /v2/{repo}/blobs/uploads/ — initiate (or monolithic if ?digest=).
		if digest := r.URL.Query().Get("digest"); digest != "" {
			data, err := ociReadBody(r)
			if err != nil {
				ociError(w, "UNSUPPORTED", err.Error(), http.StatusUnsupportedMediaType)
				return
			}
			if !reg.storeBlob(w, repo, digest, data) {
				return
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, digest))
			w.WriteHeader(http.StatusCreated)
			return
		}
		uuid := ociUUID()
		reg.Uploads.Put(uuid, OCIUpload{UUID: uuid, Repo: repo})
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid))
		w.Header().Set("Docker-Upload-UUID", uuid)
		w.Header().Set("Range", "0-0")
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPatch:
		// PATCH /v2/{repo}/blobs/uploads/{uuid} — append a chunk.
		data, err := ociReadBody(r)
		if err != nil {
			ociError(w, "UNSUPPORTED", err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		// Atomic append: concurrent chunk PATCHes for the same upload must not
		// race a Get→append→Put (a lost chunk → DIGEST_INVALID on finalize).
		var end int
		ok := reg.Uploads.Update(uploadID, func(u *OCIUpload) {
			u.Data = append(u.Data, data...)
			end = len(u.Data)
		})
		if !ok {
			ociError(w, "BLOB_UPLOAD_UNKNOWN", "upload not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uploadID))
		w.Header().Set("Docker-Upload-UUID", uploadID)
		w.Header().Set("Range", fmt.Sprintf("0-%d", maxInt(end-1, 0)))
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPut:
		// PUT /v2/{repo}/blobs/uploads/{uuid}?digest=… — finalize. Any trailing
		// chunk may ride along in the PUT body.
		upload, ok := reg.Uploads.Get(uploadID)
		if !ok {
			ociError(w, "BLOB_UPLOAD_UNKNOWN", "upload not found", http.StatusNotFound)
			return
		}
		digest := r.URL.Query().Get("digest")
		if digest == "" {
			ociError(w, "DIGEST_INVALID", "digest parameter is required", http.StatusBadRequest)
			return
		}
		data, err := ociReadBody(r)
		if err != nil {
			ociError(w, "UNSUPPORTED", err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		// Non-aliasing concat: `append(upload.Data, …)` could write into the
		// stored OCIUpload.Data backing array (MemoryStore.Get returns the value
		// with its reference fields shared), corrupting it for a concurrent
		// Get/Update. Allocate a fresh buffer so the finalize is independent.
		full := make([]byte, 0, len(upload.Data)+len(data))
		full = append(append(full, upload.Data...), data...)
		if !reg.storeBlob(w, upload.Repo, digest, full) {
			reg.Uploads.Delete(uploadID)
			return
		}
		reg.Uploads.Delete(uploadID)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", upload.Repo, digest))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusCreated)

	case http.MethodGet:
		// Upload status.
		upload, ok := reg.Uploads.Get(uploadID)
		if !ok {
			ociError(w, "BLOB_UPLOAD_UNKNOWN", "upload not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Upload-UUID", uploadID)
		w.Header().Set("Range", fmt.Sprintf("0-%d", maxInt(len(upload.Data)-1, 0)))
		w.WriteHeader(http.StatusNoContent)

	default:
		w.Header().Set("Allow", "POST, PATCH, PUT, GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// storeBlob verifies the content hashes to the asserted digest (real OCI rejects
// a mismatch with DIGEST_INVALID rather than storing under a wrong digest) and
// persists it. Returns false if it already wrote an error response.
func (reg *OCIRegistry) storeBlob(w http.ResponseWriter, repo, digest string, data []byte) bool {
	if strings.HasPrefix(digest, "sha256:") {
		if actual := ociDigest(data); actual != digest {
			ociError(w, "DIGEST_INVALID",
				fmt.Sprintf("provided digest %s does not match uploaded content digest %s", digest, actual),
				http.StatusBadRequest)
			return false
		}
	}
	reg.PutBlob(repo, digest, "application/octet-stream", data)
	return true
}

func (reg *OCIRegistry) handleBlob(w http.ResponseWriter, r *http.Request, repo, digest string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		blob, ok := reg.Blobs.Get(repo + "@" + digest)
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			ociError(w, "BLOB_UNKNOWN", fmt.Sprintf("blob %q is not found", digest), http.StatusNotFound)
			return
		}
		ct := blob.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(blob.Data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(blob.Data)
		}
	default:
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (reg *OCIRegistry) handleManifest(w http.ResponseWriter, r *http.Request, repo, ref string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		m, ok := reg.Manifests.Get(repo + ":" + ref)
		if !ok && reg.HydrateManifest != nil && reg.HydrateManifest(reg, repo, ref) {
			m, ok = reg.Manifests.Get(repo + ":" + ref)
		}
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			ociError(w, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %q is not found", ref), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", m.ContentType)
		w.Header().Set("Docker-Content-Digest", m.Digest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.Data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(m.Data)
		}

	case http.MethodPut:
		data, err := ociReadBody(r)
		if err != nil {
			ociError(w, "MANIFEST_INVALID", err.Error(), http.StatusBadRequest)
			return
		}
		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/vnd.docker.distribution.manifest.v2+json"
		}
		// putManifestRaw fires OnManifestPut under manifestMu so a concurrent
		// DELETE of the same digest can't race the control-plane row
		// registration against the data-plane alias writes.
		reg.putManifestRaw(repo, ref, contentType, data)
		w.Header().Set("Docker-Content-Digest", ociDigest(data))
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, ociDigest(data)))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		reg.manifestMu.Lock()
		entry, ok := reg.Manifests.Get(repo + ":" + ref)
		if !ok {
			reg.manifestMu.Unlock()
			ociError(w, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %q is not found", ref), http.StatusNotFound)
			return
		}
		// Delete every alias (tag entry + digest entry) for the same content —
		// deleting by digest must also drop the tags that point at it, and
		// vice-versa.
		for _, m := range reg.Manifests.Filter(func(m OCIManifest) bool {
			return m.Repo == repo && m.Digest == entry.Digest
		}) {
			reg.Manifests.Delete(repo + ":" + m.Ref)
		}
		reg.Manifests.Delete(repo + ":" + ref)
		reg.manifestMu.Unlock()
		w.WriteHeader(http.StatusAccepted)

	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// putManifestRaw stores the manifest under its tag/reference and (using the
// content digest) under its digest, with the given content type.
func (reg *OCIRegistry) putManifestRaw(repo, ref, contentType string, data []byte) {
	digest := ociDigest(data)
	m := OCIManifest{ContentType: contentType, Digest: digest, Data: data, Repo: repo, Ref: ref}
	reg.manifestMu.Lock()
	defer reg.manifestMu.Unlock()
	reg.Manifests.Put(repo+":"+ref, m)
	byDigest := m
	byDigest.Ref = digest
	reg.Manifests.Put(repo+":"+digest, byDigest)
	// Fire the control-plane hook while still holding manifestMu so it is
	// serialized against a concurrent DELETE of the same digest (which also
	// takes manifestMu). OnManifestPut registers a cloud image row and must not
	// re-enter the manifest store under this lock.
	if reg.OnManifestPut != nil {
		reg.OnManifestPut(repo, ref, contentType, data)
	}
}

func (reg *OCIRegistry) handleTagsList(w http.ResponseWriter, r *http.Request, repo string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var tags []string
	for _, m := range reg.Manifests.List() {
		if m.Repo == repo && m.Ref != "" && !strings.HasPrefix(m.Ref, "sha256:") {
			tags = append(tags, m.Ref)
		}
	}
	sort.Strings(tags)
	if tags == nil {
		tags = []string{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"name": repo, "tags": tags})
}

// --- helpers ---

func ociDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ociUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func ociError(w http.ResponseWriter, code, message string, status int) {
	WriteJSON(w, status, map[string]any{
		"errors": []map[string]any{{"code": code, "message": message}},
	})
}

// ociMaxBodyBytes bounds the size of any single blob/manifest upload the
// registry will buffer in memory. A registry caller streams whole image layers
// through here, so the cap is generous (2 GiB) — larger than any realistic test
// image layer — but finite: without it an attacker could POST a body with no
// Content-Length (or a tiny gzip blob that inflates to gigabytes — a "zip bomb")
// and OOM the simulator. The decompressed size is what the cap guards, since
// that is what gets materialized.
const ociMaxBodyBytes = 2 << 30 // 2 GiB

// ociReadBody reads the request body, transparently gunzipping it when the
// client sets Content-Encoding: gzip (real registry clients do). The read is
// bounded by ociMaxBodyBytes (post-decompression) so a maliciously large or
// highly-compressible body cannot exhaust memory.
func ociReadBody(r *http.Request) ([]byte, error) {
	switch ce := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); ce {
	case "", "identity":
		return readCapped(r.Body)
	case "gzip":
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip body: %w", err)
		}
		defer func() { _ = gz.Close() }()
		return readCapped(gz)
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", ce)
	}
}

// readCapped reads up to ociMaxBodyBytes from r, returning an error if the
// stream exceeds the cap rather than silently truncating (a truncated blob
// would later fail the digest check with a confusing DIGEST_INVALID).
func readCapped(r io.Reader) ([]byte, error) {
	// Read one byte past the cap so a body of exactly the limit succeeds while
	// anything larger is rejected.
	data, err := io.ReadAll(io.LimitReader(r, ociMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > ociMaxBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d-byte limit", int64(ociMaxBodyBytes))
	}
	return data, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
