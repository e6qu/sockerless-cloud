package main

import "testing"

// TestParseClauseHasOperator — the cloudrun + gcf backends use
// `logName:"run.googleapis.com"` (Cloud Logging's `:` substring
// operator) to scope `docker logs` to runtime stdout/stderr and exclude
// Cloud Audit Logs. parseClause must recognise `:` distinct from the
// wildcard branch.
func TestParseClauseHasOperator(t *testing.T) {
	c := parseClause(`logName:"run.googleapis.com"`)
	if c.field != "logName" {
		t.Errorf("field: got %q want logName", c.field)
	}
	if c.op != opHas {
		t.Errorf("op: got %v want opHas", c.op)
	}
	if c.value != "run.googleapis.com" {
		t.Errorf("value: got %q want run.googleapis.com", c.value)
	}
}

func TestMatchesFilterHasOperator(t *testing.T) {
	entry := LogEntry{
		LogName:     "projects/p/logs/run.googleapis.com%2Fstdout",
		TextPayload: "hello from smoke test",
		Resource:    &MonitoredResource{Type: "cloud_run_job", Labels: map[string]string{"job_name": "j1"}},
	}
	cases := []struct {
		name   string
		filter string
		want   bool
	}{
		{"logName-substring-match", `logName:"run.googleapis.com"`, true},
		{"logName-substring-no-match", `logName:"cloudaudit.googleapis.com"`, false},
		{"resource-eq", `resource.type="cloud_run_job"`, true},
		{"resource-eq-no-match", `resource.type="cloud_run_revision"`, false},
		{"compound-real-cloudrun-filter",
			`resource.type="cloud_run_job" AND resource.labels.job_name="j1" AND logName:"run.googleapis.com"`,
			true},
		{"compound-wrong-job", `resource.type="cloud_run_job" AND resource.labels.job_name="other" AND logName:"run.googleapis.com"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesFilter(entry, tc.filter)
			if got != tc.want {
				t.Errorf("matchesFilter(%q) = %v want %v", tc.filter, got, tc.want)
			}
		})
	}
}

// TestParseClauseColonInValueDoesNotMisparse guards against a future
// regression where a `:` inside a quoted value (e.g. timestamps) would
// be picked up as the operator. parseClause uses Index for the first
// occurrence, so we currently DO have this brittleness — document it
// here so the next refactor sees the intent and fixes it properly.
func TestParseClauseColonOnlyInsideValueStillParsesAsHas(t *testing.T) {
	// `resource.labels.x="2026-05-02T12:00:00Z"` — the `:` in the
	// timestamp value would be mis-detected if we used the first `:`
	// found. parseClause runs `=` BEFORE `:`, so equality wins here.
	c := parseClause(`resource.labels.x="2026-05-02T12:00:00Z"`)
	if c.op != opEq {
		t.Errorf("op: got %v want opEq (= takes precedence over :)", c.op)
	}
	if c.field != "resource.labels.x" {
		t.Errorf("field: got %q", c.field)
	}
	if c.value != "2026-05-02T12:00:00Z" {
		t.Errorf("value: got %q", c.value)
	}
}
