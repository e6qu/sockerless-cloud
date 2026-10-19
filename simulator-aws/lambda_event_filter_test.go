package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

func sqsFilterCriteria(patterns ...string) map[string]any {
	filters := make([]any, 0, len(patterns))
	for _, p := range patterns {
		filters = append(filters, map[string]any{"Pattern": p})
	}
	return map[string]any{"Filters": filters}
}

func TestLambdaFilterCriteriaValidation(t *testing.T) {
	for _, fc := range []map[string]any{
		sqsFilterCriteria(`{"body":"scalar"}`),
		sqsFilterCriteria(`not json`),
		{"Filters": []any{map[string]any{}}},
		{"Filters": "x"},
	} {
		if _, err := lambdaESMFilters(fc); err == nil {
			t.Errorf("%v: accepted", fc)
		}
	}
	if f, err := lambdaESMFilters(nil); err != nil || f != nil {
		t.Errorf("no criteria: %v %v", f, err)
	}
}

// Filters read an Amazon SQS record with a JSON body decoded, match plain
// bodies as strings, and OR across the mapping's filters.
func TestLambdaRecordPassesFilters(t *testing.T) {
	var received []sqsReceivedMessage
	if err := json.Unmarshal([]byte(`[
		{"MessageId": "json", "Body": "{\"kind\":\"order\",\"total\":150}"},
		{"MessageId": "plain", "Body": "hello world"},
		{"MessageId": "attr", "Body": "x", "MessageAttributes": {"env": {"DataType": "String", "StringValue": "prod"}}}
	]`), &received); err != nil {
		t.Fatal(err)
	}
	records := lambdaSQSEventRecords("arn:aws:sqs:us-east-1:000000000000:q", received)
	cases := []struct {
		patterns []string
		want     []bool
	}{
		{nil, []bool{true, true, true}},
		{[]string{`{"body":{"kind":["order"]}}`}, []bool{true, false, false}},
		{[]string{`{"body":{"total":[{"numeric":[">",100]}]}}`}, []bool{true, false, false}},
		{[]string{`{"body":[{"prefix":"hello"}]}`}, []bool{false, true, false}},
		{[]string{`{"messageAttributes":{"env":{"stringValue":["prod"]}}}`}, []bool{false, false, true}},
		{[]string{`{"body":{"kind":["order"]}}`, `{"body":["hello world"]}`}, []bool{true, true, false}},
		{[]string{`{"eventSource":["aws:sqs"]}`}, []bool{true, true, true}},
	}
	for _, c := range cases {
		filters, err := lambdaESMFilters(sqsFilterCriteria(c.patterns...))
		if err != nil {
			t.Fatalf("%v: %v", c.patterns, err)
		}
		for i, rec := range records {
			got, err := lambdaRecordPassesFilters(filters, rec)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want[i] {
				t.Errorf("%v on %s: got %v, want %v", c.patterns, rec["messageId"], got, c.want[i])
			}
		}
	}
}

// AWS Lambda deletes the Amazon SQS messages a mapping's filter criteria
// reject instead of invoking the function with them.
func TestLambdaSQSPollerDeletesFilteredMessages(t *testing.T) {
	buildConformanceSimulator(t)
	// Lambda polls and deletes as the function's execution role.
	role := putServiceRole(t, "filtered-poller", "lambda.amazonaws.com", "sqs:ReceiveMessage", "sqs:DeleteMessage")

	queueARN := sqsQueueARN("filtered")
	queue := SQSQueue{Name: "filtered", ARN: queueARN, Attributes: map[string]string{}}
	for i, body := range []string{`{"kind":"refund"}`, "not an order"} {
		queue.Messages.Enqueue(sqsPayload{Body: body}, msgq.EnqueueOpts{ID: fmt.Sprintf("m%d", i+1)}, sqsPolicy(queue), time.Now())
	}
	sqsQueues.Put("filtered", queue)
	functionARN := "arn:aws:lambda:us-east-1:000000000000:function:fn"
	lambdaFunctions.Put("fn", LambdaFunction{FunctionName: "fn", FunctionArn: functionARN, Role: role})
	mapping := LambdaEventSourceMapping{
		UUID: "esm-1", EventSourceArn: queueARN, FunctionArn: functionARN, State: "Enabled",
		FilterCriteria: sqsFilterCriteria(`{"body":{"kind":["order"]}}`),
	}
	lambdaESMs.Put(mapping.UUID, mapping)

	lambdaPollSQSMapping(context.Background(), mapping)

	queue, _ = sqsQueues.Get("filtered")
	if len(queue.Messages.Messages) != 0 {
		t.Fatalf("filtered messages stay on the queue: %+v", queue.Messages.Messages)
	}
}
