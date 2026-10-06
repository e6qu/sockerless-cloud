package main

import (
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// Amazon S3 object versioning. s3Objects holds each key's current object,
// the latest version when that version is not a delete marker; every other
// version of the key, and every delete marker, lives in s3ObjectVersions
// under "<bucket>/<key>\x00<version id>". The null version is stored under
// the version id "null".

// s3ObjectVersion is a version of an object other than its current one: a
// noncurrent object, or a delete marker.
type s3ObjectVersion struct {
	Object       S3Object
	DeleteMarker bool `json:",omitempty"`
	// Tags is the version's tag set, which leaves s3ObjectTags with it when
	// the version stops being current.
	Tags map[string]string `json:",omitempty"`
}

var s3ObjectVersions sim.PrefixStore[s3ObjectVersion]

const (
	s3VersioningEnabled   = "Enabled"
	s3VersioningSuspended = "Suspended"
	s3NullVersionID       = "null"
)

// s3VersioningStatus is the bucket's versioning state: Enabled, Suspended, or
// empty for a bucket whose versioning was never configured.
func s3VersioningStatus(bucket string) string {
	body, _, _, ok := getStoredBucketSubresource(bucket, "versioning")
	if !ok {
		return ""
	}
	var config struct {
		Status string `xml:"Status"`
	}
	if xml.Unmarshal(body, &config) != nil {
		return ""
	}
	return config.Status
}

// s3VersionLabel is how S3 spells a version id on the wire: the null version
// is "null".
func s3VersionLabel(versionID string) string {
	if versionID == "" {
		return s3NullVersionID
	}
	return versionID
}

// s3VersionIDFromLabel is the stored version id a request's versionId names.
func s3VersionIDFromLabel(label string) string {
	if label == s3NullVersionID {
		return ""
	}
	return label
}

func s3VersionStoreKey(storeKey, versionID string) string {
	return storeKey + "\x00" + s3VersionLabel(versionID)
}

const s3VersionIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._"

// s3NewVersionID mints an opaque 32-character version id from the alphabet S3
// version ids are written in.
func s3NewVersionID() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("read random version id: %v", err))
	}
	for i, b := range raw {
		raw[i] = s3VersionIDAlphabet[int(b)%len(s3VersionIDAlphabet)]
	}
	return string(raw)
}

var s3VersionClock atomic.Int64

// s3NextVersionSeq orders the versions a key accumulates: each one written
// after another has a higher number.
func s3NextVersionSeq() int64 {
	for {
		last := s3VersionClock.Load()
		next := max(time.Now().UnixNano(), last+1)
		if s3VersionClock.CompareAndSwap(last, next) {
			return next
		}
	}
}

// s3SetVersionHeader reports the version a response describes in
// x-amz-version-id, which S3 sends for an object in a bucket whose versioning
// was ever configured.
func s3SetVersionHeader(w http.ResponseWriter, bucket, versionID string) {
	if versionID != "" || s3VersioningStatus(bucket) != "" {
		w.Header().Set("x-amz-version-id", s3VersionLabel(versionID))
	}
}

// s3ArchiveCurrent moves the current object under storeKey, with its tags,
// among the key's noncurrent versions. The caller holds the key's write lock.
func s3ArchiveCurrent(storeKey string) {
	current, ok := s3Objects.Get(storeKey)
	if !ok {
		return
	}
	tags, _ := s3ObjectTags.Get(storeKey)
	s3ObjectVersions.Put(s3VersionStoreKey(storeKey, current.VersionID), s3ObjectVersion{Object: current, Tags: tags})
	s3Objects.Delete(storeKey)
	s3ObjectTags.Delete(storeKey)
}

// s3DropNoncurrentNull permanently deletes the key's noncurrent null version,
// which a write or a delete in a versioning-suspended bucket replaces, and
// returns the contents it referenced.
func s3DropNoncurrentNull(storeKey string) string {
	id := s3VersionStoreKey(storeKey, "")
	version, ok := s3ObjectVersions.Get(id)
	if !ok || !s3ObjectVersions.Delete(id) {
		return ""
	}
	return version.Object.Body
}

// s3NoncurrentVersions lists the key's noncurrent versions, newest first.
func s3NoncurrentVersions(storeKey string) []s3ObjectVersion {
	rows := s3ObjectVersions.ListPrefix(storeKey + "\x00")
	versions := make([]s3ObjectVersion, 0, len(rows))
	for _, row := range rows {
		versions = append(versions, row.Item)
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return versions[i].Object.VersionSeq > versions[j].Object.VersionSeq
	})
	return versions
}

// s3LatestDeleteMarker returns the delete marker that is the key's latest
// version, when the key has no current object because of it.
func s3LatestDeleteMarker(storeKey string) (S3Object, bool) {
	if _, ok := s3Objects.Get(storeKey); ok {
		return S3Object{}, false
	}
	versions := s3NoncurrentVersions(storeKey)
	if len(versions) == 0 || !versions[0].DeleteMarker {
		return S3Object{}, false
	}
	return versions[0].Object, true
}

// s3PromoteNewest makes the key's newest remaining version current once its
// current object is gone, unless that version is a delete marker, which stays
// the latest version. The caller holds the key's write lock.
func s3PromoteNewest(bucket, key string) {
	storeKey := s3ObjectKey(bucket, key)
	versions := s3NoncurrentVersions(storeKey)
	if len(versions) == 0 {
		s3DeleteObjectAnnotations(bucket, key)
		return
	}
	newest := versions[0]
	if newest.DeleteMarker {
		return
	}
	s3Objects.Put(storeKey, newest.Object)
	if len(newest.Tags) > 0 {
		s3ObjectTags.Put(storeKey, newest.Tags)
	}
	s3ObjectVersions.Delete(s3VersionStoreKey(storeKey, newest.Object.VersionID))
}

// s3LookupVersion returns the version of the object under storeKey that
// versionID ("" for the null version) names, current or not.
func s3LookupVersion(storeKey, versionID string) (s3ObjectVersion, bool) {
	if current, ok := s3Objects.Get(storeKey); ok && current.VersionID == versionID {
		tags, _ := s3ObjectTags.Get(storeKey)
		return s3ObjectVersion{Object: current, Tags: tags}, true
	}
	return s3ObjectVersions.Get(s3VersionStoreKey(storeKey, versionID))
}

// s3DeleteResult is what a delete did: the version it removed or the delete
// marker it created, reported as the response's x-amz-version-id and
// x-amz-delete-marker.
type s3DeleteResult struct {
	// VersionID is the wire version id to report, empty when the bucket is
	// unversioned and no version was named.
	VersionID    string
	DeleteMarker bool
	// MarkerCreated is set when the delete created a delete marker rather
	// than removing a version.
	MarkerCreated bool
}

// s3DeleteObject deletes key from bucket as DeleteObject does. Without a
// version, an unversioned bucket loses the object, a versioning-enabled one
// gains a delete marker as the key's latest version, and a suspended one
// gains a null delete marker in place of the null version. With versionLabel,
// that one version is permanently removed and the next newest becomes
// current, unless its Object Lock protects it, which fails with
// errS3ObjectLocked.
func s3DeleteObject(bucket, key, versionLabel string, bypassGovernance bool) (s3DeleteResult, error) {
	storeKey := s3ObjectKey(bucket, key)
	defer s3ObjectWriters.Lock(storeKey)()
	if versionLabel != "" {
		if version, ok := s3LookupVersion(storeKey, s3VersionIDFromLabel(versionLabel)); ok && !version.DeleteMarker &&
			s3VersionProtected(version.Object, bypassGovernance, time.Now()) {
			return s3DeleteResult{}, errS3ObjectLocked
		}
		return s3DeleteVersion(bucket, key, versionLabel)
	}
	status := s3VersioningStatus(bucket)
	var released []string
	if status == s3VersioningSuspended {
		released = append(released, s3DropNoncurrentNull(storeKey))
	}
	current, existed := s3Objects.Get(storeKey)
	switch {
	case !existed:
	case status == s3VersioningEnabled || (status == s3VersioningSuspended && current.VersionID != ""):
		s3ArchiveCurrent(storeKey)
	default:
		s3Objects.Delete(storeKey)
		s3ObjectTags.Delete(storeKey)
		released = append(released, current.Body)
	}
	err := s3RemoveBodies(released)
	if status == "" {
		s3DeleteObjectAnnotations(bucket, key)
		if existed {
			s3FireObjectNotifications(bucket, key, "ObjectRemoved:Delete", current.ETag, current.Size, "")
		}
		return s3DeleteResult{}, err
	}
	marker := S3Object{Key: storeKey, LastModified: time.Now().UTC(), VersionSeq: s3NextVersionSeq()}
	if status == s3VersioningEnabled {
		marker.VersionID = s3NewVersionID()
	}
	s3ObjectVersions.Put(s3VersionStoreKey(storeKey, marker.VersionID), s3ObjectVersion{Object: marker, DeleteMarker: true})
	s3FireObjectNotifications(bucket, key, "ObjectRemoved:DeleteMarkerCreated", "", 0, s3VersionLabel(marker.VersionID))
	return s3DeleteResult{VersionID: s3VersionLabel(marker.VersionID), DeleteMarker: true, MarkerCreated: true}, err
}

// s3DeleteVersion permanently removes one version of a key. A version that is
// not there is reported deleted, as S3 reports it. The caller holds the key's
// write lock.
func s3DeleteVersion(bucket, key, versionLabel string) (s3DeleteResult, error) {
	storeKey := s3ObjectKey(bucket, key)
	versionID := s3VersionIDFromLabel(versionLabel)
	result := s3DeleteResult{VersionID: versionLabel}
	if current, ok := s3Objects.Get(storeKey); ok && current.VersionID == versionID {
		s3Objects.Delete(storeKey)
		s3ObjectTags.Delete(storeKey)
		s3PromoteNewest(bucket, key)
		s3FireObjectNotifications(bucket, key, "ObjectRemoved:Delete", current.ETag, current.Size, versionLabel)
		return result, s3RemoveBodies([]string{current.Body})
	}
	id := s3VersionStoreKey(storeKey, versionID)
	version, ok := s3ObjectVersions.Get(id)
	if !ok || !s3ObjectVersions.Delete(id) {
		return result, nil
	}
	if _, hasCurrent := s3Objects.Get(storeKey); !hasCurrent {
		s3PromoteNewest(bucket, key)
	}
	result.DeleteMarker = version.DeleteMarker
	if !version.DeleteMarker {
		s3FireObjectNotifications(bucket, key, "ObjectRemoved:Delete", version.Object.ETag, version.Object.Size, versionLabel)
	}
	return result, s3RemoveBodies([]string{version.Object.Body})
}

func s3RemoveBodies(refs []string) error {
	var errs []error
	for _, ref := range refs {
		errs = append(errs, s3Bodies.Remove(ref))
	}
	return errors.Join(errs...)
}

// s3SetDeleteHeaders reports a delete's outcome the way DeleteObject's
// response headers carry it.
func s3SetDeleteHeaders(w http.ResponseWriter, result s3DeleteResult) {
	if result.VersionID != "" {
		w.Header().Set("x-amz-version-id", result.VersionID)
	}
	if result.DeleteMarker {
		w.Header().Set("x-amz-delete-marker", "true")
	}
}

// s3AddressedVersion resolves the object version a request addresses: the
// version its versionId query names, or the current object. When there is
// none it writes the error S3 answers and reports false: NoSuchVersion for a
// version that does not exist, MethodNotAllowed for a delete marker named by
// version, and NoSuchKey for a key whose latest version is a delete marker or
// that has none.
func s3AddressedVersion(w http.ResponseWriter, r *http.Request) (s3ObjectVersion, bool) {
	bucket, key := sim.PathParam(r, "bucket"), sim.PathParam(r, "key")
	storeKey := s3ObjectKey(bucket, key)
	if !r.URL.Query().Has("versionId") {
		if current, ok := s3Objects.Get(storeKey); ok {
			tags, _ := s3ObjectTags.Get(storeKey)
			return s3ObjectVersion{Object: current, Tags: tags}, true
		}
		if marker, ok := s3LatestDeleteMarker(storeKey); ok {
			w.Header().Set("x-amz-delete-marker", "true")
			w.Header().Set("x-amz-version-id", s3VersionLabel(marker.VersionID))
		}
		S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return s3ObjectVersion{}, false
	}
	label := r.URL.Query().Get("versionId")
	version, ok := s3LookupVersion(storeKey, s3VersionIDFromLabel(label))
	if !ok {
		S3ErrorXML(w, "NoSuchVersion", "The specified version does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return s3ObjectVersion{}, false
	}
	if version.DeleteMarker {
		w.Header().Set("x-amz-delete-marker", "true")
		w.Header().Set("x-amz-version-id", label)
		w.Header().Set("Last-Modified", version.Object.LastModified.UTC().Format(http.TimeFormat))
		S3ErrorXML(w, "MethodNotAllowed", "The specified method is not allowed against this resource.",
			key, sim.RequestID(r.Context()), http.StatusMethodNotAllowed)
		return s3ObjectVersion{}, false
	}
	return version, true
}

// s3UpdateVersion applies fn to the version of the object under storeKey that
// versionID names, current or not. It reports false when that version is gone
// or is a delete marker.
func s3UpdateVersion(storeKey, versionID string, fn func(obj *S3Object, tags *map[string]string)) bool {
	defer s3ObjectWriters.Lock(storeKey)()
	if current, ok := s3Objects.Get(storeKey); ok && current.VersionID == versionID {
		tags, _ := s3ObjectTags.Get(storeKey)
		fn(&current, &tags)
		s3Objects.Put(storeKey, current)
		if len(tags) > 0 {
			s3ObjectTags.Put(storeKey, tags)
		} else {
			s3ObjectTags.Delete(storeKey)
		}
		return true
	}
	updated := false
	s3ObjectVersions.Update(s3VersionStoreKey(storeKey, versionID), func(version *s3ObjectVersion) {
		if version.DeleteMarker {
			return
		}
		fn(&version.Object, &version.Tags)
		updated = true
	})
	return updated
}

// s3VersionEntry is one <Version> or <DeleteMarker> of a ListVersionsResult;
// the two interleave in key order, newest version first within a key.
type s3VersionEntry struct {
	XMLName      xml.Name
	Key          string  `xml:"Key"`
	VersionID    string  `xml:"VersionId"`
	IsLatest     bool    `xml:"IsLatest"`
	LastModified string  `xml:"LastModified"`
	ETag         string  `xml:"ETag,omitempty"`
	Size         *int64  `xml:"Size,omitempty"`
	StorageClass string  `xml:"StorageClass,omitempty"`
	Owner        s3Owner `xml:"Owner"`
}

type s3ListVersionsResult struct {
	XMLName             xml.Name         `xml:"ListVersionsResult"`
	Xmlns               string           `xml:"xmlns,attr"`
	Name                string           `xml:"Name"`
	Prefix              string           `xml:"Prefix"`
	KeyMarker           string           `xml:"KeyMarker"`
	VersionIDMarker     string           `xml:"VersionIdMarker"`
	NextKeyMarker       string           `xml:"NextKeyMarker,omitempty"`
	NextVersionIDMarker string           `xml:"NextVersionIdMarker,omitempty"`
	MaxKeys             int              `xml:"MaxKeys"`
	Delimiter           string           `xml:"Delimiter,omitempty"`
	IsTruncated         bool             `xml:"IsTruncated"`
	Entries             []s3VersionEntry `xml:"Version"`
	CommonPrefixes      []s3CommonPrefix `xml:"CommonPrefixes,omitempty"`
}

// s3ListedVersion is one version in a listing, positioned by its key and its
// age.
type s3ListedVersion struct {
	key      string
	version  s3ObjectVersion
	isLatest bool
}

// cursor orders a key's versions newest first and keys in listing order; the
// NUL separator sorts below every byte a key continues with.
func (v s3ListedVersion) cursor() string {
	return v.key + "\x00" + fmt.Sprintf("%019d", int64(^uint64(0)>>1)-v.version.Object.VersionSeq)
}

// s3BucketVersions lists every version of every key under prefix in bucket,
// current objects and noncurrent versions alike, in listing order.
func s3BucketVersions(bucket, prefix string) []s3ListedVersion {
	bucketPrefix := bucket + "/"
	var listed []s3ListedVersion
	for _, row := range s3Objects.ListPrefix(bucketPrefix + prefix) {
		listed = append(listed, s3ListedVersion{key: row.ID[len(bucketPrefix):],
			version: s3ObjectVersion{Object: row.Item}, isLatest: true})
	}
	for _, row := range s3ObjectVersions.ListPrefix(bucketPrefix + prefix) {
		storeKey, _, ok := strings.Cut(row.ID, "\x00")
		if !ok {
			continue
		}
		listed = append(listed, s3ListedVersion{key: storeKey[len(bucketPrefix):], version: row.Item})
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].cursor() < listed[j].cursor() })
	// A key's newest version is its latest: the current object, or the delete
	// marker that stands in for one.
	for i := range listed {
		if i == 0 || listed[i-1].key != listed[i].key {
			listed[i].isLatest = true
		}
	}
	return listed
}

func handleS3ListObjectVersions(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	q := r.URL.Query()
	prefix, delimiter := q.Get("prefix"), q.Get("delimiter")
	keyMarker, versionMarker := q.Get("key-marker"), q.Get("version-id-marker")
	maxKeys := s3ListLimit
	if raw := q.Get("max-keys"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &maxKeys); err != nil || maxKeys < 0 {
			S3ErrorXML(w, "InvalidArgument", "Provided max-keys not an integer or within integer range",
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
	}
	if versionMarker != "" && keyMarker == "" {
		S3ErrorXML(w, "InvalidArgument", "A version-id marker cannot be specified without a key marker.",
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
		return
	}

	listed := s3BucketVersions(bucket, prefix)
	entries := blobstore.RollUp(listed, func(v s3ListedVersion) string { return v.key },
		s3ListedVersion.cursor, prefix, delimiter)
	marker := ""
	switch {
	case versionMarker != "":
		for _, v := range listed {
			if v.key == keyMarker && s3VersionLabel(v.version.Object.VersionID) == versionMarker {
				marker = v.cursor()
				break
			}
		}
		if marker == "" {
			S3ErrorXML(w, "InvalidArgument", "Invalid version id specified",
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
	case keyMarker != "":
		// A key marker resumes after every version of that key, or after
		// everything a common prefix stands for.
		marker = keyMarker + "\x00\xff"
		for _, entry := range entries {
			if entry.Prefix && entry.Key == keyMarker {
				marker = keyMarker
				break
			}
		}
	}
	page, truncated, _ := blobstore.PageAfter(entries, marker, min(maxKeys, s3ListLimit))

	owner := s3Owner{ID: awsAccountID(), DisplayName: "simulator"}
	out := s3ListVersionsResult{
		Xmlns:           "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:            bucket,
		Prefix:          prefix,
		KeyMarker:       keyMarker,
		VersionIDMarker: versionMarker,
		MaxKeys:         maxKeys,
		Delimiter:       delimiter,
		IsTruncated:     truncated,
		Entries:         []s3VersionEntry{},
	}
	for _, entry := range page {
		if entry.Prefix {
			out.CommonPrefixes = append(out.CommonPrefixes, s3CommonPrefix{Prefix: entry.Key})
			continue
		}
		v := entry.Item
		obj := v.version.Object
		e := s3VersionEntry{
			XMLName:      xml.Name{Local: "Version"},
			Key:          v.key,
			VersionID:    s3VersionLabel(obj.VersionID),
			IsLatest:     v.isLatest,
			LastModified: obj.LastModified.UTC().Format(time.RFC3339),
			Owner:        owner,
		}
		if v.version.DeleteMarker {
			e.XMLName.Local = "DeleteMarker"
		} else {
			size := obj.Size
			e.ETag, e.Size, e.StorageClass = obj.ETag, &size, obj.storageClassOf()
		}
		out.Entries = append(out.Entries, e)
	}
	if truncated && len(page) > 0 {
		last := page[len(page)-1]
		out.NextKeyMarker = last.Key
		if !last.Prefix {
			out.NextVersionIDMarker = s3VersionLabel(last.Item.version.Object.VersionID)
		}
	}
	WriteXML(w, http.StatusOK, out)
}

// s3KeepVersionBodies keeps, through a payload adoption, the contents every
// noncurrent version references.
func s3KeepVersionBodies(adoption *blobstore.Adoption) {
	for _, row := range s3ObjectVersions.ListPrefix("") {
		adoption.Keep(row.Item.Object.Body)
	}
}

// s3BucketHoldsVersions reports whether any version or delete marker is left
// in bucket, which keeps it from being deleted.
func s3BucketHoldsVersions(bucket string) bool {
	return len(s3ObjectVersions.ListPrefix(bucket+"/")) > 0
}

// s3CopySourceObject resolves the object a copy reads: the version the
// x-amz-copy-source header's versionId names, or the source key's current
// object. When there is none it writes the error S3 answers and reports
// false.
func s3CopySourceObject(w http.ResponseWriter, r *http.Request, bucket, key string) (S3Object, bool) {
	storeKey := s3ObjectKey(bucket, key)
	label, named := s3CopySourceVersion(r)
	if !named {
		src, ok := s3Objects.Get(storeKey)
		if !ok {
			S3ErrorXML(w, "NoSuchKey", "The specified key does not exist.",
				key, sim.RequestID(r.Context()), http.StatusNotFound)
		}
		return src, ok
	}
	version, ok := s3LookupVersion(storeKey, s3VersionIDFromLabel(label))
	switch {
	case !ok:
		S3ErrorXML(w, "NoSuchVersion", "The specified version does not exist.",
			key, sim.RequestID(r.Context()), http.StatusNotFound)
		return S3Object{}, false
	case version.DeleteMarker:
		S3ErrorXML(w, "InvalidRequest", "The source of a copy request may not specifically refer to a delete marker by version id.",
			key, sim.RequestID(r.Context()), http.StatusBadRequest)
		return S3Object{}, false
	}
	return version.Object, true
}

// s3CopySourceVersion reads the versionId the x-amz-copy-source header names.
func s3CopySourceVersion(r *http.Request) (string, bool) {
	_, query, _ := strings.Cut(r.Header.Get("x-amz-copy-source"), "?")
	values, err := url.ParseQuery(query)
	if err != nil || !values.Has("versionId") {
		return "", false
	}
	return values.Get("versionId"), true
}

// s3SetCopySourceVersionHeader reports the version a copy read in
// x-amz-copy-source-version-id, as S3 does when the source bucket is
// versioned or the request named a version.
func s3SetCopySourceVersionHeader(w http.ResponseWriter, r *http.Request, bucket string, src S3Object) {
	if _, named := s3CopySourceVersion(r); named || src.VersionID != "" || s3VersioningStatus(bucket) != "" {
		w.Header().Set("x-amz-copy-source-version-id", s3VersionLabel(src.VersionID))
	}
}
