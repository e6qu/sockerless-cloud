package sim

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDIsRandomVersion4(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := NewUUID()
		if !uuidV4.MatchString(id) {
			t.Fatalf("NewUUID() = %q, not a canonical version 4 variant 1 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewUUID() repeated %q", id)
		}
		seen[id] = true
	}
}

func TestRandomHexIsRandomAndExactLength(t *testing.T) {
	hexChars := regexp.MustCompile(`^[0-9a-f]*$`)
	for _, n := range []int{0, 1, 7, 8, 32} {
		if got := RandomHex(n); len(got) != n || !hexChars.MatchString(got) {
			t.Fatalf("RandomHex(%d) = %q", n, got)
		}
	}
	// Back-to-back calls in the same clock tick must still differ; 1000 draws of
	// 32 bits collide with probability about 1e-4.
	seen := map[string]bool{}
	for range 1000 {
		v := RandomHex(8)
		if seen[v] {
			t.Fatalf("RandomHex(8) repeated %q", v)
		}
		seen[v] = true
	}
}
