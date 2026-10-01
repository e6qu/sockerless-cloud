package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// serveDeclaringWait serves req to h through sim.InFlightMiddleware and
// reports the wait h declared for it.
func serveDeclaringWait(h http.HandlerFunc, req *http.Request) (code int, wait time.Duration, openEnded bool) {
	rec := httptest.NewRecorder()
	sim.InFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(w, r)
		wait, openEnded = sim.DeclaredWait(r.Context())
	})).ServeHTTP(rec, req)
	return rec.Code, wait, openEnded
}

// WaitOperation blocks for the timeout its request carries, and without one
// for as long as the caller's connection lasts.
func TestGCPAwaitOperationDeclaresItsWait(t *testing.T) {
	useRunJobOperationStores(t)
	const name = "projects/p/locations/l/operations/never"
	cases := []struct {
		timeout       time.Duration
		wantWait      time.Duration
		wantOpenEnded bool
	}{
		{timeout: 30 * time.Second, wantWait: 30 * time.Second},
		{timeout: 0, wantOpenEnded: true},
	}
	for _, tc := range cases {
		_, wait, openEnded := serveDeclaringWait(func(w http.ResponseWriter, r *http.Request) {
			gcpAwaitOperation(r.Context(), name, tc.timeout)
		}, httptest.NewRequest(http.MethodPost, "/", nil))
		if wait != tc.wantWait || openEnded != tc.wantOpenEnded {
			t.Errorf("WaitOperation with timeout %s declared %s (open-ended %v), want %s (open-ended %v)",
				tc.timeout, wait, openEnded, tc.wantWait, tc.wantOpenEnded)
		}
	}
}

// Compute Engine's operations.wait returns within two minutes, so that is the
// wait it declares.
func TestComputeWaitOperationDeclaresItsBudget(t *testing.T) {
	saved := computeOpRegistry
	computeOpRegistry = sim.MakeStore[ComputeOperationRecord](nil, "test_compute_operations_declared_wait")
	t.Cleanup(func() { computeOpRegistry = saved })
	rec := newComputeOpRecord("p", "zones/us-central1-a", "", "insert")
	recordComputeOp(rec)
	computeOpFinish(rec.Name, nil)

	code, wait, openEnded := serveDeclaringWait(func(w http.ResponseWriter, r *http.Request) {
		computeWaitOperation(w, r, rec.Name)
	}, httptest.NewRequest(http.MethodPost, "/compute/v1/projects/p/zones/us-central1-a/operations/"+rec.Name+"/wait", nil))
	if code != http.StatusOK || wait != computeOperationWaitBudget || openEnded {
		t.Fatalf("operations.wait answered %d declaring %s (open-ended %v), want 200 declaring %s",
			code, wait, openEnded, computeOperationWaitBudget)
	}
}

// entries.tail streams until the client goes away, so it declares no bound.
func TestLoggingEntriesTailDeclaresAnOpenEndedWait(t *testing.T) {
	saved := logEntries
	logEntries = sim.MakeStore[[]LogEntry](nil, "test_logging_entries_declared_wait")
	t.Cleanup(func() { logEntries = saved })

	// The client has already gone, so the stream ends at its first wait.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v2/entries:tail",
		strings.NewReader(`{"resourceNames":["projects/p"]}`))
	code, wait, openEnded := serveDeclaringWait(handleLoggingEntriesTail, req)
	if code != http.StatusOK || wait != 0 || !openEnded {
		t.Fatalf("entries.tail answered %d declaring %s (open-ended %v), want 200 declaring an open-ended wait", code, wait, openEnded)
	}
}

func TestCloudFunctionTimeoutReadsTheServiceConfig(t *testing.T) {
	if got := cloudFunctionTimeout(&storedFunction{}); got != 60*time.Second {
		t.Fatalf("timeout of a function without a service config = %s, want the 60-second default", got)
	}
	fn := &storedFunction{ServiceConfig: &storedServiceConfig{ServiceConfig{TimeoutSeconds: 540}}}
	if got := cloudFunctionTimeout(fn); got != 540*time.Second {
		t.Fatalf("timeout of a function configured for 540 seconds = %s", got)
	}
}
