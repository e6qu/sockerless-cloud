package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

func logFilterMatches(t *testing.T, entry LogEntry, filter string) bool {
	t.Helper()
	node, err := parseLogFilter(filter)
	if err != nil {
		t.Fatalf("parseLogFilter(%q): %v", filter, err)
	}
	doc, err := listq.ToDoc(entry)
	if err != nil {
		t.Fatal(err)
	}
	return node.Eval(doc)
}

func TestLogFilterQueryLanguage(t *testing.T) {
	entry := LogEntry{
		LogName:     "projects/p/logs/run.googleapis.com%2Fstdout",
		TextPayload: "hello from smoke test",
		Severity:    "WARNING",
		Timestamp:   "2026-05-02T12:00:00.5Z",
		Resource:    &MonitoredResource{Type: "cloud_run_job", Labels: map[string]string{"job_name": "j1"}},
		JsonPayload: map[string]any{"code": float64(42)},
	}
	cases := []struct {
		filter string
		want   bool
	}{
		{`logName:"run.googleapis.com"`, true},
		{`logName:"cloudaudit.googleapis.com"`, false},
		{`resource.type="cloud_run_job"`, true},
		{`resource.type="cloud_run_revision"`, false},
		{`resource.type="cloud_run_job" AND resource.labels.job_name="j1" AND logName:"run.googleapis.com"`, true},
		{`resource.type="cloud_run_job" AND resource.labels.job_name="other"`, false},
		{`resource.labels.job_name="other" OR resource.labels.job_name="j1"`, true},
		{`NOT resource.labels.job_name="j1"`, false},
		{`-logName:"cloudaudit.googleapis.com"`, true},
		{`resource.labels.x="2026-05-02T12:00:00Z"`, false},
		{`severity>=WARNING`, true},
		{`severity>WARNING`, false},
		{`severity<ERROR`, true},
		{`severity=warning`, true},
		{`timestamp>="2026-05-02T12:00:00Z"`, true},
		{`timestamp<"2026-05-02T12:00:00.4Z"`, false},
		{`jsonPayload.code>9`, true},
		{`textPayload=~"^hello.*test$"`, true},
		{`textPayload!~"smoke"`, false},
		{`smoke`, true},
		{`"no such text"`, false},
		{`labels.absent="x"`, false},
		{`labels.absent!="x"`, false},
	}
	for _, tc := range cases {
		if got := logFilterMatches(t, entry, tc.filter); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.filter, got, tc.want)
		}
	}
	if !logFilterMatches(t, LogEntry{LogName: "l"}, `severity=DEFAULT`) {
		t.Error("an entry without a severity has severity DEFAULT")
	}
}

// Cloud Logging answers a query its language does not admit with
// INVALID_ARGUMENT rather than returning every entry.
func TestLogFilterRejectsMalformedQueries(t *testing.T) {
	for _, filter := range []string{`logName="x`, `(severity>=INFO`, `textPayload=~"("`, `severity >=`, `== x`} {
		if _, err := parseLogFilter(filter); err == nil {
			t.Errorf("%s: parsed", filter)
		}
	}
}
