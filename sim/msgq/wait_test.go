package msgq

import (
	"context"
	"sync"
	"testing"
	"time"
)

// testQueues is a cloud's store as far as a Waiter sees it: queues by key,
// read and written under one lock.
type testQueues struct {
	mu     sync.Mutex
	queues map[string]*Queue[string]
	waiter *Waiter
}

func newTestQueues() *testQueues {
	tq := &testQueues{queues: map[string]*Queue[string]{}}
	tq.waiter = NewWaiter(func(key string, now time.Time) (time.Time, bool) {
		tq.mu.Lock()
		defer tq.mu.Unlock()
		q, ok := tq.queues[key]
		if !ok {
			return time.Time{}, false
		}
		return q.NextChange(Policy{}, now)
	})
	return tq
}

func (tq *testQueues) send(key, payload string, delay time.Duration) {
	tq.mu.Lock()
	q, ok := tq.queues[key]
	if !ok {
		q = &Queue[string]{}
		tq.queues[key] = q
	}
	q.Enqueue(payload, EnqueueOpts{Delay: delay}, Policy{}, time.Now())
	tq.mu.Unlock()
	tq.waiter.Signal(key)
}

func (tq *testQueues) receive(key string, lease time.Duration) []string {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	q, ok := tq.queues[key]
	if !ok {
		return nil
	}
	return ids(q.Receive(ReceiveOpts[string]{Max: 10, Lease: lease}, Policy{}, time.Now()).Leased)
}

func TestNextChangeNamesTheEarliestDelayLeaseOrBackoffEnd(t *testing.T) {
	var q Queue[string]
	pol := Policy{Backoff: func(int) time.Duration { return 10 * time.Second }}
	q.Enqueue("delayed", EnqueueOpts{Delay: 30 * time.Second}, pol, t0)
	q.Enqueue("leased", EnqueueOpts{}, pol, t0)
	q.Receive(ReceiveOpts[string]{Max: 1, Lease: 20 * time.Second}, pol, t0)

	if at, ok := q.NextChange(pol, t0); !ok || !at.Equal(t0.Add(20*time.Second)) {
		t.Fatalf("next change = %v %v, want the lease end", at, ok)
	}
	if at, ok := q.NextChange(pol, t0.Add(20*time.Second)); !ok || !at.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("next change after the lease = %v %v, want the delay end and backoff end", at, ok)
	}
	if _, ok := q.NextChange(pol, t0.Add(30*time.Second)); ok {
		t.Fatal("a queue with nothing pending reported a next change")
	}
}

func TestPollWakesOnASignal(t *testing.T) {
	tq := newTestQueues()
	got := make(chan []string, 1)
	go func() {
		var taken []string
		err := tq.waiter.Poll(context.Background(), "q", time.Now().Add(time.Minute), func() bool {
			taken = tq.receive("q", time.Minute)
			return len(taken) > 0
		})
		if err != nil {
			t.Errorf("Poll: %v", err)
		}
		got <- taken
	}()
	tq.send("q", "a", 0)
	eq(t, <-got, []string{"a"})
}

func TestPollWakesWhenADelayEndsWithoutASignal(t *testing.T) {
	tq := newTestQueues()
	tq.send("q", "later", 50*time.Millisecond)
	start := time.Now()
	var taken []string
	if err := tq.waiter.Poll(context.Background(), "q", start.Add(time.Minute), func() bool {
		taken = tq.receive("q", time.Minute)
		return len(taken) > 0
	}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	eq(t, taken, []string{"later"})
	if waited := time.Since(start); waited < 50*time.Millisecond {
		t.Fatalf("received a delayed message after %v", waited)
	}
}

func TestPollAnswersEmptyAtItsDeadlineAndStopsWithTheContext(t *testing.T) {
	tq := newTestQueues()
	calls := 0
	if err := tq.waiter.Poll(context.Background(), "q", time.Now().Add(20*time.Millisecond), func() bool {
		calls++
		return false
	}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if calls != 2 {
		t.Fatalf("receive ran %d times, want once on arrival and once at the deadline", calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tq.waiter.Poll(ctx, "q", time.Now().Add(time.Minute), func() bool { return false }); err != context.Canceled {
		t.Fatalf("Poll on a cancelled context returned %v", err)
	}
}

func TestWaitAnyWakesOnAnyQueue(t *testing.T) {
	tq := newTestQueues()
	changed := tq.waiter.Any()
	done := make(chan error, 1)
	go func() { done <- tq.waiter.WaitAny(context.Background(), changed, []string{"a"}) }()
	tq.send("b", "x", 0)
	if err := <-done; err != nil {
		t.Fatalf("WaitAny: %v", err)
	}
}

func TestArmSignalsAndRunsTheHookAtEachChange(t *testing.T) {
	tq := newTestQueues()
	due := make(chan string, 4)
	tq.waiter.OnDue(func(key string) { due <- key })
	tq.send("q", "first", 30*time.Millisecond)
	tq.send("q", "second", 60*time.Millisecond)
	changed := tq.waiter.Changed("q")
	tq.waiter.Arm("q")
	tq.waiter.Arm("q")

	if key := <-due; key != "q" {
		t.Fatalf("hook ran for %q", key)
	}
	select {
	case <-changed:
	default:
		t.Fatal("the timer ran the hook without signalling the queue")
	}
	eq(t, tq.receive("q", time.Minute), []string{"first"})
	// The timer re-arms itself from the store for the second message.
	<-due
	eq(t, tq.receive("q", time.Minute), []string{"second"})
}

func TestArmReplacesALaterTimerWithASoonerOne(t *testing.T) {
	tq := newTestQueues()
	due := make(chan time.Time, 4)
	tq.waiter.OnDue(func(string) { due <- time.Now() })
	tq.send("q", "late", time.Hour)
	tq.waiter.Arm("q")
	start := time.Now()
	tq.send("q", "soon", 20*time.Millisecond)
	tq.waiter.Arm("q")
	select {
	case at := <-due:
		if at.Sub(start) < 20*time.Millisecond {
			t.Fatalf("hook ran after %v, before the sooner message was due", at.Sub(start))
		}
	case <-time.After(time.Minute):
		t.Fatal("the sooner change never fired")
	}
	eq(t, tq.receive("q", time.Minute), []string{"soon"})
}
