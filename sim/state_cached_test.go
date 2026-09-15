package sim

import (
	"testing"
)

func cachedTestStore(t *testing.T) (*CachedSQLiteStore[map[string]any], func() *CachedSQLiteStore[map[string]any]) {
	t.Helper()
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	open := func() *CachedSQLiteStore[map[string]any] {
		store, err := NewCachedSQLiteStore[map[string]any](db, "cached_rows")
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		return store
	}
	return open(), open
}

// Reads never reach SQLite: with the database closed underneath it, the store
// still answers every read from memory. A store that queried per read would
// fail here instead of merely being slower.
func TestCachedSQLiteStoreServesReadsWithoutSQLite(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store, err := NewCachedSQLiteStore[map[string]any](db, "cached_rows")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	store.Put("a", map[string]any{"v": "1"})
	store.Put("b", map[string]any{"v": "2"})
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, ok := store.Get("a"); !ok || got["v"] != "1" {
		t.Fatalf("Get after close = %v, %v", got, ok)
	}
	if n := store.Len(); n != 2 {
		t.Fatalf("Len after close = %d", n)
	}
	if rows := store.List(); len(rows) != 2 {
		t.Fatalf("List after close = %d rows", len(rows))
	}
	if keys := store.keys(); len(keys) != 2 {
		t.Fatalf("keys after close = %v", keys)
	}
}

func TestCachedSQLiteStoreWritesThroughAndReloads(t *testing.T) {
	store, reopen := cachedTestStore(t)
	store.Put("keep", map[string]any{"v": "1"})
	store.Put("gone", map[string]any{"v": "x"})
	if !store.Update("keep", func(v *map[string]any) { (*v)["v"] = "2" }) {
		t.Fatal("Update of a present row reported absent")
	}
	if store.Update("absent", func(*map[string]any) {}) {
		t.Fatal("Update of an absent row reported present")
	}
	store.Upsert("new", func(v *map[string]any) { *v = map[string]any{"v": "3"} })
	if !store.Delete("gone") {
		t.Fatal("Delete of a present row reported absent")
	}

	reloaded := reopen()
	want := map[string]string{"keep": "2", "new": "3"}
	if reloaded.Len() != len(want) {
		t.Fatalf("reloaded %d rows, want %d", reloaded.Len(), len(want))
	}
	for key, value := range want {
		got, ok := reloaded.Get(key)
		if !ok || got["v"] != value {
			t.Fatalf("reloaded %s = %v, %v; want %s", key, got, ok, value)
		}
	}
	if _, ok := reloaded.Get("gone"); ok {
		t.Fatal("a deleted row came back after reload")
	}
}

func TestCachedSQLiteStoreReadsAreCopies(t *testing.T) {
	store, _ := cachedTestStore(t)
	store.Put("a", map[string]any{"v": "1"})
	got, _ := store.Get("a")
	got["v"] = "changed by a caller"
	for _, row := range store.List() {
		row["v"] = "changed by a caller"
	}
	store.Filter(func(row map[string]any) bool { row["v"] = "changed by a caller"; return true })
	if again, _ := store.Get("a"); again["v"] != "1" {
		t.Fatalf("a caller's change reached the store: %v", again)
	}
}

func TestCachedSQLiteStoreGenerationFollowsWrites(t *testing.T) {
	store, _ := cachedTestStore(t)
	before := store.Generation()
	store.Put("a", map[string]any{"v": "1"})
	afterPut := store.Generation()
	if afterPut == before {
		t.Fatal("Put did not advance the generation")
	}
	store.Delete("absent")
	if store.Generation() != afterPut {
		t.Fatal("deleting an absent row advanced the generation")
	}
}

func TestMakeCachedStoreJoinsTheCrossCuttingPasses(t *testing.T) {
	ResetTrackedStores()
	t.Cleanup(ResetTrackedStores)
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := MakeCachedStore[map[string]any](db, "cached_tracked")
	store.Put("old/key", map[string]any{"ref": "old/ref"})

	stores := TrackedStores()
	if len(stores) != 1 || stores[0].Table != "cached_tracked" {
		t.Fatalf("tracked stores = %+v", stores)
	}
	rename := func(s string) string {
		if s == "old/key" {
			return "new/key"
		}
		if s == "old/ref" {
			return "new/ref"
		}
		return s
	}
	stores[0].Remap(rename, rename)

	if _, ok := store.Get("old/key"); ok {
		t.Fatal("the pass left the old key in place")
	}
	if got, ok := store.Get("new/key"); !ok || got["ref"] != "new/ref" {
		t.Fatalf("remapped row = %v, %v", got, ok)
	}
	reloaded, err := NewCachedSQLiteStore[map[string]any](db, "cached_tracked")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, ok := reloaded.Get("new/key"); !ok || got["ref"] != "new/ref" {
		t.Fatalf("the pass did not reach SQLite: %v, %v", got, ok)
	}
}
