package sim

import (
	"database/sql"
	"errors"
	"fmt"
)

// pruneBatch bounds how many rows one Prune step decodes and deletes. A store
// can hold millions of expired rows, and Prune must neither load them all nor
// hold the write lock while it decodes them.
const pruneBatch = 500

// Prune deletes every row expired reports true for and returns how many it
// deleted. It reads the table in key order a batch at a time and re-checks each
// candidate under the write lock, so a row rewritten since the scan survives.
func (s *SQLiteStore[T]) Prune(expired func(T) bool) int {
	pruned := 0
	after, started := "", false
	for {
		keys, doomed := s.pruneScan(after, started, expired)
		if len(keys) == 0 {
			return pruned
		}
		after, started = keys[len(keys)-1], true
		if len(doomed) > 0 {
			pruned += len(s.deleteExpired(doomed, expired))
		}
	}
}

func (s *SQLiteStore[T]) pruneScan(after string, started bool, expired func(T) bool) (keys, doomed []string) {
	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT key, value FROM %q WHERE (? = 0 OR key > ?) ORDER BY key LIMIT ?`, s.table),
		started, after, pruneBatch)
	if err != nil {
		s.fatalDBErr("Prune scan", after, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			s.fatalDBErr("Prune scan row", after, err)
		}
		var v T
		if err := unmarshalPersistentValue(data, &v); err != nil {
			s.fatalDBErr("Prune unmarshal (corrupt row)", key, err)
		}
		keys = append(keys, key)
		if expired(v) {
			doomed = append(doomed, key)
		}
	}
	if err := rows.Err(); err != nil {
		s.fatalDBErr("Prune scan rows", after, err)
	}
	return keys, doomed
}

// deleteExpired deletes, in one transaction, each of keys whose current value
// expired still reports true for, and returns the keys it deleted.
func (s *SQLiteStore[T]) deleteExpired(keys []string, expired func(T) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		s.fatalDBErr("Prune begin", "", err)
	}
	var deleted []string
	for _, key := range keys {
		var data []byte
		err := tx.QueryRow(fmt.Sprintf(`SELECT value FROM %q WHERE key = ?`, s.table), key).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			s.fatalDBErr("Prune read", key, err)
		}
		var v T
		if err := unmarshalPersistentValue(data, &v); err != nil {
			_ = tx.Rollback()
			s.fatalDBErr("Prune unmarshal (corrupt row)", key, err)
		}
		if !expired(v) {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %q WHERE key = ?`, s.table), key); err != nil {
			_ = tx.Rollback()
			s.fatalDBErr("Prune delete", key, err)
		}
		deleted = append(deleted, key)
	}
	if err := tx.Commit(); err != nil {
		s.fatalDBErr("Prune commit", "", err)
	}
	if len(deleted) > 0 {
		s.generation.Store(nextStoreGeneration())
	}
	return deleted
}

// Prune deletes every item expired reports true for and returns how many it
// deleted.
func (s *MemoryStore[T]) Prune(expired func(T) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	pruned := 0
	for key, v := range s.items {
		if expired(cloneStoreValue(v)) {
			delete(s.items, key)
			pruned++
		}
	}
	if pruned > 0 {
		s.generation = nextStoreGeneration()
	}
	return pruned
}

// Prune deletes every row expired reports true for and returns how many it
// deleted. It picks the candidates from memory, then deletes them from SQLite
// and memory a batch at a time, so readers wait only for one batch.
func (s *CachedSQLiteStore[T]) Prune(expired func(T) bool) int {
	s.mu.RLock()
	var doomed []string
	for key, v := range s.items {
		if expired(cloneStoreValue(v)) {
			doomed = append(doomed, key)
		}
	}
	s.mu.RUnlock()
	pruned := 0
	for start := 0; start < len(doomed); start += pruneBatch {
		end := min(start+pruneBatch, len(doomed))
		s.mu.Lock()
		for _, key := range s.disk.deleteExpired(doomed[start:end], expired) {
			delete(s.items, key)
			pruned++
		}
		s.mu.Unlock()
	}
	return pruned
}
