package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// S3BucketConfig stores a bucket subresource's raw PUT body so GET round-trips
// it byte for byte; S3 re-serialises the same logical document, and byte
// equality is the stricter check.
type S3BucketConfig struct {
	Key         string
	Bucket      string
	Subresource string
	Body        []byte
	ContentType string
	Headers     map[string]string
}

// Keyed by `<bucket>/<subresource>`.
var s3BucketConfigs sim.Store[S3BucketConfig]

const s3LifecycleTransitionDefaultMinimumObjectSize = "all_storage_classes_128K"

func s3BucketConfigKeyID(bucket, sub, id string) string {
	if id == "" {
		return bucket + "/" + sub
	}
	return bucket + "/" + sub + "/" + id
}

// bucketSubresource is one bucket-level subresource with the success status S3
// answers its PUT and DELETE with, which differ between 200 and 204 per
// operation.
type bucketSubresource struct {
	// putStatus is the success status emitted on PUT (200 or 204).
	// 0 → subresource has no PUT shape.
	putStatus int
	// hasDelete reports whether S3 accepts DELETE for the subresource (`?policy` and
	// `?lifecycle` do; `?versioning` does not).
	hasDelete bool
	// emptyGetError is the error code GET answers when no config is stored; the GET
	// dispatcher in s3.go emits it.
	emptyGetError string
}

// bucketSubresourceHandlers is the S3 bucket-level subresource table; the PUT,
// GET and DELETE dispatchers read it.
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

// firstBucketSubresource returns the bucket-level subresource the request query
// selects; S3 allows exactly one selector per request.
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
			S3ErrorXML(w, "MethodNotAllowed",
				fmt.Sprintf("DELETE is not supported on ?%s — clear via PUT with an empty configuration", name),
				sim.PathParam(r, "bucket"), sim.RequestID(r.Context()), http.StatusMethodNotAllowed)
			return
		}
		handleS3DeleteBucketSubresource(w, r, name)
		return
	}
	handleS3DeleteBucket(w, r)
}

// handleS3PutBucketSubresource stores the PUT body under
// `<bucket>/<subresource>` and answers the subresource's success status.
func handleS3PutBucketSubresource(w http.ResponseWriter, r *http.Request, sub string, spec bucketSubresource) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	// Bucket subresource bodies are small XML or JSON documents that aws-sdk-go-v2
	// sends without aws-chunked or gzip framing, so read them directly.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		S3ErrorXML(w, "IncompleteBody",
			"Failed to read request body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	switch {
	case sub == "object-lock":
		handleS3PutObjectLockConfiguration(w, r, bucket, body)
		return
	case sub == "versioning" && s3VersioningChangeRefused(w, r, bucket, body):
		return
	case sub == "acl":
		var ok bool
		if body, ok = s3RequestACL(w, r, body); !ok {
			return
		}
	}
	var notification s3NotificationConfiguration
	validateNotification := sub == "notification" && r.Header.Get("x-amz-skip-destination-validation") != "true"
	if sub == "notification" {
		if err := xml.Unmarshal(body, &notification); err != nil {
			S3ErrorXML(w, "MalformedXML",
				"The XML you provided was not well-formed or did not validate against our published schema",
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
		if validateNotification {
			if rejected := s3ValidateNotificationDestinations(bucket, notification); len(rejected) > 0 {
				s3DestinationValidationError(w, sim.RequestID(r.Context()), rejected)
				return
			}
		}
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
		iamPutResourcePolicy(s3BucketPolicyARN(r, bucket), string(body))
	}
	if validateNotification {
		s3SendTestEvents(bucket, sim.RequestID(r.Context()), notification)
	}
	setResponseHeaders(w, headers)
	w.WriteHeader(spec.putStatus)
}

// handleS3DeleteBucketSubresource clears the stored config and answers 204, as
// S3 does for every subresource delete.
func handleS3DeleteBucketSubresource(w http.ResponseWriter, r *http.Request, sub string) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	s3BucketConfigs.Delete(s3BucketConfigKeyID(bucket, sub, r.URL.Query().Get("id")))
	if sub == "policy" {
		iamDeleteResourcePolicy(s3BucketPolicyARN(r, bucket))
	}
	w.WriteHeader(http.StatusNoContent)
}

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

// emitStoredOrEmptyXML answers the stored config, or the empty `<RootElement/>`
// for subresources that do not 404 when unset (`?versioning`, `?logging`,
// `?notification`).
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

// emitStoredOr404 answers the stored config, or the error S3 answers for a
// subresource never set (`NoSuchBucketPolicy`, `NoSuchTagSet`).
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
	S3ErrorXML(w, errorCode, errorMsg, bucket, sim.RequestID(r.Context()), http.StatusNotFound)
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
	S3ErrorXML(w, "NoSuchConfiguration", "The specified configuration does not exist",
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
