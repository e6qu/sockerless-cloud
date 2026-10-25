package main

import (
	"net/http"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// A Kinesis mapping reads a child shard only once its parents are read to
// their end, so a resharded stream's records reach the function in order.
func TestLambdaKinesisMappingReadsParentShardsFirst(t *testing.T) {
	kinesisTestStores(t)
	shards := lambdaESMShards
	t.Cleanup(func() { lambdaESMShards = shards })
	lambdaESMShards = sim.NewStateStore[lambdaESMShardState]()
	if code, out := kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "lineage", "ShardCount": 1}); code != http.StatusOK {
		t.Fatalf("CreateStream: %d %v", code, out)
	}
	if code, out := kinesisCall(t, handleKinesisUpdateShardCount, map[string]any{"StreamName": "lineage", "TargetShardCount": 2, "ScalingType": "UNIFORM_SCALING"}); code != http.StatusOK {
		t.Fatalf("UpdateShardCount: %d %v", code, out)
	}
	bg.Await()
	stream, _ := kinesisStreams.Get("lineage")
	mapping := LambdaEventSourceMapping{UUID: "m", State: "Enabled", EventSourceArn: stream.StreamARN}

	var parent string
	var children []string
	for _, shard := range stream.Shards {
		if shard.ParentShardId == "" {
			parent = shard.ShardId
		} else {
			children = append(children, shard.ShardId)
		}
	}
	if parent == "" || len(children) != 2 {
		t.Fatalf("resharding left shards %+v", stream.Shards)
	}
	if keys := lambdaKinesisShardKeys(mapping); len(keys) != 1 || keys[0] != lambdaESMShardKey("m", parent) {
		t.Fatalf("pollers before the parent is read: %q, want the parent's alone", keys)
	}
	lambdaESMShards.Put(lambdaESMShardKey("m", parent), lambdaESMShardState{Done: true})
	keys := lambdaKinesisShardKeys(mapping)
	if len(keys) != 2 || keys[0] != lambdaESMShardKey("m", children[0]) || keys[1] != lambdaESMShardKey("m", children[1]) {
		t.Fatalf("pollers once the parent is read: %q, want the two children", keys)
	}
}
