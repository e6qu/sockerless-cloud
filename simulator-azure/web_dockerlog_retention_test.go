package main

import (
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func withDockerLogRetention(t *testing.T, siteID string, fileSystem map[string]any) string {
	t.Helper()
	savedLogs, savedExtras := webSiteDockerLogs, webConfigExtras
	t.Cleanup(func() { webSiteDockerLogs, webConfigExtras = savedLogs, savedExtras })
	webSiteDockerLogs = monitorNewLog(nil, "docker_retention_lines", "docker_retention_heads")
	webConfigExtras = sim.MakeStore[webConfigExtra](nil, "web_config_extras_test")
	if fileSystem != nil {
		webConfigExtras.Put(siteID, webConfigExtra{Logs: map[string]any{"httpLogs": map[string]any{"fileSystem": fileSystem}}})
	}
	t.Cleanup(func() { webForgetDockerLogUsage(siteID) })
	return strings.ToLower(siteID)
}

func appendDockerLine(scope string, at time.Time, message string) {
	monitorAppendRow(webSiteDockerLogs, scope, monitorLogRow{"TimeGenerated": at.UTC().Format(time.RFC3339Nano), "Message": message})
}

func dockerMessages(scope string) []string {
	var out []string
	for _, row := range webSiteDockerLogs.Values(scope) {
		out = append(out, row["Message"])
	}
	return out
}

// Lines older than retentionInDays go once the site's docker log rotates.
func TestWebDockerLogExpiresDaysPastRetention(t *testing.T) {
	const site = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/aged"
	scope := withDockerLogRetention(t, site, map[string]any{"retentionInDays": float64(2), "enabled": true})
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	appendDockerLine(scope, now.AddDate(0, 0, -5), "five days ago")
	appendDockerLine(scope, now.AddDate(0, 0, -1), "yesterday")
	appendDockerLine(scope, now, "now")
	webRotateSiteDockerLog(site, scope, 0, now)
	if got := dockerMessages(scope); len(got) != 2 || got[0] != "yesterday" {
		t.Fatalf("kept %v", got)
	}
}

// Past the quota the oldest days go whole, then the oldest lines of the day
// still being written.
func TestWebDockerLogQuotaDropsOldestDaysFirst(t *testing.T) {
	const site = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/full"
	scope := withDockerLogRetention(t, site, nil)
	day1 := time.Date(2026, 5, 8, 9, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{day1, day1.Add(time.Hour), day1.AddDate(0, 0, 1), day1.AddDate(0, 0, 2), day1.AddDate(0, 0, 2).Add(time.Minute)} {
		appendDockerLine(scope, at, strings.Repeat("x", 10)+string(rune('a'+i)))
	}
	line := webDockerLogLineBytes(webSiteDockerLogs.Values(scope)[0])
	freed := webDropOldestDockerLogDays(scope, 3*line)
	if got := dockerMessages(scope); len(got) != 2 || !strings.HasSuffix(got[0], "d") {
		t.Fatalf("the two oldest days and then a line of the next go: %v", got)
	}
	if freed != 3*line {
		t.Fatalf("freed %d, want %d", freed, 3*line)
	}
	webDropOldestDockerLogDays(scope, line)
	if got := dockerMessages(scope); len(got) != 1 || !strings.HasSuffix(got[0], "e") {
		t.Fatalf("the newest day gives up its oldest lines last: %v", got)
	}
}

// A site whose logs configuration sets no limit keeps its newest lines up to
// the line bound.
func TestWebDockerLogWithoutSettingsKeepsTheLineBound(t *testing.T) {
	const site = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/plain"
	scope := withDockerLogRetention(t, site, map[string]any{"enabled": false})
	if _, ok := webSiteDockerLogRetention(site); ok {
		t.Fatal("a file-system block without limits sets no rotation")
	}
	appendDockerLine(scope, time.Now(), "kept")
	webRotateSiteDockerLog(site, scope, 0, time.Now())
	if got := dockerMessages(scope); len(got) != 1 {
		t.Fatalf("kept %v", got)
	}
}
