package simulator

import (
	"os"
	"path/filepath"
	"testing"
)

// Per-instance state isolation invariant: two simulators pointed at
// different SIM_DATA_DIR values share no state. Verifies the data path
// admin composes per topology instance — the simplest test that proves
// isolation works at the persistence layer.
func TestPersistenceIsolatedAcrossDataDirs(t *testing.T) {
	tmp := t.TempDir()
	dirA := filepath.Join(tmp, "proj-a", "sim-aws")
	dirB := filepath.Join(tmp, "proj-b", "sim-aws")

	dbA, err := OpenDB(dirA)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer dbA.Close()
	dbB, err := OpenDB(dirB)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	defer dbB.Close()

	type bucket struct {
		Name     string `json:"name"`
		Location string `json:"location"`
	}

	storeA, err := NewSQLiteStore[bucket](dbA, "buckets")
	if err != nil {
		t.Fatalf("store A: %v", err)
	}
	storeB, err := NewSQLiteStore[bucket](dbB, "buckets")
	if err != nil {
		t.Fatalf("store B: %v", err)
	}

	storeA.Put("bucket-1", bucket{Name: "in-a", Location: "us-east-1"})

	if got, ok := storeA.Get("bucket-1"); !ok || got.Name != "in-a" {
		t.Errorf("A side: got %+v, ok=%v", got, ok)
	}
	if _, ok := storeB.Get("bucket-1"); ok {
		t.Errorf("B side leaked: bucket-1 should not exist in instance B")
	}

	storeB.Put("bucket-2", bucket{Name: "in-b", Location: "us-west-2"})

	if storeA.Len() != 1 {
		t.Errorf("A len = %d, want 1", storeA.Len())
	}
	if storeB.Len() != 1 {
		t.Errorf("B len = %d, want 1", storeB.Len())
	}
}

// Persistence survives a "restart" (close + re-open the DB at the
// same path). Combined with the above test, this is what makes
// per-instance state usable in the operator workflow: a sim instance
// stops + restarts and picks up where it left off, without leaking
// state to its neighbours.
func TestPersistenceSurvivesReopen(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "proj-a", "sim-aws")

	type bucket struct {
		Name string `json:"name"`
	}

	{
		db, err := OpenDB(dir)
		if err != nil {
			t.Fatalf("first open: %v", err)
		}
		store, err := NewSQLiteStore[bucket](db, "buckets")
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		store.Put("b1", bucket{Name: "preserved"})
		_ = db.Close()
	}

	// Verify the file actually landed where admin would expect it.
	if _, err := os.Stat(filepath.Join(dir, "simulator.db")); err != nil {
		t.Fatalf("simulator.db missing at %s: %v", dir, err)
	}

	{
		db, err := OpenDB(dir)
		if err != nil {
			t.Fatalf("re-open: %v", err)
		}
		defer db.Close()
		store, err := NewSQLiteStore[bucket](db, "buckets")
		if err != nil {
			t.Fatalf("store after reopen: %v", err)
		}
		got, ok := store.Get("b1")
		if !ok {
			t.Fatalf("b1 lost across reopen")
		}
		if got.Name != "preserved" {
			t.Errorf("b1 = %+v, want Name=preserved", got)
		}
	}
}

// NewServer with cfg.Persist=true must surface OpenDB failures via
// its returned error. Pointing DataDir at a path that can't be created
// (e.g. under a read-only file) is the simplest reproduction; the
// fail-loud-on-persist-open contract refuses to silently fall back to
// in-memory state.
func TestNewServerPersistFailLoud(t *testing.T) {
	tmp := t.TempDir()
	// Create a regular file then try to use it as a directory — mkdir
	// will fail with ENOTDIR.
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg := Config{
		ListenAddr: ":0",
		Provider:   "gcp",
		Persist:    true,
		DataDir:    filepath.Join(blocker, "subdir"),
		LogLevel:   "warn",
	}
	srv, err := NewServer(cfg)
	if err == nil {
		t.Fatalf("expected error when DataDir cannot be created, got nil")
	}
	if srv != nil {
		t.Errorf("server should be nil on persistence failure, got %+v", srv)
	}
}

// The happy path: persistence enabled, DataDir is writable, server
// returns ok and the DB is wired.
func TestNewServerPersistHappy(t *testing.T) {
	tmp := t.TempDir()
	cfg := Config{
		ListenAddr: ":0",
		Provider:   "gcp",
		Persist:    true,
		DataDir:    filepath.Join(tmp, "state"),
		LogLevel:   "warn",
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if srv == nil {
		t.Fatalf("server is nil")
	}
	if srv.DB() == nil {
		t.Errorf("DB() should be non-nil when persistence is enabled")
	}
}

// Persistence disabled: server constructs without touching disk.
func TestNewServerNoPersist(t *testing.T) {
	cfg := Config{
		ListenAddr: ":0",
		Provider:   "gcp",
		Persist:    false,
		LogLevel:   "warn",
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("no-persist path failed: %v", err)
	}
	if srv.DB() != nil {
		t.Errorf("DB() should be nil when persistence is disabled")
	}
}
