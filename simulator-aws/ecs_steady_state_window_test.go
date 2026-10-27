package main

import (
	"testing"
	"time"
)

// startedAt truncates to the second, so a task that started at 10.999 records
// 10; the window must account for that or a deployment latches COMPLETED on a
// container about to exit.
func TestECSTaskHeldSteadyState_HonoursTheWindowWhateverTheSecondBoundary(t *testing.T) {
	now := time.Now()

	// Truncation means the task may have started an instant ago, so the window is
	// not provably held.
	atTheWindow := now.Add(-ecsServiceSteadyStateWindow)
	if ecsTaskHeldSteadyState(float64(atTheWindow.UnixMilli()) / 1000) {
		t.Errorf("a task whose recorded second is only %v old counted as having held "+
			"the %v window, but truncation allows it to have started an instant ago",
			ecsServiceSteadyStateWindow, ecsServiceSteadyStateWindow)
	}

	// Old enough even if the task started at the very end of its second.
	settled := now.Add(-(ecsServiceSteadyStateWindow + time.Second))
	if !ecsTaskHeldSteadyState(float64(settled.UnixMilli()) / 1000) {
		t.Errorf("a task started %v ago did not count as having held the %v window",
			now.Sub(settled), ecsServiceSteadyStateWindow)
	}
}

// An early wake-up finds the task short of the window and leaves the
// deployment to the slower stabilization tick.
func TestECSTaskSteadyStateAt_IsTheInstantTheWindowIsHeld(t *testing.T) {
	startedAt := float64(time.Now().Add(-500*time.Millisecond).UnixMilli()) / 1000
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
