package sim

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type indexedRow struct {
	ID    string
	Hosts []string
}

// countingStore counts the full reads a caller makes, which is what an index
// exists to avoid. Timing would only say "fast on this machine today".
type countingStore struct {
	Store[indexedRow]
	lists atomic.Int64
}

func (s *countingStore) List() []indexedRow {
	s.lists.Add(1)
	return s.Store.List()
}

func rowHosts(r indexedRow) []string { return r.Hosts }

func newCountingStore() *countingStore {
	return &countingStore{Store: NewStateStore[indexedRow]()}
}

func TestGenerationIndexReadsTheStoreOncePerChange(t *testing.T) {
	store := newCountingStore()
	store.Put("a", indexedRow{ID: "a", Hosts: []string{"a.example"}})
	var index GenerationIndex[indexedRow]

	before := store.lists.Load()
	for range 50 {
		row, ok := index.Lookup(store, "a.example", rowHosts)
		require.True(t, ok)
		require.Equal(t, "a", row.ID)
		// A key nothing holds is the common case on a wrapper's path — every
		// request for another service — and must not read the store either.
		_, ok = index.Lookup(store, "unrelated.example", rowHosts)
		require.False(t, ok)
	}
	require.Equal(t, before+1, store.lists.Load(),
		"a hundred lookups against an unchanged store must read it once")
}

func TestGenerationIndexFollowsTheStore(t *testing.T) {
	store := newCountingStore()
	var index GenerationIndex[indexedRow]

	store.Put("a", indexedRow{ID: "a", Hosts: []string{"a.example"}})
	row, ok := index.Lookup(store, "a.example", rowHosts)
	require.True(t, ok)
	require.Equal(t, "a", row.ID)

	// A row added, changed and deleted each leaves the index on the next
	// lookup, with no invalidation call anywhere in the resource's lifecycle.
	store.Put("b", indexedRow{ID: "b", Hosts: []string{"b.example"}})
	_, ok = index.Lookup(store, "b.example", rowHosts)
	require.True(t, ok)

	store.Put("a", indexedRow{ID: "a", Hosts: []string{"renamed.example"}})
	_, ok = index.Lookup(store, "a.example", rowHosts)
	require.False(t, ok, "the old key still resolved after the row was renamed")
	_, ok = index.Lookup(store, "renamed.example", rowHosts)
	require.True(t, ok)

	require.True(t, store.Delete("b"))
	_, ok = index.Lookup(store, "b.example", rowHosts)
	require.False(t, ok, "a deleted row still answered on its key")

	// An empty key is dropped rather than indexed: a resource still being
	// created declares no hostname, and a request with no Host header must not
	// match it.
	store.Put("c", indexedRow{ID: "c", Hosts: []string{""}})
	_, ok = index.Lookup(store, "", rowHosts)
	require.False(t, ok)
}

// Two rows can legitimately share a key — two virtual networks with the same
// CIDR, two tasks on one address — and the caller needs all of them.
func TestGenerationIndexReturnsEveryRowOnAKey(t *testing.T) {
	store := newCountingStore()
	store.Put("a", indexedRow{ID: "a", Hosts: []string{"shared.example"}})
	store.Put("b", indexedRow{ID: "b", Hosts: []string{"shared.example"}})
	var index GenerationIndex[indexedRow]
	require.Len(t, index.LookupAll(store, "shared.example", rowHosts), 2)
}

// Replacing the store is what tests do between cases, and a per-store counter
// starting at zero made a stale index match the replacement's generation and
// be served for it. Generations are unique across stores, so the index refuses
// the old contents even without a Reset.
func TestGenerationIndexRefusesAnIndexBuiltFromAReplacedStore(t *testing.T) {
	first := newCountingStore()
	first.Put("a", indexedRow{ID: "a", Hosts: []string{"a.example"}})
	var index GenerationIndex[indexedRow]
	_, ok := index.Lookup(first, "a.example", rowHosts)
	require.True(t, ok)

	second := newCountingStore()
	_, ok = index.Lookup(second, "a.example", rowHosts)
	require.False(t, ok,
		"an index built from the previous store answered for its replacement")
}

func TestGenerationIndexRebuildsOnceUnderConcurrentLookups(t *testing.T) {
	store := newCountingStore()
	for _, id := range []string{"a", "b", "c"} {
		store.Put(id, indexedRow{ID: id, Hosts: []string{id + ".example"}})
	}
	var index GenerationIndex[indexedRow]

	before := store.lists.Load()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = index.Lookup(store, "b.example", rowHosts)
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, store.lists.Load()-before, int64(2),
		"a burst of concurrent first lookups must not each rebuild the index")
}
