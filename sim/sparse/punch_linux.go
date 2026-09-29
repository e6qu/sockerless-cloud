//go:build linux

package sparse

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// PunchHole deallocates [offset, offset+length) of f without changing its
// size, so the extent stops holding data and reads as zeros.
func PunchHole(f *os.File, offset, length int64) error {
	if length <= 0 {
		return nil
	}
	err := unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, offset, length)
	if err != nil {
		return fmt.Errorf("deallocate %d bytes at offset %d of %s: %w", length, offset, f.Name(), err)
	}
	return nil
}
