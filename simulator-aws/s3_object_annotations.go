package main

import (
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 object annotations — named payloads attached to an object,
// addressed through the object's `?annotation` subresource, plus the bucket-level
// annotation table configuration of the S3 Metadata feature.
//
// PUT  /{bucket}/{key}?annotation&annotationName=…  PutObjectAnnotation
// GET  /{bucket}/{key}?annotation&annotationName=…  GetObjectAnnotation
// GET  /{bucket}/{key}?annotation                   ListObjectAnnotations
// DELETE /{bucket}/{key}?annotation&annotationName=… DeleteObjectAnnotation
// PUT  /{bucket}?metadataAnnotationTable            UpdateBucketMetadataAnnotationTableConfiguration

// s3Annotation is a single annotation stored against an object. Owner is the
// "<bucket>/<key>" of the object it hangs off, so an object's annotations are
// enumerable without a key scan.
type s3Annotation struct {
	Owner        string    `json:"Owner"`
	Name         string    `json:"Name"`
	Payload      []byte    `json:"Payload"`
	ETag         string    `json:"ETag"`
	LastModified time.Time `json:"LastModified"`
}

// s3ObjectAnnotations are keyed "<bucket>/<key>\x00<annotationName>".
var s3ObjectAnnotations sim.Store[s3Annotation]

func s3AnnotationKey(bucket, key, name string) string {
	return s3ObjectKey(bucket, key) + "\x00" + name
}

// s3ObjectAnnotationsOf returns every annotation attached to one object, sorted
// by name.
func s3ObjectAnnotationsOf(bucket, key string) []s3Annotation {
	owner := s3ObjectKey(bucket, key)
	found := s3ObjectAnnotations.Filter(func(a s3Annotation) bool { return a.Owner == owner })
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found
}

// s3ValidAnnotationName reports whether a name is acceptable. Real S3 rejects an
// empty name and caps the length; both answers are modeled errors.
func s3ValidAnnotationName(name string) (code, msg string, ok bool) {
	switch {
	case name == "":
		return "InvalidAnnotationName", "The annotation name must not be empty.", false
	case len(name) > 512:
		return "AnnotationNameTooLong", "The annotation name exceeds the maximum length of 512 bytes.", false
	}
	return "", "", true
}

// s3DeleteObjectAnnotations drops every annotation attached to an object, so a
// deleted object leaves none behind.
func s3DeleteObjectAnnotations(bucket, key string) {
	for _, ann := range s3ObjectAnnotationsOf(bucket, key) {
		s3ObjectAnnotations.Delete(s3AnnotationKey(bucket, key, ann.Name))
	}
}

// handleS3PutObjectAnnotation stores the request payload under the annotation
// name. The response carries the annotation's ETag header and an
// XML body naming the object key and the annotation.
func handleS3PutObjectAnnotation(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	name := r.URL.Query().Get("annotationName")
	if code, msg, ok := s3ValidAnnotationName(name); !ok {
		S3ErrorXML(w, code, msg, key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if _, ok := s3Objects.Get(s3ObjectKey(bucket, key)); !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		S3ErrorXML(w, "IncompleteBody", "Failed to read the annotation payload: "+err.Error(),
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	etag := fmt.Sprintf("\"%x\"", md5.Sum(payload))
	s3ObjectAnnotations.Put(s3AnnotationKey(bucket, key, name), s3Annotation{
		Owner:        s3ObjectKey(bucket, key),
		Name:         name,
		Payload:      payload,
		ETag:         etag,
		LastModified: time.Now().UTC(),
	})

	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><PutObjectAnnotationOutput xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Key>%s</Key><AnnotationName>%s</AnnotationName></PutObjectAnnotationOutput>`,
		xmlEscape(key), xmlEscape(name))
}

// handleS3GetObjectAnnotation streams the stored annotation payload back, with
// the modeled ETag / Last-Modified / Content-Length headers.
func handleS3GetObjectAnnotation(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	name := r.URL.Query().Get("annotationName")
	if code, msg, ok := s3ValidAnnotationName(name); !ok {
		S3ErrorXML(w, code, msg, key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if _, ok := s3Objects.Get(s3ObjectKey(bucket, key)); !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	ann, ok := s3ObjectAnnotations.Get(s3AnnotationKey(bucket, key, name))
	if !ok {
		S3ErrorXML(w, "NoSuchAnnotation", "The specified annotation does not exist.",
			name, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	w.Header().Set("ETag", ann.ETag)
	w.Header().Set("Last-Modified", ann.LastModified.Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.Itoa(len(ann.Payload)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(ann.Payload)
}

// handleS3DeleteObjectAnnotation removes one annotation. Real S3 answers 204 No
// Content, and deleting an annotation that is not there is not an error.
func handleS3DeleteObjectAnnotation(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	name := r.URL.Query().Get("annotationName")
	if code, msg, ok := s3ValidAnnotationName(name); !ok {
		S3ErrorXML(w, code, msg, key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}
	if _, ok := s3Objects.Get(s3ObjectKey(bucket, key)); !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	s3ObjectAnnotations.Delete(s3AnnotationKey(bucket, key, name))
	w.WriteHeader(http.StatusNoContent)
}

// handleS3ListObjectAnnotations lists an object's annotations, honouring the
// AnnotationPrefix filter and the max-results / continuation-token paging the
// operation models.
func handleS3ListObjectAnnotations(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	if _, ok := s3Objects.Get(s3ObjectKey(bucket, key)); !ok {
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	annPrefix := q.Get("annotation-prefix")
	maxResults := 1000
	if v := q.Get("max-annotation-results"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			maxResults = n
		}
	}

	var found []s3Annotation
	for _, ann := range s3ObjectAnnotationsOf(bucket, key) {
		if annPrefix != "" && !strings.HasPrefix(ann.Name, annPrefix) {
			continue
		}
		found = append(found, ann)
	}

	continuationToken := q.Get("continuation-token")
	start := 0
	if continuationToken != "" {
		start = sort.Search(len(found), func(i int) bool { return found[i].Name >= continuationToken })
	}
	end := start + maxResults
	nextToken := ""
	if end < len(found) {
		nextToken = found[end].Name
	} else {
		end = len(found)
	}
	page := found[start:end]

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<ListObjectAnnotationsOutput xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&sb, `<Bucket>%s</Bucket><Key>%s</Key>`, xmlEscape(bucket), xmlEscape(key))
	if annPrefix != "" {
		fmt.Fprintf(&sb, `<AnnotationPrefix>%s</AnnotationPrefix>`, xmlEscape(annPrefix))
	}
	fmt.Fprintf(&sb, `<MaxAnnotationResults>%d</MaxAnnotationResults>`, maxResults)
	fmt.Fprintf(&sb, `<AnnotationCount>%d</AnnotationCount>`, len(page))
	if continuationToken != "" {
		fmt.Fprintf(&sb, `<ContinuationToken>%s</ContinuationToken>`, xmlEscape(continuationToken))
	}
	if nextToken != "" {
		fmt.Fprintf(&sb, `<NextContinuationToken>%s</NextContinuationToken>`, xmlEscape(nextToken))
	}
	sb.WriteString(`<Annotations>`)
	for _, ann := range page {
		sb.WriteString(`<AnnotationEntry>`)
		fmt.Fprintf(&sb, `<AnnotationName>%s</AnnotationName>`, xmlEscape(ann.Name))
		fmt.Fprintf(&sb, `<LastModified>%s</LastModified>`, ann.LastModified.Format(time.RFC3339))
		fmt.Fprintf(&sb, `<ETag>%s</ETag>`, xmlEscape(ann.ETag))
		fmt.Fprintf(&sb, `<Size>%d</Size>`, len(ann.Payload))
		sb.WriteString(`</AnnotationEntry>`)
	}
	sb.WriteString(`</Annotations>`)
	sb.WriteString(`</ListObjectAnnotationsOutput>`)

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, sb.String())
}
