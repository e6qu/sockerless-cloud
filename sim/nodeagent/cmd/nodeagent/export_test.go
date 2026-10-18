package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// An ext4 image's tree comes back out of dumpExt4 and tarTree as it went in,
// with its directories, files, modes and symlinks; lost+found stays behind.
func TestExportReadsAnExt4ImageWithoutMountingIt(t *testing.T) {
	tree := t.TempDir()
	for _, dir := range []string{"etc", "usr/bin", "var/lib/app"} {
		if err := os.MkdirAll(filepath.Join(tree, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tree, "etc", "hostname"), []byte("guest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "usr", "bin", "tool"), []byte("#!/bin/sh\necho tool\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "var", "lib", "app", "state"), bytes.Repeat([]byte("s"), 70_000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("usr/bin", filepath.Join(tree, "bin")); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), "rootfs.ext4")
	if err := createExt4(context.Background(), tree, image); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "export")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := dumpExt4(context.Background(), image, out); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := tarTree(context.Background(), out, &archive); err != nil {
		t.Fatal(err)
	}
	got := map[string]*tar.Header{}
	contents := map[string][]byte{}
	tr := tar.NewReader(&archive)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Clean(hdr.Name)
		got[name] = hdr
		if hdr.Typeflag == tar.TypeReg {
			data, _ := io.ReadAll(tr)
			contents[name] = data
		}
	}
	if _, ok := got["lost+found"]; ok {
		t.Fatal("lost+found was exported")
	}
	if string(contents["etc/hostname"]) != "guest\n" {
		t.Fatalf("etc/hostname is %q", contents["etc/hostname"])
	}
	if hdr := got["usr/bin/tool"]; hdr == nil || hdr.Mode&0o111 == 0 {
		t.Fatalf("usr/bin/tool is %+v", hdr)
	}
	if len(contents["var/lib/app/state"]) != 70_000 {
		t.Fatalf("var/lib/app/state holds %d bytes", len(contents["var/lib/app/state"]))
	}
	if hdr := got["bin"]; hdr == nil || hdr.Typeflag != tar.TypeSymlink || hdr.Linkname != "usr/bin" {
		t.Fatalf("bin is %+v", hdr)
	}
}
