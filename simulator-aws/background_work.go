package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// Asynchronous simulator work is counted, so a test can wait for it.
//
// A simulator does real work after the call that requested it returns: a
// service reconciles, a build runs, a deployment settles, a certificate
// validates. Each of those was a bare goroutine that nothing tracked, and in a
// test binary the consequence is not theoretical — the goroutine keeps reading
// package-level stores after the test that started it has finished, and the
// next test replaces those stores underneath it. The race detector reports
// that as a write racing a read with neither in the test's own code, which is
// how the first race-detector run of this package came back with 144 of them.
//
// simGo is the only thing that needs to change at a call site: `go f()` becomes
// `simGo(f)`. A test that replaces the stores calls AwaitSimulatorBackground
// first, and its work is then done with the old stores before the new ones
// appear.
//
// This is not a substitute for a worker's own shutdown. A long-lived loop
// still belongs to the server that started it, through StartBackground and
// StopBackground; simGo is for the one-shot work that finishes on its own.
var (
	simBackgroundWG      sync.WaitGroup
	simBackgroundStarted atomic.Uint64
	// simDraining is set for the duration of AwaitSimulatorBackground. While
	// it is set, work requested is dropped rather than started: a drain is a
	// barrier, and work asked for after it begins is exactly what the barrier
	// exists to keep out of the next test.
	simDraining atomic.Bool
)

// simGo runs f in a goroutine that AwaitSimulatorBackground can wait for.
//
// Once a drain has begun the work is dropped rather than started. This is not
// an optimisation: an Amazon ECS reconciliation requests another one whenever
// it moves a task, so the work feeds itself, and a drain that keeps admitting
// it waits on a group that is never empty. On a developer machine the chain
// happened to converge and the drain returned in microseconds; under the race
// detector on a CI runner it did not, and the job was killed with a
// reconciliation still runnable after eight minutes. Bounding the drain's
// rounds could not help, because the wait inside a round never returned.
//
// Dropping it is what the barrier means rather than a shortcut around it: every
// caller of AwaitSimulatorBackground is a test replacing the stores that work
// would read, and its own comment says so. Nothing in production drains.
func simGo(f func()) {
	if simDraining.Load() {
		return
	}
	simBackgroundWG.Add(1)
	simBackgroundStarted.Add(1)
	go func() {
		defer simBackgroundWG.Done()
		f()
	}()
}

// simTracked counts work the caller has already been given a goroutine for.
//
// A simulator has two ways to run work off the request path, and only one of
// them was counted here. Work handed to the server's own lifecycle —
// Server.StartBackground, which exists so orderly shutdown drains it before
// SQLite closes — runs on a goroutine this package never saw, so a drain
// returned while an Amazon ECS task start was still moving through its
// PROVISIONING→RUNNING lifecycle, and the next test replaced the stores it was
// reading. That is the same defect the 144 original races were, arriving
// through the other door: the barrier is only as good as the work it counts.
//
// The two lifecycles are both wanted and are not alternatives — the server's
// drains before the database closes, this one before a test swaps the stores —
// so work registers with both rather than choosing.
func simTracked(f func()) {
	if simDraining.Load() {
		return
	}
	simBackgroundWG.Add(1)
	defer simBackgroundWG.Done()
	simBackgroundStarted.Add(1)
	f()
}

// Work a timer has not started yet is still work.
//
// simGo counts a goroutine from the moment it is launched, which leaves one
// gap: a reconciliation scheduled with time.AfterFunc registers nothing until
// the timer fires. Between the schedule and the fire there is no goroutine and
// no count, so a drain sees quiescence, the test replaces the stores, and the
// timer then wakes into stores that belong to the next test. That is the whole
// residue of the 144 races this package started with — three of them, all from
// the two deferred Amazon ECS service reconciliations.
//
// A pending timer is therefore registered when it is scheduled. Draining stops
// the ones that have not fired rather than waiting them out: the drain's
// meaning is "no background work may touch these stores from here on", and a
// reconciliation that has not begun satisfies that by being cancelled. Waiting
// instead would add the full steady-state window to every test that drains, for
// work whose result the next test would immediately discard.
var simPendingTimers sync.Map // *simTimer -> struct{}

type simTimer struct {
	timer    *time.Timer
	released atomic.Bool
	release  func()
}

// simAfterFunc runs f after d, counted from now rather than from the fire.
func simAfterFunc(d time.Duration, f func()) *simTimer {
	// A drain is a barrier: nothing scheduled after it begins may touch the
	// stores it is draining. Arming the timer anyway makes the barrier
	// unreachable rather than late, because the two Amazon ECS reconciliations
	// that schedule these re-arm from inside a reconciliation — so every round
	// of the drain stopped a timer, waited, and found a fresh one armed by the
	// work it had just waited for. Under the race detector that ground for over
	// five minutes and a CI job timed out on it.
	//
	// Nothing in production reaches this: AwaitSimulatorBackground is called by
	// tests, between cases, and never by a running simulator.
	if simDraining.Load() {
		return &simTimer{}
	}
	simBackgroundWG.Add(1)
	simBackgroundStarted.Add(1)
	pending := &simTimer{}
	var once sync.Once
	pending.release = func() {
		once.Do(func() {
			pending.released.Store(true)
			simPendingTimers.Delete(pending)
			simBackgroundWG.Done()
		})
	}
	// The timer field is written before the timer is published, so a drain
	// reaching it through the map cannot see it half-built. Publishing first
	// and assigning after is the obvious order and the wrong one: a drain that
	// ranges in between calls Stop on a nil timer field mid-write.
	pending.timer = time.AfterFunc(d, func() {
		defer pending.release()
		f()
	})
	simPendingTimers.Store(pending, struct{}{})
	// A timer with no delay can fire and release before the line above runs,
	// which would leave its entry in the map with nothing left to remove it.
	if pending.released.Load() {
		simPendingTimers.Delete(pending)
	}
	return pending
}

// Stop cancels the timer, reporting whether it stopped it before it fired. A
// timer that had already fired releases its own count when f returns, and one
// that a drain refused to arm has nothing to stop.
func (t *simTimer) Stop() bool {
	if t.timer == nil {
		return false
	}
	stopped := t.timer.Stop()
	if stopped {
		t.release()
	}
	return stopped
}

// AwaitSimulatorBackground blocks until the simulator has no asynchronous work
// left. It drains to quiescence rather than waiting once, because this work
// chains: a service reconciliation requests another when a task transition
// falls out of it, and that one is registered after the first wait has already
// returned. Waiting once left exactly those chained goroutines running into the
// next test.
func AwaitSimulatorBackground() {
	simDraining.Store(true)
	defer simDraining.Store(false)
	for range 100 {
		before := simBackgroundStarted.Load()
		simPendingTimers.Range(func(key, _ any) bool {
			if pending, ok := key.(*simTimer); ok {
				pending.Stop()
			}
			return true
		})
		simBackgroundWG.Wait()
		if simBackgroundStarted.Load() == before {
			return
		}
	}
}
