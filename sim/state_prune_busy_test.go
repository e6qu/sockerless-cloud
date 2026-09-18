package sim

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestPruneSurvivesABusyDatabase reproduces the failure that took a deployed
// simulator down thirteen times in thirteen minutes: the retention sweeper met
// SQLITE_BUSY against a database its own service was writing to, and the panic
// it raised ended the process instead of the sweep. A sweep that cannot get the
// write lock must leave the rows for the next one.
func TestPruneSurvivesABusyDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB(db) })

	store := MakeStore[pruneRow](db, "busy_rows")
	for _, key := range []string{"one", "two", "three"} {
		store.Put(key, pruneRow{Age: 0})
	}

	// A second connection holds the write lock for longer than the store's
	// busy_timeout, which is exactly the contention a live service creates.
	blocker, err := sql.Open("sqlite", filepath.Join(dir, "simulator.db")+
		"?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open the second connection: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("begin on the second connection: %v", err)
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS blocker_rows (key TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("take the write lock: %v", err)
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO blocker_rows VALUES ('held')`); err != nil {
		t.Fatalf("hold the write lock: %v", err)
	}

	done := make(chan int, 1)
	go func() {
		// A panic here fails the test by ending the process, which is the
		// defect: the assertion is that Prune returns at all.
		done <- store.Prune(func(pruneRow) bool { return true })
	}()

	select {
	case pruned := <-done:
		if pruned != 0 {
			t.Errorf("the sweep deleted %d rows while another writer held the lock", pruned)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Prune did not return while the database was busy")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release the write lock: %v", err)
	}

	// The rows are still there, and the next sweep takes them.
	if pruned := store.Prune(func(pruneRow) bool { return true }); pruned != 3 {
		t.Errorf("the next sweep deleted %d rows, want the 3 the busy one left", pruned)
	}
}
