//go:build darwin || linux

package sparse

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// DataExtents asks the filesystem which extents of f hold data, with
// SEEK_DATA and SEEK_HOLE. An extent nothing has written is a hole until a
// write turns it into data. A filesystem that cannot answer reports an error
// wrapping errors.ErrUnsupported.
func DataExtents(f *os.File) ([]Extent, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	var out []Extent
	for offset := int64(0); offset < size; {
		dataOffset, err := unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			break
		}
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
			return nil, fmt.Errorf("the data extents of %s: %w", f.Name(), errors.ErrUnsupported)
		}
		if err != nil {
			return nil, err
		}
		holeOffset, err := unix.Seek(int(f.Fd()), dataOffset, unix.SEEK_HOLE)
		if errors.Is(err, unix.ENXIO) {
			holeOffset = size
		} else if err != nil {
			return nil, err
		}
		holeOffset = min(holeOffset, size)
		if holeOffset <= dataOffset {
			break
		}
		out = append(out, Extent{Start: dataOffset, End: holeOffset - 1})
		offset = holeOffset
	}
	return out, nil
}

// BlockSize reports the unit the filesystem allocates a file in: deallocating
// only ever removes whole units.
func BlockSize(info os.FileInfo) (int64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	// Stat_t.Blksize is int32 on darwin and linux/arm64 but int64 on
	// linux/amd64.
	size := int64(st.Blksize) //nolint:unconvert // width varies by GOOS/GOARCH
	if size <= 0 {
		return 0, false
	}
	return size, true
}
