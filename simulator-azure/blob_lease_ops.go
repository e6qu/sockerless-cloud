package main

import (
	"net/http"
)

// Lease Blob and Lease Container — the five verbs Azure spells with
// `x-ms-lease-action` on `?comp=lease` (`?restype=container&comp=lease` for a
// container). The lease a verb establishes is stored on the resource itself, so
// it survives a restart, and every write path consults it: a write to a leased
// resource without the matching `x-ms-lease-id` is refused with the same 412 the
// real service returns.

func handleBlobLease(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	req, ok := parseBlobLeaseRequest(w, r)
	if !ok {
		return
	}
	key := blobObjectKey(account, container, blob)
	b, exists := blobObjects.Get(key)
	if !exists || b.Deleted {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	next, status, headers, ok := applyBlobLeaseAction(w, req, b.Lease, "blob")
	if !ok {
		return
	}
	b.Lease = next
	putBlobObject(b)

	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.WriteHeader(status)
}

func handleContainerLease(w http.ResponseWriter, r *http.Request, account, container string) {
	req, ok := parseBlobLeaseRequest(w, r)
	if !ok {
		return
	}
	key := blobContainerKey(account, container)
	c, exists := blobContainersData.Get(key)
	if !exists {
		writeStorageError(w, "ContainerNotFound",
			"The specified container does not exist.", http.StatusNotFound)
		return
	}
	next, status, headers, ok := applyBlobLeaseAction(w, req, c.Lease, "container")
	if !ok {
		return
	}
	c.Lease = next
	blobContainersData.Put(key, c)

	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("ETag", c.ETag)
	w.Header().Set("Last-Modified", c.Created)
	w.WriteHeader(status)
}
