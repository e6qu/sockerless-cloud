package simulator

import (
	"encoding/binary"
	"io"
	"strings"
)

// This file centralizes the bounds-safe primitives for parsing untrusted input.
// Fuzzing has repeatedly found the same panic classes in hand-rolled parsers:
//   1. case-fold-then-slice — strings.ToLower/ToUpper changes byte length on
//      non-ASCII / invalid-UTF-8 input, so an index computed in the folded copy
//      is out of range in the original;
//   2. unchecked slice/index arithmetic and integer overflow in length math;
//   3. unbounded allocation / recursion from attacker-controlled sizes.
// Use ASCIIFold/CaseInsensitiveIndex for case-insensitive matching that feeds an
// index, and FrameReader for binary wire decoding, instead of raw
// strings.ToLower + binary.BigEndian + slicing.

// ASCIIFold lowercases ONLY ASCII A–Z and is byte-length preserving. Unlike
// strings.ToLower (Unicode-aware; invalid UTF-8 → 3-byte U+FFFD) it never
// changes the byte length, so an index computed against ASCIIFold(s) is valid
// in s. Keywords/operators in the sims are ASCII, so case-insensitive matching
// is unchanged.
func ASCIIFold(s string) string {
	var changed bool
	b := []byte(s)
	for i := range b {
		if c := b[i]; c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
			changed = true
		}
	}
	if !changed {
		return s
	}
	return string(b)
}

// ASCIIFoldUpper uppercases ONLY ASCII a–z and is byte-length preserving.
func ASCIIFoldUpper(s string) string {
	var changed bool
	b := []byte(s)
	for i := range b {
		if c := b[i]; c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
			changed = true
		}
	}
	if !changed {
		return s
	}
	return string(b)
}

// CaseInsensitiveIndex returns the byte index of the first ASCII-case-insensitive
// occurrence of sub in s, or -1. Because ASCIIFold preserves byte length, the
// returned index is valid for slicing the ORIGINAL s.
func CaseInsensitiveIndex(s, sub string) int {
	return strings.Index(ASCIIFold(s), ASCIIFold(sub))
}

// FrameReader is a bounds-checked cursor over an attacker-controlled byte
// buffer. Every read returns an error rather than panicking on a short buffer,
// and Take rejects a negative or over-long length so a wire-declared size can
// never slice past the buffer or drive a giant allocation. Use it for
// hand-rolled binary wire decoders instead of raw binary.BigEndian + b[i:j].
type FrameReader struct {
	data []byte
	off  int
}

// NewFrameReader returns a reader positioned at the start of data.
func NewFrameReader(data []byte) *FrameReader { return &FrameReader{data: data} }

// Remaining reports the number of unread bytes.
func (r *FrameReader) Remaining() int { return len(r.data) - r.off }

// Offset reports the current read position.
func (r *FrameReader) Offset() int { return r.off }

// Byte reads one byte.
func (r *FrameReader) Byte() (byte, error) {
	if r.off >= len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	b := r.data[r.off]
	r.off++
	return b, nil
}

// Take returns the next n bytes (a sub-slice of the backing buffer — do not
// mutate). A negative or over-long n yields io.ErrUnexpectedEOF.
func (r *FrameReader) Take(n int) ([]byte, error) {
	if n < 0 || n > r.Remaining() {
		return nil, io.ErrUnexpectedEOF
	}
	b := r.data[r.off : r.off+n]
	r.off += n
	return b, nil
}

// Uint8 reads a 1-byte unsigned int.
func (r *FrameReader) Uint8() (uint8, error) { return r.Byte() }

// Uint16 reads a 2-byte big-endian unsigned int.
func (r *FrameReader) Uint16() (uint16, error) {
	b, err := r.Take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

// Uint32 reads a 4-byte big-endian unsigned int.
func (r *FrameReader) Uint32() (uint32, error) {
	b, err := r.Take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

// Uint64 reads an 8-byte big-endian unsigned int.
func (r *FrameReader) Uint64() (uint64, error) {
	b, err := r.Take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}
