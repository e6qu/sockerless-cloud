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

func TestFrameReaderBoundsSafe(t *testing.T) {
	r := NewFrameReader([]byte{0x00, 0x00, 0x00, 0x05, 0xAA})
	b, err := r.Take(4)
	if err != nil || len(b) != 4 || b[3] != 0x05 {
		t.Fatalf("Take(4) = %x, %v", b, err)
	}
	if r.Remaining() != 1 {
		t.Fatalf("Remaining = %d, want 1", r.Remaining())
	}
	// Past the end → error, not panic.
	if _, err := r.Take(2); err == nil {
		t.Error("expected EOF past end")
	}
	// Over-long / negative Take → error, never a giant slice/alloc.
	short := NewFrameReader([]byte{0x01})
	if _, err := short.Take(1 << 30); err == nil {
		t.Error("over-long Take must error")
	}
	if _, err := short.Take(-1); err == nil {
		t.Error("negative Take must error")
	}
}
