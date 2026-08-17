package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRegionalCPUQuota_DisabledWhenBudgetZero — quota tracking is opt-in
// via SIM_GCP_CPU_QUOTA_PER_REGION. With zero budget every debit
// succeeds; this is the default for unrelated tests so no quota
// failures bleed in.
func TestRegionalCPUQuota_DisabledWhenBudgetZero(t *testing.T) {
	q := &regionalCPUQuota{window: time.Minute, debitsBy: map[string][]quotaDebit{}}
	for i := 0; i < 100; i++ {
		if !q.tryDebit("p", "us-central1", 1.0) {
			t.Fatalf("debit %d rejected with budget=0; should always pass", i)
		}
	}
}

// TestRegionalCPUQuota_BlocksOverBudget — when budget is set, debits
// are accumulated and the first one that would push the running total
// past budget is rejected.
func TestRegionalCPUQuota_BlocksOverBudget(t *testing.T) {
	q := &regionalCPUQuota{budget: 3, window: time.Minute, debitsBy: map[string][]quotaDebit{}}
	if !q.tryDebit("p", "us-central1", 1) {
		t.Fatal("first 1 CPU debit should pass under budget=3")
	}
	if !q.tryDebit("p", "us-central1", 1) {
		t.Fatal("second 1 CPU debit should pass under budget=3")
	}
	if !q.tryDebit("p", "us-central1", 1) {
		t.Fatal("third 1 CPU debit should pass at the budget limit")
	}
	if q.tryDebit("p", "us-central1", 1) {
		t.Fatal("fourth 1 CPU debit should fail; would exceed budget=3")
	}
}

// TestRegionalCPUQuota_PartitionedByProjectAndRegion — quotas are
// tracked separately per (project, region). One region exhausting its
// budget doesn't affect another.
func TestRegionalCPUQuota_PartitionedByProjectAndRegion(t *testing.T) {
	q := &regionalCPUQuota{budget: 1, window: time.Minute, debitsBy: map[string][]quotaDebit{}}
	if !q.tryDebit("p", "us-central1", 1) {
		t.Fatal("us-central1 first debit should pass")
	}
	if q.tryDebit("p", "us-central1", 1) {
		t.Fatal("us-central1 second debit should fail (budget=1 exhausted)")
	}
	if !q.tryDebit("p", "europe-west1", 1) {
		t.Fatal("europe-west1 debit should pass — quota is per-region")
	}
	if !q.tryDebit("other-project", "us-central1", 1) {
		t.Fatal("different project, same region should pass — quota is per-project")
	}
}

// TestRegionalCPUQuota_SlidingWindow — debits older than the window roll off so
// the budget refreshes over time. Reproduces the live cloud's per-minute
// regional CPU allocation behaviour.
//
// Time comes from a clock the test advances, not from the host's. Sleeping past
// a millisecond-scale window makes the boundary a race with the scheduler: a
// debit meant to land inside the window passes when the host pauses the test
// for longer than the window, and the roll-off fails when a sleep
// under-delivers. Advancing an explicit clock puts each debit exactly where the
// case means it to be, and lets the case sit on the boundary itself.
func TestRegionalCPUQuota_SlidingWindow(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	now := start
	q := &regionalCPUQuota{
		budget:   1,
		window:   time.Minute,
		debitsBy: map[string][]quotaDebit{},
		now:      func() time.Time { return now },
	}

	if !q.tryDebit("p", "us-central1", 1) {
		t.Fatal("first debit should pass")
	}

	// One tick short of the window: the first debit is still inside it.
	now = start.Add(time.Minute - time.Nanosecond)
	if q.tryDebit("p", "us-central1", 1) {
		t.Fatal("a debit while the first is still inside the window should fail")
	}

	// Past the window: the first debit has rolled off and the budget is free.
	now = start.Add(time.Minute + time.Nanosecond)
	if !q.tryDebit("p", "us-central1", 1) {
		t.Fatal("debit after window roll-off should pass")
	}

	// The debit that just landed holds the budget for its own window, so the
	// roll-off freed exactly one debit's worth rather than clearing the key.
	if q.tryDebit("p", "us-central1", 1) {
		t.Fatal("the rolled-over debit must itself hold the budget")
	}
}

// TestContainerCPULoad_ParsesAllFormats — Cloud Run accepts both whole
// vCPU ("1", "2"), fractional ("0.5"), and milli-vCPU ("500m") forms.
// Each must convert to the same numeric CPU-min cost.
func TestContainerCPULoad_ParsesAllFormats(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 1.0},
		{"1", 1.0},
		{"2", 2.0},
		{"0.5", 0.5},
		{"500m", 0.5},
		{"100m", 0.1},
		{"  1  ", 1.0},
	}
	for _, c := range cases {
		got := containerCPULoad(c.in)
		if got != c.want {
			t.Errorf("containerCPULoad(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

// TestServiceCPULoad_SumsAcrossContainers — multi-container revisions
// (e.g. gitlab-runner-cloudrun's 3-container shape) charge the union of
// container CPU limits, mirroring how the live cloud schedules them on
// a single instance.
func TestServiceCPULoad_SumsAcrossContainers(t *testing.T) {
	svc := ServiceV2{Template: &RevisionTemplate{Containers: []Container{
		{Name: "init", Resources: &ResourceRequirements{Limits: map[string]string{"cpu": "1"}}},
		{Name: "runner", Resources: &ResourceRequirements{Limits: map[string]string{"cpu": "1"}}},
		{Name: "sockerless", Resources: &ResourceRequirements{Limits: map[string]string{"cpu": "1"}}},
	}}}
	if got := serviceCPULoad(svc); got != 3.0 {
		t.Errorf("3-container service CPU load = %v, want 3.0", got)
	}
}

// TestRegionalCPUQuotaErrorJSON_Format — the simulator must produce the
// same error payload the live cloud emits when CPU quota is exhausted,
// because the gcf backend's error path matches on substrings of the
// message ("Quota exceeded for total allowable CPU per project per
// region"). Drift here would mask backend-side regressions.
func TestRegionalCPUQuotaErrorJSON_Format(t *testing.T) {
	rec := httptest.NewRecorder()
	regionalCPUQuotaErrorJSON(rec, "projects/p/locations/us-central1/services/foo")
	body := rec.Body.String()
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(body, "Quota exceeded for total allowable CPU per project per region") {
		t.Errorf("body missing the canonical quota message; got %q", body)
	}
	if !strings.Contains(body, "INVALID_ARGUMENT") {
		t.Errorf("body missing INVALID_ARGUMENT status; got %q", body)
	}
	if !strings.Contains(body, "projects/p/locations/us-central1/services/foo") {
		t.Errorf("body missing resource name; got %q", body)
	}
}
