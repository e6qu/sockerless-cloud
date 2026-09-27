package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A database written before contents left the rows holds them under "Data",
// "UncommittedData" and "CommittedData". Adopting the payload store moves each
// out, keeps it readable, and removes the files no row references.
func TestBlobAdoptBodiesMovesRowContentsOutAndSweepsOrphans(t *testing.T) {
	objects, blocks, bodies := blobObjects, blobBlocks, blobBodies
	t.Cleanup(func() { blobObjects, blobBlocks, blobBodies = objects, blocks, bodies })
	blobObjects = sim.MakeStore[BlobObject](nil, "blob_objects")
	blobBlocks = sim.MakeStore[BlobBlockData](nil, "blob_blocks")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000000000000000000"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := BlobObject{Account: "acct", Container: "c", Name: "b", LegacyData: []byte("base bytes")}
	snapshot := base
	snapshot.Snapshot, snapshot.LegacyData = "2026-09-27T00:00:00.0000000Z", []byte("older bytes")
	blobObjects.Put(blobObjectKeyOf(base), base)
	blobObjects.Put(blobObjectKeyOf(snapshot), snapshot)
	blobBlocks.Put(blobBlockKey("acct", "c", "b", "blk"), BlobBlockData{
		Account: "acct", Container: "c", Blob: "b", BlockID: "blk",
		LegacyUncommittedData: []byte("staged"), LegacyCommittedData: []byte("committed"),
		HasUncommitted: true, HasCommitted: true,
	})

	store, err := sim.OpenPayloads(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobAdoptBodies(store); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{blobObjectKeyOf(base): "base bytes", blobObjectKeyOf(snapshot): "older bytes"} {
		b, _ := blobObjects.Get(key)
		if b.LegacyData != nil || b.Body == "" || b.Size != int64(len(want)) {
			t.Fatalf("%s kept its contents in the row: %+v", key, b)
		}
		if _, data, err := blobData(b); err != nil || string(data) != want {
			t.Fatalf("%s reads %q, %v; want %q", key, data, err, want)
		}
	}
	block, _ := blobBlocks.Get(blobBlockKey("acct", "c", "b", "blk"))
	if block.LegacyUncommittedData != nil || block.LegacyCommittedData != nil {
		t.Fatalf("the block kept its contents in the row: %+v", block)
	}
	for ref, want := range map[string]string{block.UncommittedBody: "staged", block.CommittedBody: "committed"} {
		if data, err := blockData(ref); err != nil || string(data) != want {
			t.Fatalf("block contents %q, %v; want %q", data, err, want)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("%d payload files remain, want two blobs' and two block contents", len(entries))
	}
}
