package main

import (
	"net/http"
	"strings"
	"time"
)

// Snapshot Blob, Undelete Blob and Restore Container.
//
// A snapshot is a real second copy of the blob's bytes and properties, stored
// under the `?snapshot=<timestamp>` coordinate Azure addresses it at, so a
// GET/HEAD/DELETE carrying that query reads or removes the snapshot rather than
// the base blob and a listing with `include=snapshots` enumerates it.
//
// Undelete Blob and Restore Container are meaningful because the simulator
// implements the retention model that produces something to undelete: with the
// account's blob service properties declaring a DeleteRetentionPolicy (or a
// ContainerDeleteRetentionPolicy), a delete retains the row, marks it deleted
// with its remaining retention days, and hides it from every operation but a
// listing that asked to include deleted rows.

func handleCreateBlobSnapshot(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	base, exists := blobObjects.Get(blobObjectKey(account, container, blob))
	if !exists || base.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if !azureBlobPreconditionOK(w, r, base.ETag, true) {
		return
	}
	if !blobLeaseAccessOK(w, r, base.Lease, "blob") {
		return
	}

	snap := base
	snap.Snapshot = blobSnapshotStamp(time.Now())
	snap.Data = append([]byte(nil), base.Data...)
	snap.PageRanges = append([]BlobPageRange(nil), base.PageRanges...)
	snap.Metadata = cloneBlobMetadata(base.Metadata)
	snap.Tags = cloneBlobMetadata(base.Tags)
	// A snapshot carries no lease of its own; leases live on the base blob.
	snap.Lease = BlobLease{}
	// Snapshot metadata comes from the request when it supplies any, and is
	// inherited from the base blob otherwise.
	if md := collectMetadata(r); len(md) > 0 {
		snap.Metadata = md
	}
	putBlobObject(snap)

	w.Header().Set("x-ms-snapshot", snap.Snapshot)
	w.Header().Set("ETag", snap.ETag)
	w.Header().Set("Last-Modified", snap.LastModified)
	w.Header().Set("x-ms-request-server-encrypted", "true")
	w.WriteHeader(http.StatusCreated)
}

func handleUndeleteBlob(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	var restored bool
	var found bool
	for _, b := range blobsInContainer(account, container) {
		if b.Name != blob {
			continue
		}
		found = true
		if !b.Deleted {
			continue
		}
		b.Deleted = false
		b.DeletedTime = ""
		b.RemainingRetentionDays = 0
		putBlobObject(b)
		restored = true
	}
	if !found {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	// Undeleting a blob that is not soft-deleted is a no-op success on Azure,
	// so a caller can make the call unconditionally.
	_ = restored
	w.WriteHeader(http.StatusOK)
}

func handleRestoreContainer(w http.ResponseWriter, r *http.Request, account, container string) {
	name := r.Header.Get("x-ms-deleted-container-name")
	version := r.Header.Get("x-ms-deleted-container-version")
	if name == "" || version == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-deleted-container-name/x-ms-deleted-container-version.",
			http.StatusBadRequest)
		return
	}
	deletedKey := blobDeletedContainerKey(account, name, version)
	c, ok := blobContainersData.Get(deletedKey)
	if !ok || !c.Deleted {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	liveKey := blobContainerKey(account, container)
	if _, exists := blobContainersData.Get(liveKey); exists {
		writeStorageError(w, "ContainerAlreadyExists",
			"The specified container already exists.", http.StatusConflict)
		return
	}
	blobContainersData.Delete(deletedKey)
	c.Name = container
	c.Deleted = false
	c.DeletedTime = ""
	c.RemainingRetentionDays = 0
	blobContainersData.Put(liveKey, c)
	w.Header().Set("ETag", c.ETag)
	w.Header().Set("Last-Modified", c.Created)
	w.WriteHeader(http.StatusCreated)
}

// handleRenameContainer implements Rename Container: the container's rows move
// to the new name, taking their blobs with them.
func handleRenameContainer(w http.ResponseWriter, r *http.Request, account, container string) {
	source := r.Header.Get("x-ms-source-container-name")
	if source == "" {
		writeStorageError(w, "MissingRequiredHeader",
			"An HTTP header that's mandatory for this request is not specified: x-ms-source-container-name.",
			http.StatusBadRequest)
		return
	}
	srcKey := blobContainerKey(account, source)
	src, ok := blobContainersData.Get(srcKey)
	if !ok {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	if leaseID := r.Header.Get("x-ms-source-lease-id"); leaseID != "" || blobLeaseHeld(src.Lease, time.Now()) {
		if !strings.EqualFold(leaseID, src.Lease.ID) {
			writeStorageError(w, blobLeaseMismatchCode("container"),
				"The lease ID specified did not match the lease ID for the container.",
				http.StatusPreconditionFailed)
			return
		}
	}
	dstKey := blobContainerKey(account, container)
	if _, exists := blobContainersData.Get(dstKey); exists {
		writeStorageError(w, "ContainerAlreadyExists",
			"The specified container already exists.", http.StatusConflict)
		return
	}

	blobs := blobsInContainer(account, source)
	blocks := blockKeysInContainer(account, source)
	staged := make([]BlobBlockData, 0, len(blocks))
	for _, key := range blocks {
		if bl, ok := blobBlocks.Get(key); ok {
			staged = append(staged, bl)
		}
	}
	for _, b := range blobs {
		deleteBlobSnapshot(b.Account, b.Container, b.Name, b.Snapshot)
	}
	for _, bl := range staged {
		deleteBlobBlock(bl.Account, bl.Container, bl.Blob, bl.BlockID)
	}
	blobContainersData.Delete(srcKey)

	src.Name = container
	src.Lease = BlobLease{}
	blobContainersData.Put(dstKey, src)
	for _, b := range blobs {
		b.Container = container
		putBlobObject(b)
	}
	for _, bl := range staged {
		bl.Container = container
		putBlobBlock(bl.Account, bl.Container, bl.Blob, bl.BlockID, bl)
	}

	w.Header().Set("ETag", src.ETag)
	w.Header().Set("Last-Modified", src.Created)
	w.WriteHeader(http.StatusOK)
}
