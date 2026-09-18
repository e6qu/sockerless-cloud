package sim

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckpointResetsAnOversizedWAL proves the half journal_size_limit does
// not cover. The limit only applies when SQLite resets the log, and under
// sustained writes the automatic checkpoint never gets to: a deployed
// simulator's log sat at 64 MiB while it was quiet and reached 1.2 GiB once the
// retention sweeps started deleting. Asking for the reset brings it back.
func TestCheckpointResetsAnOversizedWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB(db) })

	store := MakeStore[[]byte](db, "objects_with_bodies")
	body := make([]byte, 256*1024)
	for i := 0; i < 24; i++ {
		store.Put(string(rune('a'+i)), body)
	}
	walPath := filepath.Join(dir, "simulator.db-wal")
	grown, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat the write-ahead log: %v", err)
	}
	if grown.Size() == 0 {
		t.Fatal("the writes left no write-ahead log to checkpoint")
	}

	// A threshold under the log's size is the state the checkpointer acts on.
	checkpointOversizedWAL(db, walPath, grown.Size()/2)

	reset, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat the write-ahead log after the checkpoint: %v", err)
	}
	if reset.Size() >= grown.Size() {
		t.Errorf("the log is %d bytes after the checkpoint, was %d — it was not reset",
			reset.Size(), grown.Size())
	}

	// Every row survives the reset: a checkpoint moves frames into the
	// database, it does not discard them.
	for i := 0; i < 24; i++ {
		if _, ok := store.Get(string(rune('a' + i))); !ok {
			t.Fatalf("row %d is missing after the checkpoint", i)
		}
	}

	// A log under the threshold is left alone, so the checkpointer costs
	// nothing on a quiet service.
	before, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	checkpointOversizedWAL(db, walPath, 1<<30)
	after, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if after.Size() != before.Size() {
		t.Errorf("a log under the threshold changed from %d to %d bytes", before.Size(), after.Size())
	}
}
