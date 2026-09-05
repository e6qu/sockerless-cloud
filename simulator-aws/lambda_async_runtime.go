package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

type LambdaAsyncInvocation struct {
	ID            string
	Function      LambdaFunction
	Payload       []byte
	Qualifier     string
	RequestID     string
	StartedAt     time.Time
	NextAttemptAt time.Time
	InvokeCount   int
	Failures      int
	MaxRetries    int
	MaxAgeSeconds int
	Response      []byte
	Unhandled     bool
	Condition     string
	Configured    bool
	Destination   *lambdaDestinationConfig
}

var lambdaAsyncInvocations sim.Store[LambdaAsyncInvocation]

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
	requestID := generateUUID()
	invocation := LambdaAsyncInvocation{
		ID:            requestID,
		Function:      function,
		Payload:       append([]byte(nil), payload...),
		Qualifier:     qualifier,
		RequestID:     requestID,
		StartedAt:     time.Now().UTC(),
		MaxRetries:    maxRetries,
		MaxAgeSeconds: maxAge,
		Configured:    configured,
		Destination:   destination,
		Condition:     "Success",
	}
	lambdaAsyncInvocations.Put(invocation.ID, invocation)
	go lambdaRunAsyncInvocation(invocation.ID)
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
	for _, invocation := range lambdaAsyncInvocations.List() {
		go lambdaRunAsyncInvocation(invocation.ID)
	}
	return nil
}

func lambdaRunAsyncInvocation(id string) {
	for {
		invocation, ok := lambdaAsyncInvocations.Get(id)
		if !ok {
			return
		}
		if delay := time.Until(invocation.NextAttemptAt); !invocation.NextAttemptAt.IsZero() && delay > 0 {
			timer := time.NewTimer(delay)
			<-timer.C
		}
		invocation, ok = lambdaAsyncInvocations.Get(id)
		if !ok {
			return
		}
		if time.Since(invocation.StartedAt) > time.Duration(invocation.MaxAgeSeconds)*time.Second {
			invocation.Unhandled = true
			invocation.Condition = "EventAgeExceeded"
			lambdaAsyncInvocations.Put(id, invocation)
			lambdaCompleteAsyncInvocation(id)
			return
		}

		invocation.InvokeCount++
		invocation.NextAttemptAt = time.Time{}
		lambdaAsyncInvocations.Put(id, invocation)
		response, unhandled, _ := invokeLambdaViaRuntimeAPI(invocation.Function, invocation.Payload)
		invocation, ok = lambdaAsyncInvocations.Get(id)
		if !ok {
			return
		}
		invocation.Response = append([]byte(nil), response...)
		invocation.Unhandled = unhandled
		if !unhandled {
			invocation.Condition = "Success"
			lambdaAsyncInvocations.Put(id, invocation)
			lambdaCompleteAsyncInvocation(id)
			return
		}
		if invocation.Failures >= invocation.MaxRetries {
			invocation.Condition = "RetriesExhausted"
			lambdaAsyncInvocations.Put(id, invocation)
			lambdaCompleteAsyncInvocation(id)
			return
		}
		delay := time.Minute
		if invocation.Failures > 0 {
			delay = 2 * time.Minute
		}
		invocation.Failures++
		invocation.NextAttemptAt = time.Now().UTC().Add(delay)
		lambdaAsyncInvocations.Put(id, invocation)
	}
}

func lambdaCompleteAsyncInvocation(id string) {
	invocation, ok := lambdaAsyncInvocations.Get(id)
	if !ok {
		return
	}
	if !invocation.Configured || invocation.Destination == nil {
		lambdaAsyncInvocations.Delete(id)
		return
	}
	var destination *lambdaDestination
	if invocation.Unhandled {
		destination = invocation.Destination.OnFailure
	} else {
		destination = invocation.Destination.OnSuccess
	}
	if destination == nil || destination.Destination == "" {
		lambdaAsyncInvocations.Delete(id)
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
			"condition":              invocation.Condition,
			"approximateInvokeCount": invocation.InvokeCount,
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
	lambdaAsyncInvocations.Delete(id)
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
			snsFanout(destinationARN, generateUUID(), "", string(body), nil)
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
			_, _, _ = invokeLambdaViaRuntimeAPI(target, body)
		}
	}
}
