package sim

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
	"time"
)

// OCI Distribution data plane — the Docker Registry HTTP API v2 (`/v2/…`),
// shared verbatim across the AWS ECR, GCP Artifact Registry, and Azure ACR
// simulators. The protocol is identical across registries; cloud-specific
// behaviour (control-plane image-row registration, pull-through hydration,
// non-OCI `/v2/` routes) is injected via the hooks on OCIRegistry.

// A registry's content is its own. Two registries served by one simulator hold
// unrelated repositories, so every stored manifest, blob and upload records the
// registry it belongs to and is keyed within it — the Scope, resolved per
// request by the mounting cloud (see OCIRegistry.Scope). Without it,
// `regA.example.com/foo` and `regB.example.com/foo` would resolve to the same
// content, which no real registry does.

// OCIManifest is a stored image manifest, keyed by
// `scope/repo:reference` (tag or digest).
type OCIManifest struct {
	Scope       string
	ContentType string
	Digest      string
	Data        []byte
	Repo        string
	Ref         string
	// Pushed is when the registry received this manifest. A registry knows
	// when it was written, and the properties APIs the clouds put in front of
	// it report that time; without it they would have to name a moment nothing
	// happened at.
	Pushed time.Time
}

// OCIBlob is a stored content-addressed blob (image config or layer), keyed by
// `scope/repo@digest`.
type OCIBlob struct {
	Scope       string
	Digest      string
	ContentType string
	Data        []byte
}

// OCIUpload tracks an in-progress (possibly chunked) blob upload, keyed by
// `scope/uuid`.
type OCIUpload struct {
	Scope string
	UUID  string
	Repo  string
	Data  []byte
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
	// can register a control-plane image row against the registry the request
	// addressed.
	OnManifestPut func(scope, repo, ref, contentType string, data []byte)
	// HydrateManifest, if set, is invoked on a manifest GET/HEAD miss to let the
	// cloud's pull-through cache populate the manifest (+ its blobs) via
	// PutBlob, in the scope of the registry the request addressed. Returns true
	// if it populated the requested manifest.
	HydrateManifest func(reg *OCIRegistry, scope, repo, ref string) bool
	// SkipPath, if set, returns true for `/v2/`-prefixed paths the surrounding
	// mux serves elsewhere (e.g. GCP's `/v2/projects/` control-plane routes);
	// those get a 404 here so the cloud handler can take them.
	SkipPath func(path string) bool
	// Authorize, if set, authenticates every `/v2/` request before the data
	// plane acts on it. repo is the repository the request addresses — empty
	// for the `/v2/` base ping, which addresses the registry itself. The hook
	// returns true to let the request through; on false it has already written
	// the registry's own refusal (the Docker Registry HTTP API v2 challenge and
	// error body), so the caller must return without touching the stores.
	//
	// The hook belongs to the cloud that mounts the registry because the
	// credentials, the token service and the challenge realm are that cloud's:
	// the protocol under `/v2/` is identical everywhere, the authority that
	// issues tokens for it is not.
	Authorize func(w http.ResponseWriter, r *http.Request, repo string) bool
	// AdmitRepository, if set, decides whether the repository a request names
	// may be acted on, and runs after Authorize and before the handler so a
	// refused request never reads or writes a store. push reports whether the
	// request is one of the verbs that puts content into a repository — the
	// blob upload and the manifest PUT a `docker push` is made of — which is
	// the distinction a registry needs to decide whether a name it does not
	// hold yet may come into existence now. The hook returns true to let the
	// request through; on false it has already written the registry's refusal.
	//
	// It belongs to the cloud that mounts the registry because whether a
	// repository is an explicit resource is that cloud's contract: Amazon ECR
	// requires one to be created before it can be used, and refuses a request
	// naming any other.
	AdmitRepository func(w http.ResponseWriter, r *http.Request, repo string, push bool) bool
	// BaseResponse, if set, writes the authorized answer to the API-version
	// probe at `/v2/`. The three registries this data plane serves disagree
	// there: Amazon ECR answers status 200 with `content-length: 0` and no
	// `content-type` at all, Google Artifact Registry the same empty body as
	// `text/html; charset=UTF-8`, Azure Container Registry the two-byte body
	// `{}` as `application/json; charset=utf-8`. Docker Distribution's `{}` is
	// what the reference implementation sends and none of these three do. A
	// registry mounted without the hook answers the way Amazon ECR does.
	BaseResponse func(w http.ResponseWriter)
	// RefuseChunkedUpload, if set, makes an upload session accept exactly one
	// write and calls the hook to refuse the second. Google Artifact Registry
	// documents that it does not support Docker chunked uploads; the line is
	// between one write and several, not between PATCH and PUT, because every
	// container engine's `docker push` sends the whole blob in one PATCH and
	// finalizes with PUT, and pushes to the live service succeed.
	RefuseChunkedUpload func(w http.ResponseWriter)
	// Scope, if set, returns the identity of the registry a request addresses,
	// which every stored manifest, blob and upload is keyed within. The
	// mounting cloud owns it because only that cloud knows how a request names
	// a registry — Azure Container Registry resolves the Host header against
	// the login server its control plane advertises, and the identity it
	// returns is the registry's ARM resource ID, the same key the rest of the
	// Azure stores use.
	//
	// A registry mounted without one keys its content in a single unnamed
	// scope, which is correct only for a cloud that serves exactly one
	// registry per simulator.
	Scope func(r *http.Request) string
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
		// The base endpoint is the one a client probes to discover both API
		// support and the token service, so it is authenticated like any other
		// registry route: the challenge it answers with is how the client
		// learns where to get a token.
		if !reg.authorized(w, r, "") {
			return
		}
		if reg.BaseResponse != nil {
			reg.BaseResponse(w)
			return
		}
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}
	if reg.SkipPath != nil && reg.SkipPath(path) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, "/v2/")

	// Order matters: `/blobs/uploads/` must be matched before `/blobs/`.
	// Admission runs after the repository is parsed and before the handler, so
	// a refused request never reads or writes a store. The scope is resolved
	// only after authorization has established that the request addresses a
	// registry this simulator serves.
	switch {
	case strings.Contains(rest, "/blobs/uploads/"):
		idx := strings.Index(rest, "/blobs/uploads/")
		// Every blob-upload verb but the status GET puts content into the
		// repository: POST opens the upload (or carries the whole blob when it
		// is monolithic), PATCH appends to it, PUT finalizes it.
		if !reg.admit(w, r, rest[:idx], r.Method != http.MethodGet) {
			return
		}
		reg.handleBlobUpload(w, r, reg.scope(r), rest[:idx], rest[idx+len("/blobs/uploads/"):])
	case strings.Contains(rest, "/blobs/"):
		idx := strings.Index(rest, "/blobs/")
		if !reg.admit(w, r, rest[:idx], false) {
			return
		}
		reg.handleBlob(w, r, reg.scope(r), rest[:idx], rest[idx+len("/blobs/"):])
	case strings.Contains(rest, "/manifests/"):
		idx := strings.Index(rest, "/manifests/")
		if !reg.admit(w, r, rest[:idx], r.Method == http.MethodPut) {
			return
		}
		reg.handleManifest(w, r, reg.scope(r), rest[:idx], rest[idx+len("/manifests/"):])
	case strings.HasSuffix(rest, "/tags/list"):
		repo := strings.TrimSuffix(rest, "/tags/list")
		if !reg.admit(w, r, repo, false) {
			return
		}
		reg.handleTagsList(w, r, reg.scope(r), repo)
	default:
		http.NotFound(w, r)
	}
}

// scope resolves the registry a request addresses. A registry mounted without
// a Scope hook serves one unnamed scope.
func (reg *OCIRegistry) scope(r *http.Request) string {
	if reg.Scope == nil {
		return ""
	}
	return reg.Scope(r)
}

// ociManifestKey, ociBlobKey and ociUploadKey compose the store keys of a
// registry's content. The scope is a full registry identity and the separator
// is `/`, so no two registries can ever compose the same key: a registry
// identity never ends in a segment another identity extends.
//
// The unnamed scope composes the key without a prefix, so a registry that
// serves one scope reads the rows an earlier process wrote under the same
// keys.
func ociManifestKey(scope, repo, ref string) string { return scopedKey(scope, repo+":"+ref) }

func ociBlobKey(scope, repo, digest string) string { return scopedKey(scope, repo+"@"+digest) }

func ociUploadKey(scope, uploadID string) string { return scopedKey(scope, uploadID) }

func scopedKey(scope, key string) string {
	if scope == "" {
		return key
	}
	return scope + "/" + key
}

// admit applies the mounting cloud's credential check and then its repository
// check to a repository-scoped request. Authentication comes first: which
// repositories a registry holds is not something an unauthenticated caller
// gets to learn.
func (reg *OCIRegistry) admit(w http.ResponseWriter, r *http.Request, repo string, push bool) bool {
	if !reg.authorized(w, r, repo) {
		return false
	}
	if reg.AdmitRepository == nil {
		return true
	}
	return reg.AdmitRepository(w, r, repo, push)
}

// authorized applies the mounting cloud's Authorize hook. A registry mounted
// without one serves its data plane unauthenticated.
func (reg *OCIRegistry) authorized(w http.ResponseWriter, r *http.Request, repo string) bool {
	if reg.Authorize == nil {
		return true
	}
	return reg.Authorize(w, r, repo)
}

// PutBlob stores a content-addressed blob in one registry's scope (used by
// hydration hooks).
func (reg *OCIRegistry) PutBlob(scope, repo, digest, contentType string, data []byte) {
	reg.Blobs.Put(ociBlobKey(scope, repo, digest),
		OCIBlob{Scope: scope, Digest: digest, ContentType: contentType, Data: data})
}

// PutManifest stores a manifest as a push of it would, under its reference and
// under its digest, and runs the control-plane hook so the registry's own
// records see it. A hydration hook uses this to serve content it fetched from
// somewhere else — an image cached through a pull-through cache rule is in the
// repository exactly as a pushed one is.
func (reg *OCIRegistry) PutManifest(scope, repo, ref, contentType string, data []byte) {
	reg.putManifestRaw(scope, repo, ref, contentType, data)
}

func (reg *OCIRegistry) handleBlobUpload(w http.ResponseWriter, r *http.Request, scope, repo, uploadID string) {
	switch r.Method {
	case http.MethodPost:
		// POST /v2/{repo}/blobs/uploads/ — initiate (or monolithic if ?digest=).
		if digest := r.URL.Query().Get("digest"); digest != "" {
			data, err := ociReadBody(r)
			if err != nil {
				OCIError(w, "UNSUPPORTED", err.Error(), http.StatusUnsupportedMediaType)
				return
			}
			if !reg.storeBlob(w, scope, repo, digest, data) {
				return
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, digest))
			w.WriteHeader(http.StatusCreated)
			return
		}
		uuid := ociUUID()
		reg.Uploads.Put(ociUploadKey(scope, uuid), OCIUpload{Scope: scope, UUID: uuid, Repo: repo})
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uuid))
		w.Header().Set("Docker-Upload-UUID", uuid)
		w.Header().Set("Range", "0-0")
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPatch:
		// PATCH /v2/{repo}/blobs/uploads/{uuid} — append a chunk.
		if reg.RefuseChunkedUpload != nil {
			if existing, ok := reg.Uploads.Get(ociUploadKey(scope, uploadID)); ok && len(existing.Data) > 0 {
				reg.RefuseChunkedUpload(w)
				return
			}
		}
		data, err := ociReadBody(r)
		if err != nil {
			OCIError(w, "UNSUPPORTED", err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		// Atomic append: concurrent chunk PATCHes for the same upload must not
		// race a Get→append→Put (a lost chunk → DIGEST_INVALID on finalize).
		var end int
		ok := reg.Uploads.Update(ociUploadKey(scope, uploadID), func(u *OCIUpload) {
			u.Data = append(u.Data, data...)
			end = len(u.Data)
		})
		if !ok {
			OCIError(w, "BLOB_UPLOAD_UNKNOWN", "upload not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uploadID))
		w.Header().Set("Docker-Upload-UUID", uploadID)
		w.Header().Set("Range", fmt.Sprintf("0-%d", maxInt(end-1, 0)))
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPut:
		// PUT /v2/{repo}/blobs/uploads/{uuid}?digest=… — finalize. Any trailing
		// chunk may ride along in the PUT body.
		upload, ok := reg.Uploads.Get(ociUploadKey(scope, uploadID))
		if !ok {
			OCIError(w, "BLOB_UPLOAD_UNKNOWN", "upload not found", http.StatusNotFound)
			return
		}
		digest := r.URL.Query().Get("digest")
		if digest == "" {
			OCIError(w, "DIGEST_INVALID", "digest parameter is required", http.StatusBadRequest)
			return
		}
		data, err := ociReadBody(r)
		if err != nil {
			OCIError(w, "UNSUPPORTED", err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		// Non-aliasing concat: `append(upload.Data, …)` could write into the
		// stored OCIUpload.Data backing array (MemoryStore.Get returns the value
		// with its reference fields shared), corrupting it for a concurrent
		// Get/Update. Allocate a fresh buffer so the finalize is independent.
		full := make([]byte, 0, len(upload.Data)+len(data))
		full = append(append(full, upload.Data...), data...)
		if !reg.storeBlob(w, scope, upload.Repo, digest, full) {
			reg.Uploads.Delete(ociUploadKey(scope, uploadID))
			return
		}
		reg.Uploads.Delete(ociUploadKey(scope, uploadID))
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", upload.Repo, digest))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusCreated)

	case http.MethodGet:
		// Upload status.
		upload, ok := reg.Uploads.Get(ociUploadKey(scope, uploadID))
		if !ok {
			OCIError(w, "BLOB_UPLOAD_UNKNOWN", "upload not found", http.StatusNotFound)
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
func (reg *OCIRegistry) storeBlob(w http.ResponseWriter, scope, repo, digest string, data []byte) bool {
	if strings.HasPrefix(digest, "sha256:") {
		if actual := ociDigest(data); actual != digest {
			OCIError(w, "DIGEST_INVALID",
				fmt.Sprintf("provided digest %s does not match uploaded content digest %s", digest, actual),
				http.StatusBadRequest)
			return false
		}
	}
	reg.PutBlob(scope, repo, digest, "application/octet-stream", data)
	return true
}

func (reg *OCIRegistry) handleBlob(w http.ResponseWriter, r *http.Request, scope, repo, digest string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		blob, ok := reg.Blobs.Get(ociBlobKey(scope, repo, digest))
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			OCIError(w, "BLOB_UNKNOWN", fmt.Sprintf("blob %q is not found", digest), http.StatusNotFound)
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

func (reg *OCIRegistry) handleManifest(w http.ResponseWriter, r *http.Request, scope, repo, ref string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		m, ok := reg.Manifests.Get(ociManifestKey(scope, repo, ref))
		if !ok && reg.HydrateManifest != nil && reg.HydrateManifest(reg, scope, repo, ref) {
			m, ok = reg.Manifests.Get(ociManifestKey(scope, repo, ref))
		}
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			OCIError(w, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %q is not found", ref), http.StatusNotFound)
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
			OCIError(w, "MANIFEST_INVALID", err.Error(), http.StatusBadRequest)
			return
		}
		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/vnd.docker.distribution.manifest.v2+json"
		}
		// putManifestRaw fires OnManifestPut under manifestMu so a concurrent
		// DELETE of the same digest can't race the control-plane row
		// registration against the data-plane alias writes.
		reg.putManifestRaw(scope, repo, ref, contentType, data)
		w.Header().Set("Docker-Content-Digest", ociDigest(data))
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, ociDigest(data)))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		reg.manifestMu.Lock()
		entry, ok := reg.Manifests.Get(ociManifestKey(scope, repo, ref))
		if !ok {
			reg.manifestMu.Unlock()
			OCIError(w, "MANIFEST_UNKNOWN", fmt.Sprintf("manifest %q is not found", ref), http.StatusNotFound)
			return
		}
		// Delete every alias (tag entry + digest entry) for the same content —
		// deleting by digest must also drop the tags that point at it, and
		// vice-versa.
		for _, m := range reg.Manifests.Filter(func(m OCIManifest) bool {
			return m.Scope == scope && m.Repo == repo && m.Digest == entry.Digest
		}) {
			reg.Manifests.Delete(ociManifestKey(scope, repo, m.Ref))
		}
		reg.Manifests.Delete(ociManifestKey(scope, repo, ref))
		reg.manifestMu.Unlock()
		w.WriteHeader(http.StatusAccepted)

	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// putManifestRaw stores the manifest under its tag/reference and (using the
// content digest) under its digest, with the given content type.
func (reg *OCIRegistry) putManifestRaw(scope, repo, ref, contentType string, data []byte) {
	digest := ociDigest(data)
	m := OCIManifest{
		Scope: scope, ContentType: contentType, Digest: digest,
		Data: data, Repo: repo, Ref: ref, Pushed: time.Now().UTC(),
	}
	reg.manifestMu.Lock()
	defer reg.manifestMu.Unlock()
	reg.Manifests.Put(ociManifestKey(scope, repo, ref), m)
	byDigest := m
	byDigest.Ref = digest
	reg.Manifests.Put(ociManifestKey(scope, repo, digest), byDigest)
	// Fire the control-plane hook while still holding manifestMu so it is
	// serialized against a concurrent DELETE of the same digest (which also
	// takes manifestMu). OnManifestPut registers a cloud image row and must not
	// re-enter the manifest store under this lock.
	if reg.OnManifestPut != nil {
		reg.OnManifestPut(scope, repo, ref, contentType, data)
	}
}

func (reg *OCIRegistry) handleTagsList(w http.ResponseWriter, r *http.Request, scope, repo string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var tags []string
	for _, m := range reg.Manifests.List() {
		if m.Scope == scope && m.Repo == repo && m.Ref != "" && !strings.HasPrefix(m.Ref, "sha256:") {
			tags = append(tags, m.Ref)
		}
	}
	sort.Strings(tags)
	if tags == nil {
		tags = []string{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"name": repo, "tags": tags})
}

func ociDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ociUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// OCIError writes a Docker Registry HTTP API v2 error body.
func OCIError(w http.ResponseWriter, code, message string, status int) {
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
	return ociReadBodyLimited(r, ociMaxBodyBytes)
}

// ociReadBodyLimited is ociReadBody with the cap supplied rather than assumed.
//
// The cap is two gibibytes, and a test that proves the limit by reaching it
// must materialize that much: two such tests together peaked at 7.7 GiB of
// resident memory under the race detector, on a hosted runner that has 7 GiB
// in total. What they assert — that the read stops one byte past the cap
// instead of truncating, on the plain path and after inflation alike — is a
// property of the boundary, not of its size, so the tests supply a small cap
// and assert both sides of it exactly.
func ociReadBodyLimited(r *http.Request, limit int64) ([]byte, error) {
	switch ce := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); ce {
	case "", "identity":
		return readCapped(r.Body, limit)
	case "gzip":
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip body: %w", err)
		}
		defer func() { _ = gz.Close() }()
		return readCapped(gz, limit)
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", ce)
	}
}

// readCapped reads up to limit bytes from r, returning an error if the stream
// exceeds it rather than silently truncating (a truncated blob would later
// fail the digest check with a confusing DIGEST_INVALID).
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	// Read one byte past the cap so a body of exactly the limit succeeds while
	// anything larger is rejected.
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("request body exceeds %d-byte limit", limit)
	}
	return data, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// GetBlob reads a stored blob. The properties APIs the clouds put in front of a
// registry describe an image from the config blob its manifest points at, which
// is where the registry actually holds the platform it was built for.
func (reg *OCIRegistry) GetBlob(scope, repo, digest string) ([]byte, bool) {
	blob, ok := reg.Blobs.Get(ociBlobKey(scope, repo, digest))
	if !ok {
		return nil, false
	}
	return blob.Data, true
}

// DeleteManifest removes a manifest and every alias pointing at the same
// content, which is what the registry's own DELETE does: a manifest removed by
// digest takes its tags with it, and one removed by tag takes the digest entry.
func (reg *OCIRegistry) DeleteManifest(scope, repo, ref string) bool {
	reg.manifestMu.Lock()
	defer reg.manifestMu.Unlock()
	entry, ok := reg.Manifests.Get(ociManifestKey(scope, repo, ref))
	if !ok {
		return false
	}
	for _, m := range reg.Manifests.Filter(func(m OCIManifest) bool {
		return m.Scope == scope && m.Repo == repo && m.Digest == entry.Digest
	}) {
		reg.Manifests.Delete(ociManifestKey(scope, repo, m.Ref))
	}
	reg.Manifests.Delete(ociManifestKey(scope, repo, ref))
	return true
}

// DeleteTagOnly removes a tag and leaves the manifest it pointed at in place.
// That is the difference between deleting a tag and deleting a manifest: the
// content stays addressable by digest, untagged rather than gone.
func (reg *OCIRegistry) DeleteTagOnly(scope, repo, tag string) bool {
	reg.manifestMu.Lock()
	defer reg.manifestMu.Unlock()
	key := ociManifestKey(scope, repo, tag)
	if _, ok := reg.Manifests.Get(key); !ok {
		return false
	}
	reg.Manifests.Delete(key)
	return true
}
