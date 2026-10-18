package fileshare

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testClient speaks the server's wire protocol the way a kernel client does:
// RPC calls with AUTH_SYS over one TCP connection, each a COMPOUND.
type testClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	xid  uint32
	uid  uint32
}

func startServer(t *testing.T, exports map[string]Export) *testClient {
	t.Helper()
	s, err := New(exports)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Serve(ctx, ln) }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &testClient{t: t, conn: conn, r: bufio.NewReader(conn)}
}

// op is one operation's arguments, written by the test.
type op func(e *encoder)

func (c *testClient) compound(ops ...op) (uint32, *decoder) {
	c.t.Helper()
	c.xid++
	call := &encoder{}
	call.uint32(c.xid)
	call.uint32(msgCall)
	call.uint32(rpcVersion)
	call.uint32(nfsProgram)
	call.uint32(nfsVersion4)
	call.uint32(procCompound)
	cred := &encoder{}
	cred.uint32(0)
	cred.str("guest")
	cred.uint32(c.uid)
	cred.uint32(0)
	cred.uint32(0)
	call.uint32(authSys)
	call.opaque(cred.buf)
	call.uint32(authNone)
	call.opaque(nil)
	call.str("")
	call.uint32(0)
	call.uint32(uint32(len(ops)))
	for _, o := range ops {
		o(call)
	}
	if err := writeRecord(c.conn, call.buf); err != nil {
		c.t.Fatal(err)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	record, err := readRecord(c.r)
	if err != nil {
		c.t.Fatal(err)
	}
	d := &decoder{buf: record}
	if d.uint32() != c.xid || d.uint32() != msgReply || d.uint32() != replyAccepted {
		c.t.Fatalf("reply header %x", record[:12])
	}
	d.uint32()
	d.opaque()
	if accept := d.uint32(); accept != acceptSuccess {
		c.t.Fatalf("RPC accept status %d", accept)
	}
	st := d.uint32()
	d.opaque()
	d.uint32()
	return st, d
}

func putRootFH(e *encoder) { e.uint32(opPutRootFH) }
func getFH(e *encoder)     { e.uint32(opGetFH) }
func lookup(name string) op {
	return func(e *encoder) { e.uint32(opLookup); e.str(name) }
}
func putFH(fh []byte) op {
	return func(e *encoder) { e.uint32(opPutFH); e.opaque(fh) }
}
func getAttr(attrs ...int) op {
	return func(e *encoder) {
		var b bitmap
		for _, a := range attrs {
			b.set(a)
		}
		e.uint32(opGetAttr)
		e.bitmap(b)
	}
}

// expect reads an op's number and status from the results.
func expect(t *testing.T, d *decoder, op uint32, want uint32) {
	t.Helper()
	if got := d.uint32(); got != op {
		t.Fatalf("result names op %d, want %d", got, op)
	}
	if got := d.uint32(); got != want {
		t.Fatalf("op %d answered %d, want %d", op, got, want)
	}
}

func (c *testClient) clientID() uint64 {
	c.t.Helper()
	st, d := c.compound(func(e *encoder) {
		e.uint32(opSetClientID)
		e.fixed([]byte("verifier"))
		e.opaque([]byte("Linux NFSv4.0 guest/10.0.0.10"))
		e.uint32(0x40000000)
		e.str("tcp")
		e.str("10.0.0.10.8.1")
		e.uint32(1)
	})
	if st != nfs4OK {
		c.t.Fatalf("SETCLIENTID answered %d", st)
	}
	expect(c.t, d, opSetClientID, nfs4OK)
	id := d.uint64()
	confirm := d.fixed(8)
	st, _ = c.compound(func(e *encoder) {
		e.uint32(opSetClientIDConf)
		e.uint64(id)
		e.fixed(confirm)
	})
	if st != nfs4OK {
		c.t.Fatalf("SETCLIENTID_CONFIRM answered %d", st)
	}
	return id
}

func (c *testClient) exportFH(name string) []byte {
	c.t.Helper()
	st, d := c.compound(putRootFH, lookup(name), getFH)
	if st != nfs4OK {
		c.t.Fatalf("looking up export %s answered %d", name, st)
	}
	expect(c.t, d, opPutRootFH, nfs4OK)
	expect(c.t, d, opLookup, nfs4OK)
	expect(c.t, d, opGetFH, nfs4OK)
	return append([]byte(nil), d.opaque()...)
}

func openFile(clientID uint64, owner, name string, access uint32, create bool) op {
	return func(e *encoder) {
		e.uint32(opOpen)
		e.uint32(1)
		e.uint32(access)
		e.uint32(0)
		e.uint64(clientID)
		e.opaque([]byte(owner))
		if create {
			e.uint32(openCreate)
			e.uint32(createUnchecked)
			var b bitmap
			b.set(attrMode)
			e.bitmap(b)
			vals := &encoder{}
			vals.uint32(0o640)
			e.opaque(vals.buf)
		} else {
			e.uint32(openNoCreate)
		}
		e.uint32(claimNull)
		e.str(name)
	}
}

func TestFileShareServesAnExportReadWrite(t *testing.T) {
	root := t.TempDir()
	c := startServer(t, map[string]Export{"share": {Root: root}})
	clientID := c.clientID()

	st, d := c.compound(putRootFH, getAttr(attrSupportedAttrs, attrType, attrFHExpireType, attrLeaseTime, attrFileID))
	if st != nfs4OK {
		t.Fatalf("root GETATTR answered %d", st)
	}
	expect(t, d, opPutRootFH, nfs4OK)
	expect(t, d, opGetAttr, nfs4OK)
	answered := d.bitmap()
	for _, a := range []int{attrSupportedAttrs, attrType, attrFHExpireType, attrLeaseTime, attrFileID} {
		if !answered.has(a) {
			t.Fatalf("the root does not answer attribute %d", a)
		}
	}
	vals := &decoder{buf: d.opaque()}
	vals.bitmap()
	if typ := vals.uint32(); typ != nf4Dir {
		t.Fatalf("the root's type is %d", typ)
	}
	if expire := vals.uint32(); expire != fh4Persistent {
		t.Fatalf("filehandles expire as %d", expire)
	}
	if lease := vals.uint32(); lease != leaseSeconds {
		t.Fatalf("the lease is %d seconds", lease)
	}

	share := c.exportFH("share")
	st, d = c.compound(putFH(share), openFile(clientID, "owner-1", "hello.txt", shareAccessBoth, true), getFH)
	if st != nfs4OK {
		t.Fatalf("OPEN with create answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opOpen, nfs4OK)
	openID := d.stateid()
	d.bool()
	d.uint64()
	d.uint64()
	if flags := d.uint32(); flags&open4ResultLockTypePosix == 0 {
		t.Fatalf("OPEN reports lock type flags %d", flags)
	}
	if set := d.bitmap(); !set.has(attrMode) {
		t.Fatal("OPEN did not report the mode it set")
	}
	if deleg := d.uint32(); deleg != openDelegateNone {
		t.Fatalf("OPEN granted delegation %d", deleg)
	}
	expect(t, d, opGetFH, nfs4OK)
	file := append([]byte(nil), d.opaque()...)
	if info, err := os.Stat(filepath.Join(root, "hello.txt")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("the created file: %v %v", info, err)
	}

	st, d = c.compound(putFH(file), func(e *encoder) {
		e.uint32(opWrite)
		e.stateid(openID)
		e.uint64(0)
		e.uint32(fileSync4)
		e.opaque([]byte("hello from the guest\n"))
	}, func(e *encoder) {
		e.uint32(opRead)
		e.stateid(openID)
		e.uint64(6)
		e.uint32(4)
	})
	if st != nfs4OK {
		t.Fatalf("WRITE then READ answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opWrite, nfs4OK)
	if n := d.uint32(); n != 21 {
		t.Fatalf("WRITE counted %d bytes", n)
	}
	if committed := d.uint32(); committed != fileSync4 {
		t.Fatalf("WRITE committed as %d", committed)
	}
	d.fixed(8)
	expect(t, d, opRead, nfs4OK)
	if eof := d.bool(); eof {
		t.Fatal("a read inside the file reports EOF")
	}
	if got := string(d.opaque()); got != "from" {
		t.Fatalf("READ at 6 answered %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "hello.txt")); string(data) != "hello from the guest\n" {
		t.Fatalf("the host file holds %q", data)
	}

	// What the host writes, the share reads: the file's change attribute
	// moves and a read past the end reports EOF.
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("rewritten"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, d = c.compound(putFH(file), func(e *encoder) {
		e.uint32(opRead)
		e.stateid(stateid{})
		e.uint64(0)
		e.uint32(100)
	})
	if st != nfs4OK {
		t.Fatalf("anonymous READ answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opRead, nfs4OK)
	if eof := d.bool(); !eof {
		t.Fatal("a read to the end does not report EOF")
	}
	if got := string(d.opaque()); got != "rewritten" {
		t.Fatalf("READ answered %q", got)
	}

	// A second owner's write lock conflicts with the first's; its own
	// range after the first unlocks is granted.
	st, d = c.compound(putFH(file), func(e *encoder) {
		e.uint32(opLock)
		e.uint32(writeLT)
		e.bool(false)
		e.uint64(0)
		e.uint64(^uint64(0))
		e.bool(true)
		e.uint32(1)
		e.stateid(openID)
		e.uint32(0)
		e.uint64(clientID)
		e.opaque([]byte("lock-owner-1"))
	})
	if st != nfs4OK {
		t.Fatalf("LOCK answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opLock, nfs4OK)
	lockID := d.stateid()
	st, d = c.compound(putFH(file), func(e *encoder) {
		e.uint32(opLockT)
		e.uint32(readLT)
		e.uint64(10)
		e.uint64(5)
		e.uint64(clientID)
		e.opaque([]byte("lock-owner-2"))
	})
	if st != nfs4ErrDenied {
		t.Fatalf("LOCKT against a write lock answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opLockT, nfs4ErrDenied)
	if offset, length := d.uint64(), d.uint64(); offset != 0 || length != ^uint64(0) {
		t.Fatalf("the conflicting lock spans %d+%d", offset, length)
	}
	st, _ = c.compound(putFH(file), func(e *encoder) {
		e.uint32(opLockU)
		e.uint32(writeLT)
		e.uint32(1)
		e.stateid(lockID)
		e.uint64(0)
		e.uint64(^uint64(0))
	}, func(e *encoder) {
		e.uint32(opLockT)
		e.uint32(readLT)
		e.uint64(10)
		e.uint64(5)
		e.uint64(clientID)
		e.opaque([]byte("lock-owner-2"))
	})
	if st != nfs4OK {
		t.Fatalf("LOCKU then LOCKT answered %d", st)
	}

	// The directory lists the file with the attributes asked for, a rename
	// moves it, and a remove ends it; the file's handle names it through
	// the rename.
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, d = c.compound(putFH(share), func(e *encoder) {
		e.uint32(opReadDir)
		e.uint64(0)
		e.fixed(make([]byte, 8))
		e.uint32(1024)
		e.uint32(4096)
		var b bitmap
		b.set(attrType)
		b.set(attrFileID)
		e.bitmap(b)
	})
	if st != nfs4OK {
		t.Fatalf("READDIR answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opReadDir, nfs4OK)
	d.fixed(8)
	var names []string
	for d.bool() {
		d.uint64()
		names = append(names, d.str())
		d.bitmap()
		d.opaque()
	}
	if !d.bool() {
		t.Fatal("the listing does not end")
	}
	if strings.Join(names, ",") != "hello.txt,sub" {
		t.Fatalf("READDIR listed %v", names)
	}
	st, _ = c.compound(putFH(share), func(e *encoder) { e.uint32(opSaveFH) }, lookup("sub"), func(e *encoder) {
		e.uint32(opRename)
		e.str("hello.txt")
		e.str("moved.txt")
	})
	if st != nfs4OK {
		t.Fatalf("RENAME answered %d", st)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "moved.txt")); err != nil {
		t.Fatalf("the renamed file: %v", err)
	}
	st, _ = c.compound(putFH(file), getAttr(attrSize))
	if st != nfs4OK {
		t.Fatalf("GETATTR through the rename answered %d", st)
	}
	st, _ = c.compound(putFH(share), lookup("sub"), func(e *encoder) {
		e.uint32(opRemove)
		e.str("moved.txt")
	})
	if st != nfs4OK {
		t.Fatalf("REMOVE answered %d", st)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("the removed file: %v", err)
	}
	st, _ = c.compound(putFH(file), getAttr(attrSize))
	if st != nfs4ErrStale {
		t.Fatalf("a removed file's handle answers %d, want stale", st)
	}
	st, _ = c.compound(putFH(file[:len(file)-1]))
	if st != nfs4ErrBadHandle {
		t.Fatalf("a malformed handle answers %d", st)
	}
}

func TestFileShareRefusesWritesOnAReadOnlyExport(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "layer.txt"), []byte("layer"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := startServer(t, map[string]Export{"ro": {Root: root, ReadOnly: true}})
	clientID := c.clientID()
	ro := c.exportFH("ro")
	st, _ := c.compound(putFH(ro), openFile(clientID, "owner", "new.txt", shareAccessBoth, true))
	if st != nfs4ErrROFS {
		t.Fatalf("a create on the read-only export answered %d", st)
	}
	st, d := c.compound(putFH(ro), func(e *encoder) { e.uint32(opAccess); e.uint32(accessRead | accessModify | accessLookup) })
	if st != nfs4OK {
		t.Fatalf("ACCESS answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opAccess, nfs4OK)
	d.uint32()
	if granted := d.uint32(); granted != accessRead|accessLookup {
		t.Fatalf("ACCESS granted %#x on the read-only export", granted)
	}
	st, d = c.compound(putFH(ro), openFile(clientID, "owner", "layer.txt", shareAccessRead, false), func(e *encoder) {
		e.uint32(opRead)
		e.stateid(stateid{})
		e.uint64(0)
		e.uint32(10)
	})
	if st != nfs4OK {
		t.Fatalf("a read on the read-only export answered %d", st)
	}
	expect(t, d, opPutFH, nfs4OK)
	expect(t, d, opOpen, nfs4OK)
	d.stateid()
	d.bool()
	d.uint64()
	d.uint64()
	d.uint32()
	d.bitmap()
	d.uint32()
	expect(t, d, opRead, nfs4OK)
	d.bool()
	if got := string(d.opaque()); got != "layer" {
		t.Fatalf("READ answered %q", got)
	}
}

func TestFileShareAnswersNullAndRefusesOtherPrograms(t *testing.T) {
	c := startServer(t, map[string]Export{"share": {Root: t.TempDir()}})
	call := &encoder{}
	call.uint32(7)
	call.uint32(msgCall)
	call.uint32(rpcVersion)
	call.uint32(nfsProgram)
	call.uint32(nfsVersion4)
	call.uint32(procNull)
	call.uint32(authNone)
	call.opaque(nil)
	call.uint32(authNone)
	call.opaque(nil)
	if err := writeRecord(c.conn, call.buf); err != nil {
		t.Fatal(err)
	}
	record, err := readRecord(c.r)
	if err != nil {
		t.Fatal(err)
	}
	d := &decoder{buf: record}
	if d.uint32() != 7 || d.uint32() != msgReply || d.uint32() != replyAccepted {
		t.Fatalf("NULL reply header %x", record)
	}
	d.uint32()
	d.opaque()
	if accept := d.uint32(); accept != acceptSuccess || !d.done() {
		t.Fatalf("NULL answered accept status %d with %d bytes left", accept, len(record)-d.off)
	}
	call.buf = call.buf[:0]
	call.uint32(8)
	call.uint32(msgCall)
	call.uint32(rpcVersion)
	call.uint32(100005)
	call.uint32(3)
	call.uint32(procNull)
	call.uint32(authNone)
	call.opaque(nil)
	call.uint32(authNone)
	call.opaque(nil)
	if err := writeRecord(c.conn, call.buf); err != nil {
		t.Fatal(err)
	}
	record, err = readRecord(c.r)
	if err != nil {
		t.Fatal(err)
	}
	d = &decoder{buf: record}
	d.uint32()
	d.uint32()
	d.uint32()
	d.uint32()
	d.opaque()
	if accept := d.uint32(); accept != acceptProgUnavail {
		t.Fatalf("the mount program answered accept status %d", accept)
	}
}
