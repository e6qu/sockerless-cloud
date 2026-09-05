package main

// Cloud Run regional CPU quota — a sliding-window CPU-minute budget per
// (project, region). Each fresh service revision deploy debits the
// container's CPU limit (e.g. 1 vCPU = 1 CPU-min) against the budget;
// debits older than the window roll off automatically. When the next
// debit would exceed the budget, the simulator rejects the deploy with
// the same `Quota exceeded for total allowable CPU per project per
// region` error string the live cloud produces, so backends + tests can
// reproduce deterministically without burning real
// quota.
//
// Configurable via env:
//
//   SIM_GCP_CPU_QUOTA_PER_REGION   — CPU-min budget (default 0 = unlimited)
//   SIM_GCP_CPU_QUOTA_WINDOW       — sliding window duration (default 1m)
//
// Set the quota to 0 (default) when running unrelated tests so deploys
// don't fail spuriously. Tests targeting explicitly raise
// the env to a small value (e.g. `2`) and observe the failure mode.

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// quotaDebit is one historical allocation event.
type quotaDebit struct {
	at  time.Time
	cpu float64
}

// regionalCPUQuota tracks CPU-min debits per (project, region) within a
// sliding window. Zero budget = quota disabled.
//
// now reads the clock the window is measured against. It is nil in the
// simulator, where the wall clock is the only clock that matters; a test drives
// the roll-off from a clock it controls so the boundary is decided by the
// window rather than by how the host scheduled the test.
type regionalCPUQuota struct {
	mu       sync.Mutex
	budget   float64
	window   time.Duration
	debitsBy map[string][]quotaDebit // key = project|region
	now      func() time.Time
}

// clock returns the current time on whichever clock this quota measures its
// window against.
func (q *regionalCPUQuota) clock() time.Time {
	if q.now != nil {
		return q.now()
	}
	return time.Now()
}

// quotaKey concatenates project + region into a stable map key.
func quotaKey(project, region string) string {
	return project + "|" + region
}

// newRegionalCPUQuotaFromEnv reads SIM_GCP_CPU_QUOTA_PER_REGION (CPU-min)
// and SIM_GCP_CPU_QUOTA_WINDOW (Go duration). Missing/zero budget →
// quota tracking disabled. Window defaults to 1 minute when unset.
func newRegionalCPUQuotaFromEnv() *regionalCPUQuota {
	q := &regionalCPUQuota{
		window:   time.Minute,
		debitsBy: make(map[string][]quotaDebit),
	}
	if v := os.Getenv("SIM_GCP_CPU_QUOTA_PER_REGION"); v != "" {
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && n > 0 {
			q.budget = n
		}
	}
	if v := os.Getenv("SIM_GCP_CPU_QUOTA_WINDOW"); v != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
			q.window = d
		}
	}
	return q
}

// activeBudget returns the CPU-min already used in the current window
// for (project, region) after pruning expired entries. Caller holds mu.
func (q *regionalCPUQuota) activeBudget(now time.Time, key string) float64 {
	debits := q.debitsBy[key]
	cutoff := now.Add(-q.window)
	first := 0
	for first < len(debits) && debits[first].at.Before(cutoff) {
		first++
	}
	if first > 0 {
		debits = debits[first:]
		q.debitsBy[key] = debits
	}
	var sum float64
	for _, d := range debits {
		sum += d.cpu
	}
	return sum
}

// tryDebit attempts to record a `cpu` CPU-min allocation. Returns true
// when debited, false when the deploy would exceed budget. Quota
// disabled (budget==0) → always true.
func (q *regionalCPUQuota) tryDebit(project, region string, cpu float64) bool {
	if q == nil || q.budget == 0 {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	key := quotaKey(project, region)
	now := q.clock()
	used := q.activeBudget(now, key)
	if used+cpu > q.budget {
		return false
	}
	q.debitsBy[key] = append(q.debitsBy[key], quotaDebit{at: now, cpu: cpu})
	return true
}

// regionalCPUQuotaErrorMessage is the live-cloud error string the gcf
// backend matches against. Kept exact so backend logic that parses the
// message (e.g. error classification) behaves identically.
const regionalCPUQuotaErrorMessage = "Container Healthcheck failed. Quota exceeded for total allowable CPU per project per region."

// regionalCPUQuotaErrorJSON writes the GCP-style InvalidArgument error
// the live cloud produces when CPU quota is exhausted. Status 400 +
// status="INVALID_ARGUMENT" matches the wire format the Cloud Functions
// + Cloud Run APIs return on this condition.
func regionalCPUQuotaErrorJSON(w http.ResponseWriter, resourceName string) {
	msg := regionalCPUQuotaErrorMessage
	if resourceName != "" {
		msg = "Could not create or update Cloud Run service " + resourceName + ", " + msg
	}
	GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
}

// containerCPULoad parses a Container resource limit (e.g. "1", "0.5",
// "1000m") into CPU-min cost for a single deploy. Empty/missing → 1
// (Cloud Functions Gen2 minimum). The cost model assumes a deploy
// consumes (1 minute × CPU-limit) of regional budget, which matches the
// observed live-cloud behaviour for the gcf overlay-and-swap path.
func containerCPULoad(cpuLimit string) float64 {
	cpuLimit = strings.TrimSpace(cpuLimit)
	if cpuLimit == "" {
		return 1.0
	}
	if strings.HasSuffix(cpuLimit, "m") {
		if n, err := strconv.ParseFloat(strings.TrimSuffix(cpuLimit, "m"), 64); err == nil {
			return n / 1000.0
		}
	}
	if n, err := strconv.ParseFloat(cpuLimit, 64); err == nil {
		return n
	}
	return 1.0
}

// serviceCPULoad sums CPU loads across every container in the service
// template. A deploy debits the union (real Cloud Run schedules them
// concurrently in one revision instance).
func serviceCPULoad(svc ServiceV2) float64 {
	if svc.Template == nil {
		return 1.0
	}
	var sum float64
	for _, c := range svc.Template.Containers {
		var cpu string
		if c.Resources != nil {
			cpu = c.Resources.Limits["cpu"]
		}
		sum += containerCPULoad(cpu)
	}
	if sum == 0 {
		return 1.0
	}
	return sum
}

// regionalCPUQuotaInstance is the singleton accessed by route handlers
// in cloudrunservices.go + cloudfunctions.go. Initialised by main()
// before route registration.
var regionalCPUQuotaInstance *regionalCPUQuota

// initRegionalCPUQuota wires the singleton. Idempotent — repeated calls
// (e.g. across test setups) preserve the live debit history.
func initRegionalCPUQuota() {
	if regionalCPUQuotaInstance == nil {
		regionalCPUQuotaInstance = newRegionalCPUQuotaFromEnv()
	}
}
