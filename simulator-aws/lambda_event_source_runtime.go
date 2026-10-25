package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/funcrt"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"github.com/e6qu/sockerless-cloud/sim/signals"
	"github.com/rs/zerolog"
)

var (
	lambdaESMLogger zerolog.Logger
	// lambdaESMChanges announces a mapping's UUID on every write of it, and
	// lambdaFunctionChanges a function's name.
	lambdaESMChanges      = signals.New[string]()
	lambdaFunctionChanges = signals.New[string]()
)

func lambdaESMPollsSQS(mapping LambdaEventSourceMapping) bool {
	return mapping.State == "Enabled" && strings.HasPrefix(mapping.EventSourceArn, "arn:aws:sqs:")
}

// startLambdaEventSourcePollers runs a poller for each enabled Amazon SQS
// event source mapping, starting one whenever a mapping is created, enabled or
// updated. A poller long-polls its queue batch after batch while its mapping
// stays enabled; a batch that fails for a reason no message can clear — the
// function, the queue or the execution role refusing the poller — waits for
// the mapping, the function, the queue or IAM to change before the next.
func startLambdaEventSourcePollers(srv *sim.Server) {
	funcrt.StartPollers(srv, "Lambda event source poller", funcrt.Source[string]{
		Keys: func() []string {
			var keys []string
			for _, mapping := range lambdaESMs.List() {
				if lambdaESMPollsSQS(mapping) {
					keys = append(keys, mapping.UUID)
				}
			}
			return keys
		},
		KeysChanged: func() []<-chan struct{} { return []<-chan struct{}{lambdaESMChanges.Any()} },
		Wake: func(uuid string) []<-chan struct{} {
			wake := []<-chan struct{}{lambdaESMChanges.Changed(uuid), iamChanges.Any()}
			if mapping, ok := lambdaESMs.Get(uuid); ok {
				wake = append(wake,
					lambdaFunctionChanges.Changed(snsTopicNameFromARN(mapping.FunctionArn)),
					sqsQueueChanges.Changed(sqsNameFromARN(mapping.EventSourceArn)))
			}
			return wake
		},
		Poll: func(ctx context.Context, uuid string) funcrt.Outcome {
			mapping, ok := lambdaESMs.Get(uuid)
			if !ok || !lambdaESMPollsSQS(mapping) {
				return funcrt.Inactive
			}
			if lambdaPollSQSMapping(ctx, mapping) {
				return funcrt.Polled
			}
			return funcrt.Blocked
		},
	})
}

// sqsReceivedMessage is a message as Amazon SQS's ReceiveMessage returns it.
type sqsReceivedMessage struct {
	MessageID         string            `json:"MessageId"`
	ReceiptHandle     string            `json:"ReceiptHandle"`
	MD5OfBody         string            `json:"MD5OfBody"`
	Body              string            `json:"Body"`
	Attributes        map[string]string `json:"Attributes"`
	MessageAttributes map[string]struct {
		DataType    string `json:"DataType"`
		StringValue string `json:"StringValue"`
		BinaryValue []byte `json:"BinaryValue"`
	} `json:"MessageAttributes"`
}

// lambdaESMReceiveWait is how long a poll waits for a message, the longest
// ReceiveMessage allows, as AWS Lambda's pollers long-poll the queue.
const lambdaESMReceiveWait = 20

// lambdaPollSQSMapping receives one batch from the mapping's queue, invokes
// the function with the messages its filters admit and deletes what the
// function handled, calling Amazon SQS as the function's execution role. It
// reports false when the batch failed for a reason that polling again cannot
// clear; a function error is not one, as the queue redelivers the batch once
// its visibility timeout ends.
func lambdaPollSQSMapping(ctx context.Context, mapping LambdaEventSourceMapping) bool {
	if ctx.Err() != nil {
		return true
	}
	functionName := snsTopicNameFromARN(mapping.FunctionArn)
	function, ok := lambdaFunctions.Get(functionName)
	if !ok {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: AWS Lambda function not found")
		return false
	}
	filters, err := lambdaESMFilters(mapping.FilterCriteria)
	if err != nil {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: "+err.Error())
		return false
	}
	actor := awsActor{Service: "lambda.amazonaws.com", Source: mapping.EventSourceMappingArn, Role: function.Role}
	queueURL := sqsQueueURL(mapping.EventSourceArn[strings.LastIndex(mapping.EventSourceArn, ":")+1:])
	batchSize := 10
	if mapping.BatchSize != nil && *mapping.BatchSize > 0 {
		batchSize = *mapping.BatchSize
	}

	var received []sqsReceivedMessage
	for wait := lambdaESMReceiveWait; len(received) < batchSize; wait = 0 {
		status, body, err := awsCallJSON(ctx, actor, "sqs", "AmazonSQS.ReceiveMessage", "application/x-amz-json-1.0", map[string]any{
			"QueueUrl": queueURL, "MaxNumberOfMessages": min(batchSize-len(received), 10), "WaitTimeSeconds": wait,
			"MessageSystemAttributeNames": []string{"All"}, "MessageAttributeNames": []string{"All"},
		})
		if outcome := ebCallResult(status, body, err, false); !outcome.OK() {
			if ctx.Err() != nil {
				return true
			}
			lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: "+outcome.Err.Error())
			return false
		}
		var page struct {
			Messages []sqsReceivedMessage `json:"Messages"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: Amazon SQS answered ReceiveMessage with "+err.Error())
			return false
		}
		if len(page.Messages) == 0 {
			break
		}
		received = append(received, page.Messages...)
	}
	if len(received) == 0 || ctx.Err() != nil {
		return true
	}

	// Lambda deletes the Amazon SQS messages its filter criteria reject
	// rather than invoking the function with them.
	receivedRecords := lambdaSQSEventRecords(mapping.EventSourceArn, received)
	var messages []sqsReceivedMessage
	var records []map[string]any
	var filteredOut []string
	for i, record := range receivedRecords {
		pass, err := lambdaRecordPassesFilters(filters, record)
		if err != nil {
			lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: "+err.Error())
			return false
		}
		if !pass {
			filteredOut = append(filteredOut, received[i].ReceiptHandle)
			continue
		}
		messages = append(messages, received[i])
		records = append(records, record)
	}
	if !lambdaESMDelete(ctx, actor, mapping, queueURL, filteredOut) {
		return false
	}
	if len(messages) == 0 {
		return true
	}

	payload, err := json.Marshal(map[string]any{"Records": records})
	if err != nil {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: failed to serialize Amazon SQS event")
		return false
	}
	response, unhandled, _ := invokeLambdaViaRuntimeAPI(ctx, function, payload)
	if ctx.Err() != nil {
		return true
	}
	if unhandled {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: Function call failed")
		return true
	}

	failed := lambdaSQSBatchFailures(mapping, response)
	receipts := make([]string, 0, len(messages))
	for _, message := range messages {
		if !failed[message.MessageID] {
			receipts = append(receipts, message.ReceiptHandle)
		}
	}
	if !lambdaESMDelete(ctx, actor, mapping, queueURL, receipts) {
		return false
	}
	lambdaSetESMProcessingResult(mapping.UUID, "OK")
	lambdaESMLogger.Info().
		Str("eventSourceMapping", mapping.UUID).
		Str("queueARN", mapping.EventSourceArn).
		Int("records", len(messages)).
		Int("failedRecords", len(failed)).
		Msg("AWS Lambda processed Amazon SQS event source batch")
	return true
}

// lambdaESMDelete deletes handled messages with DeleteMessageBatch, ten to a
// call, and reports whether every deletion succeeded.
func lambdaESMDelete(ctx context.Context, actor awsActor, mapping LambdaEventSourceMapping, queueURL string, receipts []string) bool {
	for start := 0; start < len(receipts); start += 10 {
		entries := make([]map[string]string, 0, 10)
		for i, receipt := range receipts[start:min(start+10, len(receipts))] {
			entries = append(entries, map[string]string{"Id": strconv.Itoa(i), "ReceiptHandle": receipt})
		}
		status, body, err := awsCallJSON(ctx, actor, "sqs", "AmazonSQS.DeleteMessageBatch", "application/x-amz-json-1.0",
			map[string]any{"QueueUrl": queueURL, "Entries": entries})
		if outcome := ebCallResult(status, body, err, false); !outcome.OK() {
			lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: "+outcome.Err.Error())
			return false
		}
	}
	return true
}

func lambdaSQSEventRecords(queueARN string, messages []sqsReceivedMessage) []map[string]any {
	records := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		attributes := map[string]string{}
		for _, name := range []string{"ApproximateReceiveCount", "SentTimestamp", "SenderId", "ApproximateFirstReceiveTimestamp",
			"SequenceNumber", "MessageGroupId", "MessageDeduplicationId", "AWSTraceHeader"} {
			if value, ok := message.Attributes[name]; ok {
				attributes[name] = value
			}
		}
		messageAttributes := map[string]any{}
		for name, attribute := range message.MessageAttributes {
			rendered := map[string]any{"dataType": attribute.DataType}
			if len(attribute.BinaryValue) > 0 {
				rendered["binaryValue"] = base64.StdEncoding.EncodeToString(attribute.BinaryValue)
			} else {
				rendered["stringValue"] = attribute.StringValue
			}
			messageAttributes[name] = rendered
		}
		records = append(records, map[string]any{
			"messageId":         message.MessageID,
			"receiptHandle":     message.ReceiptHandle,
			"body":              message.Body,
			"attributes":        attributes,
			"messageAttributes": messageAttributes,
			"md5OfBody":         message.MD5OfBody,
			"eventSource":       "aws:sqs",
			"eventSourceARN":    queueARN,
			"awsRegion":         awsRegion(),
		})
	}
	return records
}

func lambdaSQSBatchFailures(mapping LambdaEventSourceMapping, response []byte) map[string]bool {
	enabled := false
	for _, responseType := range mapping.FunctionResponseTypes {
		if responseType == "ReportBatchItemFailures" {
			enabled = true
			break
		}
	}
	if !enabled {
		return map[string]bool{}
	}
	var result struct {
		BatchItemFailures []struct {
			ItemIdentifier string `json:"itemIdentifier"`
		} `json:"batchItemFailures"`
	}
	if json.Unmarshal(response, &result) != nil {
		return map[string]bool{}
	}
	failed := make(map[string]bool, len(result.BatchItemFailures))
	for _, item := range result.BatchItemFailures {
		failed[item.ItemIdentifier] = true
	}
	return failed
}

func sqsDeleteReceiptHandles(queueName string, receipts []string) {
	if len(receipts) == 0 {
		return
	}
	remove := make(map[string]bool, len(receipts))
	for _, receipt := range receipts {
		remove[receipt] = true
	}
	sqsQueues.Update(queueName, func(queue *SQSQueue) {
		queue.Messages.Remove(func(m msgq.Message[sqsPayload]) bool { return m.Receipt != "" && remove[m.Receipt] })
	})
	sqsSignalQueue(queueName)
}

// lambdaSetESMProcessingResult records the outcome of a batch. It leaves the
// mapping untouched when the outcome repeats, so a poller that waits for its
// mapping to change after a failure does not wake on its own report.
func lambdaSetESMProcessingResult(uuid, result string) {
	if mapping, ok := lambdaESMs.Get(uuid); !ok || mapping.LastProcessingResult == result {
		return
	}
	lambdaESMs.Update(uuid, func(mapping *LambdaEventSourceMapping) {
		mapping.LastProcessingResult = result
		mapping.LastModified = lambdaNowEpoch()
	})
}
