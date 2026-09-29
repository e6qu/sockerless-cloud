//go:build darwin

package sparse

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

// PunchHole deallocates [offset, offset+length) of f without changing its
// size, so the extent stops holding data and reads as zeros.
//
// x/sys/unix passes every other fcntl struct argument (FcntlFlock,
// FcntlFstore) as the pointer's address in the integer argument slot, and so
// does this.
func PunchHole(f *os.File, offset, length int64) error {
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
