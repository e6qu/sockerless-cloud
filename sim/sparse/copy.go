package sparse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Clear makes [offset, offset+length) of f stop holding data. A zero-filled
// extent is still an allocated one, and DataExtents would go on reporting it,
// so the whole allocation units inside the range are deallocated; the partial
// units at its edges are zeroed in place, which is what the kernel does for
// the unaligned edges of a hole punch.
func Clear(f *os.File, offset, length int64) error {
	if length <= 0 {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	blockSize, ok := BlockSize(info)
	if !ok {
		return fmt.Errorf("clear a range of %s: the filesystem does not report an allocation block size", f.Name())
	}
	end := offset + length
	alignedStart := ((offset + blockSize - 1) / blockSize) * blockSize
	alignedEnd := (end / blockSize) * blockSize
	if alignedEnd <= alignedStart {
		return zero(f, offset, length)
	}
	if alignedStart > offset {
		if err := zero(f, offset, alignedStart-offset); err != nil {
			return err
		}
	}
	if end > alignedEnd {
		if err := zero(f, alignedEnd, end-alignedEnd); err != nil {
			return err
		}
	}
	return PunchHole(f, alignedStart, alignedEnd-alignedStart)
}

func zero(f *os.File, offset, length int64) error {
	const chunk = 1 << 20
	zeros := make([]byte, min(length, int64(chunk)))
	for written := int64(0); written < length; {
		n := min(length-written, int64(len(zeros)))
		if _, err := f.WriteAt(zeros[:n], offset+written); err != nil {
			return err
		}
		written += n
	}
	return nil
}

// CopyFile makes dst a copy of src that allocates only src's data extents.
// Where the filesystem cannot report extents it compares the contents
// against zeros instead, which leaves every all-zero megabyte unallocated.
func CopyFile(dst, src *os.File) error {
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if err := dst.Truncate(info.Size()); err != nil {
		return err
	}
	extents, err := DataExtents(src)
	if errors.Is(err, errors.ErrUnsupported) {
		return copyByContent(dst, src)
	}
	if err != nil {
		return err
	}
	for _, e := range extents {
		if _, err := io.Copy(io.NewOffsetWriter(dst, e.Start), io.NewSectionReader(src, e.Start, e.End-e.Start+1)); err != nil {
			return err
		}
	}
	return nil
}

func copyByContent(dst, src *os.File) error {
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	zeros := make([]byte, chunk)
	for offset := int64(0); ; {
		n, err := src.ReadAt(buf, offset)
		if n > 0 && !bytes.Equal(buf[:n], zeros[:n]) {
			if _, err := dst.WriteAt(buf[:n], offset); err != nil {
				return err
			}
		}
		offset += int64(n)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Copy copies the file at src to dst, created with mode, allocating only
// src's data extents.
func Copy(dst, src string, mode fs.FileMode) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		if err := out.Close(); retErr == nil {
			retErr = err
		}
	}()
	return CopyFile(out, in)
}

// CopyTree replaces dst with a copy of the directory tree at src, copying
// each regular file with Copy. Entries other than directories and regular
// files are not copied.
func CopyTree(dst, src string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return Copy(target, path, info.Mode().Perm())
	})
}
