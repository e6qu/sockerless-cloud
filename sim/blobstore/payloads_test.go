package blobstore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPayloadsWriteReadRangeRemove(t *testing.T) {
	payloads, err := OpenPayloads(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := payloads.Write([]byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := payloads.Write([]byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two writes returned one reference; a rewrite would replace a file a reader holds")
	}
	file, err := payloads.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	part := make([]byte, 3)
	if _, err := file.ReadAt(part, 4); err != nil || string(part) != "456" {
		t.Fatalf("ranged read = %q, %v", part, err)
	}
	if err := payloads.Remove(first); err != nil {
		t.Fatal(err)
	}
	// An open file stays readable after its payload is removed, so a reader
	// that opened it before an overwrite finishes the read it began.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(file); err != nil || string(rest) != "0123456789" {
		t.Fatalf("read after removal = %q, %v", rest, err)
	}
	_ = file.Close()
	if _, err := payloads.Open(first); !errors.Is(err, ErrPayloadGone) {
		t.Fatalf("opening a removed payload = %v, want ErrPayloadGone", err)
	}
	if err := payloads.Remove(first); err != nil {
		t.Fatalf("removing a payload twice = %v", err)
	}
	if data, err := payloads.Read(second); err != nil || string(data) != "0123456789" {
		t.Fatalf("Read = %q, %v", data, err)
	}
}

func TestPayloadsRefuseAReferenceThatIsNotOne(t *testing.T) {
	payloads, err := OpenPayloads(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"../../etc/passwd", "0123456789abcdef0123456789abcdeF", "0123456789abcdef"} {
		if _, err := payloads.Open(ref); err == nil || errors.Is(err, ErrPayloadGone) {
			t.Errorf("Open(%q) = %v, want a malformed-reference error", ref, err)
		}
	}
}

func TestPayloadsSweepKeepsExactlyTheReferencedFiles(t *testing.T) {
	dir := t.TempDir()
	payloads, err := OpenPayloads(dir)
	if err != nil {
		t.Fatal(err)
	}
	kept, _, err := payloads.Write([]byte("kept"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, _, err := payloads.Write([]byte("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".write-123"), []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := payloads.Sweep(func(ref string) bool { return ref == kept })
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("Sweep removed %d files, want the orphan and the interrupted write", removed)
	}
	if _, err := payloads.Read(kept); err != nil {
		t.Errorf("the referenced payload was swept: %v", err)
	}
	if _, err := payloads.Open(orphan); !errors.Is(err, ErrPayloadGone) {
		t.Errorf("the orphan survived: %v", err)
	}
}
