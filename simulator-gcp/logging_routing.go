package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
)

const (
	loggingRequiredID            = "_Required"
	loggingDefaultID             = "_Default"
	loggingRequiredRetentionDays = 400
	loggingDefaultRetentionDays  = 30
	// Cloud Logging discards an entry stamped further than this ahead.
	loggingFutureWindow = 24 * time.Hour
)

// loggingRequiredLogIDs are the logs the _Required sink routes, and the
// _Default sink leaves to it.
var loggingRequiredLogIDs = []string{
	"cloudaudit.googleapis.com/activity",
	"externalaudit.googleapis.com/activity",
	"cloudaudit.googleapis.com/system_event",
	"externalaudit.googleapis.com/system_event",
	"cloudaudit.googleapis.com/access_transparency",
	"externalaudit.googleapis.com/access_transparency",
}

func loggingRequiredFilter() string {
	terms := make([]string, len(loggingRequiredLogIDs))
	for i, id := range loggingRequiredLogIDs {
		terms[i] = fmt.Sprintf("LOG_ID(%q)", id)
	}
	return strings.Join(terms, " OR ")
}

func loggingDefaultFilter() string {
	terms := make([]string, len(loggingRequiredLogIDs))
	for i, id := range loggingRequiredLogIDs {
		terms[i] = fmt.Sprintf("NOT LOG_ID(%q)", id)
	}
	return strings.Join(terms, " AND ")
}

// loggingStoredEntry is an entry as the store holds it: the entry, and the log
// buckets the scope's sinks routed a copy of it to when it was written.
type loggingStoredEntry struct {
	Entry   LogEntry `json:"entry"`
	Buckets []string `json:"buckets"`
}

var logEntries *logstore.Log[loggingStoredEntry]

func loggingNewEntries(db *sql.DB) *logstore.Log[loggingStoredEntry] {
	return logstore.New(
		sim.MakeStore[logstore.Entry[loggingStoredEntry]](db, "logging_log_entries"),
		sim.MakeStore[logstore.Head](db, "logging_log_heads"))
}

// loggingScopeOf names the resource container a log belongs to, such as
// projects/p for projects/p/logs/app.
func loggingScopeOf(logName string) string {
	scope, _, _ := strings.Cut(logName, "/logs/")
	return scope
}

func loggingBucketName(scope, id string) string {
	return scope + "/locations/global/buckets/" + id
}

// loggingEnsureDefaults gives scope the _Required and _Default log buckets and
// the sinks that route to them, which every Cloud Logging resource container
// has. It leaves a bucket or sink that exists as it is, so a changed _Default
// retention or a disabled _Default sink stays changed.
func loggingEnsureDefaults(scope string) {
	if scope == "" {
		return
	}
	now := nowTimestamp()
	buckets := []LogBucket{
		{Name: loggingBucketName(scope, loggingRequiredID), Description: "Audit bucket",
			RetentionDays: loggingRequiredRetentionDays, Locked: true},
		{Name: loggingBucketName(scope, loggingDefaultID), Description: "Default bucket",
			RetentionDays: loggingDefaultRetentionDays},
	}
	for _, b := range buckets {
		logBuckets.Upsert(b.Name, func(cur *LogBucket) {
			if cur.Name != "" {
				return
			}
			b.LifecycleState = "ACTIVE"
			b.CreateTime, b.UpdateTime = now, now
			*cur = b
		})
	}
	sinks := []LoggingSink{
		{Name: scope + "/sinks/" + loggingRequiredID,
			Destination: "logging.googleapis.com/" + loggingBucketName(scope, loggingRequiredID),
			Filter:      loggingRequiredFilter()},
		{Name: scope + "/sinks/" + loggingDefaultID,
			Destination: "logging.googleapis.com/" + loggingBucketName(scope, loggingDefaultID),
			Filter:      loggingDefaultFilter()},
	}
	for _, s := range sinks {
		logSinks.Upsert(s.Name, func(cur *LoggingSink) {
			if cur.Name == "" {
				*cur = s
			}
		})
	}
}

func loggingIsBuiltIn(name string) bool {
	id := lastSegment(name)
	return id == loggingRequiredID || id == loggingDefaultID
}

type loggingRoute struct {
	bucket     string
	filter     listq.Node
	exclusions []listq.Node
}

// loggingRoutes are scope's enabled sinks that store entries in one of its log
// buckets. The exclusions of the _Default sink include those the exclusions
// API keeps for the scope.
func loggingRoutes(scope string) ([]loggingRoute, error) {
	var routes []loggingRoute
	for _, sink := range logSinks.ListPrefix(scope + "/sinks/") {
		bucket, ok := strings.CutPrefix(sink.Item.Destination, "logging.googleapis.com/")
		if sink.Item.Disabled || !ok {
			continue
		}
		if b, exists := logBuckets.Get(bucket); !exists || b.LifecycleState != "ACTIVE" {
			continue
		}
		f, err := parseLogFilter(sink.Item.Filter)
		if err != nil {
			return nil, fmt.Errorf("sink %s: %w", sink.ID, err)
		}
		rt := loggingRoute{bucket: bucket, filter: f}
		exclusions := slices.Clone(sink.Item.Exclusions)
		if lastSegment(sink.ID) == loggingDefaultID {
			for _, ex := range logExclusions.ListPrefix(scope + "/exclusions/") {
				exclusions = append(exclusions, LoggingExclusion{Name: ex.Item.Name, Filter: ex.Item.Filter, Disabled: ex.Item.Disabled})
			}
		}
		for _, ex := range exclusions {
			if ex.Disabled {
				continue
			}
			exf, err := parseLogFilter(ex.Filter)
			if err != nil {
				return nil, fmt.Errorf("sink %s exclusion %s: %w", sink.ID, ex.Name, err)
			}
			rt.exclusions = append(rt.exclusions, exf)
		}
		routes = append(routes, rt)
	}
	return routes, nil
}

// loggingBucketCutoffs maps each log bucket to the oldest entry time, in Unix
// milliseconds, its retention keeps at now.
func loggingBucketCutoffs(now time.Time) map[string]int64 {
	cutoffs := map[string]int64{}
	for _, b := range logBuckets.List() {
		days := b.RetentionDays
		if days <= 0 {
			days = loggingDefaultRetentionDays
		}
		cutoffs[b.Name] = now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	}
	return cutoffs
}

// loggingRouteEntry returns the buckets routes store entry in at now: those
// whose sink filter matches it and no exclusion does, and whose retention it
// is still inside. An entry stamped more than a day ahead goes nowhere.
func loggingRouteEntry(entry LogEntry, at int64, routes []loggingRoute, cutoffs map[string]int64, now time.Time) ([]string, error) {
	if at > now.Add(loggingFutureWindow).UnixMilli() {
		return nil, nil
	}
	doc, err := listq.ToDoc(entry)
	if err != nil {
		return nil, err
	}
	var buckets []string
	for _, rt := range routes {
		if slices.Contains(buckets, rt.bucket) || at < cutoffs[rt.bucket] || !rt.filter.Eval(doc) {
			continue
		}
		if slices.ContainsFunc(rt.exclusions, func(ex listq.Node) bool { return ex.Eval(doc) }) {
			continue
		}
		buckets = append(buckets, rt.bucket)
	}
	return buckets, nil
}

func loggingEntryMillis(entry LogEntry) (int64, error) {
	at, err := parseTimestamp(entry.Timestamp)
	if err != nil {
		return 0, fmt.Errorf("entry %q has an unreadable timestamp %q: %w", entry.InsertID, entry.Timestamp, err)
	}
	return at.UnixMilli(), nil
}

// loggingHeldIn reports whether bucket still holds its copy of e at the
// cutoffs.
func loggingHeldIn(e logstore.Entry[loggingStoredEntry], bucket string, cutoffs map[string]int64) bool {
	cutoff, exists := cutoffs[bucket]
	return exists && e.Time >= cutoff && slices.Contains(e.Value.Buckets, bucket)
}

// loggingHeld reports whether any bucket still holds a copy of e.
func loggingHeld(e logstore.Entry[loggingStoredEntry], cutoffs map[string]int64) bool {
	return slices.ContainsFunc(e.Value.Buckets, func(b string) bool { return loggingHeldIn(e, b, cutoffs) })
}

// loggingHeldEntries returns the entries of the logs under prefix that a
// bucket still holds, oldest arrival first within each log.
func loggingHeldEntries(prefix string, cutoffs map[string]int64) []logstore.Entry[loggingStoredEntry] {
	var out []logstore.Entry[loggingStoredEntry]
	for _, log := range logEntries.Scopes(prefix) {
		for _, e := range logEntries.Entries(log) {
			if loggingHeld(e, cutoffs) {
				out = append(out, e)
			}
		}
	}
	return out
}

// loggingRetentionSweepInterval is how often expired copies are deleted.
const loggingRetentionSweepInterval = time.Hour

// loggingSweepRetention deletes each bucket's copies its retention has passed,
// and the entries no bucket holds any longer. It returns how many entries it
// deleted.
func loggingSweepRetention(now time.Time) int {
	cutoffs := loggingBucketCutoffs(now)
	deleted := 0
	for _, log := range logEntries.Scopes("") {
		deleted += logEntries.Edit(log, func(e *logstore.Entry[loggingStoredEntry]) (bool, bool) {
			kept := slices.DeleteFunc(slices.Clone(e.Value.Buckets), func(b string) bool { return !loggingHeldIn(*e, b, cutoffs) })
			if len(kept) == 0 {
				return false, false
			}
			if len(kept) == len(e.Value.Buckets) {
				return true, false
			}
			e.Value.Buckets = kept
			return true, true
		})
	}
	return deleted
}

// loggingDropFromBucket deletes bucket's copies of log's entries, and the
// entries no other bucket holds.
func loggingDropFromBucket(log, bucket string) {
	logEntries.Edit(log, func(e *logstore.Entry[loggingStoredEntry]) (bool, bool) {
		if !slices.Contains(e.Value.Buckets, bucket) {
			return true, false
		}
		e.Value.Buckets = slices.DeleteFunc(slices.Clone(e.Value.Buckets), func(b string) bool { return b == bucket })
		return len(e.Value.Buckets) > 0, true
	})
}

// loggingDeleteLog deletes log's entries from its scope's global _Default
// bucket, which is what logs.delete deletes from.
func loggingDeleteLog(log string) {
	loggingDropFromBucket(log, loggingBucketName(loggingScopeOf(log), loggingDefaultID))
}

// loggingBucketChangeRefused names why Cloud Logging refuses upd to cur: the
// _Required bucket takes no change, and a locked bucket keeps its retention.
func loggingBucketChangeRefused(cur, upd LogBucket) string {
	changesRetention := upd.RetentionDays != 0 && upd.RetentionDays != cur.RetentionDays
	if lastSegment(cur.Name) == loggingRequiredID && (changesRetention || upd.Description != "" && upd.Description != cur.Description) {
		return fmt.Sprintf("bucket %s cannot be modified", cur.Name)
	}
	if cur.Locked && changesRetention {
		return fmt.Sprintf("bucket %s is locked; its retention period cannot be changed", cur.Name)
	}
	return ""
}

// loggingBucketDeleteRefused names why Cloud Logging refuses to delete b: the
// built-in buckets stay, and a locked bucket goes only once it is empty.
func loggingBucketDeleteRefused(b LogBucket) string {
	if loggingIsBuiltIn(b.Name) {
		return fmt.Sprintf("bucket %s is built in and cannot be deleted", b.Name)
	}
	if !b.Locked {
		return ""
	}
	scope, _, _ := loggingBucketParts(b.Name)
	cutoffs := loggingBucketCutoffs(time.Now())
	for _, log := range logEntries.Scopes(scope + "/logs/") {
		if slices.ContainsFunc(logEntries.Entries(log), func(e logstore.Entry[loggingStoredEntry]) bool {
			return loggingHeldIn(e, b.Name, cutoffs)
		}) {
			return fmt.Sprintf("bucket %s is locked and holds log entries", b.Name)
		}
	}
	return ""
}

// loggingSinkAdmissible answers the error Cloud Logging returns for a sink
// write it refuses: a create that takes a built-in sink's name, a change to
// the _Required sink, or a filter the query language does not admit.
func loggingSinkAdmissible(w http.ResponseWriter, sink LoggingSink, creating bool) bool {
	switch {
	case creating && loggingIsBuiltIn(sink.Name):
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "sink %s already exists", sink.Name)
		return false
	case !creating && lastSegment(sink.Name) == loggingRequiredID:
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "sink %s cannot be modified", sink.Name)
		return false
	}
	if _, err := parseLogFilter(sink.Filter); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "sink filter: %v", err)
		return false
	}
	for _, ex := range sink.Exclusions {
		if _, err := parseLogFilter(ex.Filter); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "sink exclusion %s filter: %v", ex.Name, err)
			return false
		}
	}
	return true
}
