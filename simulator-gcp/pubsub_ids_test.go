package main

import (
	"regexp"
	"testing"
)

// Pub/Sub hands out schema revision IDs, message IDs and ack IDs many times
// within one clock tick; each must still be unique.
func TestPubSubIDsAreUniqueWithinOneClockTick(t *testing.T) {
	revision := regexp.MustCompile(`^[0-9a-f]{8}$`)
	revisions, ids := map[string]bool{}, map[string]bool{}
	for range 1000 {
		rev := psNewRevisionID()
		if !revision.MatchString(rev) {
			t.Fatalf("psNewRevisionID() = %q, want 8 hexadecimal characters", rev)
		}
		if revisions[rev] {
			t.Fatalf("psNewRevisionID() repeated %q", rev)
		}
		revisions[rev] = true

		id := generateUUIDLocal()
		if ids[id] {
			t.Fatalf("generateUUIDLocal() repeated %q", id)
		}
		ids[id] = true
	}
}
