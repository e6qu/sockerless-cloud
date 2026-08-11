//go:build !darwin && !linux

package main

import (
	"fmt"
	"os"
	"runtime"
)

// fileDataRanges answers List Ranges from the filesystem's record of which
// extents of a file hold data. That record is reachable through SEEK_DATA /
// SEEK_HOLE, which only the platforms the simulator runs on provide; anywhere
// else the question has no answer and the operation says so rather than
// inventing one.
func fileDataRanges(path string, size int64) ([]fileByteRange, error) {
	return nil, fmt.Errorf("enumerating the data extents of %s is not supported on %s", path, runtime.GOOS)
}

// statLinkCount reports how many names the filesystem has for a file.
func statLinkCount(info os.FileInfo) (int, bool) {
	return 0, false
}

// fileAllocationBlockSize reports the unit the filesystem allocates a file in.
func fileAllocationBlockSize(info os.FileInfo) (int64, bool) {
	return 0, false
}

// filePunchHole deallocates a range of a file. Clear Range depends on it: a
// cleared range must stop being a valid range, and the only record of that is
// the filesystem's extent map. Where the platform cannot deallocate, the
// operation says so instead of zero-filling and reporting a success that left
// the range allocated.
func filePunchHole(f *os.File, offset, length int64) error {
	return fmt.Errorf("deallocating a range of %s is not supported on %s", f.Name(), runtime.GOOS)
}
