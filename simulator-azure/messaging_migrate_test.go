package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	amqp "github.com/Azure/go-amqp"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/streamlog"
)

func migrateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sim.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sim.CloseDB(db) })
	return db
}

func putLegacy[T any](t *testing.T, db *sql.DB, table, key string, row T) {
	t.Helper()
	s, err := sim.NewSQLiteStore[T](db, table)
	if err != nil {
		t.Fatal(err)
	}
	s.Put(key, row)
}

// Messages an earlier build persisted for a Service Bus queue load after the
// upgrade: the unlocked one is received with its sequence number and broker
// properties, the locked one keeps its lock token until it completes.
func TestServiceBusMigratesLegacyQueueRows(t *testing.T) {
	db := migrateTestDB(t)
	enqueued := time.Now().Add(-time.Minute).UTC()
	legacy := sbLegacyRecord{NextSeq: 2}
	legacy.Messages = append(legacy.Messages, struct {
		MessageID      string
		Body           []byte
		ContentType    string
		BrokerHeader   string
		EnqueuedTime   time.Time
		LockedUntilUtc time.Time
		LockToken      string
		SequenceNumber int64
	}{MessageID: "locked", Body: []byte("one"), EnqueuedTime: enqueued, LockedUntilUtc: time.Now().Add(time.Minute), LockToken: "00112233-4455-6677-8899-aabbccddeeff", SequenceNumber: 1},
		struct {
			MessageID      string
			Body           []byte
			ContentType    string
			BrokerHeader   string
			EnqueuedTime   time.Time
			LockedUntilUtc time.Time
			LockToken      string
			SequenceNumber int64
		}{MessageID: "free", Body: []byte("two"), BrokerHeader: `{"Label":"l"}`, EnqueuedTime: enqueued, SequenceNumber: 2})
	putLegacy(t, db, "servicebus_queue_messages", sbQueueKey("ns", "q"), legacy)

	newServiceBusQueueTestStores(t)
	sbQueueDurable = sim.MakeStore[sbQueueRecord](db, "servicebus_queue_messages")
	if err := sbMigrateQueues(db); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handleSBRESTDataPlane(rec, httptest.NewRequest(http.MethodDelete, "/q/messages/head", nil), "ns")
	var props map[string]any
	if err := json.Unmarshal([]byte(rec.Header().Get("BrokerProperties")), &props); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "two" || props["SequenceNumber"] != float64(2) || props["Label"] != "l" {
		t.Fatalf("receive after the migration = %d %q %v", rec.Code, rec.Body.String(), props)
	}
	if err := sbSettle("ns", "q", "00112233-4455-6677-8899-aabbccddeeff", sbSettlement{kind: sbComplete}); err != nil {
		t.Fatalf("completing with the legacy lock token: %v", err)
	}
	if total, _, _ := sbQueueCounts("ns", "q"); total != 0 {
		t.Fatalf("%d messages left", total)
	}
	sbSend("ns", "q", sbOutgoing{})
	if got, _ := sbReceive("ns", "q", "", 1, false); len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("a send after the migration took sequence %+v", got)
	}
}

// Messages an earlier build persisted for a storage queue load after the
// upgrade with their text, times, pop receipt, visibility and dequeue count.
func TestQueueStorageMigratesLegacyQueueRows(t *testing.T) {
	db := migrateTestDB(t)
	now := time.Now()
	msgs, err := json.Marshal([]queueLegacyMessage{
		{MessageID: "hidden", MessageText: "a", InsertionTime: now.UTC().Format(time.RFC1123), ExpirationTime: now.Add(time.Hour).UTC().Format(time.RFC1123), PopReceipt: "pr-1", VisibleAt: now.Add(time.Minute).Unix(), DequeueCount: 1},
		{MessageID: "visible", MessageText: "b", InsertionTime: now.UTC().Format(time.RFC1123), ExpirationTime: now.Add(time.Hour).UTC().Format(time.RFC1123)},
	})
	if err != nil {
		t.Fatal(err)
	}
	putLegacy(t, db, "queue_data", queueKey("acct", "q"), queueLegacyData{Account: "acct", Name: "q", Messages: msgs})

	saved := queueData
	t.Cleanup(func() { queueData = saved })
	queueData = sim.MakeStore[QueueData](db, "queue_data")
	if err := queueMigrateMessages(db); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handleQueueGetMessages(rec, httptest.NewRequest(http.MethodGet, "/q/messages?numofmessages=32", nil), "acct", "q")
	got := queueList(t, rec.Body.Bytes())
	if len(got) != 1 || got[0].MessageID != "visible" || got[0].MessageText != "b" || got[0].DequeueCount != 1 {
		t.Fatalf("get after the migration = %+v", got)
	}
	rec = httptest.NewRecorder()
	handleQueueDeleteMessage(rec, httptest.NewRequest(http.MethodDelete, "/q/messages/hidden?popreceipt=pr-1", nil), "acct", "q", "hidden")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete with the legacy pop receipt = %d %s", rec.Code, rec.Body)
	}
	q, _ := queueData.Get(queueKey("acct", "q"))
	if len(q.Messages.Messages) != 1 || !strings.Contains(queueExpiration(q.Messages.Messages[0]), now.Add(time.Hour).UTC().Format("15:04")) {
		t.Fatalf("migrated queue = %+v", q.Messages.Messages)
	}
}

// Events an earlier build persisted for a partition move into the partition
// log with their sequence numbers, times, bodies and properties, and the next
// event continues the sequence.
func TestEventHubsMigratesLegacyPartitions(t *testing.T) {
	db := migrateTestDB(t)
	enqueued := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	legacy := ehLegacyPartition{NextSeq: 6}
	legacy.Records = append(legacy.Records, struct {
		SequenceNumber int64
		EnqueuedTime   time.Time
		Body           []byte
		Properties     map[string]any
	}{SequenceNumber: 5, EnqueuedTime: enqueued, Body: []byte("e5"), Properties: map[string]any{"k": "v"}})
	putLegacy(t, db, "eventhub_partition_events", ehPartitionKey("ns", "hub", "0"), legacy)

	ehTestStores(t, map[string]any{"partitionCount": float64(1)})
	ehLog = streamlog.New(
		sim.MakeStore[streamlog.Record[ehEvent]](db, "eventhub_partition_records"),
		sim.MakeStore[streamlog.Head](db, "eventhub_partition_heads"))
	if err := ehMigratePartitions(db); err != nil {
		t.Fatal(err)
	}

	body, next, ok := ehAMQPNextEvent("ns", "hub/ConsumerGroups/$Default/Partitions/0", 0)
	if !ok || next != 6 {
		t.Fatalf("read after the migration: ok %v next %d", ok, next)
	}
	var m amqp.Message
	if err := m.UnmarshalBinary(body); err != nil {
		t.Fatal(err)
	}
	if string(m.GetData()) != "e5" || m.ApplicationProperties["k"] != "v" || m.Annotations["x-opt-sequence-number"] != int64(5) ||
		!m.Annotations["x-opt-enqueued-time"].(time.Time).Equal(enqueued) {
		t.Fatalf("migrated event = %+v", m)
	}
	ehAMQPEnqueue("ns", "hub/Partitions/0", &amqp.Message{Data: [][]byte{[]byte("e6")}})
	if _, seq, _, ok := ehTestRead(t, 6); !ok || seq != 6 {
		t.Fatalf("the next event took sequence %d", seq)
	}
}
