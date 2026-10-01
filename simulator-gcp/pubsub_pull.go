package main

import (
	"context"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/status"
)

// psPullWait bounds how long a Pull without returnImmediately holds the call
// open on an empty subscription before it answers with no messages. Pub/Sub
// documents only that the wait is bounded; it sits below the 60-second
// deadline the client libraries give Pull, so the caller reads an empty
// response rather than DEADLINE_EXCEEDED.
const psPullWait = 10 * time.Second

// psArrivals wakes waiting Pull calls and idle StreamingPull streams. Each
// subscription name maps to a channel that psSignalSubscription closes when
// the subscription's deliverable set may have changed: a publish, a negative
// acknowledgement or ack deadline change, an acknowledgement that unblocks an
// ordering key or frees flow-control budget, a seek, or the subscription's
// deletion, detachment or update.
var psArrivals = struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}{ch: map[string]chan struct{}{}}

func psSubscriptionSignal(name string) <-chan struct{} {
	psArrivals.mu.Lock()
	defer psArrivals.mu.Unlock()
	c, ok := psArrivals.ch[name]
	if !ok {
		c = make(chan struct{})
		psArrivals.ch[name] = c
	}
	return c
}

func psSignalSubscription(name string) {
	psArrivals.mu.Lock()
	defer psArrivals.mu.Unlock()
	if c, ok := psArrivals.ch[name]; ok {
		close(c)
		delete(psArrivals.ch, name)
	}
}

// psNextQueueChange returns the earliest moment after now at which a message
// of the subscription changes state on its own: an ack deadline lapses, which
// frees flow-control budget, or the retry backoff after a lapsed or negatively
// acknowledged lease ends, which makes the message deliverable again.
func psNextQueueChange(subName string, now time.Time) (time.Time, bool) {
	s, ok := psSubscriptions.Get(subName)
	if !ok {
		return time.Time{}, false
	}
	q, ok := psQueues.Get(subName)
	if !ok {
		return time.Time{}, false
	}
	pol := psPolicy(s)
	n := now.UnixMilli()
	var next int64
	consider := func(at int64) {
		if at > n && (next == 0 || at < next) {
			next = at
		}
	}
	for _, m := range q.Queue.Messages {
		consider(m.AvailableAt)
		if m.Leased && pol.Backoff != nil {
			consider(m.AvailableAt + pol.Backoff(m.Deliveries).Milliseconds())
		}
	}
	if next == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(next), true
}

// psPull leases up to max messages. Without returnImmediately it holds an
// empty pull until a message becomes deliverable, psPullWait passes, or the
// caller goes away, and answers with what it has.
func psPull(ctx context.Context, subName string, max int, returnImmediately bool) ([]psDelivered, error) {
	if !returnImmediately {
		sim.DeclareWait(ctx, psPullWait)
	}
	deadline := time.Now().Add(psPullWait)
	for {
		arrival := psSubscriptionSignal(subName)
		delivered, err := psDequeue(subName, max, 0)
		if err != nil || len(delivered) > 0 || returnImmediately {
			return delivered, err
		}
		now := time.Now()
		if !now.Before(deadline) {
			return delivered, nil
		}
		wake := deadline
		if next, ok := psNextQueueChange(subName, now); ok && next.Before(wake) {
			wake = next
		}
		timer := time.NewTimer(wake.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-arrival:
			timer.Stop()
		case <-timer.C:
		}
	}
}
