package main

import (
	"database/sql"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// spannerLegacyScheduleRun is the row an earlier simulator persisted in
// spanner_backup_schedule_runs: when a schedule last produced a backup.
type spannerLegacyScheduleRun struct {
	Schedule string `json:"schedule"`
	LastRun  string `json:"lastRun"`
}

// spannerAdoptLegacyScheduleRuns turns each schedule's last run into its next
// occurrence, so an upgrade neither repeats an occurrence already backed up
// nor loses the ones still to come.
func spannerAdoptLegacyScheduleRuns(db *sql.DB, schedules sim.Store[spannerBackupSchedule], into sim.Store[cron.Record]) {
	legacy := sim.MakeStore[spannerLegacyScheduleRun](db, "spanner_backup_schedule_runs")
	for _, row := range legacy.ListPrefix("") {
		if record, ok := spannerRecordFromLastRun(schedules, row.ID, row.Item.LastRun); ok {
			into.Put(row.ID, record)
		}
		legacy.Delete(row.ID)
	}
}

func spannerRecordFromLastRun(schedules sim.Store[spannerBackupSchedule], name, lastRun string) (cron.Record, bool) {
	schedule, ok := schedules.Get(name)
	if !ok {
		return cron.Record{}, false
	}
	ran, err := time.Parse(time.RFC3339Nano, lastRun)
	if err != nil {
		return cron.Record{}, false
	}
	crontab, err := spannerCrontab(schedule.Spec.GetCronText())
	if err != nil {
		return cron.Record{}, false
	}
	next, ok := crontab.Next(ran)
	return cron.Record{Spec: schedule.Spec.GetCronText(), Next: next}, ok
}
