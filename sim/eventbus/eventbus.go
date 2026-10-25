// Package eventbus fans an event out to the subscriptions that match it: an
// Amazon EventBridge rule's targets, an Amazon SNS topic's subscriptions, an
// Azure Event Grid topic's event subscriptions, a Pub/Sub topic's
// subscriptions, the Eventarc triggers an audit entry reaches.
//
// A Bus publishes events of type E, published to a named source (an event
// bus, a topic, a scope), to subscriptions of type S, as deliveries of type
// P. Publish runs, in order:
//
//  1. Archive, when set, with every published event — Replay skips it, so a
//     replayed event is not archived again.
//  2. Subscriptions, for the source's candidate subscriptions.
//  3. Admits, the cloud's filter dialect, per subscription and event; a
//     subscription no event passes gets no delivery.
//  4. Batch, which bounds how many admitted events, and how many bytes of them
//     by Size, one delivery carries; without it each event is its own
//     delivery.
//  5. Transform, the cloud's input transformation, which turns one batch into
//     the delivery payload and may drop it.
//  6. Exactly one of Sink and Delivery. Sink hands the payload to a durable
//     store that owns its own retries — a Pub/Sub subscription's leased queue
//     — on the publisher's goroutine. Delivery submits it to a
//     sim/delivery Dispatcher under the subscription's retry policy (Policy:
//     maximum attempts, maximum event age, backoff), makes each attempt with
//     Attempt, reports how each delivery ended to Settled and hands one that
//     ended without success to DeadLetter.
//
// The filter dialects, input transformations, payload shapes and target calls
// stay with each cloud; the package holds the fan-out and its bookkeeping.
package eventbus

import (
	"context"
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// Limits bounds one delivery. MaxEvents of zero or one delivers each event
// alone; MaxBytes of zero is unbounded. An event larger than MaxBytes travels
// alone rather than not at all.
type Limits struct {
	MaxEvents int
	MaxBytes  int
}

// Delivery is the push half of a Bus whose deliveries a sim/delivery
// Dispatcher retries.
type Delivery[P any] struct {
	// Name names the dispatcher's background work.
	Name    string
	Store   sim.Store[delivery.Item[P]]
	Policy  func(P) delivery.Policy
	Attempt func(ctx context.Context, item *delivery.Item[P]) delivery.Outcome
	// Inline makes a delivery's first attempt on the publisher's goroutine,
	// for a cloud whose publish returns after that attempt; retries always run
	// in the background. Nil attempts every delivery in the background.
	Inline func(P) bool
	// Settled runs once per delivery with how it ended.
	Settled func(item delivery.Item[P], reason delivery.Reason)
	// DeadLetter runs after Settled for a delivery that ended without
	// success.
	DeadLetter func(item delivery.Item[P], reason delivery.Reason)
}

// Config holds a Bus's hooks.
type Config[E, S, P any] struct {
	Subscriptions func(source string) []S
	// Admits is the cloud's filter; nil admits every event.
	Admits func(sub S, event E) bool
	// Batch bounds the deliveries of a subscription; nil delivers each event
	// alone.
	Batch func(sub S) Limits
	// Size is an event's size in bytes for Limits.MaxBytes.
	Size      func(event E) int
	Transform func(sub S, events []E) (payload P, ok bool)
	Archive   func(source string, event E)
	Sink      func(sub S, payload P)
	Delivery  *Delivery[P]
}

// Result counts what one fan-out did.
type Result struct {
	// Subscriptions counts the subscriptions at least one event reached.
	Subscriptions int
	Deliveries    int
}

// Bus is a fan-out from published events to deliveries.
type Bus[E, S, P any] struct {
	cfg        Config[E, S, P]
	dispatcher *delivery.Dispatcher[P]
}

// New builds a bus. A bus with a Delivery starts its dispatcher on srv's
// background lifecycle; deliveries a previous run persisted wait for Resume.
// A bus with a Sink runs nothing in the background and takes a nil srv.
func New[E, S, P any](srv kvstore.Background, cfg Config[E, S, P]) *Bus[E, S, P] {
	if cfg.Transform == nil {
		panic("eventbus: Config.Transform is required")
	}
	if (cfg.Sink == nil) == (cfg.Delivery == nil) {
		panic("eventbus: exactly one of Config.Sink and Config.Delivery is required")
	}
	b := &Bus[E, S, P]{cfg: cfg}
	if d := cfg.Delivery; d != nil {
		if d.Store == nil || d.Policy == nil || d.Attempt == nil {
			panic(fmt.Sprintf("eventbus %s: Delivery needs Store, Policy and Attempt", d.Name))
		}
		b.dispatcher = delivery.New(srv, d.Name, d.Store, delivery.Handler[P]{
			Policy:  d.Policy,
			Attempt: d.Attempt,
			Finish: func(item delivery.Item[P], reason delivery.Reason) {
				if d.Settled != nil {
					d.Settled(item, reason)
				}
				if reason != delivery.Succeeded && d.DeadLetter != nil {
					d.DeadLetter(item, reason)
				}
			},
		})
	}
	return b
}

// Resume schedules the deliveries a previous run persisted.
func (b *Bus[E, S, P]) Resume() {
	if b.dispatcher != nil {
		b.dispatcher.Resume()
	}
}

// Publish archives events and delivers them to the source's subscriptions
// that admit them.
func (b *Bus[E, S, P]) Publish(source string, events ...E) Result {
	if b.cfg.Archive != nil {
		for _, event := range events {
			b.cfg.Archive(source, event)
		}
	}
	return b.fanOut(b.cfg.Subscriptions(source), events, true)
}

// Replay delivers events again to the source's subscriptions that only
// accepts (every one when only is nil) and that admit them, without
// archiving them.
func (b *Bus[E, S, P]) Replay(source string, events []E, only func(S) bool) Result {
	subs := b.cfg.Subscriptions(source)
	if only != nil {
		kept := subs[:0:0]
		for _, sub := range subs {
			if only(sub) {
				kept = append(kept, sub)
			}
		}
		subs = kept
	}
	return b.fanOut(subs, events, true)
}

// DeliverTo delivers events to subs as named, without the filter or the
// archive, for an event a cloud addresses to its subscriptions directly, such
// as the occurrence of a scheduled rule.
func (b *Bus[E, S, P]) DeliverTo(subs []S, events ...E) Result {
	return b.fanOut(subs, events, false)
}

// Submit delivers a payload no published event caused, such as a
// subscription confirmation, through the bus's dispatcher, returning the
// delivery's ID.
func (b *Bus[E, S, P]) Submit(payload P) string {
	if b.dispatcher == nil {
		panic("eventbus: Submit needs a Delivery")
	}
	id := sim.NewUUID()
	b.submit(id, payload)
	return id
}

func (b *Bus[E, S, P]) fanOut(subs []S, events []E, filter bool) Result {
	var result Result
	for _, sub := range subs {
		admitted := events
		if filter && b.cfg.Admits != nil {
			admitted = nil
			for _, event := range events {
				if b.cfg.Admits(sub, event) {
					admitted = append(admitted, event)
				}
			}
		}
		if len(admitted) == 0 {
			continue
		}
		result.Subscriptions++
		var limits Limits
		if b.cfg.Batch != nil {
			limits = b.cfg.Batch(sub)
		}
		for _, batch := range Batches(admitted, limits, b.cfg.Size) {
			payload, ok := b.cfg.Transform(sub, batch)
			if !ok {
				continue
			}
			result.Deliveries++
			if b.cfg.Sink != nil {
				b.cfg.Sink(sub, payload)
				continue
			}
			b.submit(sim.NewUUID(), payload)
		}
	}
	return result
}

func (b *Bus[E, S, P]) submit(id string, payload P) {
	if inline := b.cfg.Delivery.Inline; inline != nil && inline(payload) {
		b.dispatcher.SubmitAttempted(id, payload)
		return
	}
	b.dispatcher.Submit(id, payload)
}

// Batches splits events, in order, into runs no longer than limits allow.
func Batches[E any](events []E, limits Limits, size func(E) int) [][]E {
	maxEvents := max(limits.MaxEvents, 1)
	var batches [][]E
	var current []E
	bytes := 0
	for _, event := range events {
		n := 0
		if size != nil {
			n = size(event)
		}
		full := len(current) == maxEvents || (limits.MaxBytes > 0 && bytes+n > limits.MaxBytes)
		if len(current) > 0 && full {
			batches = append(batches, current)
			current, bytes = nil, 0
		}
		current = append(current, event)
		bytes += n
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}
