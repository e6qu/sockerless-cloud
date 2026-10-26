package main

import (
	"database/sql"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

// An earlier build stored each stream whole in one row; move it into the log
// store, keeping each event's timestamp, ingestion time and arrival order.
func cwMigrateLogEvents(db *sql.DB) error {
	return sim.Migrate(db, "cw_log_events/logstore", func() error {
		legacy, streams, err := sim.LegacyRows[[]CWLogEvent](db, "cw_log_events")
		if err != nil {
			return err
		}
		for _, stream := range streams {
			entries := make([]logstore.Entry[CWLogEvent], 0, len(stream.Item))
			for _, e := range stream.Item {
				entries = append(entries, logstore.Entry[CWLogEvent]{Time: e.Timestamp, Ingested: e.IngestionTime, Value: e})
			}
			if err := cwLogEvents.Import(stream.ID, entries); err != nil {
				return err
			}
			legacy.Delete(stream.ID)
		}
		return nil
	})
}
