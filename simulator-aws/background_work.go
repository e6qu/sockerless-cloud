package main

import (
	"sync"
	"sync/atomic"
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

// AwaitSimulatorBackground blocks until the simulator has no asynchronous work
// left. It drains to quiescence rather than waiting once, because this work
// chains: a service reconciliation requests another when a task transition
// falls out of it, and that one is registered after the first wait has already
// returned. Waiting once left exactly those chained goroutines running into the
// next test.
func AwaitSimulatorBackground() {
	for range 100 {
		before := simBackgroundStarted.Load()
		simBackgroundWG.Wait()
		if simBackgroundStarted.Load() == before {
			return
		}
	}
}
