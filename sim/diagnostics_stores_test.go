package sim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreReportNamesWhatHoldsTheDatabase proves the report answers the
// question it exists for: which table holds the space. A store with one large
// blob per row must dominate a store with many small rows, and the report must
// name both, their byte sizes and — when asked — their row counts.
func TestStoreReportNamesWhatHoldsTheDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB(db) })

	blobs := MakeStore[[]byte](db, "objects_with_bodies")
	small := MakeStore[string](db, "names_only")
	body := make([]byte, 256*1024)
	for i := range body {
		body[i] = byte(i)
	}
	for i := 0; i < 8; i++ {
		blobs.Put(string(rune('a'+i)), body)
	}
	for i := 0; i < 200; i++ {
		small.Put(string(rune('a'+i%26))+string(rune('a'+i/26)), "a name")
	}

	var report strings.Builder
	writeStoreReport(&report, true)
	out := report.String()

	for _, want := range []string{
		filepath.Join(dir, "simulator.db"),
		"objects_with_bodies",
		"names_only",
		"free list",
		"200 rows",
		"8 rows",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}

	// The blob store is two megabytes of bodies; the name store is a few
	// kilobytes. The report is ordered by size, so the blobs must come first —
	// that ordering is the whole point when a database is 2.4 GB and the
	// question is what holds it.
	blobAt, smallAt := strings.Index(out, "objects_with_bodies"), strings.Index(out, "names_only")
	if blobAt > smallAt {
		t.Errorf("names_only is reported above objects_with_bodies, so the report is not ordered by size:\n%s", out)
	}

	// Without ?rows=1 the report costs no table scan and says no row counts.
	var quick strings.Builder
	writeStoreReport(&quick, false)
	if strings.Contains(quick.String(), "rows") {
		t.Errorf("the default report counted rows:\n%s", quick.String())
	}
}

// TestStoreReportBoundsTheWriteAheadLog proves the journal_size_limit the DSN
// sets is in force: after a checkpoint, the write-ahead log file is truncated
// rather than left at the high-water mark a large write drove it to.
func TestStoreReportBoundsTheWriteAheadLog(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB(db) })

	var limit int64
	if err := db.QueryRow("PRAGMA journal_size_limit").Scan(&limit); err != nil {
		t.Fatalf("read journal_size_limit: %v", err)
	}
	if limit != 64<<20 {
		t.Fatalf("journal_size_limit = %d, want %d — the DSN pragma did not reach this connection", limit, 64<<20)
	}

	store := MakeStore[[]byte](db, "objects_with_bodies")
	body := make([]byte, 512*1024)
	for i := 0; i < 16; i++ {
		store.Put(string(rune('a'+i)), body)
	}
	var busy, frames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	wal, err := os.Stat(filepath.Join(dir, "simulator.db-wal"))
	if err != nil {
		t.Fatalf("stat the write-ahead log: %v", err)
	}
	if wal.Size() > 64<<20 {
		t.Errorf("the write-ahead log is %d bytes after a checkpoint, past the %d limit", wal.Size(), 64<<20)
	}
}
