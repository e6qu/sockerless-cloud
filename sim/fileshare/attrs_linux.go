package fileshare

import (
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	utimeNow  = unix.UTIME_NOW
	utimeOmit = unix.UTIME_OMIT
)

func statAtime(st *syscall.Stat_t) syscall.Timespec { return st.Atim }
func statMtime(st *syscall.Stat_t) syscall.Timespec { return st.Mtim }
func statCtime(st *syscall.Stat_t) syscall.Timespec { return st.Ctim }
func statRdev(st *syscall.Stat_t) uint64            { return st.Rdev }
func fsBlockSize(fs *unix.Statfs_t) int64           { return fs.Bsize }
