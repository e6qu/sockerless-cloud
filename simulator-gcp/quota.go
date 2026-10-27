package main

// A sliding-window CPU-minute budget per project and region. Each new Cloud Run
// revision debits its containers' CPU limits (1 vCPU is 1 CPU-minute), debits
// older than the window roll off, and a debit past the budget fails the deploy
// with Cloud Run's "Quota exceeded for total allowable CPU per project per
// region" error. SIM_GCP_CPU_QUOTA_PER_REGION sets the budget (0, the
// default, disables it) and SIM_GCP_CPU_QUOTA_WINDOW the window (1m).

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// quotaDebit is one allocation.
type quotaDebit struct {
	at  time.Time
	cpu float64
}

// regionalCPUQuota tracks CPU-minute debits per project and region within a
// sliding window; a zero budget disables it. now is the window's clock: nil
// reads the wall clock, and a test drives roll-off from a clock it controls.
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

func quotaKey(project, region string) string {
	return project + "|" + region
}

// newRegionalCPUQuotaFromEnv reads SIM_GCP_CPU_QUOTA_PER_REGION (CPU-minutes)
// and SIM_GCP_CPU_QUOTA_WINDOW (a Go duration, default 1m); a missing or zero
// budget disables the quota.
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

// activeBudget returns the CPU-minutes used in the current window for key,
// pruning expired debits. Callers hold mu.
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

// tryDebit records a cpu CPU-minute allocation, or reports false when it would
// exceed the budget; a disabled quota always debits.
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

// regionalCPUQuotaErrorMessage is Cloud Run's error message for an exhausted
// regional CPU quota, verbatim, since clients match on it.
const regionalCPUQuotaErrorMessage = "Container Healthcheck failed. Quota exceeded for total allowable CPU per project per region."

// regionalCPUQuotaErrorJSON writes the 400 INVALID_ARGUMENT Cloud Functions
// and Cloud Run answer when the CPU quota is exhausted.
func regionalCPUQuotaErrorJSON(w http.ResponseWriter, resourceName string) {
	msg := regionalCPUQuotaErrorMessage
	if resourceName != "" {
		msg = "Could not create or update Cloud Run service " + resourceName + ", " + msg
	}
	GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
}

// containerCPULoad converts a container CPU limit ("1", "0.5", "1000m") into
// the CPU-minutes one deploy debits; an empty limit is 1, the Cloud Run
// functions minimum.
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

// serviceCPULoad sums the CPU limits of every container in the template:
// Cloud Run schedules them together on one instance.
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

// regionalCPUQuotaInstance is the quota cloudrunservices.go and
// cloudfunctions.go debit, set by initRegionalCPUQuota.
var regionalCPUQuotaInstance *regionalCPUQuota

// initRegionalCPUQuota sets the quota once; later calls keep its debit
// history.
func initRegionalCPUQuota() {
	if regionalCPUQuotaInstance == nil {
		regionalCPUQuotaInstance = newRegionalCPUQuotaFromEnv()
	}
}
