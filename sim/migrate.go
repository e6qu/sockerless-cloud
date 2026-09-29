package sim

import (
	"database/sql"
	"fmt"
	"time"
)

// Migrate runs fn the first time a persistent database opens under a build
// that declares the migration name, and records that it ran, so a store whose
// row shape a newer build changed is converted once rather than read wrong or
// rescanned on every start. In-memory state starts empty, so without a
// database there is nothing to migrate.
func Migrate(db *sql.DB, name string, fn func() error) error {
	if db == nil {
		return nil
	}
	done, err := NewSQLiteStore[string](db, "sim_migrations")
	if err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	if _, ok := done.Get(name); ok {
		return nil
	}
	if err := fn(); err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	done.Put(name, time.Now().UTC().Format(time.RFC3339))
	return nil
}

// LegacyRows reads every row of a table in the shape an earlier build wrote
// it, for a migration to convert. It does not join the tracked store set: the
// table is either rewritten through its current store or emptied.
func LegacyRows[T any](db *sql.DB, table string) (*SQLiteStore[T], []Keyed[T], error) {
	store, err := NewSQLiteStore[T](db, table)
	if err != nil {
		return nil, nil, err
	}
	return store, store.ListPrefix(""), nil
}
