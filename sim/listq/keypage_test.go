package listq

import (
	"encoding/base64"
	"slices"
	"testing"
)

// An item deleted before the cursor between two calls skips nothing after it,
// and one created before it repeats nothing.
func TestKeyPageHoldsItsPlaceAcrossChanges(t *testing.T) {
	id := func(s string) string { return s }
	items := []string{"a", "b", "c", "d"}
	first, next, err := KeyPage(items, id, "", 2, 0, 0)
	if err != nil || !slices.Equal(first, []string{"a", "b"}) || next == "" {
		t.Fatalf("first page = %v %q %v", first, next, err)
	}
	changed := []string{"aa", "b", "c", "d"}
	second, next, err := KeyPage(changed, id, next, 2, 0, 0)
	if err != nil || !slices.Equal(second, []string{"c", "d"}) || next != "" {
		t.Fatalf("second page after a change = %v %q %v", second, next, err)
	}
	for _, foreign := range []string{"!", "not-a-token", "Mw", base64.RawURLEncoding.EncodeToString([]byte("b"))} {
		if _, _, err := KeyPage(items, id, foreign, 2, 0, 0); err != ErrBadToken {
			t.Errorf("a token KeyPage never issued (%q) = %v, want ErrBadToken", foreign, err)
		}
	}
	if page, next, err := KeyPage(items, id, "", 0, 0, 0); err != nil || len(page) != 4 || next != "" {
		t.Errorf("an unbounded page = %v %q %v", page, next, err)
	}
}
