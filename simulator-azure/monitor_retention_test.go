package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

const retentionWorkspacePath = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.OperationalInsights/workspaces/retention"

func retentionWorkspace(t *testing.T, srv scheduleTestSimulator) Workspace {
	t.Helper()
	rec := armCall(t, srv, http.MethodPut, retentionWorkspacePath+"?api-version=2022-10-01",
		`{"location":"eastus","properties":{"retentionInDays":30}}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create workspace: %d %s", rec.Code, rec.Body.String())
	}
	var ws Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func queryableMessages(scope string, now time.Time) []string {
	var out []string
	for _, row := range monitorQueryableRows(scope, now) {
		out = append(out, row["Message"])
	}
	slices.Sort(out)
	return out
}

// A query reads a table's rows within its interactive retention; rows past it
// stay in long-term retention until the table's total retention, and the
// sweep deletes them after that.
func TestMonitorTableRetentionHidesThenDeletesRows(t *testing.T) {
	srv := buildScheduleTestSimulator(t)
	ws := retentionWorkspace(t, srv)
	if rec := armCall(t, srv, http.MethodPut, retentionWorkspacePath+"/tables/AppTraces?api-version=2022-10-01",
		`{"properties":{"totalRetentionInDays":90}}`); rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("set the table's total retention: %d %s", rec.Code, rec.Body.String())
	}
	now := time.Now().UTC()
	for _, r := range []struct {
		msg string
		age time.Duration
	}{{"fresh", time.Hour}, {"long-term", 40 * 24 * time.Hour}} {
		appendWorkspaceLogRow(ws.Properties.CustomerID, "AppTraces", monitorLogRow{
			"TimeGenerated": now.Add(-r.age).Format(time.RFC3339Nano), "Message": r.msg})
	}
	scope := logRowsKey(ws.Properties.CustomerID, "AppTraces")
	if got := queryableMessages(scope, now); !slices.Equal(got, []string{"fresh"}) {
		t.Fatalf("a query reads %v", got)
	}
	if deleted := monitorSweepRetention(now); deleted != 0 {
		t.Fatalf("the sweep deleted %d rows still in long-term retention", deleted)
	}
	if deleted := monitorSweepRetention(now.Add(60 * 24 * time.Hour)); deleted != 1 {
		t.Fatalf("past the total retention the sweep deleted %d rows, want 1", deleted)
	}
	if got := monitorLogs.Values(scope); len(got) != 1 || got[0]["Message"] != "fresh" {
		t.Fatalf("rows left: %v", got)
	}
}

// A table that follows its workspace's retention reads the workspace's
// current value.
func TestMonitorTableFollowsTheWorkspaceRetention(t *testing.T) {
	srv := buildScheduleTestSimulator(t)
	ws := retentionWorkspace(t, srv)
	now := time.Now().UTC()
	appendWorkspaceLogRow(ws.Properties.CustomerID, "AppTraces", monitorLogRow{
		"TimeGenerated": now.Add(-45 * 24 * time.Hour).Format(time.RFC3339Nano), "Message": "45 days"})
	scope := logRowsKey(ws.Properties.CustomerID, "AppTraces")
	if got := queryableMessages(scope, now); len(got) != 0 {
		t.Fatalf("with 30 days a query reads %v", got)
	}
	if rec := armCall(t, srv, http.MethodPatch, retentionWorkspacePath+"?api-version=2022-10-01",
		`{"properties":{"retentionInDays":60}}`); rec.Code != http.StatusOK {
		t.Fatalf("patch workspace: %d %s", rec.Code, rec.Body.String())
	}
	if got := queryableMessages(scope, now); !slices.Equal(got, []string{"45 days"}) {
		t.Fatalf("with 60 days a query reads %v", got)
	}
}

// A classic Application Insights component keeps telemetry for its own
// RetentionInDays, 90 days unless it names one.
func TestClassicAppInsightsRetention(t *testing.T) {
	saved, savedComponents := monitorLogs, azureAppInsightsComponents
	t.Cleanup(func() { monitorLogs, azureAppInsightsComponents = saved, savedComponents })
	monitorLogs = monitorNewLog(nil, "retention_test_rows", "retention_test_heads")
	azureAppInsightsComponents = nil
	scope := appLogRowsKey("classic", "AppTraces")
	now := time.Now().UTC()
	for _, days := range []int{10, 100} {
		appendLogRow(scope, monitorLogRow{"TimeGenerated": now.AddDate(0, 0, -days).Format(time.RFC3339Nano), "Message": time.Duration(days).String()})
	}
	if got := monitorQueryableRows(scope, now); len(got) != 1 {
		t.Fatalf("a classic component without a retention reads %d rows, want 1", len(got))
	}
	if deleted := monitorSweepRetention(now); deleted != 1 {
		t.Fatalf("the sweep deleted %d rows, want 1", deleted)
	}
}

// A site's container output keeps its newest lines.
func TestSiteDockerLogKeepsItsNewestLines(t *testing.T) {
	saved := webSiteDockerLogs
	t.Cleanup(func() { webSiteDockerLogs = saved })
	webSiteDockerLogs = monitorNewLog(nil, "docker_test_lines", "docker_test_heads")
	scope := "site"
	now := time.Now()
	for i := 0; i < webSiteDockerLogMaxLines+2; i++ {
		monitorAppendRow(webSiteDockerLogs, scope, monitorLogRow{"TimeGenerated": now.Format(time.RFC3339Nano), "Message": "x"})
		webSiteDockerLogs.Cap(scope, webSiteDockerLogMaxLines)
	}
	if h := webSiteDockerLogs.Head(scope); h.Count != webSiteDockerLogMaxLines {
		t.Fatalf("the docker log keeps %d lines, want %d", h.Count, webSiteDockerLogMaxLines)
	}
}

// Rows an earlier build stored whole load after the upgrade in their original
// order, timed by their TimeGenerated.
func TestMonitorMigratesLegacyRows(t *testing.T) {
	db, err := sim.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sim.CloseDB(db) })
	saved, savedDocker := monitorLogs, webSiteDockerLogs
	t.Cleanup(func() { monitorLogs, webSiteDockerLogs = saved, savedDocker })
	monitorLogs = monitorNewLog(db, "monitor_log_rows", "monitor_log_heads")
	webSiteDockerLogs = monitorNewLog(db, "web_site_docker_log_lines", "web_site_docker_log_heads")
	legacy, err := sim.NewSQLiteStore[[]monitorLogRow](db, "monitor_logs")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	legacy.Put("cust:AppTraces", []monitorLogRow{
		{"TimeGenerated": at.Format(time.RFC3339Nano), "Message": "first"},
		{"TimeGenerated": at.Add(-time.Hour).Format(time.RFC3339Nano), "Message": "second"},
	})
	if err := monitorMigrateRows(db); err != nil {
		t.Fatal(err)
	}
	got := monitorLogs.Entries("cust:AppTraces")
	if len(got) != 2 || got[0].Value["Message"] != "first" || got[1].Value["Message"] != "second" || got[0].Time != at.UnixMilli() {
		t.Fatalf("migrated rows = %+v", got)
	}
	if _, rows, err := sim.LegacyRows[[]monitorLogRow](db, "monitor_logs"); err != nil || len(rows) != 0 {
		t.Fatalf("legacy rows left: %d %v", len(rows), err)
	}
}
