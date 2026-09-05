package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestSpannerParseCrontabMatchesOccurrences(t *testing.T) {
	cron, err := spannerParseCrontab("0 2 * * *")
	if err != nil {
		t.Fatalf("parse daily crontab: %v", err)
	}
	if !cron.matches(time.Date(2026, 3, 4, 2, 0, 0, 0, time.UTC)) {
		t.Error("daily 02:00 crontab did not match 02:00")
	}
	if cron.matches(time.Date(2026, 3, 4, 3, 0, 0, 0, time.UTC)) {
		t.Error("daily 02:00 crontab matched 03:00")
	}

	stepped, err := spannerParseCrontab("30 */6 * * 1-5")
	if err != nil {
		t.Fatalf("parse stepped crontab: %v", err)
	}
	// 2026-03-04 is a Wednesday.
	if !stepped.matches(time.Date(2026, 3, 4, 12, 30, 0, 0, time.UTC)) {
		t.Error("stepped crontab did not match Wednesday 12:30")
	}
	if stepped.matches(time.Date(2026, 3, 7, 12, 30, 0, 0, time.UTC)) {
		t.Error("weekday-restricted crontab matched a Saturday")
	}

	for _, bad := range []string{"", "0 2 * *", "not a crontab", "60 2 * * *", "0 2 * * 9"} {
		if _, err := spannerParseCrontab(bad); err == nil {
			t.Errorf("crontab %q was accepted", bad)
		}
	}
}

func TestSpannerLatestCronOccurrenceIsBoundedAndInclusive(t *testing.T) {
	cron, err := spannerParseCrontab("0 2 * * *")
	if err != nil {
		t.Fatalf("parse crontab: %v", err)
	}
	now := time.Date(2026, 3, 4, 9, 17, 0, 0, time.UTC)

	occurrence, fired := spannerLatestCronOccurrence(cron, now.Add(-14*time.Hour), now)
	if !fired {
		t.Fatal("an occurrence since the last run was not reported")
	}
	if want := time.Date(2026, 3, 4, 2, 0, 0, 0, time.UTC); !occurrence.Equal(want) {
		t.Errorf("occurrence = %s, want %s", occurrence, want)
	}

	if _, fired := spannerLatestCronOccurrence(cron, now.Add(-2*time.Hour), now); fired {
		t.Error("an occurrence was reported for a window that contains none")
	}
	// The look-back window bounds how far the scheduler reaches back: a weekly
	// schedule whose last occurrence fell days ago does not fire now, because a
	// simulator that was not running does not backfill the backups it missed.
	weekly, err := spannerParseCrontab("0 2 * * 0")
	if err != nil {
		t.Fatalf("parse weekly crontab: %v", err)
	}
	// 2026-03-04 is a Wednesday; the previous Sunday 02:00 is three days back.
	if _, fired := spannerLatestCronOccurrence(weekly, now.Add(-7*24*time.Hour), now); fired {
		t.Error("an occurrence outside the look-back window was reported")
	}
}

// TestSpannerBackupScheduleTakesRealBackup drives the scheduler at a chosen
// instant and checks what it produced: a backup whose captured bytes restore
// the rows the database held. A schedule that only recorded metadata would
// restore an empty database and fail here.
func TestSpannerBackupScheduleTakesRealBackup(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}

	const (
		project  = "test-project"
		instance = "sched-unit"
		database = "sched-db"
	)
	instanceName := "projects/" + project + "/instances/" + instance
	dbName := instanceName + "/databases/" + database

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s → %d: %s", path, rec.Code, rec.Body.String())
		}
		return rec
	}

	post("/spanner/v1/projects/"+project+"/instances",
		`{"instanceId":"`+instance+`","instance":{"displayName":"`+instance+`","nodeCount":1}}`)
	post("/spanner/v1/"+instanceName+"/databases",
		`{"createStatement":"CREATE DATABASE `+"`"+database+"`"+`","extraStatements":["CREATE TABLE Rows (Id STRING(36) NOT NULL, Body STRING(MAX)) PRIMARY KEY (Id)"]}`)

	// Write through the same engine the data plane executes against.
	backend, err := spannerBackendFor(dbName)
	if err != nil {
		t.Fatalf("open backing engine: %v", err)
	}
	if _, err := backend.db.Exec(`INSERT INTO "Rows" (Id, Body) VALUES ('r1','one'),('r2','two')`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	post("/spanner/v1/"+dbName+"/backupSchedules?backupScheduleId=nightly",
		`{"retentionDuration":"86400s","spec":{"cronSpec":{"text":"0 2 * * *"}},"fullBackupSpec":{}}`)

	scheduleName := dbName + "/backupSchedules/nightly"
	now := time.Date(2026, 3, 4, 9, 17, 0, 0, time.UTC)
	spannerBackupScheduleRuns.Put(scheduleName, spannerBackupScheduleRun{
		Schedule: scheduleName,
		LastRun:  now.Add(-14 * time.Hour).Format(time.RFC3339Nano),
	})

	created := spannerRunDueBackupSchedules(now)
	if len(created) != 1 {
		t.Fatalf("scheduler created %d backups, want 1: %v", len(created), created)
	}
	want := instanceName + "/backups/nightly-20260304t020000"
	if created[0] != want {
		t.Fatalf("scheduled backup name = %q, want %q", created[0], want)
	}
	backup, ok := spannerBackups.Get(created[0])
	if !ok {
		t.Fatal("the scheduled backup was not recorded")
	}
	if len(backup.BackupSchedules) != 1 || backup.BackupSchedules[0] != scheduleName {
		t.Errorf("backup.backupSchedules = %v, want the schedule that produced it", backup.BackupSchedules)
	}
	if backup.ExpireTime == "" || backup.SizeBytes == "" || backup.SizeBytes == "0" {
		t.Errorf("scheduled backup recorded no captured bytes: %+v", backup)
	}

	// A second sweep at the same instant does not duplicate the occurrence.
	if again := spannerRunDueBackupSchedules(now); len(again) != 0 {
		t.Errorf("a repeated sweep created %v", again)
	}

	// The captured bytes are the database's: restoring them elsewhere brings
	// back the rows.
	image, ok := spannerBackupImages.Get(created[0])
	if !ok {
		t.Fatal("the scheduled backup captured no image")
	}
	restored := instanceName + "/databases/sched-db-restored"
	if err := spannerMaterializeFromImage(context.Background(), restored, image.Image); err != nil {
		t.Fatalf("materialize restored engine: %v", err)
	}
	restoredBackend, err := spannerBackendFor(restored)
	if err != nil {
		t.Fatalf("open restored engine: %v", err)
	}
	var count int
	if err := restoredBackend.db.QueryRow(`SELECT count(*) FROM "Rows"`).Scan(&count); err != nil {
		t.Fatalf("query restored rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("restored database holds %d rows, want the 2 the backup captured", count)
	}
}
