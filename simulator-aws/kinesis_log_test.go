package main

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/streamlog"
)

func kinesisTestStores(t *testing.T) {
	t.Helper()
	streams, log, iterators := kinesisStreams, kinesisLog, kinesisIterators
	t.Cleanup(func() { kinesisStreams, kinesisLog, kinesisIterators = streams, log, iterators })
	kinesisStreams = sim.MakeStore[KinesisStream](nil, "test_kinesis_streams")
	kinesisIterators = sim.MakeStore[kinesisIterator](nil, "test_kinesis_iterators")
	kinesisLog = streamlog.New(
		sim.MakeStore[streamlog.Record[kinesisRecord]](nil, "test_kinesis_shard_records"),
		sim.MakeStore[streamlog.Head](nil, "test_kinesis_shard_heads"))
}

func kinesisCall(t *testing.T, h http.HandlerFunc, body any) (int, map[string]any) {
	t.Helper()
	return sqsCall(t, h, body)
}

func kinesisIterate(t *testing.T, stream string, typ string, extra map[string]any) []map[string]any {
	t.Helper()
	req := map[string]any{"StreamName": stream, "ShardId": "shardId-000000000000", "ShardIteratorType": typ}
	for k, v := range extra {
		req[k] = v
	}
	code, out := kinesisCall(t, handleKinesisGetShardIterator, req)
	if code != http.StatusOK {
		t.Fatalf("GetShardIterator %s: %d %v", typ, code, out)
	}
	code, out = kinesisCall(t, handleKinesisGetRecords, map[string]any{"ShardIterator": out["ShardIterator"]})
	if code != http.StatusOK {
		t.Fatalf("GetRecords: %d %v", code, out)
	}
	var recs []map[string]any
	for _, r := range out["Records"].([]any) {
		recs = append(recs, r.(map[string]any))
	}
	return recs
}

func kinesisSeqs(recs []map[string]any) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r["SequenceNumber"].(string))
	}
	return out
}

func TestKinesisRetentionTrimsAgedRecords(t *testing.T) {
	kinesisTestStores(t)
	if code, out := kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "s", "ShardCount": 1}); code != http.StatusOK {
		t.Fatalf("CreateStream: %d %v", code, out)
	}
	for i := range 3 {
		if code, out := kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "s", "Data": []byte("r" + strconv.Itoa(i)), "PartitionKey": "k"}); code != http.StatusOK {
			t.Fatalf("PutRecord: %d %v", code, out)
		}
	}
	// Age the first two records past the 24-hour default retention.
	partition := kinesisShardRecordKey("s", "shardId-000000000000")
	old := time.Now().Add(-25 * time.Hour)
	aged := streamlog.New(sim.MakeStore[streamlog.Record[kinesisRecord]](nil, "test_kinesis_aged_records"), sim.MakeStore[streamlog.Head](nil, "test_kinesis_aged_heads"))
	aged.Append(partition, old, kinesisRecord{Data: []byte("a")}, kinesisRecord{Data: []byte("b")})
	aged.Append(partition, time.Now(), kinesisRecord{Data: []byte("c")})
	kinesisLog = aged

	if got := kinesisSeqs(kinesisIterate(t, "s", "TRIM_HORIZON", nil)); len(got) != 1 || got[0] != "3" {
		t.Fatalf("TRIM_HORIZON after retention = %v, want only sequence 3", got)
	}
	if got := kinesisSeqs(kinesisIterate(t, "s", "AT_SEQUENCE_NUMBER", map[string]any{"StartingSequenceNumber": "1"})); len(got) != 1 || got[0] != "3" {
		t.Fatalf("a position past the trim horizon reads from it: %v", got)
	}
	if code, out := kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "s", "Data": []byte("d"), "PartitionKey": "k"}); code != http.StatusOK || out["SequenceNumber"] != "4" {
		t.Fatalf("PutRecord after trim = %d %v", code, out)
	}
}

func TestKinesisIteratorTypes(t *testing.T) {
	kinesisTestStores(t)
	kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "s", "ShardCount": 1})
	kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "s", "Data": []byte("a"), "PartitionKey": "k"})
	between := float64(time.Now().UnixMilli())/1000 + 0.001
	time.Sleep(5 * time.Millisecond)
	kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "s", "Data": []byte("b"), "PartitionKey": "k"})

	for typ, want := range map[string][]string{"TRIM_HORIZON": {"1", "2"}, "LATEST": nil} {
		if got := kinesisSeqs(kinesisIterate(t, "s", typ, nil)); len(got) != len(want) || (len(want) > 0 && got[0] != want[0]) {
			t.Errorf("%s = %v, want %v", typ, got, want)
		}
	}
	if got := kinesisSeqs(kinesisIterate(t, "s", "AFTER_SEQUENCE_NUMBER", map[string]any{"StartingSequenceNumber": "1"})); len(got) != 1 || got[0] != "2" {
		t.Errorf("AFTER_SEQUENCE_NUMBER 1 = %v", got)
	}
	if got := kinesisSeqs(kinesisIterate(t, "s", "AT_TIMESTAMP", map[string]any{"Timestamp": between})); len(got) != 1 || got[0] != "2" {
		t.Errorf("AT_TIMESTAMP = %v", got)
	}
	for _, bad := range []map[string]any{
		{"StreamName": "s", "ShardId": "shardId-000000000000", "ShardIteratorType": "SOMEWHERE"},
		{"StreamName": "s", "ShardId": "shardId-000000000000", "ShardIteratorType": "AT_SEQUENCE_NUMBER", "StartingSequenceNumber": "x"},
	} {
		if code, _ := kinesisCall(t, handleKinesisGetShardIterator, bad); code != http.StatusBadRequest {
			t.Errorf("%v = %d", bad, code)
		}
	}
}

func TestKinesisRetentionPeriodBounds(t *testing.T) {
	kinesisTestStores(t)
	kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "s", "ShardCount": 1})
	for _, c := range []struct {
		h     http.HandlerFunc
		hours int
		code  int
	}{
		{handleKinesisDecreaseStreamRetentionPeriod, 12, http.StatusBadRequest},
		{handleKinesisIncreaseStreamRetentionPeriod, 9000, http.StatusBadRequest},
		{handleKinesisIncreaseStreamRetentionPeriod, 48, http.StatusOK},
		{handleKinesisIncreaseStreamRetentionPeriod, 36, http.StatusBadRequest},
		{handleKinesisDecreaseStreamRetentionPeriod, 72, http.StatusBadRequest},
		{handleKinesisDecreaseStreamRetentionPeriod, 24, http.StatusOK},
	} {
		if code, out := kinesisCall(t, c.h, map[string]any{"StreamName": "s", "RetentionPeriodHours": c.hours}); code != c.code {
			t.Errorf("%d hours = %d %v, want %d", c.hours, code, out, c.code)
		}
	}
}

func TestKinesisSelectShardSkipsClosedParents(t *testing.T) {
	shards := kinesisMakeShards(1)
	parent := shards[0]
	parent.SequenceNumberRange = map[string]string{"StartingSequenceNumber": "1", "EndingSequenceNumber": "9"}
	child := kinesisMakeShards(1)[0]
	child.ShardId = "shardId-000000000001"
	stream := KinesisStream{StreamName: "s", Shards: []KinesisShard{parent, child}}
	if got := kinesisSelectShard(stream, "any-key", ""); got != child.ShardId {
		t.Fatalf("record placed on %s, want the open child", got)
	}
}
