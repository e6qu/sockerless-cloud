// Package delivery runs the at-least-once push deliveries the clouds make on
// a caller's behalf — an asynchronous function invocation, a webhook POST, a
// rule target — with the retry policy, event age limit and dead-letter hand-off
// each cloud documents. Every pending delivery is persisted with its next
// attempt time, so a restarted simulator resumes it where it stopped.
package delivery

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// Outcome is the result of one attempt.
type Outcome struct {
	Err error
	// Retry marks a failure the policy may retry; a failure without it is
	// final whatever attempts remain.
	Retry bool
	// Status is the receiver's HTTP status, zero when there was none.
	Status int
}

func Delivered() Outcome                   { return Outcome{} }
func Retryable(err error) Outcome          { return Outcome{Err: err, Retry: true} }
func Permanent(err error) Outcome          { return Outcome{Err: err} }
func (o Outcome) OK() bool                 { return o.Err == nil }
func (o Outcome) WithStatus(s int) Outcome { o.Status = s; return o }
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Policy bounds how long and how often a delivery is retried.
type Policy struct {
	// MaxAttempts counts the first attempt; zero is unbounded.
	MaxAttempts int
	// MaxAge is measured from submission and checked before every attempt;
	// zero is unbounded.
	MaxAge time.Duration
	// Backoff is the wait before retry n (1 for the first retry).
	Backoff func(retry int) time.Duration
	// MinWait is a floor the failed attempt's answer sets on that wait, for a
	// cloud that waits longer after some answers than its schedule says.
	MinWait func(last Outcome) time.Duration
}

// Reason says how a delivery ended.
type Reason string

const (
	Succeeded         Reason = "Succeeded"
	AttemptsExhausted Reason = "AttemptsExhausted"
	AgeExceeded       Reason = "AgeExceeded"
	Rejected          Reason = "Rejected"
)

// Item is a pending delivery as persisted.
type Item[T any] struct {
	ID            string    `json:"id"`
	Payload       T         `json:"payload"`
	EnqueuedAt    time.Time `json:"enqueuedAt"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	Attempts      int       `json:"attempts"`
	LastError     string    `json:"lastError,omitempty"`
	LastStatus    int       `json:"lastStatus,omitempty"`
	LastAttemptAt time.Time `json:"lastAttemptAt"`
}

// Handler is what one kind of delivery does.
type Handler[T any] struct {
	Policy func(T) Policy
	// Attempt makes one delivery; it may change item.Payload, which is
	// persisted with the item for the next attempt and for Finish.
	Attempt func(ctx context.Context, item *Item[T]) Outcome
	// Finish runs once, when the delivery succeeded or ended without success
	// — where a cloud hands the event to its dead-letter target.
	Finish func(item Item[T], reason Reason)
}

// Dispatcher runs the deliveries of one kind.
type Dispatcher[T any] struct {
	store   sim.Store[Item[T]]
	handler Handler[T]
	now     func() time.Time

	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	stopped bool
	pending map[string]*bg.Timer
	running sync.WaitGroup
}

// New starts a dispatcher whose attempts stop, and whose running attempts are
// awaited, when srv stops its background work. Pending deliveries in store
// wait for Resume.
func New[T any](srv kvstore.Background, name string, store sim.Store[Item[T]], handler Handler[T]) *Dispatcher[T] {
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher[T]{
		store:   store,
		handler: handler,
		now:     time.Now,
		ctx:     ctx,
		cancel:  cancel,
		pending: map[string]*bg.Timer{},
	}
	srv.StartBackground(name, func(serverCtx context.Context) {
		<-serverCtx.Done()
		d.stop()
	})
	return d
}

// Submit persists a delivery and makes its first attempt at once.
func (d *Dispatcher[T]) Submit(id string, payload T) {
	d.store.Put(id, Item[T]{ID: id, Payload: payload, EnqueuedAt: d.now().UTC()})
	d.schedule(id, time.Time{})
}

// SubmitAttempted persists a delivery and makes its first attempt on the
// caller's goroutine, for a delivery the cloud makes before the call that
// caused it returns; retries run in the background.
func (d *Dispatcher[T]) SubmitAttempted(id string, payload T) {
	d.store.Put(id, Item[T]{ID: id, Payload: payload, EnqueuedAt: d.now().UTC()})
	d.run(id)
}

// Resume schedules every persisted delivery at its recorded next attempt.
func (d *Dispatcher[T]) Resume() {
	for _, item := range d.store.List() {
		d.schedule(item.ID, item.NextAttemptAt)
	}
}

func (d *Dispatcher[T]) schedule(id string, at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if _, ok := d.pending[id]; ok {
		return
	}
	var wait time.Duration
	if !at.IsZero() {
		wait = at.Sub(d.now())
	}
	d.pending[id] = bg.AfterFunc(max(wait, 0), func() { d.run(id) })
}

func (d *Dispatcher[T]) run(id string) {
	d.mu.Lock()
	delete(d.pending, id)
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.running.Add(1)
	d.mu.Unlock()
	defer d.running.Done()

	item, ok := d.store.Get(id)
	if !ok {
		return
	}
	policy := d.handler.Policy(item.Payload)
	if policy.MaxAge > 0 && d.now().Sub(item.EnqueuedAt) > policy.MaxAge {
		d.finish(item, AgeExceeded)
		return
	}
	item.Attempts++
	item.NextAttemptAt = time.Time{}
	item.LastAttemptAt = d.now().UTC()
	d.store.Put(id, item)

	outcome := d.handler.Attempt(d.ctx, &item)
	if !outcome.OK() && errors.Is(d.ctx.Err(), context.Canceled) {
		item.Attempts--
		d.store.Put(id, item)
		return
	}
	item.LastError = errorText(outcome.Err)
	item.LastStatus = outcome.Status
	switch {
	case outcome.OK():
		d.finish(item, Succeeded)
		return
	case !outcome.Retry:
		d.finish(item, Rejected)
		return
	case policy.MaxAttempts > 0 && item.Attempts >= policy.MaxAttempts:
		d.finish(item, AttemptsExhausted)
		return
	}
	var wait time.Duration
	if policy.Backoff != nil {
		wait = policy.Backoff(item.Attempts)
	}
	if policy.MinWait != nil {
		wait = max(wait, policy.MinWait(outcome))
	}
	item.NextAttemptAt = d.now().UTC().Add(wait)
	if _, still := d.store.Get(id); !still {
		return
	}
	d.store.Put(id, item)
	d.schedule(id, item.NextAttemptAt)
}

func (d *Dispatcher[T]) finish(item Item[T], reason Reason) {
	if d.handler.Finish != nil {
		d.handler.Finish(item, reason)
	}
	d.store.Delete(item.ID)
}

func (d *Dispatcher[T]) stop() {
	d.mu.Lock()
	d.stopped = true
	for id, timer := range d.pending {
		timer.Stop()
		delete(d.pending, id)
	}
	d.mu.Unlock()
	d.cancel()
	d.running.Wait()
}
