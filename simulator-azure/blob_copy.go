package main

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// The copy family. Azure addresses Copy Blob and Copy Blob From URL at the bare
// PUT carrying `x-ms-copy-source` (handleCopyBlob), and the specification models
// them under the `?comp=copy` key that Abort Copy Blob shares with
// `&copyid=<id>`. Both spellings reach the same machinery here.

// handleBlobCompCopy resolves the `?comp=copy` family: with a `copyid` query and
// `x-ms-copy-action: abort` it is Abort Copy Blob, otherwise it is Copy Blob.
func handleBlobCompCopy(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if copyID := r.URL.Query().Get("copyid"); copyID != "" {
		handleAbortCopyBlob(w, r, account, container, blob, copyID)
		return
	}
	if r.Header.Get("x-ms-copy-source") == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-copy-source.",
			http.StatusBadRequest)
		return
	}
	handleCopyBlob(w, r, account, container, blob)
}

func handleAbortCopyBlob(w http.ResponseWriter, r *http.Request, account, container, blob, copyID string) {
	if action := r.Header.Get("x-ms-copy-action"); action != "" && action != "abort" {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: x-ms-copy-action.",
			http.StatusBadRequest)
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
	if b.CopyID != copyID {
		writeStorageError(w, "InvalidHeaderValue",
			"The specified copy ID did not match the copy ID for the pending copy operation.",
			http.StatusConflict)
		return
	}
	// The simulator's copies complete within the request that starts them, so
	// an abort always arrives after the copy has succeeded — which is exactly
	// the state Azure refuses to abort.
	if b.CopyStatus == "success" {
		writeStorageError(w, "NoPendingCopyOperation",
			"There is currently no pending copy operation.", http.StatusConflict)
		return
	}
	b.CopyStatus = "aborted"
	b.CopyStatusDescription = "The copy operation was aborted by the client."
	putBlobObject(b)
	w.WriteHeader(http.StatusNoContent)
}

// handlePageBlobCopyIncremental implements Incremental Copy Blob: the
// destination page blob takes the source snapshot's bytes and records the copy
// as an incremental one, and the operation itself produces a destination
// snapshot — the coordinate Azure returns in x-ms-copy-destination-snapshot.
func handlePageBlobCopyIncremental(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	sourceURL := r.Header.Get("x-ms-copy-source")
	if sourceURL == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-copy-source.",
			http.StatusBadRequest)
		return
	}
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	srcAccount, srcContainer, srcBlob, ok := parseBlobCopySource(sourceURL)
	if !ok {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source is invalid.", http.StatusNotFound)
		return
	}
	snapshot := blobCopySourceSnapshot(sourceURL)
	if snapshot == "" {
		// Incremental copy is defined only from a snapshot of the source.
		writeStorageError(w, "CannotVerifyCopySource",
			"The source of an incremental copy must be a snapshot.", http.StatusBadRequest)
		return
	}
	source, ok := blobObjects.Get(blobSnapshotKey(srcAccount, srcContainer, srcBlob, snapshot))
	if !ok || source.Deleted {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source does not exist.", http.StatusNotFound)
		return
	}
	if source.BlobType != "PageBlob" {
		writeStorageError(w, "InvalidBlobType",
			"The blob type is invalid for this operation.", http.StatusConflict)
		return
	}

	existing, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if exists && existing.Deleted {
		exists, existing = false, BlobObject{}
	}
	if exists && !blobLeaseAccessOK(w, r, existing.Lease, "blob") {
		return
	}

	completion := blobNowHTTP()
	copyID := generateUUID()
	dst := source
	dst.Account, dst.Container, dst.Name, dst.Snapshot = account, container, blob, ""
	dst.Data = append([]byte(nil), source.Data...)
	dst.PageRanges = append([]BlobPageRange(nil), source.PageRanges...)
	dst.Metadata = cloneBlobMetadata(source.Metadata)
	dst.Tags = cloneBlobMetadata(source.Tags)
	dst.Lease = existing.Lease
	dst.IncrementalCopy = true
	dst.CopyID = copyID
	dst.CopyStatus = "success"
	dst.CopySource = sourceURL
	dst.CopyProgress = fmt.Sprintf("%d/%d", len(dst.Data), len(dst.Data))
	dst.CopyCompletionTime = completion
	blobTouch(&dst)

	// The destination snapshot the incremental copy produces is what a caller
	// reads the copied state back from.
	destSnapshot := dst
	destSnapshot.Snapshot = blobSnapshotStamp(time.Now())
	destSnapshot.Lease = BlobLease{}
	dst.CopyDestinationSnapshot = destSnapshot.Snapshot
	putBlobObject(destSnapshot)
	putBlobObject(dst)

	w.Header().Set("ETag", dst.ETag)
	w.Header().Set("Last-Modified", dst.LastModified)
	w.Header().Set("x-ms-copy-id", copyID)
	w.Header().Set("x-ms-copy-status", "success")
	w.WriteHeader(http.StatusAccepted)
}

// handleStageBlockFromURL implements Put Block From URL: the block's bytes come
// from a range of a source blob instead of the request body.
func handleStageBlockFromURL(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	blockID := r.URL.Query().Get("blockid")
	if blockID == "" {
		writeStorageError(w, "MissingRequiredQueryParameter",
			"A query parameter that's mandatory for this request is not specified: blockid.",
			http.StatusBadRequest)
		return
	}
	data, ok := blobReadCopySourceRange(w, r, r.Header.Get("x-ms-copy-source"), r.Header.Get("x-ms-source-range"))
	if !ok {
		return
	}
	key := blobBlockKey(account, container, blob, blockID)
	block, _ := blobBlocks.Get(key)
	block.Account, block.Container, block.Blob, block.BlockID = account, container, blob, blockID
	block.UncommittedData = data
	block.HasUncommitted = true
	putBlobBlock(account, container, blob, blockID, block)
	w.Header().Set("Content-MD5", blobContentMD5(data))
	w.Header().Set("x-ms-request-server-encrypted", "true")
	w.WriteHeader(http.StatusCreated)
}

// blobReadCopySourceRange resolves the bytes a `x-ms-copy-source` +
// `x-ms-source-range` pair names, reading them out of the simulator's own store
// the way the service reads them out of its own.
func blobReadCopySourceRange(w http.ResponseWriter, r *http.Request, sourceURL, sourceRange string) ([]byte, bool) {
	if sourceURL == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-copy-source.",
			http.StatusBadRequest)
		return nil, false
	}
	srcAccount, srcContainer, srcBlob, ok := parseBlobCopySource(sourceURL)
	if !ok {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source is invalid.", http.StatusNotFound)
		return nil, false
	}
	source, ok := blobObjects.Get(blobSnapshotKey(srcAccount, srcContainer, srcBlob, blobCopySourceSnapshot(sourceURL)))
	if !ok || source.Deleted {
		writeStorageError(w, "CannotVerifyCopySource",
			"The specified copy source does not exist.", http.StatusNotFound)
		return nil, false
	}
	if sourceRange == "" {
		return append([]byte(nil), source.Data...), true
	}
	start, end, ok := parseBlobByteRange(sourceRange)
	if !ok || start < 0 || start > end || end >= int64(len(source.Data)) {
		writeStorageError(w, "InvalidRange",
			"The range specified is invalid for the current size of the resource.",
			http.StatusRequestedRangeNotSatisfiable)
		return nil, false
	}
	return append([]byte(nil), source.Data[start:end+1]...), true
}

// blobDrainBody consumes and discards a request body, so a handler that ignores
// the body still leaves the connection in a reusable state.
func blobDrainBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
}
