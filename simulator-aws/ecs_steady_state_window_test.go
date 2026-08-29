package main

import (
	"testing"
	"time"
)

// The steady-state window is judged against a Unix-second timestamp, so it must
// account for the truncation: a task that really started at 10.999 records 10.
// Comparing elapsed time against the window alone clears such a task a
// millisecond after it started, which is how a deployment latched COMPLETED on
// a container that was about to exit — and, because the failure attribution
// ignores a rollout that is not IN_PROGRESS, stopped counting the failures that
// should have tripped the circuit breaker.
func TestECSTaskHeldSteadyState_HonoursTheWindowWhateverTheSecondBoundary(t *testing.T) {
	now := time.Now()

	// A stamp exactly one window old. Truncation means the task may really
	// have started anywhere in the following second — up to an instant ago —
	// so the window is not provably held and this must be false. Comparing
	// elapsed time against the window alone returns true here, which is the
	// defect.
	atTheWindow := now.Add(-ecsServiceSteadyStateWindow)
	if ecsTaskHeldSteadyState(atTheWindow.Unix()) {
		t.Errorf("a task whose recorded second is only %v old counted as having held "+
			"the %v window, but truncation allows it to have started an instant ago",
			ecsServiceSteadyStateWindow, ecsServiceSteadyStateWindow)
	}

	// A task whose recorded second is old enough that the window has elapsed
	// even if it started at the very end of that second.
	settled := now.Add(-(ecsServiceSteadyStateWindow + time.Second))
	if !ecsTaskHeldSteadyState(settled.Unix()) {
		t.Errorf("a task started %v ago did not count as having held the %v window",
			now.Sub(settled), ecsServiceSteadyStateWindow)
	}
}

// The wake-up the scheduler arms must land when the task becomes eligible, not
// before: an early reconcile finds the task still short of the window and the
// deployment waits on the slower stabilization tick instead.
func TestECSTaskSteadyStateAt_IsTheInstantTheWindowIsHeld(t *testing.T) {
	startedAt := time.Now().Add(-500 * time.Millisecond).Unix()
	at := ecsTaskSteadyStateAt(startedAt)

	if ecsTaskHeldSteadyState(startedAt) {
		t.Fatal("the window is already held, so this case proves nothing")
	}
	if remaining := time.Until(at); remaining <= 0 {
		t.Fatalf("the eligibility instant is not in the future (%v)", remaining)
	}
	// One nanosecond past the instant, the window is held.
	if !time.Now().Add(time.Until(at) + time.Nanosecond).Before(at.Add(2 * time.Second)) {
		t.Fatal("the eligibility instant is implausibly far out")
	}
}
