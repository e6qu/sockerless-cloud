package lbplane

import (
	"context"
	"sync"
	"time"
)

// HealthState is where a health checker has put a target.
type HealthState int

const (
	// HealthInitial is a target the checker has not yet put in or out of
	// service.
	HealthInitial HealthState = iota
	HealthHealthy
	HealthUnhealthy
)

// HealthPolicy is a health check's schedule and thresholds, in the cloud's own
// terms: how often a target is checked, and how many consecutive results move
// it into or out of service.
type HealthPolicy struct {
	Interval time.Duration
	// InitialHealthyThreshold is the consecutive successes that put a target
	// never yet in service into service.
	InitialHealthyThreshold int
	// HealthyThreshold is the consecutive successes that return a target taken
	// out of service.
	HealthyThreshold int
	// UnhealthyThreshold is the consecutive failures that take a target out of
	// service, or keep a new one from entering it.
	UnhealthyThreshold int
}

// Health is one target's recorded health.
type Health struct {
	State HealthState
	// Checks counts the checks that have completed.
	Checks int
	// Successes and Failures count the consecutive results since the last
	// result of the other kind.
	Successes int
	Failures  int
	// LastStatus and LastErr are the latest completed check's HTTP status (0
	// when none was read) and error; LastFailure is the latest failed check's
	// error.
	LastStatus  int
	LastErr     error
	LastFailure error
	// LastChecked is when the latest completed check was issued.
	LastChecked time.Time

	nextCheck time.Time
}

// HealthTarget is one target a sweep keeps health for.
type HealthTarget[K comparable] struct {
	Key    K
	Policy HealthPolicy
	// Probe runs one check and returns the HTTP status it read, if any.
	Probe func(context.Context) (int, error)
}

// HealthTracker keeps the health of a set of targets current by checking each
// on its own interval, the way a load balancer's health checker runs on its
// own schedule rather than because a request arrived. Readers see what the
// latest checks recorded.
type HealthTracker[K comparable] struct {
	mu      sync.Mutex
	records map[K]*Health
}

func NewHealthTracker[K comparable]() *HealthTracker[K] {
	return &HealthTracker[K]{records: map[K]*Health{}}
}

// Health returns the target's recorded health, and false when the tracker has
// not yet scheduled a check for it.
func (t *HealthTracker[K]) Health(key K) (Health, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	record, ok := t.records[key]
	if !ok {
		return Health{}, false
	}
	return *record, true
}

// Forget drops the target's record, so a target registered again starts from a
// registration.
func (t *HealthTracker[K]) Forget(key K) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.records, key)
}

// Reset drops every record.
func (t *HealthTracker[K]) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = map[K]*Health{}
}

// Sweep checks every target whose next check has come due at now, folds the
// results into their records, and forgets the targets not in the set. It
// returns the keys whose state changed. Checks run concurrently, so one
// unresponsive target costs the sweep its own timeout rather than the sum.
func (t *HealthTracker[K]) Sweep(ctx context.Context, now time.Time, targets []HealthTarget[K]) []K {
	due := t.schedule(now, targets)
	var pending sync.WaitGroup
	changed := make(chan K, len(due))
	for _, target := range due {
		pending.Add(1)
		go func(target HealthTarget[K]) {
			defer pending.Done()
			status, err := target.Probe(ctx)
			if t.record(target.Key, target.Policy, now, status, err) {
				changed <- target.Key
			}
		}(target)
	}
	pending.Wait()
	close(changed)
	var keys []K
	for key := range changed {
		keys = append(keys, key)
	}
	return keys
}

// schedule books the next check of every due target. A check's result lasts
// for the whole interval, so the next one is booked when this one is issued
// rather than when it answers.
func (t *HealthTracker[K]) schedule(now time.Time, targets []HealthTarget[K]) []HealthTarget[K] {
	t.mu.Lock()
	defer t.mu.Unlock()
	present := make(map[K]bool, len(targets))
	var due []HealthTarget[K]
	for _, target := range targets {
		present[target.Key] = true
		record, ok := t.records[target.Key]
		if !ok {
			record = &Health{}
			t.records[target.Key] = record
		} else if now.Before(record.nextCheck) {
			continue
		}
		record.nextCheck = now.Add(target.Policy.Interval)
		due = append(due, target)
	}
	for key := range t.records {
		if !present[key] {
			delete(t.records, key)
		}
	}
	return due
}

func (t *HealthTracker[K]) record(key K, policy HealthPolicy, issued time.Time, status int, err error) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	record, ok := t.records[key]
	if !ok {
		// The target left the set while its check was in flight.
		return false
	}
	previous := record.State
	record.Checks++
	record.LastStatus = status
	record.LastErr = err
	record.LastChecked = issued
	if err == nil {
		record.Failures = 0
		record.Successes++
		threshold := policy.InitialHealthyThreshold
		if record.State == HealthUnhealthy {
			threshold = policy.HealthyThreshold
		}
		if record.Successes >= max(threshold, 1) {
			record.State = HealthHealthy
		}
	} else {
		record.Successes = 0
		record.Failures++
		record.LastFailure = err
		if record.Failures >= max(policy.UnhealthyThreshold, 1) {
			record.State = HealthUnhealthy
		}
	}
	return record.State != previous
}

// SweepEvery calls sweep every period until ctx ends.
func SweepEvery(ctx context.Context, period time.Duration, sweep func(context.Context, time.Time)) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			sweep(ctx, now)
		}
	}
}
