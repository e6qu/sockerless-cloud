package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/funcrt"
	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// An event source mapping over an Amazon Kinesis data stream runs one poller
// per shard. "Lambda processes records in each shard in order. It stops
// processing additional records in a shard if your function returns an
// error" (with-kinesis.md.gz), and it reads a child shard only once its
// parents are read to their end. Each poller checkpoints the last sequence
// number its function handled, so a restarted poller resumes after it.

type lambdaESMShardState struct {
	// Sequence is the last sequence number the function handled or the
	// mapping discarded.
	Sequence string `json:"sequence,omitempty"`
	// StartAt pins StartingPosition LATEST to the moment the poller first
	// read the shard, so a poller that restarts reads what arrived since.
	StartAt float64 `json:"startAt,omitempty"`
	// Done reports a closed shard read to its end.
	Done bool `json:"done,omitempty"`
}

var lambdaESMShards sim.Store[lambdaESMShardState]

// lambdaEnvironmentChanges announces a function's name whenever one of its
// invocations ends or its concurrency changes, waking a stream poller whose
// batch the function's concurrency throttled.
var lambdaEnvironmentChanges = signals.New[string]()

func lambdaESMShardKey(uuid, shardID string) string { return uuid + "\x00" + shardID }

func lambdaESMPollsKinesis(mapping LambdaEventSourceMapping) bool {
	return mapping.State == "Enabled" && strings.HasPrefix(mapping.EventSourceArn, "arn:aws:kinesis:")
}

// lambdaKinesisStreamName is the stream an ARN of the form
// arn:aws:kinesis:<region>:<account>:stream/<name> names.
func lambdaKinesisStreamName(arn string) string {
	_, name, _ := strings.Cut(arn, ":stream/")
	return name
}

// lambdaKinesisShardKeys lists the shards a mapping's pollers read now: every
// shard not yet read to its end whose parents have been.
func lambdaKinesisShardKeys(mapping LambdaEventSourceMapping) []string {
	stream, ok := kinesisStreamByNameOrARN("", mapping.EventSourceArn)
	if !ok {
		return nil
	}
	done := func(shardID string) bool {
		if shardID == "" {
			return true
		}
		if !kinesisHasShard(stream, shardID) {
			return true
		}
		state, _ := lambdaESMShards.Get(lambdaESMShardKey(mapping.UUID, shardID))
		return state.Done
	}
	var keys []string
	for _, shard := range stream.Shards {
		if done(shard.ShardId) || !done(shard.ParentShardId) || !done(shard.AdjacentParentShardId) {
			continue
		}
		keys = append(keys, lambdaESMShardKey(mapping.UUID, shard.ShardId))
	}
	return keys
}

// startLambdaStreamEventSourcePollers runs the shard pollers of every enabled
// Amazon Kinesis event source mapping.
func startLambdaStreamEventSourcePollers(srv *sim.Server) {
	funcrt.StartPollers(srv, "Lambda stream event source poller", funcrt.Source[string]{
		Keys: func() []string {
			var keys []string
			for _, mapping := range lambdaESMs.List() {
				if lambdaESMPollsKinesis(mapping) {
					keys = append(keys, lambdaKinesisShardKeys(mapping)...)
				}
			}
			return keys
		},
		KeysChanged: func() []<-chan struct{} {
			return []<-chan struct{}{lambdaESMChanges.Any(), kinesisSignals.Any()}
		},
		Wake: func(key string) []<-chan struct{} {
			uuid, _, _ := strings.Cut(key, "\x00")
			wake := []<-chan struct{}{lambdaESMChanges.Changed(uuid), iamChanges.Any(), lambdaEnvironmentChanges.Any()}
			if mapping, ok := lambdaESMs.Get(uuid); ok {
				wake = append(wake, lambdaFunctionChanges.Changed(snsTopicNameFromARN(mapping.FunctionArn)))
			}
			return wake
		},
		Poll: lambdaPollKinesisShard,
	})
}

type kinesisGetRecordsRecord struct {
	SequenceNumber              string  `json:"SequenceNumber"`
	ApproximateArrivalTimestamp float64 `json:"ApproximateArrivalTimestamp"`
	Data                        []byte  `json:"Data"`
	PartitionKey                string  `json:"PartitionKey"`
	EncryptionType              string  `json:"EncryptionType,omitempty"`
}

// lambdaPollKinesisShard reads the next batch of one shard, as the
// function's execution role, and hands it to the function until it is
// handled or the mapping's error handling discards it.
func lambdaPollKinesisShard(ctx context.Context, key string) funcrt.Outcome {
	uuid, shardID, _ := strings.Cut(key, "\x00")
	mapping, ok := lambdaESMs.Get(uuid)
	if !ok || !lambdaESMPollsKinesis(mapping) {
		return funcrt.Inactive
	}
	state, _ := lambdaESMShards.Get(key)
	if state.Done {
		return funcrt.Inactive
	}
	function, ok := lambdaFunctions.Get(snsTopicNameFromARN(mapping.FunctionArn))
	if !ok {
		lambdaSetESMProcessingResult(uuid, "PROBLEM: AWS Lambda function not found")
		return funcrt.Blocked
	}
	function.Version = "$LATEST"
	filters, err := lambdaESMFilters(mapping.FilterCriteria)
	if err != nil {
		lambdaSetESMProcessingResult(uuid, "PROBLEM: "+err.Error())
		return funcrt.Blocked
	}
	actor := awsActor{Service: "lambda.amazonaws.com", Source: mapping.EventSourceMappingArn, Role: function.Role}
	streamName := lambdaKinesisStreamName(mapping.EventSourceArn)

	iteratorRequest := map[string]any{"StreamARN": mapping.EventSourceArn, "ShardId": shardID}
	switch {
	case state.Sequence != "":
		iteratorRequest["ShardIteratorType"] = "AFTER_SEQUENCE_NUMBER"
		iteratorRequest["StartingSequenceNumber"] = state.Sequence
	case mapping.StartingPosition == "TRIM_HORIZON":
		iteratorRequest["ShardIteratorType"] = "TRIM_HORIZON"
	case mapping.StartingPosition == "AT_TIMESTAMP" && mapping.StartingPositionTimestamp != nil:
		iteratorRequest["ShardIteratorType"] = "AT_TIMESTAMP"
		iteratorRequest["Timestamp"] = *mapping.StartingPositionTimestamp
	default:
		if state.StartAt == 0 {
			state.StartAt = float64(time.Now().UnixMilli()) / 1000
			lambdaESMShards.Put(key, state)
		}
		iteratorRequest["ShardIteratorType"] = "AT_TIMESTAMP"
		iteratorRequest["Timestamp"] = state.StartAt
	}
	var iterator struct {
		ShardIterator string `json:"ShardIterator"`
	}
	if !lambdaKinesisCall(ctx, actor, uuid, "GetShardIterator", iteratorRequest, &iterator) {
		return lambdaKinesisRefused(ctx)
	}

	batchSize := 100
	if mapping.BatchSize != nil && *mapping.BatchSize > 0 {
		batchSize = *mapping.BatchSize
	}
	var window <-chan time.Time
	if mapping.MaximumBatchingWindowInSeconds != nil && *mapping.MaximumBatchingWindowInSeconds > 0 {
		timer := time.NewTimer(time.Duration(*mapping.MaximumBatchingWindowInSeconds) * time.Second)
		defer timer.Stop()
		window = timer.C
	}
	shardSignal := kinesisShardSignalKey(kinesisShardRecordKey(streamName, shardID))
	var records []kinesisGetRecordsRecord
	next := iterator.ShardIterator
reading:
	for len(records) < batchSize {
		arrived := kinesisSignals.Changed(shardSignal)
		var page struct {
			Records           []kinesisGetRecordsRecord `json:"Records"`
			NextShardIterator *string                   `json:"NextShardIterator"`
		}
		if !lambdaKinesisCall(ctx, actor, uuid, "GetRecords", map[string]any{"ShardIterator": next, "Limit": min(batchSize-len(records), 10000)}, &page) {
			return lambdaKinesisRefused(ctx)
		}
		records = append(records, page.Records...)
		if page.NextShardIterator == nil {
			if len(records) == 0 {
				// The shard closed and every record in it was read: its
				// children become readable.
				state.Done = true
				lambdaESMShards.Put(key, state)
				lambdaESMChanges.Announce(uuid)
				return funcrt.Inactive
			}
			break
		}
		next = *page.NextShardIterator
		if len(page.Records) > 0 {
			if window == nil {
				break
			}
			continue
		}
		if len(records) > 0 && window == nil {
			break
		}
		// "By default, Lambda invokes your function as soon as records are
		// available"; a batching window keeps reading until it closes.
		select {
		case <-ctx.Done():
			return funcrt.Polled
		case <-arrived:
		case <-window:
			break reading
		}
	}
	if len(records) == 0 || ctx.Err() != nil {
		return funcrt.Polled
	}
	return lambdaProcessKinesisBatch(ctx, mapping, function, filters, key, shardID, records)
}

// lambdaKinesisCall calls a Kinesis Data Streams action as actor, recording
// a refusal on the mapping.
func lambdaKinesisCall(ctx context.Context, actor awsActor, uuid, action string, body map[string]any, out any) bool {
	status, response, err := awsCallJSON(ctx, actor, "kinesis", "Kinesis_20131202."+action, "application/x-amz-json-1.1", body)
	if outcome := ebCallResult(status, response, err, false); !outcome.OK() {
		if ctx.Err() == nil {
			lambdaSetESMProcessingResult(uuid, "PROBLEM: "+outcome.Err.Error())
		}
		return false
	}
	if err := json.Unmarshal(response, out); err != nil {
		lambdaSetESMProcessingResult(uuid, "PROBLEM: Amazon Kinesis answered "+action+" with "+err.Error())
		return false
	}
	return true
}

func lambdaKinesisRefused(ctx context.Context) funcrt.Outcome {
	if ctx.Err() != nil {
		return funcrt.Polled
	}
	return funcrt.Blocked
}

func lambdaKinesisRecord(mapping LambdaEventSourceMapping, function LambdaFunction, shardID string, record kinesisGetRecordsRecord) map[string]any {
	return map[string]any{
		"kinesis": map[string]any{
			"kinesisSchemaVersion":        "1.0",
			"partitionKey":                record.PartitionKey,
			"sequenceNumber":              record.SequenceNumber,
			"data":                        base64.StdEncoding.EncodeToString(record.Data),
			"approximateArrivalTimestamp": record.ApproximateArrivalTimestamp,
		},
		"eventSource":       "aws:kinesis",
		"eventVersion":      "1.0",
		"eventID":           shardID + ":" + record.SequenceNumber,
		"eventName":         "aws:kinesis:record",
		"invokeIdentityArn": function.Role,
		"awsRegion":         awsRegion(),
		"eventSourceARN":    mapping.EventSourceArn,
	}
}

// lambdaKinesisFilterView is the record a filter pattern matches: its data
// decoded when it holds JSON, as Lambda filters a Kinesis record's data.
func lambdaKinesisFilterView(record map[string]any, data []byte) map[string]any {
	view := map[string]any{}
	for k, v := range record {
		view[k] = v
	}
	var decoded any
	if json.Unmarshal(data, &decoded) == nil {
		inner := map[string]any{}
		if kinesis, ok := record["kinesis"].(map[string]any); ok {
			for k, v := range kinesis {
				inner[k] = v
			}
		}
		inner["data"] = decoded
		view["kinesis"] = inner
		view["data"] = decoded
	}
	return view
}

// lambdaProcessKinesisBatch hands a batch to the function, checkpointing
// what it handled. A failed batch retries at once — split in two first when
// BisectBatchOnFunctionError asks, which spends no retry — until it is
// handled, MaximumRetryAttempts is spent, or its records pass
// MaximumRecordAgeInSeconds, when the mapping sends the batch's description to
// its on-failure destination and moves past it
// (kinesis-on-failure-destination.md.gz).
func lambdaProcessKinesisBatch(ctx context.Context, mapping LambdaEventSourceMapping, function LambdaFunction,
	filters []map[string]any, key, shardID string, records []kinesisGetRecordsRecord) funcrt.Outcome {
	checkpoint := func(sequence string) {
		lambdaESMShards.Upsert(key, func(state *lambdaESMShardState) { state.Sequence = sequence })
	}
	maxRetries := -1
	if mapping.MaximumRetryAttempts != nil {
		maxRetries = *mapping.MaximumRetryAttempts
	}
	maxAge := -1
	if mapping.MaximumRecordAgeInSeconds != nil {
		maxAge = *mapping.MaximumRecordAgeInSeconds
	}
	bisect := mapping.BisectBatchOnFunctionError != nil && *mapping.BisectBatchOnFunctionError
	reportsItems := false
	for _, responseType := range mapping.FunctionResponseTypes {
		reportsItems = reportsItems || responseType == "ReportBatchItemFailures"
	}

	queue := [][]kinesisGetRecordsRecord{records}
	retries := 0
	for len(queue) > 0 {
		if current, ok := lambdaESMs.Get(mapping.UUID); !ok || !lambdaESMPollsKinesis(current) {
			return funcrt.Inactive
		}
		batch := queue[0]
		if maxAge >= 0 {
			cutoff := float64(time.Now().Add(-time.Duration(maxAge)*time.Second).UnixMilli()) / 1000
			kept := batch[:0:0]
			for _, record := range batch {
				if record.ApproximateArrivalTimestamp >= cutoff {
					kept = append(kept, record)
				}
			}
			if len(kept) < len(batch) {
				lambdaKinesisDiscard(ctx, mapping, function, shardID, batch[:len(batch)-len(kept)], "RecordAgeExceeded", retries)
				checkpoint(batch[len(batch)-len(kept)-1].SequenceNumber)
				batch = kept
				queue[0] = batch
			}
			if len(batch) == 0 {
				queue, retries = queue[1:], 0
				continue
			}
		}
		var eventRecords []map[string]any
		for _, record := range batch {
			event := lambdaKinesisRecord(mapping, function, shardID, record)
			pass, err := lambdaRecordPassesFilters(filters, lambdaKinesisFilterView(event, record.Data))
			if err != nil {
				lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: "+err.Error())
				return funcrt.Blocked
			}
			if pass {
				eventRecords = append(eventRecords, event)
			}
		}
		last := batch[len(batch)-1].SequenceNumber
		if len(eventRecords) == 0 {
			checkpoint(last)
			queue, retries = queue[1:], 0
			continue
		}
		payload, err := json.Marshal(map[string]any{"Records": eventRecords})
		if err != nil {
			lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: failed to serialize Amazon Kinesis event")
			return funcrt.Blocked
		}
		response, unhandled, serviceErr := invokeLambdaViaRuntimeAPI(ctx, function, payload)
		if ctx.Err() != nil {
			return funcrt.Polled
		}
		if serviceErr != nil && serviceErr.Status == http.StatusTooManyRequests {
			// "If a Lambda event source mapping is unable to invoke the
			// function due to throttling or other issues, it retries until
			// the records expire or exceed the maximum age": the batch waits
			// for the function's concurrency to free up, from its checkpoint.
			return funcrt.Blocked
		}
		if !unhandled {
			if !reportsItems {
				checkpoint(last)
				queue, retries = queue[1:], 0
				lambdaSetESMProcessingResult(mapping.UUID, "OK")
				continue
			}
			failedFrom := lambdaKinesisFirstFailure(batch, response)
			if failedFrom < 0 {
				checkpoint(last)
				queue, retries = queue[1:], 0
				lambdaSetESMProcessingResult(mapping.UUID, "OK")
				continue
			}
			// "Lambda uses the record with the lowest sequence number as the
			// checkpoint. Lambda then retries all records starting from that
			// checkpoint."
			if failedFrom > 0 {
				checkpoint(batch[failedFrom-1].SequenceNumber)
			}
			batch = batch[failedFrom:]
			queue[0] = batch
		}
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: Function call failed")
		if bisect && len(batch) > 1 {
			half := len(batch) / 2
			queue = append([][]kinesisGetRecordsRecord{batch[:half], batch[half:]}, queue[1:]...)
			continue
		}
		retries++
		if maxRetries >= 0 && retries > maxRetries {
			lambdaKinesisDiscard(ctx, mapping, function, shardID, batch, "RetryAttemptsExhausted", retries)
			checkpoint(batch[len(batch)-1].SequenceNumber)
			queue, retries = queue[1:], 0
		}
	}
	return funcrt.Polled
}

// lambdaKinesisFirstFailure is the index of the first record the function's
// batchItemFailures names, or -1 when it names none.
func lambdaKinesisFirstFailure(batch []kinesisGetRecordsRecord, response []byte) int {
	var result struct {
		BatchItemFailures []struct {
			ItemIdentifier string `json:"itemIdentifier"`
		} `json:"batchItemFailures"`
	}
	if json.Unmarshal(response, &result) != nil || len(result.BatchItemFailures) == 0 {
		return -1
	}
	failed := map[string]bool{}
	for _, item := range result.BatchItemFailures {
		failed[item.ItemIdentifier] = true
	}
	for i, record := range batch {
		if failed[record.SequenceNumber] {
			return i
		}
	}
	return -1
}

// lambdaKinesisDiscard sends the description of a batch the mapping gives up
// on to its on-failure destination, as the function's execution role.
func lambdaKinesisDiscard(ctx context.Context, mapping LambdaEventSourceMapping, function LambdaFunction, shardID string,
	batch []kinesisGetRecordsRecord, condition string, invokeCount int) {
	if mapping.DestinationConfig == nil || mapping.DestinationConfig.OnFailure == nil || mapping.DestinationConfig.OnFailure.Destination == "" {
		return
	}
	arrival := func(ts float64) string {
		return time.UnixMilli(int64(ts * 1000)).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	record := map[string]any{
		"requestContext": map[string]any{
			"requestId":              sim.NewUUID(),
			"functionArn":            function.FunctionArn,
			"condition":              condition,
			"approximateInvokeCount": invokeCount,
		},
		"responseContext": map[string]any{
			"statusCode":      200,
			"executedVersion": "$LATEST",
			"functionError":   "Unhandled",
		},
		"version":   "1.0",
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"KinesisBatchInfo": map[string]any{
			"shardId":                         shardID,
			"startSequenceNumber":             batch[0].SequenceNumber,
			"endSequenceNumber":               batch[len(batch)-1].SequenceNumber,
			"approximateArrivalOfFirstRecord": arrival(batch[0].ApproximateArrivalTimestamp),
			"approximateArrivalOfLastRecord":  arrival(batch[len(batch)-1].ApproximateArrivalTimestamp),
			"batchSize":                       len(batch),
			"streamArn":                       mapping.EventSourceArn,
		},
	}
	body, err := json.Marshal(record)
	if err != nil {
		return
	}
	if err := lambdaDeliverAsyncDestination(ctx, function, mapping.DestinationConfig.OnFailure.Destination, body); err != nil {
		lambdaSetESMProcessingResult(mapping.UUID, fmt.Sprintf("PROBLEM: the on-failure destination refused the record: %v", err))
	}
}
