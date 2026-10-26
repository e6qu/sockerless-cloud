package sim

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sync"
	"sync/atomic"
)

const persistenceEnvelopeMagic = "SLP1"

type persistenceEnvelope struct {
	Data   json.RawMessage            `json:"data"`
	Hidden map[string]json.RawMessage `json:"hidden,omitempty"`
}

// marshalPersistentValue keeps the resource's normal JSON representation
// intact and stores exported json:"-" fields in a private sidecar. Simulator
// resource structs also serve as cloud wire shapes, so internal source-of-truth
// fields must remain hidden from API responses without disappearing from
// SQLite. A field can opt out explicitly with persist:"-" when it is disposable
// process coordination such as a channel.
func marshalPersistentValue[T any](item T) ([]byte, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	envelope := persistenceEnvelope{Data: data}
	value := reflect.ValueOf(item)
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			break
		}
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") != "-" || field.Tag.Get("persist") == "-" {
				continue
			}
			encoded, marshalErr := json.Marshal(value.Field(i).Interface())
			if marshalErr != nil {
				return nil, fmt.Errorf("marshal persistent field %s: %w", field.Name, marshalErr)
			}
			if envelope.Hidden == nil {
				envelope.Hidden = make(map[string]json.RawMessage)
			}
			envelope.Hidden[field.Name] = encoded
		}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return append([]byte(persistenceEnvelopeMagic), encoded...), nil
}

func unmarshalPersistentValue[T any](data []byte, out *T) error {
	if len(data) < len(persistenceEnvelopeMagic) ||
		string(data[:len(persistenceEnvelopeMagic)]) != persistenceEnvelopeMagic {
		// A row without a persistence envelope decodes as plain JSON.
		return json.Unmarshal(data, out)
	}
	var envelope persistenceEnvelope
	if err := json.Unmarshal(data[len(persistenceEnvelopeMagic):], &envelope); err != nil {
		return err
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return err
	}
	value := reflect.ValueOf(out).Elem()
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return nil
	}
	for name, encoded := range envelope.Hidden {
		field := value.FieldByName(name)
		if !field.IsValid() || !field.CanAddr() || !field.CanSet() {
			continue
		}
		if err := json.Unmarshal(encoded, field.Addr().Interface()); err != nil {
			return fmt.Errorf("unmarshal persistent field %s: %w", name, err)
		}
	}
	return nil
}

// SQLiteStore persists a Store in one SQLite table of (key TEXT, value BLOB).
type SQLiteStore[T any] struct {
	db    *sql.DB
	table string
	mu    sync.Mutex // serialize writes (SQLite is single-writer)
	// Every write that changed the table advances generation inside mu, and
	// readers read it without mu, so a reader never blocks behind a writer just
	// to ask whether its derived index is still current. See Store.Generation.
	generation atomic.Uint64
}

// NewSQLiteStore opens a store over table, creating the table when absent.
func NewSQLiteStore[T any](db *sql.DB, table string) (*SQLiteStore[T], error) {
	_, err := db.Exec(fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %q (key TEXT PRIMARY KEY, value BLOB)`, table))
	if err != nil {
		return nil, fmt.Errorf("create table %s: %w", table, err)
	}
	store := &SQLiteStore[T]{db: db, table: table}
	store.generation.Store(nextStoreGeneration())
	return store, nil
}

// fatalDBErr panics on an unexpected SQLite error (a failed write, a query
// error, a corrupt row): net/http recovers a handler's panic into a 500 with a
// logged stack, which beats silently losing a write or reading a resource
// back as absent. A not-found is sql.ErrNoRows and never comes here.
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
	if err := unmarshalPersistentValue(data, &v); err != nil {
		s.fatalDBErr("Get unmarshal (corrupt row)", id, err)
	}
	return v, true
}

func (s *SQLiteStore[T]) Put(id string, item T) {
	data, err := marshalPersistentValue(item)
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
	s.generation.Store(nextStoreGeneration())
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
	if n > 0 {
		s.generation.Store(nextStoreGeneration())
	}
	return n > 0
}

// Generation reports the write counter described on Store.
func (s *SQLiteStore[T]) Generation() uint64 {
	return s.generation.Load()
}

// keys returns every key the table holds, for the cross-cutting passes in
// store_scan.go that must address rows they did not compose the key of.
func (s *SQLiteStore[T]) keys() []string {
	rows, err := s.db.Query(fmt.Sprintf(`SELECT key FROM %q`, s.table))
	if err != nil {
		s.fatalDBErr("keys", "", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			s.fatalDBErr("keys scan", "", err)
		}
		result = append(result, key)
	}
	if err := rows.Err(); err != nil {
		s.fatalDBErr("keys rows", "", err)
	}
	return result
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
		if err := unmarshalPersistentValue(data, &v); err != nil {
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

// ListPrefix returns the items whose id begins with prefix, in id order. It is
// a range on the primary key, so SQLite reads and this decodes only those rows.
func (s *SQLiteStore[T]) ListPrefix(prefix string) []Keyed[T] {
	query := fmt.Sprintf(`SELECT key, value FROM %q WHERE key >= ? ORDER BY key`, s.table)
	args := []any{prefix}
	if end, bounded := prefixEnd(prefix); bounded {
		query = fmt.Sprintf(`SELECT key, value FROM %q WHERE key >= ? AND key < ? ORDER BY key`, s.table)
		args = append(args, end)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		s.fatalDBErr("ListPrefix", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]Keyed[T], 0)
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			s.fatalDBErr("ListPrefix scan", prefix, err)
		}
		var v T
		if err := unmarshalPersistentValue(data, &v); err != nil {
			s.fatalDBErr("ListPrefix unmarshal (corrupt row)", id, err)
		}
		result = append(result, Keyed[T]{ID: id, Item: v})
	}
	if err := rows.Err(); err != nil {
		s.fatalDBErr("ListPrefix rows", prefix, err)
	}
	return result
}

// prefixEnd returns the least string greater than every string that begins
// with prefix, which SQLite's default BINARY collation orders byte by byte as
// Go does. A prefix of nothing but 0xff bytes, or of nothing at all, has no
// such bound.
func prefixEnd(prefix string) (string, bool) {
	end := []byte(prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return string(end[:i+1]), true
		}
	}
	return "", false
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
	if err := unmarshalPersistentValue(data, &v); err != nil {
		s.fatalDBErr("Update unmarshal (corrupt row)", id, err)
	}
	fn(&v)
	updated, err := marshalPersistentValue(v)
	if err != nil {
		s.fatalDBErr("Update marshal", id, err)
	}
	if _, err := s.db.Exec(
		fmt.Sprintf(`INSERT OR REPLACE INTO %q (key, value) VALUES (?, ?)`, s.table),
		id, updated); err != nil {
		s.fatalDBErr("Update write", id, err)
	}
	s.generation.Store(nextStoreGeneration())
	return true
}

// Upsert atomically create-or-modifies the row at id under the single write
// lock (absent → the zero value), avoiding the Update-then-Put race.
func (s *SQLiteStore[T]) Upsert(id string, fn func(*T)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var v T
	var data []byte
	err := s.db.QueryRow(
		fmt.Sprintf(`SELECT value FROM %q WHERE key = ?`, s.table), id).Scan(&data)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.fatalDBErr("Upsert read", id, err)
	}
	if err == nil {
		if err := unmarshalPersistentValue(data, &v); err != nil {
			s.fatalDBErr("Upsert unmarshal (corrupt row)", id, err)
		}
	}
	fn(&v)
	updated, err := marshalPersistentValue(v)
	if err != nil {
		s.fatalDBErr("Upsert marshal", id, err)
	}
	if _, err := s.db.Exec(
		fmt.Sprintf(`INSERT OR REPLACE INTO %q (key, value) VALUES (?, ?)`, s.table),
		id, updated); err != nil {
		s.fatalDBErr("Upsert write", id, err)
	}
	s.generation.Store(nextStoreGeneration())
}

// MakeStore returns a SQLiteStore when db is set and a MemoryStore otherwise.
// A table SQLite refuses to create exits the process through log.Fatalf
// rather than dropping that one table back to memory while its neighbours
// stay durable; every caller runs at registration, so this is a startup
// error that names the table.
func MakeStore[T any](db *sql.DB, table string) Store[T] {
	store := newStore[T](db, table)
	// Every store joins the tracked set (store_scan.go) so a cross-cutting
	// pass reaches exactly the stores this build created.
	trackStore(table, store)
	return store
}

func newStore[T any](db *sql.DB, table string) Store[T] {
	if db != nil {
		s, err := NewSQLiteStore[T](db, table)
		if err == nil {
			return s
		}
		log.Fatalf("MakeStore[%s]: %v", table, err)
	}
	return NewStateStore[T]()
}
