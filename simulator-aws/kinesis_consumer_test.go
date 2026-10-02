package main

import (
	"net/http"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// RegisterStreamConsumer answers with the consumer CREATING and activates it
// behind the request; DeregisterStreamConsumer answers with it DELETING and
// removes it behind the request. SubscribeToShard refuses a consumer that is
// not ACTIVE.
func TestKinesisConsumerMovesThroughCreatingAndDeleting(t *testing.T) {
	kinesisTestStores(t)
	consumers, policies := kinesisConsumers, iamResourcePolicies
	t.Cleanup(func() { kinesisConsumers, iamResourcePolicies = consumers, policies })
	kinesisConsumers = sim.MakeStore[KinesisConsumer](nil, "test_kinesis_consumers")
	iamResourcePolicies = sim.MakeStore[IAMResourcePolicy](nil, "test_iam_resource_policies")

	if code, out := kinesisCall(t, handleKinesisCreateStream, map[string]any{"StreamName": "efo", "ShardCount": 1}); code != http.StatusOK {
		t.Fatalf("CreateStream: %d %v", code, out)
	}
	bg.Await()
	stream, _ := kinesisStreams.Get("efo")

	code, out := kinesisCall(t, handleKinesisRegisterStreamConsumer, map[string]any{"StreamARN": stream.StreamARN, "ConsumerName": "reader"})
	consumer, _ := out["Consumer"].(map[string]any)
	if code != http.StatusOK || consumer["ConsumerStatus"] != "CREATING" {
		t.Fatalf("RegisterStreamConsumer: %d %v, want the consumer CREATING", code, out)
	}
	arn, _ := consumer["ConsumerARN"].(string)
	bg.Await()
	if stored, _ := kinesisConsumers.Get(arn); stored.ConsumerStatus != "ACTIVE" {
		t.Fatalf("consumer is %s once its registration finished, want ACTIVE", stored.ConsumerStatus)
	}

	kinesisConsumers.Update(arn, func(c *KinesisConsumer) { c.ConsumerStatus = "CREATING" })
	subscribe := map[string]any{"ConsumerARN": arn, "ShardId": "shardId-000000000000", "StartingPosition": map[string]any{"Type": "LATEST"}}
	if code, out := kinesisCall(t, handleKinesisSubscribeToShard, subscribe); code != http.StatusBadRequest || out["__type"] != "ResourceInUseException" {
		t.Fatalf("SubscribeToShard on a CREATING consumer: %d %v, want ResourceInUseException", code, out)
	}
	kinesisConsumers.Update(arn, func(c *KinesisConsumer) { c.ConsumerStatus = "ACTIVE" })

	if code, out := kinesisCall(t, handleKinesisDeregisterStreamConsumer, map[string]any{"ConsumerARN": arn}); code != http.StatusOK {
		t.Fatalf("DeregisterStreamConsumer: %d %v", code, out)
	}
	bg.Await()
	if _, ok := kinesisConsumers.Get(arn); ok {
		t.Fatal("the consumer outlived its deregistration")
	}

	code, out = kinesisCall(t, handleKinesisRegisterStreamConsumer, map[string]any{"StreamARN": stream.StreamARN, "ConsumerName": "leaving"})
	if code != http.StatusOK {
		t.Fatalf("RegisterStreamConsumer: %d %v", code, out)
	}
	leaving, _ := out["Consumer"].(map[string]any)["ConsumerARN"].(string)
	bg.Await()
	kinesisConsumers.Update(leaving, func(c *KinesisConsumer) { c.ConsumerStatus = "DELETING" })
	subscribe["ConsumerARN"] = leaving
	if code, out := kinesisCall(t, handleKinesisSubscribeToShard, subscribe); code != http.StatusBadRequest || out["__type"] != "ResourceInUseException" {
		t.Fatalf("SubscribeToShard on a DELETING consumer: %d %v, want ResourceInUseException", code, out)
	}
	if code, out := kinesisCall(t, handleKinesisRegisterStreamConsumer, map[string]any{"StreamARN": stream.StreamARN, "ConsumerName": "leaving"}); code != http.StatusBadRequest || out["__type"] != "ResourceInUseException" {
		t.Fatalf("RegisterStreamConsumer over a DELETING consumer: %d %v, want ResourceInUseException", code, out)
	}
}
