package main

import (
	"database/sql"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

// loggingMigrateEntries moves the logs an earlier build stored whole, every
// entry of a log in one row and in no bucket, into the log store. It routes
// each entry through its scope's sinks as a write at now would, so an entry no
// bucket would take, or one past the retention of every bucket that would,
// does not survive the move.
func loggingMigrateEntries(db *sql.DB, now time.Time) error {
	return sim.Migrate(db, "logging_entries/logstore", func() error {
		legacy, logs, err := sim.LegacyRows[[]LogEntry](db, "logging_entries")
		if err != nil {
			return err
		}
		for _, log := range logs {
			scope := loggingScopeOf(log.ID)
			loggingEnsureDefaults(scope)
			cutoffs := loggingBucketCutoffs(now)
			routes, err := loggingRoutes(scope)
			if err != nil {
				return err
			}
			var entries []logstore.Entry[loggingStoredEntry]
			for _, entry := range log.Item {
				at, err := loggingEntryMillis(entry)
				if err != nil {
					return err
				}
				buckets, err := loggingRouteEntry(entry, at, routes, cutoffs, now)
				if err != nil {
					return err
				}
				if len(buckets) == 0 {
					continue
				}
				entries = append(entries, logstore.Entry[loggingStoredEntry]{
					Time: at, Ingested: at, Value: loggingStoredEntry{Entry: entry, Buckets: buckets}})
			}
			if err := logEntries.Import(log.ID, entries); err != nil {
				return err
			}
			legacy.Delete(log.ID)
		}
		return nil
	})
}
