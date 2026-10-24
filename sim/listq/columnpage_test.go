package listq

import (
	"slices"
	"testing"
)

// A page in a two-column order, the first descending, resumes after the last
// row it returned even when a row sorting before that point appears.
func TestColumnPageResumesAfterItsLastRow(t *testing.T) {
	type event struct {
		day string
		seq int64
	}
	columnsOf := func(items []event) []Column {
		return []Column{
			{Value: func(i int) string { return items[i].day }, Desc: true},
			{Value: func(i int) string { return IntKey(items[i].seq) }},
		}
	}
	items := []event{{"mon", 2}, {"tue", 1}, {"mon", 1}, {"wed", -3}}
	first, next, err := ColumnPage(items, columnsOf(items), "", 2, 0, 0)
	if err != nil || !slices.Equal(first, []event{{"wed", -3}, {"tue", 1}}) || next == "" {
		t.Fatalf("first page = %v %q %v", first, next, err)
	}
	grown := append([]event{{"xmas", 9}}, items...)
	second, last, err := ColumnPage(grown, columnsOf(grown), next, 2, 0, 0)
	if err != nil || !slices.Equal(second, []event{{"mon", 1}, {"mon", 2}}) || last != "" {
		t.Fatalf("second page = %v %q %v", second, last, err)
	}
	if _, _, err := KeyPage([]string{"a"}, func(s string) string { return s }, next, 1, 0, 0); err != ErrBadToken {
		t.Errorf("a token for two columns read by a one-column page = %v, want ErrBadToken", err)
	}
}

func TestIntKeyOrdersAsTheIntegersDo(t *testing.T) {
	ordered := []int64{-1 << 62, -10, -1, 0, 1, 9, 10, 1 << 62}
	for i := 1; i < len(ordered); i++ {
		if IntKey(ordered[i-1]) >= IntKey(ordered[i]) {
			t.Errorf("IntKey(%d) sorts after IntKey(%d)", ordered[i-1], ordered[i])
		}
	}
}
