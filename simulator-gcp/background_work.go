package main

import (
	"sync"
	"sync/atomic"
)

// Asynchronous simulator work is counted, so a test can wait for it.
//
// A simulator does real work after the call that requested it returns: a
// backup captures a database volume, a restore clones one back. Each of those
// would otherwise be a bare goroutine that nothing tracks, and in a test
// binary the consequence is not theoretical — the goroutine keeps reading
// package-level stores after the test that started it has finished, and the
// next test replaces those stores underneath it.
//
// simGo is the only thing that needs to change at a call site: `go f()`
// becomes `simGo(f)`. A test that replaces the stores calls
// AwaitSimulatorBackground first, and its work is then done with the old
// stores before the new ones appear.
//
// This is not a substitute for a worker's own shutdown. A long-lived loop
// still belongs to the server that started it; simGo is for the one-shot work
// that finishes on its own.
var (
	simBackgroundWG      sync.WaitGroup
	simBackgroundStarted atomic.Uint64
	// simDraining is set for the duration of AwaitSimulatorBackground. While
	// it is set, work requested is dropped rather than started: a drain is a
	// barrier, and work asked for after it begins is exactly what the barrier
	// exists to keep out of the next test. Nothing in production drains.
	simDraining atomic.Bool
)

// simGo runs f in a goroutine that AwaitSimulatorBackground can wait for.
// Once a drain has begun the work is dropped rather than started — chained
// work admitted mid-drain leaves the barrier waiting on a group that is never
// empty.
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

// AwaitSimulatorBackground blocks until the simulator has no asynchronous
// work left. It drains to quiescence rather than waiting once, because this
// work can chain: a capture that finishes may settle state that requests more
// work, registered after the first wait has already returned.
func AwaitSimulatorBackground() {
	simDraining.Store(true)
	defer simDraining.Store(false)
	for range 100 {
		before := simBackgroundStarted.Load()
		simBackgroundWG.Wait()
		if simBackgroundStarted.Load() == before {
			return
		}
	}
}
