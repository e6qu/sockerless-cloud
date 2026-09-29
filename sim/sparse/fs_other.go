//go:build !darwin && !linux

package sparse

import (
	"errors"
	"fmt"
	"os"
)

// DataExtents answers from the filesystem's record of which extents hold
// data, which only SEEK_DATA and SEEK_HOLE reach; this platform has neither.
func DataExtents(f *os.File) ([]Extent, error) {
	return nil, fmt.Errorf("the data extents of %s: %w", f.Name(), errors.ErrUnsupported)
}

// BlockSize reports the unit the filesystem allocates a file in.
func BlockSize(os.FileInfo) (int64, bool) {
	return 0, false
}

// PunchHole deallocates a range of f, which this platform cannot do.
func PunchHole(f *os.File, offset, length int64) error {
	if length <= 0 {
		return nil
	}
	return fmt.Errorf("deallocate %d bytes at offset %d of %s: %w", length, offset, f.Name(), errors.ErrUnsupported)
}
