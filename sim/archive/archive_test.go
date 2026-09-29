package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name string
	mode fs.FileMode
	body string
}

func buildZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTar(t *testing.T, gz bool, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	var out = &buf
	var gzw *gzip.Writer
	tw := tar.NewWriter(out)
	if gz {
		gzw = gzip.NewWriter(out)
		tw = tar.NewWriter(gzw)
	}
	for _, h := range hdrs {
		body := h.Linkname
		if h.Typeflag == tar.TypeReg {
			body = h.Uname
			h.Uname = ""
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if gzw != nil {
		if err := gzw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// regular builds a tar header for a regular file, carrying its body in Uname
// for buildTar.
func regular(name, body string) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Uname: body}
}

// siblingDirs returns a destination "<base>/a" and its sibling "<base>/ab",
// the pair a string-prefix containment check confuses.
func siblingDirs(t *testing.T) (dest, sibling string) {
	t.Helper()
	base := t.TempDir()
	dest = filepath.Join(base, "a")
	sibling = filepath.Join(base, "ab")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	return dest, sibling
}

func assertAbsent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s exists after a refused extraction (err %v)", p, err)
	}
}

func TestExtractTarRefusesSiblingPrefixTraversal(t *testing.T) {
	dest, sibling := siblingDirs(t)
	data := buildTar(t, true, regular("../ab/owned", "x"))
	if err := ExtractTar(bytes.NewReader(data), dest, 1<<20); err == nil {
		t.Fatal("an entry naming the destination's sibling directory was extracted")
	}
	assertAbsent(t, filepath.Join(sibling, "owned"))
}

func TestExtractZipRefusesSiblingPrefixTraversal(t *testing.T) {
	dest, sibling := siblingDirs(t)
	data := buildZip(t, zipEntry{name: "../ab/owned", body: "x"})
	if err := ExtractZip(data, dest, 1<<20); err == nil {
		t.Fatal("an entry naming the destination's sibling directory was extracted")
	}
	assertAbsent(t, filepath.Join(sibling, "owned"))
}

func TestExtractRefusesDotDotEntries(t *testing.T) {
	for _, name := range []string{"..", "../x", "a/../../x", "a/b/../../../x"} {
		dest := t.TempDir()
		if err := ExtractZip(buildZip(t, zipEntry{name: name, body: "x"}), dest, 1<<20); err == nil {
			t.Errorf("zip entry %q was extracted", name)
		}
		if err := ExtractTar(bytes.NewReader(buildTar(t, false, regular(name, "x"))), dest, 1<<20); err == nil {
			t.Errorf("tar entry %q was extracted", name)
		}
	}
}

func TestExtractPlacesAbsoluteNamesUnderTheRoot(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "abs")
	dest := t.TempDir()
	if err := ExtractZip(buildZip(t, zipEntry{name: outside + "/z", body: "zip"}), dest, 1<<20); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	if err := ExtractTar(bytes.NewReader(buildTar(t, false, regular(outside+"/t", "tar"))), dest, 1<<20); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}
	assertAbsent(t, outside)
	for name, want := range map[string]string{"z": "zip", "t": "tar"} {
		got, err := os.ReadFile(filepath.Join(dest, outside, name))
		if err != nil || string(got) != want {
			t.Fatalf("absolute entry %s did not land under the root: %q, %v", name, got, err)
		}
	}
}

func TestExtractRefusesWritesThroughEscapingSymlinks(t *testing.T) {
	for _, target := range []string{"/", "../..", "../outside"} {
		t.Run(target, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, d := range []string{dest, outside} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			linkTarget := target
			if target == "/" {
				linkTarget = outside
			}
			zipData := buildZip(t,
				zipEntry{name: "evil", mode: fs.ModeSymlink | 0o777, body: linkTarget},
				zipEntry{name: "evil/owned", body: "x"})
			if err := ExtractZip(zipData, dest, 1<<20); err == nil {
				t.Fatal("zip: a file was written through a symbolic link leaving the root")
			}
			assertAbsent(t, filepath.Join(outside, "owned"))

			dest2 := filepath.Join(base, "root2")
			if err := os.Mkdir(dest2, 0o755); err != nil {
				t.Fatal(err)
			}
			tarData := buildTar(t, true,
				&tar.Header{Typeflag: tar.TypeSymlink, Name: "evil", Linkname: linkTarget},
				regular("evil/owned", "x"))
			if err := ExtractTar(bytes.NewReader(tarData), dest2, 1<<20); err == nil {
				t.Fatal("tar: a file was written through a symbolic link leaving the root")
			}
			assertAbsent(t, filepath.Join(outside, "owned"))
		})
	}
}

func TestExtractRecreatesSymlinksAndHardLinks(t *testing.T) {
	dest := t.TempDir()
	data := buildTar(t, false,
		&tar.Header{Typeflag: tar.TypeDir, Name: "lib/", Mode: 0o755},
		regular("lib/libfoo.so.1", "elf"),
		&tar.Header{Typeflag: tar.TypeSymlink, Name: "lib/libfoo.so", Linkname: "libfoo.so.1"},
		&tar.Header{Typeflag: tar.TypeLink, Name: "lib/hard", Linkname: "lib/libfoo.so.1"})
	if err := ExtractTar(bytes.NewReader(data), dest, 1<<20); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "lib/libfoo.so")); err != nil || target != "libfoo.so.1" {
		t.Fatalf("symbolic link: %q, %v", target, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "lib/hard")); err != nil || string(got) != "elf" {
		t.Fatalf("hard link: %q, %v", got, err)
	}

	zdest := t.TempDir()
	zdata := buildZip(t,
		zipEntry{name: "bin/tool", mode: 0o755, body: "#!/bin/sh"},
		zipEntry{name: "tool", mode: fs.ModeSymlink | 0o777, body: "bin/tool"})
	if err := ExtractZip(zdata, zdest, 1<<20); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(zdest, "tool")); err != nil || target != "bin/tool" {
		t.Fatalf("zip symbolic link: %q, %v", target, err)
	}
	info, err := os.Stat(filepath.Join(zdest, "bin/tool"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("zip entry lost its executable mode: %v, %v", info, err)
	}
}

func TestExtractEnforcesTheSizeLimit(t *testing.T) {
	big := strings.Repeat("a", 1000)
	zipData := buildZip(t, zipEntry{name: "a", body: big}, zipEntry{name: "b", body: big})
	if err := ExtractZip(zipData, t.TempDir(), 1500); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ExtractZip past the limit: %v", err)
	}
	if err := ExtractZip(zipData, t.TempDir(), 2000); err != nil {
		t.Fatalf("ExtractZip at the limit: %v", err)
	}
	tarData := buildTar(t, true, regular("a", big), regular("b", big))
	if err := ExtractTar(bytes.NewReader(tarData), t.TempDir(), 1999); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ExtractTar past the limit: %v", err)
	}
	if err := ExtractTar(bytes.NewReader(tarData), t.TempDir(), 2000); err != nil {
		t.Fatalf("ExtractTar at the limit: %v", err)
	}
	if err := ReadZip(zipData, 1999, func(File) error { return nil }); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadZip past the limit: %v", err)
	}
}

func TestReadZipVisitsRegularFilesWithLocalNames(t *testing.T) {
	data := buildZip(t,
		zipEntry{name: "dir/", mode: fs.ModeDir | 0o755},
		zipEntry{name: "/dir/index.html", body: "<p>"},
		zipEntry{name: "link", mode: fs.ModeSymlink | 0o777, body: "dir"})
	var got []File
	if err := ReadZip(data, 1<<20, func(f File) error { got = append(got, f); return nil }); err != nil {
		t.Fatalf("ReadZip: %v", err)
	}
	if len(got) != 1 || got[0].Name != "dir/index.html" || string(got[0].Data) != "<p>" {
		t.Fatalf("ReadZip visited %+v", got)
	}
	if err := ReadZip(buildZip(t, zipEntry{name: "..", body: "x"}), 1<<20, func(File) error { return nil }); err == nil {
		t.Fatal("ReadZip accepted an entry named ..")
	}
}
