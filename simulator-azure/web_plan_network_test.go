package main

import "testing"

// TestCanonicalServerFarmIDSurvivesInvalidUTF8 pins the rewrite against the
// slice-bounds class: the marker's index is computed case-insensitively and
// then used to slice the caller's id, so it has to be an index into that id
// and not into a Unicode-folded copy, whose invalid bytes each grow to a
// 3-byte U+FFFD and push every later index past the original's end.
func TestCanonicalServerFarmIDSurvivesInvalidUTF8(t *testing.T) {
	// Two invalid bytes ahead of the marker and a one-character plan name: a
	// folded index lands 4 bytes beyond the id's end.
	id := "/subscriptions/\xff\xfe/providers/microsoft.web/serverfarms/p"

	got := canonicalServerFarmID(id)

	want := "/subscriptions/\xff\xfe/providers/Microsoft.Web/serverfarms/p"
	if got != want {
		t.Errorf("canonicalServerFarmID(%q) = %q, want %q", id, got, want)
	}
}

// TestCanonicalServerFarmIDLeavesUnmarkedIDsAlone keeps the no-match answer
// honest: an id without the segment comes back unchanged.
func TestCanonicalServerFarmIDLeavesUnmarkedIDsAlone(t *testing.T) {
	const id = "/subscriptions/s/resourceGroups/rg"
	if got := canonicalServerFarmID(id); got != id {
		t.Errorf("canonicalServerFarmID(%q) = %q, want it unchanged", id, got)
	}
}
