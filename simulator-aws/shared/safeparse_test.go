package simulator

import "testing"

func TestASCIIFoldPreservesByteLength(t *testing.T) {
	// Invalid UTF-8 + non-ASCII must keep the same byte length (the whole point:
	// an index in the folded string must be valid in the original).
	for _, s := range []string{"WHERE", "where", "WhErE", "Ünïcøde", "0\xf3\xf0 AS x", "K"} {
		if got := ASCIIFold(s); len(got) != len(s) {
			t.Errorf("ASCIIFold(%q): len %d != %d", s, len(got), len(s))
		}
		if got := ASCIIFoldUpper(s); len(got) != len(s) {
			t.Errorf("ASCIIFoldUpper(%q): len %d != %d", s, len(got), len(s))
		}
	}
	if ASCIIFold("ABC") != "abc" {
		t.Error("ASCIIFold ascii")
	}
	if ASCIIFoldUpper("abc") != "ABC" {
		t.Error("ASCIIFoldUpper ascii")
	}
}

func TestCaseInsensitiveIndexValidInOriginal(t *testing.T) {
	s := "0\xf3\xf0 WHERE x" // invalid UTF-8 before the keyword
	i := CaseInsensitiveIndex(s, "where")
	if i < 0 {
		t.Fatalf("expected to find 'where', got %d", i)
	}
	// The index must be slice-valid in the ORIGINAL string (the bug class).
	if i > len(s) {
		t.Fatalf("index %d out of range for len %d", i, len(s))
	}
	_ = s[i:] // must not panic
	if CaseInsensitiveIndex("abc", "XYZ") != -1 {
		t.Error("expected -1 for no match")
	}
}

func TestFrameReaderBoundsSafe(t *testing.T) {
	r := NewFrameReader([]byte{0x00, 0x00, 0x00, 0x05, 0xAA})
	n, err := r.Uint32()
	if err != nil || n != 5 {
		t.Fatalf("Uint32 = %d, %v", n, err)
	}
	if b, err := r.Byte(); err != nil || b != 0xAA {
		t.Fatalf("Byte = %x, %v", b, err)
	}
	// Past the end → error, not panic.
	if _, err := r.Byte(); err == nil {
		t.Error("expected EOF past end")
	}
	// Over-long / negative Take → error, never a giant slice/alloc.
	short := NewFrameReader([]byte{0x01})
	if _, err := short.Uint32(); err == nil {
		t.Error("Uint32 on short buffer must error")
	}
	if _, err := short.Take(1 << 30); err == nil {
		t.Error("over-long Take must error")
	}
	if _, err := short.Take(-1); err == nil {
		t.Error("negative Take must error")
	}
}
