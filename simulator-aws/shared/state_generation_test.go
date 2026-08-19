package simulator

import (
	"path/filepath"
	"testing"
)

// Store.Generation is the invalidation signal a caller relies on to keep a
// derived index instead of rebuilding it per request, so what matters is that
// it moves for every write that changed the store and stays put for
// everything else. A generation that failed to move after a write would let a
// caller serve a stale answer indefinitely, which is worse than the cost the
// counter exists to avoid.

type generationBucket struct {
	Name string `json:"name"`
}

func runStoreGenerationContract(t *testing.T, store Store[generationBucket]) {
	t.Helper()

	start := store.Generation()

	store.Put("bucket-1", generationBucket{Name: "one"})
	afterPut := store.Generation()
	if afterPut == start {
		t.Fatalf("Put did not move the generation (still %d)", afterPut)
	}

	if _, ok := store.Get("bucket-1"); !ok {
		t.Fatal("Get missed the item just written")
	}
	store.List()
	store.Filter(func(generationBucket) bool { return true })
	store.Len()
	if store.Generation() != afterPut {
		t.Fatalf("reads moved the generation from %d to %d", afterPut, store.Generation())
	}

	if !store.Update("bucket-1", func(b *generationBucket) { b.Name = "two" }) {
		t.Fatal("Update missed an item that exists")
	}
	afterUpdate := store.Generation()
	if afterUpdate == afterPut {
		t.Fatalf("Update did not move the generation (still %d)", afterUpdate)
	}

	// An Update that matches nothing wrote nothing.
	if store.Update("absent", func(*generationBucket) {}) {
		t.Fatal("Update reported success for an absent item")
	}
	if store.Generation() != afterUpdate {
		t.Fatalf("a no-op Update moved the generation to %d", store.Generation())
	}

	store.Upsert("bucket-2", func(b *generationBucket) { b.Name = "three" })
	afterUpsert := store.Generation()
	if afterUpsert == afterUpdate {
		t.Fatalf("Upsert did not move the generation (still %d)", afterUpsert)
	}

	if !store.Delete("bucket-2") {
		t.Fatal("Delete missed an item that exists")
	}
	afterDelete := store.Generation()
	if afterDelete == afterUpsert {
		t.Fatalf("Delete did not move the generation (still %d)", afterDelete)
	}

	// A Delete that matches nothing removed nothing.
	if store.Delete("bucket-2") {
		t.Fatal("Delete reported success for an absent item")
	}
	if store.Generation() != afterDelete {
		t.Fatalf("a no-op Delete moved the generation to %d", store.Generation())
	}
}

func TestMemoryStoreGenerationTracksWrites(t *testing.T) {
	runStoreGenerationContract(t, NewStateStore[generationBucket]())
}

func TestSQLiteStoreGenerationTracksWrites(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "sim-aws"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	store, err := NewSQLiteStore[generationBucket](db, "buckets")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	runStoreGenerationContract(t, store)
}
