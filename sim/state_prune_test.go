package sim

import (
	"fmt"
	"testing"
)

type pruneRow struct {
	Age int
}

func pruneStores(t *testing.T) map[string]Store[pruneRow] {
	t.Helper()
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqlite, err := NewSQLiteStore[pruneRow](db, "prune_sqlite")
	if err != nil {
		t.Fatal(err)
	}
	cached, err := NewCachedSQLiteStore[pruneRow](db, "prune_cached")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Store[pruneRow]{
		"sqlite": sqlite,
		"cached": cached,
		"memory": NewStateStore[pruneRow](),
	}
}

// More rows than one batch, and a row under the empty key, which a scan that
// starts at `key > ”` would never reach.
func TestPruneDeletesExactlyTheExpiredRows(t *testing.T) {
	const rows = 2*pruneBatch + 234
	for name, store := range pruneStores(t) {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < rows; i++ {
				store.Put(fmt.Sprintf("row-%05d", i), pruneRow{Age: i})
			}
			store.Put("", pruneRow{Age: 0})
			expired := func(r pruneRow) bool { return r.Age%2 == 0 }

			before := store.Generation()
			if got, want := store.Prune(expired), rows/2+1; got != want {
				t.Fatalf("Prune deleted %d rows, want %d", got, want)
			}
			if store.Generation() == before {
				t.Error("Prune deleted rows without advancing the generation")
			}
			if got, want := store.Len(), rows/2; got != want {
				t.Fatalf("%d rows remain, want %d", got, want)
			}
			if _, ok := store.Get(""); ok {
				t.Error("the row under the empty key survived")
			}
			for _, r := range store.List() {
				if expired(r) {
					t.Fatalf("expired row %+v survived", r)
				}
			}
			if got := store.Prune(expired); got != 0 {
				t.Errorf("a second Prune deleted %d rows, want 0", got)
			}
		})
	}
}

// A row the scan judged expired but a writer renewed before the delete must
// survive: the delete re-reads each candidate under the write lock.
func TestPruneKeepsARowRewrittenAfterTheScan(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore[pruneRow](db, "prune_renewed")
	if err != nil {
		t.Fatal(err)
	}
	store.Put("renewed", pruneRow{Age: 0})
	store.Put("stale", pruneRow{Age: 0})
	expired := func(r pruneRow) bool { return r.Age == 0 }

	_, doomed, busy := store.pruneScan("", false, expired)
	if busy {
		t.Fatal("the scan reported a busy database on an idle store")
	}
	if len(doomed) != 2 {
		t.Fatalf("scan found %v, want both rows", doomed)
	}
	store.Put("renewed", pruneRow{Age: 1})

	deleted, busy := store.deleteExpired(doomed, expired)
	if busy {
		t.Fatal("the delete reported a busy database on an idle store")
	}
	if len(deleted) != 1 || deleted[0] != "stale" {
		t.Fatalf("deleted %v, want only the stale row", deleted)
	}
	if _, ok := store.Get("renewed"); !ok {
		t.Error("the renewed row was deleted")
	}
}
