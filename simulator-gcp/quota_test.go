package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A zero budget disables the quota: every debit succeeds.
func TestRegionalCPUQuota_DisabledWhenBudgetZero(t *testing.T) {
	q := &regionalCPUQuota{window: time.Minute, debitsBy: map[string][]quotaDebit{}}
	for i := 0; i < 100; i++ {
		if !q.tryDebit("p", "us-central1", 1.0) {
			t.Fatalf("debit %d rejected with budget=0; should always pass", i)
		}
	}
}

// With a budget set, the first debit that would pass it is refused.
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

// Each project and region has its own budget.
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

// Debits older than the window roll off, freeing the budget. The test drives
// an explicit clock so each debit lands exactly where the case puts it,
// including on the boundary, rather than racing the scheduler with sleeps.
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

// Whole ("1"), fractional ("0.5") and milli-vCPU ("500m") limits convert to
// the same CPU-minute cost.
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

// A multi-container revision debits the sum of its containers' CPU limits.
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

// The error payload matches Cloud Run's exactly, since clients match on its
// message.
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
