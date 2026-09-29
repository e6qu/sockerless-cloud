package kvstore

import (
	"sort"
	"sync"
	"sync/atomic"
)

// RWLocks holds a read-write lock per key: a table, a container, a partition —
// whatever unit a store isolates writers at. It extends sim.KeyedLocks, which
// serializes one key, to readers that share a key and to operations that span
// several.
//
// Lock takes every key a call names in one step, sorted and deduplicated, so
// two calls naming overlapping sets in different orders cannot deadlock. A
// caller holding keys must not take more in a second call. A key's entry lives
// only while someone holds or awaits it, so the table stays as small as the
// set of keys in use however many partitions a store has.
type RWLocks struct {
	mu       sync.Mutex
	held     map[string]*rwEntry
	acquired atomic.Uint64
}

type rwEntry struct {
	sync.RWMutex
	users int
}

func (l *RWLocks) enter(key string) *rwEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		l.held = map[string]*rwEntry{}
	}
	entry := l.held[key]
	if entry == nil {
		entry = &rwEntry{}
		l.held[key] = entry
	}
	entry.users++
	return entry
}

func (l *RWLocks) leave(key string, entry *rwEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.users--
	if entry.users == 0 {
		delete(l.held, key)
	}
}

// Lock takes keys for writing when write is true and for reading otherwise,
// and returns the release. It ignores empty keys.
func (l *RWLocks) Lock(write bool, keys ...string) func() {
	unique := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, key)
	}
	sort.Strings(unique)
	l.acquired.Add(uint64(len(unique)))
	entries := make([]*rwEntry, 0, len(unique))
	for _, key := range unique {
		entry := l.enter(key)
		if write {
			entry.Lock()
		} else {
			entry.RLock()
		}
		entries = append(entries, entry)
	}
	return func() {
		for i := len(entries) - 1; i >= 0; i-- {
			if write {
				entries[i].Unlock()
			} else {
				entries[i].RUnlock()
			}
			l.leave(unique[i], entries[i])
		}
	}
}

// Acquired counts the key locks taken so far, one per key rather than one per
// call — how a test observes what an operation locked.
func (l *RWLocks) Acquired() uint64 { return l.acquired.Load() }

// Held reports how many keys are held or awaited.
func (l *RWLocks) Held() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held)
}
