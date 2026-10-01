package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// SubscribeToShard is an awsJson1.1 request whose response is an AWS event
// stream (Content-Type application/vnd.amazon.eventstream). The handler holds
// the response for the five-minute subscription and writes a
// SubscribeToShardEvent frame each time the shard's log grows, using the same
// awsEventStreamMessage framing Lambda's InvokeWithResponseStream uses.
func registerKinesisStreaming(r *AWSRouter, srv *sim.Server) {
	_ = srv
	r.Register("Kinesis_20131202.SubscribeToShard", handleKinesisSubscribeToShard)
}

const (
	// kinesisSubscriptionLifetime: "your consumer starts receiving events of
	// type SubscribeToShardEvent over the HTTP/2 connection for up to 5
	// minutes".
	kinesisSubscriptionLifetime = 5 * time.Minute
	// kinesisSubscriptionTakeoverAfter: a second call "within 5 seconds of a
	// successful call" fails with ResourceInUseException; a later one takes
	// the subscription over.
	kinesisSubscriptionTakeoverAfter = 5 * time.Second
	// One event carries at most what one GetRecords call returns: 10,000
	// records or 10 MiB of data.
	kinesisEventMaxRecords = 10000
	kinesisEventMaxBytes   = 10 << 20
)

// kinesisSignals wakes held SubscribeToShard streams. A key maps to a channel
// that kinesisNotify closes when what it names changed: "shard:<partition>"
// when a record lands in the shard or the shard closes or goes away,
// "consumer:<ARN>" when the consumer is deregistered.
var kinesisSignals = struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}{ch: map[string]chan struct{}{}}

func kinesisWatch(key string) <-chan struct{} {
	kinesisSignals.mu.Lock()
	defer kinesisSignals.mu.Unlock()
	c, ok := kinesisSignals.ch[key]
	if !ok {
		c = make(chan struct{})
		kinesisSignals.ch[key] = c
	}
	return c
}

func kinesisNotify(key string) {
	kinesisSignals.mu.Lock()
	defer kinesisSignals.mu.Unlock()
	if c, ok := kinesisSignals.ch[key]; ok {
		close(c)
		delete(kinesisSignals.ch, key)
	}
}

func kinesisShardSignalKey(partition string) string { return "shard:" + partition }

func kinesisConsumerSignalKey(consumerARN string) string { return "consumer:" + consumerARN }

// kinesisNotifyStream wakes the subscriptions to every shard of a stream.
func kinesisNotifyStream(stream KinesisStream) {
	for _, shard := range stream.Shards {
		kinesisNotify(kinesisShardSignalKey(kinesisShardRecordKey(stream.StreamName, shard.ShardId)))
	}
}

// kinesisSubscription is one held SubscribeToShard call. takeover closes when
// a later call for the same consumer and shard takes the subscription over.
type kinesisSubscription struct {
	started  time.Time
	takeover chan struct{}
}

var kinesisSubscriptions = struct {
	mu     sync.Mutex
	active map[string]*kinesisSubscription
}{active: map[string]*kinesisSubscription{}}

func kinesisSubscriptionKey(consumerARN, shardID string) string {
	return consumerARN + "\x00" + shardID
}

// kinesisSubscribe starts a subscription, taking over an active one that is
// at least five seconds old. It reports false when the active one is younger.
func kinesisSubscribe(key string, now time.Time) (*kinesisSubscription, bool) {
	kinesisSubscriptions.mu.Lock()
	defer kinesisSubscriptions.mu.Unlock()
	if prev, ok := kinesisSubscriptions.active[key]; ok {
		if now.Sub(prev.started) < kinesisSubscriptionTakeoverAfter {
			return nil, false
		}
		close(prev.takeover)
	}
	sub := &kinesisSubscription{started: now, takeover: make(chan struct{})}
	kinesisSubscriptions.active[key] = sub
	return sub, true
}

func kinesisUnsubscribe(key string, sub *kinesisSubscription) {
	kinesisSubscriptions.mu.Lock()
	defer kinesisSubscriptions.mu.Unlock()
	if kinesisSubscriptions.active[key] == sub {
		delete(kinesisSubscriptions.active, key)
	}
}

func handleKinesisSubscribeToShard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConsumerARN      string `json:"ConsumerARN"`
		ShardId          string `json:"ShardId"`
		StartingPosition *struct {
			Type           string  `json:"Type"`
			SequenceNumber string  `json:"SequenceNumber"`
			Timestamp      float64 `json:"Timestamp"`
		} `json:"StartingPosition"`
		DryRun bool `json:"DryRun"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ConsumerARN == "" || req.ShardId == "" || req.StartingPosition == nil {
		AWSError(w, "InvalidArgumentException",
			"ConsumerARN, ShardId and StartingPosition are required", http.StatusBadRequest)
		return
	}

	consumer, ok := kinesisConsumers.Get(kinesisConsumerKey(req.ConsumerARN))
	if !ok {
		AWSError(w, "ResourceNotFoundException",
			"Consumer not found: "+req.ConsumerARN, http.StatusBadRequest)
		return
	}
	if consumer.ConsumerStatus != "ACTIVE" {
		AWSError(w, "ResourceInUseException",
			"Consumer "+req.ConsumerARN+" is not ACTIVE", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByARN(consumer.StreamARN)
	if !ok {
		AWSError(w, "ResourceNotFoundException",
			"Stream not found for consumer", http.StatusBadRequest)
		return
	}
	if !kinesisHasShard(stream, req.ShardId) {
		AWSError(w, "ResourceNotFoundException",
			"Shard not found: "+req.ShardId, http.StatusBadRequest)
		return
	}
	start, problem := kinesisStartPosition(stream, req.ShardId, req.StartingPosition.Type,
		req.StartingPosition.SequenceNumber, req.StartingPosition.Timestamp)
	if problem != "" {
		AWSError(w, "InvalidArgumentException", problem, http.StatusBadRequest)
		return
	}
	if req.DryRun {
		AWSError(w, "DryRunOperationException",
			"The request would have succeeded, but the DryRun parameter was specified.", http.StatusBadRequest)
		return
	}

	subKey := kinesisSubscriptionKey(req.ConsumerARN, req.ShardId)
	sub, ok := kinesisSubscribe(subKey, time.Now())
	if !ok {
		AWSError(w, "ResourceInUseException",
			"Another active subscription exists for this consumer: "+req.ConsumerARN, http.StatusBadRequest)
		return
	}
	defer kinesisUnsubscribe(subKey, sub)

	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	// The controller reaches the connection through the middleware writers
	// that wrap w, so each frame leaves as it is written.
	controller := http.NewResponseController(w)
	send := func(frame []byte) bool {
		if _, err := w.Write(frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}

	// The aws-sdk-go-v2 eventstream deserializer blocks the SubscribeToShard
	// call until it reads the initial-response message, whose payload is the
	// structural output: SubscribeToShardOutput has only the event stream, so
	// an empty document.
	if !send(awsEventStreamInitialResponse([]byte("{}"))) {
		return
	}

	sim.DeclareWait(r.Context(), kinesisSubscriptionLifetime)
	expiry := time.NewTimer(kinesisSubscriptionLifetime)
	defer expiry.Stop()
	deregistered := kinesisWatch(kinesisConsumerSignalKey(req.ConsumerARN))
	partition := kinesisShardRecordKey(stream.StreamName, req.ShardId)
	next := start
	first := true
	for {
		grew := kinesisWatch(kinesisShardSignalKey(partition))
		if _, ok := kinesisConsumers.Get(kinesisConsumerKey(req.ConsumerARN)); !ok {
			send(kinesisEventStreamException("ResourceNotFoundException",
				"Consumer "+req.ConsumerARN+" not found."))
			return
		}
		stream, ok = kinesisStreamByARN(consumer.StreamARN)
		if !ok {
			send(kinesisEventStreamException("ResourceNotFoundException",
				"Stream "+consumer.StreamARN+" not found."))
			return
		}
		event, after, end := kinesisSubscribeEvent(stream, req.ShardId, partition, next)
		if first || after > next || end {
			if !send(kinesisEventStreamFrame("SubscribeToShardEvent", event)) {
				return
			}
		}
		first = false
		next = after
		if end {
			return
		}
		if next < kinesisLog.Head(partition).Next {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-expiry.C:
			return
		case <-sub.takeover:
			send(kinesisEventStreamException("ResourceInUseException",
				"Another active subscription exists for this consumer: "+req.ConsumerARN))
			return
		case <-deregistered:
		case <-grew:
		}
	}
}

// kinesisSubscribeEvent reads the shard from position next into one
// SubscribeToShardEvent and returns the position after it. end reports the
// event that drains a closed shard: it names the children and carries no
// ContinuationSequenceNumber.
func kinesisSubscribeEvent(stream KinesisStream, shardID, partition string, next int64) (map[string]any, int64, bool) {
	now := time.Now()
	kinesisTrim(stream, shardID, now)
	next = max(next, kinesisLog.Head(partition).First)
	out := []map[string]any{}
	size := 0
	for _, rec := range kinesisLog.Read(partition, next, kinesisEventMaxRecords) {
		size += len(rec.Value.Data) + len(rec.Value.PartitionKey)
		if len(out) > 0 && size > kinesisEventMaxBytes {
			break
		}
		out = append(out, kinesisRecordJSON(rec))
		next = rec.Seq + 1
	}
	event := map[string]any{
		"Records":            out,
		"MillisBehindLatest": kinesisMillisBehind(partition, next, now),
	}
	shard, _ := kinesisFindShard(stream, shardID)
	if kinesisShardClosed(shard) && next >= kinesisLog.Head(partition).Next {
		event["ChildShards"] = kinesisChildShards(stream, shardID)
		return event, next, true
	}
	event["ContinuationSequenceNumber"] = kinesisSequenceNumber(next - 1)
	return event, next, false
}

// awsEventStreamInitialResponse encodes the initial-response event-stream frame
// the aws-sdk-go-v2 eventstream deserializer reads synchronously before
// returning a streaming operation's output. Its payload is the operation's
// structural (non-event) output document — empty for the streaming ops whose
// output is event-only. The :event-type header is the reserved "initial-response"
// value the SDK dispatches on.
func awsEventStreamInitialResponse(payload []byte) []byte {
	return awsEventStreamMessage(map[string]string{
		":message-type": "event",
		":event-type":   "initial-response",
		":content-type": "application/json",
	}, payload)
}

// kinesisEventStreamFrame encodes one event-stream frame for the given union
// member name and JSON payload, using the shared awsEventStreamMessage framing.
// The :event-type header carries the union member name exactly as the smithy
// model spells it, which is how aws-sdk-go-v2's eventstream decoder dispatches
// to the matching SubscribeToShardEventStream member type.
func kinesisEventStreamFrame(eventType string, payload any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		// A response-stream payload assembled from sim-owned types must always
		// marshal; an error here is a sim bug, so surface an honest empty frame
		// body rather than silently corrupting the wire shape.
		body = []byte("{}")
	}
	return awsEventStreamMessage(map[string]string{
		":message-type": "event",
		":event-type":   eventType,
		":content-type": "application/x-amz-json-1.1",
	}, body)
}

// kinesisEventStreamException encodes an exception member of
// SubscribeToShardEventStream, which ends the stream on the client.
func kinesisEventStreamException(exceptionType, message string) []byte {
	body, _ := json.Marshal(map[string]string{"message": message})
	return awsEventStreamMessage(map[string]string{
		":message-type":   "exception",
		":exception-type": exceptionType,
		":content-type":   "application/x-amz-json-1.1",
	}, body)
}
