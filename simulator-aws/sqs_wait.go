package main

import (
	"context"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// sqsArrivals wakes long-polling ReceiveMessage calls, keyed by queue name.
// sqsSignalQueue signals a queue whose receivable set may have grown: a send,
// a redrive into it, a visibility change, or a delete that unblocks a FIFO
// message group. A delayed or in-flight message becoming visible wakes them
// at the instant the queue stores for it.
var sqsArrivals = msgq.NewWaiter(func(name string, now time.Time) (time.Time, bool) {
	q, ok := sqsQueues.Get(name)
	if !ok {
		return time.Time{}, false
	}
	return q.Messages.NextChange(sqsPolicy(q), now)
})

func sqsSignalQueue(name string) { sqsArrivals.Signal(name) }

// sqsLongPoll receives from the queue, and while nothing is receivable waits
// until deadline for a message to arrive or for a delayed or in-flight one to
// become visible. It reports false when ctx ends first.
func sqsLongPoll(ctx context.Context, name string, maxN, visTimeout int, deadline time.Time) ([]SQSMessage, bool) {
	var picked []SQSMessage
	err := sqsArrivals.Poll(ctx, name, deadline, func() bool {
		picked = sqsReceiveAvailableMessages(name, maxN, visTimeout)
		return len(picked) > 0
	})
	return picked, err == nil
}
