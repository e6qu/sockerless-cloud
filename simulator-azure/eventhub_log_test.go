package main

import (
	"strconv"
	"testing"
	"time"

	amqp "github.com/Azure/go-amqp"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/streamlog"
)

func ehTestStores(t *testing.T, props map[string]any) {
	t.Helper()
	hubs, log := ehEventHubs, ehLog
	t.Cleanup(func() { ehEventHubs, ehLog = hubs, log })
	ehEventHubs = sim.MakeStore[EHEventHub](nil, "test_eh_hubs")
	ehLog = streamlog.New(
		sim.MakeStore[streamlog.Record[ehEvent]](nil, "test_eh_records"),
		sim.MakeStore[streamlog.Head](nil, "test_eh_heads"))
	id := ehEventHubID("sub", "rg", "ns", "hub")
	ehEventHubs.Put(id, EHEventHub{ID: id, Name: "hub", Properties: props})
}

func ehTestRead(t *testing.T, seq int64) (string, int64, int64, bool) {
	t.Helper()
	body, next, ok := ehAMQPNextEvent("ns", "hub/ConsumerGroups/$Default/Partitions/0", seq)
	if !ok {
		return "", 0, next, false
	}
	var m amqp.Message
	if err := m.UnmarshalBinary(body); err != nil {
		t.Fatal(err)
	}
	return string(m.GetData()), m.Annotations["x-opt-sequence-number"].(int64), next, true
}

func TestEventHubsRetentionAgesOutEvents(t *testing.T) {
	ehTestStores(t, map[string]any{"partitionCount": float64(1), "retentionDescription": map[string]any{"retentionTimeInHours": float64(1)}})
	if got := ehRetention(EHEventHub{Properties: map[string]any{"messageRetentionInDays": float64(3)}}); got != 72*time.Hour {
		t.Fatalf("messageRetentionInDays retention = %v", got)
	}
	key := ehPartitionKey("ns", "hub", "0")
	ehLog.Append(key, time.Now().Add(-2*time.Hour), ehEvent{AMQP: mustMarshalAMQP(t, "old")})
	ehAMQPEnqueue("ns", "hub/Partitions/0", &amqp.Message{Data: [][]byte{[]byte("new")}})

	data, seq, next, ok := ehTestRead(t, 0)
	if !ok || data != "new" || seq != 1 || next != 2 {
		t.Fatalf("read from 0 = %q seq %d next %d ok %v, want the event after the aged-out one", data, seq, next, ok)
	}
	if head := ehLog.Head(key); head.First != 1 {
		t.Fatalf("head after retention = %+v", head)
	}
}

func mustMarshalAMQP(t *testing.T, data string) []byte {
	t.Helper()
	b, err := (&amqp.Message{Data: [][]byte{[]byte(data)}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEventHubsStartPositionFilters(t *testing.T) {
	ehTestStores(t, map[string]any{"partitionCount": float64(1)})
	start := time.Now()
	for _, d := range []string{"a", "b", "c"} {
		ehAMQPEnqueue("ns", "hub/Partitions/0", &amqp.Message{Data: [][]byte{[]byte(d)}})
	}
	address := "hub/ConsumerGroups/$Default/Partitions/0"
	for filter, want := range map[string]int64{
		"":                                    0,
		"amqp.annotation.x-opt-offset > '-1'": 0,
		"amqp.annotation.x-opt-offset > '@latest'":    3,
		"amqp.annotation.x-opt-offset > '0'":          1,
		"amqp.annotation.x-opt-offset >= '1'":         1,
		"amqp.annotation.x-opt-sequence-number > '1'": 2,
		"amqp.annotation.x-opt-enqueued-time >= '" + strconv.FormatInt(start.Add(-time.Second).UnixMilli(), 10) + "'":  0,
		"amqp.annotation.x-opt-enqueued-time > '" + strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + "'": 3,
	} {
		if got := ehStartPosition("ns", address, filter); got != want {
			t.Errorf("%q starts at %d, want %d", filter, got, want)
		}
	}
}

func TestEventHubsBatchKeepsEachEventsProperties(t *testing.T) {
	ehTestStores(t, map[string]any{"partitionCount": float64(1)})
	inner, err := (&amqp.Message{Data: [][]byte{[]byte("e1")}, ApplicationProperties: map[string]any{"k": "v"}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	ehAMQPEnqueue("ns", "hub", &amqp.Message{Data: [][]byte{inner}})
	body, _, ok := ehAMQPNextEvent("ns", "hub/ConsumerGroups/$Default/Partitions/0", 0)
	if !ok {
		t.Fatal("no event")
	}
	var m amqp.Message
	if err := m.UnmarshalBinary(body); err != nil {
		t.Fatal(err)
	}
	if string(m.GetData()) != "e1" || m.ApplicationProperties["k"] != "v" || m.Annotations["x-opt-offset"] != "0" {
		t.Fatalf("event = %+v", m)
	}
}
