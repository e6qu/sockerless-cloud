package main

import (
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

// A site's docker log is one file a day in its file system, rotated by
// httpLogs.fileSystem: files older than retentionInDays go, and past
// retentionInMb the oldest go first. Without either setting the site keeps
// webSiteDockerLogMaxLines lines.

type webDockerLogRetention struct {
	quotaBytes int64
	days       int
}

// ok is false when it sets no limit.
func webSiteDockerLogRetention(siteID string) (webDockerLogRetention, bool) {
	extra, found := webConfigExtras.Get(siteID)
	if !found {
		return webDockerLogRetention{}, false
	}
	httpLogs, _ := extra.Logs["httpLogs"].(map[string]any)
	fs, _ := httpLogs["fileSystem"].(map[string]any)
	if fs == nil {
		return webDockerLogRetention{}, false
	}
	var r webDockerLogRetention
	if mb, ok := fs["retentionInMb"].(float64); ok && mb > 0 {
		r.quotaBytes = int64(mb) << 20
	}
	if days, ok := fs["retentionInDays"].(float64); ok && days > 0 {
		r.days = int(days)
	}
	return r, r.quotaBytes > 0 || r.days > 0
}

// Counted from the store on first need and kept as lines arrive and go.
var webDockerLogUsage = struct {
	sync.Mutex
	bytes     map[string]int64
	agedCheck map[string]time.Time
}{bytes: map[string]int64{}, agedCheck: map[string]time.Time{}}

func webDockerLogLineBytes(row monitorLogRow) int64 {
	return int64(len(row["TimeGenerated"]) + 1 + len(row["Message"]) + 1)
}

const webDockerLogAgeCheckInterval = time.Minute

func webRotateSiteDockerLog(siteID, scope string, lineBytes int64, now time.Time) {
	retention, ok := webSiteDockerLogRetention(siteID)
	if !ok {
		webSiteDockerLogs.Cap(scope, webSiteDockerLogMaxLines)
		return
	}
	webDockerLogUsage.Lock()
	defer webDockerLogUsage.Unlock()
	used, counted := webDockerLogUsage.bytes[scope]
	if !counted {
		for _, row := range webSiteDockerLogs.Values(scope) {
			used += webDockerLogLineBytes(row)
		}
	} else {
		used += lineBytes
	}
	if retention.days > 0 && now.Sub(webDockerLogUsage.agedCheck[scope]) >= webDockerLogAgeCheckInterval {
		webDockerLogUsage.agedCheck[scope] = now
		cutoff := now.UTC().AddDate(0, 0, -retention.days)
		day := cutoff.Format("2006-01-02")
		used -= webDropDockerLogLines(scope, func(e *logstore.Entry[monitorLogRow]) bool {
			return webDockerLogDay(e) < day
		})
	}
	if retention.quotaBytes > 0 && used > retention.quotaBytes {
		used -= webDropOldestDockerLogDays(scope, used-retention.quotaBytes)
	}
	webDockerLogUsage.bytes[scope] = used
}

func webDockerLogDay(e *logstore.Entry[monitorLogRow]) string {
	return time.UnixMilli(e.Time).UTC().Format("2006-01-02")
}

func webDropDockerLogLines(scope string, doomed func(*logstore.Entry[monitorLogRow]) bool) int64 {
	var freed int64
	webSiteDockerLogs.Edit(scope, func(e *logstore.Entry[monitorLogRow]) (bool, bool) {
		if doomed(e) {
			freed += webDockerLogLineBytes(e.Value)
			return false, false
		}
		return true, false
	})
	return freed
}

// Whole days, oldest first; when the newest day alone is over the quota, its
// oldest lines.
func webDropOldestDockerLogDays(scope string, excess int64) int64 {
	entries := webSiteDockerLogs.Entries(scope)
	if len(entries) == 0 {
		return 0
	}
	newest := webDockerLogDay(&entries[len(entries)-1])
	byDay := map[string]int64{}
	var days []string
	for i := range entries {
		d := webDockerLogDay(&entries[i])
		if _, seen := byDay[d]; !seen {
			days = append(days, d)
		}
		byDay[d] += webDockerLogLineBytes(entries[i].Value)
	}
	dropDays := map[string]bool{}
	var planned int64
	for _, d := range days {
		if planned >= excess || d == newest {
			break
		}
		dropDays[d] = true
		planned += byDay[d]
	}
	remaining := excess - planned
	return webDropDockerLogLines(scope, func(e *logstore.Entry[monitorLogRow]) bool {
		if dropDays[webDockerLogDay(e)] {
			return true
		}
		if remaining > 0 {
			remaining -= webDockerLogLineBytes(e.Value)
			return true
		}
		return false
	})
}

// For when the logs configuration or the log itself is replaced.
func webForgetDockerLogUsage(siteID string) {
	scope := strings.ToLower(siteID)
	webDockerLogUsage.Lock()
	defer webDockerLogUsage.Unlock()
	delete(webDockerLogUsage.bytes, scope)
	delete(webDockerLogUsage.agedCheck, scope)
}
