// Package bg counts the work a simulator runs after the request that asked for
// it has returned, so a test can drain that work before it replaces the
// package-level stores the work reads.
//
// An untracked goroutine outlives the test that started it and reads stores the
// next test has rebuilt, which the race detector reports as a race in code
// neither test owns. Every one-shot goroutine, timer and watch a simulator
// starts therefore goes through this package, and Await is the barrier a test
// calls before it rebuilds a simulator.
//
// A long-lived loop still belongs to the server that started it, through
// sim.Server.StartBackground and StopBackground; Handoff counts work handed to
// that lifecycle.
//
// Nothing in production drains: Await is a test barrier. While a drain runs,
// work requested is dropped rather than started, because work chains — an
// Amazon ECS reconciliation requests another whenever it moves a task — and a
// drain that kept admitting it would wait on a group that never empties.
package bg

import (
	"sync"
	"sync/atomic"
	"time"
)

var (
	wg       sync.WaitGroup
	started  atomic.Uint64
	draining atomic.Bool
	timers   sync.Map // *Timer -> struct{}
	watches  sync.Map // *Watch -> struct{}
)

func admit() bool {
	if draining.Load() {
		return false
	}
	wg.Add(1)
	started.Add(1)
	return true
}

// Draining reports whether a drain is in progress, for work that must decide
// before it starts anything whether Go would drop it.
func Draining() bool { return draining.Load() }

// Go runs f in a goroutine Await waits for. A drain in progress drops f.
func Go(f func()) {
	if !admit() {
		return
	}
	go func() {
		defer wg.Done()
		f()
	}()
}

// JoinedGo runs f in a goroutine the caller itself waits for, and never drops
// it: the caller does not return until f does, so dropping f would hang a
// caller the drain is already waiting on.
func JoinedGo(f func()) {
	wg.Add(1)
	started.Add(1)
	go func() {
		defer wg.Done()
		f()
	}()
}

// Handoff counts f before it is handed to another lifecycle such as
// sim.Server.StartBackground, whose goroutine may not run until after a drain
// has looked. It refuses work offered during a drain (ok is false and the
// caller must not hand it over), and run drops f when it only starts once a
// drain has begun.
func Handoff(f func()) (run func(), ok bool) {
	if !admit() {
		return nil, false
	}
	return func() {
		defer wg.Done()
		if draining.Load() {
			return
		}
		f()
	}, true
}

// Timer is work scheduled by AfterFunc, counted from when it is armed.
type Timer struct {
	timer    *time.Timer
	released atomic.Bool
	release  func()
}

// AfterFunc runs f after d. The work counts from now, not from the fire, and a
// drain cancels a timer that has not fired rather than waiting it out: the
// stores it would read belong to a test that is over.
func AfterFunc(d time.Duration, f func()) *Timer {
	// Work that re-arms itself from inside its own run would otherwise hand
	// every round of a drain a fresh timer.
	if !admit() {
		return &Timer{}
	}
	pending := &Timer{}
	var once sync.Once
	pending.release = func() {
		once.Do(func() {
			pending.released.Store(true)
			timers.Delete(pending)
			wg.Done()
		})
	}
	// Assign the timer before publishing the entry, so a drain ranging over
	// the map never calls Stop on a half-built Timer.
	pending.timer = time.AfterFunc(d, func() {
		defer pending.release()
		f()
	})
	timers.Store(pending, struct{}{})
	// A zero-delay timer can fire and release before the Store above.
	if pending.released.Load() {
		timers.Delete(pending)
	}
	return pending
}

// Stop cancels the timer, reporting whether it stopped it before it fired.
func (t *Timer) Stop() bool {
	if t.timer == nil {
		return false
	}
	stopped := t.timer.Stop()
	if stopped {
		t.release()
	}
	return stopped
}

// Watch is work that runs once an event outside the simulator arrives, such as
// a container exiting.
type Watch struct {
	mu       sync.Mutex
	running  bool
	detached bool
	release  func()
	done     chan struct{}
}

// WatchThen runs wait on its own goroutine and f after wait returns. A drain
// waits for f once it has begun, but detaches a watch still waiting, whose f
// then never runs: waiting on a container nobody stops would hang the drain.
// It returns nil when a drain refused to arm it.
func WatchThen(wait func(), f func()) *Watch {
	if !admit() {
		return nil
	}
	watch := &Watch{done: make(chan struct{})}
	var once sync.Once
	watch.release = func() {
		once.Do(func() {
			watches.Delete(watch)
			wg.Done()
		})
	}
	watches.Store(watch, struct{}{})
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

// Done closes when the watch's goroutine returns, whether f ran or a drain
// detached it.
func (w *Watch) Done() <-chan struct{} { return w.done }

func (w *Watch) detach() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running || w.detached {
		return
	}
	w.detached = true
	w.release()
}

// Await blocks until no counted work is left. It drains to quiescence rather
// than waiting once, because work finishing inside one round can request more.
func Await() {
	draining.Store(true)
	defer draining.Store(false)
	for range 100 {
		before := started.Load()
		timers.Range(func(key, _ any) bool {
			if t, ok := key.(*Timer); ok {
				t.Stop()
			}
			return true
		})
		watches.Range(func(key, _ any) bool {
			if w, ok := key.(*Watch); ok {
				w.detach()
			}
			return true
		})
		wg.Wait()
		if started.Load() == before {
			return
		}
	}
}
