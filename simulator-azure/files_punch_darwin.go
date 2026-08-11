//go:build darwin

package main

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// darwinPunchhole mirrors the kernel's `fpunchhole_t`, the argument
// fcntl(F_PUNCHHOLE) takes:
//
//	typedef struct fpunchhole {
//	    u_int32_t fp_flags;   /* unused */
//	    u_int32_t reserved;   /* to maintain 8-byte alignment */
//	    off_t     fp_offset;  /* IN: start of the region */
//	    off_t     fp_length;  /* IN: size of the region */
//	} fpunchhole_t;
type darwinPunchhole struct {
	flags    uint32
	reserved uint32
	offset   int64
	length   int64
}

// filePunchHole deallocates [offset, offset+length) of a file without changing
// its size, so the extent stops holding data and starts reading as zeros. It is
// what Clear Range does to an Azure file: the cleared range is no longer a
// valid range, and List Ranges — which reads the filesystem's own extent map —
// stops reporting it.
//
// The argument is passed the way x/sys/unix passes every other fcntl struct
// argument (FcntlFlock, FcntlFstore): as the pointer's address in the integer
// argument slot.
func filePunchHole(f *os.File, offset, length int64) error {
	if length <= 0 {
		return nil
	}
	arg := darwinPunchhole{offset: offset, length: length}
	_, err := unix.FcntlInt(f.Fd(), unix.F_PUNCHHOLE, int(uintptr(unsafe.Pointer(&arg))))
	runtime.KeepAlive(&arg)
	if err != nil {
		return fmt.Errorf("deallocate %d bytes at offset %d of %s: %w", length, offset, f.Name(), err)
	}
	return nil
}
