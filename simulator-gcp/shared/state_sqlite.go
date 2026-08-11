package simulator

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
)

// SQLiteStore is a persistent implementation of Store backed by SQLite.
// Each instance maps to a single table with (key TEXT, value BLOB) schema.
type SQLiteStore[T any] struct {
	db    *sql.DB
	table string
	mu    sync.Mutex // serialize writes (SQLite is single-writer)
}

// NewSQLiteStore creates a persistent store backed by a SQLite table.
// Creates the table if it doesn't exist.
func NewSQLiteStore[T any](db *sql.DB, table string) (*SQLiteStore[T], error) {
	_, err := db.Exec(fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %q (key TEXT PRIMARY KEY, value BLOB)`, table))
	if err != nil {
		return nil, fmt.Errorf("create table %s: %w", table, err)
	}
	return &SQLiteStore[T]{db: db, table: table}, nil
}

// fatalDBErr panics on an UNEXPECTED SQLite error (a failed write, a query
// error, or a corrupt row). A persistence fault is a real fault for the sim:
// failing loudly (net/http recovers a handler panic into a 500 with a logged
// stack) is far better than silently losing a write or reading a resource back
// as absent — the same stance MakeStore takes with log.Fatalf on a bad open.
// A legitimate not-found is sql.ErrNoRows and is NOT routed here.
func (s *SQLiteStore[T]) fatalDBErr(op, id string, err error) {
	panic(fmt.Sprintf("sim SQLiteStore[%s].%s id=%q: %v", s.table, op, id, err))
}

func (s *SQLiteStore[T]) Get(id string) (T, bool) {
	var data []byte
	err := s.db.QueryRow(
		fmt.Sprintf(`SELECT value FROM %q WHERE key = ?`, s.table), id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		var zero T
		return zero, false
	}
	if err != nil {
		s.fatalDBErr("Get", id, err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		s.fatalDBErr("Get unmarshal (corrupt row)", id, err)
	}
	return v, true
}

func (s *SQLiteStore[T]) Put(id string, item T) {
	data, err := json.Marshal(item)
	if err != nil {
		s.fatalDBErr("Put marshal", id, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(
		fmt.Sprintf(`INSERT OR REPLACE INTO %q (key, value) VALUES (?, ?)`, s.table),
		id, data); err != nil {
		s.fatalDBErr("Put", id, err)
	}
}

func (s *SQLiteStore[T]) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(
		fmt.Sprintf(`DELETE FROM %q WHERE key = ?`, s.table), id)
	if err != nil {
		s.fatalDBErr("Delete", id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		s.fatalDBErr("Delete rows-affected", id, err)
	}
	return n > 0
}

func (s *SQLiteStore[T]) List() []T {
	rows, err := s.db.Query(fmt.Sprintf(`SELECT value FROM %q`, s.table))
	if err != nil {
		s.fatalDBErr("List", "", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]T, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			s.fatalDBErr("List scan", "", err)
		}
		var v T
		if err := json.Unmarshal(data, &v); err != nil {
			s.fatalDBErr("List unmarshal (corrupt row)", "", err)
		}
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		s.fatalDBErr("List rows", "", err)
	}
	return result
}

func (s *SQLiteStore[T]) Filter(fn func(T) bool) []T {
	all := s.List()
	result := make([]T, 0)
	for _, v := range all {
		if fn(v) {
			result = append(result, v)
		}
	}
	return result
}

func (s *SQLiteStore[T]) Len() int {
	var count int
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %q`, s.table)).Scan(&count); err != nil {
		s.fatalDBErr("Len", "", err)
	}
	return count
}

func (s *SQLiteStore[T]) Update(id string, fn func(*T)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	var data []byte
	err := s.db.QueryRow(
		fmt.Sprintf(`SELECT value FROM %q WHERE key = ?`, s.table), id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		s.fatalDBErr("Update read", id, err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		s.fatalDBErr("Update unmarshal (corrupt row)", id, err)
	}
	fn(&v)
	updated, err := json.Marshal(v)
	if err != nil {
		s.fatalDBErr("Update marshal", id, err)
	}
	if _, err := s.db.Exec(
		fmt.Sprintf(`INSERT OR REPLACE INTO %q (key, value) VALUES (?, ?)`, s.table),
		id, updated); err != nil {
		s.fatalDBErr("Update write", id, err)
	}
	return true
}

// MakeStore returns a SQLiteStore if db is non-nil, or a MemoryStore
// otherwise.
//
// When db is non-nil but NewSQLiteStore fails (CREATE TABLE rejected
// by SQLite — typically corruption or fs perms post-OpenDB), the
// process exits via log.Fatalf rather than silently dropping that one
// table back to memory while its neighbours stay durable. Half-
// persistent state would surface as confusing "some data lost on
// restart" reports later.
//
// All 100+ call sites are register*-time, so log.Fatalf here is the
// equivalent of a startup error — operator sees the message and the
// failing table name immediately, no degraded-mode running.
func MakeStore[T any](db *sql.DB, table string) Store[T] {
	if db != nil {
		s, err := NewSQLiteStore[T](db, table)
		if err == nil {
			return s
		}
		log.Fatalf("MakeStore[%s]: %v", table, err)
	}
	return NewStateStore[T]()
}
