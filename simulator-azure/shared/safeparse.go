package simulator

import (
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
