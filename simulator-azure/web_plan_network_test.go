package main

import "testing"

// The marker's index is computed case-insensitively and slices the caller's
// id, so it must index that id, not a folded copy whose invalid bytes each grow
// to a 3-byte U+FFFD.
func TestCanonicalServerFarmIDSurvivesInvalidUTF8(t *testing.T) {
	// Two invalid bytes ahead of the marker: a folded index lands 4 bytes past the
	// id's end.
	id := "/subscriptions/\xff\xfe/providers/microsoft.web/serverfarms/p"

	got := canonicalServerFarmID(id)

	want := "/subscriptions/\xff\xfe/providers/Microsoft.Web/serverfarms/p"
	if got != want {
		t.Errorf("canonicalServerFarmID(%q) = %q, want %q", id, got, want)
	}
}

func TestCanonicalServerFarmIDLeavesUnmarkedIDsAlone(t *testing.T) {
	const id = "/subscriptions/s/resourceGroups/rg"
	if got := canonicalServerFarmID(id); got != id {
		t.Errorf("canonicalServerFarmID(%q) = %q, want it unchanged", id, got)
	}
}
