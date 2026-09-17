package main

import (
	"encoding/json"
	"testing"
)

func eventsConditionContext(operation, body string) map[string][]string {
	return populatedConditionContext(jsonConditionRequest("AWSEvents."+operation), "events", operation, body)
}

func TestEventsConditionKeysReadTheEventsSent(t *testing.T) {
	ctx := eventsConditionContext("PutEvents", `{"Entries": [
		{"Source": "com.example.orders", "DetailType": "OrderPlaced", "Detail": "{}"},
		{"Source": "com.example.orders", "DetailType": "OrderShipped", "Detail": "{}"}
	]}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"events:source":      {"com.example.orders"},
		"events:detail-type": {"OrderPlaced", "OrderShipped"},
	})
}

func TestEventsConditionKeysReadTheRulePattern(t *testing.T) {
	pattern, err := json.Marshal(map[string]any{
		"source":      []any{"aws.health", map[string]any{"prefix": "aws."}},
		"detail-type": []string{"AWS Health Event"},
		"detail": map[string]any{
			"service":       []string{"EC2"},
			"eventTypeCode": []string{"AWS_EC2_INSTANCE_RETIREMENT_SCHEDULED"},
			"userIdentity":  map[string]any{"principalId": []string{"AIDAEXAMPLE"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"Name": "r", "EventPattern": string(pattern)})
	if err != nil {
		t.Fatal(err)
	}
	assertPopulatedConditionValues(t, eventsConditionContext("PutRule", string(body)), map[string][]string{
		"events:source":                          {"aws.health"},
		"events:detail-type":                     {"AWS Health Event"},
		"events:detail.service":                  {"EC2"},
		"events:detail.eventTypeCode":            {"AWS_EC2_INSTANCE_RETIREMENT_SCHEDULED"},
		"events:detail.userIdentity.principalId": {"AIDAEXAMPLE"},
	})
}

func TestEventsConditionKeysReadTargetsAndEndpointBuses(t *testing.T) {
	ctx := eventsConditionContext("PutTargets", `{"Rule": "r", "Targets": [
		{"Id": "1", "Arn": "arn:aws:sqs:us-east-1:123456789012:q", "DeadLetterConfig": {"Arn": "arn:aws:sqs:us-east-1:123456789012:dlq"}},
		{"Id": "2", "Arn": "arn:aws:lambda:us-east-1:123456789012:function:f"}
	]}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{"events:TargetArn": {
		"arn:aws:sqs:us-east-1:123456789012:q",
		"arn:aws:lambda:us-east-1:123456789012:function:f",
	}})

	ctx = eventsConditionContext("CreateEndpoint", `{"Name": "e", "EventBuses": [
		{"EventBusArn": "arn:aws:events:us-east-1:123456789012:event-bus/b"},
		{"EventBusArn": "arn:aws:events:us-west-2:123456789012:event-bus/b"}
	]}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{"events:EventBusArn": {
		"arn:aws:events:us-east-1:123456789012:event-bus/b",
		"arn:aws:events:us-west-2:123456789012:event-bus/b",
	}})
}

func TestEventsConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := eventsConditionContext("PutRule", `{"Name": "r", "ScheduleExpression": "rate(5 minutes)"}`)
	assertConditionKeysAbsent(t, ctx, "events:source", "events:detail-type", "events:detail.service",
		"events:detail.eventTypeCode", "events:detail.userIdentity.principalId")
	ctx = eventsConditionContext("PutRule", `{"Name": "r", "EventPattern": "{\"source\": [{\"prefix\": \"aws.\"}]}"}`)
	assertConditionKeysAbsent(t, ctx, "events:source")
	ctx = eventsConditionContext("DescribeRule", `{"Name": "r", "Targets": [{"Arn": "arn:aws:sqs:us-east-1:123456789012:q"}]}`)
	assertConditionKeysAbsent(t, ctx, "events:TargetArn")
}
