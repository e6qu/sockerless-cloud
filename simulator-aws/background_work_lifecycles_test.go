package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A simulator runs work off the request path two ways, and a test's drain has
// to see both.
//
// simGo's goroutines were counted from the start. Work handed to the server's
// own lifecycle — Server.StartBackground, which exists so orderly shutdown
// drains it before SQLite closes — was not, so AwaitSimulatorBackground could
// return while an Amazon ECS task start was still moving through its
// PROVISIONING→RUNNING lifecycle, and the next test replaced the control-plane
// stores it was reading. That surfaced as four data races in
// TestSchedulerStopsTheUnhealthyTaskOnceItsReplacementIsInService on a CI
// runner, and never on a developer machine, because the window is scheduling
// latency.
//
// This holds the barrier to both lifecycles directly, so the guarantee does
// not depend on a timing window reopening to be noticed.
func TestAwaitSimulatorBackgroundDrainsServerLifecycleWork(t *testing.T) {
	srv, err := sim.NewServer(sim.Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new aws sim server: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	var finished atomic.Bool
	started := make(chan struct{})
	run, ok := simHandoff(func() {
		close(started)
		// Long enough that a barrier which does not count this work
		// returns first, short enough not to slow the suite if it does.
		time.Sleep(250 * time.Millisecond)
		finished.Store(true)
	})
	if !ok {
		t.Fatal("work offered outside a drain was refused")
	}
	srv.StartBackground(func(context.Context) { run() })

	// The work must be running before the drain, or the drain would be
	// entitled to find nothing and the case would prove nothing.
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the background worker never started")
	}

	AwaitSimulatorBackground()

	if !finished.Load() {
		t.Fatal("AwaitSimulatorBackground returned while work on the server's " +
			"lifecycle was still running; a test would now replace the stores it reads")
	}
}

// The drain must also not admit work once it has begun, or a reconciliation
// that requests another one would keep it waiting forever — the reason simGo
// drops rather than queues. simHandoff follows the same rule, and this pins it
// so the two cannot diverge.
func TestSimHandoffRefusesWorkOnceTheDrainHasBegun(t *testing.T) {
	// Drain first so the barrier is quiescent, then observe what a drain in
	// progress does with newly offered work.
	AwaitSimulatorBackground()

	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Offer work from inside a drain: AwaitSimulatorBackground holds the
		// draining flag for the length of its call, so this runs under it.
		simDraining.Store(true)
		defer simDraining.Store(false)
		if run, ok := simHandoff(func() { ran.Store(true) }); ok {
			run()
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("offering work during a drain blocked")
	}
	if ran.Load() {
		t.Fatal("simHandoff admitted work offered during a drain; the drain it is " +
			"feeding would never empty")
	}
}

// A watch is armed long before its event arrives, and a drain has to treat the
// two halves differently: waiting on a container that has not exited would
// hang every test that drains beside a running task, while returning during
// the work that follows the exit is the race this barrier exists to prevent.
func TestAwaitSimulatorBackgroundDetachesAWatchStillWaiting(t *testing.T) {
	AwaitSimulatorBackground()

	event := make(chan struct{})
	var ran atomic.Bool
	watch := simWatchThen(func() { <-event }, func() { ran.Store(true) })
	if watch == nil {
		t.Fatal("a watch armed outside a drain was refused")
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		AwaitSimulatorBackground()
	}()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("AwaitSimulatorBackground waited on a watch whose event never arrived")
	}

	// The event arriving after the drain must not reach stores the next test owns.
	close(event)
	select {
	case <-watch.done:
	case <-time.After(10 * time.Second):
		t.Fatal("a detached watch's goroutine did not return once its event arrived")
	}
	if ran.Load() {
		t.Fatal("a watch detached by a drain ran its work when its event arrived")
	}
}

func TestAwaitSimulatorBackgroundWaitsForAWatchWhoseEventArrived(t *testing.T) {
	AwaitSimulatorBackground()

	started := make(chan struct{})
	var finished atomic.Bool
	_ = simWatchThen(func() {}, func() {
		close(started)
		time.Sleep(250 * time.Millisecond)
		finished.Store(true)
	})
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("a watch whose wait returned never ran its work")
	}

	AwaitSimulatorBackground()

	if !finished.Load() {
		t.Fatal("AwaitSimulatorBackground returned while a watch's work was still running")
	}
}

// Work handed to a lifecycle is counted before that lifecycle's goroutine runs.
// Counting it from inside the goroutine left a window in which a drain saw
// nothing and returned, and the goroutine then read stores the next test had
// built; a CI race run found it. Here the handed-off work is deliberately not
// started, so a barrier that counts only running work would return at once.
func TestAwaitSimulatorBackgroundWaitsForHandedOffWorkNotYetStarted(t *testing.T) {
	AwaitSimulatorBackground()

	var ran atomic.Bool
	run, ok := simHandoff(func() { ran.Store(true) })
	if !ok {
		t.Fatal("work offered outside a drain was refused")
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		AwaitSimulatorBackground()
	}()
	select {
	case <-drained:
		t.Fatal("AwaitSimulatorBackground returned while handed-off work had not started; " +
			"its goroutine would now read stores the next test owns")
	case <-time.After(250 * time.Millisecond):
	}

	// The goroutine finally runs, inside the drain: its work is dropped and the
	// drain can complete.
	run()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("the drain did not complete once the handed-off work ran")
	}
	if ran.Load() {
		t.Fatal("handed-off work that only started during a drain ran against the stores")
	}
}
