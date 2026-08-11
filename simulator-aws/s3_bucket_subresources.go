package main

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// S3BucketConfig stores arbitrary per-bucket subresource configuration
// payloads. Each (bucket, subresource) pair holds the raw bytes the
// client PUT so a subsequent GET round-trips byte-for-byte (real S3
// re-serialises but emits the same logical document; matching the
// stored bytes is a stricter equivalent that catches re-encoding
// drift).
type S3BucketConfig struct {
	Key         string
	Bucket      string
	Subresource string
	Body        []byte
	ContentType string
	Headers     map[string]string
}

// keyed by `<bucket>/<subresource>`.
var s3BucketConfigs sim.Store[S3BucketConfig]

const s3LifecycleTransitionDefaultMinimumObjectSize = "all_storage_classes_128K"

func s3BucketConfigKeyID(bucket, sub, id string) string {
	if id == "" {
		return bucket + "/" + sub
	}
	return bucket + "/" + sub + "/" + id
}

// bucketSubresource enumerates a single bucket-level subresource —
// each carries the canonical S3 success status code for its PUT and
// DELETE shapes (real S3 differs between `200 OK` and `204 No Content`
// per operation; preserving that fidelity keeps SDKs and the
// `aws s3api` CLI happy).
type bucketSubresource struct {
	// putStatus is the success status emitted on PUT (200 or 204).
	// 0 → subresource has no PUT shape.
	putStatus int
	// hasDelete indicates that real S3 accepts a DELETE for this
	// subresource (only some of them do — e.g. `?policy` and
	// `?lifecycle` yes; `?versioning` no — it's set/suspended via
	// PUT, never deleted).
	hasDelete bool
	// emptyGetError, when non-empty, is the S3 error code returned
	// by the GET-side handler when no config has been stored. Used
	// only to keep the canonical-table reference in one place — the
	// GET-side dispatcher in s3.go already emits these.
	emptyGetError string
}

// bucketSubresourceHandlers is the canonical AWS S3 bucket-level
// subresource table — every entry corresponds to one or more
// canonical operations in the AWS S3 REST API surface. New entries
// land here when a new subresource is added (or one becomes
// supported); the handler dispatcher reads from this table so the
// PUT / GET / DELETE wiring stays in lockstep with the enumeration.
//
// Real-S3 reference:
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_Simple_Storage_Service.html
var bucketSubresourceHandlers = map[string]bucketSubresource{
	"versioning":          {putStatus: http.StatusOK},
	"lifecycle":           {putStatus: http.StatusOK, hasDelete: true, emptyGetError: "NoSuchLifecycleConfiguration"},
	"cors":                {putStatus: http.StatusOK, hasDelete: true, emptyGetError: "NoSuchCORSConfiguration"},
	"policy":              {putStatus: http.StatusNoContent, hasDelete: true, emptyGetError: "NoSuchBucketPolicy"},
	"encryption":          {putStatus: http.StatusOK, hasDelete: true, emptyGetError: "ServerSideEncryptionConfigurationNotFoundError"},
	"replication":         {putStatus: http.StatusOK, hasDelete: true, emptyGetError: "ReplicationConfigurationNotFoundError"},
	"tagging":             {putStatus: http.StatusNoContent, hasDelete: true, emptyGetError: "NoSuchTagSet"},
	"website":             {putStatus: http.StatusOK, hasDelete: true, emptyGetError: "NoSuchWebsiteConfiguration"},
	"logging":             {putStatus: http.StatusOK},
	"acl":                 {putStatus: http.StatusOK},
	"requestPayment":      {putStatus: http.StatusOK},
	"accelerate":          {putStatus: http.StatusOK},
	"ownershipControls":   {putStatus: http.StatusOK, hasDelete: true, emptyGetError: "OwnershipControlsNotFoundError"},
	"notification":        {putStatus: http.StatusOK},
	"publicAccessBlock":   {putStatus: http.StatusOK, hasDelete: true, emptyGetError: "NoSuchPublicAccessBlockConfiguration"},
	"object-lock":         {putStatus: http.StatusOK, emptyGetError: "ObjectLockConfigurationNotFoundError"},
	"intelligent-tiering": {putStatus: http.StatusOK, hasDelete: true},
	"inventory":           {putStatus: http.StatusOK, hasDelete: true},
	"analytics":           {putStatus: http.StatusOK, hasDelete: true},
	"metrics":             {putStatus: http.StatusOK, hasDelete: true},
}

// firstBucketSubresource returns the first bucket-level subresource
// matching the request query. Order doesn't matter — real S3
// supports exactly one subresource selector per request.
func firstBucketSubresource(q map[string][]string) (string, bucketSubresource, bool) {
	for name, spec := range bucketSubresourceHandlers {
		if _, ok := q[name]; ok {
			return name, spec, true
		}
	}
	return "", bucketSubresource{}, false
}

// handleS3PutBucketDispatch routes `PUT /{bucket}` between
// CreateBucket (no subresource query) and the table-driven
// subresource-config handler.
func handleS3PutBucketDispatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Has("metadataInventoryTable"):
		handleS3UpdateBucketMetadataInventoryTable(w, r)
		return
	case q.Has("metadataJournalTable"):
		handleS3UpdateBucketMetadataJournalTable(w, r)
		return
	case q.Has("metadataAnnotationTable"):
		handleS3UpdateBucketMetadataAnnotationTable(w, r)
		return
	case q.Has("abac"):
		handleS3PutBucketAbac(w, r)
		return
	}
	if name, spec, ok := firstBucketSubresource(q); ok {
		handleS3PutBucketSubresource(w, r, name, spec)
		return
	}
	handleS3CreateBucket(w, r)
}

// handleS3DeleteBucketDispatch routes `DELETE /{bucket}` between
// DeleteBucket (no subresource query) and the table-driven
// subresource-config clear handler.
func handleS3DeleteBucketDispatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Has("metadataConfiguration"):
		handleS3DeleteBucketMetadataConfiguration(w, r)
		return
	case q.Has("metadataTable"):
		handleS3DeleteBucketMetadataTableConfiguration(w, r)
		return
	}
	if name, spec, ok := firstBucketSubresource(q); ok {
		if !spec.hasDelete {
			sim.S3ErrorXML(w, "MethodNotAllowed",
				fmt.Sprintf("DELETE is not supported on ?%s — clear via PUT with an empty configuration", name),
				sim.PathParam(r, "bucket"), sim.RequestID(r.Context()), http.StatusMethodNotAllowed)
			return
		}
		handleS3DeleteBucketSubresource(w, r, name)
		return
	}
	handleS3DeleteBucket(w, r)
}

// handleS3PutBucketSubresource reads the PUT body, stores it under
// `<bucket>/<subresource>`, and responds with the canonical success
// status for that subresource (200 OK or 204 No Content).
func handleS3PutBucketSubresource(w http.ResponseWriter, r *http.Request, sub string, spec bucketSubresource) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	// Bucket-subresource bodies are small fixed-shape XML or JSON
	// documents (VersioningConfiguration, LifecycleConfiguration,
	// CORSConfiguration, Tagging, …). aws-sdk-go-v2 sends them
	// without aws-chunked / gzip framing; a direct `io.ReadAll` is
	// wire-faithful.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.S3ErrorXML(w, "IncompleteBody",
			"Failed to read request body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/xml"
	}
	headers := bucketSubresourceResponseHeaders(sub, r)
	id := r.URL.Query().Get("id")
	key := s3BucketConfigKeyID(bucket, sub, id)
	s3BucketConfigs.Put(key, S3BucketConfig{
		Key:         key,
		Bucket:      bucket,
		Subresource: sub,
		Body:        body,
		ContentType: contentType,
		Headers:     headers,
	})
	// Mirror the bucket policy into the central resource-policy store so the
	// IAM enforcement gate can resolve it by the bucket ARN.
	if sub == "policy" {
		iamPutResourcePolicy(s3BucketARN(bucket), string(body))
	}
	setResponseHeaders(w, headers)
	w.WriteHeader(spec.putStatus)
}

// handleS3DeleteBucketSubresource clears the stored config for the
// bucket+subresource pair and responds 204 No Content (real S3
// uses 204 across the board for the delete subresource ops).
func handleS3DeleteBucketSubresource(w http.ResponseWriter, r *http.Request, sub string) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	s3BucketConfigs.Delete(s3BucketConfigKeyID(bucket, sub, r.URL.Query().Get("id")))
	if sub == "policy" {
		iamDeleteResourcePolicy(s3BucketARN(bucket))
	}
	w.WriteHeader(http.StatusNoContent)
}

// getStoredBucketSubresource looks up the stored config bytes for a
// (bucket, subresource) pair. Returns the body bytes + content type
// when set; (nil, "", false) when never PUT.
func getStoredBucketSubresource(bucket, sub string) ([]byte, string, map[string]string, bool) {
	return getStoredBucketSubresourceID(bucket, sub, "")
}

func getStoredBucketSubresourceID(bucket, sub, id string) ([]byte, string, map[string]string, bool) {
	cfg, ok := s3BucketConfigs.Get(s3BucketConfigKeyID(bucket, sub, id))
	if !ok {
		return nil, "", nil, false
	}
	return cfg.Body, cfg.ContentType, cfg.Headers, true
}

func listStoredBucketSubresources(bucket, sub string) [][]byte {
	configs := s3BucketConfigs.Filter(func(config S3BucketConfig) bool {
		return config.Bucket == bucket && config.Subresource == sub &&
			strings.HasPrefix(config.Key, s3BucketConfigKeyID(bucket, sub, "")+"/")
	})
	sort.Slice(configs, func(i, j int) bool { return configs[i].Key < configs[j].Key })
	out := make([][]byte, 0, len(configs))
	for _, config := range configs {
		out = append(out, append([]byte(nil), config.Body...))
	}
	return out
}

// emitStoredOrEmptyXML responds with the stored config for the
// bucket+subresource pair when one exists; otherwise emits the
// canonical empty-config XML envelope shape (`<RootElement/>`).
//
// Used by GET-side handlers for subresources that don't 404 on
// empty (e.g. `?versioning`, `?logging`, `?notification`).
func emitStoredOrEmptyXML(w http.ResponseWriter, bucket, sub, rootElement string) {
	if body, ct, headers, ok := getStoredBucketSubresource(bucket, sub); ok {
		if ct == "" {
			ct = "application/xml"
		}
		w.Header().Set("Content-Type", ct)
		setResponseHeaders(w, headers)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xmlDoc := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><%s xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`, rootElement)
	_, _ = w.Write([]byte(xmlDoc))
}

// emitStoredOr404 responds with the stored config when one exists;
// otherwise emits the canonical S3 error code for "this subresource
// has never been set" (`NoSuchBucketPolicy`, `NoSuchTagSet`, etc.).
func emitStoredOr404(w http.ResponseWriter, r *http.Request, bucket, sub, errorCode, errorMsg string) {
	if body, ct, headers, ok := getStoredBucketSubresource(bucket, sub); ok {
		if ct == "" {
			ct = "application/xml"
		}
		w.Header().Set("Content-Type", ct)
		setResponseHeaders(w, headers)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	sim.S3ErrorXML(w, errorCode, errorMsg, bucket, sim.RequestID(r.Context()), http.StatusNotFound)
}

func emitStoredIDOr404(w http.ResponseWriter, r *http.Request, bucket, sub, id string) {
	if body, ct, headers, ok := getStoredBucketSubresourceID(bucket, sub, id); ok {
		if ct == "" {
			ct = "application/xml"
		}
		w.Header().Set("Content-Type", ct)
		setResponseHeaders(w, headers)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	sim.S3ErrorXML(w, "NoSuchConfiguration", "The specified configuration does not exist",
		bucket, sim.RequestID(r.Context()), http.StatusNotFound)
}

func emitBucketConfigurationList(w http.ResponseWriter, bucket, sub, rootElement string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><%s xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated>`, rootElement)
	for _, body := range listStoredBucketSubresources(bucket, sub) {
		_, _ = w.Write(stripXMLDeclaration(body))
	}
	_, _ = fmt.Fprintf(w, `</%s>`, rootElement)
}

func bucketSubresourceResponseHeaders(sub string, r *http.Request) map[string]string {
	if sub != "lifecycle" {
		return nil
	}
	value := r.Header.Get("x-amz-transition-default-minimum-object-size")
	if value == "" {
		value = s3LifecycleTransitionDefaultMinimumObjectSize
	}
	return map[string]string{"x-amz-transition-default-minimum-object-size": value}
}

func setResponseHeaders(w http.ResponseWriter, headers map[string]string) {
	for name, value := range headers {
		w.Header().Set(name, value)
	}
}

func stripXMLDeclaration(body []byte) []byte {
	s := strings.TrimSpace(string(body))
	if strings.HasPrefix(s, "<?xml") {
		if idx := strings.Index(s, "?>"); idx >= 0 {
			s = strings.TrimSpace(s[idx+2:])
		}
	}
	return []byte(s)
}
