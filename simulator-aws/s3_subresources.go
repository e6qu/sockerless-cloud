package main

import (
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// S3MultipartUpload tracks an in-flight multipart upload between
// InitiateMultipartUpload and CompleteMultipartUpload. Each Part is
// persisted and stitched together on Complete.
type S3MultipartUpload struct {
	UploadID    string
	Bucket      string
	Key         string
	ContentType string
	Initiated   time.Time
	Parts       map[int]s3MultipartPart // partNumber → bytes+etag
}

type s3MultipartPart struct {
	Data []byte
	ETag string
}

var (
	s3MultipartUploads sim.Store[S3MultipartUpload]
	s3ObjectTags       sim.Store[map[string]string] // "bucket/key" → tag map
)

// handleS3PostObjectDispatch routes POST /{bucket}/{key...} based on
// the canonical S3 subresource query strings. The wildcard pattern
// `POST /{bucket}/{key...}` is the most-greedy POST route on the
// AWS sim's collapsed-port mux; without a known-bucket gate it
// would shadow any other AWS service whose POST path happens to
// share the 2+-segment shape (API Gateway v2
// `POST /v2/apis/{id}/deployments`). The gate routes to NotFound
// when the first segment isn't a registered bucket so the SDK
// surfaces a real 404 instead of an S3-shaped InvalidRequest.
func handleS3PostObjectDispatch(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	switch {
	case q.Has("uploads"):
		handleS3InitiateMultipart(w, r)
	case q.Has("uploadId"):
		handleS3CompleteMultipart(w, r)
	case q.Has("restore"):
		handleS3RestoreObject(w, r)
	case q.Has("select"):
		handleS3SelectObjectContent(w, r)
	default:
		sim.S3ErrorXML(w, "InvalidRequest",
			"POST on an object requires ?uploads (InitiateMultipartUpload), ?uploadId (CompleteMultipartUpload), ?restore (RestoreObject), or ?select (SelectObjectContent)",
			"", sim.RequestID(r.Context()), http.StatusBadRequest)
	}
}

// handleS3PostBucketDispatch handles bucket-level POSTs:
// `?uploads` lists in-flight multipart uploads, `?delete` is the
// multi-object delete (already covered elsewhere; surface a friendly
// 400 if reached without that path).
func handleS3PostBucketDispatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Has("delete"):
		// Multi-object delete: parse <Delete><Object><Key>... XML, delete
		// each, return <DeleteResult><Deleted>...</Deleted></DeleteResult>.
		handleS3MultiObjectDelete(w, r)
	case q.Has("metadataConfiguration"):
		handleS3CreateBucketMetadataConfiguration(w, r)
	case q.Has("metadataTable"):
		handleS3CreateBucketMetadataTableConfiguration(w, r)
	default:
		sim.S3ErrorXML(w, "InvalidRequest",
			"POST on a bucket requires ?delete, ?metadataConfiguration, or ?metadataTable",
			sim.PathParam(r, "bucket"), sim.RequestID(r.Context()),
			http.StatusBadRequest)
	}
}

// handleS3PutObjectDispatch routes PUT /{bucket}/{key...} based on the
// special headers and subresource query strings. CopyObject is
// signaled by `x-amz-copy-source`; UploadPart by `?uploadId` + `?partNumber`;
// PutObjectTagging by `?tagging`; otherwise PutObject. Known-bucket
// gate (see handleS3PostObjectDispatch for rationale).
func handleS3PutObjectDispatch(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if q.Has("renameObject") {
		handleS3RenameObject(w, r)
		return
	}
	if q.Has("encryption") {
		handleS3UpdateObjectEncryption(w, r)
		return
	}
	if r.Header.Get("x-amz-copy-source") != "" && q.Has("uploadId") && q.Has("partNumber") {
		handleS3UploadPartCopy(w, r)
		return
	}
	if r.Header.Get("x-amz-copy-source") != "" {
		handleS3CopyObject(w, r)
		return
	}
	switch {
	case q.Has("uploadId") && q.Has("partNumber"):
		handleS3UploadPart(w, r)
	case q.Has("annotation"):
		handleS3PutObjectAnnotation(w, r)
	case q.Has("tagging"):
		handleS3PutObjectTagging(w, r)
	case q.Has("acl"):
		handleS3PutObjectAcl(w, r)
	case q.Has("legal-hold"):
		handleS3PutObjectLegalHold(w, r)
	case q.Has("retention"):
		handleS3PutObjectRetention(w, r)
	default:
		handleS3PutObject(w, r)
	}
}

// handleS3GetOrHeadObjectDispatch routes GET / HEAD /{bucket}/{key...}
// based on subresource query strings. Known-bucket gate.
func handleS3GetOrHeadObjectDispatch(w http.ResponseWriter, r *http.Request) {
	if s3ServeObjectLambdaRead(w, r) {
		return
	}
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	switch {
	case q.Has("uploadId"):
		handleS3ListParts(w, r)
	case q.Has("annotation"):
		if q.Get("annotationName") != "" {
			handleS3GetObjectAnnotation(w, r)
		} else {
			handleS3ListObjectAnnotations(w, r)
		}
	case q.Has("tagging"):
		handleS3GetObjectTagging(w, r)
	case q.Has("acl"):
		handleS3GetObjectAcl(w, r)
	case q.Has("attributes"):
		handleS3GetObjectAttributes(w, r)
	case q.Has("legal-hold"):
		handleS3GetObjectLegalHold(w, r)
	case q.Has("retention"):
		handleS3GetObjectRetention(w, r)
	case q.Has("torrent"):
		handleS3GetObjectTorrent(w, r)
	default:
		handleS3GetOrHeadObject(w, r)
	}
}

// handleS3DeleteObjectDispatch routes DELETE /{bucket}/{key...} based on
// subresource query strings. Known-bucket gate.
func handleS3DeleteObjectDispatch(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	switch {
	case q.Has("uploadId"):
		handleS3AbortMultipart(w, r)
	case q.Has("annotation"):
		handleS3DeleteObjectAnnotation(w, r)
	case q.Has("tagging"):
		handleS3DeleteObjectTagging(w, r)
	default:
		handleS3DeleteObject(w, r)
	}
}

// ── Multipart upload ─────────────────────────────────────────────────

func handleS3InitiateMultipart(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	uploadID := generateUUID()
	contentType := r.Header.Get("Content-Type")
	s3MultipartUploads.Put(uploadID, S3MultipartUpload{
		UploadID:    uploadID,
		Bucket:      bucket,
		Key:         key,
		ContentType: contentType,
		Initiated:   time.Now().UTC(),
		Parts:       map[int]s3MultipartPart{},
	})
	result := struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		Xmlns    string   `xml:"xmlns,attr"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		UploadID string   `xml:"UploadId"`
	}{
		Xmlns:    "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket:   bucket,
		Key:      key,
		UploadID: uploadID,
	}
	sim.WriteXML(w, http.StatusOK, result)
}

func handleS3UploadPart(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("uploadId")
	partNumStr := r.URL.Query().Get("partNumber")
	var partNum int
	// Malformed partNumber leaves partNum=0; the [1, 10000] bounds
	// check below catches both empty/garbage input and out-of-range
	// values with the same canonical InvalidArgument response.
	_, _ = fmt.Sscanf(partNumStr, "%d", &partNum)
	if partNum < 1 || partNum > 10000 {
		sim.S3ErrorXML(w, "InvalidArgument",
			"Part number must be between 1 and 10000",
			sim.PathParam(r, "bucket"), sim.RequestID(r.Context()),
			http.StatusBadRequest)
		return
	}

	mp, ok := s3MultipartUploads.Get(uploadID)
	if !ok {
		sim.S3ErrorXML(w, "NoSuchUpload",
			"The specified multipart upload does not exist",
			sim.PathParam(r, "bucket"), sim.RequestID(r.Context()),
			http.StatusNotFound)
		return
	}

	defer r.Body.Close()
	var bodyReader io.Reader = r.Body
	if isAWSChunkedRequest(r.Header) {
		bodyReader = newAWSChunkedReader(r.Body)
	}
	body, err := io.ReadAll(bodyReader)
	if err != nil {
		sim.S3ErrorXML(w, "InternalError", "Failed to read part body: "+err.Error(),
			mp.Bucket, sim.RequestID(r.Context()), http.StatusInternalServerError)
		return
	}
	hash := md5.Sum(body)
	etag := fmt.Sprintf(`"%x"`, hash)

	s3MultipartUploads.Update(uploadID, func(upload *S3MultipartUpload) {
		upload.Parts[partNum] = s3MultipartPart{Data: body, ETag: etag}
	})
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func handleS3CompleteMultipart(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("uploadId")
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")

	mp, ok := s3MultipartUploads.Get(uploadID)
	if !ok {
		sim.S3ErrorXML(w, "NoSuchUpload",
			"The specified multipart upload does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	// Parse the <CompleteMultipartUpload><Part>...</Part></CompleteMultipartUpload>
	// XML to learn the order in which to stitch parts. Real S3 verifies
	// the client-supplied ETag matches the stored one; the sim does too.
	var req struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		} `xml:"Part"`
	}
	defer r.Body.Close()
	// CompleteMultipartUpload body is a small fixed-shape XML
	// document listing part numbers + ETags. aws-sdk-go-v2 sends it
	// without aws-chunked or gzip framing (no streaming-envelope
	// header set), so a direct `io.ReadAll` is wire-faithful.
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		sim.S3ErrorXML(w, "IncompleteBody",
			"Failed to read request body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if err := xml.Unmarshal(rawBody, &req); err != nil {
		sim.S3ErrorXML(w, "MalformedXML",
			"Failed to parse CompleteMultipartUpload body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if len(req.Parts) == 0 {
		sim.S3ErrorXML(w, "MalformedXML",
			"CompleteMultipartUpload requires at least one Part",
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}

	var assembled []byte
	// partMD5s accumulates the raw 16-byte MD5 digests of each part
	// in part-number order; the final ETag hashes their concatenation.
	partMD5s := make([]byte, 0, len(req.Parts)*md5.Size)
	for _, p := range req.Parts {
		part, ok := mp.Parts[p.PartNumber]
		if !ok {
			sim.S3ErrorXML(w, "InvalidPart",
				fmt.Sprintf("Part number %d not found", p.PartNumber),
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
		// Real S3 compares part ETags ignoring the surrounding quotes — the
		// `aws s3api` CLI's shorthand parser strips them, while the SDK
		// preserves them; both must validate against the stored quoted ETag.
		if p.ETag != "" && strings.Trim(p.ETag, `"`) != strings.Trim(part.ETag, `"`) {
			sim.S3ErrorXML(w, "InvalidPart",
				fmt.Sprintf("ETag mismatch for part %d: got %s want %s", p.PartNumber, p.ETag, part.ETag),
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
		assembled = append(assembled, part.Data...)
		partHash := md5.Sum(part.Data)
		partMD5s = append(partMD5s, partHash[:]...)
	}

	// S3 multipart ETag: `"<hex(md5(concat(part_md5_bytes)))>-<numParts>"`.
	// Distinct from a single-shot PUT ETag (hex(md5(body))) — SDKs
	// and the `aws s3api` CLI use the `-N` suffix as a multipart marker.
	finalHash := md5.Sum(partMD5s)
	finalETag := fmt.Sprintf(`"%x-%d"`, finalHash, len(req.Parts))

	obj := S3Object{
		Key:          key,
		Data:         assembled,
		Size:         int64(len(assembled)),
		ETag:         finalETag,
		ContentType:  mp.ContentType,
		LastModified: time.Now().UTC(),
	}
	s3Objects.Put(bucket+"/"+key, obj)
	s3MultipartUploads.Delete(uploadID)

	// The Location field is the real-AWS canonical
	// `https://<bucket>.s3.amazonaws.com/<key>` URL that the SDK
	// surfaces as the completed-upload location. aws-sdk-go-v2's
	// high-level Uploader and terraform-provider-aws treat it as
	// advertised metadata (bucket+key are the load-bearing fields
	// for subsequent operations); the sim emits the canonical shape
	// for fidelity even though the *.s3.amazonaws.com subdomain
	// resolves to real S3, not the sim.
	result := struct {
		XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
		Xmlns    string   `xml:"xmlns,attr"`
		Location string   `xml:"Location"` // external: real-AWS canonical *.s3.amazonaws.com URL
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		ETag     string   `xml:"ETag"`
	}{
		Xmlns:    "http://s3.amazonaws.com/doc/2006-03-01/",
		Location: fmt.Sprintf("https://%s.s3.amazonaws.com/%s", bucket, key),
		Bucket:   bucket,
		Key:      key,
		ETag:     finalETag,
	}
	sim.WriteXML(w, http.StatusOK, result)
}

func handleS3AbortMultipart(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("uploadId")
	ok := s3MultipartUploads.Delete(uploadID)
	if !ok {
		sim.S3ErrorXML(w, "NoSuchUpload",
			"The specified multipart upload does not exist",
			sim.PathParam(r, "bucket"), sim.RequestID(r.Context()),
			http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleS3ListMultipartUploads(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	q := r.URL.Query()
	prefix := q.Get("prefix")
	keyMarker := q.Get("key-marker")
	uploadIDMarker := q.Get("upload-id-marker")
	maxUploads := parsePositiveQueryInt(q.Get("max-uploads"), 1000)

	type uploadXML struct {
		Key          string  `xml:"Key"`
		UploadID     string  `xml:"UploadId"`
		Initiator    s3Owner `xml:"Initiator"`
		Owner        s3Owner `xml:"Owner"`
		StorageClass string  `xml:"StorageClass"`
		Initiated    string  `xml:"Initiated"`
	}
	type uploadEntry struct {
		key       string
		uploadID  string
		initiated time.Time
	}

	var entries []uploadEntry
	for _, upload := range s3MultipartUploads.List() {
		if upload.Bucket != bucket {
			continue
		}
		if prefix != "" && !strings.HasPrefix(upload.Key, prefix) {
			continue
		}
		if keyMarker != "" {
			switch {
			case upload.Key < keyMarker:
				continue
			case upload.Key == keyMarker && upload.UploadID <= uploadIDMarker:
				continue
			}
		}
		entries = append(entries, uploadEntry{
			key:       upload.Key,
			uploadID:  upload.UploadID,
			initiated: upload.Initiated,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key == entries[j].key {
			return entries[i].initiated.Before(entries[j].initiated)
		}
		return entries[i].key < entries[j].key
	})

	result := struct {
		XMLName            xml.Name    `xml:"ListMultipartUploadsResult"`
		Xmlns              string      `xml:"xmlns,attr"`
		Bucket             string      `xml:"Bucket"`
		KeyMarker          string      `xml:"KeyMarker,omitempty"`
		UploadIDMarker     string      `xml:"UploadIdMarker,omitempty"`
		NextKeyMarker      string      `xml:"NextKeyMarker,omitempty"`
		NextUploadIDMarker string      `xml:"NextUploadIdMarker,omitempty"`
		MaxUploads         int         `xml:"MaxUploads"`
		IsTruncated        bool        `xml:"IsTruncated"`
		Uploads            []uploadXML `xml:"Upload"`
	}{
		Xmlns:          "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket:         bucket,
		KeyMarker:      keyMarker,
		UploadIDMarker: uploadIDMarker,
		MaxUploads:     maxUploads,
	}

	limit := len(entries)
	if maxUploads > 0 && limit > maxUploads {
		limit = maxUploads
		result.IsTruncated = true
		result.NextKeyMarker = entries[limit-1].key
		result.NextUploadIDMarker = entries[limit-1].uploadID
	}
	owner := s3Owner{ID: awsAccountID(), DisplayName: "simulator"}
	for _, entry := range entries[:limit] {
		result.Uploads = append(result.Uploads, uploadXML{
			Key:          entry.key,
			UploadID:     entry.uploadID,
			Initiator:    owner,
			Owner:        owner,
			StorageClass: "STANDARD",
			Initiated:    entry.initiated.UTC().Format(time.RFC3339),
		})
	}

	sim.WriteXML(w, http.StatusOK, result)
}

// handleS3ListParts returns the set of uploaded parts for an in-flight
// multipart upload. aws-sdk-go-v2's `manager.Uploader` calls
// `ListParts` on every retry path to learn which part numbers have
// already been uploaded and skip re-sending those — without this
// route, the retry path issues UploadPart for every part on every
// retry (correctness-affecting; not just slow).
func handleS3ListParts(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	uploadID := r.URL.Query().Get("uploadId")
	partNumberMarker := parsePositiveQueryInt(r.URL.Query().Get("part-number-marker"), 0)
	maxParts := parsePositiveQueryInt(r.URL.Query().Get("max-parts"), 1000)

	mp, ok := s3MultipartUploads.Get(uploadID)
	if !ok || mp.Bucket != bucket || mp.Key != key {
		sim.S3ErrorXML(w, "NoSuchUpload",
			"The specified multipart upload does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	type partXML struct {
		PartNumber   int    `xml:"PartNumber"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int    `xml:"Size"`
	}
	result := struct {
		XMLName   xml.Name `xml:"ListPartsResult"`
		Xmlns     string   `xml:"xmlns,attr"`
		Bucket    string   `xml:"Bucket"`
		Key       string   `xml:"Key"`
		UploadID  string   `xml:"UploadId"`
		Initiator struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Initiator"`
		Owner struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
		StorageClass         string    `xml:"StorageClass"`
		PartNumberMarker     int       `xml:"PartNumberMarker"`
		NextPartNumberMarker int       `xml:"NextPartNumberMarker"`
		MaxParts             int       `xml:"MaxParts"`
		IsTruncated          bool      `xml:"IsTruncated"`
		Parts                []partXML `xml:"Part"`
	}{
		Xmlns:                "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket:               bucket,
		Key:                  key,
		UploadID:             uploadID,
		StorageClass:         "STANDARD",
		NextPartNumberMarker: 0,
		PartNumberMarker:     partNumberMarker,
		MaxParts:             maxParts,
		IsTruncated:          false,
	}
	result.Initiator.ID = awsAccountID()
	result.Initiator.DisplayName = "simulator"
	result.Owner.ID = awsAccountID()
	result.Owner.DisplayName = "simulator"

	partNumbers := make([]int, 0, len(mp.Parts))
	for n := range mp.Parts {
		if n > partNumberMarker {
			partNumbers = append(partNumbers, n)
		}
	}
	sort.Ints(partNumbers)
	if maxParts > 0 && len(partNumbers) > maxParts {
		result.IsTruncated = true
		partNumbers = partNumbers[:maxParts]
	}
	for _, n := range partNumbers {
		part := mp.Parts[n]
		result.Parts = append(result.Parts, partXML{
			PartNumber:   n,
			LastModified: mp.Initiated.UTC().Format(time.RFC3339),
			ETag:         part.ETag,
			Size:         len(part.Data),
		})
		if n > result.NextPartNumberMarker {
			result.NextPartNumberMarker = n
		}
	}

	sim.WriteXML(w, http.StatusOK, result)
}

func parsePositiveQueryInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// ── Object tagging ───────────────────────────────────────────────────

func handleS3PutObjectTagging(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	if _, ok := s3Objects.Get(bucket + "/" + key); !ok {
		sim.S3ErrorXML(w, "NoSuchKey", "The specified key does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	var req struct {
		XMLName xml.Name `xml:"Tagging"`
		TagSet  struct {
			Tags []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Tag"`
		} `xml:"TagSet"`
	}
	// Object Tagging body is a fixed-shape XML document; aws-sdk-go-v2
	// sends it without aws-chunked or gzip framing.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.S3ErrorXML(w, "IncompleteBody",
			"Failed to read Tagging body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		sim.S3ErrorXML(w, "MalformedXML",
			"Failed to parse Tagging body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	tags := make(map[string]string, len(req.TagSet.Tags))
	for _, t := range req.TagSet.Tags {
		tags[t.Key] = t.Value
	}
	s3ObjectTags.Put(bucket+"/"+key, tags)
	// PUT Object tagging answers 200, which is what the operation's own
	// smithy.api#http trait declares and what the service returns — unlike the
	// bucket-level subresources beside it, several of which really do answer
	// 204. The body is empty either way, so nothing but the code says which.
	w.WriteHeader(http.StatusOK)
}

func handleS3GetObjectTagging(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	if _, ok := s3Objects.Get(bucket + "/" + key); !ok {
		sim.S3ErrorXML(w, "NoSuchKey", "The specified key does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	tags, _ := s3ObjectTags.Get(bucket + "/" + key)

	type tag struct {
		Key   string `xml:"Key"`
		Value string `xml:"Value"`
	}
	out := struct {
		XMLName xml.Name `xml:"Tagging"`
		Xmlns   string   `xml:"xmlns,attr"`
		TagSet  struct {
			Tags []tag `xml:"Tag"`
		} `xml:"TagSet"`
	}{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
	}
	for k, v := range tags {
		out.TagSet.Tags = append(out.TagSet.Tags, tag{Key: k, Value: v})
	}
	sim.WriteXML(w, http.StatusOK, out)
}

func handleS3DeleteObjectTagging(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	s3ObjectTags.Delete(bucket + "/" + key)
	w.WriteHeader(http.StatusNoContent)
}

// ── CopyObject ───────────────────────────────────────────────────────

func handleS3CopyObject(w http.ResponseWriter, r *http.Request) {
	srcRaw := r.Header.Get("x-amz-copy-source")
	srcRaw = strings.TrimPrefix(srcRaw, "/")
	parts := strings.SplitN(srcRaw, "/", 2)
	if len(parts) != 2 {
		sim.S3ErrorXML(w, "InvalidArgument",
			"x-amz-copy-source must be of the form /<bucket>/<key>",
			"", sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	srcBucket, srcKey := parts[0], parts[1]
	dstBucket := sim.PathParam(r, "bucket")
	dstKey := sim.PathParam(r, "key")

	src, ok := s3Objects.Get(srcBucket + "/" + srcKey)
	if !ok {
		sim.S3ErrorXML(w, "NoSuchKey",
			"The specified source object does not exist",
			srcBucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	if _, ok := s3Buckets_.Get(dstBucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			dstBucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	now := time.Now().UTC()
	dst := S3Object{
		Key:          s3ObjectKey(dstBucket, dstKey),
		Data:         append([]byte(nil), src.Data...),
		Size:         src.Size,
		ETag:         src.ETag,
		ContentType:  src.ContentType,
		LastModified: now,
	}
	s3Objects.Put(dstBucket+"/"+dstKey, dst)
	result := struct {
		XMLName      xml.Name `xml:"CopyObjectResult"`
		Xmlns        string   `xml:"xmlns,attr"`
		ETag         string   `xml:"ETag"`
		LastModified string   `xml:"LastModified"`
	}{
		Xmlns:        "http://s3.amazonaws.com/doc/2006-03-01/",
		ETag:         src.ETag,
		LastModified: now.Format(time.RFC3339),
	}
	sim.WriteXML(w, http.StatusOK, result)
}

// ── Multi-object delete ──────────────────────────────────────────────

func handleS3MultiObjectDelete(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		sim.S3ErrorXML(w, "NoSuchBucket", "The specified bucket does not exist",
			bucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	var req struct {
		XMLName xml.Name `xml:"Delete"`
		Quiet   bool     `xml:"Quiet"`
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
	}
	defer r.Body.Close()
	// Multi-object delete body is a fixed-shape XML document
	// (<Delete><Object>...</Object>...); aws-sdk-go-v2 sends it
	// without aws-chunked or gzip framing.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.S3ErrorXML(w, "IncompleteBody",
			"Failed to read Delete body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		sim.S3ErrorXML(w, "MalformedXML",
			"Failed to parse Delete body: "+err.Error(),
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	type deleted struct {
		Key string `xml:"Key"`
	}
	out := struct {
		XMLName xml.Name  `xml:"DeleteResult"`
		Xmlns   string    `xml:"xmlns,attr"`
		Deleted []deleted `xml:"Deleted"`
	}{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
	}
	for _, o := range req.Objects {
		storeKey := s3ObjectKey(bucket, o.Key)
		s3Objects.Delete(storeKey)
		s3ObjectTags.Delete(storeKey)
		s3DeleteObjectAnnotations(bucket, o.Key)
		if !req.Quiet {
			out.Deleted = append(out.Deleted, deleted{Key: o.Key})
		}
	}
	sim.WriteXML(w, http.StatusOK, out)
}

func handleS3ListObjectVersions(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	prefix := r.URL.Query().Get("prefix")
	bucketPrefix := bucket + "/"
	objects := s3Objects.Filter(func(obj S3Object) bool {
		if !strings.HasPrefix(obj.Key, bucketPrefix) {
			return false
		}
		relKey := obj.Key[len(bucketPrefix):]
		return prefix == "" || strings.HasPrefix(relKey, prefix)
	})
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Key < objects[j].Key
	})

	type owner struct {
		ID          string `xml:"ID"`
		DisplayName string `xml:"DisplayName"`
	}
	type version struct {
		Key          string `xml:"Key"`
		VersionId    string `xml:"VersionId"`
		IsLatest     bool   `xml:"IsLatest"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
		StorageClass string `xml:"StorageClass"`
		Owner        owner  `xml:"Owner"`
	}
	out := struct {
		XMLName         xml.Name  `xml:"ListVersionsResult"`
		Xmlns           string    `xml:"xmlns,attr"`
		Name            string    `xml:"Name"`
		Prefix          string    `xml:"Prefix"`
		KeyMarker       string    `xml:"KeyMarker"`
		VersionIDMarker string    `xml:"VersionIdMarker"`
		MaxKeys         int       `xml:"MaxKeys"`
		IsTruncated     bool      `xml:"IsTruncated"`
		Versions        []version `xml:"Version"`
	}{
		Xmlns:       "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:        bucket,
		Prefix:      prefix,
		MaxKeys:     1000,
		IsTruncated: false,
	}
	for _, obj := range objects {
		out.Versions = append(out.Versions, version{
			Key:          strings.TrimPrefix(obj.Key, bucketPrefix),
			VersionId:    "null",
			IsLatest:     true,
			LastModified: obj.LastModified.UTC().Format(time.RFC3339),
			ETag:         obj.ETag,
			Size:         obj.Size,
			StorageClass: "STANDARD",
			Owner: owner{
				ID:          awsAccountID(),
				DisplayName: "simulator",
			},
		})
	}
	sim.WriteXML(w, http.StatusOK, out)
}
