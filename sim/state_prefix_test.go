package sim

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// prefixRow carries its id quoted: a stored value is JSON, which does not keep
// the invalid UTF-8 some of these ids are made of.
type prefixRow struct {
	ID string
}

func rowOf(id string) prefixRow { return prefixRow{ID: strconv.Quote(id)} }

func prefixStores(t *testing.T) (map[string]Store[prefixRow], *SQLiteStore[prefixRow]) {
	t.Helper()
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqlite, err := NewSQLiteStore[prefixRow](db, "prefix_sqlite")
	if err != nil {
		t.Fatal(err)
	}
	cached, err := NewCachedSQLiteStore[prefixRow](db, "prefix_cached")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Store[prefixRow]{
		"sqlite": sqlite,
		"cached": cached,
		"memory": NewStateStore[prefixRow](),
	}, sqlite
}

// The ids sit on every side of each prefix's bounds: a sibling that shares all
// but the last byte, one that extends the prefix without a separator, ids
// under multibyte characters, and ids of bytes at the top of the range, where
// the bound has to carry.
func TestListPrefixReturnsExactlyThePrefixInIDOrder(t *testing.T) {
	ids := []string{
		"", "a", "a/", "a/b", "a/b/1", "a/b/2", "a/b0", "a/c", "a0", "b",
		"é/x", "é/y", "ê", "a/\x7f", "a/\x7f\xff", "a/\x80", "\xff", "\xff\xff", "\xff\xff/z",
	}
	cases := []struct {
		prefix string
		want   []string
	}{
		{"a/b/", []string{"a/b/1", "a/b/2"}},
		{"a/b", []string{"a/b", "a/b/1", "a/b/2", "a/b0"}},
		{"a/", []string{"a/", "a/b", "a/b/1", "a/b/2", "a/b0", "a/c", "a/\x7f", "a/\x7f\xff", "a/\x80"}},
		{"a/\x7f", []string{"a/\x7f", "a/\x7f\xff"}},
		{"é/", []string{"é/x", "é/y"}},
		{"\xff\xff", []string{"\xff\xff", "\xff\xff/z"}},
		{"\xff", []string{"\xff", "\xff\xff", "\xff\xff/z"}},
		{"nothing", []string{}},
	}
	stores, _ := prefixStores(t)
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			// Put in reverse, so an implementation that returns insertion
			// order is caught.
			for i := len(ids) - 1; i >= 0; i-- {
				store.Put(ids[i], rowOf(ids[i]))
			}
			for _, c := range cases {
				got := []string{}
				for _, row := range store.ListPrefix(c.prefix) {
					got = append(got, row.ID)
					if row.Item != rowOf(row.ID) {
						t.Errorf("ListPrefix(%q) paired id %q with %s", c.prefix, row.ID, row.Item.ID)
					}
				}
				if !reflect.DeepEqual(got, c.want) {
					t.Errorf("ListPrefix(%q) = %q, want %q", c.prefix, got, c.want)
				}
			}
			sorted := append([]string(nil), ids...)
			sort.Strings(sorted)
			got := []string{}
			for _, row := range store.ListPrefix("") {
				got = append(got, row.ID)
			}
			if !reflect.DeepEqual(got, sorted) {
				t.Errorf("ListPrefix(\"\") = %q, want %q", got, sorted)
			}
		})
	}
}

// A listing of one prefix must not decode the rows outside it: that is what
// makes it cost the prefix and not the store. A row that cannot be decoded
// proves it, because decoding it panics.
func TestListPrefixDecodesNoRowOutsideThePrefix(t *testing.T) {
	_, sqlite := prefixStores(t)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("wanted/%d", i)
		sqlite.Put(id, rowOf(id))
	}
	for _, id := range []string{"other/corrupt", "wanted0", "wantec"} {
		if _, err := sqlite.db.Exec(fmt.Sprintf(`INSERT INTO %q (key, value) VALUES (?, ?)`, sqlite.table), id, []byte("{not json")); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(sqlite.ListPrefix("wanted/")); got != 5 {
		t.Fatalf("ListPrefix returned %d rows, want 5", got)
	}
}
