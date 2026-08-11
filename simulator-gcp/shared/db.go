package simulator

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// OpenDB opens a SQLite database at the given path with WAL mode enabled.
// Creates the directory and file if they don't exist.
func OpenDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "simulator.db")
	// Apply the PRAGMAs via the DSN so the driver runs them on EVERY connection
	// it opens for the database/sql pool. A `db.Exec("PRAGMA …")` only configures
	// the single pooled connection it happens to run on; the pool then opens
	// further connections for concurrent reads that would inherit none of these
	// PRAGMAs — most importantly busy_timeout=0, so a read connection holding a
	// WAL read lock makes a concurrent write fail immediately with SQLITE_BUSY
	// ("database is locked") instead of waiting. WAL lets readers and the single
	// writer (serialized by SQLiteStore.mu) coexist; busy_timeout on every
	// connection absorbs the brief lock hand-offs under load.
	dsn := dbPath +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Verify the database is reachable and the PRAGMAs applied (the DSN PRAGMAs
	// run lazily on first connect; Ping forces a connection so a bad path/perms
	// surfaces here rather than on the first store operation).
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return db, nil
}

// CloseDB checkpoints all committed WAL records into the database before
// closing it. FULL synchronous mode protects committed transactions when the
// host loses power; the explicit checkpoint makes an orderly service shutdown
// leave one self-contained database file for the next process.
func CloseDB(db *sql.DB) error {
	if db == nil {
		return nil
	}

	var checkpointErr error
	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(
		&busy, &logFrames, &checkpointedFrames,
	); err != nil {
		checkpointErr = fmt.Errorf("checkpoint sqlite WAL: %w", err)
	} else if busy != 0 {
		checkpointErr = fmt.Errorf(
			"checkpoint sqlite WAL: %d of %d frames remained busy",
			logFrames-checkpointedFrames,
			logFrames,
		)
	}

	return errors.Join(checkpointErr, db.Close())
}
