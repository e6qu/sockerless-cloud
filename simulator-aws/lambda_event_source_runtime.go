package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/rs/zerolog"
)

var (
	lambdaESMLogger zerolog.Logger
	lambdaESMRunMu  sync.Mutex
	lambdaESMActive = map[string]bool{}
)

func startLambdaEventSourcePollers(srv *sim.Server) {
	srv.StartBackground(func(ctx context.Context) {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			mappings := make([]LambdaEventSourceMapping, 0, lambdaESMs.Len())
			for _, mapping := range lambdaESMs.List() {
				if mapping.State == "Enabled" && strings.HasPrefix(mapping.EventSourceArn, "arn:aws:sqs:") {
					mappings = append(mappings, mapping)
				}
			}
			for _, mapping := range mappings {
				if lambdaBeginESMRun(mapping.UUID) {
					mapping := mapping
					srv.StartBackground(func(ctx context.Context) {
						lambdaPollSQSMapping(ctx, mapping)
					})
				}
			}
		}
	})
}

func lambdaBeginESMRun(uuid string) bool {
	lambdaESMRunMu.Lock()
	defer lambdaESMRunMu.Unlock()
	if lambdaESMActive[uuid] {
		return false
	}
	lambdaESMActive[uuid] = true
	return true
}

func lambdaFinishESMRun(uuid string) {
	lambdaESMRunMu.Lock()
	delete(lambdaESMActive, uuid)
	lambdaESMRunMu.Unlock()
}

func lambdaPollSQSMapping(ctx context.Context, mapping LambdaEventSourceMapping) {
	defer lambdaFinishESMRun(mapping.UUID)
	if ctx.Err() != nil {
		return
	}
	queueName := snsTopicNameFromARN(mapping.EventSourceArn)
	queue, ok := sqsQueues.Get(queueName)
	if !ok {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: Amazon SQS queue not found")
		return
	}
	functionName := snsTopicNameFromARN(mapping.FunctionArn)
	function, ok := lambdaFunctions.Get(functionName)
	if !ok {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: AWS Lambda function not found")
		return
	}

	batchSize := 10
	if mapping.BatchSize != nil && *mapping.BatchSize > 0 {
		batchSize = *mapping.BatchSize
	}
	visibilityTimeout := 30
	if raw := queue.Attributes["VisibilityTimeout"]; raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			visibilityTimeout = parsed
		}
	}
	messages := sqsReceiveAvailableMessages(queueName, batchSize, visibilityTimeout)
	if len(messages) == 0 {
		return
	}
	if ctx.Err() != nil {
		return
	}

	payload, err := json.Marshal(map[string]any{"Records": lambdaSQSEventRecords(mapping.EventSourceArn, messages)})
	if err != nil {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: failed to serialize Amazon SQS event")
		return
	}
	response, unhandled, _ := invokeLambdaViaRuntimeAPI(function, payload)
	if ctx.Err() != nil {
		return
	}
	if unhandled {
		lambdaSetESMProcessingResult(mapping.UUID, "PROBLEM: Function call failed")
		return
	}

	failed := lambdaSQSBatchFailures(mapping, response)
	receipts := make([]string, 0, len(messages))
	for _, message := range messages {
		if !failed[message.MessageId] {
			receipts = append(receipts, message.ReceiptHandle)
		}
	}
	sqsDeleteReceiptHandles(queueName, receipts)
	lambdaSetESMProcessingResult(mapping.UUID, "OK")
	lambdaESMLogger.Info().
		Str("eventSourceMapping", mapping.UUID).
		Str("queueARN", mapping.EventSourceArn).
		Int("records", len(messages)).
		Int("failedRecords", len(failed)).
		Msg("AWS Lambda processed Amazon SQS event source batch")
}

func lambdaSQSEventRecords(queueARN string, messages []SQSMessage) []map[string]any {
	records := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		attributes := map[string]string{
			"ApproximateReceiveCount":          strconv.Itoa(message.ApproximateReceiveCount),
			"SentTimestamp":                    strconv.FormatInt(message.SentTimestamp, 10),
			"ApproximateFirstReceiveTimestamp": strconv.FormatInt(message.FirstReceivedAt, 10),
			"SenderId":                         awsAccountID(),
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
			"messageId":         message.MessageId,
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
		kept := queue.Messages[:0]
		for _, message := range queue.Messages {
			if !remove[message.ReceiptHandle] {
				kept = append(kept, message)
			}
		}
		queue.Messages = kept
	})
}

func lambdaSetESMProcessingResult(uuid, result string) {
	lambdaESMs.Update(uuid, func(mapping *LambdaEventSourceMapping) {
		mapping.LastProcessingResult = result
		mapping.LastModified = lambdaNowEpoch()
	})
}
