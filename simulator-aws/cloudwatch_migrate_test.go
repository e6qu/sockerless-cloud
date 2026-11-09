package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A stream stored whole by an earlier build loads in arrival order with its
// times kept, and takes new events after them.
func TestCloudWatchLogsMigratesLegacyStreamRows(t *testing.T) {
	db := migrateTestDB(t)
	key := cwEventsKey("/g", "s")
	legacyStore[[]CWLogEvent](t, db, "cw_log_events").Put(key, []CWLogEvent{
		{Timestamp: 2000, IngestionTime: 2100, Message: "second-timestamp-first"},
		{Timestamp: 1000, IngestionTime: 2200, Message: "older-arrived-later"},
	})
	legacyStore[[]CWLogEvent](t, db, "cw_log_events").Put(cwEventsKey("/g", "empty"), []CWLogEvent{})

	events := cwLogEvents
	t.Cleanup(func() { cwLogEvents = events })
	cwLogEvents = cwNewLogEvents(db)
	if err := cwMigrateLogEvents(db); err != nil {
		t.Fatal(err)
	}

	got, ok := cwStreamEvents(key)
	if !ok || len(got) != 2 || got[0].Message != "second-timestamp-first" || got[1].IngestionTime != 2200 {
		t.Fatalf("migrated stream = %v, %v", got, ok)
	}
	if got, ok := cwStreamEvents(cwEventsKey("/g", "empty")); !ok || len(got) != 0 {
		t.Fatalf("a migrated stream with no events = %v, %v", got, ok)
	}
	cwPutTestEvents(key, []CWLogEvent{{Timestamp: 3000, Message: "after"}})
	if got, _ := cwStreamEvents(key); len(got) != 3 || got[2].Message != "after" {
		t.Fatalf("stream after a new event = %v", got)
	}
	_, rows, err := sim.LegacyRows[[]CWLogEvent](db, "cw_log_events")
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy stream rows left: %d %v", len(rows), err)
	}
}

// A log group an earlier build protected from deletion, or gave a data
// protection policy, reads back with both after the upgrade.
func TestCloudWatchLogsMigratesLogGroupProtection(t *testing.T) {
	db := migrateTestDB(t)
	groups, policies := cwLogGroups, cwDataProtection
	t.Cleanup(func() { cwLogGroups, cwDataProtection = groups, policies })
	cwLogGroups = sim.MakeStore[CWLogGroup](db, "cw_log_groups")
	cwDataProtection = sim.MakeStore[CWDataProtectionPolicy](db, "cw_data_protection")
	cwLogGroups.Put("/protected", CWLogGroup{LogGroupName: "/protected"})
	cwLogGroups.Put("/masked", CWLogGroup{LogGroupName: "/masked"})
	legacyStore[bool](t, db, "cw_deletion_protection").Put("/protected", true)
	cwDataProtection.Put("/masked", CWDataProtectionPolicy{LogGroupName: "/masked", PolicyDocument: "{}"})

	if err := cwMigrateLogGroupProtection(db); err != nil {
		t.Fatal(err)
	}

	if g, _ := cwLogGroups.Get("/protected"); !g.DeletionProtectionEnabled {
		t.Fatalf("migrated protected group = %+v", g)
	}
	if g, _ := cwLogGroups.Get("/masked"); g.DataProtectionStatus != "ACTIVATED" || g.DeletionProtectionEnabled {
		t.Fatalf("migrated group with a data protection policy = %+v", g)
	}
	_, rows, err := sim.LegacyRows[bool](db, "cw_deletion_protection")
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy deletion protection rows left: %d %v", len(rows), err)
	}
}
