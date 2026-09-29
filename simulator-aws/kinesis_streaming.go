package main

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Kinesis Data Streams enhanced fan-out streaming.
//
// SubscribeToShard is an awsJson1.1 request (dispatched off the X-Amz-Target
// header through the shared AWSRouter) whose response is an AWS event stream
// (Content-Type application/vnd.amazon.eventstream). The handler takes over the
// raw http.ResponseWriter and writes SubscribeToShardEvent frames using the
// same awsEventStreamMessage framing Lambda's InvokeWithResponseStream uses, so
// aws-sdk-go-v2's eventstream decoder reassembles them natively.
func registerKinesisStreaming(r *AWSRouter, srv *sim.Server) {
	_ = srv
	r.Register("Kinesis_20131202.SubscribeToShard", handleKinesisSubscribeToShard)
}

// handleKinesisSubscribeToShard pushes the records currently in the subscribed
// shard to the consumer over the event stream, then ends. Real SubscribeToShard
// holds the HTTP/2 connection open for up to 5 minutes pushing
// SubscribeToShardEvent frames; the sim emits the stored records (honoring the
// StartingPosition) in a single deterministic SubscribeToShardEvent and closes,
// so the SDK reader completes rather than hanging. A zero-record shard yields an
// event with an empty Records array (the honest-empty case).
func handleKinesisSubscribeToShard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConsumerARN      string `json:"ConsumerARN"`
		ShardId          string `json:"ShardId"`
		StartingPosition struct {
			Type           string  `json:"Type"`
			SequenceNumber string  `json:"SequenceNumber"`
			Timestamp      float64 `json:"Timestamp"`
		} `json:"StartingPosition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ConsumerARN == "" || req.ShardId == "" {
		AWSError(w, "InvalidArgumentException",
			"ConsumerARN and ShardId are required", http.StatusBadRequest)
		return
	}

	consumer, ok := kinesisConsumers.Get(kinesisConsumerKey(req.ConsumerARN))
	if !ok {
		AWSError(w, "ResourceNotFoundException",
			"Consumer not found: "+req.ConsumerARN, http.StatusBadRequest)
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
	partition := kinesisShardRecordKey(stream.StreamName, req.ShardId)
	next := max(start, kinesisLog.Head(partition).First)
	selected := kinesisLog.Read(partition, next, math.MaxInt)
	outRecords := make([]map[string]any, 0, len(selected))
	for _, rec := range selected {
		outRecords = append(outRecords, kinesisRecordJSON(rec))
		next = rec.Seq + 1
	}

	// The event streams every stored record, so a follow-up SubscribeToShard
	// resumes at the tip.
	event := map[string]any{
		"Records":            outRecords,
		"MillisBehindLatest": 0,
		"ChildShards":        []map[string]any{},
	}
	if shard, _ := kinesisFindShard(stream, req.ShardId); kinesisShardClosed(shard) && next >= kinesisLog.Head(partition).Next {
		// The end of a closed shard names its children and no continuation.
		event["ChildShards"] = kinesisChildShards(stream, req.ShardId)
	} else {
		event["ContinuationSequenceNumber"] = kinesisSequenceNumber(next)
	}

	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	// The aws-sdk-go-v2 eventstream deserializer blocks the SubscribeToShard call
	// until it reads the initial-response message (it populates the structural
	// SubscribeToShardOutput, which has no non-event members, so its payload is an
	// empty document). It must precede any data event, or the reader goroutine
	// deadlocks pushing the first event before a consumer attaches.
	_, _ = w.Write(awsEventStreamInitialResponse([]byte("{}")))
	if flusher != nil {
		flusher.Flush()
	}

	_, _ = w.Write(kinesisEventStreamFrame("SubscribeToShardEvent", event))
	if flusher != nil {
		flusher.Flush()
	}
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
