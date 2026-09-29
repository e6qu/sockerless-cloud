package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

func TestSpannerCrontabIsFiveFieldUTC(t *testing.T) {
	crontab, err := spannerCrontab("30 */6 * * 1-5")
	if err != nil {
		t.Fatalf("parse stepped crontab: %v", err)
	}
	// 2026-03-06 is a Friday; the next weekday 30 minutes past a sixth hour after
	// 18:40 is Monday 00:30.
	next, ok := crontab.Next(time.Date(2026, 3, 6, 18, 40, 0, 0, time.UTC))
	if want := time.Date(2026, 3, 9, 0, 30, 0, 0, time.UTC); !ok || !next.Equal(want) {
		t.Errorf("next = %s, want %s", next, want)
	}
	for _, bad := range []string{"", "0 2 * *", "not a crontab", "60 2 * * *", "0 2 * * 9", "0 2 * * ? *"} {
		if _, err := spannerCrontab(bad); err == nil {
			t.Errorf("crontab %q was accepted", bad)
		}
	}
}

// TestSpannerBackupScheduleSkipsOccurrencesPastTheWindow checks the look-back
// bound: a weekly schedule whose occurrence fell days before the simulator
// ticked again does not take that backup late.
func TestSpannerBackupScheduleSkipsOccurrencesPastTheWindow(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	name := "projects/p/instances/i/databases/d/backupSchedules/weekly"
	spannerBackupSchedules.Put(name, spannerBackupSchedule{
		Name: name, RetentionDuration: "86400s",
		Spec: &spannerBackupScheduleSpec{CronSpec: &spannerCrontabSpec{Text: "0 2 * * 0"}},
	})
	// 2026-03-01 is a Sunday; the tick comes three days later.
	spannerBackupScheduleRuns.Put(name, cron.Record{Spec: "0 2 * * 0", Next: time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)})
	spannerBackupScheduleTicker().Tick(time.Date(2026, 3, 4, 9, 17, 0, 0, time.UTC))
	if backups := spannerBackups.List(); len(backups) != 0 {
		t.Fatalf("a backup was taken for an occurrence outside the window: %+v", backups)
	}
	if rec, _ := spannerBackupScheduleRuns.Get(name); !rec.Next.Equal(time.Date(2026, 3, 8, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("next occurrence = %s, want the coming Sunday", rec.Next)
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
	t.Cleanup(srv.StopBackground)

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
	spannerBackupScheduleRuns.Put(scheduleName, cron.Record{
		Spec: "0 2 * * *",
		Next: time.Date(2026, 3, 4, 2, 0, 0, 0, time.UTC),
	})

	ticker := spannerBackupScheduleTicker()
	ticker.Tick(now)
	want := instanceName + "/backups/nightly-20260304t020000"
	backup, ok := spannerBackups.Get(want)
	if !ok {
		t.Fatal("the scheduled backup was not recorded")
	}
	if len(backup.BackupSchedules) != 1 || backup.BackupSchedules[0] != scheduleName {
		t.Errorf("backup.backupSchedules = %v, want the schedule that produced it", backup.BackupSchedules)
	}
	if backup.ExpireTime == "" || backup.SizeBytes == "" || backup.SizeBytes == "0" {
		t.Errorf("scheduled backup recorded no captured bytes: %+v", backup)
	}

	// A second tick at the same instant does not duplicate the occurrence.
	before := spannerBackups.Len()
	ticker.Tick(now)
	if after := spannerBackups.Len(); after != before {
		t.Errorf("a repeated tick took %d more backups", after-before)
	}

	// The captured bytes are the database's: restoring them elsewhere brings
	// back the rows.
	image, ok := spannerBackupImages.Get(want)
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
