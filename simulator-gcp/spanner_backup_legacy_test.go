package main

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// TestSpannerAdoptsScheduleRunsAnEarlierSimulatorPersisted proves an upgrade
// keeps a schedule's history: the last run an earlier simulator recorded
// becomes the next occurrence after it, so the occurrence already backed up is
// not taken again.
func TestSpannerAdoptsScheduleRunsAnEarlierSimulatorPersisted(t *testing.T) {
	db, err := sim.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const name = "projects/p/instances/i/databases/d/backupSchedules/nightly"
	schedules, err := sim.NewSQLiteStore[spannerBackupSchedule](db, "spanner_backup_schedules")
	if err != nil {
		t.Fatal(err)
	}
	schedules.Put(name, spannerBackupSchedule{
		Name: name, RetentionDuration: "86400s",
		Spec: &spannerBackupScheduleSpec{CronSpec: &spannerCrontabSpec{Text: "0 2 * * *"}},
	})
	old, err := sim.NewSQLiteStore[spannerLegacyScheduleRun](db, "spanner_backup_schedule_runs")
	if err != nil {
		t.Fatal(err)
	}
	old.Put(name, spannerLegacyScheduleRun{Schedule: name, LastRun: "2026-03-04T02:00:30Z"})
	old.Put("projects/p/instances/i/databases/d/backupSchedules/deleted", spannerLegacyScheduleRun{LastRun: "2026-03-04T02:00:30Z"})

	into, err := sim.NewSQLiteStore[cron.Record](db, "spanner_backup_schedule_occurrences")
	if err != nil {
		t.Fatal(err)
	}
	spannerAdoptLegacyScheduleRuns(db, schedules, into)

	rec, ok := into.Get(name)
	if !ok || rec.Spec != "0 2 * * *" || !rec.Next.Equal(time.Date(2026, 3, 5, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("adopted record = %+v (present %v), want the occurrence after the last run", rec, ok)
	}
	if into.Len() != 1 || old.Len() != 0 {
		t.Fatalf("records %d, old rows left %d", into.Len(), old.Len())
	}
}
