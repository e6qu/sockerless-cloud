package main

import (
	"bytes"
	"io"
	"io/fs"
	"os"
)

const ebsSparseCopyBufferSize = 1024 * 1024

// ebsCopySparseFileByContent preserves zero-filled extents without allocating
// them on the destination filesystem. It is also the portable implementation
// for filesystems that do not expose SEEK_DATA and SEEK_HOLE.
func ebsCopySparseFileByContent(dst, src string, mode fs.FileMode) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if err := in.Close(); retErr == nil {
			retErr = err
		}
	}()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		if err := out.Close(); retErr == nil {
			retErr = err
		}
	}()
	if err := out.Truncate(info.Size()); err != nil {
		return err
	}

	buf := make([]byte, ebsSparseCopyBufferSize)
	zero := make([]byte, len(buf))
	var offset int64
	for {
		n, readErr := in.Read(buf)
		if n > 0 && !bytes.Equal(buf[:n], zero[:n]) {
			if _, err := out.WriteAt(buf[:n], offset); err != nil {
				return err
			}
		}
		offset += int64(n)
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
