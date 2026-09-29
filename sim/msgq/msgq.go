// Package msgq is the leased message queue the simulators' queueing services
// share: Amazon SQS queues, Cloud Pub/Sub subscriptions, Azure Service Bus
// queues and subscriptions, and Azure Queue Storage queues.
//
// A Queue is plain data that serializes with its owner's record, so a cloud
// keeps it inside the row it already persists through sim.Store and mutates it
// inside that store's Update, which makes every operation atomic with the rest
// of the row. Each cloud builds the Policy from its own resource configuration
// on every call, spells receipts its own way, and renders messages in its own
// wire format.
//
// Operations that remove messages for somewhere else — dead-lettering and
// expiry — return them instead of calling out, because the destination is
// usually a row of the same store, which cannot be written from inside that
// store's Update.
package msgq

import (
	"time"

	"github.com/google/uuid"
)

// Message is one stored message. Times are Unix milliseconds.
type Message[P any] struct {
	ID      string `json:"id"`
	Seq     uint64 `json:"seq"`
	Payload P      `json:"payload"`
	// EnqueuedAt is when the queue accepted the message.
	EnqueuedAt int64 `json:"enqueuedAt"`
	// AvailableAt is when the message can next be received: the end of its
	// delivery delay, its current lease, or its redelivery backoff.
	AvailableAt int64 `json:"availableAt"`
	// DelayedUntil is the end of the initial delivery delay.
	DelayedUntil int64 `json:"delayedUntil,omitempty"`
	// ExpiresAt is the message's own time to live; zero never expires.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// Leased is true from a receive until the lease is settled or abandoned.
	// A lease that runs out leaves it true: the receipt it handed out may
	// still be honoured, depending on Policy.ReceiptOutlivesLease.
	Leased           bool   `json:"leased,omitempty"`
	Receipt          string `json:"receipt,omitempty"`
	Deliveries       int    `json:"deliveries,omitempty"`
	FirstDeliveredAt int64  `json:"firstDeliveredAt,omitempty"`
	Group            string `json:"group,omitempty"`
	DedupID          string `json:"dedupId,omitempty"`
}

// Held reports whether the message is under a live lease at now.
func (m Message[P]) Held(now time.Time) bool {
	return m.Leased && m.AvailableAt > ms(now)
}

// Delayed reports whether the message is still inside its initial delay.
func (m Message[P]) Delayed(now time.Time) bool {
	return m.DelayedUntil > ms(now) && m.Deliveries == 0
}

// DedupRecord remembers an accepted message for the deduplication window, so
// a resend inside the window answers with the original identity even after
// the original was settled.
type DedupRecord struct {
	ID        string `json:"id"`
	Seq       uint64 `json:"seq"`
	ExpiresAt int64  `json:"expiresAt"`
}

// Dedup holds the deduplication records of a queue, or of a topic that
// deduplicates before it fans out.
type Dedup map[string]DedupRecord

// Lookup drops the records expired at now and returns the live one for key.
func (d Dedup) Lookup(key string, now time.Time) (DedupRecord, bool) {
	n := ms(now)
	for k, rec := range d {
		if rec.ExpiresAt <= n {
			delete(d, k)
		}
	}
	rec, ok := d[key]
	return rec, ok
}

// Remember records key for window from now.
func (d *Dedup) Remember(key, id string, seq uint64, window time.Duration, now time.Time) {
	if *d == nil {
		*d = Dedup{}
	}
	(*d)[key] = DedupRecord{ID: id, Seq: seq, ExpiresAt: ms(now) + window.Milliseconds()}
}

// Queue holds a queue's messages in enqueue order.
type Queue[P any] struct {
	Messages []Message[P] `json:"messages,omitempty"`
	Dedup    Dedup        `json:"dedup,omitempty"`
	NextSeq  uint64       `json:"nextSeq,omitempty"`
}

// Policy is the queue configuration an operation honours.
type Policy struct {
	// MaxDeliveries dead-letters a message instead of delivering it once more
	// than this many times; zero delivers without limit.
	MaxDeliveries int
	// Retention drops a message this long after it was enqueued; zero keeps
	// it until it is settled.
	Retention time.Duration
	// DedupWindow is how long a deduplication key suppresses a resend.
	DedupWindow time.Duration
	// Ordered delivers a group's messages in order: while one of them is
	// unavailable, none after it is delivered. Messages without a group are
	// unordered.
	Ordered bool
	// ReceiptOutlivesLease keeps a receipt valid after its lease runs out,
	// until the message is received again.
	ReceiptOutlivesLease bool
	// Backoff delays redelivery after a lease runs out or is abandoned, given
	// the deliveries so far; nil redelivers at once.
	Backoff func(deliveries int) time.Duration
}

func (p Policy) backoff(deliveries int) int64 {
	if p.Backoff == nil {
		return 0
	}
	return p.Backoff(deliveries).Milliseconds()
}

// EnqueueOpts are the per-message options of Enqueue.
type EnqueueOpts struct {
	// ID is the message id; empty assigns a random UUID.
	ID    string
	Delay time.Duration
	// TTL expires the message this long after it is enqueued; zero leaves it
	// to the queue's retention.
	TTL time.Duration
	// DedupKey suppresses the message when a live record holds the same key;
	// empty never deduplicates.
	DedupKey string
	DedupID  string
	Group    string
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// Enqueue appends a message. When opts.DedupKey names a live deduplication
// record it stores nothing and returns the original's identity with dup true.
func (q *Queue[P]) Enqueue(payload P, opts EnqueueOpts, pol Policy, now time.Time) (Message[P], bool) {
	n := ms(now)
	if rec, ok := q.Dedup.Lookup(opts.DedupKey, now); ok && opts.DedupKey != "" && pol.DedupWindow > 0 {
		return Message[P]{ID: rec.ID, Seq: rec.Seq, Payload: payload, DedupID: opts.DedupID, Group: opts.Group}, true
	}
	q.NextSeq++
	m := Message[P]{
		ID:          opts.ID,
		Seq:         q.NextSeq,
		Payload:     payload,
		EnqueuedAt:  n,
		AvailableAt: n + opts.Delay.Milliseconds(),
		Group:       opts.Group,
		DedupID:     opts.DedupID,
	}
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if opts.Delay > 0 {
		m.DelayedUntil = m.AvailableAt
	}
	if opts.TTL > 0 {
		m.ExpiresAt = n + opts.TTL.Milliseconds()
	}
	q.Messages = append(q.Messages, m)
	if opts.DedupKey != "" && pol.DedupWindow > 0 {
		q.Dedup.Remember(opts.DedupKey, m.ID, m.Seq, pol.DedupWindow, now)
	}
	return m, false
}

// Expire removes and returns the messages past their retention or time to
// live.
func (q *Queue[P]) Expire(pol Policy, now time.Time) []Message[P] {
	n := ms(now)
	var expired []Message[P]
	kept := q.Messages[:0]
	for _, m := range q.Messages {
		if (pol.Retention > 0 && n-m.EnqueuedAt >= pol.Retention.Milliseconds()) || (m.ExpiresAt > 0 && m.ExpiresAt <= n) {
			expired = append(expired, m)
			continue
		}
		kept = append(kept, m)
	}
	q.Messages = kept
	return expired
}

// availableAt is when m can next be received, counting the redelivery
// backoff that follows a lease running out.
func (q *Queue[P]) availableAt(m Message[P], pol Policy) int64 {
	if m.Leased {
		return m.AvailableAt + pol.backoff(m.Deliveries)
	}
	return m.AvailableAt
}

// Available reports whether m can be received at now.
func (q *Queue[P]) Available(m Message[P], pol Policy, now time.Time) bool {
	return q.availableAt(m, pol) <= ms(now)
}

// ReceiveOpts are the options of Receive.
type ReceiveOpts[P any] struct {
	Max   int
	Lease time.Duration
	// Receipt spells the receipt of each delivery; nil uses a random UUID.
	Receipt func(Message[P]) string
	// Accept skips a message it reports false for, leaving it in place.
	Accept func(Message[P]) bool
}

// Received is what one Receive did.
type Received[P any] struct {
	Leased []Message[P]
	// DeadLettered left the queue because delivering them again would exceed
	// Policy.MaxDeliveries; each carries the count it reached.
	DeadLettered []Message[P]
	Expired      []Message[P]
}

// Receive leases up to opts.Max available messages in enqueue order. With
// Policy.Ordered, a group whose earliest message is unavailable is skipped
// entirely, and once a group has been skipped no later message of it is
// taken in the same call.
func (q *Queue[P]) Receive(opts ReceiveOpts[P], pol Policy, now time.Time) Received[P] {
	var out Received[P]
	out.Expired = q.Expire(pol, now)
	n := ms(now)
	blocked := map[string]bool{}
	picked := map[string]bool{}
	kept := q.Messages[:0]
	for _, m := range q.Messages {
		ordered := pol.Ordered && m.Group != ""
		if q.availableAt(m, pol) > n {
			if ordered {
				blocked[m.Group] = true
			}
			kept = append(kept, m)
			continue
		}
		if len(out.Leased) >= opts.Max || (ordered && blocked[m.Group] && !picked[m.Group]) ||
			(opts.Accept != nil && !opts.Accept(m)) {
			if ordered {
				blocked[m.Group] = true
			}
			kept = append(kept, m)
			continue
		}
		m.Deliveries++
		if m.FirstDeliveredAt == 0 {
			m.FirstDeliveredAt = n
		}
		if pol.MaxDeliveries > 0 && m.Deliveries > pol.MaxDeliveries {
			m.Deliveries--
			out.DeadLettered = append(out.DeadLettered, m)
			continue
		}
		m.Leased = true
		m.AvailableAt = n + opts.Lease.Milliseconds()
		if opts.Receipt != nil {
			m.Receipt = opts.Receipt(m)
		} else {
			m.Receipt = uuid.NewString()
		}
		kept = append(kept, m)
		out.Leased = append(out.Leased, m)
		if ordered {
			picked[m.Group] = true
		}
	}
	q.Messages = kept
	return out
}

// index returns the position of the message a receipt names, honouring
// Policy.ReceiptOutlivesLease, or -1.
func (q *Queue[P]) index(receipt string, pol Policy, now time.Time) int {
	if receipt == "" {
		return -1
	}
	for i := range q.Messages {
		m := q.Messages[i]
		if m.Receipt != receipt {
			continue
		}
		if !pol.ReceiptOutlivesLease && !m.Held(now) {
			return -1
		}
		return i
	}
	return -1
}

// ByReceipt returns the stored message a receipt names, for a caller that
// changes it in place, or nil.
func (q *Queue[P]) ByReceipt(receipt string, pol Policy, now time.Time) *Message[P] {
	if i := q.index(receipt, pol, now); i >= 0 {
		return &q.Messages[i]
	}
	return nil
}

// ByID returns the stored message with the given id, or nil.
func (q *Queue[P]) ByID(id string) *Message[P] {
	for i := range q.Messages {
		if q.Messages[i].ID == id {
			return &q.Messages[i]
		}
	}
	return nil
}

// Extend moves the lease a receipt names to end d from now. found reports
// whether the receipt names a message; held whether its lease was still live.
// Only a live lease is moved.
func (q *Queue[P]) Extend(receipt string, d time.Duration, pol Policy, now time.Time) (m Message[P], found, held bool) {
	i := q.index(receipt, pol, now)
	if i < 0 {
		return m, false, false
	}
	held = q.Messages[i].Held(now)
	if held {
		q.Messages[i].AvailableAt = ms(now) + d.Milliseconds()
	}
	return q.Messages[i], true, held
}

// Settle removes the message a receipt names.
func (q *Queue[P]) Settle(receipt string, pol Policy, now time.Time) (Message[P], bool) {
	i := q.index(receipt, pol, now)
	if i < 0 {
		return Message[P]{}, false
	}
	return q.removeAt(i), true
}

// Abandon ends the lease a receipt names: the message becomes available again
// after the policy's backoff, and the receipt stops naming it.
func (q *Queue[P]) Abandon(receipt string, pol Policy, now time.Time) (Message[P], bool) {
	i := q.index(receipt, pol, now)
	if i < 0 {
		return Message[P]{}, false
	}
	m := &q.Messages[i]
	m.Leased = false
	m.Receipt = ""
	m.AvailableAt = ms(now) + pol.backoff(m.Deliveries)
	return *m, true
}

// Exhausted removes and returns the available messages that have been
// delivered Policy.MaxDeliveries times, which a service dead-letters once the
// last delivery's lease runs out rather than at the next receive.
func (q *Queue[P]) Exhausted(pol Policy, now time.Time) []Message[P] {
	if pol.MaxDeliveries <= 0 {
		return nil
	}
	return q.Remove(func(m Message[P]) bool {
		return m.Deliveries >= pol.MaxDeliveries && q.Available(m, pol, now)
	})
}

// Release ends the lease a receipt names without counting the delivery, for
// a message the receiver gave back unprocessed.
func (q *Queue[P]) Release(receipt string, pol Policy, now time.Time) (Message[P], bool) {
	i := q.index(receipt, pol, now)
	if i < 0 {
		return Message[P]{}, false
	}
	m := &q.Messages[i]
	m.Leased = false
	m.Receipt = ""
	m.AvailableAt = ms(now)
	if m.Deliveries > 0 {
		m.Deliveries--
	}
	return *m, true
}

// DeadLetter removes and returns the message a receipt names, for the caller
// to forward to its dead-letter destination.
func (q *Queue[P]) DeadLetter(receipt string, pol Policy, now time.Time) (Message[P], bool) {
	return q.Settle(receipt, pol, now)
}

// Peek returns up to n messages with a sequence number of at least fromSeq,
// in order, without leasing them. availableOnly leaves out delayed and leased
// messages.
func (q *Queue[P]) Peek(fromSeq uint64, n int, availableOnly bool, pol Policy, now time.Time) []Message[P] {
	var out []Message[P]
	for _, m := range q.Messages {
		if len(out) >= n {
			break
		}
		if m.Seq < fromSeq || (availableOnly && !q.Available(m, pol, now)) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Remove deletes and returns every message drop reports true for.
func (q *Queue[P]) Remove(drop func(Message[P]) bool) []Message[P] {
	var removed []Message[P]
	kept := q.Messages[:0]
	for _, m := range q.Messages {
		if drop(m) {
			removed = append(removed, m)
			continue
		}
		kept = append(kept, m)
	}
	q.Messages = kept
	return removed
}

// Purge removes every message and returns how many it removed. The
// deduplication records survive, as they outlive settlement too.
func (q *Queue[P]) Purge() int {
	n := len(q.Messages)
	q.Messages = nil
	return n
}

// Counts are a queue's messages by state.
type Counts struct {
	Available, Held, Delayed int
}

// Count classifies the unexpired messages at now.
func (q *Queue[P]) Count(pol Policy, now time.Time) Counts {
	n := ms(now)
	var c Counts
	for _, m := range q.Messages {
		if (pol.Retention > 0 && n-m.EnqueuedAt >= pol.Retention.Milliseconds()) || (m.ExpiresAt > 0 && m.ExpiresAt <= n) {
			continue
		}
		switch {
		case m.Delayed(now):
			c.Delayed++
		case q.Available(m, pol, now):
			c.Available++
		default:
			c.Held++
		}
	}
	return c
}

func (q *Queue[P]) removeAt(i int) Message[P] {
	m := q.Messages[i]
	q.Messages = append(q.Messages[:i], q.Messages[i+1:]...)
	return m
}
