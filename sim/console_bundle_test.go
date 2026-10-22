package sim

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestConsoleBundleServesADirectoryWithItsIndex(t *testing.T) {
	dir := t.TempDir()
	if _, err := ConsoleBundle(dir); err == nil {
		t.Fatal("a directory with no index.html opened as a console bundle")
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle, err := ConsoleBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := fs.ReadFile(bundle, "index.html"); err != nil || string(data) != "<!doctype html>" {
		t.Fatalf("index.html = %q, %v", data, err)
	}
}
