//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// fileDataRanges reports the extents of a file that hold data, which is what
// List Ranges answers about an Azure file. A share's files are real files, so
// the question is put to the filesystem itself with SEEK_DATA / SEEK_HOLE: the
// space Create File allocates is a hole until something writes into it, and
// every write — through Upload Range or from a workload with the share
// mounted — turns the extent it touched into data.
func fileDataRanges(path string, size int64) ([]fileByteRange, error) {
	if size == 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []fileByteRange
	for offset := int64(0); offset < size; {
		dataOffset, err := unix.Seek(int(f.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			// No data at or after this offset: the rest of the file is a hole.
			break
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
		if holeOffset > size {
			holeOffset = size
		}
		if holeOffset <= dataOffset {
			break
		}
		out = append(out, fileByteRange{Start: dataOffset, End: holeOffset - 1})
		offset = holeOffset
	}
	return out, nil
}

// statLinkCount reports how many names the filesystem has for a file.
func statLinkCount(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Nlink), true
}

// fileAllocationBlockSize reports the unit the filesystem allocates a file in.
// Deallocating only ever removes whole units, so this is the granularity at
// which clearing a range can actually take space out of a file.
func fileAllocationBlockSize(info os.FileInfo) (int64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	// Stat_t.Blksize is int32 on darwin and linux/arm64 but int64 on
	// linux/amd64, so the conversion is required on some build targets and
	// redundant on others.
	size := int64(st.Blksize) //nolint:unconvert // width varies by GOOS/GOARCH
	if size <= 0 {
		return 0, false
	}
	return size, true
}
