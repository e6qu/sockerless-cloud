// Package archive unpacks the ZIP and tar archives clients upload — function
// deployment packages, build contexts, site deployments — without letting an
// entry write outside the destination or expand past a size bound.
package archive

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrTooLarge reports an archive whose entries expand past the caller's bound.
var ErrTooLarge = errors.New("archive expands past its size limit")

// EntryName is an archive entry's name as a slash-separated path relative to
// the archive root. A leading slash is dropped, as unzip and GNU tar drop it; a
// name that still leaves the root — "..", "../x", "a/../../x" — is refused.
func EntryName(name string) (string, error) {
	rel := strings.TrimLeft(name, "/")
	if rel == "" {
		return "", fmt.Errorf("archive entry %q has an empty name", name)
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("archive entry %q escapes the archive root", name)
	}
	return path.Clean(rel), nil
}

// File is one regular file of a ZIP archive read into memory.
type File struct {
	Name string
	Mode fs.FileMode
	Data []byte
}

// ReadZip calls fn with every regular file of the ZIP archive in data, in
// archive order. Directories and symbolic links are not visited. The files'
// total expanded size may not exceed maxBytes.
func ReadZip(data []byte, maxBytes int64, fn func(File) error) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("not a valid ZIP archive: %w", err)
	}
	budget := maxBytes
	for _, entry := range zr.File {
		mode := entry.Mode()
		if mode.IsDir() || mode&fs.ModeSymlink != 0 {
			continue
		}
		name, err := EntryName(entry.Name)
		if err != nil {
			return err
		}
		content, err := readEntry(entry, &budget)
		if err != nil {
			return err
		}
		if err := fn(File{Name: name, Mode: mode.Perm(), Data: content}); err != nil {
			return err
		}
	}
	return nil
}

func readEntry(entry *zip.File, budget *int64) ([]byte, error) {
	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("open archive entry %q: %w", entry.Name, err)
	}
	defer rc.Close()
	content, err := io.ReadAll(io.LimitReader(rc, *budget+1))
	if err != nil {
		return nil, fmt.Errorf("read archive entry %q: %w", entry.Name, err)
	}
	*budget -= int64(len(content))
	if *budget < 0 {
		return nil, ErrTooLarge
	}
	return content, nil
}

// ExtractZip unpacks the ZIP archive in data into dir, which must exist.
// Directories, regular files and symbolic links are recreated; the files'
// total expanded size may not exceed maxBytes. Every write goes through an
// os.Root on dir, so neither an entry name nor a symbolic link an earlier
// entry created can place a file outside it.
func ExtractZip(data []byte, dir string, maxBytes int64) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("not a valid ZIP archive: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open extraction root: %w", err)
	}
	defer func() { _ = root.Close() }()
	x := extractor{root: root, budget: maxBytes}
	for _, entry := range zr.File {
		name, err := EntryName(entry.Name)
		if err != nil {
			return err
		}
		mode := entry.Mode()
		switch {
		case mode.IsDir():
			err = x.mkdir(name)
		case mode&fs.ModeSymlink != 0:
			var target []byte
			budget := int64(4096)
			if target, err = readEntry(entry, &budget); err == nil {
				err = x.symlink(string(target), name)
			}
		case mode.IsRegular():
			var rc io.ReadCloser
			if rc, err = entry.Open(); err == nil {
				err = x.file(name, mode.Perm(), rc)
				_ = rc.Close()
			}
		default:
			err = fmt.Errorf("archive entry %q has unsupported type %s", entry.Name, mode.Type())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ExtractTar unpacks the tar archive r carries into dir, which must exist;
// a gzip-compressed stream is recognised by its magic bytes. Directories,
// regular files, symbolic links and hard links are recreated, under the same
// containment and size bound as ExtractZip.
func ExtractTar(r io.Reader, dir string, maxBytes int64) error {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("open gzip stream: %w", err)
		}
		defer func() { _ = gz.Close() }()
		src = gz
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open extraction root: %w", err)
	}
	defer func() { _ = root.Close() }()
	x := extractor{root: root, budget: maxBytes}
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := EntryName(hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = x.mkdir(name)
		case tar.TypeReg:
			err = x.file(name, hdr.FileInfo().Mode().Perm(), tr)
		case tar.TypeSymlink:
			err = x.symlink(hdr.Linkname, name)
		case tar.TypeLink:
			var target string
			if target, err = EntryName(hdr.Linkname); err == nil {
				err = x.link(target, name)
			}
		default:
			err = fmt.Errorf("archive entry %q has unsupported tar type %q", hdr.Name, hdr.Typeflag)
		}
		if err != nil {
			return err
		}
	}
}

type extractor struct {
	root   *os.Root
	budget int64
}

func (x *extractor) mkdir(name string) error {
	if err := x.root.MkdirAll(name, 0o755); err != nil {
		return fmt.Errorf("create directory %q: %w", name, err)
	}
	return nil
}

func (x *extractor) parent(name string) error {
	if dir := path.Dir(name); dir != "." {
		return x.mkdir(dir)
	}
	return nil
}

func (x *extractor) file(name string, perm fs.FileMode, content io.Reader) error {
	if err := x.parent(name); err != nil {
		return err
	}
	if perm == 0 {
		perm = 0o644
	}
	f, err := x.root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create file %q: %w", name, err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(content, x.budget+1))
	closeErr := f.Close()
	x.budget -= n
	if x.budget < 0 {
		return ErrTooLarge
	}
	if copyErr != nil {
		return fmt.Errorf("write file %q: %w", name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close file %q: %w", name, closeErr)
	}
	return nil
}

// symlink recreates a symbolic link verbatim: its target is resolved by
// whoever reads the tree — the workload that mounts it, the build that copies
// it — and the root keeps any later entry from writing through it.
func (x *extractor) symlink(target, name string) error {
	if target == "" {
		return fmt.Errorf("symbolic link %q has an empty target", name)
	}
	if err := x.parent(name); err != nil {
		return err
	}
	if err := x.root.Symlink(target, name); err != nil {
		return fmt.Errorf("create symbolic link %q: %w", name, err)
	}
	return nil
}

func (x *extractor) link(target, name string) error {
	if err := x.parent(name); err != nil {
		return err
	}
	if err := x.root.Link(target, name); err != nil {
		return fmt.Errorf("create hard link %q: %w", name, err)
	}
	return nil
}
