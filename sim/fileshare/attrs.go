package fileshare

import (
	"io/fs"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// bitmap4 is the attribute set of a request or reply: word i holds
// attributes 32*i to 32*i+31.
type bitmap []uint32

func (b bitmap) has(attr int) bool {
	word := attr / 32
	return word < len(b) && b[word]&(1<<(attr%32)) != 0
}

func (b *bitmap) set(attr int) {
	word := attr / 32
	for len(*b) <= word {
		*b = append(*b, 0)
	}
	(*b)[word] |= 1 << (attr % 32)
}

func (d *decoder) bitmap() bitmap {
	n := d.uint32()
	if n > 8 {
		d.err = errShort
		return nil
	}
	b := make(bitmap, n)
	for i := range b {
		b[i] = d.uint32()
	}
	return b
}

func (e *encoder) bitmap(b bitmap) {
	e.uint32(uint32(len(b)))
	for _, w := range b {
		e.uint32(w)
	}
}

// supported lists the attributes the server answers.
var supported = func() bitmap {
	var b bitmap
	for _, a := range []int{
		attrSupportedAttrs, attrType, attrFHExpireType, attrChange, attrSize, attrLinkSupport,
		attrSymlinkSupport, attrNamedAttr, attrFSID, attrUniqueHandles, attrLeaseTime, attrRdattrError,
		attrACLSupport, attrCanSetTime, attrCaseInsensitive, attrCasePreserving, attrChownRestricted,
		attrFileHandle, attrFileID, attrFilesAvail, attrFilesFree, attrFilesTotal, attrHomogeneous,
		attrMaxFileSize, attrMaxLink, attrMaxName, attrMaxRead, attrMaxWrite, attrMode, attrNoTrunc,
		attrNumLinks, attrOwner, attrOwnerGroup, attrRawDev, attrSpaceAvail, attrSpaceFree,
		attrSpaceTotal, attrSpaceUsed, attrTimeAccess, attrTimeAccessSet, attrTimeDelta,
		attrTimeMetadata, attrTimeModify, attrTimeModifySet, attrMountedOnFileID,
	} {
		b.set(a)
	}
	return b
}()

// fileAttrs is what the server knows about one file when it answers
// attributes: the pseudo-root has none of the filesystem's.
type fileAttrs struct {
	key      fhKey
	fh       []byte
	info     fs.FileInfo
	st       *syscall.Stat_t
	readOnly bool
	root     string
	// mountedOn is the file id a mount point shows its parent filesystem: an
	// export's root inside the pseudo-root.
	mountedOn uint64
}

func (s *Server) attrsOf(n *node, info fs.FileInfo) fileAttrs {
	key := fhKey{n.export.index, n.ino}
	a := fileAttrs{key: key, fh: s.filehandle(key), info: info, readOnly: n.export.readOnly, root: n.export.root, mountedOn: n.ino}
	a.st, _ = info.Sys().(*syscall.Stat_t)
	if n.parent == nil {
		a.mountedOn = pseudoIno(n.export.name)
	}
	return a
}

func (s *Server) pseudoRootAttrs() fileAttrs {
	key := fhKey{export: pseudoRootExport, ino: 1}
	return fileAttrs{key: key, fh: s.filehandle(key), readOnly: true, mountedOn: 1}
}

// pseudoIno is the file id an export's name has in the pseudo-root listing.
func pseudoIno(name string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(name); i++ {
		h ^= uint64(name[i])
		h *= 1099511628211
	}
	return h | 2
}

func nfsType(info fs.FileInfo) uint32 {
	if info == nil {
		return nf4Dir
	}
	switch mode := info.Mode(); {
	case mode.IsDir():
		return nf4Dir
	case mode&fs.ModeSymlink != 0:
		return nf4Lnk
	case mode&fs.ModeNamedPipe != 0:
		return nf4Fifo
	case mode&fs.ModeSocket != 0:
		return nf4Sock
	case mode&fs.ModeCharDevice != 0:
		return nf4Chr
	case mode&fs.ModeDevice != 0:
		return nf4Blk
	}
	return nf4Reg
}

func (e *encoder) nfstime(t time.Time) {
	e.uint64(uint64(t.Unix()))
	e.uint32(uint32(t.Nanosecond()))
}

func statTime(ts syscall.Timespec) time.Time { return time.Unix(ts.Sec, ts.Nsec) }

// change is the file's change attribute: its ctime in nanoseconds, which
// moves on every change of content or metadata.
func (a fileAttrs) change() uint64 {
	if a.st == nil {
		return 1
	}
	return uint64(a.st.Ctim.Sec)*1e9 + uint64(a.st.Ctim.Nsec)
}

// encodeAttrs answers the requested attributes the server supports: the
// bitmap of those answered, then their values in attribute order.
func (e *encoder) encodeAttrs(want bitmap, a fileAttrs) {
	var answered bitmap
	vals := &encoder{}
	var fsStat unix.Statfs_t
	haveFS := a.root != "" && unix.Statfs(a.root, &fsStat) == nil
	for attr := 0; attr <= attrMountedOnFileID; attr++ {
		if !want.has(attr) || !supported.has(attr) {
			continue
		}
		switch attr {
		case attrSupportedAttrs:
			vals.bitmap(supported)
		case attrType:
			vals.uint32(nfsType(a.info))
		case attrFHExpireType:
			vals.uint32(fh4Persistent)
		case attrChange:
			vals.uint64(a.change())
		case attrSize:
			if a.info != nil {
				vals.uint64(uint64(a.info.Size()))
			} else {
				vals.uint64(4096)
			}
		case attrLinkSupport, attrSymlinkSupport, attrUniqueHandles, attrCanSetTime, attrCasePreserving, attrHomogeneous, attrNoTrunc:
			vals.bool(true)
		case attrNamedAttr, attrCaseInsensitive, attrChownRestricted:
			vals.bool(false)
		case attrFSID:
			vals.uint64(uint64(a.key.export) + 1)
			vals.uint64(0)
		case attrLeaseTime:
			vals.uint32(leaseSeconds)
		case attrRdattrError:
			vals.uint32(nfs4OK)
		case attrACLSupport:
			vals.uint32(0)
		case attrFileHandle:
			vals.opaque(a.fh)
		case attrFileID:
			vals.uint64(a.key.ino)
		case attrFilesAvail, attrFilesFree:
			if haveFS {
				vals.uint64(fsStat.Ffree)
			} else {
				vals.uint64(0)
			}
		case attrFilesTotal:
			if haveFS {
				vals.uint64(fsStat.Files)
			} else {
				vals.uint64(0)
			}
		case attrMaxFileSize:
			vals.uint64(1 << 62)
		case attrMaxLink:
			vals.uint32(65000)
		case attrMaxName:
			vals.uint32(255)
		case attrMaxRead, attrMaxWrite:
			vals.uint64(maxIO)
		case attrMode:
			if a.info != nil {
				vals.uint32(uint32(a.info.Mode().Perm()) | setBits(a.info.Mode()))
			} else {
				vals.uint32(0o555)
			}
		case attrNumLinks:
			if a.st != nil {
				vals.uint32(uint32(a.st.Nlink))
			} else {
				vals.uint32(2)
			}
		case attrOwner:
			if a.st != nil {
				vals.str(strconv.FormatUint(uint64(a.st.Uid), 10))
			} else {
				vals.str("0")
			}
		case attrOwnerGroup:
			if a.st != nil {
				vals.str(strconv.FormatUint(uint64(a.st.Gid), 10))
			} else {
				vals.str("0")
			}
		case attrRawDev:
			if a.st != nil {
				vals.uint32(unix.Major(a.st.Rdev))
				vals.uint32(unix.Minor(a.st.Rdev))
			} else {
				vals.uint32(0)
				vals.uint32(0)
			}
		case attrSpaceAvail:
			vals.uint64(blocks(fsStat.Bavail, fsStat.Bsize, haveFS))
		case attrSpaceFree:
			vals.uint64(blocks(fsStat.Bfree, fsStat.Bsize, haveFS))
		case attrSpaceTotal:
			vals.uint64(blocks(fsStat.Blocks, fsStat.Bsize, haveFS))
		case attrSpaceUsed:
			if a.st != nil {
				vals.uint64(uint64(a.st.Blocks) * 512)
			} else {
				vals.uint64(0)
			}
		case attrTimeAccess:
			if a.st != nil {
				vals.nfstime(statTime(a.st.Atim))
			} else {
				vals.nfstime(time.Unix(0, 0))
			}
		case attrTimeAccessSet, attrTimeModifySet:
			continue
		case attrTimeDelta:
			vals.uint64(0)
			vals.uint32(1)
		case attrTimeMetadata:
			if a.st != nil {
				vals.nfstime(statTime(a.st.Ctim))
			} else {
				vals.nfstime(time.Unix(0, 0))
			}
		case attrTimeModify:
			if a.st != nil {
				vals.nfstime(statTime(a.st.Mtim))
			} else {
				vals.nfstime(time.Unix(0, 0))
			}
		case attrMountedOnFileID:
			vals.uint64(a.mountedOn)
		default:
			continue
		}
		answered.set(attr)
	}
	e.bitmap(answered)
	e.opaque(vals.buf)
}

func setBits(mode fs.FileMode) uint32 {
	var bits uint32
	if mode&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&fs.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}

func blocks(n uint64, size int64, ok bool) uint64 {
	if !ok {
		return 0
	}
	return n * uint64(size)
}

// settable are the attributes SETATTR and a create may carry.
type settable struct {
	size     *uint64
	mode     *uint32
	owner    *string
	group    *string
	atime    *time.Time
	mtime    *time.Time
	atimeNow bool
	mtimeNow bool
	asked    bitmap
}

// decodeSettable reads a fattr4 of the attributes a client may set, and
// answers the first attribute it asks for that the server does not set.
func (d *decoder) decodeSettable() (settable, int) {
	var out settable
	out.asked = d.bitmap()
	vals := &decoder{buf: d.opaque()}
	for attr := 0; attr <= attrMountedOnFileID; attr++ {
		if !out.asked.has(attr) {
			continue
		}
		switch attr {
		case attrSize:
			v := vals.uint64()
			out.size = &v
		case attrMode:
			v := vals.uint32()
			out.mode = &v
		case attrOwner:
			v := vals.str()
			out.owner = &v
		case attrOwnerGroup:
			v := vals.str()
			out.group = &v
		case attrTimeAccessSet:
			if vals.uint32() == setToClientTime4 {
				t := time.Unix(int64(vals.uint64()), int64(vals.uint32()))
				out.atime = &t
			} else {
				out.atimeNow = true
			}
		case attrTimeModifySet:
			if vals.uint32() == setToClientTime4 {
				t := time.Unix(int64(vals.uint64()), int64(vals.uint32()))
				out.mtime = &t
			} else {
				out.mtimeNow = true
			}
		default:
			return out, attr
		}
	}
	if vals.err != nil {
		d.err = vals.err
	}
	return out, -1
}

// apply sets the attributes on the path and answers which it set.
func (a settable) apply(path string) (bitmap, uint32) {
	var set bitmap
	if a.size != nil {
		if err := os.Truncate(path, int64(*a.size)); err != nil {
			return set, statusOf(err)
		}
		set.set(attrSize)
	}
	if a.mode != nil {
		if err := os.Chmod(path, fs.FileMode(*a.mode&0o7777)); err != nil {
			return set, statusOf(err)
		}
		set.set(attrMode)
	}
	if a.owner != nil || a.group != nil {
		uid, gid := -1, -1
		if a.owner != nil {
			id, err := parseID(*a.owner)
			if err != nil {
				return set, nfs4ErrBadOwner
			}
			uid = id
		}
		if a.group != nil {
			id, err := parseID(*a.group)
			if err != nil {
				return set, nfs4ErrBadOwner
			}
			gid = id
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return set, statusOf(err)
		}
		if a.owner != nil {
			set.set(attrOwner)
		}
		if a.group != nil {
			set.set(attrOwnerGroup)
		}
	}
	if a.atime != nil || a.mtime != nil || a.atimeNow || a.mtimeNow {
		times := []unix.Timespec{{Nsec: unix.UTIME_OMIT}, {Nsec: unix.UTIME_OMIT}}
		if a.atimeNow {
			times[0] = unix.Timespec{Nsec: unix.UTIME_NOW}
		} else if a.atime != nil {
			times[0] = unix.NsecToTimespec(a.atime.UnixNano())
		}
		if a.mtimeNow {
			times[1] = unix.Timespec{Nsec: unix.UTIME_NOW}
		} else if a.mtime != nil {
			times[1] = unix.NsecToTimespec(a.mtime.UnixNano())
		}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return set, statusOf(err)
		}
		if a.atime != nil || a.atimeNow {
			set.set(attrTimeAccessSet)
		}
		if a.mtime != nil || a.mtimeNow {
			set.set(attrTimeModifySet)
		}
	}
	return set, nfs4OK
}

// parseID reads the numeric form an AUTH_SYS client sends for an owner; a
// name@domain form is one the server has no mapping for.
func parseID(s string) (int, error) {
	if at := indexByte(s, '@'); at >= 0 {
		s = s[:at]
	}
	return strconv.Atoi(s)
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
