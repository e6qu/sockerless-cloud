package simulator

import (
	"database/sql"
	"os"
	"testing"
)

type testItem struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type refItem struct {
	Labels map[string]string `json:"labels"`
	Items  []string          `json:"items"`
}

func makeStores(t *testing.T) map[string]Store[testItem] {
	t.Helper()

	// SQLite store
	dir, err := os.MkdirTemp("", "sim-state-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	sqliteStore, err := NewSQLiteStore[testItem](db, "test_items")
	if err != nil {
		t.Fatal(err)
	}

	return map[string]Store[testItem]{
		"memory": NewStateStore[testItem](),
		"sqlite": sqliteStore,
	}
}

func TestStorePutGet(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("a", testItem{Name: "alpha", Value: 1})
			v, ok := store.Get("a")
			if !ok {
				t.Fatal("expected to find item")
			}
			if v.Name != "alpha" || v.Value != 1 {
				t.Errorf("got %+v", v)
			}
		})
	}
}

func TestStoreGetMissing(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			_, ok := store.Get("nonexistent")
			if ok {
				t.Error("expected not found")
			}
		})
	}
}

func TestStoreDelete(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("del", testItem{Name: "delete-me"})
			if !store.Delete("del") {
				t.Error("expected delete to return true")
			}
			if store.Delete("del") {
				t.Error("expected second delete to return false")
			}
			if _, ok := store.Get("del"); ok {
				t.Error("expected item gone after delete")
			}
		})
	}
}

func TestStoreList(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("x", testItem{Name: "x"})
			store.Put("y", testItem{Name: "y"})
			items := store.List()
			if len(items) != 2 {
				t.Errorf("expected 2 items, got %d", len(items))
			}
		})
	}
}

// TestStoreEmptyListNotNil asserts both store backends return a non-nil empty
// slice from List/Filter on an empty store. The two backends are swappable, so
// a nil-vs-[] divergence would silently change a handler's JSON (null vs []).
func TestStoreEmptyListNotNil(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			if got := store.List(); got == nil {
				t.Error("List() on empty store returned nil, want non-nil empty slice")
			}
			if got := store.Filter(func(testItem) bool { return true }); got == nil {
				t.Error("Filter() on empty store returned nil, want non-nil empty slice")
			}
		})
	}
}

func TestStoreFilter(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("a", testItem{Name: "a", Value: 10})
			store.Put("b", testItem{Name: "b", Value: 20})
			store.Put("c", testItem{Name: "c", Value: 30})
			filtered := store.Filter(func(item testItem) bool { return item.Value > 15 })
			if len(filtered) != 2 {
				t.Errorf("expected 2 filtered items, got %d", len(filtered))
			}
		})
	}
}

func TestStoreLen(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			if store.Len() != 0 {
				t.Errorf("expected 0, got %d", store.Len())
			}
			store.Put("1", testItem{})
			store.Put("2", testItem{})
			if store.Len() != 2 {
				t.Errorf("expected 2, got %d", store.Len())
			}
		})
	}
}

func TestStoreUpdate(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("upd", testItem{Name: "original", Value: 1})
			if !store.Update("upd", func(item *testItem) {
				item.Value = 42
			}) {
				t.Error("expected update to return true")
			}
			v, _ := store.Get("upd")
			if v.Value != 42 {
				t.Errorf("expected 42, got %d", v.Value)
			}
		})
	}
}

func TestStoreUpdateMissing(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			if store.Update("nope", func(item *testItem) {}) {
				t.Error("expected update on missing key to return false")
			}
		})
	}
}

func makeRefStores(t *testing.T) map[string]Store[refItem] {
	t.Helper()

	dir, err := os.MkdirTemp("", "sim-state-ref-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	sqliteStore, err := NewSQLiteStore[refItem](db, "ref_items")
	if err != nil {
		t.Fatal(err)
	}

	return map[string]Store[refItem]{
		"memory": NewStateStore[refItem](),
		"sqlite": sqliteStore,
	}
}

func TestStoreSnapshotsReferenceFields(t *testing.T) {
	for name, store := range makeRefStores(t) {
		t.Run(name, func(t *testing.T) {
			seed := refItem{Labels: map[string]string{"role": "stored"}, Items: []string{"a"}}
			store.Put("x", seed)
			seed.Labels["role"] = "mutated-after-put"
			seed.Items[0] = "mutated-after-put"

			got, ok := store.Get("x")
			if !ok {
				t.Fatal("missing item")
			}
			got.Labels["role"] = "mutated-after-get"
			got.Items[0] = "mutated-after-get"

			again, _ := store.Get("x")
			if again.Labels["role"] != "stored" || again.Items[0] != "a" {
				t.Fatalf("Get leaked mutable references: %+v", again)
			}

			listed := store.List()
			listed[0].Labels["role"] = "mutated-after-list"
			filtered := store.Filter(func(item refItem) bool {
				item.Labels["role"] = "mutated-in-filter"
				return true
			})
			filtered[0].Items[0] = "mutated-after-filter"

			final, _ := store.Get("x")
			if final.Labels["role"] != "stored" || final.Items[0] != "a" {
				t.Fatalf("List/Filter leaked mutable references: %+v", final)
			}
		})
	}
}

func TestStoreUpdateSnapshotsReferenceFields(t *testing.T) {
	for name, store := range makeRefStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("x", refItem{Labels: map[string]string{"role": "stored"}, Items: []string{"a"}})
			if !store.Update("x", func(item *refItem) {
				item.Labels["role"] = "updated"
				item.Items = append(item.Items, "b")
			}) {
				t.Fatal("update failed")
			}
			got, _ := store.Get("x")
			if got.Labels["role"] != "updated" || len(got.Items) != 2 || got.Items[1] != "b" {
				t.Fatalf("update did not persist expected value: %+v", got)
			}
		})
	}
}

func TestStoreOverwrite(t *testing.T) {
	for name, store := range makeStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("k", testItem{Name: "first", Value: 1})
			store.Put("k", testItem{Name: "second", Value: 2})
			v, _ := store.Get("k")
			if v.Name != "second" {
				t.Errorf("expected overwrite, got %q", v.Name)
			}
		})
	}
}

func TestMakeStoreMemory(t *testing.T) {
	s := MakeStore[testItem](nil, "unused")
	s.Put("a", testItem{Name: "a"})
	if s.Len() != 1 {
		t.Error("expected memory store to work")
	}
}

func TestMakeStoreSQLite(t *testing.T) {
	dir, _ := os.MkdirTemp("", "sim-make-*")
	defer os.RemoveAll(dir)
	db, _ := OpenDB(dir)
	defer db.Close()

	s := MakeStore[testItem](db, "make_test")
	s.Put("b", testItem{Name: "b"})
	if s.Len() != 1 {
		t.Error("expected sqlite store to work")
	}
}

func TestSQLitePersistence(t *testing.T) {
	dir, _ := os.MkdirTemp("", "sim-persist-*")
	defer os.RemoveAll(dir)

	// Write with first connection
	db1, _ := OpenDB(dir)
	s1 := MakeStore[testItem](db1, "persist_test")
	s1.Put("key1", testItem{Name: "persisted", Value: 99})
	db1.Close()

	// Read with second connection
	db2, _ := OpenDB(dir)
	defer db2.Close()
	s2 := MakeStore[testItem](db2, "persist_test")
	v, ok := s2.Get("key1")
	if !ok {
		t.Fatal("expected to find persisted item after reopen")
	}
	if v.Name != "persisted" || v.Value != 99 {
		t.Errorf("got %+v", v)
	}
}

func TestSQLiteStoreWithNilDB(t *testing.T) {
	s := MakeStore[testItem]((*sql.DB)(nil), "whatever")
	// Should fall back to memory
	s.Put("x", testItem{Name: "x"})
	if _, ok := s.Get("x"); !ok {
		t.Error("memory fallback should work with nil db")
	}
}

// TestSQLiteStoreFailsLoudOnDBError proves an unexpected DB error (here: the
// underlying DB is closed) panics rather than being swallowed into a silent
// "stored"/"not found"/"empty". A persistence fault must surface, not corrupt
// state — net/http recovers a handler panic into a 500.
func TestSQLiteStoreFailsLoudOnDBError(t *testing.T) {
	dir, err := os.MkdirTemp("", "sim-state-faulttest-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLiteStore[testItem](db, "fault_items")
	if err != nil {
		t.Fatal(err)
	}
	s.Put("a", testItem{Name: "alpha", Value: 1}) // works while the DB is open
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// Every op against the now-closed DB must panic (fail loud), not swallow.
	ops := map[string]func(){
		"Put":    func() { s.Put("b", testItem{Name: "beta"}) },
		"Get":    func() { s.Get("a") },
		"Update": func() { s.Update("a", func(i *testItem) { i.Value = 2 }) },
		"List":   func() { s.List() },
		"Len":    func() { s.Len() },
		"Delete": func() { s.Delete("a") },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s on a closed DB must panic (fail loud), not swallow the error", name)
				}
			}()
			op()
		})
	}
}
