package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

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

func legacyStore[T any](t *testing.T, db *sql.DB, table string) *sim.SQLiteStore[T] {
	t.Helper()
	s, err := sim.NewSQLiteStore[T](db, table)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A queue an earlier build persisted loads after the upgrade with its
// messages' bodies, attributes, receive counts, receipt handles, visibility
// and FIFO deduplication records.
func TestSQSMigratesLegacyQueueRows(t *testing.T) {
	db := migrateTestDB(t)
	now := time.Now().UnixMilli()
	msgs, err := json.Marshal([]sqsLegacyMessage{
		{MessageId: "m1", Body: "in flight", MD5OfBody: "x", ReceiptHandle: "rh-1", SentTimestamp: now - 1000,
			VisibleAt: now + 60_000, ApproximateReceiveCount: 2, FirstReceivedAt: now - 500,
			MessageGroupID: "g", MessageDeduplicationID: "d1", SequenceNumber: "7",
			MessageAttributes: map[string]SQSMessageAttribute{"k": {DataType: "String", StringValue: "v"}}},
		{MessageId: "m2", Body: "waiting", SentTimestamp: now - 900, VisibleAt: now - 900,
			MessageGroupID: "g", MessageDeduplicationID: "d2", SequenceNumber: "8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	old := sqsLegacyQueue{
		Name: "legacy.fifo", URL: sqsQueueURL("legacy.fifo"), ARN: sqsQueueARN("legacy.fifo"),
		VisibilityTimeout: 30, Messages: msgs, NextSequence: 8,
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
	}
	old.Deduplication = map[string]struct {
		MessageID      string
		SequenceNumber string
		ExpiresAt      int64
	}{"d2": {MessageID: "m2", SequenceNumber: "8", ExpiresAt: now + 60_000}}
	legacyStore[sqsLegacyQueue](t, db, "sqs_queues").Put("legacy.fifo", old)

	saved := sqsQueues
	t.Cleanup(func() { sqsQueues = saved })
	sqsQueues = sim.MakeStore[SQSQueue](db, "sqs_queues")
	if err := sqsMigrateQueues(db); err != nil {
		t.Fatal(err)
	}

	q, ok := sqsQueues.Get("legacy.fifo")
	if !ok || len(q.Messages.Messages) != 2 || q.Messages.NextSeq != 8 {
		t.Fatalf("migrated queue = %+v", q)
	}
	// The in-flight message holds group g; its receipt still deletes it.
	if got := sqsReceive(t, q.URL, 10, 30); len(got) != 0 {
		t.Fatalf("group g was not blocked by its in-flight message: %v", got)
	}
	if code, _ := sqsCall(t, handleSQSDeleteMessage, map[string]any{"QueueUrl": q.URL, "ReceiptHandle": "rh-1"}); code != http.StatusOK {
		t.Fatalf("DeleteMessage with the legacy receipt = %d", code)
	}
	got := sqsReceive(t, q.URL, 10, 30)
	if len(got) != 1 || got[0]["Body"] != "waiting" {
		t.Fatalf("after deleting m1 = %v", got)
	}
	if attrs := got[0]["Attributes"].(map[string]any); attrs["SequenceNumber"] != "8" || attrs["ApproximateReceiveCount"] != "1" {
		t.Fatalf("attributes = %v", attrs)
	}
	// The deduplication record survives: a resend answers with m2.
	code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": q.URL, "MessageBody": "again", "MessageGroupId": "g", "MessageDeduplicationId": "d2"})
	if code != http.StatusOK || out["MessageId"] != "m2" {
		t.Fatalf("resend inside the deduplication interval = %d %v", code, out)
	}
	if err := sqsMigrateQueues(db); err != nil {
		t.Fatalf("a second start re-ran the migration over current rows: %v", err)
	}
}

// Shards an earlier build stored whole move into the shard log with their
// sequence numbers and arrival times, and outstanding iterators keep their
// place.
func TestKinesisMigratesLegacyShardRecords(t *testing.T) {
	db := migrateTestDB(t)
	arrival := float64(time.Now().Add(-time.Hour).Unix())
	partition := kinesisShardRecordKey("s", "shardId-000000000000")
	legacyStore[[]kinesisLegacyRecord](t, db, "kinesis_records").Put(partition, []kinesisLegacyRecord{
		{SequenceNumber: "1", ApproximateArrivalTimestamp: arrival, Data: []byte("a"), PartitionKey: "k"},
		{SequenceNumber: "2", ApproximateArrivalTimestamp: arrival, Data: []byte("b"), PartitionKey: "k"},
	})
	legacyStore[kinesisLegacyIterator](t, db, "kinesis_iterators").Put("it", kinesisLegacyIterator{StreamName: "s", ShardID: "shardId-000000000000", Index: 1})

	streams, log, iterators := kinesisStreams, kinesisLog, kinesisIterators
	t.Cleanup(func() { kinesisStreams, kinesisLog, kinesisIterators = streams, log, iterators })
	kinesisStreams = sim.MakeStore[KinesisStream](db, "kinesis_streams")
	kinesisLog = streamlog.New(
		sim.MakeStore[streamlog.Record[kinesisRecord]](db, "kinesis_shard_records"),
		sim.MakeStore[streamlog.Head](db, "kinesis_shard_heads"))
	kinesisIterators = sim.MakeStore[kinesisIterator](db, "kinesis_iterators")
	kinesisStreams.Put("s", KinesisStream{StreamName: "s", Shards: kinesisMakeShards(1), RetentionPeriodHours: 24})
	if err := kinesisMigrateRecords(db); err != nil {
		t.Fatal(err)
	}

	code, out := kinesisCall(t, handleKinesisGetRecords, map[string]any{"ShardIterator": "it"})
	if code != http.StatusOK {
		t.Fatalf("GetRecords on a legacy iterator = %d %v", code, out)
	}
	recs := out["Records"].([]any)
	if len(recs) != 1 || recs[0].(map[string]any)["SequenceNumber"] != "2" ||
		recs[0].(map[string]any)["ApproximateArrivalTimestamp"] != arrival {
		t.Fatalf("records after the legacy iterator = %v", recs)
	}
	if code, out := kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "s", "Data": []byte("c"), "PartitionKey": "k"}); code != http.StatusOK || out["SequenceNumber"] != "3" {
		t.Fatalf("PutRecord after the migration = %d %v", code, out)
	}
	_, rows, err := sim.LegacyRows[[]kinesisLegacyRecord](db, "kinesis_records")
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy shard rows left: %d %v", len(rows), err)
	}
}
