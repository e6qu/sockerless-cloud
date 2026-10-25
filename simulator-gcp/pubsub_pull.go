package main

import (
	"context"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"google.golang.org/grpc/status"
)

// psPullWait bounds how long a Pull without returnImmediately holds the call
// open on an empty subscription before it answers with no messages. Pub/Sub
// documents only that the wait is bounded; it sits below the 60-second
// deadline the client libraries give Pull, so the caller reads an empty
// response rather than DEADLINE_EXCEEDED.
const psPullWait = 10 * time.Second

// psArrivals wakes waiting Pull calls, idle StreamingPull streams and the
// push and dead-letter loops, keyed by subscription name.
// psSignalSubscription signals a subscription whose deliverable set may have
// changed: a publish, a negative acknowledgement or ack deadline change, an
// acknowledgement that unblocks an ordering key or frees flow-control budget,
// a seek, or the subscription's deletion, detachment or update. A message
// changing state on its own — an ack deadline lapsing, which frees
// flow-control budget, or a retry backoff ending — wakes them at the instant
// the queue stores for it.
var psArrivals = msgq.NewWaiter(func(subName string, now time.Time) (time.Time, bool) {
	s, ok := psSubscriptions.Get(subName)
	if !ok {
		return time.Time{}, false
	}
	q, ok := psQueues.Get(subName)
	if !ok {
		return time.Time{}, false
	}
	return q.Queue.NextChange(psPolicy(s), now)
})

func psSignalSubscription(name string) { psArrivals.Signal(name) }

// psPull leases up to max messages. Without returnImmediately it holds an
// empty pull until a message becomes deliverable, psPullWait passes, or the
// caller goes away, and answers with what it has.
func psPull(ctx context.Context, subName string, max int, returnImmediately bool) ([]psDelivered, error) {
	if returnImmediately {
		return psDequeue(subName, max, 0)
	}
	sim.DeclareWait(ctx, psPullWait)
	var delivered []psDelivered
	var err error
	if waitErr := psArrivals.Poll(ctx, subName, time.Now().Add(psPullWait), func() bool {
		delivered, err = psDequeue(subName, max, 0)
		return err != nil || len(delivered) > 0
	}); waitErr != nil {
		return nil, status.FromContextError(waitErr).Err()
	}
	return delivered, err
}
