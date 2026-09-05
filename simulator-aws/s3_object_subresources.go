package main

import (
	"bytes"
	"crypto/md5"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Object-level S3 subresources that ride on the stored S3Object:
// ACL, Object Lock legal-hold / retention, GetObjectAttributes,
// GetObjectTorrent, RestoreObject, and the multipart UploadPartCopy.
// Each is faithful to the real S3 REST wire shape (the Smithy output
// shapes in specs/cloud-api/aws/s3.smithy.json.gz) so aws-sdk-go-v2's
// deserializers and the `aws s3api` CLI parse the responses.

// ── Object ACL ───────────────────────────────────────────────────────

// handleS3PutObjectAcl stores the raw <AccessControlPolicy> body (or the
// canned-ACL header) on the object. PutObjectAcl's modeled output is
// empty except for the RequestCharged header, so the success body is
// empty (200 OK).
func handleS3PutObjectAcl(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	storeKey := s3ObjectKey(bucket, key)
	obj, ok := s3Objects.Get(storeKey)
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		S3ErrorXML(w, "IncompleteBody", "Failed to read ACL body: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	obj.ACL = body
	s3Objects.Put(storeKey, obj)
	w.WriteHeader(http.StatusOK)
}

// handleS3GetObjectAcl returns the object ACL. With no ACL ever set the
// canonical owner-FULL_CONTROL policy is synthesized (real S3 returns
// that default for objects created without an explicit ACL).
func handleS3GetObjectAcl(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	if len(obj.ACL) > 0 {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(obj.ACL)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>` + awsAccountID() + `</ID><DisplayName>simulator</DisplayName></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>` + awsAccountID() + `</ID><DisplayName>simulator</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`))
}

// ── Object Lock legal-hold ───────────────────────────────────────────

func handleS3PutObjectLegalHold(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	storeKey := s3ObjectKey(bucket, key)
	obj, ok := s3Objects.Get(storeKey)
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		S3ErrorXML(w, "IncompleteBody", "Failed to read LegalHold body: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	var req struct {
		XMLName xml.Name `xml:"LegalHold"`
		Status  string   `xml:"Status"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		S3ErrorXML(w, "MalformedXML", "Failed to parse LegalHold body: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	obj.LegalHoldStatus = req.Status
	s3Objects.Put(storeKey, obj)
	w.WriteHeader(http.StatusOK)
}

func handleS3GetObjectLegalHold(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	status := obj.LegalHoldStatus
	if status == "" {
		// No legal hold ever set: real S3 reports OFF.
		status = "OFF"
	}
	// httpPayload member LegalHold serializes as <LegalHold> (its xmlName).
	out := struct {
		XMLName xml.Name `xml:"LegalHold"`
		Xmlns   string   `xml:"xmlns,attr"`
		Status  string   `xml:"Status"`
	}{
		Xmlns:  "http://s3.amazonaws.com/doc/2006-03-01/",
		Status: status,
	}
	WriteXML(w, http.StatusOK, out)
}

// ── Object Lock retention ────────────────────────────────────────────

func handleS3PutObjectRetention(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	storeKey := s3ObjectKey(bucket, key)
	obj, ok := s3Objects.Get(storeKey)
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		S3ErrorXML(w, "IncompleteBody", "Failed to read Retention body: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	var req struct {
		XMLName         xml.Name `xml:"Retention"`
		Mode            string   `xml:"Mode"`
		RetainUntilDate string   `xml:"RetainUntilDate"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		S3ErrorXML(w, "MalformedXML", "Failed to parse Retention body: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	obj.RetentionMode = req.Mode
	obj.RetainUntilDate = req.RetainUntilDate
	s3Objects.Put(storeKey, obj)
	w.WriteHeader(http.StatusOK)
}

func handleS3GetObjectRetention(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	if obj.RetentionMode == "" {
		// No retention configured: real S3 returns 404
		// NoSuchObjectLockConfiguration.
		S3ErrorXML(w, "NoSuchObjectLockConfiguration",
			"The specified object does not have an ObjectLock configuration",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	// httpPayload member Retention serializes as <Retention> (its xmlName);
	// its RetainUntilDate member serializes as <RetainUntilDate>.
	out := struct {
		XMLName         xml.Name `xml:"Retention"`
		Xmlns           string   `xml:"xmlns,attr"`
		Mode            string   `xml:"Mode"`
		RetainUntilDate string   `xml:"RetainUntilDate"`
	}{
		Xmlns:           "http://s3.amazonaws.com/doc/2006-03-01/",
		Mode:            obj.RetentionMode,
		RetainUntilDate: obj.RetainUntilDate,
	}
	WriteXML(w, http.StatusOK, out)
}

// ── GetObjectAttributes ──────────────────────────────────────────────

// handleS3GetObjectAttributes returns the object metadata selected by the
// x-amz-object-attributes header. The modeled output's body members are
// ETag / Checksum / ObjectParts / StorageClass / ObjectSize (the rest are
// header-bound). Real S3 only emits the elements named in the header; the
// sim mirrors that so the response stays a valid subset of the shape.
func handleS3GetObjectAttributes(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	// The x-amz-object-attributes header carries the requested attribute
	// names. aws-sdk-go-v2 serializes the list as repeated header lines
	// (one value each); the CLI / botocore comma-joins them into one. Honor
	// both by splitting every header value on commas.
	want := map[string]bool{}
	for _, hv := range r.Header.Values("x-amz-object-attributes") {
		for _, a := range strings.Split(hv, ",") {
			if a = strings.TrimSpace(a); a != "" {
				want[a] = true
			}
		}
	}

	w.Header().Set("Last-Modified", obj.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Type", "application/xml")

	// The output shape's root xmlName is GetObjectAttributesResponse.
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<GetObjectAttributesResponse xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	if want["ETag"] {
		// The ETag attribute is the bare hex (no surrounding quotes),
		// unlike the ETag response header.
		fmt.Fprintf(&b, "<ETag>%s</ETag>", strings.Trim(obj.ETag, `"`))
	}
	if want["StorageClass"] {
		b.WriteString("<StorageClass>STANDARD</StorageClass>")
	}
	if want["ObjectSize"] {
		fmt.Fprintf(&b, "<ObjectSize>%d</ObjectSize>", obj.Size)
	}
	b.WriteString(`</GetObjectAttributesResponse>`)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// ── GetObjectTorrent ─────────────────────────────────────────────────

// handleS3GetObjectTorrent returns the BitTorrent metainfo for the object.
// The output's sole body member is the streaming Body blob (httpPayload),
// so the response is raw bytes — the sim returns a minimal, well-formed
// bencoded torrent dictionary referencing the object so SDK/CLI parse a
// non-empty body without error.
func handleS3GetObjectTorrent(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	// Bencoded metainfo: a dict with the object length and name. This is
	// a faithful (minimal) BitTorrent metainfo document, not XML — the
	// httpPayload Body member carries opaque bytes.
	torrent := fmt.Sprintf("d4:infod6:lengthi%de4:name%d:%see",
		obj.Size, len(key), key)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(torrent)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(torrent))
}

// ── RestoreObject ────────────────────────────────────────────────────

// handleS3RestoreObject initiates a restore of an archived object. Real
// S3 returns 202 Accepted for a newly-initiated restore and 200 OK when a
// restore is already in progress; RestoreObject's modeled output carries
// only header-bound members, so the body is empty.
func handleS3RestoreObject(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	storeKey := s3ObjectKey(bucket, key)
	obj, ok := s3Objects.Get(storeKey)
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	// Drain the RestoreRequest body (Days / GlacierJobParameters / Tier);
	// the sim doesn't model storage tiers, so the request is accepted and
	// the object marked restored.
	_, _ = io.ReadAll(r.Body)

	status := http.StatusAccepted
	if obj.RestoreRequested {
		// A restore was already requested for this object.
		status = http.StatusOK
	}
	obj.RestoreRequested = true
	obj.RestoreInProgress = false
	obj.RestoreExpiryDate = time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	s3Objects.Put(storeKey, obj)
	w.WriteHeader(status)
}

// ── UploadPartCopy ───────────────────────────────────────────────────

// handleS3UploadPartCopy copies a byte range from the x-amz-copy-source
// object into a multipart upload part — UploadPart + CopyObject combined.
// The output's CopyPartResult httpPayload carries the part ETag and last
// modified time.
func handleS3UploadPartCopy(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("uploadId")
	partNumStr := r.URL.Query().Get("partNumber")
	var partNum int
	_, _ = fmt.Sscanf(partNumStr, "%d", &partNum)
	if partNum < 1 || partNum > 10000 {
		S3ErrorXML(w, "InvalidArgument", "Part number must be between 1 and 10000",
			sim.PathParam(r, "bucket"), sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}

	srcRaw := strings.TrimPrefix(r.Header.Get("x-amz-copy-source"), "/")
	srcParts := strings.SplitN(srcRaw, "/", 2)
	if len(srcParts) != 2 {
		S3ErrorXML(w, "InvalidArgument",
			"x-amz-copy-source must be of the form /<bucket>/<key>",
			"", sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	srcBucket, srcKey := srcParts[0], srcParts[1]
	src, ok := s3Objects.Get(srcBucket + "/" + srcKey)
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified source object does not exist",
			srcBucket, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	_, ok = s3MultipartUploads.Get(uploadID)
	if !ok {
		S3ErrorXML(w, "NoSuchUpload", "The specified multipart upload does not exist",
			sim.PathParam(r, "bucket"), sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	// Optional x-amz-copy-source-range: bytes=<start>-<end> (inclusive).
	data := src.Data
	if rng := r.Header.Get("x-amz-copy-source-range"); rng != "" {
		start, end, ok := parseCopySourceRange(rng, int64(len(src.Data)))
		if !ok {
			S3ErrorXML(w, "InvalidArgument",
				"The x-amz-copy-source-range value must be of the form bytes=first-last",
				"", sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
		data = src.Data[start : end+1]
	}

	part := append([]byte(nil), data...)
	hash := md5.Sum(part)
	etag := fmt.Sprintf(`"%x"`, hash)
	now := time.Now().UTC()

	s3MultipartUploads.Update(uploadID, func(upload *S3MultipartUpload) {
		upload.Parts[partNum] = s3MultipartPart{Data: part, ETag: etag}
	})

	// httpPayload member CopyPartResult serializes as <CopyPartResult>.
	out := struct {
		XMLName      xml.Name `xml:"CopyPartResult"`
		Xmlns        string   `xml:"xmlns,attr"`
		ETag         string   `xml:"ETag"`
		LastModified string   `xml:"LastModified"`
	}{
		Xmlns:        "http://s3.amazonaws.com/doc/2006-03-01/",
		ETag:         etag,
		LastModified: now.Format(time.RFC3339),
	}
	WriteXML(w, http.StatusOK, out)
}

// parseCopySourceRange parses a `bytes=first-last` range header against the
// object length, returning the inclusive [start, end] byte offsets.
func parseCopySourceRange(rng string, length int64) (int64, int64, bool) {
	spec, ok := strings.CutPrefix(rng, "bytes=")
	if !ok {
		return 0, 0, false
	}
	firstStr, lastStr, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, false
	}
	var first, last int64
	if _, err := fmt.Sscanf(firstStr, "%d", &first); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(lastStr, "%d", &last); err != nil {
		return 0, 0, false
	}
	if first < 0 || last < first || last >= length {
		return 0, 0, false
	}
	return first, last, true
}

// ── ListObjects (V1) ─────────────────────────────────────────────────

type s3ListBucketResultV1 struct {
	XMLName        xml.Name         `xml:"ListBucketResult"`
	Xmlns          string           `xml:"xmlns,attr"`
	Name           string           `xml:"Name"`
	Prefix         string           `xml:"Prefix"`
	Marker         string           `xml:"Marker"`
	NextMarker     string           `xml:"NextMarker,omitempty"`
	MaxKeys        int              `xml:"MaxKeys"`
	Delimiter      string           `xml:"Delimiter,omitempty"`
	IsTruncated    bool             `xml:"IsTruncated"`
	Contents       []s3ObjectInfo   `xml:"Contents"`
	CommonPrefixes []s3CommonPrefix `xml:"CommonPrefixes,omitempty"`
}

// handleS3ListObjectsV1 implements the legacy ListObjects (V1) operation.
// It mirrors ListObjectsV2's filtering/delimiter/pagination logic but uses
// the V1 response shape: Marker (request cursor) and NextMarker (the
// continuation cursor when truncated) replace ContinuationToken, and there
// is no KeyCount.
func handleS3ListObjectsV1(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	marker := q.Get("marker")
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &maxKeys); err != nil {
			maxKeys = 1000
		}
	}
	if maxKeys < 0 {
		maxKeys = 0
	}

	bucketPrefix := bucket + "/"
	objects := s3Objects.Filter(func(obj S3Object) bool {
		if !strings.HasPrefix(obj.Key, bucketPrefix) {
			return false
		}
		relKey := obj.Key[len(bucketPrefix):]
		return prefix == "" || strings.HasPrefix(relKey, prefix)
	})

	var contents []s3ObjectInfo
	for _, obj := range objects {
		contents = append(contents, s3ObjectInfo{
			Key:          obj.Key[len(bucketPrefix):],
			LastModified: obj.LastModified.UTC().Format(time.RFC3339),
			ETag:         obj.ETag,
			Size:         obj.Size,
			StorageClass: "STANDARD",
		})
	}
	sort.Slice(contents, func(i, j int) bool {
		return contents[i].Key < contents[j].Key
	})

	if marker != "" {
		next := contents[:0]
		for _, obj := range contents {
			if obj.Key > marker {
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
	nextMarker := ""
	if len(entries) > maxKeys {
		if maxKeys > 0 {
			nextMarker = entries[maxKeys-1].key
		}
		entries = entries[:maxKeys]
		isTruncated = true
	}

	var outContents []s3ObjectInfo
	var commonPrefixes []s3CommonPrefix
	for _, entry := range entries {
		if entry.isPrefix {
			commonPrefixes = append(commonPrefixes, s3CommonPrefix{Prefix: entry.commonPrefix})
			continue
		}
		outContents = append(outContents, entry.object)
	}
	if outContents == nil {
		outContents = []s3ObjectInfo{}
	}

	result := s3ListBucketResultV1{
		Xmlns:          "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:           bucket,
		Prefix:         prefix,
		Marker:         marker,
		MaxKeys:        maxKeys,
		Delimiter:      delimiter,
		IsTruncated:    isTruncated,
		Contents:       outContents,
		CommonPrefixes: commonPrefixes,
	}
	// NextMarker is only meaningful when a delimiter is in play, but real
	// S3 also returns it for a truncated non-delimited listing as a
	// convenience; emit it whenever truncated.
	if isTruncated {
		result.NextMarker = nextMarker
	}
	WriteXML(w, http.StatusOK, result)
}

// ── SelectObjectContent ──────────────────────────────────────────────
//
// SelectObjectContent (POST /{bucket}/{key}?select&select-type=2) runs an
// SQL query over a stored object's bytes and streams the result as the
// SelectObjectContentEventStream — an application/vnd.amazon.eventstream
// HTTP body that aws-sdk-go-v2's eventstream decoder reassembles natively.
//
// The wire framing matches real S3 exactly:
//   - one or more Records events (:event-type=Records,
//     :content-type=application/octet-stream) whose payload is the selected
//     rows serialized per OutputSerialization;
//   - one Stats event (:event-type=Stats, :content-type=text/xml) carrying a
//     <Stats> XML document with real BytesScanned/BytesProcessed/BytesReturned;
//   - one terminal End event (:event-type=End).
// Every frame carries :message-type=event.
//
// Supported SQL subset: `SELECT * FROM S3Object[ alias]` (case-insensitive).
// This faithfully echoes every input row, reserialized into the requested
// OutputSerialization, over both CSV and JSON-Lines input. Projections
// (`SELECT s._1, s._2 …`) and WHERE filtering are NOT evaluated — rather than
// fabricate rows that don't match the query, the sim returns a real
// S3-shaped error for any expression it can't honor faithfully.

type selectInputSerialization struct {
	XMLName xml.Name `xml:"InputSerialization"`
	CSV     *struct {
		FileHeaderInfo string `xml:"FileHeaderInfo"`
		RecordDelim    string `xml:"RecordDelimiter"`
		FieldDelim     string `xml:"FieldDelimiter"`
		QuoteChar      string `xml:"QuoteCharacter"`
	} `xml:"CSV"`
	JSON *struct {
		Type string `xml:"Type"`
	} `xml:"JSON"`
	CompressionType string `xml:"CompressionType"`
}

type selectOutputSerialization struct {
	XMLName xml.Name `xml:"OutputSerialization"`
	CSV     *struct {
		FieldDelim  string `xml:"FieldDelimiter"`
		RecordDelim string `xml:"RecordDelimiter"`
		QuoteChar   string `xml:"QuoteCharacter"`
	} `xml:"CSV"`
	JSON *struct {
		RecordDelim string `xml:"RecordDelimiter"`
	} `xml:"JSON"`
}

type selectObjectContentRequest struct {
	XMLName             xml.Name                  `xml:"SelectObjectContentRequest"`
	Expression          string                    `xml:"Expression"`
	ExpressionType      string                    `xml:"ExpressionType"`
	InputSerialization  selectInputSerialization  `xml:"InputSerialization"`
	OutputSerialization selectOutputSerialization `xml:"OutputSerialization"`
}

// selectStatsXML is the <Stats> payload of the Stats event. The field names
// and order mirror com.amazonaws.s3#Stats so aws-sdk-go-v2's XML deserializer
// reads BytesScanned/BytesProcessed/BytesReturned.
type selectStatsXML struct {
	XMLName        xml.Name `xml:"Stats"`
	BytesScanned   int64    `xml:"BytesScanned"`
	BytesProcessed int64    `xml:"BytesProcessed"`
	BytesReturned  int64    `xml:"BytesReturned"`
}

func handleS3SelectObjectContent(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	storeKey := s3ObjectKey(bucket, key)
	obj, ok := s3Objects.Get(storeKey)
	if !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		S3ErrorXML(w, "InvalidRequest", "failed to read request body: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	var req selectObjectContentRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		S3ErrorXML(w, "MalformedXML", "the SelectObjectContentRequest XML is malformed: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if req.ExpressionType != "" && !strings.EqualFold(req.ExpressionType, "SQL") {
		S3ErrorXML(w, "InvalidExpressionType",
			"The ExpressionType is not valid. Only SQL expressions are supported.",
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if req.InputSerialization.CompressionType != "" &&
		!strings.EqualFold(req.InputSerialization.CompressionType, "NONE") {
		S3ErrorXML(w, "InvalidCompressionFormat",
			"The compression type is not supported by this simulator. Use CompressionType NONE.",
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if !selectIsStar(req.Expression) {
		S3ErrorXML(w, "InvalidExpression",
			"This simulator faithfully supports only `SELECT * FROM S3Object` queries; "+
				"projection and WHERE filtering are not evaluated.",
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}

	// Run the (minimal) S3 Select over the real stored bytes: parse the input
	// per InputSerialization into rows, then reserialize per OutputSerialization.
	rows, err := selectParseRows(obj.Data, req.InputSerialization)
	if err != nil {
		S3ErrorXML(w, "InvalidTextEncoding", err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	records := selectSerializeRows(rows, req.OutputSerialization)

	bytesScanned := int64(len(obj.Data))
	bytesProcessed := int64(len(obj.Data))
	bytesReturned := int64(len(records))

	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	if len(records) > 0 {
		_, _ = w.Write(awsEventStreamMessage(map[string]string{
			":message-type": "event",
			":event-type":   "Records",
			":content-type": "application/octet-stream",
		}, records))
		if flusher != nil {
			flusher.Flush()
		}
	}

	statsXML, _ := xml.Marshal(selectStatsXML{
		BytesScanned:   bytesScanned,
		BytesProcessed: bytesProcessed,
		BytesReturned:  bytesReturned,
	})
	_, _ = w.Write(awsEventStreamMessage(map[string]string{
		":message-type": "event",
		":event-type":   "Stats",
		":content-type": "text/xml",
	}, statsXML))
	if flusher != nil {
		flusher.Flush()
	}

	// End event: empty payload terminates the stream deterministically.
	_, _ = w.Write(awsEventStreamMessage(map[string]string{
		":message-type": "event",
		":event-type":   "End",
	}, nil))
	if flusher != nil {
		flusher.Flush()
	}
}

// selectIsStar reports whether the expression is a plain
// `SELECT * FROM S3Object[ alias]` (case-insensitive, whitespace-tolerant),
// i.e. an unfiltered, unprojected echo of every row.
func selectIsStar(expr string) bool {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(expr)))
	// Minimum: SELECT * FROM S3OBJECT
	if len(fields) < 4 {
		return false
	}
	if fields[0] != "SELECT" || fields[1] != "*" || fields[2] != "FROM" {
		return false
	}
	if fields[3] != "S3OBJECT" {
		return false
	}
	// Allow an optional table alias; reject WHERE / LIMIT / anything else.
	for _, f := range fields[4:] {
		if f == "WHERE" || f == "LIMIT" || f == "GROUP" || f == "ORDER" {
			return false
		}
	}
	return true
}

// selectParseRows turns the raw object bytes into rows of string fields,
// honoring the InputSerialization (CSV vs JSON-Lines/Document). For JSON
// each parsed record's object is preserved as a row so JSON output can be
// reserialized faithfully; CSV output flattens object values in key order.
func selectParseRows(data []byte, in selectInputSerialization) ([]selectRow, error) {
	switch {
	case in.JSON != nil:
		return selectParseJSON(data, in.JSON.Type)
	default:
		// CSV is the default when neither CSV nor JSON is named; honoring
		// an explicit CSV block is identical.
		return selectParseCSV(data, in)
	}
}

// selectRow carries both shapes a row can take: csv holds the raw ordered
// fields (CSV input), json holds the decoded record (JSON input). Exactly
// one is populated per row depending on the input serialization.
type selectRow struct {
	csv  []string
	json json.RawMessage
}

func selectParseCSV(data []byte, in selectInputSerialization) ([]selectRow, error) {
	rd := csv.NewReader(bytes.NewReader(data))
	rd.FieldsPerRecord = -1
	if in.CSV != nil && in.CSV.FieldDelim != "" {
		rd.Comma = rune(in.CSV.FieldDelim[0])
	}
	var rows []selectRow
	first := true
	skipHeader := in.CSV != nil && strings.EqualFold(in.CSV.FileHeaderInfo, "USE")
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse CSV input: %v", err)
		}
		if first && skipHeader {
			first = false
			continue
		}
		first = false
		rows = append(rows, selectRow{csv: rec})
	}
	return rows, nil
}

func selectParseJSON(data []byte, jsonType string) ([]selectRow, error) {
	// JSON-Lines (Type=LINES, the default) and Document (Type=DOCUMENT) both
	// decode a stream of top-level JSON values via a streaming decoder, which
	// transparently handles whitespace/newlines between records.
	_ = jsonType
	dec := json.NewDecoder(bytes.NewReader(data))
	var rows []selectRow
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse JSON input: %v", err)
		}
		rows = append(rows, selectRow{json: raw})
	}
	return rows, nil
}

// selectSerializeRows reserializes the parsed rows per OutputSerialization.
// CSV output joins fields with the field delimiter and terminates each row
// with the record delimiter; JSON output emits one compact JSON value per
// record delimiter. Defaults match real S3: ',' field, '\n' record.
func selectSerializeRows(rows []selectRow, out selectOutputSerialization) []byte {
	var buf bytes.Buffer
	if out.JSON != nil {
		recDelim := "\n"
		if out.JSON.RecordDelim != "" {
			recDelim = out.JSON.RecordDelim
		}
		for _, row := range rows {
			buf.Write(selectRowToJSON(row))
			buf.WriteString(recDelim)
		}
		return buf.Bytes()
	}
	// CSV output (the default when neither block is present).
	field := ","
	rec := "\n"
	if out.CSV != nil {
		if out.CSV.FieldDelim != "" {
			field = out.CSV.FieldDelim
		}
		if out.CSV.RecordDelim != "" {
			rec = out.CSV.RecordDelim
		}
	}
	for _, row := range rows {
		buf.WriteString(strings.Join(selectRowToCSVFields(row), field))
		buf.WriteString(rec)
	}
	return buf.Bytes()
}

// selectRowToJSON renders a row as a compact JSON value. JSON-input rows are
// echoed verbatim (compacted); CSV-input rows become a JSON object keyed by
// column position (_1, _2, …), matching S3 Select's CSV→JSON column naming.
func selectRowToJSON(row selectRow) []byte {
	if row.json != nil {
		var compact bytes.Buffer
		if err := json.Compact(&compact, row.json); err != nil {
			return row.json
		}
		return compact.Bytes()
	}
	obj := make(map[string]string, len(row.csv))
	for i, v := range row.csv {
		obj["_"+strconv.Itoa(i+1)] = v
	}
	b, _ := json.Marshal(obj)
	return b
}

// selectRowToCSVFields renders a row as ordered CSV fields. CSV-input rows
// pass through; JSON-input objects flatten to their values in key order.
func selectRowToCSVFields(row selectRow) []string {
	if row.csv != nil {
		return row.csv
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(row.json, &m); err != nil {
		// A non-object JSON value (array/scalar) serializes as a single field.
		return []string{strings.TrimSpace(string(row.json))}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.Trim(string(m[k]), `"`)
		fields = append(fields, v)
	}
	return fields
}
