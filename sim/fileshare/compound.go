package fileshare

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// compoundState is one COMPOUND's current and saved filehandles.
type compoundState struct {
	cred      credentials
	current   fhKey
	haveFH    bool
	saved     fhKey
	haveSaved bool
}

// compound runs a COMPOUND (RFC 7530 section 15.2) and returns its reply,
// or false for arguments the server cannot decode at all.
func (s *Server) compound(cred credentials, d *decoder) ([]byte, bool) {
	tag := d.opaque()
	minor := d.uint32()
	count := d.uint32()
	if d.err != nil || count > 1024 {
		return nil, false
	}
	reply := &encoder{}
	results := &encoder{}
	status := uint32(nfs4OK)
	done := uint32(0)
	if minor != 0 {
		status = nfs4ErrMinorVersMismatch
		count = 0
	}
	st := &compoundState{cred: cred}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := uint32(0); i < count; i++ {
		op := d.uint32()
		if d.err != nil {
			break
		}
		results.uint32(op)
		status = s.operation(st, op, d, results)
		done++
		if d.err != nil {
			// The arguments ran out inside the operation: what was answered
			// is replaced by the one status the client can act on.
			results.buf = results.buf[:0]
			results.uint32(op)
			results.uint32(nfs4ErrBadXDR)
			status = nfs4ErrBadXDR
			done = 1
			break
		}
		if status != nfs4OK {
			break
		}
	}
	reply.uint32(status)
	reply.opaque(tag)
	reply.uint32(done)
	reply.buf = append(reply.buf, results.buf...)
	return reply.buf, true
}

// operation runs one op, writing its status and result after the op number
// the caller wrote, and returns the status.
func (s *Server) operation(st *compoundState, op uint32, d *decoder, r *encoder) uint32 {
	switch op {
	case opAccess:
		return s.opAccess(st, d, r)
	case opClose:
		return s.opClose(st, d, r)
	case opCommit:
		return s.opCommit(st, d, r)
	case opCreate:
		return s.opCreate(st, d, r)
	case opDelegPurge:
		d.uint64()
		return status(r, nfs4OK)
	case opDelegReturn:
		d.stateid()
		return status(r, nfs4OK)
	case opGetAttr:
		return s.opGetAttr(st, d, r)
	case opGetFH:
		if !st.haveFH {
			return status(r, nfs4ErrNoFileHandle)
		}
		status(r, nfs4OK)
		r.opaque(s.filehandle(st.current))
		return nfs4OK
	case opLink:
		return s.opLink(st, d, r)
	case opLock:
		return s.opLock(st, d, r)
	case opLockT:
		return s.opLockT(st, d, r)
	case opLockU:
		return s.opLockU(st, d, r)
	case opLookup:
		return s.opLookup(st, d, r)
	case opLookupP:
		return s.opLookupP(st, r)
	case opNVerify:
		return s.opVerify(st, d, r, false)
	case opOpen:
		return s.opOpen(st, d, r)
	case opOpenAttr:
		d.bool()
		return status(r, nfs4ErrNotSupp)
	case opOpenConfirm:
		return s.opOpenConfirm(d, r)
	case opOpenDowngrade:
		return s.opOpenDowngrade(d, r)
	case opPutFH:
		key, code := s.parseFH(d.opaque())
		if code != nfs4OK {
			return status(r, code)
		}
		if _, _, code := s.resolve(key); code != nfs4OK {
			return status(r, code)
		}
		st.current, st.haveFH = key, true
		return status(r, nfs4OK)
	case opPutPubFH, opPutRootFH:
		st.current, st.haveFH = fhKey{export: pseudoRootExport, ino: 1}, true
		return status(r, nfs4OK)
	case opRead:
		return s.opRead(st, d, r)
	case opReadDir:
		return s.opReadDir(st, d, r)
	case opReadLink:
		return s.opReadLink(st, r)
	case opRemove:
		return s.opRemove(st, d, r)
	case opRename:
		return s.opRename(st, d, r)
	case opRenew:
		id := d.uint64()
		c, ok := s.clients[id]
		if !ok {
			return status(r, nfs4ErrStaleClientID)
		}
		c.renewed = time.Now()
		return status(r, nfs4OK)
	case opRestoreFH:
		if !st.haveSaved {
			return status(r, nfs4ErrRestoreFH)
		}
		st.current, st.haveFH = st.saved, true
		return status(r, nfs4OK)
	case opSaveFH:
		if !st.haveFH {
			return status(r, nfs4ErrNoFileHandle)
		}
		st.saved, st.haveSaved = st.current, true
		return status(r, nfs4OK)
	case opSecInfo:
		d.str()
		if !st.haveFH {
			return status(r, nfs4ErrNoFileHandle)
		}
		status(r, nfs4OK)
		r.uint32(1)
		r.uint32(authSys)
		return nfs4OK
	case opSetAttr:
		return s.opSetAttr(st, d, r)
	case opSetClientID:
		return s.opSetClientID(d, r)
	case opSetClientIDConf:
		id := d.uint64()
		var verifier [8]byte
		copy(verifier[:], d.fixed(8))
		c, ok := s.clients[id]
		if !ok || c.confirm != verifier {
			return status(r, nfs4ErrStaleClientID)
		}
		c.confirmed = true
		c.renewed = time.Now()
		return status(r, nfs4OK)
	case opVerify:
		return s.opVerify(st, d, r, true)
	case opWrite:
		return s.opWrite(st, d, r)
	case opReleaseLockOwner:
		d.uint64()
		d.opaque()
		return status(r, nfs4OK)
	}
	return status(r, nfs4ErrOpIllegal)
}

func status(r *encoder, code uint32) uint32 {
	r.uint32(code)
	return code
}

// current resolves the compound's current filehandle to a node; the
// pseudo-root resolves to a nil node.
func (s *Server) current(st *compoundState) (*node, fs.FileInfo, uint32) {
	if !st.haveFH {
		return nil, nil, nfs4ErrNoFileHandle
	}
	return s.resolve(st.current)
}

// currentDir resolves the current filehandle to a directory of an export.
func (s *Server) currentDir(st *compoundState) (*node, uint32) {
	n, info, code := s.current(st)
	if code != nfs4OK {
		return nil, code
	}
	if n == nil {
		return nil, nfs4ErrROFS
	}
	if !info.IsDir() {
		return nil, nfs4ErrNotDir
	}
	return n, nfs4OK
}

// writable refuses a change on a read-only export.
func writable(n *node) uint32 {
	if n.export.readOnly {
		return nfs4ErrROFS
	}
	if n.confined() {
		return nfs4ErrAccess
	}
	return nfs4OK
}

func (s *Server) opAccess(st *compoundState, d *decoder, r *encoder) uint32 {
	want := d.uint32()
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	supportedBits := uint32(accessRead | accessLookup | accessModify | accessExtend | accessDelete | accessExecute)
	granted := uint32(accessRead | accessLookup)
	isDir := true
	if n != nil {
		granted = permitted(st.cred, info)
		if n.export.readOnly {
			granted &^= accessModify | accessExtend | accessDelete
		}
		isDir = info.IsDir()
	}
	if isDir {
		supportedBits &^= accessExecute
	} else {
		supportedBits &^= accessLookup | accessDelete
	}
	status(r, nfs4OK)
	r.uint32(want & supportedBits)
	r.uint32(want & supportedBits & granted)
	return nfs4OK
}

// permitted reads the access the file's mode bits give the caller: root
// gets everything, as a share with root squashing off gives it.
func permitted(cred credentials, info fs.FileInfo) uint32 {
	if info == nil {
		return accessRead | accessLookup
	}
	if cred.uid == 0 {
		return accessRead | accessLookup | accessModify | accessExtend | accessDelete | accessExecute
	}
	mode := info.Mode().Perm()
	st, _ := info.Sys().(*syscall.Stat_t)
	shift := 0
	switch {
	case st != nil && st.Uid == cred.uid:
		shift = 6
	case st != nil && (st.Gid == cred.gid || containsGID(cred.gids, st.Gid)):
		shift = 3
	}
	bits := uint32(mode>>shift) & 7
	var out uint32
	if bits&4 != 0 {
		out |= accessRead
	}
	if bits&2 != 0 {
		out |= accessModify | accessExtend | accessDelete
	}
	if bits&1 != 0 {
		out |= accessLookup | accessExecute
	}
	return out
}

func containsGID(gids []uint32, gid uint32) bool {
	for _, g := range gids {
		if g == gid {
			return true
		}
	}
	return false
}

func (s *Server) opGetAttr(st *compoundState, d *decoder, r *encoder) uint32 {
	want := d.bitmap()
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	status(r, nfs4OK)
	if n == nil {
		r.encodeAttrs(want, s.pseudoRootAttrs())
	} else {
		r.encodeAttrs(want, s.attrsOf(n, info))
	}
	return nfs4OK
}

func (s *Server) opVerify(st *compoundState, d *decoder, r *encoder, same bool) uint32 {
	want := d.bitmap()
	vals := d.opaque()
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	ours := &encoder{}
	if n == nil {
		ours.encodeAttrs(want, s.pseudoRootAttrs())
	} else {
		ours.encodeAttrs(want, s.attrsOf(n, info))
	}
	od := &decoder{buf: ours.buf}
	od.bitmap()
	equal := bytes.Equal(od.opaque(), vals)
	switch {
	case same && !equal:
		return status(r, nfs4ErrNotSame)
	case !same && equal:
		return status(r, nfs4ErrSame)
	}
	return status(r, nfs4OK)
}

const nfs4ErrSame = 10009

func (s *Server) opLookup(st *compoundState, d *decoder, r *encoder) uint32 {
	name := d.str()
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil {
		ex, ok := s.exports[name]
		if !ok {
			return status(r, nfs4ErrNoEnt)
		}
		root, rootInfo, code := s.resolve(fhKey{ex.index, 0})
		if code != nfs4OK {
			info, err := os.Lstat(ex.root)
			if err != nil {
				return status(r, statusOf(err))
			}
			root = &node{export: ex, ino: inodeOf(info)}
			s.nodes[fhKey{ex.index, root.ino}] = root
			rootInfo = info
		}
		_ = rootInfo
		st.current, st.haveFH = fhKey{ex.index, root.ino}, true
		return status(r, nfs4OK)
	}
	if !info.IsDir() {
		if info.Mode()&fs.ModeSymlink != 0 {
			return status(r, nfs4ErrSymlink)
		}
		return status(r, nfs4ErrNotDir)
	}
	child, _, code := s.lookup(n, name)
	if code != nfs4OK {
		return status(r, code)
	}
	st.current, st.haveFH = fhKey{child.export.index, child.ino}, true
	return status(r, nfs4OK)
}

func (s *Server) opLookupP(st *compoundState, r *encoder) uint32 {
	n, _, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	switch {
	case n == nil:
		return status(r, nfs4ErrNoEnt)
	case n.parent == nil:
		st.current = fhKey{export: pseudoRootExport, ino: 1}
	default:
		st.current = fhKey{n.export.index, n.parent.ino}
	}
	return status(r, nfs4OK)
}

func (s *Server) opReadLink(st *compoundState, r *encoder) uint32 {
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil || info.Mode()&fs.ModeSymlink == 0 {
		return status(r, nfs4ErrInval)
	}
	target, err := os.Readlink(n.path())
	if err != nil {
		return status(r, statusOf(err))
	}
	status(r, nfs4OK)
	r.str(target)
	return nfs4OK
}

// changeInfo is the change_info4 of a directory around an operation.
func (s *Server) changeInfo(r *encoder, before uint64, dir *node) {
	r.bool(false)
	r.uint64(before)
	r.uint64(s.changeOf(dir))
}

func (s *Server) changeOf(n *node) uint64 {
	info, err := os.Lstat(n.path())
	if err != nil {
		return 0
	}
	return s.attrsOf(n, info).change()
}

func (s *Server) opCreate(st *compoundState, d *decoder, r *encoder) uint32 {
	objType := d.uint32()
	var linkTarget string
	var major, minor uint32
	switch objType {
	case nf4Lnk:
		linkTarget = d.str()
	case nf4Blk, nf4Chr:
		major, minor = d.uint32(), d.uint32()
	}
	name := d.str()
	attrs, unsettable := d.decodeSettable()
	if d.err != nil {
		return nfs4ErrBadXDR
	}
	if unsettable >= 0 {
		return status(r, nfs4ErrAttrNotSupp)
	}
	dir, code := s.currentDir(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if code := writable(dir); code != nfs4OK {
		return status(r, code)
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return status(r, nfs4ErrBadName)
	}
	before := s.changeOf(dir)
	path := filepath.Join(dir.path(), name)
	mode := fs.FileMode(0o755)
	if attrs.mode != nil {
		mode = fs.FileMode(*attrs.mode & 0o7777)
	}
	var err error
	switch objType {
	case nf4Dir:
		err = os.Mkdir(path, mode)
	case nf4Lnk:
		err = os.Symlink(linkTarget, path)
	case nf4Fifo:
		err = unix.Mkfifo(path, uint32(mode))
	case nf4Sock:
		err = unix.Mknod(path, unix.S_IFSOCK|uint32(mode), 0)
	case nf4Blk:
		err = unix.Mknod(path, unix.S_IFBLK|uint32(mode), int(unix.Mkdev(major, minor)))
	case nf4Chr:
		err = unix.Mknod(path, unix.S_IFCHR|uint32(mode), int(unix.Mkdev(major, minor)))
	default:
		return status(r, nfs4ErrBadType)
	}
	if err != nil {
		return status(r, statusOf(err))
	}
	attrs.mode = nil
	set, code := attrs.apply(path)
	if code != nfs4OK {
		return status(r, code)
	}
	set.set(attrMode)
	child, _, code := s.lookup(dir, name)
	if code != nfs4OK {
		return status(r, code)
	}
	st.current = fhKey{child.export.index, child.ino}
	status(r, nfs4OK)
	s.changeInfo(r, before, dir)
	r.bitmap(set)
	return nfs4OK
}

func (s *Server) opRemove(st *compoundState, d *decoder, r *encoder) uint32 {
	name := d.str()
	dir, code := s.currentDir(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if code := writable(dir); code != nfs4OK {
		return status(r, code)
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return status(r, nfs4ErrBadName)
	}
	before := s.changeOf(dir)
	path := filepath.Join(dir.path(), name)
	info, err := os.Lstat(path)
	if err != nil {
		return status(r, statusOf(err))
	}
	if info.IsDir() {
		err = syscall.Rmdir(path)
	} else {
		err = syscall.Unlink(path)
	}
	if err != nil {
		return status(r, statusOf(err))
	}
	delete(s.nodes, fhKey{dir.export.index, inodeOf(info)})
	status(r, nfs4OK)
	s.changeInfo(r, before, dir)
	return nfs4OK
}

func (s *Server) opRename(st *compoundState, d *decoder, r *encoder) uint32 {
	oldName := d.str()
	newName := d.str()
	if !st.haveSaved {
		return status(r, nfs4ErrNoFileHandle)
	}
	target, code := s.currentDir(st)
	if code != nfs4OK {
		return status(r, code)
	}
	source, sourceInfo, code := s.resolve(st.saved)
	if code != nfs4OK {
		return status(r, code)
	}
	if source == nil || !sourceInfo.IsDir() {
		return status(r, nfs4ErrNotDir)
	}
	if source.export != target.export {
		return status(r, nfs4ErrXDev)
	}
	if code := writable(target); code != nfs4OK {
		return status(r, code)
	}
	for _, name := range []string{oldName, newName} {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return status(r, nfs4ErrBadName)
		}
	}
	beforeSource, beforeTarget := s.changeOf(source), s.changeOf(target)
	oldPath := filepath.Join(source.path(), oldName)
	info, err := os.Lstat(oldPath)
	if err != nil {
		return status(r, statusOf(err))
	}
	newPath := filepath.Join(target.path(), newName)
	if replaced, err := os.Lstat(newPath); err == nil {
		delete(s.nodes, fhKey{target.export.index, inodeOf(replaced)})
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return status(r, statusOf(err))
	}
	if moved, ok := s.nodes[fhKey{source.export.index, inodeOf(info)}]; ok {
		moved.parent, moved.name = target, newName
	}
	status(r, nfs4OK)
	s.changeInfo(r, beforeSource, source)
	s.changeInfo(r, beforeTarget, target)
	return nfs4OK
}

func (s *Server) opLink(st *compoundState, d *decoder, r *encoder) uint32 {
	name := d.str()
	if !st.haveSaved {
		return status(r, nfs4ErrNoFileHandle)
	}
	dir, code := s.currentDir(st)
	if code != nfs4OK {
		return status(r, code)
	}
	file, fileInfo, code := s.resolve(st.saved)
	if code != nfs4OK {
		return status(r, code)
	}
	if file == nil || fileInfo.IsDir() {
		return status(r, nfs4ErrIsDir)
	}
	if file.export != dir.export {
		return status(r, nfs4ErrXDev)
	}
	if code := writable(dir); code != nfs4OK {
		return status(r, code)
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return status(r, nfs4ErrBadName)
	}
	before := s.changeOf(dir)
	if err := os.Link(file.path(), filepath.Join(dir.path(), name)); err != nil {
		return status(r, statusOf(err))
	}
	status(r, nfs4OK)
	s.changeInfo(r, before, dir)
	return nfs4OK
}

func (s *Server) opSetAttr(st *compoundState, d *decoder, r *encoder) uint32 {
	d.stateid()
	attrs, unsettable := d.decodeSettable()
	if d.err != nil {
		return nfs4ErrBadXDR
	}
	n, _, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil {
		return status(r, nfs4ErrROFS)
	}
	if code := writable(n); code != nfs4OK {
		return status(r, code)
	}
	if unsettable >= 0 {
		status(r, nfs4ErrAttrNotSupp)
		r.bitmap(nil)
		return nfs4ErrAttrNotSupp
	}
	set, code := attrs.apply(n.path())
	status(r, code)
	r.bitmap(set)
	return code
}

func (s *Server) opReadDir(st *compoundState, d *decoder, r *encoder) uint32 {
	cookie := d.uint64()
	d.fixed(8)
	d.uint32() // dircount, advisory
	maxCount := d.uint32()
	want := d.bitmap()
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	type entry struct {
		name  string
		attrs fileAttrs
	}
	var entries []entry
	switch {
	case n == nil:
		for _, ex := range s.byIndex {
			root, rootInfo, code := s.resolve(fhKey{ex.index, 0})
			if code != nfs4OK {
				rootInfo, err := os.Lstat(ex.root)
				if err != nil {
					continue
				}
				root = &node{export: ex, ino: inodeOf(rootInfo)}
				s.nodes[fhKey{ex.index, root.ino}] = root
				entries = append(entries, entry{ex.name, s.attrsOf(root, rootInfo)})
				continue
			}
			entries = append(entries, entry{ex.name, s.attrsOf(root, rootInfo)})
		}
	case !info.IsDir():
		return status(r, nfs4ErrNotDir)
	default:
		var names []string
		if n.confined() {
			names = []string{n.export.only}
		} else {
			listed, err := os.ReadDir(n.path())
			if err != nil {
				return status(r, statusOf(err))
			}
			for _, item := range listed {
				names = append(names, item.Name())
			}
		}
		for _, name := range names {
			child, childInfo, code := s.lookup(n, name)
			if code != nfs4OK {
				continue
			}
			entries = append(entries, entry{name, s.attrsOf(child, childInfo)})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	// Cookies 0, 1 and 2 are reserved; an entry's cookie is its position
	// after them, so a listing resumes after the last entry delivered.
	if cookie != 0 && cookie < 3 {
		return status(r, nfs4ErrBadCookie)
	}
	start := 0
	if cookie >= 3 {
		start = int(cookie - 2)
	}
	if start > len(entries) {
		return status(r, nfs4ErrBadCookie)
	}
	status(r, nfs4OK)
	r.fixed(make([]byte, 8))
	budget := int(maxCount) - 16
	eof := true
	delivered := 0
	for i := start; i < len(entries); i++ {
		item := &encoder{}
		item.bool(true)
		item.uint64(uint64(i + 3))
		item.str(entries[i].name)
		item.encodeAttrs(want, entries[i].attrs)
		if len(item.buf) > budget {
			if delivered == 0 {
				r.buf = r.buf[:len(r.buf)-8]
				r.buf = r.buf[:len(r.buf)-4]
				return status(r, nfs4ErrTooSmall)
			}
			eof = false
			break
		}
		budget -= len(item.buf)
		r.buf = append(r.buf, item.buf...)
		delivered++
	}
	r.bool(false)
	r.bool(eof)
	return nfs4OK
}

func (s *Server) opSetClientID(d *decoder, r *encoder) uint32 {
	var verifier [8]byte
	copy(verifier[:], d.fixed(8))
	ident := d.opaque()
	d.uint32() // callback program
	d.str()    // callback network id
	d.str()    // callback address
	d.uint32() // callback identifier
	if d.err != nil {
		return nfs4ErrBadXDR
	}
	for _, c := range s.clients {
		if bytes.Equal(c.ident, ident) {
			if c.verifier != verifier {
				// A client that rebooted gets a new id; its state is gone.
				s.forgetClient(c.id)
				break
			}
			status(r, nfs4OK)
			r.uint64(c.id)
			r.fixed(c.confirm[:])
			return nfs4OK
		}
	}
	s.next++
	c := &client{id: s.next, verifier: verifier, ident: append([]byte(nil), ident...), renewed: time.Now()}
	_, _ = rand.Read(c.confirm[:])
	s.clients[c.id] = c
	status(r, nfs4OK)
	r.uint64(c.id)
	r.fixed(c.confirm[:])
	return nfs4OK
}

func (s *Server) forgetClient(id uint64) {
	delete(s.clients, id)
	for other, o := range s.opens {
		if o.client == id {
			delete(s.opens, other)
		}
	}
	for other, l := range s.lockers {
		if l.client == id {
			delete(s.lockers, other)
		}
	}
	for key, locks := range s.locks {
		kept := locks[:0]
		for _, l := range locks {
			if l.client != id {
				kept = append(kept, l)
			}
		}
		s.locks[key] = kept
	}
}

func (s *Server) opOpen(st *compoundState, d *decoder, r *encoder) uint32 {
	d.uint32() // seqid
	shareAccess := d.uint32()
	d.uint32() // share deny
	clientID := d.uint64()
	owner := d.opaque()
	openType := d.uint32()
	var create bool
	var createMode uint32
	var attrs settable
	unsettable := -1
	var verifier []byte
	if openType == openCreate {
		create = true
		createMode = d.uint32()
		switch createMode {
		case createUnchecked, createGuarded:
			attrs, unsettable = d.decodeSettable()
		case createExclusive:
			verifier = d.fixed(8)
		default:
			return status(r, nfs4ErrInval)
		}
	}
	claim := d.uint32()
	var name string
	switch claim {
	case claimNull:
		name = d.str()
	case claimPrevious:
		d.uint32()
		return status(r, nfs4ErrNoGrace)
	case claimDelegateCur:
		d.stateid()
		d.str()
		return status(r, nfs4ErrBadStateID)
	case claimDelegatePrev:
		d.str()
		return status(r, nfs4ErrNotSupp)
	default:
		return status(r, nfs4ErrInval)
	}
	if d.err != nil {
		return nfs4ErrBadXDR
	}
	c, ok := s.clients[clientID]
	if !ok {
		return status(r, nfs4ErrStaleClientID)
	}
	c.renewed = time.Now()
	if unsettable >= 0 {
		return status(r, nfs4ErrAttrNotSupp)
	}
	dir, code := s.currentDir(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return status(r, nfs4ErrBadName)
	}
	if (create || shareAccess&shareAccessWrite != 0) && dir.export.readOnly {
		return status(r, nfs4ErrROFS)
	}
	// The one entry of a confined root is opened for writing as any file;
	// a create under that root with another name would add a second entry.
	if create && dir.confined() && name != dir.export.only {
		return status(r, nfs4ErrAccess)
	}
	before := s.changeOf(dir)
	path := filepath.Join(dir.path(), name)
	var set bitmap
	if create {
		flags := os.O_RDWR | os.O_CREATE
		mode := fs.FileMode(0o644)
		if attrs.mode != nil {
			mode = fs.FileMode(*attrs.mode & 0o7777)
		}
		switch createMode {
		case createGuarded:
			flags |= os.O_EXCL
		case createExclusive:
			// The verifier the client sent is kept in the file's access
			// time, as servers do, so a retried create of the same file by
			// the same client succeeds and another client's is refused.
			if existing, err := os.Lstat(path); err == nil {
				if st, ok := existing.Sys().(*syscall.Stat_t); ok && exclusiveVerifier(st) == string(verifier) {
					break
				}
				return status(r, nfs4ErrExist)
			}
			flags |= os.O_EXCL
		}
		f, err := os.OpenFile(path, flags, mode)
		if err != nil {
			return status(r, statusOf(err))
		}
		_ = f.Close()
		if createMode == createExclusive {
			times := []unix.Timespec{unix.NsecToTimespec(verifierTime(verifier)), {Nsec: unix.UTIME_OMIT}}
			_ = unix.UtimesNanoAt(unix.AT_FDCWD, path, times, 0)
			set.set(attrTimeAccess)
		} else {
			attrs.mode = nil
			if createMode == createUnchecked && attrs.size != nil && *attrs.size == 0 {
				_ = os.Truncate(path, 0)
			}
			set, code = attrs.apply(path)
			if code != nfs4OK {
				return status(r, code)
			}
			set.set(attrMode)
		}
	}
	child, childInfo, code := s.lookup(dir, name)
	if code != nfs4OK {
		return status(r, code)
	}
	if childInfo.IsDir() {
		return status(r, nfs4ErrIsDir)
	}
	if childInfo.Mode()&fs.ModeSymlink != 0 {
		return status(r, nfs4ErrSymlink)
	}
	if !childInfo.Mode().IsRegular() {
		return status(r, nfs4ErrInval)
	}
	key := fhKey{child.export.index, child.ino}
	ownerKey := string(owner)
	var open *openState
	for _, o := range s.opens {
		if o.client == clientID && o.owner == ownerKey && o.key == key && !o.closed {
			open = o
			break
		}
	}
	if open == nil {
		open = &openState{other: s.newOther(), client: clientID, owner: ownerKey, key: key}
		s.opens[open.other] = open
	}
	open.access |= shareAccess
	open.seqid++
	st.current, st.haveFH = key, true
	status(r, nfs4OK)
	r.stateid(stateid{seqid: open.seqid, other: open.other})
	s.changeInfo(r, before, dir)
	r.uint32(open4ResultLockTypePosix)
	r.bitmap(set)
	r.uint32(openDelegateNone)
	return nfs4OK
}

// exclusiveVerifier reads the verifier an exclusive create stored in the
// file's access time.
func exclusiveVerifier(st *syscall.Stat_t) string {
	var v [8]byte
	v[0] = byte(st.Atim.Sec >> 24)
	v[1] = byte(st.Atim.Sec >> 16)
	v[2] = byte(st.Atim.Sec >> 8)
	v[3] = byte(st.Atim.Sec)
	v[4] = byte(st.Atim.Nsec >> 24)
	v[5] = byte(st.Atim.Nsec >> 16)
	v[6] = byte(st.Atim.Nsec >> 8)
	v[7] = byte(st.Atim.Nsec)
	return string(v[:])
}

func verifierTime(v []byte) int64 {
	sec := int64(v[0])<<24 | int64(v[1])<<16 | int64(v[2])<<8 | int64(v[3])
	nsec := int64(v[4])<<24 | int64(v[5])<<16 | int64(v[6])<<8 | int64(v[7])
	return sec*1e9 + nsec%1e9
}

func (s *Server) openFor(st stateid, key fhKey, write bool) (*openState, uint32) {
	if st.special() {
		return nil, nfs4OK
	}
	o, ok := s.opens[st.other]
	if !ok {
		if l, ok := s.lockers[st.other]; ok {
			o = l.open
		} else {
			return nil, nfs4ErrBadStateID
		}
	}
	if o.closed {
		return nil, nfs4ErrBadStateID
	}
	if o.key != key {
		return nil, nfs4ErrBadStateID
	}
	if write && o.access&shareAccessWrite == 0 {
		return nil, nfs4ErrOpenMode
	}
	return o, nfs4OK
}

func (s *Server) opOpenConfirm(d *decoder, r *encoder) uint32 {
	st := d.stateid()
	d.uint32()
	o, ok := s.opens[st.other]
	if !ok || o.closed {
		return status(r, nfs4ErrBadStateID)
	}
	o.seqid++
	status(r, nfs4OK)
	r.stateid(stateid{seqid: o.seqid, other: o.other})
	return nfs4OK
}

func (s *Server) opOpenDowngrade(d *decoder, r *encoder) uint32 {
	st := d.stateid()
	d.uint32()
	access := d.uint32()
	d.uint32()
	o, ok := s.opens[st.other]
	if !ok || o.closed {
		return status(r, nfs4ErrBadStateID)
	}
	if access&^o.access != 0 {
		return status(r, nfs4ErrInval)
	}
	o.access = access
	o.seqid++
	status(r, nfs4OK)
	r.stateid(stateid{seqid: o.seqid, other: o.other})
	return nfs4OK
}

func (s *Server) opClose(st *compoundState, d *decoder, r *encoder) uint32 {
	d.uint32()
	id := d.stateid()
	o, ok := s.opens[id.other]
	if !ok || o.closed {
		return status(r, nfs4ErrBadStateID)
	}
	o.closed = true
	delete(s.opens, id.other)
	for other, l := range s.lockers {
		if l.open == o {
			delete(s.lockers, other)
		}
	}
	o.seqid++
	status(r, nfs4OK)
	r.stateid(stateid{seqid: o.seqid, other: o.other})
	return nfs4OK
}

func (s *Server) opRead(st *compoundState, d *decoder, r *encoder) uint32 {
	id := d.stateid()
	offset := d.uint64()
	count := d.uint32()
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil || info.IsDir() {
		return status(r, nfs4ErrIsDir)
	}
	if _, code := s.openFor(id, fhKey{n.export.index, n.ino}, false); code != nfs4OK {
		return status(r, code)
	}
	if count > maxIO {
		count = maxIO
	}
	f, err := os.Open(n.path())
	if err != nil {
		return status(r, statusOf(err))
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, count)
	got, err := f.ReadAt(buf, int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return status(r, statusOf(err))
	}
	status(r, nfs4OK)
	r.bool(errors.Is(err, io.EOF) || got < int(count))
	r.opaque(buf[:got])
	return nfs4OK
}

func (s *Server) opWrite(st *compoundState, d *decoder, r *encoder) uint32 {
	id := d.stateid()
	offset := d.uint64()
	stable := d.uint32()
	data := d.opaque()
	if d.err != nil {
		return nfs4ErrBadXDR
	}
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil || info.IsDir() {
		return status(r, nfs4ErrIsDir)
	}
	if code := writable(n); code != nfs4OK {
		return status(r, code)
	}
	if _, code := s.openFor(id, fhKey{n.export.index, n.ino}, true); code != nfs4OK {
		return status(r, code)
	}
	f, err := os.OpenFile(n.path(), os.O_WRONLY, 0)
	if err != nil {
		return status(r, statusOf(err))
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt(data, int64(offset)); err != nil {
		return status(r, statusOf(err))
	}
	if stable != unstable4 {
		if err := f.Sync(); err != nil {
			return status(r, statusOf(err))
		}
	}
	status(r, nfs4OK)
	r.uint32(uint32(len(data)))
	if stable == unstable4 {
		r.uint32(unstable4)
	} else {
		r.uint32(fileSync4)
	}
	r.fixed(s.boot[:])
	return nfs4OK
}

func (s *Server) opCommit(st *compoundState, d *decoder, r *encoder) uint32 {
	d.uint64()
	d.uint32()
	n, info, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil || info.IsDir() {
		return status(r, nfs4ErrIsDir)
	}
	f, err := os.OpenFile(n.path(), os.O_WRONLY, 0)
	if err != nil {
		return status(r, statusOf(err))
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return status(r, statusOf(err))
	}
	status(r, nfs4OK)
	r.fixed(s.boot[:])
	return nfs4OK
}

// conflicting finds a lock on the file another owner holds over the range
// that the requested lock cannot share.
func (s *Server) conflicting(key fhKey, owner string, clientID uint64, offset, length uint64, write bool) *byteLock {
	end := offset + length
	if length == ^uint64(0) || end < offset {
		end = ^uint64(0)
	}
	for i := range s.locks[key] {
		l := &s.locks[key][i]
		if l.owner == owner && l.client == clientID {
			continue
		}
		lend := l.offset + l.length
		if l.length == ^uint64(0) || lend < l.offset {
			lend = ^uint64(0)
		}
		if l.offset >= end || offset >= lend {
			continue
		}
		if write || l.write {
			return l
		}
	}
	return nil
}

func (r *encoder) denied(l *byteLock) {
	r.uint64(l.offset)
	r.uint64(l.length)
	if l.write {
		r.uint32(writeLT)
	} else {
		r.uint32(readLT)
	}
	r.uint64(l.client)
	r.opaque([]byte(l.owner))
}

func (s *Server) opLock(st *compoundState, d *decoder, r *encoder) uint32 {
	lockType := d.uint32()
	reclaim := d.bool()
	offset := d.uint64()
	length := d.uint64()
	newOwner := d.bool()
	var locker *lockState
	var owner string
	var clientID uint64
	if newOwner {
		d.uint32() // open seqid
		openID := d.stateid()
		d.uint32() // lock seqid
		clientID = d.uint64()
		owner = string(d.opaque())
		if d.err != nil {
			return nfs4ErrBadXDR
		}
		o, ok := s.opens[openID.other]
		if !ok || o.closed {
			return status(r, nfs4ErrBadStateID)
		}
		for _, l := range s.lockers {
			if l.open == o && l.owner == owner && l.client == clientID {
				locker = l
				break
			}
		}
		if locker == nil {
			locker = &lockState{other: s.newOther(), client: clientID, owner: owner, key: o.key, open: o}
			s.lockers[locker.other] = locker
		}
	} else {
		lockID := d.stateid()
		d.uint32() // lock seqid
		if d.err != nil {
			return nfs4ErrBadXDR
		}
		l, ok := s.lockers[lockID.other]
		if !ok {
			return status(r, nfs4ErrBadStateID)
		}
		locker, owner, clientID = l, l.owner, l.client
	}
	if reclaim {
		return status(r, nfs4ErrNoGrace)
	}
	n, _, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil {
		return status(r, nfs4ErrInval)
	}
	key := fhKey{n.export.index, n.ino}
	if locker.key != key {
		return status(r, nfs4ErrBadStateID)
	}
	if length == 0 {
		return status(r, nfs4ErrInval)
	}
	write := lockType == writeLT || lockType == writeWLT
	if write && locker.open.access&shareAccessWrite == 0 {
		return status(r, nfs4ErrOpenMode)
	}
	if conflict := s.conflicting(key, owner, clientID, offset, length, write); conflict != nil {
		status(r, nfs4ErrDenied)
		r.denied(conflict)
		return nfs4ErrDenied
	}
	s.locks[key] = s.unlocked(s.locks[key], owner, clientID, offset, length)
	s.locks[key] = append(s.locks[key], byteLock{owner: owner, client: clientID, offset: offset, length: length, write: write})
	locker.seqid++
	status(r, nfs4OK)
	r.stateid(stateid{seqid: locker.seqid, other: locker.other})
	return nfs4OK
}

// unlocked removes the owner's locks over the range, splitting a lock the
// range cuts through.
func (s *Server) unlocked(locks []byteLock, owner string, clientID uint64, offset, length uint64) []byteLock {
	end := offset + length
	if length == ^uint64(0) || end < offset {
		end = ^uint64(0)
	}
	var kept []byteLock
	for _, l := range locks {
		if l.owner != owner || l.client != clientID {
			kept = append(kept, l)
			continue
		}
		lend := l.offset + l.length
		if l.length == ^uint64(0) || lend < l.offset {
			lend = ^uint64(0)
		}
		if l.offset >= end || offset >= lend {
			kept = append(kept, l)
			continue
		}
		if l.offset < offset {
			kept = append(kept, byteLock{owner: owner, client: clientID, offset: l.offset, length: offset - l.offset, write: l.write})
		}
		if lend > end && end != ^uint64(0) {
			tail := byteLock{owner: owner, client: clientID, offset: end, write: l.write}
			if lend == ^uint64(0) {
				tail.length = ^uint64(0)
			} else {
				tail.length = lend - end
			}
			kept = append(kept, tail)
		}
	}
	return kept
}

func (s *Server) opLockT(st *compoundState, d *decoder, r *encoder) uint32 {
	lockType := d.uint32()
	offset := d.uint64()
	length := d.uint64()
	clientID := d.uint64()
	owner := string(d.opaque())
	if d.err != nil {
		return nfs4ErrBadXDR
	}
	if _, ok := s.clients[clientID]; !ok {
		return status(r, nfs4ErrStaleClientID)
	}
	n, _, code := s.current(st)
	if code != nfs4OK {
		return status(r, code)
	}
	if n == nil {
		return status(r, nfs4ErrInval)
	}
	write := lockType == writeLT || lockType == writeWLT
	if conflict := s.conflicting(fhKey{n.export.index, n.ino}, owner, clientID, offset, length, write); conflict != nil {
		status(r, nfs4ErrDenied)
		r.denied(conflict)
		return nfs4ErrDenied
	}
	return status(r, nfs4OK)
}

func (s *Server) opLockU(st *compoundState, d *decoder, r *encoder) uint32 {
	d.uint32() // lock type
	d.uint32() // seqid
	lockID := d.stateid()
	offset := d.uint64()
	length := d.uint64()
	l, ok := s.lockers[lockID.other]
	if !ok {
		return status(r, nfs4ErrBadStateID)
	}
	s.locks[l.key] = s.unlocked(s.locks[l.key], l.owner, l.client, offset, length)
	l.seqid++
	status(r, nfs4OK)
	r.stateid(stateid{seqid: l.seqid, other: l.other})
	return nfs4OK
}
