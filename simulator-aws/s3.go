package main

import (
	"bytes"
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// S3 types

type S3Bucket struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`

	// DirectoryZone is the Availability Zone id a directory bucket is placed
	// in, empty for a general purpose bucket. It is what separates the two
	// listings — ListBuckets returns general purpose buckets and
	// ListDirectoryBuckets returns these — and it is not part of either
	// response body, which is why it carries no XML name.
	DirectoryZone string `xml:"-"`
	// DataRedundancy is how many zones a directory bucket's data is held in.
	DataRedundancy string `xml:"-"`
}

type S3Object struct {
	Key          string
	Data         []byte
	ContentType  string
	ETag         string
	LastModified time.Time
	Size         int64
	Metadata     map[string]string

	// ACL holds the raw <AccessControlPolicy> XML body set by
	// PutObjectAcl (empty until set; GetObjectAcl synthesizes the
	// canonical owner-FULL_CONTROL ACL when empty).
	ACL []byte
	// LegalHoldStatus is the Object Lock legal-hold status ("ON"/"OFF"),
	// empty when never set.
	LegalHoldStatus string
	// RetentionMode / RetainUntilDate hold the Object Lock retention set
	// by PutObjectRetention. Mode is "GOVERNANCE"/"COMPLIANCE"; empty
	// when no retention is configured.
	RetentionMode   string
	RetainUntilDate string
	// RestoreInProgress / RestoreExpiryDate track an in-flight or
	// completed RestoreObject request (Glacier-class restore lifecycle).
	RestoreRequested  bool
	RestoreInProgress bool
	RestoreExpiryDate string
	// SSEAlgorithm / SSEKMSKeyID hold the object's server-side encryption
	// settings, set by UpdateObjectEncryption. Empty when the object uses
	// the default (none recorded). Surfaced as the
	// x-amz-server-side-encryption[-aws-kms-key-id] response headers.
	SSEAlgorithm string
	SSEKMSKeyID  string
}

// XML response types for S3

type s3ListAllMyBucketsResult struct {
	XMLName xml.Name  `xml:"ListAllMyBucketsResult"`
	Xmlns   string    `xml:"xmlns,attr"`
	Owner   s3Owner   `xml:"Owner"`
	Buckets s3Buckets `xml:"Buckets"`
}

type s3Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type s3Buckets struct {
	Bucket []S3Bucket `xml:"Bucket"`
}

type s3ListBucketResult struct {
	XMLName               xml.Name         `xml:"ListBucketResult"`
	Xmlns                 string           `xml:"xmlns,attr"`
	Name                  string           `xml:"Name"`
	Prefix                string           `xml:"Prefix,omitempty"`
	MaxKeys               int              `xml:"MaxKeys"`
	KeyCount              int              `xml:"KeyCount"`
	IsTruncated           bool             `xml:"IsTruncated"`
	Contents              []s3ObjectInfo   `xml:"Contents"`
	CommonPrefixes        []s3CommonPrefix `xml:"CommonPrefixes,omitempty"`
	ContinuationToken     string           `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string           `xml:"NextContinuationToken,omitempty"`
	StartAfter            string           `xml:"StartAfter,omitempty"`
}

type s3ObjectInfo struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type s3CommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// State stores
var (
	s3Buckets_ sim.Store[S3Bucket]
	s3Objects  sim.Store[S3Object]
)

func s3ObjectKey(bucket, key string) string {
	return bucket + "/" + key
}

// s3PutServiceObject stores bytes through Amazon Simple Storage Service's
// object data plane on behalf of another AWS service. Amazon Data Firehose,
// AWS Private CA audit reports, and certificate-revocation lists use the same
// durable object representation and notification pipeline as PutObject.
func s3PutServiceObject(bucket, key string, body []byte, contentType string, metadata map[string]string) (S3Object, error) {
	if _, ok := s3Buckets_.Get(bucket); !ok {
		return S3Object{}, fmt.Errorf("bucket %q does not exist", bucket)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	hash := md5.Sum(body)
	etag := fmt.Sprintf("\"%x\"", hash)
	obj := S3Object{
		Key:          s3ObjectKey(bucket, key),
		Data:         append([]byte(nil), body...),
		ContentType:  contentType,
		ETag:         etag,
		LastModified: time.Now(),
		Size:         int64(len(body)),
		Metadata:     metadata,
	}
	s3Objects.Put(obj.Key, obj)
	s3FireObjectNotifications(bucket, key, "ObjectCreated:Put", etag, obj.Size)
	return obj, nil
}

// s3BucketARN returns the canonical S3 bucket ARN (no account/region — S3
// bucket ARNs are global: arn:aws:s3:::bucket-name).
func s3BucketARN(bucket string) string {
	return "arn:aws:s3:::" + bucket
}

func registerS3(srv *sim.Server) {
	// Object-level ops are CloudTrail DATA events (excluded from LookupEvents);
	// bucket-level ops (ListBuckets, CreateBucket, …) are management events.
	cloudTrailDeclareDataEvents("s3.amazonaws.com",
		"GetObject", "PutObject", "DeleteObject", "HeadObject", "CopyObject",
		"DeleteObjects", "GetObjectTagging", "PutObjectTagging", "DeleteObjectTagging",
		"ListParts", "UploadPart", "CreateMultipartUpload", "CompleteMultipartUpload",
		"AbortMultipartUpload")
	s3Buckets_ = sim.MakeStore[S3Bucket](srv.DB(), "s3_buckets")
	s3Objects = sim.MakeStore[S3Object](srv.DB(), "s3_objects")
	s3ObjectAnnotations = sim.MakeStore[s3Annotation](srv.DB(), "s3_object_annotations")
	s3BucketConfigs = sim.MakeStore[S3BucketConfig](srv.DB(), "s3_bucket_configs")
	s3MultipartUploads = sim.MakeStore[S3MultipartUpload](srv.DB(), "s3_multipart_uploads")
	s3ObjectTags = sim.MakeStore[map[string]string](srv.DB(), "s3_object_tags")
	s3MetadataConfigs = sim.MakeStore[s3MetadataConfig](srv.DB(), "s3_metadata_configs")
	s3MetadataTableConfigs = sim.MakeStore[s3MetadataTableConfig](srv.DB(), "s3_metadata_table_configs")
	s3AbacStatus = sim.MakeStore[string](srv.DB(), "s3_abac_status")

	mux := srv

	// S3 uses path-style URLs at the root of the endpoint:
	//   GET    /                       ListBuckets
	//   PUT    /{bucket}                CreateBucket
	//   HEAD   /{bucket}                HeadBucket
	//   DELETE /{bucket}                DeleteBucket
	//   GET    /{bucket}                ListObjectsV2 / sub-resource dispatch
	//   PUT    /{bucket}/{key...}       PutObject
	//   GET    /{bucket}/{key...}       GetObject
	//   HEAD   /{bucket}/{key...}       HeadObject
	//   DELETE /{bucket}/{key...}       DeleteObject
	//
	// This matches the real S3 wire protocol; stock `aws` CLI / SDK /
	// Terraform clients work against the published AWS_ENDPOINT_URL
	// without a path-prefix workaround. Cross-routing notes:
	//
	//   - `POST /` is owned by the AWS-JSON / Query-protocol
	//     dispatcher in main.go; S3 has no POST-at-root operations
	//     (multi-object delete is `POST /{bucket}?delete`).
	//   - Other REST services mount under fixed API-version prefixes
	//     (Lambda /2015-03-31/, EFS /2015-02-01/, CloudFront
	//     /2020-05-31/, Route 53 /2013-04-01/, Amplify /apps/ +
	//     /webhooks/). Go's net/http mux gives literal segments
	//     priority over wildcards, so `PUT /2015-03-31/functions/...`
	//     reaches Lambda even though `PUT /{bucket}/{key...}` is
	//     also registered. The only edge case is a bucket name that
	//     exactly matches one of those API-version literals (e.g.,
	//     a bucket named "2015-03-31") plus a key whose path aligns
	//     with the literal route — sim documents this as a known
	//     edge case rather than rejecting such bucket names.
	s3BucketResource := cloudTrailRESTResource("AWS::S3::Bucket", "bucket")
	s3ObjectResource := cloudTrailRESTResource("AWS::S3::Object", "key", "bucket")
	staticOp := func(name string) func(*http.Request, []byte) string {
		return func(*http.Request, []byte) string { return name }
	}
	mux.HandleFunc("GET /{$}", cloudTrailRecordedREST("ListBuckets", "s3.amazonaws.com", nil, s3Enforced(staticOp("ListBuckets"), handleS3ListBuckets)))
	mux.HandleFunc("PUT /{bucket}", cloudTrailRecordedRESTDynamic(s3BucketOperationName, "s3.amazonaws.com", s3BucketResource, s3Enforced(s3BucketOperationName, handleS3PutBucketDispatch)))
	mux.HandleFunc("DELETE /{bucket}", cloudTrailRecordedRESTDynamic(s3BucketOperationName, "s3.amazonaws.com", s3BucketResource, s3Enforced(s3BucketOperationName, handleS3DeleteBucketDispatch)))
	mux.HandleFunc("GET /{bucket}", cloudTrailRecordedRESTDynamic(s3BucketOperationName, "s3.amazonaws.com", s3BucketResource, s3Enforced(s3BucketOperationName, handleS3GetOrHeadBucket)))
	mux.HandleFunc("PUT /{bucket}/{key...}", cloudTrailRecordedRESTDynamic(s3ObjectOperationName, "s3.amazonaws.com", s3ObjectResource, s3Enforced(s3ObjectOperationName, handleS3PutObjectDispatch)))
	mux.HandleFunc("GET /{bucket}/{key...}", cloudTrailRecordedRESTDynamic(s3ObjectOperationName, "s3.amazonaws.com", s3ObjectResource, s3Enforced(s3ObjectOperationName, handleS3GetOrHeadObjectDispatch)))
	mux.HandleFunc("DELETE /{bucket}/{key...}", cloudTrailRecordedRESTDynamic(s3ObjectOperationName, "s3.amazonaws.com", s3ObjectResource, s3Enforced(s3ObjectOperationName, handleS3DeleteObjectDispatch)))
	// POST routes for S3 subresource families. Without these, the
	// catch-all `POST /` in main.go dispatches the request as awsQuery
	// (looks for an `Action` parameter), which returns the wrong-
	// protocol `MissingAction` envelope.
	mux.HandleFunc("POST /{bucket}/{key...}", cloudTrailRecordedRESTDynamic(s3ObjectOperationName, "s3.amazonaws.com", s3ObjectResource, s3Enforced(s3ObjectOperationName, handleS3PostObjectDispatch)))
	mux.HandleFunc("POST /{bucket}", cloudTrailRecordedRESTDynamic(s3BucketOperationName, "s3.amazonaws.com", s3BucketResource, s3Enforced(s3BucketOperationName, handleS3PostBucketDispatch)))
	// HEAD routes intentionally NOT registered: Go's net/http mux
	// auto-routes HEAD requests to the matching GET handler when no
	// HEAD-specific handler exists. Registering both forms together
	// trips the mux's method-coverage conflict detector against
	// existing literal routes (e.g., `GET /health`). The dispatch
	// to HeadBucket / HeadObject happens by method check inside the
	// GET handler.
}

// s3Enforced applies call-time IAM enforcement to an S3 REST handler. The IAM
// action is "s3:" + the request's S3 operation name; the resource is the
// bucket/object ARN. Enforcement is a no-op for unregistered/test credentials
// (the permissive default), so existing S3 tests keep working — only a caller
// with a registered restricted key, or a bucket policy that denies, is blocked.
func s3Enforced(opName func(*http.Request, []byte) string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Authenticate the SigV4 signature first. A request that presents no
		// credential (sigv4NoCredential) is anonymous — allowed through so a
		// bucket/object policy granting Principal:"*" still serves public
		// reads; a present-but-invalid signature is rejected. The object body
		// is never read here (nil): S3 clients always declare the payload hash
		// in x-amz-content-sha256, so large uploads are not buffered.
		if _, serr := sigv4VerifyS3(r, nil); serr != nil {
			sigv4WriteS3Error(w, r, serr)
			return
		}
		if op := opName(r, nil); op != "" {
			// A request signed with a session credential is authorized by that
			// session first: it opens one directory bucket, in one mode, until
			// it expires.
			if s3EnforceExpressSession(w, r, op, sim.PathParam(r, "bucket")) {
				return
			}
			// An access point narrows what can be done through it, whatever
			// the caller's own policies allow on the bucket behind it.
			if s3EnforceAccessPointScope(w, r, op) {
				return
			}
			if !iamEnforceREST(w, r, "s3:"+op, s3RequestResourceARN(r), s3WriteIAMDeny) {
				return
			}
		}
		h(w, r)
	}
}

func s3RequestResourceARN(r *http.Request) string {
	bucket := sim.PathParam(r, "bucket")
	if bucket == "" {
		return "*"
	}
	if key := sim.PathParam(r, "key"); key != "" {
		return "arn:aws:s3:::" + bucket + "/" + key
	}
	return "arn:aws:s3:::" + bucket
}

func s3WriteIAMDeny(w http.ResponseWriter, r *http.Request, principalArn, action string) {
	sim.S3ErrorXML(w, "AccessDenied",
		"User: "+principalArn+" is not authorized to perform: "+action+
			" because no identity-based policy or resource-based policy allows the "+action+" action",
		strings.TrimPrefix(r.URL.Path, "/"), generateUUID(), http.StatusForbidden)
}

func s3BucketOperationName(r *http.Request, _ []byte) string {
	// A read addressed to an Object Lambda access point arrives on the access
	// point's own host with the object key as the whole path, so it matches the
	// bucket route even though the operation is GetObject.
	if _, ok := s3ObjectLambdaHostAccessPoint(r.Host); ok &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return "GetObject"
	}
	q := r.URL.Query()
	switch r.Method {
	case http.MethodHead:
		return "HeadBucket"
	case http.MethodPut:
		if q.Has("object-lock") {
			// The Object Lock configuration's API operation name has no
			// "Bucket" infix (PutObjectLockConfiguration), unlike the
			// generic PutBucket<Subresource> family.
			return "PutObjectLockConfiguration"
		}
		switch {
		case q.Has("metadataInventoryTable"):
			return "UpdateBucketMetadataInventoryTableConfiguration"
		case q.Has("metadataJournalTable"):
			return "UpdateBucketMetadataJournalTableConfiguration"
		case q.Has("metadataAnnotationTable"):
			return "UpdateBucketMetadataAnnotationTableConfiguration"
		case q.Has("abac"):
			return "PutBucketAbac"
		}
		if name, _, ok := firstBucketSubresource(q); ok {
			return "PutBucket" + s3SubresourceOperationSuffix(name)
		}
		return "CreateBucket"
	case http.MethodDelete:
		switch {
		case q.Has("metadataConfiguration"):
			return "DeleteBucketMetadataConfiguration"
		case q.Has("metadataTable"):
			return "DeleteBucketMetadataTableConfiguration"
		}
		if name, _, ok := firstBucketSubresource(q); ok {
			return "DeleteBucket" + s3SubresourceOperationSuffix(name)
		}
		return "DeleteBucket"
	case http.MethodPost:
		if q.Has("delete") {
			return "DeleteObjects"
		}
		if q.Has("metadataConfiguration") {
			return "CreateBucketMetadataConfiguration"
		}
		if q.Has("metadataTable") {
			return "CreateBucketMetadataTableConfiguration"
		}
	case http.MethodGet:
		switch {
		case q.Has("session"):
			return "CreateSession"
		case q.Has("metadataConfiguration"):
			return "GetBucketMetadataConfiguration"
		case q.Has("metadataTable"):
			return "GetBucketMetadataTableConfiguration"
		case q.Has("abac"):
			return "GetBucketAbac"
		case q.Has("policy"):
			return "GetBucketPolicy"
		case q.Has("uploads"):
			return "ListMultipartUploads"
		case q.Has("versions"):
			return "ListObjectVersions"
		case q.Has("location"):
			return "GetBucketLocation"
		case q.Has("policyStatus"):
			return "GetBucketPolicyStatus"
		case q.Has("intelligent-tiering"):
			if q.Get("id") != "" {
				return "GetBucketIntelligentTieringConfiguration"
			}
			return "ListBucketIntelligentTieringConfigurations"
		case q.Has("inventory"):
			if q.Get("id") != "" {
				return "GetBucketInventoryConfiguration"
			}
			return "ListBucketInventoryConfigurations"
		case q.Has("analytics"):
			if q.Get("id") != "" {
				return "GetBucketAnalyticsConfiguration"
			}
			return "ListBucketAnalyticsConfigurations"
		case q.Has("metrics"):
			if q.Get("id") != "" {
				return "GetBucketMetricsConfiguration"
			}
			return "ListBucketMetricsConfigurations"
		case q.Has("object-lock"):
			// GetObjectLockConfiguration — no "Bucket" infix, unlike the
			// generic GetBucket<Subresource> family.
			return "GetObjectLockConfiguration"
		}
		if name, _, ok := firstBucketSubresource(q); ok {
			return "GetBucket" + s3SubresourceOperationSuffix(name)
		}
		// The legacy V1 list (ListObjects) is selected by the absence of
		// `list-type=2`; ListObjectsV2 sets it. Both share GET /{bucket}
		// with no subresource selector.
		if q.Get("list-type") == "2" {
			return "ListObjectsV2"
		}
		return "ListObjects"
	}
	return ""
}

func s3ObjectOperationName(r *http.Request, _ []byte) string {
	q := r.URL.Query()
	switch r.Method {
	case http.MethodHead:
		return "HeadObject"
	case http.MethodPut:
		switch {
		case q.Has("renameObject"):
			return "RenameObject"
		case q.Has("encryption"):
			// Object-level ?encryption is UpdateObjectEncryption (the
			// bucket-level ?encryption op is PutBucketEncryption, routed
			// by s3BucketOperationName on the no-key path).
			return "UpdateObjectEncryption"
		case r.Header.Get("x-amz-copy-source") != "" && q.Has("uploadId") && q.Has("partNumber"):
			return "UploadPartCopy"
		case r.Header.Get("x-amz-copy-source") != "":
			return "CopyObject"
		case q.Has("uploadId") && q.Has("partNumber"):
			return "UploadPart"
		case q.Has("annotation"):
			return "PutObjectAnnotation"
		case q.Has("tagging"):
			return "PutObjectTagging"
		case q.Has("acl"):
			return "PutObjectAcl"
		case q.Has("legal-hold"):
			return "PutObjectLegalHold"
		case q.Has("retention"):
			return "PutObjectRetention"
		default:
			return "PutObject"
		}
	case http.MethodGet:
		switch {
		case q.Has("uploadId"):
			return "ListParts"
		case q.Has("annotation"):
			// The annotation subresource resolves to the single-annotation
			// read when AnnotationName names one, and to the list otherwise.
			if q.Get("annotationName") != "" {
				return "GetObjectAnnotation"
			}
			return "ListObjectAnnotations"
		case q.Has("tagging"):
			return "GetObjectTagging"
		case q.Has("acl"):
			return "GetObjectAcl"
		case q.Has("attributes"):
			return "GetObjectAttributes"
		case q.Has("legal-hold"):
			return "GetObjectLegalHold"
		case q.Has("retention"):
			return "GetObjectRetention"
		case q.Has("torrent"):
			return "GetObjectTorrent"
		default:
			return "GetObject"
		}
	case http.MethodPost:
		switch {
		case q.Has("uploads"):
			return "CreateMultipartUpload"
		case q.Has("uploadId"):
			return "CompleteMultipartUpload"
		case q.Has("restore"):
			return "RestoreObject"
		case q.Has("select"):
			return "SelectObjectContent"
		}
	case http.MethodDelete:
		switch {
		case q.Has("uploadId"):
			return "AbortMultipartUpload"
		case q.Has("annotation"):
			return "DeleteObjectAnnotation"
		case q.Has("tagging"):
			return "DeleteObjectTagging"
		default:
			return "DeleteObject"
		}
	}
	return ""
}

func s3SubresourceOperationSuffix(name string) string {
	switch name {
	case "acl":
		return "Acl"
	case "cors":
		return "Cors"
	case "lifecycle":
		return "LifecycleConfiguration"
	case "policy":
		return "Policy"
	case "versioning":
		return "Versioning"
	case "website":
		return "Website"
	case "logging":
		return "Logging"
	case "requestPayment":
		return "RequestPayment"
	case "accelerate":
		return "AccelerateConfiguration"
	case "replication":
		return "Replication"
	case "encryption":
		return "Encryption"
	case "tagging":
		return "Tagging"
	case "notification":
		return "NotificationConfiguration"
	case "publicAccessBlock":
		return "PublicAccessBlock"
	case "object-lock":
		return "ObjectLockConfiguration"
	case "ownershipControls":
		return "OwnershipControls"
	case "intelligent-tiering":
		return "IntelligentTieringConfiguration"
	case "inventory":
		return "InventoryConfiguration"
	case "analytics":
		return "AnalyticsConfiguration"
	case "metrics":
		return "MetricsConfiguration"
	}
	return ""
}

// handleS3GetOrHeadBucket dispatches `HEAD /{bucket}` to HeadBucket
// (metadata-only existence check) and otherwise runs the GET-flavor
// sub-resource dispatch in handleS3GetBucket.
func handleS3GetOrHeadBucket(w http.ResponseWriter, r *http.Request) {
	if s3ServeObjectLambdaRead(w, r) {
		return
	}
	if r.Method == http.MethodHead {
		handleS3HeadBucket(w, r)
		return
	}
	handleS3GetBucket(w, r)
}

// handleS3GetOrHeadObject dispatches `HEAD /{bucket}/{key...}` to
// HeadObject (headers-only) and otherwise runs the full GetObject.
func handleS3GetOrHeadObject(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		handleS3HeadObject(w, r)
		return
	}
	handleS3GetObject(w, r)
}

func handleS3ListBuckets(w http.ResponseWriter, r *http.Request) {
	if s3ServeListDirectoryBuckets(w, r) {
		return
	}
	// A directory bucket is returned by ListDirectoryBuckets and not by this
	// listing, which is what separates the two surfaces.
	buckets := []S3Bucket{}
	for _, b := range s3Buckets_.List() {
		if b.DirectoryZone == "" {
			buckets = append(buckets, b)
		}
	}

	result := s3ListAllMyBucketsResult{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
		Owner: s3Owner{
			ID:          awsAccountID(),
			DisplayName: "simulator",
		},
		Buckets: s3Buckets{
			Bucket: buckets,
		},
	}

	sim.WriteXML(w, http.StatusOK, result)
}

func handleS3CreateBucket(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	if bucket == "" {
		sim.S3ErrorXML(w, "InvalidBucketName", "Bucket name is required", "/", sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}

	if _, exists := s3Buckets_.Get(bucket); exists {
		// In us-east-1 a same-owner repeat CreateBucket is idempotent (200);
		// every other region returns BucketAlreadyOwnedByYou (409).
		if awsRegion() == "us-east-1" {
			w.Header().Set("Location", "/"+bucket)
			w.WriteHeader(http.StatusOK)
			return
		}
		sim.S3ErrorXML(w, "BucketAlreadyOwnedByYou",
			"Your previous request to create the named bucket succeeded and you already own it.",
			bucket, sim.RequestID(r.Context()), http.StatusConflict)
		return
	}

	b := S3Bucket{
		Name:         bucket,
		CreationDate: time.Now().UTC().Format(time.RFC3339),
	}
	var createConfig struct {
		Tags []struct {
			Key   string `xml:"Key"`
			Value string `xml:"Value"`
		} `xml:"Tags>Tag"`
		Location struct {
			Type string `xml:"Type"`
			Name string `xml:"Name"`
		} `xml:"Location"`
		Bucket struct {
			Type           string `xml:"Type"`
			DataRedundancy string `xml:"DataRedundancy"`
		} `xml:"Bucket"`
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		sim.S3ErrorXML(w, "IncompleteBody", "The request body could not be read",
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if len(bytes.TrimSpace(bodyBytes)) > 0 {
		if err := xml.Unmarshal(bodyBytes, &createConfig); err != nil {
			sim.S3ErrorXML(w, "MalformedXML", "The XML you provided was not well-formed",
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
	}
	if createConfig.Bucket.Type == "Directory" {
		if !s3ApplyDirectoryBucketConfiguration(w, r, &b, createConfig.Location.Type,
			createConfig.Location.Name, createConfig.Bucket.DataRedundancy) {
			return
		}
	}
	s3Buckets_.Put(bucket, b)
	if len(createConfig.Tags) > 0 {
		var body strings.Builder
		body.WriteString(`<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet>`)
		sort.Slice(createConfig.Tags, func(i, j int) bool {
			return createConfig.Tags[i].Key < createConfig.Tags[j].Key
		})
		for _, tag := range createConfig.Tags {
			fmt.Fprintf(&body, "<Tag><Key>%s</Key><Value>%s</Value></Tag>",
				xmlEscape(tag.Key), xmlEscape(tag.Value))
		}
		body.WriteString(`</TagSet></Tagging>`)
		configKey := s3BucketConfigKeyID(bucket, "tagging", "")
		s3BucketConfigs.Put(configKey, S3BucketConfig{
			Key: configKey, Bucket: bucket, Subresource: "tagging",
			Body: []byte(body.String()), ContentType: "application/xml",
		})
	}

	w.Header().Set("Location", "/"+bucket)
	w.WriteHeader(http.StatusOK)
}

func handleS3HeadBucket(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")

	if _, ok := s3Buckets_.Get(bucket); !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// HeadBucket answers with the bucket's Region — the header a client that
	// reached the wrong regional endpoint reads to learn where the bucket is.
	w.Header().Set("x-amz-bucket-region", awsRegion())
	w.WriteHeader(http.StatusOK)
}

func handleS3DeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")

	if _, ok := s3Buckets_.Get(bucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	// Check if bucket is empty
	objects := s3Objects.Filter(func(obj S3Object) bool {
		return strings.HasPrefix(obj.Key, bucket+"/") || obj.Key == bucket+"/"
	})
	if len(objects) > 0 {
		sim.S3ErrorXML(w, "BucketNotEmpty", "The bucket you tried to delete is not empty",
			bucket, sim.RequestID(r.Context()), http.StatusConflict)
		return
	}

	s3Buckets_.Delete(bucket)
	for _, config := range s3BucketConfigs.Filter(func(config S3BucketConfig) bool {
		return config.Bucket == bucket
	}) {
		s3BucketConfigs.Delete(config.Key)
	}
	for _, upload := range s3MultipartUploads.Filter(func(upload S3MultipartUpload) bool {
		return upload.Bucket == bucket
	}) {
		s3MultipartUploads.Delete(upload.UploadID)
	}
	s3MetadataConfigs.Delete(bucket)
	s3MetadataTableConfigs.Delete(bucket)
	s3AbacStatus.Delete(bucket)
	for _, annotation := range s3ObjectAnnotations.Filter(func(annotation s3Annotation) bool {
		return strings.HasPrefix(annotation.Owner, bucket+"/")
	}) {
		s3ObjectAnnotations.Delete(s3AnnotationKey(
			bucket,
			strings.TrimPrefix(annotation.Owner, bucket+"/"),
			annotation.Name,
		))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleS3GetBucket dispatches GET /s3/{bucket} based on sub-resource
// query strings. Real S3 uses the query-string (e.g. `?policy`,
// `?versioning`) to differentiate between ListObjects (no query) and the
// various Get* / Describe* sub-resources. terraform-provider-aws fans out
// across many of these on Create+Read to populate the resource's
// attributes; the sim has to mirror real behaviour for each so the
// provider's response parsers don't NPE or mis-decode.
func handleS3GetBucket(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")

	if _, ok := s3Buckets_.Get(bucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	// Sub-resource dispatch. Real S3 treats the presence of any of these
	// keys (with empty or non-empty value) as the action selector — so
	// `r.URL.Query().Has(...)` is the right check, not value-based.
	q := r.URL.Query()
	switch {
	case q.Has("session"):
		handleS3CreateSession(w, r)
		return
	case q.Has("metadataConfiguration"):
		handleS3GetBucketMetadataConfiguration(w, r)
		return
	case q.Has("metadataTable"):
		handleS3GetBucketMetadataTableConfiguration(w, r)
		return
	case q.Has("abac"):
		handleS3GetBucketAbac(w, r)
		return
	case q.Has("policy"):
		emitStoredOr404(w, r, bucket, "policy", "NoSuchBucketPolicy", "The bucket policy does not exist")
		return
	case q.Has("uploads"):
		handleS3ListMultipartUploads(w, r)
		return
	case q.Has("versions"):
		handleS3ListObjectVersions(w, r)
		return
	case q.Has("versioning"):
		emitStoredOrEmptyXML(w, bucket, "versioning", "VersioningConfiguration")
		return
	case q.Has("accelerate"):
		emitStoredOrEmptyXML(w, bucket, "accelerate", "AccelerateConfiguration")
		return
	case q.Has("logging"):
		emitStoredOrEmptyXML(w, bucket, "logging", "BucketLoggingStatus")
		return
	case q.Has("location"):
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` + awsRegion() + `</LocationConstraint>`))
		return
	case q.Has("lifecycle"):
		emitStoredOr404(w, r, bucket, "lifecycle", "NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist")
		return
	case q.Has("cors"):
		emitStoredOr404(w, r, bucket, "cors", "NoSuchCORSConfiguration", "The CORS configuration does not exist")
		return
	case q.Has("website"):
		emitStoredOr404(w, r, bucket, "website", "NoSuchWebsiteConfiguration", "The website configuration does not exist")
		return
	case q.Has("replication"):
		emitStoredOr404(w, r, bucket, "replication", "ReplicationConfigurationNotFoundError", "The replication configuration was not found")
		return
	case q.Has("encryption"):
		emitStoredOr404(w, r, bucket, "encryption", "ServerSideEncryptionConfigurationNotFoundError", "The server side encryption configuration was not found")
		return
	case q.Has("tagging"):
		emitStoredOr404(w, r, bucket, "tagging", "NoSuchTagSet", "The TagSet does not exist")
		return
	case q.Has("policyStatus"):
		// PolicyStatus is derived from the stored bucket policy. Real
		// S3: no policy → 404 NoSuchBucketPolicy; policy present →
		// IsPublic true iff the policy grants any action to
		// Principal:"*" without a matching Condition restricting it.
		// Sim approximation: scan for `"Principal":"*"` or `"AWS":"*"`
		// substrings — catches the common "public read-bucket" shape
		// without parsing the full IAM AST.
		policy, _, _, ok := getStoredBucketSubresource(bucket, "policy")
		if !ok {
			sim.S3ErrorXML(w, "NoSuchBucketPolicy", "The bucket policy does not exist",
				bucket, sim.RequestID(r.Context()), http.StatusNotFound)
			return
		}
		isPublic := strings.Contains(string(policy), `"Principal":"*"`) ||
			strings.Contains(string(policy), `"AWS":"*"`) ||
			strings.Contains(string(policy), `"Principal": "*"`)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w,
			`<?xml version="1.0" encoding="UTF-8"?><PolicyStatus xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsPublic>%t</IsPublic></PolicyStatus>`,
			isPublic)
		return
	case q.Has("publicAccessBlock"):
		emitStoredOr404(w, r, bucket, "publicAccessBlock", "NoSuchPublicAccessBlockConfiguration", "The public access block configuration was not found")
		return
	case q.Has("object-lock"):
		emitStoredOr404(w, r, bucket, "object-lock", "ObjectLockConfigurationNotFoundError", "Object Lock configuration does not exist for this bucket")
		return
	case q.Has("ownershipControls"):
		emitStoredOr404(w, r, bucket, "ownershipControls", "OwnershipControlsNotFoundError", "The bucket ownership controls were not found")
		return
	case q.Has("intelligent-tiering"):
		if id := q.Get("id"); id != "" {
			emitStoredIDOr404(w, r, bucket, "intelligent-tiering", id)
			return
		}
		emitBucketConfigurationList(w, bucket, "intelligent-tiering", "ListBucketIntelligentTieringConfigurationsOutput")
		return
	case q.Has("inventory"):
		if id := q.Get("id"); id != "" {
			emitStoredIDOr404(w, r, bucket, "inventory", id)
			return
		}
		emitBucketConfigurationList(w, bucket, "inventory", "ListInventoryConfigurationsResult")
		return
	case q.Has("analytics"):
		if id := q.Get("id"); id != "" {
			emitStoredIDOr404(w, r, bucket, "analytics", id)
			return
		}
		emitBucketConfigurationList(w, bucket, "analytics", "ListBucketAnalyticsConfigurationResult")
		return
	case q.Has("metrics"):
		if id := q.Get("id"); id != "" {
			emitStoredIDOr404(w, r, bucket, "metrics", id)
			return
		}
		emitBucketConfigurationList(w, bucket, "metrics", "ListMetricsConfigurationsResult")
		return
	case q.Has("requestPayment"):
		if body, ct, _, ok := getStoredBucketSubresource(bucket, "requestPayment"); ok {
			if ct == "" {
				ct = "application/xml"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><RequestPaymentConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Payer>BucketOwner</Payer></RequestPaymentConfiguration>`))
		return
	case q.Has("notification"):
		emitStoredOrEmptyXML(w, bucket, "notification", "NotificationConfiguration")
		return
	case q.Has("acl"):
		// Real S3: GetBucketAcl returns the canonical owner-only ACL
		// even on BucketOwnerEnforced buckets (200, not the 400 that
		// PutBucketAcl returns). terraform-provider-aws's bucket Read
		// reads the ACL regardless of ownership-controls state.
		if body, ct, _, ok := getStoredBucketSubresource(bucket, "acl"); ok {
			if ct == "" {
				ct = "application/xml"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>` + awsAccountID() + `</ID><DisplayName>simulator</DisplayName></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>` + awsAccountID() + `</ID><DisplayName>simulator</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`))
		return
	}

	// No sub-resource → list operation. ListObjectsV2 sets `list-type=2`;
	// the legacy V1 ListObjects omits it and carries a distinct response
	// shape (Marker / NextMarker instead of ContinuationToken).
	if r.URL.Query().Get("list-type") != "2" {
		handleS3ListObjectsV1(w, r, bucket)
		return
	}

	// ListObjectsV2.
	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	continuationToken := r.URL.Query().Get("continuation-token")
	startAfter := r.URL.Query().Get("start-after")
	maxKeysStr := r.URL.Query().Get("max-keys")
	maxKeys := 1000
	if maxKeysStr != "" {
		if _, err := fmt.Sscanf(maxKeysStr, "%d", &maxKeys); err != nil {
			maxKeys = 1000
		}
	}
	if maxKeys < 0 {
		maxKeys = 0
	}

	// Collect objects for this bucket
	bucketPrefix := bucket + "/"
	objects := s3Objects.Filter(func(obj S3Object) bool {
		objKey := obj.Key
		if !strings.HasPrefix(objKey, bucketPrefix) {
			return false
		}
		// Get the key relative to bucket
		relKey := objKey[len(bucketPrefix):]
		if prefix != "" && !strings.HasPrefix(relKey, prefix) {
			return false
		}
		return true
	})

	var contents []s3ObjectInfo
	for _, obj := range objects {
		relKey := obj.Key[len(bucketPrefix):]
		contents = append(contents, s3ObjectInfo{
			Key:          relKey,
			LastModified: obj.LastModified.UTC().Format(time.RFC3339),
			ETag:         obj.ETag,
			Size:         obj.Size,
			StorageClass: "STANDARD",
		})
	}
	if contents == nil {
		contents = []s3ObjectInfo{}
	}
	sort.Slice(contents, func(i, j int) bool {
		return contents[i].Key < contents[j].Key
	})

	cursor := continuationToken
	if cursor == "" {
		cursor = startAfter
	}
	if cursor != "" {
		next := contents[:0]
		for _, obj := range contents {
			if obj.Key > cursor && (delimiter == "" || !strings.HasPrefix(obj.Key, cursor)) {
				next = append(next, obj)
			}
		}
		contents = next
	}

	type listEntry struct {
		key          string
		object       s3ObjectInfo
		commonPrefix string
		isPrefix     bool
	}
	entries := make([]listEntry, 0, len(contents))
	if delimiter != "" {
		prefixes := map[string]bool{}
		for _, obj := range contents {
			rest := strings.TrimPrefix(obj.Key, prefix)
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				cp := prefix + rest[:idx+len(delimiter)]
				if !prefixes[cp] {
					prefixes[cp] = true
					entries = append(entries, listEntry{key: cp, commonPrefix: cp, isPrefix: true})
				}
				continue
			}
			entries = append(entries, listEntry{key: obj.Key, object: obj})
		}
	} else {
		for _, obj := range contents {
			entries = append(entries, listEntry{key: obj.Key, object: obj})
		}
	}

	isTruncated := false
	nextContinuationToken := ""
	if len(entries) > maxKeys {
		if maxKeys > 0 {
			nextContinuationToken = entries[maxKeys-1].key
		}
		entries = entries[:maxKeys]
		isTruncated = true
	}
	contents = contents[:0]
	var commonPrefixes []s3CommonPrefix
	for _, entry := range entries {
		if entry.isPrefix {
			commonPrefixes = append(commonPrefixes, s3CommonPrefix{Prefix: entry.commonPrefix})
			continue
		}
		contents = append(contents, entry.object)
	}
	if contents == nil {
		contents = []s3ObjectInfo{}
	}

	result := s3ListBucketResult{
		Xmlns:                 "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:                  bucket,
		Prefix:                prefix,
		MaxKeys:               maxKeys,
		KeyCount:              len(contents) + len(commonPrefixes),
		IsTruncated:           isTruncated,
		Contents:              contents,
		CommonPrefixes:        commonPrefixes,
		ContinuationToken:     continuationToken,
		NextContinuationToken: nextContinuationToken,
		StartAfter:            startAfter,
	}

	sim.WriteXML(w, http.StatusOK, result)
}

func handleS3PutObject(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")

	if _, ok := s3Buckets_.Get(bucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	// Conditional write (optimistic concurrency): If-None-Match: * fails if the
	// object already exists; If-Match: <etag> fails if the current ETag differs.
	existing, exists := s3Objects.Get(s3ObjectKey(bucket, key))
	if inm := r.Header.Get("If-None-Match"); inm == "*" && exists {
		sim.S3ErrorXML(w, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold",
			key, sim.RequestID(r.Context()), http.StatusPreconditionFailed)
		return
	}
	if im := r.Header.Get("If-Match"); im != "" {
		if !exists || strings.Trim(im, `"`) != strings.Trim(existing.ETag, `"`) {
			sim.S3ErrorXML(w, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold",
				key, sim.RequestID(r.Context()), http.StatusPreconditionFailed)
			return
		}
	}

	defer r.Body.Close()
	// AWS SDKs switch to aws-chunked encoding when the request body
	// is non-seekable (io.Pipe, http.Request.Body, streaming
	// compressors). Real S3 unwraps that framing server-side; the
	// sim must do the same or it stores the chunk envelope verbatim.
	// Sentinel: Content-Encoding: aws-chunked, x-amz-content-sha256:
	// STREAMING-*, or x-amz-decoded-content-length present.
	var bodyReader io.Reader = r.Body
	if isAWSChunkedRequest(r.Header) {
		bodyReader = newAWSChunkedReader(r.Body)
	}
	body, err := io.ReadAll(bodyReader)
	if err != nil {
		sim.S3ErrorXML(w, "InternalError", "Failed to read request body: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusInternalServerError)
		return
	}

	contentType := r.Header.Get("Content-Type")

	// Collect user metadata from x-amz-meta-* headers
	metadata := make(map[string]string)
	for k, v := range r.Header {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-meta-") && len(v) > 0 {
			metaKey := strings.TrimPrefix(lower, "x-amz-meta-")
			metadata[metaKey] = v[0]
		}
	}

	obj, err := s3PutServiceObject(bucket, key, body, contentType, metadata)
	if err != nil {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	w.Header().Set("ETag", obj.ETag)
	w.WriteHeader(http.StatusOK)
}

func handleS3GetObject(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")

	storeKey := s3ObjectKey(bucket, key)
	obj, ok := s3Objects.Get(storeKey)
	if !ok {
		sim.S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", obj.ContentType)
	w.Header().Set("ETag", obj.ETag)
	w.Header().Set("Last-Modified", obj.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", obj.Size))

	for k, v := range obj.Metadata {
		w.Header().Set("x-amz-meta-"+k, v)
	}
	s3SetObjectEncryptionHeaders(w, obj)

	http.ServeContent(w, r, key, obj.LastModified, bytes.NewReader(obj.Data))
}

func handleS3HeadObject(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")

	storeKey := s3ObjectKey(bucket, key)
	obj, ok := s3Objects.Get(storeKey)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", obj.ContentType)
	w.Header().Set("ETag", obj.ETag)
	w.Header().Set("Last-Modified", obj.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", obj.Size))

	for k, v := range obj.Metadata {
		w.Header().Set("x-amz-meta-"+k, v)
	}
	s3SetObjectEncryptionHeaders(w, obj)

	w.WriteHeader(http.StatusOK)
}

// s3SetObjectEncryptionHeaders emits the x-amz-server-side-encryption
// response headers for an object whose SSE was set by
// UpdateObjectEncryption.
func s3SetObjectEncryptionHeaders(w http.ResponseWriter, obj S3Object) {
	if obj.SSEAlgorithm == "" {
		return
	}
	w.Header().Set("x-amz-server-side-encryption", obj.SSEAlgorithm)
	if obj.SSEKMSKeyID != "" {
		w.Header().Set("x-amz-server-side-encryption-aws-kms-key-id", obj.SSEKMSKeyID)
	}
}

func handleS3DeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")

	storeKey := s3ObjectKey(bucket, key)
	existing, existed := s3Objects.Get(storeKey)
	s3Objects.Delete(storeKey)
	s3ObjectTags.Delete(storeKey)
	s3DeleteObjectAnnotations(bucket, key)

	if existed {
		s3FireObjectNotifications(bucket, key, "ObjectRemoved:Delete", existing.ETag, existing.Size)
	}

	// S3 returns 204 even if the object didn't exist
	w.WriteHeader(http.StatusNoContent)
}
