package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A backlog and an outstanding message an earlier build persisted load after
// the upgrade: the pending message is delivered, the outstanding one keeps its
// ack id and deadline and is acknowledged by it.
func TestPubSubMigratesLegacyBacklogAndInFlight(t *testing.T) {
	db, err := sim.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sim.CloseDB(db) })
	const sub = "projects/p/subscriptions/s"

	pending, err := json.Marshal([]PSMessage{{MessageId: "m2", PublishTime: nowTimestamp(), Data: "cGVuZGluZw==", Attributes: map[string]string{"a": "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	legacyQueues, err := sim.NewSQLiteStore[psLegacyQueue](db, "pubsub_queues")
	if err != nil {
		t.Fatal(err)
	}
	legacyQueues.Put(sub, psLegacyQueue{Subscription: sub, Messages: pending})
	legacyInFlight, err := sim.NewSQLiteStore[psLegacyInFlight](db, "pubsub_inflight")
	if err != nil {
		t.Fatal(err)
	}
	legacyInFlight.Put("ack-1", psLegacyInFlight{
		AckId: "ack-1", Subscription: sub, DeliveredAt: time.Now(), AckDeadline: time.Now().Add(time.Minute),
		Message: PSMessage{MessageId: "m1", PublishTime: nowTimestamp(), Data: "b3V0c3RhbmRpbmc="},
	})

	psTestStores(t)
	psQueues = sim.MakeStore[psQueue](db, "pubsub_queues")
	psSubscriptions.Put(sub, PSSubscription{Name: sub, Topic: "projects/p/topics/t", AckDeadlineSeconds: 10})
	if err := psMigrateQueues(db); err != nil {
		t.Fatal(err)
	}

	got := must(psDequeue(sub, 10, 0))
	if len(got) != 1 || got[0].Message.MessageId != "m2" || got[0].Message.Attributes["a"] != "b" {
		t.Fatalf("pull after the migration = %+v", got)
	}
	if psOutstanding(PSSubscription{Name: sub}) != 2 {
		t.Fatal("the legacy outstanding message is not under its ack deadline")
	}
	psAcknowledge(sub, []string{"ack-1", got[0].AckID})
	if q, _ := psQueues.Get(sub); len(q.Queue.Messages) != 0 {
		t.Fatalf("messages left after acknowledging both: %+v", q.Queue.Messages)
	}
	if _, rows, err := sim.LegacyRows[psLegacyInFlight](db, "pubsub_inflight"); err != nil || len(rows) != 0 {
		t.Fatalf("legacy in-flight rows left: %d %v", len(rows), err)
	}
}
