package main

import (
	"sync"
	"time"
)

// sqsArrivals wakes long-polling ReceiveMessage calls. Each queue name maps to
// a channel that sqsSignalQueue closes when the queue's receivable set may
// have grown: a send, a redrive into it, a visibility change, or a delete
// that unblocks a FIFO message group.
var sqsArrivals = struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}{ch: map[string]chan struct{}{}}

func sqsQueueSignal(name string) <-chan struct{} {
	sqsArrivals.mu.Lock()
	defer sqsArrivals.mu.Unlock()
	c, ok := sqsArrivals.ch[name]
	if !ok {
		c = make(chan struct{})
		sqsArrivals.ch[name] = c
	}
	return c
}

func sqsSignalQueue(name string) {
	sqsArrivals.mu.Lock()
	defer sqsArrivals.mu.Unlock()
	if c, ok := sqsArrivals.ch[name]; ok {
		close(c)
		delete(sqsArrivals.ch, name)
	}
}

// sqsNextAvailable returns when the earliest message that is not receivable
// at now becomes receivable: the end of its delivery delay or of its
// visibility timeout.
func sqsNextAvailable(name string, now time.Time) (time.Time, bool) {
	q, ok := sqsQueues.Get(name)
	if !ok {
		return time.Time{}, false
	}
	pol := sqsPolicy(q)
	n := now.UnixMilli()
	var next int64
	for _, m := range q.Messages.Messages {
		if q.Messages.Available(m, pol, now) {
			continue
		}
		if m.AvailableAt > n && (next == 0 || m.AvailableAt < next) {
			next = m.AvailableAt
		}
	}
	if next == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(next), true
}

// sqsLongPoll receives from the queue, and while nothing is receivable waits
// until deadline for a message to arrive or for a delayed or in-flight one to
// become visible. It reports false when done closes first.
func sqsLongPoll(done <-chan struct{}, name string, maxN, visTimeout int, deadline time.Time) ([]SQSMessage, bool) {
	for {
		arrival := sqsQueueSignal(name)
		picked := sqsReceiveAvailableMessages(name, maxN, visTimeout)
		now := time.Now()
		if len(picked) > 0 || !now.Before(deadline) {
			return picked, true
		}
		wake := deadline
		if next, ok := sqsNextAvailable(name, now); ok && next.Before(wake) {
			wake = next
		}
		timer := time.NewTimer(wake.Sub(now))
		select {
		case <-done:
			timer.Stop()
			return nil, false
		case <-arrival:
			timer.Stop()
		case <-timer.C:
		}
	}
}
