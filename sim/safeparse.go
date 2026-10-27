package sim

import (
	"io"
	"strings"
)

// Bounds-safe primitives for parsing untrusted input. Hand-rolled parsers
// panic in three ways:
//  1. case-fold-then-slice — strings.ToLower/ToUpper changes byte length on
//     non-ASCII or invalid-UTF-8 input, so an index computed in the folded
//     copy is out of range in the original;
//  2. unchecked slice and index arithmetic, and integer overflow in length
//     math;
//  3. unbounded allocation or recursion from attacker-controlled sizes.
//
// Use ASCIIFold and CaseInsensitiveIndex for case-insensitive matching that
// feeds an index, and FrameReader for binary wire decoding, instead of raw
// strings.ToLower, binary.BigEndian and slicing.

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

// CaseInsensitiveLastIndex returns the byte index of the LAST
// ASCII-case-insensitive occurrence of sub in s, or -1. Like
// CaseInsensitiveIndex, the index it returns is valid for slicing the
// ORIGINAL s.
func CaseInsensitiveLastIndex(s, sub string) int {
	return strings.LastIndex(ASCIIFold(s), ASCIIFold(sub))
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

func NewFrameReader(data []byte) *FrameReader { return &FrameReader{data: data} }

// Remaining reports the number of unread bytes.
func (r *FrameReader) Remaining() int { return len(r.data) - r.off }

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
