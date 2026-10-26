package main

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// Append blob blocks. Append Block adds bytes at the end of the blob and reports
// the offset the block landed at plus the resulting committed-block count;
// Seal Append Blob makes the blob permanently read-only, and every later append
// is refused.

// blobAppendBlobFor loads an append blob for a write, enforcing its type, its
// seal, its lease and its immutability protections, plus the two append-position
// preconditions Azure defines.
func blobAppendBlobFor(w http.ResponseWriter, r *http.Request, account, container, blob string, incoming int64) (BlobObject, bool) {
	b, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return BlobObject{}, false
	}
	if b.BlobType != "AppendBlob" {
		writeStorageError(w, "InvalidBlobType",
			"The blob type is invalid for this operation.", http.StatusConflict)
		return BlobObject{}, false
	}
	if b.Sealed {
		writeStorageError(w, "BlobIsSealed",
			"The blob is sealed and no further appends are allowed.", http.StatusConflict)
		return BlobObject{}, false
	}
	if !blobWriteAllowed(w, r, b, true, blobstore.Modify) {
		return BlobObject{}, false
	}
	if raw := r.Header.Get("x-ms-blob-condition-appendpos"); raw != "" {
		want, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-blob-condition-appendpos.",
				http.StatusBadRequest)
			return BlobObject{}, false
		}
		if want != b.Size {
			writeStorageError(w, "AppendPositionConditionNotMet",
				"The append position condition specified was not met.",
				http.StatusPreconditionFailed)
			return BlobObject{}, false
		}
	}
	if raw := r.Header.Get("x-ms-blob-condition-maxsize"); raw != "" {
		max, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeStorageError(w, "InvalidHeaderValue",
				"The value for one of the HTTP headers is not in the correct format: x-ms-blob-condition-maxsize.",
				http.StatusBadRequest)
			return BlobObject{}, false
		}
		if b.Size+incoming > max {
			writeStorageError(w, "MaxBlobSizeConditionNotMet",
				"The max blob size condition specified was not met.",
				http.StatusPreconditionFailed)
			return BlobObject{}, false
		}
	}
	return b, true
}

func handleAppendBlock(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if !blobBodyChecksums.exclusive(w, r) {
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	blobAppendChecked(w, r, account, container, blob, data, blobBodyChecksums)
}

func handleAppendBlockFromURL(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if !blobSourceChecksums.exclusive(w, r) {
		return
	}
	data, ok := blobReadCopySourceRange(w, r, r.Header.Get("x-ms-copy-source"), r.Header.Get("x-ms-source-range"))
	if !ok {
		return
	}
	blobAppendChecked(w, r, account, container, blob, data, blobSourceChecksums)
}

// blobAppendChecked appends data once it matches the checksums the request
// states in checksums' headers, and answers with the block's own checksums.
func blobAppendChecked(w http.ResponseWriter, r *http.Request, account, container, blob string, data []byte, checksums blobChecksumHeaders) {
	blockMD5 := blobstore.Digest(data).MD5Base64()
	crc := blobCRC64(data)
	if !checksums.match(w, r, blockMD5, crc) {
		return
	}
	b, ok := blobAppendBlobFor(w, r, account, container, blob, int64(len(data)))
	if !ok {
		return
	}
	b, existing, err := blobOpen(b)
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = existing.Close() }()
	offset := b.Size
	b.CommittedBlockCount++
	blobTouch(&b)
	digests, err := blobSetContentsFrom(&b, io.MultiReader(existing, bytes.NewReader(data)))
	if err != nil {
		writeStorageError(w, "InternalError", err.Error(), http.StatusInternalServerError)
		return
	}
	b.ContentMD5 = digests.MD5Base64()
	putBlobObject(b)

	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	checksums.respond(w, r, account, blockMD5, crc, blobVersionBothDigests)
	w.Header().Set("x-ms-blob-append-offset", strconv.FormatInt(offset, 10))
	w.Header().Set("x-ms-blob-committed-block-count", strconv.FormatInt(int64(b.CommittedBlockCount), 10))
	w.Header().Set("x-ms-request-server-encrypted", "true")
	w.WriteHeader(http.StatusCreated)
}

func handleAppendBlobSeal(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	blobDrainBody(r)
	b, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if b.BlobType != "AppendBlob" {
		writeStorageError(w, "InvalidBlobType",
			"The blob type is invalid for this operation.", http.StatusConflict)
		return
	}
	if !blobWriteAllowed(w, r, b, true, blobstore.Modify) {
		return
	}
	if raw := r.Header.Get("x-ms-blob-condition-appendpos"); raw != "" {
		want, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || want != b.Size {
			writeStorageError(w, "AppendPositionConditionNotMet",
				"The append position condition specified was not met.",
				http.StatusPreconditionFailed)
			return
		}
	}
	b.Sealed = true
	blobTouch(&b)
	putBlobObject(b)
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-blob-sealed", "true")
	w.WriteHeader(http.StatusOK)
}
