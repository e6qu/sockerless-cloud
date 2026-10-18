package fileshare

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// ONC RPC (RFC 5531) over TCP with record marking (RFC 5531 section 11): a
// record is fragments, each a four-byte header whose top bit marks the last
// fragment and whose rest is the fragment's length.

const (
	rpcVersion    = 2
	nfsProgram    = 100003
	nfsVersion4   = 4
	procNull      = 0
	procCompound  = 1
	msgCall       = 0
	msgReply      = 1
	replyAccepted = 0
	replyDenied   = 1

	acceptSuccess      = 0
	acceptProgUnavail  = 1
	acceptProgMismatch = 2
	acceptProcUnavail  = 3
	acceptGarbageArgs  = 4

	authNone = 0
	authSys  = 1

	// maxRecord bounds one request: a WRITE of the largest size the server
	// advertises plus its headers.
	maxRecord = maxIO + 64<<10
)

// credentials is the AUTH_SYS identity a call carries; a call with AUTH_NONE
// runs as nobody.
type credentials struct {
	uid, gid uint32
	gids     []uint32
}

func readRecord(r *bufio.Reader) ([]byte, error) {
	var record []byte
	for {
		var header [4]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return nil, err
		}
		h := binary.BigEndian.Uint32(header[:])
		last := h&0x80000000 != 0
		n := int(h & 0x7fffffff)
		if len(record)+n > maxRecord {
			return nil, fmt.Errorf("RPC record of more than %d bytes", maxRecord)
		}
		fragment := make([]byte, n)
		if _, err := io.ReadFull(r, fragment); err != nil {
			return nil, err
		}
		record = append(record, fragment...)
		if last {
			return record, nil
		}
	}
}

func writeRecord(w io.Writer, record []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 0x80000000|uint32(len(record)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(record)
	return err
}

// serveConn answers the calls of one connection in order, until the peer
// closes it or ctx ends.
func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	reader := bufio.NewReaderSize(conn, 256<<10)
	var writeMu sync.Mutex
	for {
		record, err := readRecord(reader)
		if err != nil {
			return
		}
		reply := s.handleCall(record)
		if reply == nil {
			continue
		}
		writeMu.Lock()
		err = writeRecord(conn, reply)
		writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

// handleCall decodes one RPC call and returns its reply record; a call whose
// header cannot be read gets no reply, as the peer cannot be matched to it.
func (s *Server) handleCall(record []byte) []byte {
	d := &decoder{buf: record}
	xid := d.uint32()
	if d.uint32() != msgCall {
		return nil
	}
	version := d.uint32()
	program := d.uint32()
	progVersion := d.uint32()
	procedure := d.uint32()
	credFlavor := d.uint32()
	credBody := d.opaque()
	d.uint32() // verifier flavor
	d.opaque() // verifier body
	if d.err != nil {
		return nil
	}
	reply := &encoder{}
	reply.uint32(xid)
	reply.uint32(msgReply)
	if version != rpcVersion {
		reply.uint32(replyDenied)
		reply.uint32(0) // RPC_MISMATCH
		reply.uint32(rpcVersion)
		reply.uint32(rpcVersion)
		return reply.buf
	}
	reply.uint32(replyAccepted)
	reply.uint32(authNone)
	reply.uint32(0)
	switch {
	case program != nfsProgram:
		reply.uint32(acceptProgUnavail)
	case progVersion != nfsVersion4:
		reply.uint32(acceptProgMismatch)
		reply.uint32(nfsVersion4)
		reply.uint32(nfsVersion4)
	case procedure == procNull:
		reply.uint32(acceptSuccess)
	case procedure == procCompound:
		cred, err := parseCredentials(credFlavor, credBody)
		if err != nil {
			reply.uint32(acceptGarbageArgs)
			return reply.buf
		}
		args := &decoder{buf: d.buf[d.off:]}
		result, ok := s.compound(cred, args)
		if !ok {
			reply.uint32(acceptGarbageArgs)
			return reply.buf
		}
		reply.uint32(acceptSuccess)
		reply.buf = append(reply.buf, result...)
	default:
		reply.uint32(acceptProcUnavail)
	}
	return reply.buf
}

func parseCredentials(flavor uint32, body []byte) (credentials, error) {
	switch flavor {
	case authNone:
		return credentials{uid: nobody, gid: nobody}, nil
	case authSys:
		d := &decoder{buf: body}
		d.uint32() // stamp
		d.str()    // machine name
		cred := credentials{uid: d.uint32(), gid: d.uint32()}
		n := d.uint32()
		if n > 16 {
			return credentials{}, errors.New("AUTH_SYS with more than 16 groups")
		}
		for i := uint32(0); i < n; i++ {
			cred.gids = append(cred.gids, d.uint32())
		}
		if d.err != nil {
			return credentials{}, d.err
		}
		return cred, nil
	}
	return credentials{}, fmt.Errorf("authentication flavor %d", flavor)
}

const nobody = 65534
