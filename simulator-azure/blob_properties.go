package main

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The blob property surfaces: metadata, the system HTTP headers, the access
// tier, the scheduled expiry, the blob tag set, and the immutability policy /
// legal hold pair. Each writes through to the stored record — a Set Blob Tier
// really changes what Get Blob Properties and List Blobs report — and each
// respects the blob's lease and immutability protections.

func handleSetBlobMetadata(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := blobForPropertyWrite(w, r, account, container, blob)
	if !ok {
		return
	}
	b.Metadata = collectMetadata(r)
	blobTouch(&b)
	putBlobObject(b)
	writeBlobPropertyWriteHeaders(w, b, http.StatusOK)
}

func handleSetBlobHTTPHeaders(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := blobForPropertyWrite(w, r, account, container, blob)
	if !ok {
		return
	}
	// Set Blob HTTP Headers replaces the whole system-property set: a header the
	// request omits is cleared, exactly as on Azure.
	applyBlobHTTPHeaders(&b, r)
	blobTouch(&b)
	putBlobObject(b)
	if b.BlobType == "PageBlob" {
		w.Header().Set("x-ms-blob-sequence-number", strconv.FormatInt(b.SequenceNumber, 10))
	}
	writeBlobPropertyWriteHeaders(w, b, http.StatusOK)
}

// blobAccessTiers are the tier names Set Blob Tier accepts: the block-blob
// lifecycle tiers and the premium page-blob tiers.
var blobAccessTiers = map[string]bool{
	"Hot": true, "Cool": true, "Cold": true, "Archive": true,
	"P4": true, "P6": true, "P10": true, "P15": true, "P20": true,
	"P30": true, "P40": true, "P50": true, "P60": true, "P70": true, "P80": true,
}

func handleSetBlobTier(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	tier := r.Header.Get("x-ms-access-tier")
	if tier == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-access-tier.",
			http.StatusBadRequest)
		return
	}
	if !blobAccessTiers[tier] {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-access-tier.",
			http.StatusBadRequest)
		return
	}
	snapshot := r.URL.Query().Get("snapshot")
	b, exists := blobObjects.Get(blobSnapshotKey(account, container, blob, snapshot))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if !blobLeaseAccessOK(w, r, b.Lease, "blob") {
		return
	}
	b.AccessTier = tier
	b.AccessTierInferred = false
	b.AccessTierChangeTime = blobNowHTTP()
	putBlobObject(b)
	w.WriteHeader(http.StatusOK)
}

func handleSetBlobExpiry(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	option := r.Header.Get("x-ms-expiry-option")
	if option == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-expiry-option.",
			http.StatusBadRequest)
		return
	}
	b, ok := blobForPropertyWrite(w, r, account, container, blob)
	if !ok {
		return
	}
	raw := r.Header.Get("x-ms-expiry-time")
	switch strings.ToLower(option) {
	case "neverexpire":
		b.ExpiresOn = ""
	case "relativetocreation", "relativetonow":
		ms, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || ms < 0 {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-expiry-time.",
				http.StatusBadRequest)
			return
		}
		base := time.Now()
		if strings.EqualFold(option, "relativetocreation") && b.CreationTime != "" {
			if t, err := http.ParseTime(b.CreationTime); err == nil {
				base = t
			}
		}
		b.ExpiresOn = base.Add(time.Duration(ms) * time.Millisecond).UTC().Format(http.TimeFormat)
	case "absolute":
		t, err := http.ParseTime(raw)
		if err != nil {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-expiry-time.",
				http.StatusBadRequest)
			return
		}
		b.ExpiresOn = t.UTC().Format(http.TimeFormat)
	default:
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-expiry-option.",
			http.StatusBadRequest)
		return
	}
	blobTouch(&b)
	putBlobObject(b)
	writeBlobPropertyWriteHeaders(w, b, http.StatusOK)
}

// ---------------------------------------------------------------------------
// Immutability policy and legal hold
// ---------------------------------------------------------------------------

func handleSetBlobImmutabilityPolicy(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	raw := r.Header.Get("x-ms-immutability-policy-until-date")
	if raw == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-immutability-policy-until-date.",
			http.StatusBadRequest)
		return
	}
	until, err := http.ParseTime(raw)
	if err != nil {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-immutability-policy-until-date.",
			http.StatusBadRequest)
		return
	}
	mode := r.Header.Get("x-ms-immutability-policy-mode")
	if mode == "" {
		mode = "Unlocked"
	}
	if !strings.EqualFold(mode, "Locked") && !strings.EqualFold(mode, "Unlocked") &&
		!strings.EqualFold(mode, "Mutable") {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-immutability-policy-mode.",
			http.StatusBadRequest)
		return
	}
	b, exists := blobObjects.Get(blobSnapshotKey(account, container, blob, r.URL.Query().Get("snapshot")))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	// A locked policy can only be extended, never shortened or downgraded.
	if strings.EqualFold(b.ImmutabilityPolicyMode, "Locked") {
		current, err := http.ParseTime(b.ImmutabilityPolicyExpiry)
		if err == nil && (until.Before(current) || !strings.EqualFold(mode, "Locked")) {
			writeStorageError(w, "BlobImmutableDueToPolicy",
				"A locked immutability policy can only be extended.", http.StatusConflict)
			return
		}
	}
	b.ImmutabilityPolicyExpiry = until.UTC().Format(http.TimeFormat)
	b.ImmutabilityPolicyMode = mode
	putBlobObject(b)
	w.Header().Set("x-ms-immutability-policy-until-date", b.ImmutabilityPolicyExpiry)
	w.Header().Set("x-ms-immutability-policy-mode", strings.ToLower(mode))
	w.WriteHeader(http.StatusOK)
}

func handleDeleteBlobImmutabilityPolicy(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, exists := blobObjects.Get(blobSnapshotKey(account, container, blob, r.URL.Query().Get("snapshot")))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if strings.EqualFold(b.ImmutabilityPolicyMode, "Locked") {
		if until, err := http.ParseTime(b.ImmutabilityPolicyExpiry); err == nil && time.Now().Before(until) {
			writeStorageError(w, "BlobImmutableDueToPolicy",
				"A locked immutability policy cannot be deleted before it expires.", http.StatusConflict)
			return
		}
	}
	b.ImmutabilityPolicyExpiry = ""
	b.ImmutabilityPolicyMode = ""
	putBlobObject(b)
	w.WriteHeader(http.StatusOK)
}

func handleSetBlobLegalHold(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	raw := r.Header.Get("x-ms-legal-hold")
	if raw == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-legal-hold.",
			http.StatusBadRequest)
		return
	}
	hold, err := strconv.ParseBool(raw)
	if err != nil {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-legal-hold.",
			http.StatusBadRequest)
		return
	}
	b, exists := blobObjects.Get(blobSnapshotKey(account, container, blob, r.URL.Query().Get("snapshot")))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	b.LegalHold = hold
	putBlobObject(b)
	w.Header().Set("x-ms-legal-hold", strconv.FormatBool(hold))
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Blob tags
// ---------------------------------------------------------------------------

// blobTagsDocument is the <Tags> document Get Blob Tags returns and Set Blob
// Tags accepts.
type blobTagsDocument struct {
	XMLName xml.Name      `xml:"Tags"`
	TagSet  []blobTagPair `xml:"TagSet>Tag"`
}

type blobTagPair struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

func blobTagsDocumentFor(tags map[string]string) *blobTagsDocument {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	doc := &blobTagsDocument{}
	for _, k := range keys {
		doc.TagSet = append(doc.TagSet, blobTagPair{Key: k, Value: tags[k]})
	}
	return doc
}

// parseBlobTagsHeader reads the `x-ms-tags` header a write may carry, which
// encodes the tag set as a URL-form-encoded key/value list.
func parseBlobTagsHeader(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range values {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func handleGetBlobTags(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := lookupBlob(r, account, container, blob)
	if !ok {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	writeStorageXML(w, http.StatusOK, blobTagsDocumentFor(b.Tags))
}

func handleSetBlobTags(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	defer r.Body.Close()
	var doc blobTagsDocument
	if err := xml.NewDecoder(r.Body).Decode(&doc); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	b, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if !blobLeaseAccessOK(w, r, b.Lease, "blob") {
		return
	}
	tags := map[string]string{}
	for _, tag := range doc.TagSet {
		if tag.Key == "" {
			writeStorageError(w, "InvalidXmlNodeValue",
				"The value for one of the XML nodes is not in the correct format: Key.",
				http.StatusBadRequest)
			return
		}
		tags[tag.Key] = tag.Value
	}
	b.Tags = tags
	putBlobObject(b)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Shared property-write plumbing
// ---------------------------------------------------------------------------

// blobForPropertyWrite loads the blob a property write addresses and enforces
// its lease and immutability protections. It writes the error and returns
// ok=false when the write must be refused.
func blobForPropertyWrite(w http.ResponseWriter, r *http.Request, account, container, blob string) (BlobObject, bool) {
	b, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return BlobObject{}, false
	}
	if !azureBlobPreconditionOK(w, r, b.ETag, true) {
		return BlobObject{}, false
	}
	if !blobWriteAllowed(w, r, b, true) {
		return BlobObject{}, false
	}
	return b, true
}

func writeBlobPropertyWriteHeaders(w http.ResponseWriter, b BlobObject, status int) {
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-request-server-encrypted", "true")
	w.WriteHeader(status)
}
