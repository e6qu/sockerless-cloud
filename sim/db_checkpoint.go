package sim

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Keeping the write-ahead log from outgrowing the database it belongs to.
//
// journal_size_limit bounds the FILE, but only at the moment SQLite resets the
// log, and it resets it only when a checkpoint finishes copying every frame
// into the database. A checkpoint cannot finish while any reader still holds an
// older snapshot, so under sustained traffic the automatic one copies what it
// can and the file keeps growing. A deployed simulator showed the shape of it
// exactly: the log sat at the 64 MiB limit while the service was quiet, and
// reached 1.2 GiB once the retention sweeps began deleting in earnest -- a
// gigabyte of log beside a 2.2 GiB database, all of it replayed on the next
// start.
//
// So the simulator asks for the reset itself. A TRUNCATE checkpoint waits for
// the readers it needs and returns busy when it cannot have them, which is not
// a failure: the frames stay in the log and the next attempt takes them.

// walCheckpointInterval is how often the checkpointer looks at the log. It is
// far longer than a request and far shorter than a deployment's lifetime: the
// log only has to be bounded, not small.
const walCheckpointInterval = 2 * time.Minute

// walCheckpointThreshold is the size a log has to reach before the checkpointer
// asks for a reset. It matches the journal_size_limit the DSN sets, so the
// checkpointer acts exactly when the automatic path has stopped keeping the
// promise that limit makes.
const walCheckpointThreshold = 64 << 20

// startWALCheckpointer runs the checkpointer for this server's database under
// the server lifecycle. A server without persistence has no log to bound.
func (s *Server) startWALCheckpointer() {
	if s.db == nil {
		return
	}
	walPath := filepath.Join(s.config.DataDir, "simulator.db-wal")
	s.StartBackground(func(ctx context.Context) {
		ticker := time.NewTicker(walCheckpointInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkpointOversizedWAL(s.db, walPath, walCheckpointThreshold)
			}
		}
	})
}

// checkpointOversizedWAL resets the write-ahead log when it has grown past
// threshold, and reports what happened. It never fails the caller: a log that
// cannot be reset now is reset later, and a database that cannot be read here
// is a fault the service's own traffic will surface.
func checkpointOversizedWAL(db *sql.DB, walPath string, threshold int64) {
	info, err := os.Stat(walPath)
	if err != nil || info.Size() <= threshold {
		return
	}
	before := info.Size()

	// TRUNCATE reports the log it leaves behind, which is empty when it
	// succeeds, so the frame counts it returns say nothing about the work it
	// did. The sizes on either side of the call are what an operator can act
	// on, and a reader that held the log back is reported from the busy flag.
	var busy, frames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-wal] checkpoint of a %s log failed: %v\n", humanBytes(before), err)
		return
	}
	after := before
	if info, err := os.Stat(walPath); err == nil {
		after = info.Size()
	}
	if busy != 0 {
		fmt.Fprintf(os.Stderr,
			"[sim-wal] %s log: a reader held it open, %s now — retrying in %s\n",
			humanBytes(before), humanBytes(after), walCheckpointInterval)
		return
	}
	fmt.Fprintf(os.Stderr, "[sim-wal] %s log checkpointed and reset to %s\n",
		humanBytes(before), humanBytes(after))
}
