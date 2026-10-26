package main

import (
	"database/sql"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

const classicAppInsightsDefaultRetentionDays = 90

const webSiteDockerLogMaxLines = 50000

const monitorRetentionSweepInterval = time.Hour

func monitorNewLog(db *sql.DB, entries, heads string) *logstore.Log[monitorLogRow] {
	return logstore.New(
		sim.MakeStore[logstore.Entry[monitorLogRow]](db, entries),
		sim.MakeStore[logstore.Head](db, heads))
}

// A row without TimeGenerated counts from now.
func monitorRowTime(row monitorLogRow, now time.Time) time.Time {
	if at, err := time.Parse(time.RFC3339Nano, row["TimeGenerated"]); err == nil {
		return at
	}
	return now
}

func monitorAppendRow(log *logstore.Log[monitorLogRow], scope string, row monitorLogRow) {
	now := time.Now()
	log.Append(scope, now, logstore.Item[monitorLogRow]{Time: monitorRowTime(row, now).UnixMilli(), Value: row})
}

// logTableRetention returns the interactive and total retention in days,
// resolving a table that follows its workspace against the workspace now.
func logTableRetention(ws Workspace, table string) (interactive, total int) {
	interactive = ws.Properties.RetentionInDays
	total = interactive
	t, ok := logAnalyticsWorkspaceTable(ws, table)
	if !ok {
		return interactive, total
	}
	if !t.Properties.RetentionInDaysAsDefault && t.Properties.RetentionInDays > 0 {
		interactive = t.Properties.RetentionInDays
	}
	total = interactive
	if !t.Properties.TotalRetentionInDaysAsDefault && t.Properties.TotalRetentionInDays > total {
		total = t.Properties.TotalRetentionInDays
	}
	return interactive, total
}

func classicAppInsightsRetention(appID string) int {
	if azureAppInsightsComponents == nil {
		return classicAppInsightsDefaultRetentionDays
	}
	c, ok := azureInsightsByApplicationID.Lookup(azureAppInsightsComponents, strings.ToLower(appID), azureInsightsApplicationIDKeys)
	if !ok || c.Properties.RetentionInDays <= 0 {
		return classicAppInsightsDefaultRetentionDays
	}
	return c.Properties.RetentionInDays
}

// False for a scope whose workspace is gone.
func monitorScopeRetention(scope string) (interactive, total int, ok bool) {
	if rest, isApp := strings.CutPrefix(scope, "app/"); isApp {
		appID, _, _ := strings.Cut(rest, ":")
		days := classicAppInsightsRetention(appID)
		return days, days, true
	}
	customerID, table, found := strings.Cut(scope, ":")
	if !found {
		return 0, 0, false
	}
	ws, exists := logWorkspaceByCustomerID(customerID)
	if !exists {
		return 0, 0, false
	}
	interactive, total = logTableRetention(ws, table)
	return interactive, total, true
}

func retentionCutoff(now time.Time, days int) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

// Rows past the interactive retention but within the total stay in long-term
// retention, which queries do not read.
func monitorQueryableRows(scope string, now time.Time) []monitorLogRow {
	interactive, _, ok := monitorScopeRetention(scope)
	if !ok {
		return nil
	}
	cutoff := retentionCutoff(now, interactive).UnixMilli()
	var rows []monitorLogRow
	for _, e := range monitorLogs.Entries(scope) {
		if e.Time >= cutoff {
			rows = append(rows, e.Value)
		}
	}
	return rows
}

func monitorSweepRetention(now time.Time) int {
	deleted := 0
	for _, scope := range monitorLogs.Scopes("") {
		_, total, ok := monitorScopeRetention(scope)
		if !ok {
			monitorLogs.Drop(scope)
			continue
		}
		deleted += monitorLogs.Expire(scope, retentionCutoff(now, total))
	}
	return deleted
}

// monitorMigrateRows moves rows earlier builds stored whole — every row of a
// table, or a site's docker log, in one row — into the log stores.
func monitorMigrateRows(db *sql.DB) error {
	for _, legacy := range []struct {
		table string
		log   *logstore.Log[monitorLogRow]
	}{
		{"monitor_logs", monitorLogs},
		{"web_site_docker_logs", webSiteDockerLogs},
	} {
		if err := sim.Migrate(db, legacy.table+"/logstore", func() error {
			store, scopes, err := sim.LegacyRows[[]monitorLogRow](db, legacy.table)
			if err != nil {
				return err
			}
			now := time.Now()
			for _, scope := range scopes {
				entries := make([]logstore.Entry[monitorLogRow], 0, len(scope.Item))
				for _, row := range scope.Item {
					at := monitorRowTime(row, now).UnixMilli()
					entries = append(entries, logstore.Entry[monitorLogRow]{Time: at, Ingested: at, Value: row})
				}
				if err := legacy.log.Import(scope.ID, entries); err != nil {
					return err
				}
				store.Delete(scope.ID)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
