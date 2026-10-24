package msgq

import (
	"context"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// NextChange returns the earliest instant after now at which a message
// changes state without anyone acting on it: its delivery delay or lease
// ends, or the redelivery backoff after a lapsed lease ends.
func (q *Queue[P]) NextChange(pol Policy, now time.Time) (time.Time, bool) {
	n := ms(now)
	var next int64
	consider := func(at int64) {
		if at > n && (next == 0 || at < next) {
			next = at
		}
	}
	for _, m := range q.Messages {
		consider(m.AvailableAt)
		if m.Leased {
			consider(q.availableAt(m, pol))
		}
	}
	if next == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(next), true
}

// Waiter wakes the receivers of a cloud's queues, each named by a key.
//
// A queue's receivable set grows in two ways. A client acts — a send, an
// abandoned or released lease, a deleted message that unblocks an ordered
// group — and the cloud calls Signal for the queue. Or time passes — a delay,
// a lease or a backoff ends — and Waiter wakes the receivers itself at the
// instant the cloud's next hook names, which the cloud computes with
// Queue.NextChange from the queue as stored.
//
// Poll is the long-poll receive: it calls the cloud's receive until that
// takes something or the deadline passes, sleeping in between until the queue
// was signalled or its next change arrives. Arm keeps a timer at the next
// change for a cloud that pushes messages to its receivers instead of being
// polled, so they hear of a message that became available on its own; it
// re-arms itself from the store, so a simulator that restarts arms each
// stored queue once.
type Waiter struct {
	changes signals.Keyed[string]
	next    func(key string, now time.Time) (time.Time, bool)

	mu    sync.Mutex
	onDue func(key string)
	due   map[string]armed
}

type armed struct {
	at    time.Time
	timer *bg.Timer
}

// NewWaiter returns a Waiter whose next hook reports when the queue key names
// next changes on its own, false when nothing in it will.
func NewWaiter(next func(key string, now time.Time) (time.Time, bool)) *Waiter {
	return &Waiter{next: next, due: map[string]armed{}}
}

// OnDue sets the hook an armed timer runs, after it signals the queue, for a
// cloud that hands newly available messages to its receivers.
func (w *Waiter) OnDue(f func(key string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onDue = f
}

// Changed returns a channel the next Signal for key closes. Take it before
// reading the queue, as package signals requires.
func (w *Waiter) Changed(key string) <-chan struct{} { return w.changes.Changed(key) }

// Any returns a channel the next Signal for any queue closes.
func (w *Waiter) Any() <-chan struct{} { return w.changes.Any() }

// Signal wakes the receivers of the queue key names: its receivable set may
// have grown.
func (w *Waiter) Signal(key string) { w.changes.Announce(key) }

// Wait sleeps until changed closes, the queue's next change arrives, the
// deadline passes — a zero deadline never does — or ctx ends, whose error it
// then returns.
func (w *Waiter) Wait(ctx context.Context, key string, changed <-chan struct{}, deadline time.Time) error {
	return w.sleep(ctx, changed, w.earliest([]string{key}, deadline))
}

// Poll calls receive until it reports that it took something or deadline
// passes, waiting between calls as Wait does. It returns ctx's error when ctx
// ends first.
func (w *Waiter) Poll(ctx context.Context, key string, deadline time.Time, receive func() bool) error {
	for {
		changed := w.Changed(key)
		if receive() || !time.Now().Before(deadline) {
			return nil
		}
		if err := w.Wait(ctx, key, changed, deadline); err != nil {
			return err
		}
	}
}

// WaitAny sleeps until any queue is signalled after changed was taken from
// Any, the earliest next change of the queues keys names arrives, or ctx
// ends, whose error it then returns. A loop that sweeps many queues waits
// with it.
func (w *Waiter) WaitAny(ctx context.Context, changed <-chan struct{}, keys []string) error {
	return w.sleep(ctx, changed, w.earliest(keys, time.Time{}))
}

// earliest is the soonest of bound and the next changes of keys; zero when
// there is none.
func (w *Waiter) earliest(keys []string, bound time.Time) time.Time {
	now := time.Now()
	wake := bound
	for _, key := range keys {
		if next, ok := w.next(key, now); ok && (wake.IsZero() || next.Before(wake)) {
			wake = next
		}
	}
	return wake
}

func (w *Waiter) sleep(ctx context.Context, changed <-chan struct{}, wake time.Time) error {
	var timeout <-chan time.Time
	if !wake.IsZero() {
		timer := time.NewTimer(time.Until(wake))
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
	case <-timeout:
	}
	return nil
}

// Arm keeps a timer at the queue's next change. A timer already due sooner
// stands; one due later is replaced. When the timer fires it signals the
// queue, runs the OnDue hook, and arms the queue's following change.
func (w *Waiter) Arm(key string) {
	now := time.Now()
	at, ok := w.next(key, now)
	if !ok {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if current, armedAlready := w.due[key]; armedAlready && current.timer.Pending() {
		if !current.at.After(at) {
			return
		}
		current.timer.Stop()
	}
	var fired *bg.Timer
	fired = bg.AfterFunc(at.Sub(now), func() {
		w.mu.Lock()
		if current, ok := w.due[key]; ok && current.timer == fired {
			delete(w.due, key)
		}
		onDue := w.onDue
		w.mu.Unlock()
		w.Signal(key)
		if onDue != nil {
			onDue(key)
		}
		w.Arm(key)
	})
	w.due[key] = armed{at: at, timer: fired}
}
