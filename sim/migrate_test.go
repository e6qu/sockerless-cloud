package sim

import (
	"errors"
	"testing"
)

func TestMigrateRunsOncePerDatabase(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB(db) })

	runs := 0
	step := func() error { runs++; return nil }
	for range 2 {
		if err := Migrate(db, "shape-v2", step); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 1 {
		t.Fatalf("migration ran %d times", runs)
	}

	failed := errors.New("boom")
	if err := Migrate(db, "broken", func() error { return failed }); !errors.Is(err, failed) {
		t.Fatalf("a failed migration returned %v", err)
	}
	if err := Migrate(db, "broken", step); err != nil || runs != 2 {
		t.Fatalf("a failed migration was recorded as done: err %v runs %d", err, runs)
	}
	if err := Migrate(nil, "memory", func() error { return failed }); err != nil {
		t.Fatalf("in-memory state ran a migration: %v", err)
	}
}

func TestLegacyRowsReadsTheOldShape(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB(db) })
	type oldRow struct{ Items []string }
	old, err := NewSQLiteStore[oldRow](db, "rows")
	if err != nil {
		t.Fatal(err)
	}
	old.Put("a", oldRow{Items: []string{"x", "y"}})
	_, rows, err := LegacyRows[oldRow](db, "rows")
	if err != nil || len(rows) != 1 || rows[0].ID != "a" || len(rows[0].Item.Items) != 2 {
		t.Fatalf("legacy rows = %+v %v", rows, err)
	}
}
