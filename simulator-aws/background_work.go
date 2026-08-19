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
)

// simGo runs f in a goroutine that AwaitSimulatorBackground can wait for.
func simGo(f func()) {
	simBackgroundWG.Add(1)
	simBackgroundStarted.Add(1)
	go func() {
		defer simBackgroundWG.Done()
		f()
	}()
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
// timer that had already fired releases its own count when f returns.
func (t *simTimer) Stop() bool {
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
