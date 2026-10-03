package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			response, unhandled, _ := invokeLambdaViaRuntimeAPI(ctx, item.Payload.Function, item.Payload.Payload)
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

func lambdaInvokeAsynchronously(function LambdaFunction, payload []byte, qualifier string) {
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
	lambdaDeliverAsyncDestination(destination.Destination, body)
}

func lambdaDeliverAsyncDestination(destinationARN string, body []byte) {
	switch {
	case strings.HasPrefix(destinationARN, "arn:aws:sqs:"):
		queueName := snsTopicNameFromARN(destinationARN)
		if _, ok := sqsQueues.Get(queueName); ok {
			sqsEnqueueBody(queueName, string(body))
		}
	case strings.HasPrefix(destinationARN, "arn:aws:sns:"):
		if _, ok := snsTopics.Get(snsTopicNameFromARN(destinationARN)); ok {
			snsFanout(destinationARN, sim.NewUUID(), "", string(body), nil)
		}
	case strings.HasPrefix(destinationARN, "arn:aws:events:"):
		_, _ = sfnInvokeJSONService(handleEBPutEvents, map[string]any{"Entries": []map[string]any{{
			"Source":       "lambda",
			"DetailType":   "Lambda Function Invocation Result",
			"Detail":       string(body),
			"EventBusName": destinationARN,
		}}})
	case strings.HasPrefix(destinationARN, "arn:aws:lambda:"):
		if target, _, ok := lambdaResolveInvocationTarget(destinationARN, ""); ok {
			_, _, _ = invokeLambdaViaRuntimeAPI(context.Background(), target, body)
		}
	}
}
