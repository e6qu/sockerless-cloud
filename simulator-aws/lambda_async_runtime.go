package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// LambdaAsyncInvocation is an event AWS Lambda accepted for asynchronous
// invocation; the delivery dispatcher persists it between attempts.
type LambdaAsyncInvocation struct {
	Function      LambdaFunction
	Payload       []byte
	Qualifier     string
	RequestID     string
	MaxRetries    int
	MaxAgeSeconds int
	Response      []byte
	Unhandled     bool
	Configured    bool
	Destination   *lambdaDestinationConfig
	// Throttles counts the attempts the function's concurrency refused.
	Throttles int
}

// lambdaAsyncThrottleBackoff is the wait before retrying an event after its
// nth throttle: one second, doubling, at most five minutes.
func lambdaAsyncThrottleBackoff(n int) time.Duration {
	wait := time.Second
	for i := 1; i < n && wait < 5*time.Minute; i++ {
		wait *= 2
	}
	return min(wait, 5*time.Minute)
}

var lambdaAsyncInvocations *delivery.Dispatcher[LambdaAsyncInvocation]

// registerLambdaAsyncInvocations runs asynchronous invocations the way AWS
// Lambda documents them: up to MaximumRetryAttempts retries (default 2), the
// first after one minute and later ones after two, while the event is younger
// than MaximumEventAgeInSeconds (default six hours).
func registerLambdaAsyncInvocations(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[LambdaAsyncInvocation]](srv.DB(), "lambda_async_deliveries")
	lambdaAdoptLegacyInvocations(srv.DB(), store)
	lambdaAsyncInvocations = delivery.New(srv, "Lambda asynchronous invocations", store, delivery.Handler[LambdaAsyncInvocation]{
		Policy: func(invocation LambdaAsyncInvocation) delivery.Policy {
			return delivery.Policy{
				MaxAttempts: invocation.MaxRetries + 1,
				MaxAge:      time.Duration(invocation.MaxAgeSeconds) * time.Second,
				Backoff:     delivery.Steps(time.Minute, 2*time.Minute),
			}
		},
		Attempt: func(ctx context.Context, item *delivery.Item[LambdaAsyncInvocation]) delivery.Outcome {
			response, unhandled, serviceErr := invokeLambdaViaRuntimeAPI(ctx, item.Payload.Function, item.Payload.Payload)
			if serviceErr != nil && serviceErr.Status == http.StatusTooManyRequests {
				// "For throttling errors (429) and system errors (500-series),
				// Lambda returns the event to the queue and attempts to run the
				// function again for up to 6 hours by default. The retry
				// interval increases exponentially from 1 second after the
				// first attempt to a maximum of 5 minutes."
				// (invocation-async-error-handling.md.gz.) A throttle is not a
				// function error, so it spends none of MaximumRetryAttempts.
				item.Payload.Throttles++
				return delivery.Deferred(errors.New(serviceErr.Message), lambdaAsyncThrottleBackoff(item.Payload.Throttles))
			}
			item.Payload.Response = append([]byte(nil), response...)
			item.Payload.Unhandled = unhandled
			if unhandled {
				return delivery.Retryable(errors.New("function error"))
			}
			return delivery.Delivered()
		},
		Finish: lambdaCompleteAsyncInvocation,
	})
}

func lambdaAsyncQualifier(identifier, queryQualifier string) string {
	if queryQualifier != "" {
		return queryQualifier
	}
	if marker := strings.Index(identifier, ":function:"); marker >= 0 {
		identifier = identifier[marker+len(":function:"):]
	}
	if separator := strings.IndexByte(identifier, ':'); separator >= 0 {
		return identifier[separator+1:]
	}
	return "$LATEST"
}

func lambdaEventInvokeConfig(functionName, qualifier string) (LambdaEventInvokeConfig, bool) {
	return lambdaEICs.Get(lambdaEICKey(functionName, qualifier))
}

func lambdaInvokeAsynchronously(function LambdaFunction, payload []byte, qualifier string) string {
	config, configured := lambdaEventInvokeConfig(function.FunctionName, qualifier)
	maxRetries := 2
	maxAge := 6 * 60 * 60
	var destination *lambdaDestinationConfig
	if configured {
		if config.MaximumRetryAttempts != nil {
			maxRetries = *config.MaximumRetryAttempts
		}
		if config.MaximumEventAgeInSeconds != nil {
			maxAge = *config.MaximumEventAgeInSeconds
		}
		destination = config.DestinationConfig
	}
	requestID := sim.NewUUID()
	lambdaAsyncInvocations.Submit(requestID, LambdaAsyncInvocation{
		Function:      function,
		Payload:       append([]byte(nil), payload...),
		Qualifier:     qualifier,
		RequestID:     requestID,
		MaxRetries:    maxRetries,
		MaxAgeSeconds: maxAge,
		Configured:    configured,
		Destination:   destination,
	})
	return requestID
}

func recoverLambdaInvocations() error {
	if !sim.HasPersistentWorkloadIdentity() {
		return nil
	}
	existing, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-lambda": ""})
	if err != nil {
		return fmt.Errorf("find interrupted AWS Lambda runtime containers: %w", err)
	}
	for _, workload := range existing {
		if err := sim.RemoveExistingContainer(workload.ID); err != nil {
			return fmt.Errorf("remove interrupted AWS Lambda runtime container %s: %w", workload.ID, err)
		}
	}
	lambdaAsyncInvocations.Resume()
	return nil
}

var lambdaAsyncConditions = map[delivery.Reason]string{
	delivery.Succeeded:         "Success",
	delivery.AttemptsExhausted: "RetriesExhausted",
	delivery.AgeExceeded:       "EventAgeExceeded",
	delivery.Rejected:          "RetriesExhausted",
}

func lambdaCompleteAsyncInvocation(item delivery.Item[LambdaAsyncInvocation], reason delivery.Reason) {
	invocation := item.Payload
	condition := lambdaAsyncConditions[reason]
	if reason == delivery.AgeExceeded {
		invocation.Unhandled = true
	}
	if !invocation.Configured || invocation.Destination == nil {
		return
	}
	var destination *lambdaDestination
	if invocation.Unhandled {
		destination = invocation.Destination.OnFailure
	} else {
		destination = invocation.Destination.OnSuccess
	}
	if destination == nil || destination.Destination == "" {
		return
	}

	var requestPayload any
	if json.Unmarshal(invocation.Payload, &requestPayload) != nil {
		requestPayload = string(invocation.Payload)
	}
	var responsePayload any
	if json.Unmarshal(invocation.Response, &responsePayload) != nil {
		responsePayload = string(invocation.Response)
	}
	responseContext := map[string]any{
		"statusCode":      200,
		"executedVersion": invocation.Function.Version,
	}
	if invocation.Unhandled {
		responseContext["functionError"] = "Unhandled"
	}
	record := map[string]any{
		"version":   "1.0",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"requestContext": map[string]any{
			"requestId":              invocation.RequestID,
			"functionArn":            invocation.Function.FunctionArn,
			"condition":              condition,
			"approximateInvokeCount": item.Attempts,
		},
		"requestPayload":  requestPayload,
		"responseContext": responseContext,
		"responsePayload": responsePayload,
	}
	body, err := json.Marshal(record)
	if err != nil {
		return
	}
	if err := lambdaDeliverAsyncDestination(context.Background(), invocation.Function, destination.Destination, body); err != nil {
		lambdaRecordDestinationDeliveryFailure(invocation.Function, destination.Destination, err)
	}
}

// lambdaDestinationActor is who AWS Lambda sends a function's invocation
// record as: a session of the function's execution role, whose policies must
// allow the send.
func lambdaDestinationActor(function LambdaFunction) awsActor {
	return awsActor{Service: "lambda.amazonaws.com", Source: function.FunctionArn, Role: function.Role}
}

// lambdaDeliverAsyncDestination sends the record to the destination through
// the destination service's API as the function's execution role and returns
// what the service refused.
func lambdaDeliverAsyncDestination(ctx context.Context, function LambdaFunction, destinationARN string, body []byte) error {
	actor := lambdaDestinationActor(function)
	var (
		status    int
		response  []byte
		err       error
		xmlErrors bool
	)
	switch {
	case strings.HasPrefix(destinationARN, "arn:aws:sqs:"):
		queue := destinationARN[strings.LastIndex(destinationARN, ":")+1:]
		status, response, err = awsCallJSON(ctx, actor, "sqs", "AmazonSQS.SendMessage", "application/x-amz-json-1.0",
			map[string]any{"QueueUrl": sqsQueueURL(queue), "MessageBody": string(body)})
	case strings.HasPrefix(destinationARN, "arn:aws:sns:"):
		xmlErrors = true
		status, response, err = awsCallQuery(ctx, actor, "sns", "Publish", "2010-03-31",
			url.Values{"TopicArn": {destinationARN}, "Message": {string(body)}})
	case strings.HasPrefix(destinationARN, "arn:aws:events:"):
		status, response, err = awsCallJSON(ctx, actor, "events", "AWSEvents.PutEvents", "application/x-amz-json-1.1",
			map[string]any{"Entries": []map[string]any{{
				"Source":       "lambda",
				"DetailType":   "Lambda Function Invocation Result",
				"Detail":       string(body),
				"EventBusName": destinationARN,
			}}})
		if err == nil && status < http.StatusBadRequest {
			return ebPutEventsEntryError(response)
		}
	case strings.HasPrefix(destinationARN, "arn:aws:lambda:"):
		status, response, err = awsCall(ctx, actor, "lambda", http.MethodPost,
			"/2015-03-31/functions/"+url.PathEscape(destinationARN)+"/invocations",
			http.Header{"X-Amz-Invocation-Type": {"Event"}, "Content-Type": {"application/json"}}, body)
	default:
		return &awsCallError{Status: http.StatusBadRequest, Code: "InvalidParameterValueException",
			Message: "The destination ARN " + destinationARN + " is not an Amazon SQS queue, Amazon SNS topic, AWS Lambda function or Amazon EventBridge event bus"}
	}
	if err != nil {
		return err
	}
	if status >= http.StatusBadRequest {
		code, message := awsJSONError(response)
		if xmlErrors {
			code, message = awsXMLError(response)
		}
		return &awsCallError{Status: status, Code: code, Message: message}
	}
	return nil
}

// ebPutEventsEntryError is the error a PutEvents response reports for its one
// entry, if it reports one.
func ebPutEventsEntryError(response []byte) error {
	var result struct {
		FailedEntryCount int `json:"FailedEntryCount"`
		Entries          []struct {
			ErrorCode    string `json:"ErrorCode"`
			ErrorMessage string `json:"ErrorMessage"`
		} `json:"Entries"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return err
	}
	if result.FailedEntryCount > 0 && len(result.Entries) > 0 {
		return &awsCallError{Status: http.StatusBadRequest, Code: result.Entries[0].ErrorCode, Message: result.Entries[0].ErrorMessage}
	}
	return nil
}

// lambdaRecordDestinationDeliveryFailure reports a record the destination did
// not accept the way AWS Lambda does: in the function's
// DestinationDeliveryFailures metric and in the simulator's log.
func lambdaRecordDestinationDeliveryFailure(function LambdaFunction, destinationARN string, err error) {
	cwStoreDatum(CWMetricDatum{
		Namespace:  "AWS/Lambda",
		MetricName: "DestinationDeliveryFailures",
		Dimensions: []CWDimension{{Name: "FunctionName", Value: function.FunctionName}},
		Value:      1,
		Unit:       "Count",
		Timestamp:  float64(time.Now().Unix()),
	})
	logger := sfnAWSServer.Logger()
	logger.Warn().Str("function", function.FunctionArn).Str("destination", destinationARN).Err(err).
		Msg("AWS Lambda could not deliver an asynchronous invocation record to its destination")
}
