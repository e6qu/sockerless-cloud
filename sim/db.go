package sim

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
	// Apply the PRAGMAs through the DSN so the driver runs them on every
	// connection the database/sql pool opens; a `db.Exec("PRAGMA …")` configures
	// only the connection it runs on. Without busy_timeout on a read connection,
	// a concurrent write fails at once with SQLITE_BUSY instead of waiting out
	// the lock hand-off. WAL lets readers and the single writer (serialized by
	// SQLiteStore.mu) coexist.
	//
	// synchronous=NORMAL, not FULL: FULL fsyncs the WAL on every commit, and
	// under concurrent load every writer queues behind the previous one's fsync,
	// turning a millisecond Amazon DynamoDB PutItem into a second. NORMAL still
	// fsyncs at every checkpoint and survives an application or process crash;
	// it gives up only a power loss between a commit and its checkpoint, which
	// the simulator's rebuildable state does not need and no cloud client
	// contract promises.
	//
	// journal_size_limit bounds the WAL file, not the WAL a transaction may use:
	// after each checkpoint SQLite truncates the file back to this size instead of
	// leaving it at its high-water mark. 64 MiB leaves a large write room to
	// commit without SQLite retruncating the file constantly. The retention
	// sweeps — deleting sampled AWS WAF requests, temporary credentials and AWS
	// CloudTrail events — are what fill the log.
	dsn := dbPath +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=journal_size_limit(67108864)"
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

	registerDiagnosticsDB(dbPath, db)
	return db, nil
}

// CloseDB checkpoints all committed WAL records into the database before
// closing it, so an orderly service shutdown leaves one self-contained
// database file for the next process rather than relying on that process to
// find and replay a WAL segment.
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

	unregisterDiagnosticsDB(db)
	return errors.Join(checkpointErr, db.Close())
}
