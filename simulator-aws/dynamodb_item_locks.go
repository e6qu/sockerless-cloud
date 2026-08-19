package main

import (
	"sort"
	"strings"
	"sync"
)

// The item store is guarded per table rather than by one lock for the whole
// service.
//
// One lock made every DynamoDB operation queue behind every other one, whatever
// table it addressed: a workspace create fanning out into a few dozen calls
// served them one at a time. Making it a read-write lock let reads run
// together, which is most of the traffic, but a write to one table still
// excluded reads of every other — tables that have nothing to do with each
// other contending for no reason.
//
// So the lock is striped by table name, which is the granularity DynamoDB
// itself isolates at. Every item key is `<table>/<itemKey>`, so the stripe is
// derivable from the key alone.
//
// Two rules keep this safe, and both are enforced by the only API that hands
// out these locks:
//
//   - An operation spanning several tables takes their locks in one call, and
//     the call sorts them. Acquiring in a fixed order is what makes two
//     concurrent multi-table operations unable to deadlock against each other.
//   - Every operation can name its tables. PartiQL looked like the exception
//     until the statements were parsed before locking rather than inside the
//     lock, which is where the table names were all along.
//
// Nothing may take a second item lock while holding one: the ordering rule
// covers the sets a single call asks for, not a nested acquisition.

// ddbItemLocks holds one read-write lock per table, created on first use.
var ddbItemLocks = struct {
	mu sync.Mutex
	by map[string]*sync.RWMutex
}{by: map[string]*sync.RWMutex{}}

// ddbTableLock returns the stripe for one table, creating it if this is the
// first operation to address that table.
func ddbTableLock(table string) *sync.RWMutex {
	ddbItemLocks.mu.Lock()
	defer ddbItemLocks.mu.Unlock()
	lock, ok := ddbItemLocks.by[table]
	if !ok {
		lock = &sync.RWMutex{}
		ddbItemLocks.by[table] = lock
	}
	return lock
}

// ddbTableOfItemKey returns the table an item key belongs to. Keys are
// `<table>/<itemKey>`; a key with no separator is its own stripe rather than a
// silent fall-through to a shared one.
func ddbTableOfItemKey(itemKey string) string {
	if i := strings.IndexByte(itemKey, '/'); i >= 0 {
		return itemKey[:i]
	}
	return itemKey
}

// ddbLockTables takes the stripes for the named tables — for writing when
// write is true — and returns the function that releases them. Duplicates are
// collapsed and the order is fixed, so two operations naming the same tables in
// different orders cannot deadlock against each other.
func ddbLockTables(write bool, tables ...string) func() {
	unique := make([]string, 0, len(tables))
	seen := make(map[string]bool, len(tables))
	for _, table := range tables {
		if table == "" || seen[table] {
			continue
		}
		seen[table] = true
		unique = append(unique, table)
	}
	sort.Strings(unique)

	locks := make([]*sync.RWMutex, 0, len(unique))
	for _, table := range unique {
		lock := ddbTableLock(table)
		if write {
			lock.Lock()
		} else {
			lock.RLock()
		}
		locks = append(locks, lock)
	}
	return func() {
		// Released in reverse, which is not required for correctness here but
		// keeps the acquire/release order symmetric for anyone reading a stack.
		for i := len(locks) - 1; i >= 0; i-- {
			if write {
				locks[i].Unlock()
			} else {
				locks[i].RUnlock()
			}
		}
	}
}

// ddbLockItemKeys takes the stripes covering a set of item keys.
func ddbLockItemKeys(write bool, itemKeys ...string) func() {
	tables := make([]string, 0, len(itemKeys))
	for _, itemKey := range itemKeys {
		tables = append(tables, ddbTableOfItemKey(itemKey))
	}
	return ddbLockTables(write, tables...)
}

// ddbResetItemLocks drops every stripe. Tests that rebuild the item store call
// it so a lock does not outlive the store it guarded.
func ddbResetItemLocks() {
	ddbItemLocks.mu.Lock()
	defer ddbItemLocks.mu.Unlock()
	ddbItemLocks.by = map[string]*sync.RWMutex{}
}
