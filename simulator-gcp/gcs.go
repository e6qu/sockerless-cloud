package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// GCSBucketHostDir returns the on-disk directory backing a simulated GCS
// bucket, which inserting the bucket makes. Exported for use by the Cloud Run
// Jobs/Services + Cloud Functions task runners when they honour
// `Volume{Gcs{Bucket}}`.
func GCSBucketHostDir(bucket string) string {
	return filepath.Join(gcsDataRoot(), bucket)
}

// gcsResetBucketHostDir empties the bucket's host directory, keeping the
// directory itself so a mount of it stays the bucket's.
func gcsResetBucketHostDir(bucket string) error {
	dir := GCSBucketHostDir(bucket)
	gcsMountMu.Lock()
	defer gcsMountMu.Unlock()
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		if err := os.Remove(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := os.RemoveAll(filepath.Join(dir, child.Name())); err != nil {
			return err
		}
	}
	gcsMountResetLocked(bucket)
	return nil
}

// gcsDataRoot is the directory holding every bucket's host directory.
func gcsDataRoot() string {
	return sim.ScopedDataDir("SIM_GCS_DATA_DIR", "gcs", "sockerless-sim-gcs")
}

// gcsListLimit is the most entries one page of objects.list holds, and the
// size of a page whose request names no maxResults.
const gcsListLimit = 1000

// Bucket stores the full JSON object from the API so that terraform read-backs
// return every field the provider expects (id, selfLink, iamConfiguration, etc.).
type Bucket struct {
	Data map[string]any
	// Project is the project the bucket was created in — the `project`
	// parameter of the insert, which the bucket resource does not carry.
	Project string
}

// gcsDefaultBucketPolicy is the IAM policy a bucket carries from creation, as
// Cloud Storage grants it: the project's owners and editors own the bucket
// and its objects through the legacy bucket and object roles, and its viewers
// read them. It is what remains when a client removes the bindings it added,
// so a policy is never set empty.
func gcsDefaultBucketPolicy(project string) IAMPolicy {
	owners := []string{"projectEditor:" + project, "projectOwner:" + project}
	viewers := []string{"projectViewer:" + project}
	return IAMPolicy{
		Bindings: []IAMBinding{
			{Role: "roles/storage.legacyBucketOwner", Members: owners},
			{Role: "roles/storage.legacyBucketReader", Members: viewers},
			{Role: "roles/storage.legacyObjectOwner", Members: owners},
			{Role: "roles/storage.legacyObjectReader", Members: viewers},
		},
		Etag:    gcpPolicyETag(),
		Version: 1,
	}
}

// gcsSeedDefaultBucketPolicy stores and returns the bucket's default policy;
// storing it keeps its etag stable across reads, which the optimistic
// concurrency check on setIamPolicy compares against.
func gcsSeedDefaultBucketPolicy(name string) IAMPolicy {
	project := ""
	if bucket, ok := gcsBuckets.Get(name); ok {
		project = bucket.Project
	}
	policy := gcsDefaultBucketPolicy(project)
	gcpResourcePolicies.Put("bucket/"+name, policy)
	return policy
}

// GCSObject represents a Cloud Storage object (metadata).
type GCSObject struct {
	Name               string                      `json:"name"`
	Bucket             string                      `json:"bucket"`
	Size               string                      `json:"size"`
	ContentType        string                      `json:"contentType,omitempty"`
	ContentEncoding    string                      `json:"contentEncoding,omitempty"`
	ContentLanguage    string                      `json:"contentLanguage,omitempty"`
	ContentDisposition string                      `json:"contentDisposition,omitempty"`
	CacheControl       string                      `json:"cacheControl,omitempty"`
	StorageClass       string                      `json:"storageClass,omitempty"`
	CustomTime         string                      `json:"customTime,omitempty"`
	Metadata           map[string]string           `json:"metadata,omitempty"`
	CustomContexts     map[string]gcsCustomContext `json:"customContexts,omitempty"`
	TimeCreated        string                      `json:"timeCreated"`
	Updated            string                      `json:"updated"`
	Generation         string                      `json:"generation,omitempty"`
	Metageneration     string                      `json:"metageneration,omitempty"`
	Md5Hash            string                      `json:"md5Hash,omitempty"`
	Crc32c             string                      `json:"crc32c,omitempty"`
	ComponentCount     int64                       `json:"componentCount,omitempty"`
	Etag               string                      `json:"etag,omitempty"`
	// Body references the generation's contents in gcsBodies. The store
	// persists it; no API response carries it.
	Body           string `json:"-"`
	metadataCloned bool
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
	Acl      *[]GCSObjectACL  `json:"acl,omitempty"`
	Contexts gcsContextsInput `json:"contexts"`
}

// Package-level store: gcsObjects
// is exposed so other slices (e.g. cloudbuild.go) can read uploaded
// build context tarballs without depending on the gcs.go handler
// closure.
var (
	gcsObjects sim.PrefixStore[GCSObject]
	// gcsBuckets is package-level so a slice that exports into Cloud Storage
	// (Artifact Registry's exportArtifact) can refuse a bucket that does not
	// exist rather than inventing one.
	gcsBuckets sim.Store[Bucket]
)

// gcsBucketObjects returns the objects of one bucket whose names begin with
// prefix, in name order.
func gcsBucketObjects(bucket, prefix string) []GCSObject {
	rows := gcsObjects.ListPrefix(bucket + "/" + prefix)
	out := make([]GCSObject, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Item)
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
	case "ASIA1", "EUR4", "EUR5", "EUR7", "EUR8", "NAM4":
		return "dual-region"
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

// persistGCSObjectMetadata writes a metadata-only update (objects.patch /
// ObjectHandle.Update) to the store. Unlike persistGCSObject it does NOT
// touch the host backing file or bump generation — a metadata patch leaves
// the object payload (and its generation) unchanged, bumping only
// metageneration. Kept as a distinct, auditable write path so the
// data-write invariant (every payload write goes through persistGCSObject
// and reaches the host backing store) stays enforced by the AST guard test.
func persistGCSObjectMetadata(objects sim.PrefixStore[GCSObject], key string, obj GCSObject) {
	objects.Put(key, obj)
}

// persistGCSObject stores the contents body references, whose digests are
// given, as a new generation of the object. It evaluates the write's
// preconditions and stores the generation as one step, under the object's
// write lock, and gives it a generation no version of the object has had
// before. The generation it replaces is retired as a delete would retire it.
// place puts the new generation in the bucket's host directory: gcsMirror for
// a write through the API, and a record of the file already there for a write
// that came in through a volume mount. A write that fails releases body.
func persistGCSObject(objects sim.PrefixStore[GCSObject], bucketName, objectName string, body string, digests blobstore.Digests, attrs GCSObject, pre gcsPreconditions, place func(GCSObject)) (GCSObject, error) {
	fail := func(err error) (GCSObject, error) {
		return GCSObject{}, errors.Join(err, gcsBodies.Remove(body))
	}
	if attrs.ContentType == "" {
		attrs.ContentType = "application/octet-stream"
	}
	if err := validateGCSObjectAttrs(attrs); err != nil {
		return fail(err)
	}
	defer gcsObjectWriters.Lock(bucketName + "/" + objectName)()
	existing, existed := objects.Get(bucketName + "/" + objectName)
	if !pre.holds(existing, existed) {
		return fail(errGCSPreconditionFailed)
	}
	now := gcsTimestamp()
	generation := gcsNextGeneration()
	etag := gcsObjectETag(digests.MD5[:], generation, now)

	obj := attrs
	obj.Name = objectName
	obj.Bucket = bucketName
	obj.Size = strconv.FormatInt(digests.Size, 10)
	obj.TimeCreated = now
	obj.Updated = now
	obj.Generation = strconv.FormatInt(generation, 10)
	obj.Metageneration = "1"
	obj.Md5Hash = digests.MD5Base64()
	// A composite object has no MD5 of its whole: Cloud Storage reports only
	// the CRC32C it can combine from its components'.
	if attrs.ComponentCount > 0 {
		obj.Md5Hash = ""
	}
	obj.Crc32c = digests.CRC32CBase64()
	obj.Etag = etag
	obj.Body = body
	obj.CustomContexts = gcsStampContexts(attrs.CustomContexts, now)
	if !attrs.metadataCloned {
		obj.Metadata = cloneStringMap(attrs.Metadata)
	}
	obj.metadataCloned = true
	// An object written without a storage class takes the bucket's default.
	if obj.StorageClass == "" {
		if bucket, ok := gcsBuckets.Get(bucketName); ok {
			obj.StorageClass, _ = bucket.Data["storageClass"].(string)
		}
	}
	objects.Put(bucketName+"/"+objectName, obj)
	if existed {
		bucket, _ := gcsBuckets.Get(bucketName)
		gcsRetireGeneration(bucket, existing)
	} else {
		gcsSeedObjectACL(bucketName, objectName, obj.Generation)
	}
	place(obj)
	gcsNotifyWrite(obj, existing, existed)
	return obj, nil
}

// gcsRetireDeletedObject retires obj, which a delete has just taken out of the
// store. Under a soft-delete policy the object is retired rather than
// destroyed, and its payload is retained for objects.restore to bring back.
// Without one it is destroyed here, bytes included.
func gcsRetireDeletedObject(bucket Bucket, obj GCSObject) {
	gcsRetireObject(bucket, obj.Bucket, obj)
	gcsNotify(gcsEventDelete, obj, nil)
}

func writeGCSPersistError(w http.ResponseWriter, action string, err error) {
	if errors.Is(err, errGCSPreconditionFailed) {
		writeGCSPreconditionFailed(w)
		return
	}
	var invalid *invalidGCSObjectMetadataError
	if errors.As(err, &invalid) {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s: %v", action, err)
		return
	}
	GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%s: %v", action, err)
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
	// Body references the bytes received so far, staged in gcsBodies.
	Body string `json:"body,omitempty"`
	// LegacyData is where a build before Body buffered them.
	LegacyData []byte `json:"data,omitempty"`
	// Preconditions stated when the session began, which the write that
	// completes the upload must meet.
	Preconditions gcsPreconditions `json:"preconditions"`
	// Response is the finished object's metadata, which a request to the
	// session answers with once the last byte has arrived.
	Response json.RawMessage `json:"response,omitempty"`
}

// gcsStagedSize is how many bytes a session's staging payload holds.
func gcsStagedSize(ref string) (int64, error) {
	staged, err := gcsBodies.Open(ref)
	if err != nil {
		return 0, err
	}
	defer func() { _ = staged.Close() }()
	return staged.Seek(0, io.SeekEnd)
}

var gcsResumableSessions sim.Store[gcsResumableSession]

// gcsResumableWriters serializes the chunk PUTs of one session. Real clients
// upload a session's chunks sequentially; this guards the staged payload and
// the session record against a retried chunk racing the one it repeats.
var gcsResumableWriters = sim.NewKeyedLocks()

// handleGCSResumableCancel cancels a resumable upload: Cloud Storage answers
// the DELETE of a session URI with 499 and forgets the bytes it received.
func handleGCSResumableCancel(w http.ResponseWriter, uploadID string) {
	defer gcsResumableWriters.Lock(uploadID)()
	sess, ok := gcsResumableSessions.Get(uploadID)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"resumable upload session %q not found", uploadID)
		return
	}
	gcsResumableSessions.Delete(uploadID)
	gcsReleaseBody(sess.Body)
	w.WriteHeader(499)
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
		"id":             fmt.Sprintf("%s/%s/%s", obj.Bucket, obj.Name, defaultStr(obj.Generation, "1")),
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
	if len(obj.CustomContexts) > 0 {
		meta["contexts"] = gcsContextsResource(obj.CustomContexts)
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
	srv.WrapHandler(gcsMountSyncMiddleware)
	// Cloud Storage's long-running methods record into the operation store
	// every Google slice shares, whichever register function reaches it first.
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	registerGCSBuckets(srv, buckets)

	registerGCSObjects(srv, buckets, objects)

	registerGCSUploads(srv, buckets, objects)

	registerGCSExtras(srv, buckets, objects)
	registerGCSBatch(srv)
	gcsOpenBodies(srv)
	seedGCSGenerations()
}

func setGCSObjectResponseHeaders(h http.Header, obj GCSObject) {
	h.Set("Content-Type", obj.ContentType)
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

func handleGCSObjectCopyRequest(w http.ResponseWriter, r *http.Request, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) bool {
	srcBucket, srcObject, dstBucket, dstObject, op, ok := parseGCSCopyPath(r)
	if !ok {
		return false
	}
	if _, ok := buckets.Get(srcBucket); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", srcBucket)
		return true
	}
	if _, ok := buckets.Get(dstBucket); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", dstBucket)
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

func copyGCSObject(w http.ResponseWriter, r *http.Request, srcBucket, srcObject, dstBucket, dstObject string, objects sim.PrefixStore[GCSObject]) (GCSObject, bool) {
	pre, err := parseGCSPreconditions(r.URL.Query(), false)
	if err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return GCSObject{}, false
	}
	sourcePre, err := parseGCSPreconditions(r.URL.Query(), true)
	if err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return GCSObject{}, false
	}
	src, ok := objects.Get(srcBucket + "/" + srcObject)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"source object %q not found in bucket %q", srcObject, srcBucket)
		return GCSObject{}, false
	}
	if !sourcePre.holds(src, true) {
		writeGCSPreconditionFailed(w)
		return GCSObject{}, false
	}
	dstAttrs := src
	var meta gcsObjectResource
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&meta); err != nil && err != io.EOF {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"failed to parse copy metadata: %v", err)
			return GCSObject{}, false
		}
		dstAttrs = meta.applyTo(dstAttrs)
	}
	if dstAttrs.CustomContexts, err = gcsDestinationContexts(r, &meta, src); err != nil {
		GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
		return GCSObject{}, false
	}
	_, copied, digests, err := gcsCopyContents(src)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return GCSObject{}, false
	}
	dst, err := persistGCSObject(objects, dstBucket, dstObject, copied, digests, dstAttrs, pre, gcsMirror)
	if err != nil {
		writeGCSPersistError(w, "write copied object", err)
		return GCSObject{}, false
	}
	return dst, true
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
// bucket-level lifecycle verbs (relocate/restore/lockRetentionPolicy), the
// project service account, and the metadata-only object insert. Each is a faithful slice of the real JSON API.
func registerGCSExtras(srv *sim.Server, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) {
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", name)
			return false
		}
		return true
	}

	registerGCSBucketACLs(srv, buckets, bucketExists)
	registerGCSDefaultObjectACLs(srv, buckets, bucketExists)
	registerGCSObjectACLs(srv, buckets, objects)
	registerGCSObjectIAM(srv, buckets, objects)
	registerGCSObjectRestore(srv, buckets, objects)
	registerGCSObjectContexts(srv, buckets, objects)
	registerGCSFolders(srv, buckets, bucketExists)
	registerGCSManagedFolders(srv, buckets, bucketExists)
	registerGCSNotifications(srv, buckets, bucketExists)
	registerGCSHmacKeys(srv)
	registerGCSAnywhereCaches(srv, buckets, bucketExists)
	registerGCSRapidCaches(srv, buckets, bucketExists)
	registerGCSBucketLifecycle(srv, buckets, objects, bucketExists)
}

func registerGCSNotifications(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	srv.HandleFunc("GET /storage/v1/b/{bucket}/notificationConfigs", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#notifications", "items": gcsBucketNotifications(bucket)})
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/notificationConfigs/{notification}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "notification")
		n, ok := gcsNotifications.Get(gcsNotificationKey(bucket, id))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "notification %q not found in bucket %q", id, bucket)
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Topic == "" {
			GCPError(w, http.StatusBadRequest, "topic is required", "INVALID_ARGUMENT")
			return
		}
		topicName, valid := gcsNotificationTopicName(in.Topic)
		if !valid {
			writeGCSJSONError(w, http.StatusBadRequest, "invalid",
				"Invalid Google Cloud Pub/Sub topic. It should look like '//pubsub.googleapis.com/projects/*/topics/*'.")
			return
		}
		in.Topic = "//pubsub.googleapis.com/" + topicName
		owner, _ := buckets.Get(bucket)
		number, _ := owner.Data["projectNumber"].(string)
		if agent := gcsServiceAgentEmail(number); !gcsAgentMayPublish(agent, topicName) {
			writeGCSJSONError(w, http.StatusForbidden, "forbidden", fmt.Sprintf(
				"The service account '%s' does not have permission to publish messages to to the Cloud Pub/Sub topic '%s', or that topic does not exist.",
				agent, in.Topic))
			return
		}
		id := gcsNextNotificationID(bucket)
		in.Kind = "storage#notification"
		in.ID = id
		if in.PayloadFormat == "" {
			in.PayloadFormat = "JSON_API_V1"
		}
		in.SelfLink = gcpSelfLink(r, "/storage/v1/b/"+bucket+"/notificationConfigs/"+id)
		gcsNotifications.Put(gcsNotificationKey(bucket, id), in)
		sim.WriteJSON(w, http.StatusOK, in)
	})

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/notificationConfigs/{notification}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "notification")
		if !gcsNotifications.Delete(gcsNotificationKey(bucket, id)) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "notification %q not found in bucket %q", id, bucket)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
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

// gcsWriteTestPermissions answers a testIamPermissions with the requested
// permissions the caller holds under the policies that govern the resource.
func gcsWriteTestPermissions(w http.ResponseWriter, r *http.Request, policies []IAMPolicy, resource gcpIAMResource) {
	principal, owner := gcpRequestPrincipal(r)
	held := gcpPermissionsHeldUnder(principal, owner, policies, r.URL.Query()["permissions"], resource)
	resp := map[string]any{"kind": "storage#testIamPermissionsResponse"}
	if len(held) > 0 {
		resp["permissions"] = held
	}
	sim.WriteJSON(w, http.StatusOK, resp)
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
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
	GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
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

// gcsPageTag marks a listing page token as one this simulator issued, so a
// token that merely decodes as base64 is still refused.
const gcsPageTag = "after\x00"

// gcsPageToken resumes a listing strictly after the key it names, the way
// Cloud Storage's tokens resume: a resource created or deleted between two
// page reads moves no other resource onto a second page or off every page.
func gcsPageToken(after string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(gcsPageTag + after))
}

func gcsPageMarker(token string) (string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	return strings.CutPrefix(string(decoded), gcsPageTag)
}

// gcsPageAfter pages items in ascending order of key by the JSON API's
// maxResults and pageToken. Without maxResults it returns everything after the
// token.
func gcsPageAfter[T any](w http.ResponseWriter, r *http.Request, items []T, key func(T) string) ([]T, string, bool) {
	size, ok := gcpPageSizeParam(w, r, "maxResults")
	if !ok {
		return nil, "", false
	}
	return gcpKeyPage(w, items, []gcpSortKey[T]{gcpAsc(key)}, r.URL.Query().Get("pageToken"), size, 0, 0)
}

func gcsBucketName(b Bucket) string {
	name, _ := b.Data["name"].(string)
	return name
}

// gcsWriteMapList answers a list method of a bucket's child collection: items
// paged by key, each in its JSON form, as a list of kind.
func gcsWriteMapList[T any](w http.ResponseWriter, r *http.Request, items []T, kind string, key func(T) string) {
	listed, next, ok := gcsPageAfter(w, r, items, key)
	if !ok {
		return
	}
	page := make([]map[string]any, 0, len(listed))
	for _, item := range listed {
		page = append(page, gcsStructToMap(item))
	}
	resp := map[string]any{"kind": kind, "items": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}
