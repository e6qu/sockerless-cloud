package simulator

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteDurabilityAndOrderlyCheckpoint(t *testing.T) {
	dataDir := t.TempDir()
	db, err := OpenDB(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.Conn(ctx)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	for name, conn := range map[string]interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}{
		"first":  first,
		"second": second,
	} {
		var journalMode string
		var synchronous int
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatalf("%s connection journal mode: %v", name, err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("%s connection synchronous mode: %v", name, err)
		}
		if journalMode != "wal" || synchronous != 2 {
			t.Fatalf(
				"%s connection durability = journal_mode %q, synchronous %d",
				name, journalMode, synchronous,
			)
		}
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("CREATE TABLE durable (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO durable (value) VALUES ('preserved')"); err != nil {
		t.Fatal(err)
	}
	if err := CloseDB(db); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dataDir, "simulator.db-wal")); err == nil && info.Size() != 0 {
		t.Fatalf("orderly close left %d uncheckpointed WAL bytes", info.Size())
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	reopened, err := OpenDB(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB(reopened) })
	var value string
	if err := reopened.QueryRow("SELECT value FROM durable").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "preserved" {
		t.Fatalf("reopened value = %q, want preserved", value)
	}
}
