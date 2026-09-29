package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func kinesisDescribeShards(t *testing.T, stream string) []KinesisShard {
	t.Helper()
	stored, ok := kinesisStreams.Get(stream)
	if !ok {
		t.Fatalf("stream %s is gone", stream)
	}
	return stored.Shards
}

// UpdateShardCount reaches the target by splitting and merging: the parents
// close with their last sequence number, the open shards are the uniform
// ranges, and every open shard names the closed shards it came from.
func TestKinesisUpdateShardCountSplitsAndMergesShards(t *testing.T) {
	kinesisTestStores(t)
	if code, out := kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "reshard", "ShardCount": 2}); code != http.StatusOK {
		t.Fatalf("CreateStream: %d %v", code, out)
	}
	for _, key := range []string{"a", "b", "c", "d"} {
		if code, out := kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "reshard", "PartitionKey": key, "Data": []byte(key)}); code != http.StatusOK {
			t.Fatalf("PutRecord: %d %v", code, out)
		}
	}
	for _, step := range []struct{ target, current int64 }{{3, 2}, {2, 3}, {4, 2}} {
		before := map[string]bool{}
		for _, shard := range kinesisDescribeShards(t, "reshard") {
			before[shard.ShardId] = true
		}
		code, out := kinesisCall(t, handleKinesisUpdateShardCount, map[string]any{"StreamName": "reshard", "TargetShardCount": step.target, "ScalingType": "UNIFORM_SCALING"})
		if code != http.StatusOK || out["CurrentShardCount"] != float64(step.current) || out["TargetShardCount"] != float64(step.target) {
			t.Fatalf("UpdateShardCount to %d: %d %v, want current %d", step.target, code, out, step.current)
		}
		shards := kinesisDescribeShards(t, "reshard")
		byID := map[string]KinesisShard{}
		for _, shard := range shards {
			byID[shard.ShardId] = shard
		}
		for id := range before {
			if _, kept := byID[id]; !kept {
				t.Fatalf("shard %s disappeared from the lineage", id)
			}
		}
		open := kinesisOpenShards(KinesisStream{Shards: shards})
		uniform := kinesisMakeShards(step.target)
		if len(open) != len(uniform) {
			t.Fatalf("%d open shards after scaling to %d", len(open), step.target)
		}
		for i, shard := range open {
			if shard.HashKeyRange["StartingHashKey"] != uniform[i].HashKeyRange["StartingHashKey"] ||
				shard.HashKeyRange["EndingHashKey"] != uniform[i].HashKeyRange["EndingHashKey"] {
				t.Fatalf("open shard %d covers %v, want the uniform range %v", i, shard.HashKeyRange, uniform[i].HashKeyRange)
			}
			if before[shard.ShardId] {
				continue
			}
			for _, parentID := range []string{shard.ParentShardId, shard.AdjacentParentShardId} {
				if parentID == "" {
					continue
				}
				if parent, ok := byID[parentID]; !ok || !kinesisShardClosed(parent) {
					t.Fatalf("child %s names parent %s, which is not a closed shard", shard.ShardId, parentID)
				}
			}
			if shard.ParentShardId == "" {
				t.Fatalf("new shard %s names no parent", shard.ShardId)
			}
		}
	}
	merged := false
	for _, shard := range kinesisDescribeShards(t, "reshard") {
		merged = merged || shard.AdjacentParentShardId != ""
	}
	if !merged {
		t.Fatal("scaling down merged no shards")
	}
}

// A closed shard's EndingSequenceNumber is its last record's, and GetRecords
// reaching the end of it returns no NextShardIterator but the child shards.
func TestKinesisDrainedClosedShardEndsWithItsChildren(t *testing.T) {
	kinesisTestStores(t)
	if code, out := kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "drain", "ShardCount": 1}); code != http.StatusOK {
		t.Fatalf("CreateStream: %d %v", code, out)
	}
	for _, key := range []string{"a", "b"} {
		kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "drain", "PartitionKey": key, "Data": []byte(key)})
	}
	code, out := kinesisCall(t, handleKinesisSplitShard, map[string]any{
		"StreamName": "drain", "ShardToSplit": "shardId-000000000000", "NewStartingHashKey": "170141183460469231731687303715884105728",
	})
	if code != http.StatusOK {
		t.Fatalf("SplitShard: %d %v", code, out)
	}
	parent, _ := kinesisFindShard(KinesisStream{Shards: kinesisDescribeShards(t, "drain")}, "shardId-000000000000")
	if parent.SequenceNumberRange["EndingSequenceNumber"] != "2" {
		t.Fatalf("closed parent %v, want EndingSequenceNumber 2, its last record", parent.SequenceNumberRange)
	}
	code, out = kinesisCall(t, handleKinesisSplitShard, map[string]any{
		"StreamName": "drain", "ShardToSplit": "shardId-000000000000", "NewStartingHashKey": "1",
	})
	if code != http.StatusBadRequest || out["__type"] != "InvalidArgumentException" || !strings.Contains(out["message"].(string), "already been merged or split") {
		t.Fatalf("SplitShard of a closed shard: %d %v, want InvalidArgumentException", code, out)
	}

	_, iterator := kinesisCall(t, handleKinesisGetShardIterator, map[string]any{"StreamName": "drain", "ShardId": "shardId-000000000000", "ShardIteratorType": "TRIM_HORIZON"})
	code, out = kinesisCall(t, handleKinesisGetRecords, map[string]any{"ShardIterator": iterator["ShardIterator"]})
	if code != http.StatusOK || len(out["Records"].([]any)) != 2 {
		t.Fatalf("GetRecords on the closed parent: %d %v, want its two records", code, out)
	}
	if next, present := out["NextShardIterator"]; present {
		t.Fatalf("drained closed shard returned NextShardIterator %v", next)
	}
	children := out["ChildShards"].([]any)
	if len(children) != 2 {
		t.Fatalf("ChildShards %v, want the two split children", children)
	}
	for _, child := range children {
		parents := child.(map[string]any)["ParentShards"].([]any)
		if len(parents) != 1 || parents[0] != "shardId-000000000000" {
			t.Fatalf("child %v, want the split parent as its only parent", child)
		}
	}

	_, iterator = kinesisCall(t, handleKinesisGetShardIterator, map[string]any{"StreamName": "drain", "ShardId": "shardId-000000000001", "ShardIteratorType": "TRIM_HORIZON"})
	_, out = kinesisCall(t, handleKinesisGetRecords, map[string]any{"ShardIterator": iterator["ShardIterator"]})
	if _, present := out["NextShardIterator"]; !present {
		t.Fatalf("GetRecords on an open shard %v, want a NextShardIterator", out)
	}
}

// A shard iterator expires five minutes after GetShardIterator or GetRecords
// returned it.
func TestKinesisShardIteratorsExpireAfterFiveMinutes(t *testing.T) {
	kinesisTestStores(t)
	if code, out := kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "expiry", "ShardCount": 1}); code != http.StatusOK {
		t.Fatalf("CreateStream: %d %v", code, out)
	}
	kinesisCall(t, handleKinesisPutRecord, map[string]any{"StreamName": "expiry", "PartitionKey": "a", "Data": []byte("a")})
	now := time.Now()
	kinesisIterators.Put("fresh", kinesisIterator{StreamName: "expiry", ShardID: "shardId-000000000000", IssuedAt: now.Add(-4 * time.Minute).UnixMilli()})
	kinesisIterators.Put("stale", kinesisIterator{StreamName: "expiry", ShardID: "shardId-000000000000", IssuedAt: now.Add(-6 * time.Minute).UnixMilli()})

	code, out := kinesisCall(t, handleKinesisGetRecords, map[string]any{"ShardIterator": "fresh"})
	if code != http.StatusOK || len(out["Records"].([]any)) != 1 {
		t.Fatalf("GetRecords with a four-minute-old iterator: %d %v", code, out)
	}
	next, _ := kinesisIterators.Get(out["NextShardIterator"].(string))
	if time.Since(time.UnixMilli(next.IssuedAt)) > time.Minute {
		t.Fatalf("NextShardIterator issued at %d, want now", next.IssuedAt)
	}
	code, out = kinesisCall(t, handleKinesisGetRecords, map[string]any{"ShardIterator": "stale"})
	if code != http.StatusBadRequest || out["__type"] != "ExpiredIteratorException" ||
		!strings.Contains(out["message"].(string), "tolerated delay of 300000 milliseconds") {
		t.Fatalf("GetRecords with a six-minute-old iterator: %d %v, want ExpiredIteratorException", code, out)
	}
	if _, kept := kinesisIterators.Get("stale"); kept {
		t.Fatal("the expired iterator is still stored")
	}
}
