package msgq

import (
	"encoding/json"
	"testing"
	"time"
)

var t0 = time.UnixMilli(1_700_000_000_000)

func ids(ms []Message[string]) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Payload)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestReceiveLeasesAndRedeliversAfterLease(t *testing.T) {
	var q Queue[string]
	pol := Policy{ReceiptOutlivesLease: true}
	q.Enqueue("a", EnqueueOpts{}, pol, t0)
	q.Enqueue("b", EnqueueOpts{}, pol, t0)

	got := q.Receive(ReceiveOpts[string]{Max: 1, Lease: 10 * time.Second}, pol, t0)
	eq(t, ids(got.Leased), []string{"a"})
	if got.Leased[0].Deliveries != 1 || got.Leased[0].Receipt == "" {
		t.Fatalf("lease = %+v", got.Leased[0])
	}
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 10, Lease: 10 * time.Second}, pol, t0).Leased), []string{"b"})
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 10}, pol, t0.Add(5*time.Second)).Leased), nil)

	again := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Second}, pol, t0.Add(10*time.Second))
	eq(t, ids(again.Leased), []string{"a"})
	if again.Leased[0].Deliveries != 2 || again.Leased[0].Receipt == got.Leased[0].Receipt {
		t.Fatalf("redelivery = %+v", again.Leased[0])
	}
	if c := q.Count(pol, t0.Add(10*time.Second)); c.Held != 1 || c.Available != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestSettleAndReceiptLifetime(t *testing.T) {
	var q Queue[string]
	q.Enqueue("a", EnqueueOpts{}, Policy{}, t0)
	r := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Second}, Policy{}, t0).Leased[0].Receipt

	if _, ok := q.Settle(r, Policy{}, t0.Add(2*time.Second)); ok {
		t.Fatal("a lease-bound receipt settled after its lease ran out")
	}
	if _, ok := q.Settle(r, Policy{ReceiptOutlivesLease: true}, t0.Add(2*time.Second)); !ok {
		t.Fatal("a receipt that outlives its lease did not settle")
	}
	if len(q.Messages) != 0 {
		t.Fatalf("messages left: %+v", q.Messages)
	}
}

func TestDelayAndExtend(t *testing.T) {
	var q Queue[string]
	pol := Policy{}
	q.Enqueue("a", EnqueueOpts{Delay: 5 * time.Second}, pol, t0)
	if c := q.Count(pol, t0); c.Delayed != 1 {
		t.Fatalf("counts = %+v", c)
	}
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, t0.Add(4*time.Second)).Leased), nil)
	m := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Second}, pol, t0.Add(5*time.Second)).Leased[0]

	if _, found, held := q.Extend(m.Receipt, 30*time.Second, pol, t0.Add(5500*time.Millisecond)); !found || !held {
		t.Fatalf("extend found=%v held=%v", found, held)
	}
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, t0.Add(20*time.Second)).Leased), nil)
	if _, found, _ := q.Extend("nope", time.Second, pol, t0); found {
		t.Fatal("unknown receipt found")
	}
}

func TestMaxDeliveriesDeadLetters(t *testing.T) {
	var q Queue[string]
	pol := Policy{MaxDeliveries: 2}
	q.Enqueue("a", EnqueueOpts{}, pol, t0)
	for i := range 2 {
		now := t0.Add(time.Duration(i) * time.Minute)
		if got := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Second}, pol, now); len(got.Leased) != 1 {
			t.Fatalf("delivery %d: %+v", i+1, got)
		}
	}
	got := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Second}, pol, t0.Add(3*time.Minute))
	if len(got.Leased) != 0 || len(got.DeadLettered) != 1 || got.DeadLettered[0].Deliveries != 2 {
		t.Fatalf("third receive = %+v", got)
	}
	if len(q.Messages) != 0 {
		t.Fatal("dead-lettered message stayed")
	}
}

func TestDeduplicationWindow(t *testing.T) {
	var q Queue[string]
	pol := Policy{DedupWindow: time.Minute}
	first, dup := q.Enqueue("a", EnqueueOpts{DedupKey: "k"}, pol, t0)
	if dup {
		t.Fatal("first send reported duplicate")
	}
	q.Receive(ReceiveOpts[string]{Max: 1}, pol, t0)
	q.Purge()
	again, dup := q.Enqueue("a", EnqueueOpts{DedupKey: "k"}, pol, t0.Add(30*time.Second))
	if !dup || again.ID != first.ID || again.Seq != first.Seq || len(q.Messages) != 0 {
		t.Fatalf("resend inside the window = %+v dup=%v", again, dup)
	}
	later, dup := q.Enqueue("a", EnqueueOpts{DedupKey: "k"}, pol, t0.Add(time.Minute))
	if dup || later.ID == first.ID || later.Seq <= first.Seq {
		t.Fatalf("resend after the window = %+v dup=%v", later, dup)
	}
}

func TestOrderedGroupsBlockBehindAnUnavailableMessage(t *testing.T) {
	var q Queue[string]
	pol := Policy{Ordered: true}
	q.Enqueue("g1-1", EnqueueOpts{Group: "g1"}, pol, t0)
	q.Enqueue("g2-1", EnqueueOpts{Group: "g2"}, pol, t0)
	q.Enqueue("g1-2", EnqueueOpts{Group: "g1"}, pol, t0)
	q.Enqueue("g2-2", EnqueueOpts{Group: "g2"}, pol, t0)

	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, pol, t0).Leased), []string{"g1-1"})
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 10, Lease: time.Minute}, pol, t0).Leased), []string{"g2-1", "g2-2"})
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 10, Lease: time.Minute}, pol, t0).Leased), nil)
}

func TestAbandonBacksOff(t *testing.T) {
	var q Queue[string]
	pol := Policy{Backoff: func(d int) time.Duration { return time.Duration(d) * 10 * time.Second }}
	q.Enqueue("a", EnqueueOpts{}, pol, t0)
	m := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, pol, t0).Leased[0]
	if _, ok := q.Abandon(m.Receipt, pol, t0); !ok {
		t.Fatal("abandon failed")
	}
	if _, ok := q.Settle(m.Receipt, pol, t0); ok {
		t.Fatal("an abandoned receipt still settles")
	}
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, t0.Add(9*time.Second)).Leased), nil)
	if got := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Second}, pol, t0.Add(10*time.Second)).Leased; len(got) != 1 {
		t.Fatalf("redelivery after the backoff = %+v", got)
	}
	// A lease that runs out backs off by the delivery count too.
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, t0.Add(11*time.Second+19*time.Second)).Leased), nil)
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, t0.Add(31*time.Second)).Leased), []string{"a"})
}

func TestExpiry(t *testing.T) {
	var q Queue[string]
	pol := Policy{Retention: time.Hour}
	q.Enqueue("old", EnqueueOpts{}, pol, t0)
	q.Enqueue("ttl", EnqueueOpts{TTL: time.Minute}, pol, t0.Add(30*time.Minute))
	q.Enqueue("new", EnqueueOpts{}, pol, t0.Add(30*time.Minute))
	got := q.Receive(ReceiveOpts[string]{Max: 10}, pol, t0.Add(time.Hour))
	eq(t, ids(got.Expired), []string{"old", "ttl"})
	eq(t, ids(got.Leased), []string{"new"})
}

func TestAcceptSkipsWithoutDelivering(t *testing.T) {
	var q Queue[string]
	q.Enqueue("a", EnqueueOpts{}, Policy{}, t0)
	q.Enqueue("b", EnqueueOpts{}, Policy{}, t0)
	got := q.Receive(ReceiveOpts[string]{Max: 10, Accept: func(m Message[string]) bool { return m.Payload == "b" }}, Policy{}, t0)
	eq(t, ids(got.Leased), []string{"b"})
	if q.Messages[0].Deliveries != 0 {
		t.Fatal("a skipped message counted a delivery")
	}
}

func TestPeekAndRemove(t *testing.T) {
	var q Queue[string]
	for _, p := range []string{"a", "b", "c"} {
		q.Enqueue(p, EnqueueOpts{}, Policy{}, t0)
	}
	q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, Policy{}, t0)
	eq(t, ids(q.Peek(0, 10, false, Policy{}, t0)), []string{"a", "b", "c"})
	eq(t, ids(q.Peek(0, 10, true, Policy{}, t0)), []string{"b", "c"})
	eq(t, ids(q.Peek(3, 10, false, Policy{}, t0)), []string{"c"})
	eq(t, ids(q.Remove(func(m Message[string]) bool { return m.Payload == "b" })), []string{"b"})
	if q.ByID(q.Messages[0].ID) == nil {
		t.Fatal("lookup failed")
	}
}

func TestQueueRoundTripsThroughJSON(t *testing.T) {
	var q Queue[string]
	pol := Policy{DedupWindow: time.Minute}
	q.Enqueue("a", EnqueueOpts{DedupKey: "k", Group: "g"}, pol, t0)
	q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, pol, t0)
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var back Queue[string]
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Messages) != 1 || back.Messages[0] != q.Messages[0] || back.NextSeq != 1 || back.Dedup["k"] != q.Dedup["k"] {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestOrderedLeavesUngroupedMessagesUnordered(t *testing.T) {
	var q Queue[string]
	pol := Policy{Ordered: true}
	q.Enqueue("x", EnqueueOpts{}, pol, t0)
	q.Enqueue("y", EnqueueOpts{}, pol, t0)
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, pol, t0).Leased), []string{"x"})
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, pol, t0).Leased), []string{"y"})
}

func TestExhaustedWaitsForTheLastLease(t *testing.T) {
	var q Queue[string]
	pol := Policy{MaxDeliveries: 1}
	q.Enqueue("a", EnqueueOpts{}, pol, t0)
	q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, pol, t0)
	if got := q.Exhausted(pol, t0.Add(30*time.Second)); len(got) != 0 {
		t.Fatalf("dead-lettered under a live lease: %+v", got)
	}
	eq(t, ids(q.Exhausted(pol, t0.Add(time.Minute))), []string{"a"})
}

func TestReleaseDoesNotCountTheDelivery(t *testing.T) {
	var q Queue[string]
	q.Enqueue("a", EnqueueOpts{}, Policy{}, t0)
	m := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, Policy{}, t0).Leased[0]
	if got, ok := q.Release(m.Receipt, Policy{}, t0); !ok || got.Deliveries != 0 {
		t.Fatalf("release = %+v %v", got, ok)
	}
	if again := q.Receive(ReceiveOpts[string]{Max: 1, Lease: time.Minute}, Policy{}, t0).Leased; len(again) != 1 || again[0].Deliveries != 1 {
		t.Fatalf("after release = %+v", again)
	}
}

func TestDedupOutsideAQueue(t *testing.T) {
	var d Dedup
	d.Remember("k", "id", 1, time.Minute, t0)
	if rec, ok := d.Lookup("k", t0.Add(59*time.Second)); !ok || rec.ID != "id" {
		t.Fatalf("lookup = %+v %v", rec, ok)
	}
	if _, ok := d.Lookup("k", t0.Add(time.Minute)); ok || len(d) != 0 {
		t.Fatal("expired record survived")
	}
}

func TestDelayEndsNoEarlierThanItsInstant(t *testing.T) {
	var q Queue[string]
	pol := Policy{}
	sent := t0.Add(900 * time.Microsecond)
	q.Enqueue("a", EnqueueOpts{Delay: time.Second}, pol, sent)
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, sent.Add(time.Second-time.Microsecond)).Leased), nil)
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, t0.Add(1001*time.Millisecond)).Leased), []string{"a"})
}

func TestUndelayedMessageIsReceivableAtOnce(t *testing.T) {
	var q Queue[string]
	pol := Policy{}
	sent := t0.Add(900 * time.Microsecond)
	q.Enqueue("a", EnqueueOpts{}, pol, sent)
	eq(t, ids(q.Receive(ReceiveOpts[string]{Max: 1}, pol, sent).Leased), []string{"a"})
}
