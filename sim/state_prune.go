package sim

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// pruneBatch bounds how many rows one Prune step decodes and deletes. A store
// can hold millions of expired rows, and Prune must neither load them all nor
// hold the write lock while it decodes them.
const pruneBatch = 500

// Prune deletes every row expired reports true for and returns how many it
// deleted. It reads the table in key order a batch at a time and re-checks each
// candidate under the write lock, so a row rewritten since the scan survives.
//
// Prune runs on a background goroutine, which is why it treats a busy database
// as a reason to stop rather than as a fault. A panic in a handler is recovered
// by net/http into a 500; a panic here takes the whole simulator down with it,
// and a deployed one did: the sweeper met SQLITE_BUSY against a database its
// own service was writing to, fatalDBErr panicked, systemd restarted the
// process, and the next sweep met it again -- thirteen times in thirteen
// minutes, each restart losing every task's network namespace, so an
// application behind the simulator's load balancer never became reachable.
// Contention is not corruption: the rows are still there, and the next sweep
// deletes them.
func (s *SQLiteStore[T]) Prune(expired func(T) bool) int {
	pruned := 0
	after, started := "", false
	for {
		keys, doomed, busy := s.pruneScan(after, started, expired)
		if busy || len(keys) == 0 {
			return pruned
		}
		after, started = keys[len(keys)-1], true
		if len(doomed) > 0 {
			deleted, busy := s.deleteExpired(doomed, expired)
			pruned += len(deleted)
			if busy {
				return pruned
			}
		}
	}
}

// transientDBError reports whether err is SQLite telling the caller to come
// back later rather than reporting a broken database. The driver carries the
// result code, so this asks it rather than matching on a message.
func transientDBError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	}
	return false
}

// reportBusy says what the sweep gave up on, at the level an operator reads.
// It is not a failure to act on: the rows remain and the next sweep takes them.
func (s *SQLiteStore[T]) reportBusy(op string, err error) {
	fmt.Fprintf(os.Stderr, "[sim-prune] %s.%s: %v — leaving the rest of this sweep to the next one\n",
		s.table, op, err)
}

func (s *SQLiteStore[T]) pruneScan(after string, started bool, expired func(T) bool) (keys, doomed []string, busy bool) {
	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT key, value FROM %q WHERE (? = 0 OR key > ?) ORDER BY key LIMIT ?`, s.table),
		started, after, pruneBatch)
	if transientDBError(err) {
		s.reportBusy("Prune scan", err)
		return nil, nil, true
	}
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
		if transientDBError(err) {
			s.reportBusy("Prune scan rows", err)
			return nil, nil, true
		}
		s.fatalDBErr("Prune scan rows", after, err)
	}
	return keys, doomed, false
}

// deleteExpired deletes, in one transaction, each of keys whose current value
// expired still reports true for, and returns the keys it deleted.
func (s *SQLiteStore[T]) deleteExpired(keys []string, expired func(T) bool) (deletedKeys []string, busy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if transientDBError(err) {
		s.reportBusy("Prune begin", err)
		return nil, true
	}
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
			if transientDBError(err) {
				s.reportBusy("Prune read", err)
				return nil, true
			}
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
			if transientDBError(err) {
				s.reportBusy("Prune delete", err)
				return nil, true
			}
			s.fatalDBErr("Prune delete", key, err)
		}
		deleted = append(deleted, key)
	}
	if err := tx.Commit(); err != nil {
		if transientDBError(err) {
			s.reportBusy("Prune commit", err)
			return nil, true
		}
		s.fatalDBErr("Prune commit", "", err)
	}
	if len(deleted) > 0 {
		s.generation.Store(nextStoreGeneration())
	}
	return deleted, false
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
		deleted, busy := s.disk.deleteExpired(doomed[start:end], expired)
		for _, key := range deleted {
			delete(s.items, key)
			pruned++
		}
		s.mu.Unlock()
		// A busy database ends this sweep; the rows are still expired and the
		// next one takes them.
		if busy {
			return pruned
		}
	}
	return pruned
}
