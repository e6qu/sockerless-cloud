package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Kinesis Data Streams enhanced fan-out streaming.
//
// SubscribeToShard is an awsJson1.1 request (dispatched off the X-Amz-Target
// header through the shared AWSRouter) whose response is an AWS event stream
// (Content-Type application/vnd.amazon.eventstream). The handler takes over the
// raw http.ResponseWriter and writes SubscribeToShardEvent frames using the
// same awsEventStreamMessage framing Lambda's InvokeWithResponseStream uses, so
// aws-sdk-go-v2's eventstream decoder reassembles them natively.
func registerKinesisStreaming(r *sim.AWSRouter, srv *sim.Server) {
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
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ConsumerARN == "" || req.ShardId == "" {
		sim.AWSError(w, "InvalidArgumentException",
			"ConsumerARN and ShardId are required", http.StatusBadRequest)
		return
	}

	consumer, ok := kinesisConsumers.Get(kinesisConsumerKey(req.ConsumerARN))
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException",
			"Consumer not found: "+req.ConsumerARN, http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByARN(consumer.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException",
			"Stream not found for consumer", http.StatusBadRequest)
		return
	}
	if !kinesisHasShard(stream, req.ShardId) {
		sim.AWSError(w, "ResourceNotFoundException",
			"Shard not found: "+req.ShardId, http.StatusBadRequest)
		return
	}

	records, _ := kinesisRecords.Get(kinesisShardRecordKey(stream.StreamName, req.ShardId))

	// Honor the StartingPosition the consumer asked for, mirroring
	// GetShardIterator's index selection.
	start := 0
	switch req.StartingPosition.Type {
	case "LATEST":
		start = len(records)
	case "AT_SEQUENCE_NUMBER", "AFTER_SEQUENCE_NUMBER":
		if seq, err := strconv.Atoi(req.StartingPosition.SequenceNumber); err == nil && seq > 0 {
			start = seq - 1
			if req.StartingPosition.Type == "AFTER_SEQUENCE_NUMBER" {
				start = seq
			}
		}
	case "", "TRIM_HORIZON", "AT_TIMESTAMP":
		start = 0
	default:
		start = 0
	}
	if start > len(records) {
		start = len(records)
	}

	selected := records[start:]
	outRecords := make([]map[string]any, 0, len(selected))
	for _, rec := range selected {
		outRecords = append(outRecords, map[string]any{
			"SequenceNumber":              rec.SequenceNumber,
			"ApproximateArrivalTimestamp": rec.ApproximateArrivalTimestamp,
			"Data":                        rec.Data,
			"PartitionKey":                rec.PartitionKey,
			"EncryptionType":              "NONE",
		})
	}

	// ContinuationSequenceNumber is the next sequence number a follow-up
	// SubscribeToShard call would resume from. MillisBehindLatest is 0 because
	// the sim has streamed every stored record (it is caught up to the tip).
	continuation := strconv.Itoa(len(records) + 1)

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

	_, _ = w.Write(kinesisEventStreamFrame("SubscribeToShardEvent", map[string]any{
		"Records":                    outRecords,
		"ContinuationSequenceNumber": continuation,
		"MillisBehindLatest":         0,
		"ChildShards":                []any{},
	}))
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
