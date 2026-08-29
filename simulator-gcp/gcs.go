package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// gcsHostRoot returns the on-disk backing directory for the whole
// simulated GCS slice. Each bucket becomes a subdirectory so Cloud Run
// tasks the sim launches can bind-mount a real host path and observe
// the same files across invocations.
//
// Resolution order:
//  1. SIM_GCS_DATA_DIR — explicit override.
//  2. <SIM_DATA_DIR>/gcs — when the simulator runs with a data
//     directory, object payloads live next to the SQLite metadata so a
//     restart on the same directory serves the same bytes.
//  3. A temp-dir default, only when neither is set (state is then
//     process-lifetime only, matching the in-memory stores).
func gcsHostRoot() string {
	if dir := os.Getenv("SIM_GCS_DATA_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("SIM_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "gcs")
	}
	return filepath.Join(os.TempDir(), "sockerless-sim-gcs")
}

// GCSBucketHostDir returns the on-disk directory backing a simulated
// GCS bucket. Created lazily; safe for concurrent callers. Exported
// for use by the Cloud Run Jobs/Services + Cloud Functions task
// runners when they honour `Volume{Gcs{Bucket}}`.
func GCSBucketHostDir(bucket string) string {
	dir := filepath.Join(gcsHostRoot(), bucket)
	_ = os.MkdirAll(dir, 0o777)
	return dir
}

// GCS types

// Bucket stores the full JSON object from the API so that terraform read-backs
// return every field the provider expects (id, selfLink, iamConfiguration, etc.).
type Bucket struct {
	Data map[string]any
}

// GCSObject represents a Cloud Storage object (metadata).
type GCSObject struct {
	Name               string            `json:"name"`
	Bucket             string            `json:"bucket"`
	Size               string            `json:"size"`
	ContentType        string            `json:"contentType,omitempty"`
	ContentEncoding    string            `json:"contentEncoding,omitempty"`
	ContentLanguage    string            `json:"contentLanguage,omitempty"`
	ContentDisposition string            `json:"contentDisposition,omitempty"`
	CacheControl       string            `json:"cacheControl,omitempty"`
	StorageClass       string            `json:"storageClass,omitempty"`
	CustomTime         string            `json:"customTime,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	TimeCreated        string            `json:"timeCreated"`
	Updated            string            `json:"updated"`
	Generation         string            `json:"generation,omitempty"`
	Metageneration     string            `json:"metageneration,omitempty"`
	Md5Hash            string            `json:"md5Hash,omitempty"`
	Crc32c             string            `json:"crc32c,omitempty"`
	ComponentCount     int64             `json:"componentCount,omitempty"`
	Etag               string            `json:"etag,omitempty"`
	data               []byte            // unexported: raw object data
	metadataCloned     bool
}

type gcsObjectResource struct {
	Name               string             `json:"name,omitempty"`
	ContentType        *string            `json:"contentType,omitempty"`
	ContentEncoding    *string            `json:"contentEncoding,omitempty"`
	ContentLanguage    *string            `json:"contentLanguage,omitempty"`
	ContentDisposition *string            `json:"contentDisposition,omitempty"`
	CacheControl       *string            `json:"cacheControl,omitempty"`
	StorageClass       *string            `json:"storageClass,omitempty"`
	CustomTime         *string            `json:"customTime,omitempty"`
	Metadata           *map[string]string `json:"metadata,omitempty"`
	// Acl is the object's access controls as carried on the object resource
	// itself. The objectAccessControls collection is one door onto these
	// entries and objects.patch is the other: `gcloud storage objects update
	// --add-acl-grant` writes them here, never through the collection, so a
	// patch that ignored this member acknowledged the grant and dropped it.
	Acl *[]GCSObjectACL `json:"acl,omitempty"`
}

// Package-level store: gcsObjects
// is exposed so other slices (e.g. cloudbuild.go) can read uploaded
// build context tarballs without depending on the gcs.go handler
// closure.
var (
	gcsObjects sim.Store[GCSObject]
	// gcsBuckets is package-level so a slice that exports into Cloud Storage
	// (Artifact Registry's exportArtifact) can refuse a bucket that does not
	// exist rather than inventing one.
	gcsBuckets sim.Store[Bucket]
)

// gcsBucketObjectIndex maps a bucket name to the set of object names it
// holds, so listing one bucket fetches only that bucket's rows instead
// of scanning every object in every bucket (the underlying Store only
// exposes a whole-store Filter). It is a pure optimization derived from
// the object store — `gcsBucketObjects` rebuilds it from the store on
// first use, and every persist/delete keeps it in sync. The object
// store (and the host backing files) remain the source of truth.
var (
	gcsIndexMu     sync.RWMutex
	gcsObjectIndex map[string]map[string]struct{}
	gcsIndexBuilt  bool
)

// gcsIndexRebuildLocked populates gcsObjectIndex from the object store.
// Caller holds gcsIndexMu for writing. Runs once (one full scan) and is
// idempotent thereafter — incremental add/remove keep it current.
func gcsIndexRebuildLocked() {
	gcsObjectIndex = make(map[string]map[string]struct{})
	for _, o := range gcsObjects.List() {
		set := gcsObjectIndex[o.Bucket]
		if set == nil {
			set = make(map[string]struct{})
			gcsObjectIndex[o.Bucket] = set
		}
		set[o.Name] = struct{}{}
	}
	gcsIndexBuilt = true
}

// gcsIndexAdd records bucket/objectName in the per-bucket index.
func gcsIndexAdd(bucket, objectName string) {
	gcsIndexMu.Lock()
	defer gcsIndexMu.Unlock()
	if !gcsIndexBuilt {
		gcsIndexRebuildLocked()
	}
	set := gcsObjectIndex[bucket]
	if set == nil {
		set = make(map[string]struct{})
		gcsObjectIndex[bucket] = set
	}
	set[objectName] = struct{}{}
}

// gcsIndexRemove drops bucket/objectName from the per-bucket index.
func gcsIndexRemove(bucket, objectName string) {
	gcsIndexMu.Lock()
	defer gcsIndexMu.Unlock()
	if !gcsIndexBuilt {
		gcsIndexRebuildLocked()
	}
	if set := gcsObjectIndex[bucket]; set != nil {
		delete(set, objectName)
		if len(set) == 0 {
			delete(gcsObjectIndex, bucket)
		}
	}
}

// gcsBucketObjects returns every object in one bucket, looking each up
// by exact key (bucket/object) via the per-bucket index. This avoids the
// whole-store Filter scan, so listing bucket A doesn't touch bucket B's
// objects. Output is identical to filtering the store by bucket — a
// stale index entry whose object was removed out-of-band is skipped
// (the Get miss), and the store remains authoritative.
func gcsBucketObjects(bucket string) []GCSObject {
	gcsIndexMu.RLock()
	if !gcsIndexBuilt {
		gcsIndexMu.RUnlock()
		gcsIndexMu.Lock()
		if !gcsIndexBuilt {
			gcsIndexRebuildLocked()
		}
		gcsIndexMu.Unlock()
		gcsIndexMu.RLock()
	}
	names := make([]string, 0, len(gcsObjectIndex[bucket]))
	for name := range gcsObjectIndex[bucket] {
		names = append(names, name)
	}
	gcsIndexMu.RUnlock()

	out := make([]GCSObject, 0, len(names))
	for _, name := range names {
		if o, ok := gcsObjects.Get(bucket + "/" + name); ok {
			out = append(out, o)
		}
	}
	return out
}

func (res gcsObjectResource) applyTo(obj GCSObject) GCSObject {
	if res.ContentType != nil {
		obj.ContentType = *res.ContentType
	}
	if res.ContentEncoding != nil {
		obj.ContentEncoding = *res.ContentEncoding
	}
	if res.ContentLanguage != nil {
		obj.ContentLanguage = *res.ContentLanguage
	}
	if res.ContentDisposition != nil {
		obj.ContentDisposition = *res.ContentDisposition
	}
	if res.CacheControl != nil {
		obj.CacheControl = *res.CacheControl
	}
	if res.StorageClass != nil {
		obj.StorageClass = *res.StorageClass
	}
	if res.CustomTime != nil {
		obj.CustomTime = *res.CustomTime
	}
	if res.Metadata != nil {
		obj.Metadata = cloneStringMap(*res.Metadata)
		obj.metadataCloned = true
	}
	return obj
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type invalidGCSObjectMetadataError struct {
	field  string
	reason string
}

func (e *invalidGCSObjectMetadataError) Error() string {
	return fmt.Sprintf("%s: %s", e.field, e.reason)
}

func validateGCSObjectAttrs(attrs GCSObject) error {
	if attrs.CustomTime != "" {
		if _, err := time.Parse(time.RFC3339Nano, attrs.CustomTime); err != nil {
			return &invalidGCSObjectMetadataError{
				field:  "customTime",
				reason: "must be an RFC 3339 timestamp",
			}
		}
	}
	if utf8.RuneCountInString(attrs.ContentLanguage) > 100 {
		return &invalidGCSObjectMetadataError{
			field:  "contentLanguage",
			reason: "must be at most 100 characters",
		}
	}
	return nil
}

func gcsMultipartBoundary(contentType string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err == nil && params["boundary"] != "" {
		return params["boundary"]
	}
	for _, part := range strings.Split(contentType, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "boundary=") {
			return strings.Trim(strings.TrimSpace(part[strings.Index(part, "=")+1:]), `"'`)
		}
	}
	return ""
}

func gcsLocationType(location string) string {
	switch strings.ToUpper(location) {
	case "US", "EU", "ASIA":
		return "multi-region"
	}
	if strings.Contains(location, "+") {
		return "dual-region"
	}
	return "region"
}

// applyBucketPatch merges a PATCH/PUT body into the stored bucket JSON.
// Top-level fields replace; `labels` follows GCS null-value-deletes
// semantics (a null value removes the label, others merge in). Immutable
// server-set fields (id/name/kind/selfLink/timeCreated/projectNumber) are
// not overwritten from the patch body.
func applyBucketPatch(data, patch map[string]any) {
	for k, v := range patch {
		switch k {
		case "id", "name", "kind", "selfLink", "timeCreated", "projectNumber", "metageneration", "etag":
			continue
		case "labels":
			incoming, ok := v.(map[string]any)
			if !ok {
				data["labels"] = v
				continue
			}
			labels, _ := data["labels"].(map[string]any)
			if labels == nil {
				labels = map[string]any{}
			}
			for lk, lv := range incoming {
				if lv == nil {
					delete(labels, lk)
				} else {
					labels[lk] = lv
				}
			}
			data["labels"] = labels
		default:
			data[k] = v
		}
	}
}

func gcsTimestamp() string {
	return time.Now().UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func gcsCRC32C(data []byte) string {
	sum := crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))
	b := []byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)}
	return base64.StdEncoding.EncodeToString(b)
}

// persistGCSObjectMetadata writes a metadata-only update (objects.patch /
// ObjectHandle.Update) to the store. Unlike persistGCSObject it does NOT
// touch the host backing file or bump generation — a metadata patch leaves
// the object payload (and its generation) unchanged, bumping only
// metageneration. Kept as a distinct, auditable write path so the
// data-write invariant (every payload write goes through persistGCSObject
// and reaches the host backing store) stays enforced by the AST guard test.
func persistGCSObjectMetadata(objects sim.Store[GCSObject], key string, obj GCSObject) {
	objects.Put(key, obj)
}

func persistGCSObject(objects sim.Store[GCSObject], bucketName, objectName string, data []byte, attrs GCSObject) (GCSObject, error) {
	if attrs.ContentType == "" {
		attrs.ContentType = "application/octet-stream"
	}
	if err := validateGCSObjectAttrs(attrs); err != nil {
		return GCSObject{}, err
	}
	now := gcsTimestamp()
	hash := md5.Sum(data)
	md5Hash := base64.StdEncoding.EncodeToString(hash[:])
	etag := base64.StdEncoding.EncodeToString(append(hash[:], []byte(now)...))
	generation := int64(1)
	if existing, ok := objects.Get(bucketName + "/" + objectName); ok {
		if n, err := strconv.ParseInt(existing.Generation, 10, 64); err == nil && n >= generation {
			generation = n + 1
		}
	}

	objPath := filepath.Join(GCSBucketHostDir(bucketName), objectName)
	if err := os.MkdirAll(filepath.Dir(objPath), 0o755); err != nil {
		return GCSObject{}, fmt.Errorf("create object dir: %w", err)
	}
	if err := os.WriteFile(objPath, data, 0o644); err != nil {
		return GCSObject{}, fmt.Errorf("write object: %w", err)
	}

	obj := attrs
	obj.Name = objectName
	obj.Bucket = bucketName
	obj.Size = strconv.Itoa(len(data))
	obj.TimeCreated = now
	obj.Updated = now
	obj.Generation = strconv.FormatInt(generation, 10)
	obj.Metageneration = "1"
	obj.Md5Hash = md5Hash
	obj.Crc32c = gcsCRC32C(data)
	obj.Etag = etag
	if !attrs.metadataCloned {
		obj.Metadata = cloneStringMap(attrs.Metadata)
	}
	obj.metadataCloned = true
	obj.data = append([]byte(nil), data...)
	objects.Put(bucketName+"/"+objectName, obj)
	gcsIndexAdd(bucketName, objectName)
	if generation == 1 {
		gcsSeedObjectACL(bucketName, objectName, obj.Generation)
	}
	return obj, nil
}

func writeGCSPersistError(w http.ResponseWriter, action string, err error) {
	var invalid *invalidGCSObjectMetadataError
	if errors.As(err, &invalid) {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s: %v", action, err)
		return
	}
	sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%s: %v", action, err)
}

// gcsResumableSession holds the in-flight state of a resumable upload
// between session initiation (POST with metadata) and finalization
// (the chunk PUT that delivers the last byte). Keyed by upload_id in
// gcsResumableSessions. Real GCS resumable sessions survive server
// restarts (the session URL stays valid for a week), so the session —
// including the buffered bytes — lives in the store rather than a
// process-local map.
type gcsResumableSession struct {
	Bucket string    `json:"bucket"`
	Object string    `json:"object"`
	Attrs  GCSObject `json:"attrs"`
	Data   []byte    `json:"data,omitempty"`
}

var gcsResumableSessions sim.Store[gcsResumableSession]

// gcsResumableMu serializes the read-modify-write a chunk PUT performs
// on its session record. Real clients upload a session's chunks
// sequentially; this guards the store round-trip itself.
var gcsResumableMu sync.Mutex

// handleGCSResumableChunk processes a chunk PUT during a resumable
// upload. The client sends `Content-Range: bytes <start>-<end>/<total>`
// (or `bytes <start>-<end>/*` if total unknown). When `end+1 == total`,
// the upload is complete: the session bytes get persisted as a real
// GCS object and the canonical 200 + object-metadata response goes back.
// Otherwise the sim returns 308 Resume Incomplete with a `Range` header
// naming the bytes received so the client knows where to resume.
func handleGCSResumableChunk(w http.ResponseWriter, r *http.Request, uploadID string, buckets sim.Store[Bucket], objects sim.Store[GCSObject]) {
	sess, ok := gcsResumableSessions.Get(uploadID)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"resumable upload session %q not found", uploadID)
		return
	}
	if _, exists := buckets.Get(sess.Bucket); !exists {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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
		sim.GCPErrorf(w, http.StatusUnsupportedMediaType, "INVALID_ARGUMENT",
			"%s", err.Error())
		return
	}
	chunk, err := io.ReadAll(chunkReader)
	_ = chunkReader.Close()
	if err != nil {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
			"failed to read resumable chunk: %v", err)
		return
	}

	contentRange := r.Header.Get("Content-Range")
	start, end, total, rangeErr := parseGCSContentRange(contentRange, int64(len(chunk)))
	if rangeErr != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"%s", rangeErr.Error())
		return
	}

	gcsResumableMu.Lock()
	sess, ok = gcsResumableSessions.Get(uploadID)
	if !ok {
		gcsResumableMu.Unlock()
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"resumable upload session %q not found", uploadID)
		return
	}
	// Grow the buffer if this chunk extends past current length.
	needed := int(end + 1)
	if needed > len(sess.Data) {
		grown := make([]byte, needed)
		copy(grown, sess.Data)
		sess.Data = grown
	}
	if len(chunk) > 0 {
		copy(sess.Data[start:end+1], chunk)
	}
	dataLen := int64(len(sess.Data))
	if total < 0 || dataLen < total {
		// Buffer the chunk durably so the session resumes with its
		// received bytes even across a simulator restart.
		gcsResumableSessions.Put(uploadID, sess)
		gcsResumableMu.Unlock()
		// Resume Incomplete.
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", dataLen-1))
		if strings.EqualFold(r.Header.Get("X-GUploader-No-308"), "yes") {
			w.Header().Set("X-Http-Status-Code-Override", "308")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(308)
		return
	}

	// Final chunk — finalize the object. Trim accumulated data to
	// the exact total (in case the buffer was over-grown).
	finalData := sess.Data[:total]
	gcsResumableSessions.Delete(uploadID)
	gcsResumableMu.Unlock()

	obj, err := persistGCSObject(objects, sess.Bucket, sess.Object, finalData, sess.Attrs)
	if err != nil {
		writeGCSPersistError(w, "write resumable object", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
}

// parseGCSContentRange parses `Content-Range: bytes <start>-<end>/<total>`
// (total may be `*`). Returns (start, end, total) where total is -1
// when the header carries `/*`. An absent header assumes the chunk is
// the entire object; any other malformed shape returns an error so
// the caller can emit a real 400, matching GCS's Content-Range
// validation.
func parseGCSContentRange(s string, chunkLen int64) (start, end, total int64, err error) {
	if s == "" {
		return 0, chunkLen - 1, chunkLen, nil
	}
	raw := s
	if strings.HasPrefix(s, "bytes */") {
		if _, e := fmt.Sscanf(strings.TrimPrefix(s, "bytes */"), "%d", &total); e != nil {
			return 0, 0, 0, fmt.Errorf("Content-Range %q: bad total: %v", raw, e)
		}
		return 0, -1, total, nil
	}
	if !strings.HasPrefix(s, "bytes ") {
		return 0, 0, 0, fmt.Errorf("Content-Range %q: missing `bytes ` unit", raw)
	}
	s = strings.TrimPrefix(s, "bytes ")
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return 0, 0, 0, fmt.Errorf("Content-Range %q: missing `/` separator", raw)
	}
	rangePart := s[:slash]
	totalPart := s[slash+1:]
	dash := strings.IndexByte(rangePart, '-')
	if dash < 0 {
		return 0, 0, 0, fmt.Errorf("Content-Range %q: missing `-` in range", raw)
	}
	if _, e := fmt.Sscanf(rangePart[:dash], "%d", &start); e != nil {
		return 0, 0, 0, fmt.Errorf("Content-Range %q: bad start: %v", raw, e)
	}
	if _, e := fmt.Sscanf(rangePart[dash+1:], "%d", &end); e != nil {
		return 0, 0, 0, fmt.Errorf("Content-Range %q: bad end: %v", raw, e)
	}
	if totalPart == "*" {
		return start, end, -1, nil
	}
	if _, e := fmt.Sscanf(totalPart, "%d", &total); e != nil {
		return 0, 0, 0, fmt.Errorf("Content-Range %q: bad total: %v", raw, e)
	}
	return start, end, total, nil
}

// gcsObjectMetadata returns the canonical object-metadata response
// shape with hard-coded https URLs (real GCS is HTTPS-only on the
// JSON API surface).
func gcsObjectMetadata(r *http.Request, obj GCSObject) map[string]any {
	escapedObject := url.PathEscape(obj.Name)
	selfLink := fmt.Sprintf("https://%s/storage/v1/b/%s/o/%s", r.Host, obj.Bucket, escapedObject)
	mediaLink := fmt.Sprintf("https://%s/download/storage/v1/b/%s/o/%s?alt=media", r.Host, obj.Bucket, escapedObject)
	meta := map[string]any{
		"kind":           "storage#object",
		"id":             fmt.Sprintf("%s/%s/1", obj.Bucket, obj.Name),
		"selfLink":       selfLink,
		"mediaLink":      mediaLink,
		"name":           obj.Name,
		"bucket":         obj.Bucket,
		"generation":     defaultStr(obj.Generation, "1"),
		"metageneration": defaultStr(obj.Metageneration, "1"),
		"size":           obj.Size,
		"contentType":    obj.ContentType,
		"timeCreated":    obj.TimeCreated,
		"updated":        obj.Updated,
		"crc32c":         obj.Crc32c,
		"etag":           obj.Etag,
	}
	if obj.Md5Hash != "" {
		meta["md5Hash"] = obj.Md5Hash
	}
	if obj.ComponentCount > 0 {
		meta["componentCount"] = obj.ComponentCount
	}
	if obj.ContentEncoding != "" {
		meta["contentEncoding"] = obj.ContentEncoding
	}
	if obj.ContentLanguage != "" {
		meta["contentLanguage"] = obj.ContentLanguage
	}
	if obj.ContentDisposition != "" {
		meta["contentDisposition"] = obj.ContentDisposition
	}
	if obj.CacheControl != "" {
		meta["cacheControl"] = obj.CacheControl
	}
	if obj.StorageClass != "" {
		meta["storageClass"] = obj.StorageClass
	}
	if obj.CustomTime != "" {
		meta["customTime"] = obj.CustomTime
	}
	if len(obj.Metadata) > 0 {
		meta["metadata"] = cloneStringMap(obj.Metadata)
	}
	// The full projection carries the object's access controls; the default
	// noAcl projection omits them, which is the difference between the two.
	if r.URL.Query().Get("projection") == "full" {
		if entries := gcsObjectACLEntries(obj.Bucket, obj.Name); len(entries) > 0 {
			meta["acl"] = entries
		}
	}
	return meta
}

func requestScheme(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded != "" {
		return forwarded
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func registerGCS(srv *sim.Server) {
	buckets := sim.MakeStore[Bucket](srv.DB(), "gcs_buckets")
	gcsBuckets = buckets
	gcsObjects = sim.MakeStore[GCSObject](srv.DB(), "gcs_objects")
	gcsResumableSessions = sim.MakeStore[gcsResumableSession](srv.DB(), "gcs_resumable_sessions")
	objects := gcsObjects
	// Cloud Storage's long-running methods record into the operation store
	// every Google slice shares, whichever register function reaches it first.
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	// Create bucket
	srv.HandleFunc("POST /storage/v1/b", func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if err := sim.ReadJSON(r, &data); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name, _ := data["name"].(string)
		if name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}

		if _, exists := buckets.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "bucket %q already exists", name)
			return
		}

		now := gcsTimestamp()
		data["id"] = name
		data["kind"] = "storage#bucket"
		data["selfLink"] = gcpSelfLink(r, fmt.Sprintf("/storage/v1/b/%s", name))
		data["projectNumber"] = "123456789012"
		data["metageneration"] = "1"
		data["etag"] = "CAE="
		data["timeCreated"] = now
		data["updated"] = now
		// GCS normalizes bucket locations to upper-case on read; echo that or
		// terraform-provider-google replaces the bucket (location is ForceNew).
		if loc, ok := data["location"].(string); ok && loc != "" {
			data["location"] = strings.ToUpper(loc)
		} else {
			data["location"] = "US"
		}
		if data["storageClass"] == nil {
			data["storageClass"] = "STANDARD"
		}
		gcsApplyDefaultSoftDeletePolicy(data)

		bucket := Bucket{Data: data}
		buckets.Put(name, bucket)
		gcsSeedDefaultObjectACL(name, bucket)
		sim.WriteJSON(w, http.StatusOK, data)
	})

	// Get bucket
	srv.HandleFunc("GET /storage/v1/b/{bucket}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")

		// Don't match if the path continues with /o (objects)
		if strings.Contains(r.URL.Path, "/o/") || strings.HasSuffix(r.URL.Path, "/o") {
			return
		}

		bucket, ok := buckets.Get(bucketName)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, bucket.Data)
	})

	// Patch / Update bucket. The Go storage client's BucketHandle.Update
	// and terraform-provider-google send PATCH (PUT for full Update) to
	// mutate versioning/lifecycle/labels/iamConfiguration/retentionPolicy.
	// The bucket stores its JSON verbatim, so apply the patch body as a
	// top-level field merge into that map. `labels` honours GCS null-value-
	// deletes semantics (a null label value removes the key).
	patchBucket := func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		if strings.Contains(r.URL.Path, "/o/") || strings.HasSuffix(r.URL.Path, "/o") {
			return
		}
		bucket, ok := buckets.Get(bucketName)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		var patch map[string]any
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		applyBucketPatch(bucket.Data, patch)
		bucket.Data["updated"] = gcsTimestamp()
		buckets.Put(bucketName, bucket)
		sim.WriteJSON(w, http.StatusOK, bucket.Data)
	}
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}", patchBucket)
	srv.HandleFunc("PUT /storage/v1/b/{bucket}", patchBucket)

	// Get bucket storage layout
	srv.HandleFunc("GET /storage/v1/b/{bucket}/storageLayout", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		bucket, ok := buckets.Get(bucketName)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}

		location, _ := bucket.Data["location"].(string)
		if location == "" {
			location = "US"
		}
		enabled := false
		if hns, ok := bucket.Data["hierarchicalNamespace"].(map[string]any); ok {
			enabled, _ = hns["enabled"].(bool)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":         "storage#storageLayout",
			"bucket":       bucketName,
			"location":     location,
			"locationType": gcsLocationType(location),
			"hierarchicalNamespace": map[string]any{
				"enabled": enabled,
			},
		})
	})

	// Delete bucket
	srv.HandleFunc("DELETE /storage/v1/b/{bucket}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")

		if !buckets.Delete(bucketName) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}

		// Delete all objects in the bucket (index-scoped — only this
		// bucket's rows, not every object in the store).
		for _, obj := range gcsBucketObjects(bucketName) {
			objects.Delete(bucketName + "/" + obj.Name)
			gcsIndexRemove(bucketName, obj.Name)
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// List buckets
	srv.HandleFunc("GET /storage/v1/b", func(w http.ResponseWriter, r *http.Request) {
		all := buckets.List()
		sort.Slice(all, func(i, j int) bool {
			ni, _ := all[i].Data["name"].(string)
			nj, _ := all[j].Data["name"].(string)
			return ni < nj
		})
		items := make([]map[string]any, 0, len(all))
		for _, b := range all {
			items = append(items, b.Data)
		}
		page, next, ok := paginateListGCS(w, r, items)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "storage#buckets", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// List objects
	srv.HandleFunc("GET /storage/v1/b/{bucket}/o", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		prefix := r.URL.Query().Get("prefix")
		delimiter := r.URL.Query().Get("delimiter")

		if _, ok := buckets.Get(bucketName); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}

		// softDeleted=true lists the retired generations instead of the live
		// objects — the listing a client reads to find what restore can bring
		// back. The two selections are exclusive, as they are in the service.
		if gcsQueryBool(r, "softDeleted") {
			retired := gcsSoftDeletedListing(bucketName, prefix)
			items := make([]map[string]any, 0, len(retired))
			for _, entry := range retired {
				resource := gcsObjectMetadata(r, entry.Object)
				resource["softDeleteTime"] = entry.SoftDeleteTime
				resource["hardDeleteTime"] = entry.HardDeleteTime
				items = append(items, resource)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#objects", "items": items})
			return
		}

		// Index-scoped: fetch only this bucket's objects, then apply the
		// prefix filter. Same result as filtering the whole store by
		// bucket+prefix, without scanning other buckets' objects.
		var allObjects []GCSObject
		for _, o := range gcsBucketObjects(bucketName) {
			if prefix != "" && !strings.HasPrefix(o.Name, prefix) {
				continue
			}
			allObjects = append(allObjects, o)
		}

		var items []map[string]any
		var prefixes []string
		seen := make(map[string]bool)

		for _, obj := range allObjects {
			if delimiter != "" && prefix != "" {
				// Check if there's a delimiter after the prefix
				rest := strings.TrimPrefix(obj.Name, prefix)
				if idx := strings.Index(rest, delimiter); idx >= 0 {
					p := prefix + rest[:idx+len(delimiter)]
					if !seen[p] {
						prefixes = append(prefixes, p)
						seen[p] = true
					}
					continue
				}
			} else if delimiter != "" {
				if idx := strings.Index(obj.Name, delimiter); idx >= 0 {
					p := obj.Name[:idx+len(delimiter)]
					if !seen[p] {
						prefixes = append(prefixes, p)
						seen[p] = true
					}
					continue
				}
			}
			items = append(items, gcsObjectMetadata(r, obj))
		}

		if items == nil {
			items = []map[string]any{}
		}
		sort.Slice(items, func(i, j int) bool {
			ni, _ := items[i]["name"].(string)
			nj, _ := items[j]["name"].(string)
			return ni < nj
		})
		sort.Strings(prefixes)

		page, next, ok := paginateListGCS(w, r, items)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "storage#objects", "items": page}
		if len(prefixes) > 0 {
			resp["prefixes"] = prefixes
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Get object metadata or media. The JSON API uses the same object
	// resource path for both; alt=media switches the response from the
	// metadata resource to the stored object bytes.
	srv.HandleFunc("GET /storage/v1/b/{bucket}/o/{object...}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName

		obj, ok := objects.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			body, err := gcsObjectBytes(obj, bucketName, objectName)
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
				return
			}
			setGCSObjectResponseHeaders(w.Header(), obj, len(body))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
	})

	// Patch / Update object metadata. ObjectHandle.Update (and the JSON
	// API's objects.patch / objects.update) mutate contentType /
	// cacheControl / metadata etc. in place without re-uploading the
	// payload. Merge the resource fields onto the stored object.
	patchObject := func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName
		obj, ok := objects.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}
		var res gcsObjectResource
		if err := sim.ReadJSON(r, &res); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		obj = res.applyTo(obj)
		if err := validateGCSObjectAttrs(obj); err != nil {
			writeGCSPersistError(w, "patch object", err)
			return
		}
		obj.Updated = gcsTimestamp()
		if mg, err := strconv.ParseInt(obj.Metageneration, 10, 64); err == nil {
			obj.Metageneration = strconv.FormatInt(mg+1, 10)
		}
		persistGCSObjectMetadata(objects, key, obj)
		if res.Acl != nil {
			gcsReplaceObjectACL(bucketName, objectName, obj.Generation, *res.Acl)
		}
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
	}
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/o/{object...}", patchObject)
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/o/{object...}", patchObject)

	// Delete object
	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/o/{object...}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName

		obj, found := objects.Get(key)
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}
		bucket, ok := buckets.Get(bucketName)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		objects.Delete(key)
		gcsIndexRemove(bucketName, objectName)
		// Under a soft-delete policy the object is retired rather than
		// destroyed, and its payload is retained for objects.restore to bring
		// back. Without one it is destroyed here, bytes included.
		gcsRetireObject(bucket, bucketName, obj)

		w.WriteHeader(http.StatusNoContent)
	})

	// Resumable chunk uploads come back as PUT on the same path with
	// `upload_id` in the query — share the same dispatch by treating
	// PUT identically to POST for the upload route.
	srv.HandleFunc("PUT /upload/storage/v1/b/{bucket}/o", func(w http.ResponseWriter, r *http.Request) {
		uploadID := r.URL.Query().Get("upload_id")
		if uploadID == "" {
			sim.GCPError(w, http.StatusBadRequest,
				"PUT /upload/... requires upload_id (resumable chunk only)",
				"INVALID_ARGUMENT")
			return
		}
		handleGCSResumableChunk(w, r, uploadID, buckets, objects)
	})

	// Upload object
	srv.HandleFunc("POST /upload/storage/v1/b/{bucket}/o", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := r.URL.Query().Get("name")
		uploadType := r.URL.Query().Get("uploadType")
		uploadID := r.URL.Query().Get("upload_id")

		if _, ok := buckets.Get(bucketName); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}

		var data []byte
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
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
					"failed to read resumable metadata: %v", err)
				return
			}
			if len(body) > 0 {
				if err := json.Unmarshal(body, &meta); err != nil {
					sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"failed to parse resumable metadata: %v", err)
					return
				}
			}
			if objectName == "" {
				objectName = meta.Name
			}
			if objectName == "" {
				sim.GCPError(w, http.StatusBadRequest,
					"name is required (in query or body)", "INVALID_ARGUMENT")
				return
			}
			objAttrs = meta.applyTo(objAttrs)
			if err := validateGCSObjectAttrs(objAttrs); err != nil {
				writeGCSPersistError(w, "init resumable object", err)
				return
			}
			sessionID := generateUUID()
			gcsResumableSessions.Put(sessionID, gcsResumableSession{
				Bucket: bucketName,
				Object: objectName,
				Attrs:  objAttrs,
				Data:   nil,
			})
			location := fmt.Sprintf("%s://%s/upload/storage/v1/b/%s/o?uploadType=resumable&upload_id=%s",
				requestScheme(r), r.Host, bucketName, sessionID)
			w.Header().Set("Location", location)
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
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "failed to read metadata part: %v", err)
				return
			}
			metaBytes, err := io.ReadAll(metaPart)
			_ = metaPart.Close()
			if err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"failed to read multipart metadata part: %v", err)
				return
			}
			if len(metaBytes) > 0 {
				var meta gcsObjectResource
				if jsonErr := json.Unmarshal(metaBytes, &meta); jsonErr != nil {
					sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"failed to parse multipart metadata: %v", jsonErr)
					return
				}
				if objectName == "" {
					objectName = meta.Name
				}
				objAttrs = meta.applyTo(objAttrs)
			}
			if objectName == "" {
				sim.GCPError(w, http.StatusBadRequest,
					"name is required (in query or multipart metadata)", "INVALID_ARGUMENT")
				return
			}
			// Read data part
			dataPart, err := mr.NextPart()
			if err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "failed to read data part: %v", err)
				return
			}
			if objAttrs.ContentType == "" {
				objAttrs.ContentType = dataPart.Header.Get("Content-Type")
			}
			data, err = io.ReadAll(dataPart)
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "failed to read data: %v", err)
				return
			}
		} else {
			if objectName == "" {
				sim.GCPError(w, http.StatusBadRequest,
					"name query parameter is required", "INVALID_ARGUMENT")
				return
			}
			// Simple upload (streaming-aware: handles gzip).
			rc, err := openStreamingBody(r)
			if err != nil {
				sim.GCPErrorf(w, http.StatusUnsupportedMediaType, "INVALID_ARGUMENT", "%s", err.Error())
				return
			}
			data, err = io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "failed to read body: %v", err)
				return
			}
			objAttrs.ContentType = ct
		}

		obj, err := persistGCSObject(objects, bucketName, objectName, data, objAttrs)
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
	})

	// Objects.compose — concatenate source objects into a destination
	// object. Real GCS reads up to 32 source objects in order and
	// emits a single composed object; the Go SDK's high-level write
	// path uses this for compositing, and any S3-multipart-equivalent
	// against GCS uses it as the joining primitive.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/o/{destObject...}", func(w http.ResponseWriter, r *http.Request) {
		if handled := handleGCSObjectCopyRequest(w, r, buckets, objects); handled {
			return
		}
		// Only dispatch :compose paths — other POSTs at this prefix
		// should fall through to the upload handler family.
		destObject := sim.PathParam(r, "destObject")
		if !strings.HasSuffix(destObject, "/compose") {
			http.NotFound(w, r)
			return
		}
		destObject = strings.TrimSuffix(destObject, "/compose")
		bucketName := sim.PathParam(r, "bucket")
		if _, ok := buckets.Get(bucketName); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		var req struct {
			SourceObjects []struct {
				Name       string `json:"name"`
				Generation string `json:"generation,omitempty"`
			} `json:"sourceObjects"`
			Destination *gcsObjectResource `json:"destination"`
		}
		// Compose request body is a fixed-shape JSON document
		// (sourceObjects + destination); the GCS Go SDK does not
		// content-encode control-plane JSON bodies. No
		// openStreamingBody wrap needed.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"failed to parse compose request: %v", err)
			return
		}
		if len(req.SourceObjects) == 0 {
			sim.GCPError(w, http.StatusBadRequest,
				"compose requires at least one sourceObject", "INVALID_ARGUMENT")
			return
		}
		var composed []byte
		var componentCount int64
		for _, src := range req.SourceObjects {
			srcObj, ok := objects.Get(bucketName + "/" + src.Name)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"source object %q not found in bucket %q", src.Name, bucketName)
				return
			}
			if srcObj.ComponentCount > 0 {
				componentCount += srcObj.ComponentCount
			} else {
				componentCount++
			}
			srcBytes, err := gcsObjectBytes(srcObj, bucketName, src.Name)
			if err != nil {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
				return
			}
			composed = append(composed, srcBytes...)
		}
		objAttrs := GCSObject{}
		if req.Destination != nil {
			objAttrs = req.Destination.applyTo(objAttrs)
		}
		composedObj, err := persistGCSObject(objects, bucketName, destObject, composed, objAttrs)
		if err != nil {
			writeGCSPersistError(w, "write composed object", err)
			return
		}
		composedObj.ComponentCount = componentCount
		composedObj.Md5Hash = ""
		objects.Update(bucketName+"/"+destObject, func(o *GCSObject) {
			o.ComponentCount = componentCount
			o.Md5Hash = ""
		})
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, composedObj))
	})

	// XML API style object access (used by cloud.google.com/go/storage for reads).
	// Registered without method prefix to avoid conflict with "/v2/" (both match all methods,
	// resolved by path specificity — more specific literal paths always win).
	//
	// The first path segment is the BUCKET name. Reject (404) when
	// the store has no matching bucket: this catch-all
	// `/{bucket}/{object...}` route would otherwise shadow unrelated
	// top-level paths (e.g. AIP-151 `/v1/...` operations) and answer
	// them with a GCS-shaped 404 that looks like a real GCS not-found
	// to clients.
	//
	// Because the pattern matches every URI, the credential gate that fronts
	// the published API methods does not cover it — a request this route does
	// not serve must not read as an authentication failure. The bearer is
	// verified here instead, once the bucket has resolved: a path that names no
	// bucket is not found, and a real bucket requires the token.
	srv.HandleFunc("/{bucket}/{object...}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		if objectName == "" {
			http.NotFound(w, r)
			return
		}
		if _, ok := buckets.Get(bucketName); !ok {
			http.NotFound(w, r)
			return
		}
		if !verifyRequestBearer(w, r) {
			return
		}
		key := bucketName + "/" + objectName

		obj, ok := objects.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}

		body, err := gcsObjectBytes(obj, bucketName, objectName)
		if err != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
			return
		}
		setGCSObjectResponseHeaders(w.Header(), obj, len(body))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})

	// Download object data (JSON API)
	srv.HandleFunc("GET /download/storage/v1/b/{bucket}/o/{object...}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName

		obj, ok := objects.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}

		body, err := gcsObjectBytes(obj, bucketName, objectName)
		if err != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
			return
		}
		setGCSObjectResponseHeaders(w.Header(), obj, len(body))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})

	registerGCSExtras(srv, buckets, objects)
}

func setGCSObjectResponseHeaders(h http.Header, obj GCSObject, size int) {
	h.Set("Content-Type", obj.ContentType)
	h.Set("Content-Length", strconv.Itoa(size))
	if obj.ContentEncoding != "" {
		h.Set("Content-Encoding", obj.ContentEncoding)
	}
	if obj.ContentLanguage != "" {
		h.Set("Content-Language", obj.ContentLanguage)
	}
	if obj.ContentDisposition != "" {
		h.Set("Content-Disposition", obj.ContentDisposition)
	}
	if obj.CacheControl != "" {
		h.Set("Cache-Control", obj.CacheControl)
	}
	for k, v := range obj.Metadata {
		h.Set("X-Goog-Meta-"+k, v)
	}
}

func handleGCSObjectCopyRequest(w http.ResponseWriter, r *http.Request, buckets sim.Store[Bucket], objects sim.Store[GCSObject]) bool {
	srcBucket, srcObject, dstBucket, dstObject, op, ok := parseGCSCopyPath(r)
	if !ok {
		return false
	}
	if _, ok := buckets.Get(srcBucket); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", srcBucket)
		return true
	}
	if _, ok := buckets.Get(dstBucket); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", dstBucket)
		return true
	}
	copied, ok := copyGCSObject(w, r, srcBucket, srcObject, dstBucket, dstObject, objects)
	if !ok {
		return true
	}
	switch op {
	case "rewriteTo":
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":                "storage#rewriteResponse",
			"totalBytesRewritten": copied.Size,
			"objectSize":          copied.Size,
			"done":                true,
			"resource":            gcsObjectMetadata(r, copied),
		})
	case "copyTo":
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, copied))
	}
	return true
}

func parseGCSCopyPath(r *http.Request) (srcBucket, srcObject, dstBucket, dstObject, op string, ok bool) {
	srcBucket = sim.PathParam(r, "bucket")
	if srcBucket == "" {
		return "", "", "", "", "", false
	}
	prefix := "/storage/v1/b/" + url.PathEscape(srcBucket) + "/o/"
	escapedPath := r.URL.EscapedPath()
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", "", "", "", "", false
	}
	rest := strings.TrimPrefix(escapedPath, prefix)
	for _, candidate := range []string{"rewriteTo", "copyTo"} {
		marker := "/" + candidate + "/b/"
		idx := strings.LastIndex(rest, marker)
		if idx < 0 {
			continue
		}
		before := rest[:idx]
		after := rest[idx+len(marker):]
		dstBucketEsc, dstObjectEsc, found := strings.Cut(after, "/o/")
		if !found || before == "" || dstBucketEsc == "" || dstObjectEsc == "" {
			return "", "", "", "", "", false
		}
		srcObject, ok = pathUnescape(before)
		if !ok {
			return "", "", "", "", "", false
		}
		dstBucket, ok = pathUnescape(dstBucketEsc)
		if !ok {
			return "", "", "", "", "", false
		}
		dstObject, ok = pathUnescape(dstObjectEsc)
		if !ok {
			return "", "", "", "", "", false
		}
		return srcBucket, srcObject, dstBucket, dstObject, candidate, true
	}
	return "", "", "", "", "", false
}

func pathUnescape(s string) (string, bool) {
	out, err := url.PathUnescape(s)
	if err != nil {
		return "", false
	}
	return out, true
}

func copyGCSObject(w http.ResponseWriter, r *http.Request, srcBucket, srcObject, dstBucket, dstObject string, objects sim.Store[GCSObject]) (GCSObject, bool) {
	src, ok := objects.Get(srcBucket + "/" + srcObject)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"source object %q not found in bucket %q", srcObject, srcBucket)
		return GCSObject{}, false
	}
	dstAttrs := src
	if r.Body != nil {
		defer r.Body.Close()
		var meta gcsObjectResource
		if err := json.NewDecoder(r.Body).Decode(&meta); err != nil && err != io.EOF {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"failed to parse copy metadata: %v", err)
			return GCSObject{}, false
		}
		dstAttrs = meta.applyTo(dstAttrs)
	}
	srcBytes, err := gcsObjectBytes(src, srcBucket, srcObject)
	if err != nil {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return GCSObject{}, false
	}
	data := append([]byte(nil), srcBytes...)
	dst, err := persistGCSObject(objects, dstBucket, dstObject, data, dstAttrs)
	if err != nil {
		writeGCSPersistError(w, "write copied object", err)
		return GCSObject{}, false
	}
	return dst, true
}

// gcsObjectBytes returns the object's payload bytes. Prefers the
// in-memory copy when present (uploaded in the same process lifetime);
// otherwise reads the on-disk file at <gcsHostRoot>/<bucket>/<object>
// (which IS the source of truth — the in-memory `data` field is
// stripped by the SQLite-backed sim.Store's JSON round-trip on every
// Get). A read failure is a real error (the object metadata exists but
// its payload is unreadable) and is returned so the caller fails the
// request loudly rather than serving an empty body. A legitimately
// empty object reads as a zero-length file with no error.
func gcsObjectBytes(obj GCSObject, bucket, object string) ([]byte, error) {
	if len(obj.data) > 0 {
		return obj.data, nil
	}
	body, err := os.ReadFile(filepath.Join(GCSBucketHostDir(bucket), object))
	if err != nil {
		return nil, fmt.Errorf("read object payload %s/%s: %w", bucket, object, err)
	}
	return body, nil
}

// GCSObjectBytes is exported for cross-package callers (e.g.
// cloudbuild.go's executeBuild source-fetch). It errors when the object
// is unknown or its payload is unreadable.
func GCSObjectBytes(bucket, object string) ([]byte, error) {
	obj, ok := gcsObjects.Get(bucket + "/" + object)
	if !ok {
		return nil, fmt.Errorf("object %s/%s not found", bucket, object)
	}
	return gcsObjectBytes(obj, bucket, object)
}

// The types below mirror the storage v1 Discovery schemas exactly (field
// names + the constant `kind` discriminator) so the runtime spec-validator
// — which rejects any member the schema does not define — passes. Each is a
// real CRUD resource tied to the existing bucket/object stores.

// GCSBucketACL mirrors the storage#bucketAccessControl resource.
type GCSBucketACL struct {
	Kind        string          `json:"kind"`
	ID          string          `json:"id"`
	SelfLink    string          `json:"selfLink,omitempty"`
	Bucket      string          `json:"bucket"`
	Entity      string          `json:"entity"`
	Role        string          `json:"role"`
	Email       string          `json:"email,omitempty"`
	EntityID    string          `json:"entityId,omitempty"`
	Domain      string          `json:"domain,omitempty"`
	ProjectTeam *gcsProjectTeam `json:"projectTeam,omitempty"`
	Etag        string          `json:"etag,omitempty"`
}

// GCSObjectACL mirrors the storage#objectAccessControl resource. The
// default-object-ACL surface reuses it (same schema, the `object`/`generation`
// fields stay empty for a bucket-level default entry).
type GCSObjectACL struct {
	Kind        string          `json:"kind"`
	ID          string          `json:"id"`
	SelfLink    string          `json:"selfLink,omitempty"`
	Bucket      string          `json:"bucket"`
	Object      string          `json:"object,omitempty"`
	Generation  string          `json:"generation,omitempty"`
	Entity      string          `json:"entity"`
	Role        string          `json:"role"`
	Email       string          `json:"email,omitempty"`
	EntityID    string          `json:"entityId,omitempty"`
	Domain      string          `json:"domain,omitempty"`
	ProjectTeam *gcsProjectTeam `json:"projectTeam,omitempty"`
	Etag        string          `json:"etag,omitempty"`
}

type gcsProjectTeam struct {
	ProjectNumber string `json:"projectNumber,omitempty"`
	Team          string `json:"team,omitempty"`
}

// GCSFolder mirrors the storage#folder resource (HNS-enabled buckets).
type GCSFolder struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	SelfLink       string `json:"selfLink,omitempty"`
	Bucket         string `json:"bucket"`
	Name           string `json:"name"`
	Metageneration string `json:"metageneration,omitempty"`
	CreateTime     string `json:"createTime,omitempty"`
	UpdateTime     string `json:"updateTime,omitempty"`
}

// GCSManagedFolder mirrors the storage#managedFolder resource.
// RapidCacheConfig is the folder's one mutable member (a policies map of
// rapid cache IDs to RapidCachePolicy objects, per the schema); it is
// persisted verbatim
// so patch→get round-trips byte-exact.
type GCSManagedFolder struct {
	Kind             string          `json:"kind"`
	ID               string          `json:"id"`
	SelfLink         string          `json:"selfLink,omitempty"`
	Bucket           string          `json:"bucket"`
	Name             string          `json:"name"`
	Metageneration   string          `json:"metageneration,omitempty"`
	CreateTime       string          `json:"createTime,omitempty"`
	UpdateTime       string          `json:"updateTime,omitempty"`
	RapidCacheConfig json.RawMessage `json:"rapidCacheConfig,omitempty"`
}

// GCSNotification mirrors the storage#notification resource.
type GCSNotification struct {
	Kind             string            `json:"kind"`
	ID               string            `json:"id"`
	SelfLink         string            `json:"selfLink,omitempty"`
	Topic            string            `json:"topic"`
	PayloadFormat    string            `json:"payload_format,omitempty"`
	EventTypes       []string          `json:"event_types,omitempty"`
	CustomAttributes map[string]string `json:"custom_attributes,omitempty"`
	ObjectNamePrefix string            `json:"object_name_prefix,omitempty"`
	Etag             string            `json:"etag,omitempty"`

	bucket string // unexported: store-key scoping, never serialized
}

// GCSHmacKey mirrors the storage#hmacKeyMetadata resource. The secret is
// stored on the record but only ever emitted in the create response
// (storage#hmacKey), matching real GCS.
type GCSHmacKey struct {
	Kind                string `json:"kind"`
	ID                  string `json:"id"`
	AccessID            string `json:"accessId"`
	ProjectID           string `json:"projectId"`
	ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`
	State               string `json:"state"`
	TimeCreated         string `json:"timeCreated,omitempty"`
	Updated             string `json:"updated,omitempty"`
	SelfLink            string `json:"selfLink,omitempty"`
	Etag                string `json:"etag,omitempty"`

	secret string // unexported: returned only on create, never on read/list
}

var (
	gcsBucketACLs     sim.Store[GCSBucketACL]
	gcsObjectDefACLs  sim.Store[GCSObjectACL]
	gcsFolders        sim.Store[GCSFolder]
	gcsManagedFolders sim.Store[GCSManagedFolder]
	gcsNotifications  sim.Store[GCSNotification]
	gcsHmacKeys       sim.Store[GCSHmacKey]
)

// gcsACLEmailFor derives the email/projectTeam fields from an ACL entity in
// the documented forms (user-<email>, group-<email>, project-team-<id>,
// allUsers, allAuthenticatedUsers) so the returned resource carries the
// derived members the schema describes.
func gcsACLEmailFor(entity string) (email string, team *gcsProjectTeam) {
	switch {
	case strings.HasPrefix(entity, "user-") && strings.Contains(entity, "@"):
		return strings.TrimPrefix(entity, "user-"), nil
	case strings.HasPrefix(entity, "group-") && strings.Contains(entity, "@"):
		return strings.TrimPrefix(entity, "group-"), nil
	case strings.HasPrefix(entity, "project-"):
		rest := strings.TrimPrefix(entity, "project-")
		if i := strings.LastIndex(rest, "-"); i > 0 {
			return "", &gcsProjectTeam{ProjectNumber: rest[i+1:], Team: rest[:i]}
		}
	}
	return "", nil
}

// registerGCSExtras mounts the remaining storage v1 control-plane surfaces —
// bucket ACLs, default object ACLs, folders, managed folders (+ their IAM),
// notification configs, HMAC keys, anywhere caches, bucket operations,
// bucket-level lifecycle verbs (relocate/restore/lockRetentionPolicy),
// channels.stop, the project service account, and the metadata-only object
// insert. Each is a faithful slice of the real JSON API.
func registerGCSExtras(srv *sim.Server, buckets sim.Store[Bucket], objects sim.Store[GCSObject]) {
	gcsBucketACLs = sim.MakeStore[GCSBucketACL](srv.DB(), "gcs_bucket_acls")
	gcsObjectDefACLs = sim.MakeStore[GCSObjectACL](srv.DB(), "gcs_default_object_acls")
	gcsObjectACLs = sim.MakeStore[GCSObjectACL](srv.DB(), "gcs_object_acls")
	gcsSoftDeletedObjects = sim.MakeStore[gcsSoftDeleted](srv.DB(), "gcs_soft_deleted_objects")
	gcsFolders = sim.MakeStore[GCSFolder](srv.DB(), "gcs_folders")
	gcsManagedFolders = sim.MakeStore[GCSManagedFolder](srv.DB(), "gcs_managed_folders")
	gcsNotifications = sim.MakeStore[GCSNotification](srv.DB(), "gcs_notifications")
	gcsHmacKeys = sim.MakeStore[GCSHmacKey](srv.DB(), "gcs_hmac_keys")

	bucketExists := func(w http.ResponseWriter, name string) bool {
		if _, ok := buckets.Get(name); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", name)
			return false
		}
		return true
	}

	registerGCSBucketACLs(srv, buckets, bucketExists)
	registerGCSDefaultObjectACLs(srv, buckets, bucketExists)
	registerGCSObjectACLs(srv, buckets, objects)
	registerGCSObjectIAM(srv, buckets, objects)
	registerGCSObjectRestore(srv, buckets, objects)
	registerGCSFolders(srv, buckets, bucketExists)
	registerGCSManagedFolders(srv, buckets, bucketExists)
	registerGCSNotifications(srv, buckets, bucketExists)
	registerGCSHmacKeys(srv)
	registerGCSAnywhereCaches(srv, buckets, bucketExists)
	registerGCSRapidCaches(srv, buckets, bucketExists)
	registerGCSBucketLifecycle(srv, buckets, objects, bucketExists)
}

func registerGCSBucketACLs(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, entity string) string { return bucket + "\x00" + entity }
	build := func(r *http.Request, bucket, entity, role string) GCSBucketACL {
		email, team := gcsACLEmailFor(entity)
		return GCSBucketACL{
			Kind:        "storage#bucketAccessControl",
			ID:          bucket + "/" + entity,
			SelfLink:    gcpSelfLink(r, "/storage/v1/b/"+bucket+"/acl/"+entity),
			Bucket:      bucket,
			Entity:      entity,
			Role:        role,
			Email:       email,
			ProjectTeam: team,
			Etag:        "CAE=",
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/acl", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := gcsBucketACLs.Filter(func(a GCSBucketACL) bool { return a.Bucket == bucket })
		sort.Slice(items, func(i, j int) bool { return items[i].Entity < items[j].Entity })
		if items == nil {
			items = []GCSBucketACL{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#bucketAccessControls", "items": items})
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/acl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		acl, ok := gcsBucketACLs.Get(key(bucket, entity))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	insert := func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSBucketACL
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Entity == "" || in.Role == "" {
			sim.GCPError(w, http.StatusBadRequest, "entity and role are required", "INVALID_ARGUMENT")
			return
		}
		acl := build(r, bucket, in.Entity, in.Role)
		gcsBucketACLs.Put(key(bucket, in.Entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	}
	srv.HandleFunc("POST /storage/v1/b/{bucket}/acl", insert)

	update := func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		if _, ok := gcsBucketACLs.Get(key(bucket, entity)); !ok {
			if !bucketExists(w, bucket) {
				return
			}
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		var in GCSBucketACL
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		acl := build(r, bucket, entity, in.Role)
		gcsBucketACLs.Put(key(bucket, entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	}
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/acl/{entity}", update)
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/acl/{entity}", update)

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/acl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		if !gcsBucketACLs.Delete(key(bucket, entity)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func registerGCSDefaultObjectACLs(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, entity string) string { return bucket + "\x00" + entity }
	build := func(r *http.Request, bucket, entity, role string) GCSObjectACL {
		email, team := gcsACLEmailFor(entity)
		return GCSObjectACL{
			Kind:        "storage#objectAccessControl",
			ID:          bucket + "/" + entity,
			SelfLink:    gcpSelfLink(r, "/storage/v1/b/"+bucket+"/defaultObjectAcl/"+entity),
			Bucket:      bucket,
			Entity:      entity,
			Role:        role,
			Email:       email,
			ProjectTeam: team,
			Etag:        "CAE=",
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/defaultObjectAcl", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := gcsObjectDefACLs.Filter(func(a GCSObjectACL) bool { return a.Bucket == bucket })
		sort.Slice(items, func(i, j int) bool { return items[i].Entity < items[j].Entity })
		if items == nil {
			items = []GCSObjectACL{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#objectAccessControls", "items": items})
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		acl, ok := gcsObjectDefACLs.Get(key(bucket, entity))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no default object ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/defaultObjectAcl", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSObjectACL
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Entity == "" || in.Role == "" {
			sim.GCPError(w, http.StatusBadRequest, "entity and role are required", "INVALID_ARGUMENT")
			return
		}
		acl := build(r, bucket, in.Entity, in.Role)
		gcsObjectDefACLs.Put(key(bucket, in.Entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	})

	update := func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		if _, ok := gcsObjectDefACLs.Get(key(bucket, entity)); !ok {
			if !bucketExists(w, bucket) {
				return
			}
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no default object ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		var in GCSObjectACL
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		acl := build(r, bucket, entity, in.Role)
		gcsObjectDefACLs.Put(key(bucket, entity), acl)
		sim.WriteJSON(w, http.StatusOK, acl)
	}
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", update)
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", update)

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/defaultObjectAcl/{entity}", func(w http.ResponseWriter, r *http.Request) {
		bucket, entity := sim.PathParam(r, "bucket"), sim.PathParam(r, "entity")
		if !gcsObjectDefACLs.Delete(key(bucket, entity)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no default object ACL entry for entity %q on bucket %q", entity, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func registerGCSFolders(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, name string) string { return bucket + "\x00" + name }
	build := func(r *http.Request, bucket, name string) GCSFolder {
		now := gcsTimestamp()
		return GCSFolder{
			Kind:           "storage#folder",
			ID:             bucket + "/" + name,
			SelfLink:       gcpSelfLink(r, "/storage/v1/b/"+bucket+"/folders/"+name),
			Bucket:         bucket,
			Name:           name,
			Metageneration: "1",
			CreateTime:     now,
			UpdateTime:     now,
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/folders", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		prefix := r.URL.Query().Get("prefix")
		items := gcsFolders.Filter(func(f GCSFolder) bool {
			return f.Bucket == bucket && (prefix == "" || strings.HasPrefix(f.Name, prefix))
		})
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		mapped := make([]map[string]any, 0, len(items))
		for _, f := range items {
			mapped = append(mapped, gcsStructToMap(f))
		}
		page, next, ok := paginateListGCS(w, r, mapped)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "storage#folders", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "folder")
		f, ok := gcsFolders.Get(key(bucket, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", name, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/folders", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSFolder
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		if _, exists := gcsFolders.Get(key(bucket, in.Name)); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "folder %q already exists", in.Name)
			return
		}
		f := build(r, bucket, in.Name)
		gcsFolders.Put(key(bucket, in.Name), f)
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "folder")
		if !gcsFolders.Delete(key(bucket, name)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", name, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// deleteRecursive and renameTo are long-running operations: GCS returns a
	// GoogleLongrunningOperation. The sim performs the mutation synchronously
	// and returns a done=true operation carrying the result.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/folders/{folder}/deleteRecursive", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "folder")
		removed := false
		for _, f := range gcsFolders.Filter(func(f GCSFolder) bool { return f.Bucket == bucket && strings.HasPrefix(f.Name, name) }) {
			if gcsFolders.Delete(key(bucket, f.Name)) {
				removed = true
			}
		}
		if !removed {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", name, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, nil))
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/folders/{sourceFolder}/renameTo/folders/{destinationFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		src, dst := sim.PathParam(r, "sourceFolder"), sim.PathParam(r, "destinationFolder")
		f, ok := gcsFolders.Get(key(bucket, src))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %q not found in bucket %q", src, bucket)
			return
		}
		gcsFolders.Delete(key(bucket, src))
		f.Name = dst
		f.ID = bucket + "/" + dst
		f.SelfLink = gcpSelfLink(r, "/storage/v1/b/"+bucket+"/folders/"+dst)
		f.UpdateTime = gcsTimestamp()
		gcsFolders.Put(key(bucket, dst), f)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(f)))
	})
}

func registerGCSManagedFolders(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, name string) string { return bucket + "\x00" + name }
	build := func(r *http.Request, bucket, name string) GCSManagedFolder {
		now := gcsTimestamp()
		return GCSManagedFolder{
			Kind:           "storage#managedFolder",
			ID:             bucket + "/" + name,
			SelfLink:       gcpSelfLink(r, "/storage/v1/b/"+bucket+"/managedFolders/"+name),
			Bucket:         bucket,
			Name:           name,
			Metageneration: "1",
			CreateTime:     now,
			UpdateTime:     now,
		}
	}

	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		prefix := r.URL.Query().Get("prefix")
		items := gcsManagedFolders.Filter(func(f GCSManagedFolder) bool {
			return f.Bucket == bucket && (prefix == "" || strings.HasPrefix(f.Name, prefix))
		})
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		mapped := make([]map[string]any, 0, len(items))
		for _, f := range items {
			mapped = append(mapped, gcsStructToMap(f))
		}
		page, next, ok := paginateListGCS(w, r, mapped)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "storage#managedFolders", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		f, ok := gcsManagedFolders.Get(key(bucket, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed folder %q not found in bucket %q", name, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/managedFolders", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSManagedFolder
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		if _, exists := gcsManagedFolders.Get(key(bucket, in.Name)); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "managed folder %q already exists", in.Name)
			return
		}
		f := build(r, bucket, in.Name)
		gcsManagedFolders.Put(key(bucket, in.Name), f)
		sim.WriteJSON(w, http.StatusOK, f)
	})

	// managedFolders.update (PATCH). Every other member of the resource is
	// output-only or fixed at creation (name, bucket, the timestamps), so
	// rapidCacheConfig is the metadata a patch can change: a present member
	// replaces the stored one, an explicit null clears it, an absent member
	// leaves it alone — JSON-API PATCH semantics. A metadata write bumps the
	// folder's metageneration, as any GCS metadata write does.
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/managedFolders/{managedFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		f, ok := gcsManagedFolders.Get(key(bucket, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed folder %q not found in bucket %q", name, bucket)
			return
		}
		var in struct {
			// json.RawMessage distinguishes the three PATCH states: nil for
			// absent, the literal "null" for an explicit clear, JSON otherwise.
			RapidCacheConfig json.RawMessage `json:"rapidCacheConfig"`
		}
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		switch {
		case in.RapidCacheConfig == nil:
			// absent: unchanged
		case string(in.RapidCacheConfig) == "null":
			f.RapidCacheConfig = nil
		default:
			f.RapidCacheConfig = in.RapidCacheConfig
		}
		if metageneration, err := strconv.ParseInt(f.Metageneration, 10, 64); err == nil {
			f.Metageneration = strconv.FormatInt(metageneration+1, 10)
		}
		f.UpdateTime = gcsTimestamp()
		gcsManagedFolders.Put(key(bucket, name), f)
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/managedFolders/{managedFolder}", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		if !gcsManagedFolders.Delete(key(bucket, name)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed folder %q not found in bucket %q", name, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Managed-folder IAM — getIamPolicy / setIamPolicy / testIamPermissions.
	// Backed by the same shared policy store as bucket/object IAM.
	resource := func(bucket, name string) string {
		return "projects/_/buckets/" + bucket + "/managedFolders/" + name
	}
	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		policy, ok := gcpResourcePolicies.Get("managedFolder/" + bucket + "/" + name)
		if !ok {
			policy = IAMPolicy{Bindings: []IAMBinding{}, Etag: gcpPolicyETag(), Version: 1}
			gcpResourcePolicies.Put("managedFolder/"+bucket+"/"+name, policy)
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = resource(bucket, name)
		sim.WriteJSON(w, http.StatusOK, policy)
	})
	srv.HandleFunc("PUT /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam", func(w http.ResponseWriter, r *http.Request) {
		bucket, name := sim.PathParam(r, "bucket"), sim.PathParam(r, "managedFolder")
		var policy IAMPolicy
		if err := sim.ReadJSON(r, &policy); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		policy.Etag = gcpPolicyETag()
		if policy.Version == 0 {
			policy.Version = 1
		}
		policy.Kind = "storage#policy"
		policy.ResourceId = resource(bucket, name)
		gcpResourcePolicies.Put("managedFolder/"+bucket+"/"+name, policy)
		sim.WriteJSON(w, http.StatusOK, policy)
	})
	srv.HandleFunc("GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam/testPermissions", func(w http.ResponseWriter, r *http.Request) {
		gcsWriteTestPermissions(w, r)
	})
}

func registerGCSNotifications(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	key := func(bucket, id string) string { return bucket + "\x00" + id }

	srv.HandleFunc("GET /storage/v1/b/{bucket}/notificationConfigs", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := gcsNotifications.Filter(func(n GCSNotification) bool { return n.bucket == bucket })
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		if items == nil {
			items = []GCSNotification{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#notifications", "items": items})
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/notificationConfigs/{notification}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "notification")
		n, ok := gcsNotifications.Get(key(bucket, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "notification %q not found in bucket %q", id, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, n)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/notificationConfigs", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSNotification
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Topic == "" {
			sim.GCPError(w, http.StatusBadRequest, "topic is required", "INVALID_ARGUMENT")
			return
		}
		id := strconv.FormatInt(int64(gcsNotifications.Len()+1), 10)
		in.Kind = "storage#notification"
		in.ID = id
		in.bucket = bucket
		if in.PayloadFormat == "" {
			in.PayloadFormat = "JSON_API_V1"
		}
		in.SelfLink = gcpSelfLink(r, "/storage/v1/b/"+bucket+"/notificationConfigs/"+id)
		gcsNotifications.Put(key(bucket, id), in)
		sim.WriteJSON(w, http.StatusOK, in)
	})

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/notificationConfigs/{notification}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "notification")
		if !gcsNotifications.Delete(key(bucket, id)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "notification %q not found in bucket %q", id, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func registerGCSHmacKeys(srv *sim.Server) {
	srv.HandleFunc("GET /storage/v1/projects/{projectId}/serviceAccount", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "projectId")
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":          "storage#serviceAccount",
			"email_address": "service-" + project + "@gs-project-accounts.iam.gserviceaccount.com",
		})
	})

	srv.HandleFunc("GET /storage/v1/projects/{projectId}/hmacKeys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "projectId")
		saFilter := r.URL.Query().Get("serviceAccountEmail")
		showDeleted := r.URL.Query().Get("showDeletedKeys") == "true"
		items := gcsHmacKeys.Filter(func(k GCSHmacKey) bool {
			if k.ProjectID != project {
				return false
			}
			if saFilter != "" && k.ServiceAccountEmail != saFilter {
				return false
			}
			if k.State == "DELETED" && !showDeleted {
				return false
			}
			return true
		})
		sort.Slice(items, func(i, j int) bool { return items[i].AccessID < items[j].AccessID })
		mapped := make([]map[string]any, 0, len(items))
		for _, k := range items {
			mapped = append(mapped, gcsStructToMap(k))
		}
		page, next, ok := paginateListGCS(w, r, mapped)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "storage#hmacKeysMetadata", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /storage/v1/projects/{projectId}/hmacKeys/{accessId}", func(w http.ResponseWriter, r *http.Request) {
		project, accessID := sim.PathParam(r, "projectId"), sim.PathParam(r, "accessId")
		k, ok := gcsHmacKeys.Get(project + "\x00" + accessID)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "HMAC key %q not found", accessID)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsStructToMap(k))
	})

	srv.HandleFunc("POST /storage/v1/projects/{projectId}/hmacKeys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "projectId")
		sa := r.URL.Query().Get("serviceAccountEmail")
		if sa == "" {
			sim.GCPError(w, http.StatusBadRequest, "serviceAccountEmail is required", "INVALID_ARGUMENT")
			return
		}
		accessID := "GOOG" + strings.ToUpper(gcsRandHex(12))
		now := gcsTimestamp()
		k := GCSHmacKey{
			Kind:                "storage#hmacKeyMetadata",
			ID:                  project + "/" + accessID,
			AccessID:            accessID,
			ProjectID:           project,
			ServiceAccountEmail: sa,
			State:               "ACTIVE",
			TimeCreated:         now,
			Updated:             now,
			SelfLink:            gcpSelfLink(r, "/storage/v1/projects/"+project+"/hmacKeys/"+accessID),
			Etag:                "CAE=",
			secret:              base64.StdEncoding.EncodeToString([]byte(gcsRandHex(20))),
		}
		gcsHmacKeys.Put(project+"\x00"+accessID, k)
		// Create returns storage#hmacKey: { kind, metadata, secret }.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":     "storage#hmacKey",
			"metadata": gcsStructToMap(k),
			"secret":   k.secret,
		})
	})

	srv.HandleFunc("PUT /storage/v1/projects/{projectId}/hmacKeys/{accessId}", func(w http.ResponseWriter, r *http.Request) {
		project, accessID := sim.PathParam(r, "projectId"), sim.PathParam(r, "accessId")
		k, ok := gcsHmacKeys.Get(project + "\x00" + accessID)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "HMAC key %q not found", accessID)
			return
		}
		var in GCSHmacKey
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.State != "" {
			k.State = in.State
		}
		k.Updated = gcsTimestamp()
		gcsHmacKeys.Put(project+"\x00"+accessID, k)
		sim.WriteJSON(w, http.StatusOK, gcsStructToMap(k))
	})

	srv.HandleFunc("DELETE /storage/v1/projects/{projectId}/hmacKeys/{accessId}", func(w http.ResponseWriter, r *http.Request) {
		project, accessID := sim.PathParam(r, "projectId"), sim.PathParam(r, "accessId")
		k, ok := gcsHmacKeys.Get(project + "\x00" + accessID)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "HMAC key %q not found", accessID)
			return
		}
		// Real GCS only allows deleting an INACTIVE key; it then transitions
		// to DELETED rather than vanishing.
		k.State = "DELETED"
		k.Updated = gcsTimestamp()
		gcsHmacKeys.Put(project+"\x00"+accessID, k)
		w.WriteHeader(http.StatusNoContent)
	})
}

func registerGCSAnywhereCaches(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	caches := sim.MakeStore[GCSAnywhereCache](srv.DB(), "gcs_anywhere_caches")
	key := func(bucket, id string) string { return bucket + "\x00" + id }

	srv.HandleFunc("GET /storage/v1/b/{bucket}/anywhereCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := caches.Filter(func(c GCSAnywhereCache) bool { return c.Bucket == bucket })
		sort.Slice(items, func(i, j int) bool { return items[i].AnywhereCacheID < items[j].AnywhereCacheID })
		mapped := make([]map[string]any, 0, len(items))
		for _, c := range items {
			mapped = append(mapped, gcsStructToMap(c))
		}
		page, next, ok := paginateListGCS(w, r, mapped)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "storage#anywhereCaches", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "anywhereCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "anywhere cache %q not found in bucket %q", id, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})

	// insert / update are long-running operations returning a
	// GoogleLongrunningOperation whose response carries the cache.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSAnywhereCache
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		id := "anywhere-cache-" + gcsRandHex(6)
		now := gcsTimestamp()
		c := GCSAnywhereCache{
			Kind:            "storage#anywhereCache",
			ID:              bucket + "/" + id,
			SelfLink:        gcpSelfLink(r, "/storage/v1/b/"+bucket+"/anywhereCaches/"+id),
			Bucket:          bucket,
			AnywhereCacheID: id,
			Zone:            in.Zone,
			State:           "running",
			CreateTime:      now,
			UpdateTime:      now,
			Ttl:             in.Ttl,
			AdmissionPolicy: in.AdmissionPolicy,
		}
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "anywhereCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "anywhere cache %q not found in bucket %q", id, bucket)
			return
		}
		var in GCSAnywhereCache
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Ttl != "" {
			c.Ttl = in.Ttl
		}
		if in.AdmissionPolicy != "" {
			c.AdmissionPolicy = in.AdmissionPolicy
		}
		c.UpdateTime = gcsTimestamp()
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	stateVerb := func(state string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "anywhereCacheId")
			c, ok := caches.Get(key(bucket, id))
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "anywhere cache %q not found in bucket %q", id, bucket)
				return
			}
			c.State = state
			c.UpdateTime = gcsTimestamp()
			caches.Put(key(bucket, id), c)
			sim.WriteJSON(w, http.StatusOK, c)
		}
	}
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/pause", stateVerb("paused"))
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/resume", stateVerb("running"))
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/disable", stateVerb("disabled"))
}

// GCSAnywhereCache mirrors the storage#anywhereCache resource.
type GCSAnywhereCache struct {
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	SelfLink        string `json:"selfLink,omitempty"`
	Bucket          string `json:"bucket"`
	AnywhereCacheID string `json:"anywhereCacheId"`
	Zone            string `json:"zone,omitempty"`
	State           string `json:"state,omitempty"`
	CreateTime      string `json:"createTime,omitempty"`
	UpdateTime      string `json:"updateTime,omitempty"`
	Ttl             string `json:"ttl,omitempty"`
	AdmissionPolicy string `json:"admissionPolicy,omitempty"`
	PendingUpdate   bool   `json:"pendingUpdate,omitempty"`
	IngestOnWrite   bool   `json:"ingestOnWrite,omitempty"`
}

// registerGCSRapidCaches mounts the rapidCaches collection: a rapid cache is
// bucket-scoped control-plane state (zone, TTL, admission policy, cache
// type), managed here exactly as the schema describes it and carrying no
// data-plane behavior claim beyond its states. Nothing in this deployment
// provisions real cache hardware, so a cache settles synchronously — created
// running, and the long-running operations insert/update/disable return are
// already done, the same convention the anywhere-caches collection follows.
func registerGCSRapidCaches(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	caches := sim.MakeStore[GCSRapidCache](srv.DB(), "gcs_rapid_caches")
	key := func(bucket, id string) string { return bucket + "\x00" + id }

	srv.HandleFunc("GET /storage/v1/b/{bucket}/rapidCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := caches.Filter(func(c GCSRapidCache) bool { return c.Bucket == bucket })
		sort.Slice(items, func(i, j int) bool { return items[i].RapidCacheID < items[j].RapidCacheID })
		mapped := make([]map[string]any, 0, len(items))
		for _, c := range items {
			mapped = append(mapped, gcsStructToMap(c))
		}
		page, next, ok := paginateListGCS(w, r, mapped)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "storage#rapidCaches", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "rapidCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rapid cache %q not found in bucket %q", id, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/rapidCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSRapidCache
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		id := in.RapidCacheID
		if id == "" {
			id = "rapid-cache-" + gcsRandHex(6)
		}
		if _, exists := caches.Get(key(bucket, id)); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "rapid cache %q already exists in bucket %q", id, bucket)
			return
		}
		now := gcsTimestamp()
		c := GCSRapidCache{
			Kind:            "storage#rapidCache",
			ID:              bucket + "/" + id,
			SelfLink:        gcpSelfLink(r, "/storage/v1/b/"+bucket+"/rapidCaches/"+id),
			Bucket:          bucket,
			RapidCacheID:    id,
			Zone:            in.Zone,
			State:           "running",
			CreateTime:      now,
			UpdateTime:      now,
			Ttl:             in.Ttl,
			AdmissionPolicy: in.AdmissionPolicy,
			IngestOnWrite:   in.IngestOnWrite,
			CacheType:       in.CacheType,
		}
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	// rapidCaches.update (PATCH) — the mutable members are the TTL, the
	// admission policy and the ingest-on-write flag; zone and cacheType are
	// fixed at creation. The ingest flag rides a pointer so an absent member
	// is left alone while an explicit false lands.
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "rapidCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rapid cache %q not found in bucket %q", id, bucket)
			return
		}
		var in struct {
			Ttl             string `json:"ttl"`
			AdmissionPolicy string `json:"admissionPolicy"`
			IngestOnWrite   *bool  `json:"ingestOnWrite"`
		}
		if err := sim.ReadJSON(r, &in); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Ttl != "" {
			c.Ttl = in.Ttl
		}
		if in.AdmissionPolicy != "" {
			c.AdmissionPolicy = in.AdmissionPolicy
		}
		if in.IngestOnWrite != nil {
			c.IngestOnWrite = *in.IngestOnWrite
		}
		c.UpdateTime = gcsTimestamp()
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}/disable", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "rapidCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rapid cache %q not found in bucket %q", id, bucket)
			return
		}
		c.State = "disabled"
		c.UpdateTime = gcsTimestamp()
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})
}

// GCSRapidCache mirrors the storage#rapidCache resource.
type GCSRapidCache struct {
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	SelfLink        string `json:"selfLink,omitempty"`
	Bucket          string `json:"bucket"`
	RapidCacheID    string `json:"rapidCacheId"`
	Zone            string `json:"zone,omitempty"`
	State           string `json:"state,omitempty"`
	CreateTime      string `json:"createTime,omitempty"`
	UpdateTime      string `json:"updateTime,omitempty"`
	Ttl             string `json:"ttl,omitempty"`
	AdmissionPolicy string `json:"admissionPolicy,omitempty"`
	PendingUpdate   bool   `json:"pendingUpdate,omitempty"`
	IngestOnWrite   bool   `json:"ingestOnWrite,omitempty"`
	CacheType       string `json:"cacheType,omitempty"`
}

// --- Bucket lifecycle verbs, operations, IAM testPermissions, channels,
//     and the metadata-only object insert. ---

func registerGCSBucketLifecycle(srv *sim.Server, buckets sim.Store[Bucket], objects sim.Store[GCSObject], bucketExists func(http.ResponseWriter, string) bool) {
	// bucket IAM testPermissions (getIamPolicy/setIamPolicy already live in iam.go)
	srv.HandleFunc("GET /storage/v1/b/{bucket}/iam/testPermissions", func(w http.ResponseWriter, r *http.Request) {
		gcsWriteTestPermissions(w, r)
	})

	// lockRetentionPolicy / restore both return the Bucket resource.
	returnBucket := func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		b, ok := buckets.Get(bucket)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, b.Data)
	}
	srv.HandleFunc("POST /storage/v1/b/{bucket}/lockRetentionPolicy", returnBucket)
	srv.HandleFunc("POST /storage/v1/b/{bucket}/restore", returnBucket)

	// buckets.relocate moves the bucket to the requested location and answers
	// with the operation that did it. validateOnly checks the request and
	// leaves the bucket where it is, which is what the member says it does.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/relocate", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "bucket")
		bucket, ok := buckets.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", name)
			return
		}
		var request GCSRelocateBucketRequest
		if err := sim.ReadJSON(r, &request); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		destination := request.DestinationLocation
		if destination == "" {
			// "If no location is provided, Cloud Storage will use the default
			// location, which is us" — the same default the create path
			// applies to a bucket that names none.
			destination = "US"
		}
		if !request.ValidateOnly {
			// GCS normalizes a bucket's location to upper case, the same way
			// the create path does.
			bucket.Data["location"] = strings.ToUpper(destination)
			if request.DestinationCustomPlacementConfig != nil {
				bucket.Data["customPlacementConfig"] = map[string]any{
					"dataLocations": request.DestinationCustomPlacementConfig.DataLocations,
				}
			}
			if request.DestinationKmsKeyName != "" {
				bucket.Data["encryption"] = map[string]any{
					"defaultKmsKeyName": request.DestinationKmsKeyName,
				}
			}
			bucket.Data["updated"] = gcsTimestamp()
			buckets.Put(name, bucket)
		}
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, name, nil))
	})

	// Bucket operations: list / get / cancel / advanceRelocateBucket. Every
	// answer here is about a record the bucket's long-running methods wrote —
	// an identifier no method minted is NOT_FOUND, as it is in real Cloud
	// Storage.
	srv.HandleFunc("GET /storage/v1/b/{bucket}/operations", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		done, filtered, ok := gcsOperationFilter(w, r)
		if !ok {
			return
		}
		prefix := gcsOperationName(bucket, "")
		items := crOperations.Filter(func(op Operation) bool {
			if !strings.HasPrefix(op.Name, prefix) {
				return false
			}
			return !filtered || op.Done == done
		})
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		page, next, ok := paginateList(w, r, items)
		if !ok {
			return
		}
		response := map[string]any{"kind": "storage#operations", "operations": page}
		if next != "" {
			response["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, response)
	})
	srv.HandleFunc("GET /storage/v1/b/{bucket}/operations/{operationId}", func(w http.ResponseWriter, r *http.Request) {
		op, ok := gcsLookupOperation(w, r, bucketExists)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, op)
	})
	// The method's own description is that cancellation is best effort and
	// that a client polls the operation to learn whether it succeeded or the
	// operation completed anyway. Every operation this simulator records is
	// already complete when its name reaches a client, so the completed
	// outcome is the honest one: the record stands and the empty body the
	// document declares comes back.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/operations/{operationId}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := gcsLookupOperation(w, r, bucketExists); !ok {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// Advancing a relocation is best effort for the same reason: the write
	// downtime it schedules belongs to a relocation still in flight, and this
	// simulator's relocations finish inside the request that starts them.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/operations/{operationId}/advanceRelocateBucket", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := gcsLookupOperation(w, r, bucketExists); !ok {
			return
		}
		var request GCSAdvanceRelocateBucketOperationRequest
		if err := sim.ReadJSON(r, &request); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// channels.stop — stops a watch channel; returns an empty 200.
	srv.HandleFunc("POST /storage/v1/channels/stop", func(w http.ResponseWriter, r *http.Request) {
		_ = sim.ReadJSON(r, &map[string]any{})
		w.WriteHeader(http.StatusOK)
	})

	// Metadata-only object insert (objects.insert with no media upload).
	// Creates a zero-length object from the supplied resource fields.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/o", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		name := r.URL.Query().Get("name")
		var res GCSObject
		if r.ContentLength != 0 {
			if err := sim.ReadJSON(r, &res); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
		}
		if name == "" {
			name = res.Name
		}
		if name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		attrs := GCSObject{
			Name:        name,
			ContentType: res.ContentType,
			Metadata:    res.Metadata,
		}
		obj, err := persistGCSObject(objects, bucket, name, nil, attrs)
		if err != nil {
			writeGCSPersistError(w, "insert object", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
	})
}

// gcsWriteTestPermissions echoes the requested permissions back as granted —
// the sim's single-tenant model treats the caller as the bucket owner.
func gcsWriteTestPermissions(w http.ResponseWriter, r *http.Request) {
	perms := r.URL.Query()["permissions"]
	if perms == nil {
		perms = []string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":        "storage#testIamPermissionsResponse",
		"permissions": perms,
	})
}

// GCSRelocateBucketRequest mirrors the Discovery RelocateBucketRequest schema.
type GCSRelocateBucketRequest struct {
	DestinationLocation              string                         `json:"destinationLocation,omitempty"`
	DestinationCustomPlacementConfig *GCSCustomPlacementDestination `json:"destinationCustomPlacementConfig,omitempty"`
	ValidateOnly                     bool                           `json:"validateOnly,omitempty"`
	DestinationKmsKeyName            string                         `json:"destinationKmsKeyName,omitempty"`
}

// GCSCustomPlacementDestination mirrors the RelocateBucketRequest's
// destinationCustomPlacementConfig member — the regions a custom dual-region
// bucket places its data in.
type GCSCustomPlacementDestination struct {
	DataLocations []string `json:"dataLocations,omitempty"`
}

// GCSAdvanceRelocateBucketOperationRequest mirrors the Discovery
// AdvanceRelocateBucketOperationRequest schema.
type GCSAdvanceRelocateBucketOperationRequest struct {
	Ttl        string `json:"ttl,omitempty"`
	ExpireTime string `json:"expireTime,omitempty"`
}

// gcsOperationName is the resource name of a Cloud Storage long-running
// operation. Operations are parented by the bucket, under the `projects/_`
// wildcard project the service uses for them, and the name is what
// `gcloud storage operations describe` is given.
func gcsOperationName(bucket, id string) string {
	return "projects/_/buckets/" + bucket + "/operations/" + id
}

// gcsRecordDoneOperation records the operation for work a request just
// performed and returns it. Cloud Storage answers its long-running methods with
// an operation the bucket's operations collection can then be asked about, so
// the record has to outlive the request that minted it — it goes into the same
// store every other Google slice writes its operations to.
func gcsRecordDoneOperation(r *http.Request, bucket string, response map[string]any) Operation {
	// One operation, so one id: the name and the selfLink must address the
	// same record.
	id := gcsRandHex(8)
	op := Operation{
		Kind:     "storage#operation",
		Name:     gcsOperationName(bucket, id),
		Done:     true,
		SelfLink: gcpSelfLink(r, "/storage/v1/b/"+bucket+"/operations/"+id),
	}
	if response != nil {
		op.Response = response
	}
	crOperations.Put(op.Name, op)
	return op
}

// gcsLookupOperation resolves the {bucket}/{operationId} pair the operations
// methods address into the recorded operation, answering NOT_FOUND for a bucket
// or an operation the service holds no record of.
func gcsLookupOperation(w http.ResponseWriter, r *http.Request, bucketExists func(http.ResponseWriter, string) bool) (Operation, bool) {
	bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "operationId")
	if !bucketExists(w, bucket) {
		return Operation{}, false
	}
	name := gcsOperationName(bucket, id)
	op, ok := crOperations.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
		return Operation{}, false
	}
	return op, true
}

// gcsOperationFilter reads the operations.list `filter` parameter. The
// filtering language is AIP-160, and the term the collection is asked for —
// the one `gcloud storage operations list --server-filter` documents — is
// `done = true` / `done = false`. A filter naming anything else is refused
// rather than ignored: a silently dropped filter answers with every operation
// in the bucket and reads as a result.
func gcsOperationFilter(w http.ResponseWriter, r *http.Request) (done, filtered, ok bool) {
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if filter == "" {
		return false, false, true
	}
	field, value, found := strings.Cut(filter, "=")
	if found && strings.TrimSpace(field) == "done" {
		switch strings.TrimSpace(value) {
		case "true":
			return true, true, true
		case "false":
			return false, true, true
		}
	}
	sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
		"unsupported filter %q: the operations collection filters on `done = true` or `done = false`", filter)
	return false, false, false
}

// gcsStructToMap round-trips a resource struct through JSON so list/response
// bodies carry exactly the schema-defined members (omitempty honored,
// unexported fields dropped) the spec-validator accepts.
func gcsStructToMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// gcsRandHex returns n random hex characters for synthesizing resource IDs.
func gcsRandHex(n int) string {
	buf := make([]byte, (n+1)/2)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)[:n]
}
