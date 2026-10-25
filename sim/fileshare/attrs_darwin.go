package fileshare

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// macOS's <sys/stat.h> defines these for utimensat; x/sys/unix does not
// export them for darwin.
const (
	utimeNow  = -1
	utimeOmit = -2
)

func statAtime(st *syscall.Stat_t) syscall.Timespec { return st.Atimespec }
func statMtime(st *syscall.Stat_t) syscall.Timespec { return st.Mtimespec }
func statCtime(st *syscall.Stat_t) syscall.Timespec { return st.Ctimespec }
func statRdev(st *syscall.Stat_t) uint64            { return uint64(uint32(st.Rdev)) }
func fsBlockSize(fs *unix.Statfs_t) int64           { return int64(fs.Bsize) }
