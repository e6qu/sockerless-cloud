package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A database written before bodies left the rows holds them under "Data".
// Adopting the payload store moves each one out, keeps it readable, and
// removes the files no row references.
func TestS3AdoptBodiesMovesRowContentsOutAndSweepsOrphans(t *testing.T) {
	objects, uploads, bodies := s3Objects, s3MultipartUploads, s3Bodies
	t.Cleanup(func() { s3Objects, s3MultipartUploads, s3Bodies = objects, uploads, bodies })
	s3Objects = sim.MakeStore[S3Object](nil, "s3_objects")
	s3MultipartUploads = sim.MakeStore[S3MultipartUpload](nil, "s3_multipart_uploads")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000000000000000000"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	s3Objects.Put("bucket/legacy", S3Object{Key: "bucket/legacy", LegacyData: []byte("old contents"), Size: 12})
	s3Objects.Put("bucket/empty", S3Object{Key: "bucket/empty"})
	s3MultipartUploads.Put("upload", S3MultipartUpload{UploadID: "upload", Bucket: "bucket", Key: "big",
		Parts: map[int]s3MultipartPart{1: {LegacyData: []byte("part one"), ETag: `"x"`}}})

	store, err := sim.OpenPayloads(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s3AdoptBodies(store); err != nil {
		t.Fatal(err)
	}

	legacy, _ := s3Objects.Get("bucket/legacy")
	if legacy.LegacyData != nil || legacy.Body == "" {
		t.Fatalf("the contents stayed in the row: %+v", legacy)
	}
	if data, err := s3ObjectData(legacy); err != nil || string(data) != "old contents" {
		t.Fatalf("migrated contents = %q, %v", data, err)
	}
	if data, err := s3ObjectData(S3Object{Key: "bucket/empty"}); err != nil || len(data) != 0 {
		t.Fatalf("an empty object reads %q, %v", data, err)
	}
	upload, _ := s3MultipartUploads.Get("upload")
	part := upload.Parts[1]
	if part.LegacyData != nil || part.Size != 8 {
		t.Fatalf("the part stayed in the row: %+v", part)
	}
	if data, err := s3PartData(part); err != nil || string(data) != "part one" {
		t.Fatalf("migrated part = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "00000000000000000000000000000000")); !os.IsNotExist(err) {
		t.Fatalf("the orphan survived adoption: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d payload files remain, want the object's and the part's", len(entries))
	}
}
