package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// blobBodies holds the contents of every blob, snapshot and staged or
// committed block. Each file belongs to exactly one row: a copy or a snapshot
// writes a file of its own, so releasing one row's contents never takes
// another's.
var blobBodies *blobstore.Payloads

// blobOpenBodies opens the payload store and adopts it; it runs once, as the
// Blob Storage slice registers and before anything is served.
func blobOpenBodies(srv *sim.Server) {
	bodies, err := srv.Payloads("azure-blob")
	if err != nil {
		log.Fatalf("blob contents: %v", err)
	}
	if err := blobAdoptBodies(bodies); err != nil {
		log.Fatalf("blob contents: %v", err)
	}
}

// blobAdoptBodies makes bodies the payload store, moves the contents rows
// written before it out of them, and removes the files no row references.
func blobAdoptBodies(bodies *blobstore.Payloads) error {
	blobBodies = bodies
	return bodies.Adopt(func(adoption *blobstore.Adoption) error {
		for _, row := range blobObjects.ListPrefix("") {
			b := row.Item
			if len(b.LegacyData) > 0 {
				ref, digests, err := adoption.Move(b.LegacyData)
				if err != nil {
					return fmt.Errorf("move the contents of %s out of its row: %w", row.ID, err)
				}
				b.Body, b.Size, b.LegacyData = ref, digests.Size, nil
				blobObjects.Put(row.ID, b)
			}
			adoption.Keep(b.Body)
		}
		for _, block := range blobBlocks.List() {
			moved := false
			if len(block.LegacyUncommittedData) > 0 {
				ref, digests, err := adoption.Move(block.LegacyUncommittedData)
				if err != nil {
					return fmt.Errorf("move staged block %s out of its row: %w", block.BlockID, err)
				}
				block.UncommittedBody, block.UncommittedSize, block.LegacyUncommittedData = ref, digests.Size, nil
				moved = true
			}
			if len(block.LegacyCommittedData) > 0 {
				ref, digests, err := adoption.Move(block.LegacyCommittedData)
				if err != nil {
					return fmt.Errorf("move committed block %s out of its row: %w", block.BlockID, err)
				}
				block.CommittedBody, block.CommittedSize, block.LegacyCommittedData = ref, digests.Size, nil
				moved = true
			}
			if moved {
				blobBlocks.Put(blobBlockKey(block.Account, block.Container, block.Blob, block.BlockID), block)
			}
			adoption.Keep(block.UncommittedBody, block.CommittedBody)
		}
		return nil
	})
}

// blobSetContentsFrom gives b the contents r yields and returns their
// digests. The row still has to be stored with putBlobObject, which releases
// the contents it replaces.
func blobSetContentsFrom(b *BlobObject, r io.Reader) (blobstore.Digests, error) {
	ref, digests, err := blobBodies.WriteFrom(r)
	if err != nil {
		return blobstore.Digests{}, err
	}
	b.Body, b.Size, b.LegacyData = ref, digests.Size, nil
	return digests, nil
}

// blobSetContents gives b data as its contents.
func blobSetContents(b *BlobObject, data []byte) (blobstore.Digests, error) {
	return blobSetContentsFrom(b, bytes.NewReader(data))
}

// blobOpen opens b's contents for reading, returning the blob they belong to,
// which is a newer one when b was overwritten before its file was opened.
func blobOpen(b BlobObject) (BlobObject, blobstore.Reader, error) {
	return blobstore.OpenCurrent(blobBodies, b,
		func(b BlobObject) string { return b.Body },
		func(b BlobObject) (BlobObject, bool) { return blobObjects.Get(blobObjectKeyOf(b)) },
		blobObjectKeyOf(b))
}

// blobData returns b's whole contents together with the blob they belong to.
func blobData(b BlobObject) (BlobObject, []byte, error) {
	current, reader, err := blobOpen(b)
	if err != nil {
		return b, nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	return current, data, err
}

// blobCopyContents gives the contents of src a payload of their own,
// returning the blob they were read from, which is a newer one when src was
// overwritten before its file was opened.
func blobCopyContents(src BlobObject) (BlobObject, string, blobstore.Digests, error) {
	current, reader, err := blobOpen(src)
	if err != nil {
		return src, "", blobstore.Digests{}, err
	}
	defer func() { _ = reader.Close() }()
	ref, digests, err := blobBodies.WriteFrom(reader)
	return current, ref, digests, err
}

// blobZeros reads as an endless run of zero bytes.
type blobZeros struct{}

func (blobZeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// blobContentMD5Matches checks the Content-MD5 a write states against the
// contents that arrived, writing Md5Mismatch and returning false when they
// differ.
func blobContentMD5Matches(w http.ResponseWriter, r *http.Request, digests blobstore.Digests) bool {
	stated := r.Header.Get("Content-MD5")
	if stated == "" || stated == digests.MD5Base64() {
		return true
	}
	writeStorageError(w, "Md5Mismatch",
		"The MD5 value specified in the request did not match with the MD5 value calculated by the server.",
		http.StatusBadRequest)
	return false
}

// blobReleaseBody releases contents no row references any more. A failure
// leaves an unreferenced file, which the next start's sweep removes; the
// write that replaced or deleted the row has already happened.
func blobReleaseBody(ref string) {
	if err := blobBodies.Remove(ref); err != nil {
		log.Printf("blob: release contents %s: %v", ref, err)
	}
}

// blobEditContents replaces b's contents with what edit makes of them. The
// row still has to be stored with putBlobObject.
func blobEditContents(b *BlobObject, edit func(data []byte) []byte) error {
	_, data, err := blobData(*b)
	if err != nil {
		return err
	}
	_, err = blobSetContents(b, edit(data))
	return err
}

// putBlobWithContents stores b with data as its contents, releasing the
// contents of the blob it replaces. It is how a service other than the Blob
// data plane writes a blob, so it takes the blob's write lock the data plane
// takes for every write: a data-plane edit that read the row before this write
// must not store it back over it.
func putBlobWithContents(b BlobObject, data []byte) error {
	defer blobWriters.Lock(blobObjectKey(b.Account, b.Container, b.Name))()
	if _, err := blobSetContents(&b, data); err != nil {
		return err
	}
	putBlobObject(b)
	return nil
}
