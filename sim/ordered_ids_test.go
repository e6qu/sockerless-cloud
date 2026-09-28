package sim

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// ListPrefix answers from the ordered ids, so after any sequence of writes it
// must name exactly the ids a pass over every item would, in order, for both
// stores that keep their items in memory.
func TestListPrefixFollowsEveryWrite(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cached, err := NewCachedSQLiteStore[int](db, "ordered_ids_probe")
	if err != nil {
		t.Fatal(err)
	}
	stores := map[string]interface {
		Put(string, int)
		Delete(string) bool
		Upsert(string, func(*int))
		Prune(func(int) bool) int
		ListPrefix(string) []Keyed[int]
	}{"memory": NewStateStore[int](), "cached": cached}

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			random := rand.New(rand.NewSource(1))
			want := map[string]int{}
			id := func() string { return fmt.Sprintf("t%d/%d|%d", random.Intn(3), random.Intn(8), random.Intn(4)) }
			for step := 0; step < 2000; step++ {
				key := id()
				switch random.Intn(10) {
				case 0, 1, 2, 3:
					store.Put(key, step)
					want[key] = step
				case 4, 5:
					store.Upsert(key, func(v *int) { *v = step })
					want[key] = step
				case 6, 7:
					store.Delete(key)
					delete(want, key)
				case 8:
					if step%50 == 0 {
						store.Prune(func(v int) bool { return v%7 == 0 })
						for k, v := range want {
							if v%7 == 0 {
								delete(want, k)
							}
						}
					}
				}
				prefix := []string{"", "t1/", "t2/3", "t0/5|"}[random.Intn(4)]
				var expected []string
				for k := range want {
					if strings.HasPrefix(k, prefix) {
						expected = append(expected, k)
					}
				}
				sort.Strings(expected)
				var got []string
				for _, row := range store.ListPrefix(prefix) {
					got = append(got, row.ID)
					if row.Item != want[row.ID] {
						t.Fatalf("step %d: %s holds %d, want %d", step, row.ID, row.Item, want[row.ID])
					}
				}
				if strings.Join(got, ",") != strings.Join(expected, ",") {
					t.Fatalf("step %d: ListPrefix(%q) = %v, want %v", step, prefix, got, expected)
				}
			}
		})
	}
}
