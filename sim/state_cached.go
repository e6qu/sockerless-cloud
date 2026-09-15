package sim

import (
	"database/sql"
	"log"
	"sync"
)

// CachedSQLiteStore keeps every row of a SQLite-backed store in memory and
// writes through to SQLite.
//
// SQLiteStore answers each Get with a SQL query and a JSON decode. For a store
// read in bulk that is the whole cost of the read path. The AWS simulator's
// DynamoDB serves a query on a secondary index by reading every item of the
// table: 2,500 items of about 1.8 KB took 116 ms to read from SQLite on a
// developer's SSD and 3.2 ms from memory, and on the deployed simulator's
// virtual disk the query held its table lock long enough for PutItem and
// UpdateItem to wait 10–21 s behind it.
//
// SQLite stays the durable copy: every write reaches it before memory does,
// and a new process loads the rows back. Reads return a copy, as MemoryStore's
// do, so a caller that changes what it read cannot change what is stored.
type CachedSQLiteStore[T any] struct {
	disk  *SQLiteStore[T]
	mu    sync.RWMutex
	items map[string]T
}

// NewCachedSQLiteStore opens the SQLite table and loads every row into memory.
func NewCachedSQLiteStore[T any](db *sql.DB, table string) (*CachedSQLiteStore[T], error) {
	disk, err := NewSQLiteStore[T](db, table)
	if err != nil {
		return nil, err
	}
	store := &CachedSQLiteStore[T]{disk: disk, items: make(map[string]T)}
	for _, key := range disk.keys() {
		if v, ok := disk.Get(key); ok {
			store.items[key] = v
		}
	}
	return store, nil
}

// MakeCachedStore is MakeStore for a store whose reads must not each cost a
// SQLite query (see CachedSQLiteStore). With a database it keeps every row in
// memory and writes through; without one it is the MemoryStore MakeStore
// returns. Like MakeStore, a table SQLite rejects is a startup error.
func MakeCachedStore[T any](db *sql.DB, table string) Store[T] {
	var store Store[T]
	if db != nil {
		cached, err := NewCachedSQLiteStore[T](db, table)
		if err != nil {
			log.Fatalf("MakeCachedStore[%s]: %v", table, err)
		}
		store = cached
	} else {
		store = NewStateStore[T]()
	}
	trackStore(table, store)
	return store
}

func (s *CachedSQLiteStore[T]) Get(id string) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.items[id]
	if !ok {
		var zero T
		return zero, false
	}
	return cloneStoreValue(v), true
}

func (s *CachedSQLiteStore[T]) Put(id string, item T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disk.Put(id, item)
	s.items[id] = cloneStoreValue(item)
}

func (s *CachedSQLiteStore[T]) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := s.disk.Delete(id)
	delete(s.items, id)
	return removed
}

func (s *CachedSQLiteStore[T]) List() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]T, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, cloneStoreValue(v))
	}
	return out
}

func (s *CachedSQLiteStore[T]) Filter(fn func(T) bool) []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]T, 0)
	for _, v := range s.items {
		c := cloneStoreValue(v)
		if fn(c) {
			out = append(out, c)
		}
	}
	return out
}

func (s *CachedSQLiteStore[T]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

func (s *CachedSQLiteStore[T]) Update(id string, fn func(*T)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[id]
	if !ok {
		return false
	}
	v = cloneStoreValue(v)
	fn(&v)
	s.disk.Put(id, v)
	s.items[id] = cloneStoreValue(v)
	return true
}

func (s *CachedSQLiteStore[T]) Upsert(id string, fn func(*T)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v T
	if current, ok := s.items[id]; ok {
		v = cloneStoreValue(current)
	}
	fn(&v)
	s.disk.Put(id, v)
	s.items[id] = cloneStoreValue(v)
}

// Generation reports the write counter described on Store; every write goes
// through the SQLite store, which advances it.
func (s *CachedSQLiteStore[T]) Generation() uint64 {
	return s.disk.Generation()
}

// keys returns every key the store holds, for the cross-cutting passes in
// store_scan.go.
func (s *CachedSQLiteStore[T]) keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.items))
	for key := range s.items {
		out = append(out, key)
	}
	return out
}
