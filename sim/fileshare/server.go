// Package fileshare serves host directories to guests over NFSv4.0 (RFC
// 7530): what a bind mount gives a container, a guest mounts over its
// network from the simulator that holds the directory. The server speaks
// the one network filesystem the guest kernel carries, needs no privilege
// and one TCP port, and answers AUTH_SYS as a share with root squashing off.
package fileshare

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Export is a host directory the server serves under a name: a client mounts
// `<server>:/<name>`.
type Export struct {
	Root     string
	ReadOnly bool
}

type export struct {
	index    uint32
	name     string
	root     string
	readOnly bool
}

// node is a file or directory a filehandle names: its export, its parent and
// its name, so a rename moves one node and its descendants with it.
type node struct {
	export *export
	parent *node
	name   string
	ino    uint64
}

// path is the node's host path.
func (n *node) path() string {
	if n.parent == nil {
		return n.export.root
	}
	return filepath.Join(n.parent.path(), n.name)
}

type fhKey struct {
	export uint32
	ino    uint64
}

const pseudoRootExport = ^uint32(0)

// Server is one NFSv4.0 server over its exports.
type Server struct {
	mu      sync.Mutex
	exports map[string]*export
	byIndex []*export
	nodes   map[fhKey]*node
	clients map[uint64]*client
	opens   map[[12]byte]*openState
	lockers map[[12]byte]*lockState
	locks   map[fhKey][]byteLock
	next    uint64
	boot    [8]byte
	started time.Time
}

type client struct {
	id        uint64
	verifier  [8]byte
	ident     []byte
	confirm   [8]byte
	confirmed bool
	renewed   time.Time
}

// openState is one OPEN's stateid: the file it opened and the access it
// holds, the seqid advancing with each operation on it.
type openState struct {
	other  [12]byte
	seqid  uint32
	client uint64
	owner  string
	key    fhKey
	access uint32
	closed bool
}

// lockState is a lock-owner's stateid on one file.
type lockState struct {
	other  [12]byte
	seqid  uint32
	client uint64
	owner  string
	key    fhKey
	open   *openState
}

type byteLock struct {
	owner  string
	client uint64
	offset uint64
	length uint64
	write  bool
}

// New serves the exports, by name.
func New(exports map[string]Export) (*Server, error) {
	s := &Server{
		exports: map[string]*export{},
		nodes:   map[fhKey]*node{},
		clients: map[uint64]*client{},
		opens:   map[[12]byte]*openState{},
		lockers: map[[12]byte]*lockState{},
		locks:   map[fhKey][]byteLock{},
		started: time.Now(),
	}
	if _, err := rand.Read(s.boot[:]); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(exports))
	for name := range exports {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := s.addExport(name, exports[name]); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Server) addExport(name string, e Export) error {
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("export name %q: a name without a slash is required", name)
	}
	root, err := filepath.Abs(e.Root)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("export %s: %w", name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("export %s: %s is not a directory", name, root)
	}
	if _, exists := s.exports[name]; exists {
		return fmt.Errorf("export %s is already served", name)
	}
	ex := &export{index: uint32(len(s.byIndex)), name: name, root: root, readOnly: e.ReadOnly}
	s.exports[name] = ex
	s.byIndex = append(s.byIndex, ex)
	s.nodes[fhKey{ex.index, inodeOf(info)}] = &node{export: ex, ino: inodeOf(info)}
	return nil
}

// AddExport serves one more directory; a name already served is replaced
// only when it serves the same directory.
func (s *Server) AddExport(name string, e Export) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if have, ok := s.exports[name]; ok {
		root, _ := filepath.Abs(e.Root)
		if have.root == root && have.readOnly == e.ReadOnly {
			return nil
		}
	}
	return s.addExport(name, e)
}

// Serve answers connections on the listener until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, conn)
	}
}

func inodeOf(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// filehandle is the wire form of a node: the export's index and the inode,
// which outlive the server's process on the same directory, and the boot
// verifier, which tells a handle of this process from an older one's.
func (s *Server) filehandle(key fhKey) []byte {
	fh := make([]byte, 0, 20)
	fh = binary.BigEndian.AppendUint32(fh, key.export)
	fh = binary.BigEndian.AppendUint64(fh, key.ino)
	fh = append(fh, s.boot[:]...)
	return fh
}

func (s *Server) parseFH(fh []byte) (fhKey, uint32) {
	if len(fh) != 20 {
		return fhKey{}, nfs4ErrBadHandle
	}
	key := fhKey{export: binary.BigEndian.Uint32(fh), ino: binary.BigEndian.Uint64(fh[4:])}
	if string(fh[12:]) != string(s.boot[:]) {
		return key, nfs4ErrStale
	}
	return key, nfs4OK
}

// remember records a node for its handle, replacing a node whose inode the
// filesystem reused.
func (s *Server) remember(n *node) fhKey {
	key := fhKey{n.export.index, n.ino}
	if have, ok := s.nodes[key]; ok && have.path() == n.path() {
		return key
	}
	s.nodes[key] = n
	return key
}

// resolve finds the node of a key and checks it still names what the
// filesystem holds at its path.
func (s *Server) resolve(key fhKey) (*node, fs.FileInfo, uint32) {
	if key.export == pseudoRootExport {
		return nil, nil, nfs4OK
	}
	n, ok := s.nodes[key]
	if !ok {
		return nil, nil, nfs4ErrStale
	}
	info, err := os.Lstat(n.path())
	if err != nil || inodeOf(info) != n.ino {
		delete(s.nodes, key)
		return nil, nil, nfs4ErrStale
	}
	return n, info, nfs4OK
}

// lookup names a child of a directory node.
func (s *Server) lookup(dir *node, name string) (*node, fs.FileInfo, uint32) {
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return nil, nil, nfs4ErrBadName
	}
	if name == "." {
		info, err := os.Lstat(dir.path())
		return dir, info, statusOf(err)
	}
	if name == ".." {
		if dir.parent == nil {
			return nil, nil, nfs4ErrNoEnt
		}
		info, err := os.Lstat(dir.parent.path())
		return dir.parent, info, statusOf(err)
	}
	info, err := os.Lstat(filepath.Join(dir.path(), name))
	if err != nil {
		return nil, nil, statusOf(err)
	}
	child := &node{export: dir.export, parent: dir, name: name, ino: inodeOf(info)}
	s.remember(child)
	return s.nodes[fhKey{dir.export.index, child.ino}], info, nfs4OK
}

func statusOf(err error) uint32 {
	switch {
	case err == nil:
		return nfs4OK
	case errors.Is(err, fs.ErrNotExist):
		return nfs4ErrNoEnt
	case errors.Is(err, fs.ErrExist):
		return nfs4ErrExist
	case errors.Is(err, fs.ErrPermission):
		return nfs4ErrAccess
	case errors.Is(err, syscall.ENOTDIR):
		return nfs4ErrNotDir
	case errors.Is(err, syscall.EISDIR):
		return nfs4ErrIsDir
	case errors.Is(err, syscall.ENOTEMPTY):
		return nfs4ErrNotEmpty
	case errors.Is(err, syscall.EXDEV):
		return nfs4ErrXDev
	case errors.Is(err, syscall.ENOSPC):
		return nfs4ErrNoSpc
	case errors.Is(err, syscall.EFBIG):
		return nfs4ErrFBig
	case errors.Is(err, syscall.EMLINK):
		return nfs4ErrMLink
	case errors.Is(err, syscall.ENAMETOOLONG):
		return nfs4ErrNameTooLong
	case errors.Is(err, syscall.EINVAL):
		return nfs4ErrInval
	case errors.Is(err, syscall.EROFS):
		return nfs4ErrROFS
	case errors.Is(err, syscall.EPERM):
		return nfs4ErrPerm
	}
	return nfs4ErrIO
}

// stateid is the wire form of an open or lock state.
type stateid struct {
	seqid uint32
	other [12]byte
}

func (d *decoder) stateid() stateid {
	var st stateid
	st.seqid = d.uint32()
	copy(st.other[:], d.fixed(12))
	return st
}

func (e *encoder) stateid(st stateid) {
	e.uint32(st.seqid)
	e.fixed(st.other[:])
}

func (st stateid) special() bool {
	zero, ones := true, true
	for _, b := range st.other {
		if b != 0 {
			zero = false
		}
		if b != 0xff {
			ones = false
		}
	}
	return (zero && st.seqid == 0) || (ones && st.seqid == ^uint32(0))
}

func (s *Server) newOther() [12]byte {
	s.next++
	var other [12]byte
	copy(other[:4], s.boot[:4])
	binary.BigEndian.PutUint64(other[4:], s.next)
	return other
}
