package fileshare

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// encoder writes XDR (RFC 4506): every item is big-endian and padded to four
// bytes.
type encoder struct {
	buf []byte
}

func (e *encoder) uint32(v uint32) {
	e.buf = binary.BigEndian.AppendUint32(e.buf, v)
}

func (e *encoder) uint64(v uint64) {
	e.buf = binary.BigEndian.AppendUint64(e.buf, v)
}

func (e *encoder) bool(v bool) {
	if v {
		e.uint32(1)
	} else {
		e.uint32(0)
	}
}

func (e *encoder) opaque(b []byte) {
	e.uint32(uint32(len(b)))
	e.fixed(b)
}

// fixed writes bytes of a known length, padded to four.
func (e *encoder) fixed(b []byte) {
	e.buf = append(e.buf, b...)
	for pad := (4 - len(b)%4) % 4; pad > 0; pad-- {
		e.buf = append(e.buf, 0)
	}
}

func (e *encoder) str(s string) { e.opaque([]byte(s)) }

// decoder reads XDR from a buffer; a short read is an error the caller
// answers as a malformed request.
type decoder struct {
	buf []byte
	off int
	err error
}

var errShort = errors.New("short XDR item")

func (d *decoder) uint32() uint32 {
	if d.err != nil {
		return 0
	}
	if d.off+4 > len(d.buf) {
		d.err = errShort
		return 0
	}
	v := binary.BigEndian.Uint32(d.buf[d.off:])
	d.off += 4
	return v
}

func (d *decoder) uint64() uint64 {
	hi := d.uint32()
	lo := d.uint32()
	return uint64(hi)<<32 | uint64(lo)
}

func (d *decoder) bool() bool { return d.uint32() != 0 }

func (d *decoder) fixed(n int) []byte {
	if d.err != nil {
		return nil
	}
	padded := n + (4-n%4)%4
	if n < 0 || d.off+padded > len(d.buf) {
		d.err = errShort
		return nil
	}
	b := d.buf[d.off : d.off+n]
	d.off += padded
	return b
}

func (d *decoder) opaque() []byte {
	n := d.uint32()
	if n > uint32(len(d.buf)) {
		d.err = fmt.Errorf("XDR opaque of %d bytes in a %d-byte message", n, len(d.buf))
		return nil
	}
	return d.fixed(int(n))
}

func (d *decoder) str() string { return string(d.opaque()) }

func (d *decoder) done() bool { return d.err == nil && d.off == len(d.buf) }
