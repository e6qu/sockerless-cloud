package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
	start, end, parsed := parseBlobByteRange(raw)
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

// parseBlobByteRange parses a `bytes=<start>-<end>` header value.
func parseBlobByteRange(raw string) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(raw), "bytes=")
	if !found {
		return 0, 0, false
	}
	lo, hi, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(lo), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, err = strconv.ParseInt(strings.TrimSpace(hi), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return start, end, true
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
	if !blobWriteAllowed(w, r, b, true) {
		return BlobObject{}, false
	}
	return b, true
}

func handlePageBlobUploadPages(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	start, end, ok := blobPageAlignedRange(w, r, int64(len(b.Data)))
	if !ok {
		return
	}
	body, err := openStreamingBody(r)
	if err != nil {
		writeStorageError(w, "UnsupportedHttpVerb", err.Error(), http.StatusUnsupportedMediaType)
		return
	}
	defer body.Close()
	data, err := io.ReadAll(body)
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
	blobWritePages(&b, start, end, data)
	putBlobObject(b)
	writePageOperationHeaders(w, b, data, http.StatusCreated)
}

func handlePageBlobUploadPagesFromURL(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	start, end, ok := blobPageAlignedRange(w, r, int64(len(b.Data)))
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
	blobWritePages(&b, start, end, data)
	putBlobObject(b)
	writePageOperationHeaders(w, b, data, http.StatusCreated)
}

func handlePageBlobClearPages(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	blobDrainBody(r)
	b, ok := blobPageBlobFor(w, r, account, container, blob)
	if !ok {
		return
	}
	start, end, ok := blobPageAlignedRange(w, r, int64(len(b.Data)))
	if !ok {
		return
	}
	for i := start; i <= end; i++ {
		b.Data[i] = 0
	}
	b.PageRanges = blobSubtractRange(b.PageRanges, start, end)
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
	w.Header().Set("Content-MD5", blobContentMD5(written))
	w.Header().Set("x-ms-blob-sequence-number", strconv.FormatInt(b.SequenceNumber, 10))
	w.Header().Set("x-ms-request-server-encrypted", "true")
	w.WriteHeader(status)
}

// blobWritePages copies data into the blob at [start,end] and records the range
// as written.
func blobWritePages(b *BlobObject, start, end int64, data []byte) {
	copy(b.Data[start:end+1], data)
	b.PageRanges = blobMergeRange(b.PageRanges, start, end)
	blobTouch(b)
}

// blobMergeRange adds [start,end] to a sorted, disjoint range set, coalescing
// with any range it touches or overlaps.
func blobMergeRange(ranges []BlobPageRange, start, end int64) []BlobPageRange {
	out := append([]BlobPageRange(nil), ranges...)
	out = append(out, BlobPageRange{Start: start, End: end})
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, rg := range out {
		if n := len(merged); n > 0 && rg.Start <= merged[n-1].End+1 {
			if rg.End > merged[n-1].End {
				merged[n-1].End = rg.End
			}
			continue
		}
		merged = append(merged, rg)
	}
	return append([]BlobPageRange(nil), merged...)
}

// blobSubtractRange removes [start,end] from a sorted, disjoint range set.
func blobSubtractRange(ranges []BlobPageRange, start, end int64) []BlobPageRange {
	var out []BlobPageRange
	for _, rg := range ranges {
		if rg.End < start || rg.Start > end {
			out = append(out, rg)
			continue
		}
		if rg.Start < start {
			out = append(out, BlobPageRange{Start: rg.Start, End: start - 1})
		}
		if rg.End > end {
			out = append(out, BlobPageRange{Start: end + 1, End: rg.End})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
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
		if start, end, parsed := parseBlobByteRange(raw); parsed {
			current = blobClipRanges(current, start, end)
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
		for _, rg := range blobDiffRanges(current, base.PageRanges) {
			doc.PageRange = append(doc.PageRange, blobPageRangeXML(rg))
		}
		for _, rg := range blobDiffRanges(base.PageRanges, current) {
			doc.ClearRange = append(doc.ClearRange, blobPageRangeXML(rg))
		}
	}

	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-blob-content-length", strconv.Itoa(len(b.Data)))
	writeStorageXML(w, http.StatusOK, doc)
}

// blobClipRanges restricts a range set to [start,end].
func blobClipRanges(ranges []BlobPageRange, start, end int64) []BlobPageRange {
	var out []BlobPageRange
	for _, rg := range ranges {
		lo, hi := rg.Start, rg.End
		if lo < start {
			lo = start
		}
		if hi > end {
			hi = end
		}
		if lo <= hi {
			out = append(out, BlobPageRange{Start: lo, End: hi})
		}
	}
	return out
}

// blobDiffRanges returns the parts of `a` that `b` does not cover.
func blobDiffRanges(a, b []BlobPageRange) []BlobPageRange {
	out := append([]BlobPageRange(nil), a...)
	for _, rg := range b {
		out = blobSubtractRange(out, rg.Start, rg.End)
	}
	return out
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
	switch {
	case size < int64(len(b.Data)):
		b.Data = b.Data[:size]
		if size == 0 {
			b.PageRanges = nil
		} else {
			b.PageRanges = blobSubtractRange(b.PageRanges, size, int64(len(b.Data))+size)
		}
	case size > int64(len(b.Data)):
		grown := make([]byte, size)
		copy(grown, b.Data)
		b.Data = grown
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
