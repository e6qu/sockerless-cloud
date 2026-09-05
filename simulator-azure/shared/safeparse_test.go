package simulator

import "testing"

func TestASCIIFoldPreservesByteLength(t *testing.T) {
	// Invalid UTF-8 + non-ASCII must keep the same byte length (the whole point:
	// an index in the folded string must be valid in the original).
	for _, s := range []string{"WHERE", "where", "WhErE", "Ünïcøde", "0\xf3\xf0 AS x", "K"} {
		if got := ASCIIFold(s); len(got) != len(s) {
			t.Errorf("ASCIIFold(%q): len %d != %d", s, len(got), len(s))
		}
	}
	if ASCIIFold("ABC") != "abc" {
		t.Error("ASCIIFold ascii")
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

func TestCaseInsensitiveLastIndexValidInOriginal(t *testing.T) {
	// Two occurrences, invalid UTF-8 before both: the LAST one is returned and
	// its index is slice-valid in the ORIGINAL string.
	s := "\xf3\xf0/resourceGroups/a/x/RESOURCEGROUPS/b"
	i := CaseInsensitiveLastIndex(s, "/resourcegroups/")
	if i < 0 {
		t.Fatalf("expected to find the marker, got %d", i)
	}
	if i > len(s) {
		t.Fatalf("index %d out of range for len %d", i, len(s))
	}
	if got := s[i+len("/resourcegroups/"):]; got != "b" {
		t.Errorf("tail after the last marker = %q, want %q", got, "b")
	}
	if CaseInsensitiveLastIndex("abc", "XYZ") != -1 {
		t.Error("expected -1 for no match")
	}
}
