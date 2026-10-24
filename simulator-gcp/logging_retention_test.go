package main

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func loggingPayloads(t *testing.T, srv *sim.Server, resourceName string) []string {
	t.Helper()
	out := gcpHostOK(t, srv, "logging.googleapis.com", http.MethodPost, "/v2/entries:list", `{"resourceNames":["`+resourceName+`"]}`)
	entries, _ := out["entries"].([]any)
	var payloads []string
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		if loggingIsAuditEntry(entry) {
			continue
		}
		payload, _ := entry["textPayload"].(string)
		payloads = append(payloads, payload)
	}
	slices.Sort(payloads)
	return payloads
}

// loggingIsAuditEntry reports whether entry is an audit log entry Cloud
// Logging wrote for a call the test made, such as its own CreateSink, rather
// than an entry the test wrote.
func loggingIsAuditEntry(entry map[string]any) bool {
	proto, _ := entry["protoPayload"].(map[string]any)
	return proto["@type"] == "type.googleapis.com/google.cloud.audit.AuditLog"
}

// A scope starts with the _Required and _Default buckets and the sinks that
// route to them: audit activity to _Required, everything else to _Default.
func TestLoggingScopeStartsWithTheBuiltInBucketsAndSinks(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	sinks := gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/fresh/sinks", ``)["sinks"].([]any)
	var names []string
	for _, s := range sinks {
		names = append(names, s.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"_Default", "_Required"}) {
		t.Fatalf("a new project's sinks = %v", names)
	}
	required := gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/fresh/locations/global/buckets/_Required", ``)
	if required["retentionDays"] != float64(loggingRequiredRetentionDays) || required["locked"] != true {
		t.Fatalf("_Required bucket = %v", required)
	}
	def := gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/fresh/locations/global/buckets/_Default", ``)
	if def["retentionDays"] != float64(loggingDefaultRetentionDays) {
		t.Fatalf("_Default bucket = %v", def)
	}

	now := time.Now().UTC()
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/fresh/logs/cloudaudit.googleapis.com%2Factivity","timestamp":"`+now.Format(time.RFC3339)+`","textPayload":"admin activity"},
		{"logName":"projects/fresh/logs/app","timestamp":"`+now.Format(time.RFC3339)+`","textPayload":"application"}]}`)
	if got := loggingPayloads(t, srv, "projects/fresh/locations/global/buckets/_Required"); !slices.Equal(got, []string{"admin activity"}) {
		t.Errorf("_Required holds %v", got)
	}
	if got := loggingPayloads(t, srv, "projects/fresh/locations/global/buckets/_Default"); !slices.Equal(got, []string{"application"}) {
		t.Errorf("_Default holds %v", got)
	}
}

// Cloud Logging discards an entry stamped before a bucket's retention or more
// than a day ahead, and one no sink routes to a bucket.
func TestLoggingDiscardsWhatNoBucketTakes(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	now := time.Now().UTC()
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/d/logs/app","timestamp":"`+now.Add(-31*24*time.Hour).Format(time.RFC3339)+`","textPayload":"too old"},
		{"logName":"projects/d/logs/app","timestamp":"`+now.Add(25*time.Hour).Format(time.RFC3339)+`","textPayload":"too far ahead"},
		{"logName":"projects/d/logs/app","timestamp":"`+now.Format(time.RFC3339)+`","textPayload":"kept"}]}`)
	if got := loggingPayloads(t, srv, "projects/d"); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("the project holds %v", got)
	}

	gcpHostOK(t, srv, host, http.MethodPut, "/v2/projects/d/sinks/_Default?updateMask=disabled", `{"disabled":true}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/d/logs/app","timestamp":"`+now.Format(time.RFC3339)+`","textPayload":"unrouted"}]}`)
	if got := loggingPayloads(t, srv, "projects/d"); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("with the _Default sink disabled the project holds %v", got)
	}
}

// The exclusions API's exclusions keep entries out of the _Default bucket.
func TestLoggingExclusionsApplyToTheDefaultSink(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/x/exclusions", `{"name":"debug","filter":"severity<INFO"}`)
	now := time.Now().UTC().Format(time.RFC3339)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/x/logs/app","timestamp":"`+now+`","severity":"DEBUG","textPayload":"excluded"},
		{"logName":"projects/x/logs/app","timestamp":"`+now+`","severity":"ERROR","textPayload":"stored"}]}`)
	if got := loggingPayloads(t, srv, "projects/x"); !slices.Equal(got, []string{"stored"}) {
		t.Fatalf("the project holds %v", got)
	}
}

// Each bucket keeps its copy for its own retention: an entry another bucket
// still holds stays readable through that bucket after the shorter one drops
// it, and the sweep deletes an entry once no bucket holds it.
func TestLoggingBucketsExpireTheirOwnCopies(t *testing.T) {
	useLoggingMemoryStores(t)
	scope := "projects/r"
	loggingEnsureDefaults(scope)
	long := scope + "/locations/global/buckets/long"
	logBuckets.Put(long, LogBucket{Name: long, RetentionDays: 90, LifecycleState: "ACTIVE"})
	logSinks.Put(scope+"/sinks/to-long", LoggingSink{Name: scope + "/sinks/to-long", Destination: "logging.googleapis.com/" + long})

	now := time.Now().UTC()
	write := func(payload string, age time.Duration) {
		t.Helper()
		if err := writeLogEntries(scope+"/logs/app", nil, nil, []LogEntry{{
			TextPayload: payload, Timestamp: now.Add(-age).Format(time.RFC3339)}}); err != nil {
			t.Fatal(err)
		}
	}
	write("twenty days", 20*24*time.Hour)
	write("one day", 24*time.Hour)

	later := now.Add(15 * 24 * time.Hour)
	if deleted := loggingSweepRetention(later); deleted != 0 {
		t.Fatalf("the sweep deleted %d entries the long bucket still holds", deleted)
	}
	cutoffs := loggingBucketCutoffs(later)
	var inDefault, inLong []string
	for _, e := range logEntries.Entries(scope + "/logs/app") {
		if loggingHeldIn(e, loggingBucketName(scope, loggingDefaultID), cutoffs) {
			inDefault = append(inDefault, e.Value.Entry.TextPayload)
		}
		if loggingHeldIn(e, long, cutoffs) {
			inLong = append(inLong, e.Value.Entry.TextPayload)
		}
	}
	if !slices.Equal(inDefault, []string{"one day"}) || !slices.Equal(inLong, []string{"twenty days", "one day"}) {
		t.Fatalf("fifteen days on, _Default holds %v and long holds %v", inDefault, inLong)
	}
	if deleted := loggingSweepRetention(now.Add(100 * 24 * time.Hour)); deleted != 2 {
		t.Fatalf("past every retention the sweep deleted %d entries, want 2", deleted)
	}
}

// logs.delete deletes a log's entries from the _Default bucket; another
// bucket's copies stay.
func TestLoggingDeleteLogClearsOnlyTheDefaultBucket(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	bucket := "projects/k/locations/global/buckets/keep"
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/k/locations/global/buckets?bucketId=keep", `{}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/k/sinks", `{"name":"to-keep","destination":"logging.googleapis.com/`+bucket+`","filter":"severity>=ERROR"}`)
	now := time.Now().UTC().Format(time.RFC3339)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/k/logs/app","timestamp":"`+now+`","severity":"ERROR","textPayload":"in both"},
		{"logName":"projects/k/logs/app","timestamp":"`+now+`","severity":"INFO","textPayload":"default only"}]}`)
	gcpHostOK(t, srv, host, http.MethodDelete, "/v2/projects/k/logs/app", ``)
	if got := loggingPayloads(t, srv, "projects/k/locations/global/buckets/_Default"); len(got) != 0 {
		t.Errorf("_Default still holds %v", got)
	}
	if got := loggingPayloads(t, srv, bucket); !slices.Equal(got, []string{"in both"}) {
		t.Errorf("the other bucket holds %v", got)
	}
}

// Cloud Logging keeps the built-in buckets and sinks, and a locked bucket's
// retention; a bucket created without a retention period keeps 30 days.
func TestLoggingRefusesChangesToBuiltInsAndLockedBuckets(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodDelete, "/v2/projects/b/locations/global/buckets/_Default", ``, http.StatusBadRequest},
		{http.MethodPatch, "/v2/projects/b/locations/global/buckets/_Required", `{"retentionDays":10}`, http.StatusBadRequest},
		{http.MethodDelete, "/v2/projects/b/sinks/_Required", ``, http.StatusBadRequest},
		{http.MethodPut, "/v2/projects/b/sinks/_Required", `{"destination":"logging.googleapis.com/projects/b/locations/global/buckets/_Default"}`, http.StatusBadRequest},
		{http.MethodPost, "/v2/projects/b/sinks", `{"name":"bad","destination":"logging.googleapis.com/projects/b/locations/global/buckets/_Default","filter":"severity>="}`, http.StatusBadRequest},
		{http.MethodPatch, "/v2/projects/b/locations/global/buckets/_Default", `{"retentionDays":45}`, http.StatusOK},
	} {
		if code, out := gcpHostCall(t, srv, host, tc.method, tc.path, tc.body); code != tc.want {
			t.Errorf("%s %s answered %d %v, want %d", tc.method, tc.path, code, out, tc.want)
		}
	}
	created := gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/b/locations/global/buckets?bucketId=plain", `{}`)
	if created["retentionDays"] != float64(loggingDefaultRetentionDays) {
		t.Fatalf("a bucket created without a retention period = %v", created)
	}
	gcpHostOK(t, srv, host, http.MethodPatch, "/v2/projects/b/locations/global/buckets/plain", `{"locked":true}`)
	if code, _ := gcpHostCall(t, srv, host, http.MethodPatch, "/v2/projects/b/locations/global/buckets/plain", `{"retentionDays":60}`); code != http.StatusBadRequest {
		t.Errorf("changing a locked bucket's retention answered %d", code)
	}
}

func TestLogFilterLogIDAndSource(t *testing.T) {
	for filter, want := range map[string]bool{
		`LOG_ID("cloudaudit.googleapis.com/activity")`:                  true,
		`log_id("cloudaudit.googleapis.com/data_access")`:               false,
		`SOURCE("projects/p")`:                                          true,
		`NOT LOG_ID("cloudaudit.googleapis.com/activity") AND severity`: false,
	} {
		node, err := parseLogFilter(filter)
		if err != nil {
			t.Fatalf("%s: %v", filter, err)
		}
		doc := map[string]any{"logName": "projects/p/logs/cloudaudit.googleapis.com%2Factivity"}
		if got := node.Eval(doc); got != want {
			t.Errorf("%s = %v, want %v", filter, got, want)
		}
	}
	if _, err := parseLogFilter(`SAMPLE(insertId, 0.5)`); err == nil {
		t.Error("an unsupported function parsed")
	}
}

// A log an earlier build stored whole loads after the upgrade with each entry
// in the buckets its scope routes it to, and without the entries no bucket
// would take.
func TestLoggingMigratesLegacyLogRows(t *testing.T) {
	db, err := sim.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sim.CloseDB(db) })
	useLoggingMemoryStores(t)
	logEntries = loggingNewEntries(db)
	legacy, err := sim.NewSQLiteStore[[]LogEntry](db, "logging_entries")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	legacy.Put("projects/m/logs/app", []LogEntry{
		{LogName: "projects/m/logs/app", InsertID: "1", Timestamp: now.Add(-time.Hour).Format(time.RFC3339), TextPayload: "recent"},
		{LogName: "projects/m/logs/app", InsertID: "2", Timestamp: now.Add(-60 * 24 * time.Hour).Format(time.RFC3339), TextPayload: "expired"},
	})
	if err := loggingMigrateEntries(db, now); err != nil {
		t.Fatal(err)
	}
	got := logEntries.Values("projects/m/logs/app")
	if len(got) != 1 || got[0].Entry.TextPayload != "recent" || !slices.Equal(got[0].Buckets, []string{loggingBucketName("projects/m", loggingDefaultID)}) {
		t.Fatalf("migrated log = %+v", got)
	}
	if _, rows, err := sim.LegacyRows[[]LogEntry](db, "logging_entries"); err != nil || len(rows) != 0 {
		t.Fatalf("legacy log rows left: %d %v", len(rows), err)
	}
}
