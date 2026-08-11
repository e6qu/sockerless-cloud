//go:build linux

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// filePunchHole deallocates [offset, offset+length) of a file without changing
// its size, so the extent stops holding data and starts reading as zeros. It is
// what Clear Range does to an Azure file: the cleared range is no longer a
// valid range, and List Ranges — which reads the filesystem's own extent map —
// stops reporting it.
func filePunchHole(f *os.File, offset, length int64) error {
	if length <= 0 {
		return nil
	}
	err := unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, offset, length)
	if err != nil {
		return fmt.Errorf("deallocate %d bytes at offset %d of %s: %w", length, offset, f.Name(), err)
	}
	return nil
}
