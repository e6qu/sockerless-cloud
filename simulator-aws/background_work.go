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

// simJoinedGo runs f in a goroutine the caller waits for, and never drops it.
//
// simGo drops what it is handed once a drain has begun, which is what makes the
// barrier a barrier: the work it drops is work that outlives the request that
// asked for it, and admitting more of it would keep the drain waiting forever.
// A goroutine the caller joins is the opposite case. Nothing outlives anything
// — the call does not return until the goroutine is done — and dropping it does
// not shorten the drain, it hangs the caller, whose own goroutine the drain is
// already waiting on. A Step Functions Map whose fan-out was dropped left its
// map run RUNNING for good; the Lambda Runtime API sidecar would have accepted
// no connection at all.
//
// It is still counted, so a drain waits for it like any other work.
func simJoinedGo(f func()) {
	simBackgroundWG.Add(1)
	simBackgroundStarted.Add(1)
	go func() {
		defer simBackgroundWG.Done()
		f()
	}()
}

// simHandoff counts work at the moment it is handed to another lifecycle.
//
// A simulator has two ways to run work off the request path, and only one of
// them was counted here at first. Work handed to the server's own lifecycle —
// Server.StartBackground, which exists so orderly shutdown drains it before
// SQLite closes — runs on a goroutine this package never saw, so a drain
// returned while an Amazon ECS task start was still moving through its
// PROVISIONING→RUNNING lifecycle, and the next test replaced the stores it was
// reading.
//
// Counting it from inside that goroutine closed most of the gap but not all of
// it. The count began when the goroutine ran, and a goroutine that has been
// created has not necessarily run: a drain in the moment between the two saw
// nothing, returned, and the next test replaced the stores the task start then
// read. The race job caught exactly that on a CI runner, a task start from
// TestRunTaskReportsContainerTaskImageSizingAndDefaultGroup reading ecsTasks
// while TestRunTaskKeepsTheGroupTheRequestNamed rebuilt it. The count is
// therefore taken here, before the work is handed over, the way simAfterFunc
// counts a timer when it is armed rather than when it fires.
//
// Work offered once a drain has begun is refused (ok is false, and the caller
// must not hand it over), and work handed over earlier that only starts during a
// drain is dropped: either way the drain is a barrier, and work it has not let
// in must not reach stores the next test will own. The two lifecycles are both
// wanted and are not alternatives — the server's drains before the database
// closes, this one before a test swaps the stores — so work registers with both.
func simHandoff(f func()) (run func(), ok bool) {
	if simDraining.Load() {
		return nil, false
	}
	simBackgroundWG.Add(1)
	simBackgroundStarted.Add(1)
	return func() {
		defer simBackgroundWG.Done()
		if simDraining.Load() {
			return
		}
		f()
	}, true
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

// Work that waits on something outside the simulator is still work.
//
// An Amazon ECS task's containers are watched by a goroutine each: it blocks
// until its container exits and then moves the task to STOPPED, which reads
// and writes the control-plane stores. Those goroutines were bare, so nothing
// counted them. A test that started a real task finished, the next test
// replaced the stores, and the watcher — whose busybox container exited a
// moment later — wrote into stores that belonged to that next test. The race
// detector reported it on a CI runner as TestListTasksOmitsTasksThatAgedOut
// building its simulator while a watcher from the test before it updated
// ecsTasks.
//
// Counting the whole goroutine is not the repair: its wait lasts as long as
// the container runs, and a drain would then wait for a task nobody stops. A
// watch is instead handled the way a pending timer is. It is counted when it
// is armed. Once its event arrives and its work has begun, a drain waits for
// that work like any other. A drain that reaches it while it is still waiting
// detaches it, and its work then never runs: the stores it would have written
// belong to a test that is over.
var simPendingWatches sync.Map // *simWatch -> struct{}

type simWatch struct {
	mu       sync.Mutex
	running  bool
	detached bool
	release  func()
	// done closes when the watch's goroutine returns, whether its work ran or
	// a drain detached it.
	done chan struct{}
}

// simWatchThen runs wait on its own goroutine, and f after wait returns. It
// returns nil when a drain refused to arm it.
func simWatchThen(wait func(), f func()) *simWatch {
	// Refused during a drain for the reason simAfterFunc is: work armed after
	// the barrier began is what the barrier exists to keep out.
	if simDraining.Load() {
		return nil
	}
	simBackgroundWG.Add(1)
	simBackgroundStarted.Add(1)
	watch := &simWatch{done: make(chan struct{})}
	var once sync.Once
	watch.release = func() {
		once.Do(func() {
			simPendingWatches.Delete(watch)
			simBackgroundWG.Done()
		})
	}
	simPendingWatches.Store(watch, struct{}{})
	go func() {
		defer close(watch.done)
		wait()
		watch.mu.Lock()
		if watch.detached {
			watch.mu.Unlock()
			return
		}
		watch.running = true
		watch.mu.Unlock()
		defer watch.release()
		f()
	}()
	return watch
}

// detach releases a watch whose event has not arrived, so its work never runs.
// A watch already running its work is left to finish, and the drain waits.
func (w *simWatch) detach() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running || w.detached {
		return
	}
	w.detached = true
	w.release()
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
		simPendingWatches.Range(func(key, _ any) bool {
			if watch, ok := key.(*simWatch); ok {
				watch.detach()
			}
			return true
		})
		simBackgroundWG.Wait()
		if simBackgroundStarted.Load() == before {
			return
		}
	}
}
