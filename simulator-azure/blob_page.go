package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/blobstore"
	"github.com/e6qu/sockerless-cloud/sim/sparse"
)

// Page blob ranges. A page blob is a fixed-size sparse byte array written in
// 512-byte pages: Put Page writes a page-aligned range, Clear Page returns one
// to the sparse state, and Get Page Ranges enumerates exactly the ranges that
// have been written. The simulator tracks those ranges explicitly rather than
// inferring them from the bytes, because a page written with zeros is written,
// not sparse — the distinction Get Page Ranges exists to report.

const blobPageSize = 512

// blobPageAlignedRange parses and validates the `x-ms-range` / `Range` header of
// a page operation: it must be present, well formed, page aligned at the start
// and page aligned one past the end, and inside the blob.
func blobPageAlignedRange(w http.ResponseWriter, r *http.Request, size int64) (start, end int64, ok bool) {
	raw := r.Header.Get("x-ms-range")
	if raw == "" {
		raw = r.Header.Get("Range")
	}
	if raw == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-range.",
			http.StatusBadRequest)
		return 0, 0, false
	}
	start, end, parsed := blobExactRange(raw)
	if !parsed {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-range.",
			http.StatusBadRequest)
		return 0, 0, false
	}
	if start%blobPageSize != 0 || (end+1)%blobPageSize != 0 || end < start {
		writeStorageError(w, "InvalidPageRange",
			"The page range specified is invalid.", http.StatusRequestedRangeNotSatisfiable)
		return 0, 0, false
	}
	if end >= size {
		writeStorageError(w, "InvalidPageRange",
			"The page range specified is invalid.", http.StatusRequestedRangeNotSatisfiable)
		return 0, 0, false
	}
	return start, end, true
}

// blobExactRange parses a `bytes=<start>-<end>` header value, the form page
// and file ranges take.
func blobExactRange(raw string) (start, end int64, ok bool) {
	requested, err := blobstore.ParseRange(raw, blobstore.RangeOpts{})
	if err != nil {
		return 0, 0, false
	}
	return requested.Bounds()
}

// blobPageBlobFor loads a page blob for a write, refusing a blob of another
// type or one whose lease/immutability protections deny the write.
func blobPageBlobFor(w http.ResponseWriter, r *http.Request, account, container, blob string) (BlobObject, bool) {
	b, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return BlobObject{}, false
	}
	if b.BlobType != "PageBlob" {
		writeStorageError(w, "InvalidBlobType",
			"The blob type is invalid for this operation.", http.StatusConflict)
		return BlobObject{}, false
	}
	if !blobWriteAllowed(w, r, b, true, blobstore.Modify) {
		return BlobObject{}, false
	}
	return b, true
}

func handlePageBlobUploadPages(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	start, end, ok := blobPageAlignedRange(w, r, b.Size)
	if !ok {
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	if int64(len(data)) != end-start+1 {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: Content-Length.",
			http.StatusBadRequest)
		return
	}
	if err := blobWritePages(&b, start, end, data); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	putBlobObject(b)
	writePageOperationHeaders(w, b, data, http.StatusCreated)
}

func handlePageBlobUploadPagesFromURL(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	start, end, ok := blobPageAlignedRange(w, r, b.Size)
	if !ok {
		return
	}
	data, ok := blobReadCopySourceRange(w, r, r.Header.Get("x-ms-copy-source"), r.Header.Get("x-ms-source-range"))
	if !ok {
		return
	}
	if int64(len(data)) != end-start+1 {
		writeStorageError(w, "InvalidRange",
			"The range specified is invalid for the current size of the resource.",
			http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if err := blobWritePages(&b, start, end, data); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	putBlobObject(b)
	writePageOperationHeaders(w, b, data, http.StatusCreated)
}

func handlePageBlobClearPages(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	blobDrainBody(r)
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	start, end, ok := blobPageAlignedRange(w, r, b.Size)
	if !ok {
		return
	}
	if err := blobEditContents(&b, func(data []byte) []byte {
		clear(data[start : end+1])
		return data
	}); err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	b.PageRanges = sparse.Subtract(b.PageRanges, start, end)
	blobTouch(&b)
	putBlobObject(b)
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-blob-sequence-number", strconv.FormatInt(b.SequenceNumber, 10))
	w.WriteHeader(http.StatusCreated)
}

func writePageOperationHeaders(w http.ResponseWriter, b BlobObject, written []byte, status int) {
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("Content-MD5", blobstore.Digest(written).MD5Base64())
	w.Header().Set("x-ms-blob-sequence-number", strconv.FormatInt(b.SequenceNumber, 10))
	w.Header().Set("x-ms-request-server-encrypted", "true")
	w.WriteHeader(status)
}

// blobWritePages copies data into the blob at [start,end] and records the range
// as written.
func blobWritePages(b *BlobObject, start, end int64, data []byte) error {
	if err := blobEditContents(b, func(current []byte) []byte {
		copy(current[start:end+1], data)
		return current
	}); err != nil {
		return err
	}
	b.PageRanges = sparse.Merge(b.PageRanges, start, end)
	blobTouch(b)
	return nil
}

// blobPageListDocument is the <PageList> Get Page Ranges returns.
type blobPageListDocument struct {
	XMLName    xml.Name           `xml:"PageList"`
	PageRange  []blobPageRangeXML `xml:"PageRange"`
	ClearRange []blobPageRangeXML `xml:"ClearRange"`
	NextMarker string             `xml:"NextMarker"`
}

type blobPageRangeXML struct {
	Start int64 `xml:"Start"`
	End   int64 `xml:"End"`
}

// handleGetPageRanges serves both Get Page Ranges and, when `prevsnapshot` names
// an earlier snapshot, Get Page Ranges Diff: the diff reports the ranges written
// since that snapshot as PageRange and the ranges cleared since it as
// ClearRange.
func handleGetPageRanges(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := lookupBlob(r, account, container, blob)
	if !ok {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if b.BlobType != "PageBlob" {
		writeStorageError(w, "InvalidBlobType",
			"The blob type is invalid for this operation.", http.StatusConflict)
		return
	}
	if !blobLeaseAccessOK(w, r, b.Lease, "blob") {
		return
	}

	current := b.PageRanges
	if raw := r.Header.Get("x-ms-range"); raw != "" {
		if start, end, parsed := blobExactRange(raw); parsed {
			current = sparse.Clip(current, start, end)
		}
	}

	doc := blobPageListDocument{}
	prev := r.URL.Query().Get("prevsnapshot")
	if prev == "" {
		for _, rg := range current {
			doc.PageRange = append(doc.PageRange, blobPageRangeXML(rg))
		}
	} else {
		base, ok := blobObjects.Get(blobSnapshotKey(account, container, blob, prev))
		if !ok || base.Deleted {
			writeStorageError(w, "PreviousSnapshotNotFound",
				"The previous snapshot is not found.", http.StatusNotFound)
			return
		}
		for _, rg := range sparse.Diff(current, base.PageRanges) {
			doc.PageRange = append(doc.PageRange, blobPageRangeXML(rg))
		}
		for _, rg := range sparse.Diff(base.PageRanges, current) {
			doc.ClearRange = append(doc.ClearRange, blobPageRangeXML(rg))
		}
	}

	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-blob-content-length", strconv.FormatInt(b.Size, 10))
	writeStorageXML(w, http.StatusOK, doc)
}

func handlePageBlobResize(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	size, err := strconv.ParseInt(r.Header.Get("x-ms-blob-content-length"), 10, 64)
	if err != nil || size < 0 || size%blobPageSize != 0 {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-blob-content-length.",
			http.StatusBadRequest)
		return
	}
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	oldSize := b.Size
	if size != oldSize {
		if err := blobEditContents(&b, func(data []byte) []byte {
			if size < int64(len(data)) {
				return data[:size]
			}
			grown := make([]byte, size)
			copy(grown, data)
			return grown
		}); err != nil {
			writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
	}
	switch {
	case size == 0:
		b.PageRanges = nil
	case size < oldSize:
		// Every written page past the new end goes with the bytes.
		b.PageRanges = sparse.Subtract(b.PageRanges, size, oldSize-1)
	}
	blobTouch(&b)
	putBlobObject(b)
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-blob-sequence-number", strconv.FormatInt(b.SequenceNumber, 10))
	w.WriteHeader(http.StatusOK)
}

func handlePageBlobUpdateSequenceNumber(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	action := strings.ToLower(r.Header.Get("x-ms-sequence-number-action"))
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	var requested int64
	hasRequested := false
	if raw := r.Header.Get("x-ms-blob-sequence-number"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-blob-sequence-number.",
				http.StatusBadRequest)
			return
		}
		requested, hasRequested = v, true
	}
	switch action {
	case "increment":
		if hasRequested {
			writeStorageError(w, "InvalidHeaderValue",
				"x-ms-blob-sequence-number must not accompany the increment action.",
				http.StatusBadRequest)
			return
		}
		b.SequenceNumber++
	case "max":
		if !hasRequested {
			writeStorageError(w, "MissingRequiredHeader",
				"An HTTP header that's mandatory for this request is not specified: x-ms-blob-sequence-number.",
				http.StatusBadRequest)
			return
		}
		if requested > b.SequenceNumber {
			b.SequenceNumber = requested
		}
	case "update":
		if !hasRequested {
			writeStorageError(w, "MissingRequiredHeader",
				"An HTTP header that's mandatory for this request is not specified: x-ms-blob-sequence-number.",
				http.StatusBadRequest)
			return
		}
		b.SequenceNumber = requested
	default:
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-sequence-number-action.",
			http.StatusBadRequest)
		return
	}
	blobTouch(&b)
	putBlobObject(b)
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-blob-sequence-number", strconv.FormatInt(b.SequenceNumber, 10))
	w.WriteHeader(http.StatusOK)
}
