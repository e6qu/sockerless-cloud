package blobstore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPayloadsWriteFromDigestsWhatItStores(t *testing.T) {
	payloads, err := OpenPayloads(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, digests, err := payloads.WriteFrom(strings.NewReader("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if digests.Size != 11 || digests.MD5Hex() != "5eb63bbbe01eeed093cb22bb8f5acdc3" {
		t.Fatalf("digests = %d %s", digests.Size, digests.MD5Hex())
	}
	// The CRC32C Cloud Storage reports for "hello world".
	if digests.CRC32CBase64() != "yZRlqg==" {
		t.Fatalf("CRC32C = %s", digests.CRC32CBase64())
	}
	if again, err := payloads.Digest(ref); err != nil || again != digests {
		t.Fatalf("Digest = %+v, %v; want %+v", again, err, digests)
	}
	if Digest([]byte("hello world")) != digests {
		t.Fatal("Digest of the bytes differs from the digests of their write")
	}
}

func TestPayloadsEmptyReferenceIsTheEmptyPayload(t *testing.T) {
	dir := t.TempDir()
	payloads, err := OpenPayloads(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, digests, err := payloads.Write(nil)
	if err != nil || ref != "" || digests.Size != 0 {
		t.Fatalf("an empty write = %q, %+v, %v", ref, digests, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("an empty write left %d files", len(entries))
	}
	if data, err := payloads.Read(""); err != nil || len(data) != 0 {
		t.Fatalf("Read(\"\") = %q, %v", data, err)
	}
}

func TestPayloadsConcatStreamsPartsIntoANewPayload(t *testing.T) {
	payloads, err := OpenPayloads(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, part := range []string{"abc", "", "defg"} {
		ref, _, err := payloads.Write([]byte(part))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	joined, digests, err := payloads.Concat(refs...)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := payloads.Read(joined); string(data) != "abcdefg" || digests != Digest([]byte("abcdefg")) {
		t.Fatalf("Concat = %q, %+v", data, digests)
	}
	if err := payloads.Remove(refs[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := payloads.Concat(refs...); !errors.Is(err, ErrPayloadGone) {
		t.Fatalf("Concat over a removed part = %v, want ErrPayloadGone", err)
	}
}

func TestPayloadsWriteAtAssemblesAStagingPayload(t *testing.T) {
	payloads, err := OpenPayloads(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, size, err := payloads.WriteAt("", 0, strings.NewReader("0123"))
	if err != nil || size != 4 {
		t.Fatalf("first chunk = %d, %v", size, err)
	}
	same, size, err := payloads.WriteAt(ref, 2, strings.NewReader("23456"))
	if err != nil || same != ref || size != 7 {
		t.Fatalf("overlapping chunk = %q %d, %v", same, size, err)
	}
	if err := payloads.Truncate(ref, 6); err != nil {
		t.Fatal(err)
	}
	if data, _ := payloads.Read(ref); string(data) != "012345" {
		t.Fatalf("staged = %q", data)
	}
}

func TestPayloadsMaterializeReplacesTheDestinationWithACopy(t *testing.T) {
	payloads, err := OpenPayloads(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "object")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	ref, _, err := payloads.Write([]byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := payloads.Materialize(ref, dest); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dest); string(data) != "new" {
		t.Fatalf("dest = %q", data)
	}
	// A reader that opened the old file keeps reading the old bytes.
	if data, _ := io.ReadAll(held); string(data) != "old" {
		t.Fatalf("the held file reads %q", data)
	}
	if err := os.WriteFile(dest, []byte("written through the mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := payloads.Read(ref); string(data) != "new" {
		t.Fatalf("a write to the copy reached the payload: %q", data)
	}
}

func TestPayloadsAdoptMovesInlineContentsAndSweepsTheRest(t *testing.T) {
	dir := t.TempDir()
	payloads, err := OpenPayloads(dir)
	if err != nil {
		t.Fatal(err)
	}
	kept, _, err := payloads.Write([]byte("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := payloads.Write([]byte("orphan")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	var moved, adopted string
	err = payloads.Adopt(func(a *Adoption) error {
		a.Keep(kept)
		var err error
		if moved, _, err = a.Move([]byte("inline")); err != nil {
			return err
		}
		adopted, _, err = a.MoveFrom(outside)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{kept: "kept", moved: "inline", adopted: "outside"} {
		if data, err := payloads.Read(ref); err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", ref, data, err, want)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Fatalf("%d payloads remain, want the three kept", len(entries))
	}
}

func TestOpenCurrentFollowsAnOverwrite(t *testing.T) {
	payloads, err := OpenPayloads(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ body string }
	old, _, _ := payloads.Write([]byte("old"))
	replacement, _, _ := payloads.Write([]byte("replacement"))
	stored := row{body: replacement}
	if err := payloads.Remove(old); err != nil {
		t.Fatal(err)
	}
	ref := func(r row) string { return r.body }
	reload := func(row) (row, bool) { return stored, true }
	current, reader, err := OpenCurrent(payloads, row{body: old}, ref, reload, "object")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if data, _ := io.ReadAll(reader); current != stored || string(data) != "replacement" {
		t.Fatalf("OpenCurrent = %+v %q", current, data)
	}
	gone := func(row) (row, bool) { return row{}, false }
	if _, _, err := OpenCurrent(payloads, row{body: old}, ref, gone, "object"); !errors.Is(err, ErrPayloadGone) {
		t.Fatalf("OpenCurrent of a deleted row = %v", err)
	}
}
