package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// putServiceRole records an IAM role the service may assume, allowing action
// on every resource.
func putServiceRole(t *testing.T, name, service string, actions ...string) string {
	t.Helper()
	arn := "arn:aws:iam::" + awsAccountID() + ":role/" + name
	iamRoles.Put(name, IAMRole{
		RoleName: name,
		Arn:      arn,
		AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"` + service + `"},"Action":"sts:AssumeRole"}]}`,
	})
	if len(actions) > 0 {
		allowed, _ := json.Marshal(actions)
		iamRolePolicies.Put(name+"/targets", IAMRolePolicy{
			RoleName:       name,
			PolicyName:     "targets",
			PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":` + string(allowed) + `,"Resource":"*"}]}`,
		})
	}
	return arn
}

func ebRuleWithTarget(t *testing.T, router *AWSRouter, rule string, target map[string]any) {
	t.Helper()
	if status, out := awsJSONCall(t, router, "AWSEvents.PutRule", map[string]any{
		"Name": rule, "EventPattern": `{"source":["` + rule + `"]}`,
	}); status != 200 {
		t.Fatalf("PutRule: %d %v", status, out)
	}
	if status, out := awsJSONCall(t, router, "AWSEvents.PutTargets", map[string]any{
		"Rule": rule, "Targets": []map[string]any{target},
	}); status != 200 || out["FailedEntryCount"] != float64(0) {
		t.Fatalf("PutTargets: %d %v", status, out)
	}
}

func ebPutEvent(t *testing.T, router *AWSRouter, source, detail string) {
	t.Helper()
	if status, out := awsJSONCall(t, router, "AWSEvents.PutEvents", map[string]any{
		"Entries": []map[string]any{{"Source": source, "DetailType": "Order Placed", "Detail": detail}},
	}); status != 200 || out["FailedEntryCount"] != float64(0) {
		t.Fatalf("PutEvents: %d %v", status, out)
	}
}

// An API destination target is called with the connection's authorization and
// the event as its body; a 5xx answer is retried under the target's
// RetryPolicy until the endpoint accepts it.
func TestEventBridgeApiDestinationTargetRetriesUntilAccepted(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	var mu sync.Mutex
	var calls []*http.Request
	var bodies []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		calls, bodies = append(calls, r), append(bodies, string(body))
		if len(calls) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(endpoint.Close)

	status, connection := awsJSONCall(t, router, "AWSEvents.CreateConnection", map[string]any{
		"Name": "orders-api", "AuthorizationType": "BASIC",
		"AuthParameters": map[string]any{"BasicAuthParameters": map[string]any{"Username": "events", "Password": "s3cret"}},
	})
	if status != 200 {
		t.Fatalf("CreateConnection: %d %v", status, connection)
	}
	status, destination := awsJSONCall(t, router, "AWSEvents.CreateApiDestination", map[string]any{
		"Name": "orders-endpoint", "ConnectionArn": connection["ConnectionArn"],
		"InvocationEndpoint": endpoint.URL + "/orders/*", "HttpMethod": "POST",
	})
	if status != 200 {
		t.Fatalf("CreateApiDestination: %d %v", status, destination)
	}
	role := putServiceRole(t, "events-api-destination", "events.amazonaws.com", "events:InvokeApiDestination")
	ebRuleWithTarget(t, router, "orders", map[string]any{
		"Id": "api", "Arn": destination["ApiDestinationArn"], "RoleArn": role,
		"HttpParameters": map[string]any{"PathParameterValues": []string{"eu"}, "QueryStringParameters": map[string]string{"tenant": "a"}},
		"RetryPolicy":    map[string]any{"MaximumRetryAttempts": 2, "MaximumEventAgeInSeconds": 60},
	})
	ebPutEvent(t, router, "orders", `{"id":7}`)

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		count := len(calls)
		mu.Unlock()
		if count >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("the API destination was called %d times, want a 503 then a retry", len(calls))
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("events:s3cret"))
	for i, call := range calls {
		var event map[string]any
		if call.URL.Path != "/orders/eu" || call.URL.Query().Get("tenant") != "a" || call.Header.Get("Authorization") != want ||
			json.Unmarshal([]byte(bodies[i]), &event) != nil || event["detail-type"] != "Order Placed" {
			t.Fatalf("call %d: %s %s auth %q body %s", i, call.Method, call.URL, call.Header.Get("Authorization"), bodies[i])
		}
	}
}

// An API destination that keeps failing exhausts MaximumRetryAttempts and the
// event reaches the dead-letter queue naming the exhausted condition.
func TestEventBridgeApiDestinationExhaustsItsRetries(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(endpoint.Close)
	dlqURL, dlqARN := sqsQueueAllowing(t, router, "eb-api-dlq", "events.amazonaws.com")
	_, connection := awsJSONCall(t, router, "AWSEvents.CreateConnection", map[string]any{
		"Name": "failing-api", "AuthorizationType": "API_KEY",
		"AuthParameters": map[string]any{"ApiKeyAuthParameters": map[string]any{"ApiKeyName": "x-api-key", "ApiKeyValue": "k"}},
	})
	_, destination := awsJSONCall(t, router, "AWSEvents.CreateApiDestination", map[string]any{
		"Name": "failing-endpoint", "ConnectionArn": connection["ConnectionArn"],
		"InvocationEndpoint": endpoint.URL, "HttpMethod": "POST",
	})
	role := putServiceRole(t, "events-failing-api", "events.amazonaws.com", "events:InvokeApiDestination")
	ebRuleWithTarget(t, router, "failing", map[string]any{
		"Id": "api", "Arn": destination["ApiDestinationArn"], "RoleArn": role,
		"RetryPolicy":      map[string]any{"MaximumRetryAttempts": 1, "MaximumEventAgeInSeconds": 60},
		"DeadLetterConfig": map[string]any{"Arn": dlqARN},
	})
	ebPutEvent(t, router, "failing", `{"id":8}`)

	got := awaitSQSMessage(t, router, dlqURL, 15*time.Second)
	for name, want := range map[string]string{
		"TARGET_ARN": destination["ApiDestinationArn"].(string), "ERROR_CODE": "500",
		"RETRY_ATTEMPTS": "1", "EXHAUSTED_RETRY_CONDITION": "MaximumRetryAttempts",
	} {
		if got.Attributes[name] != want {
			t.Errorf("attribute %s = %q, want %q", name, got.Attributes[name], want)
		}
	}
}

// A Kinesis target puts the event on the stream as the target's role, keyed
// by the value at PartitionKeyPath; a role without kinesis:PutRecord sends the
// event to the dead-letter queue instead.
func TestEventBridgeKinesisTargetPutsTheEventAsItsRole(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	if status, out := awsJSONCall(t, router, "Kinesis_20131202.CreateStream", map[string]any{"StreamName": "orders", "ShardCount": 1}); status != 200 {
		t.Fatalf("CreateStream: %d %v", status, out)
	}
	streamARN := kinesisStreamARN("orders")
	role := putServiceRole(t, "events-kinesis", "events.amazonaws.com", "kinesis:PutRecord")
	ebRuleWithTarget(t, router, "stream", map[string]any{
		"Id": "stream", "Arn": streamARN, "RoleArn": role,
		"KinesisParameters": map[string]any{"PartitionKeyPath": "$.detail.customer"},
	})
	ebPutEvent(t, router, "stream", `{"customer":"c-42"}`)

	_, iterator := awsJSONCall(t, router, "Kinesis_20131202.GetShardIterator", map[string]any{
		"StreamName": "orders", "ShardId": "shardId-000000000000", "ShardIteratorType": "TRIM_HORIZON",
	})
	_, records := awsJSONCall(t, router, "Kinesis_20131202.GetRecords", map[string]any{"ShardIterator": iterator["ShardIterator"]})
	list, _ := records["Records"].([]any)
	if len(list) != 1 {
		t.Fatalf("stream records %v, want the event", records)
	}
	record := list[0].(map[string]any)
	data, _ := base64.StdEncoding.DecodeString(record["Data"].(string))
	var event map[string]any
	if record["PartitionKey"] != "c-42" || json.Unmarshal(data, &event) != nil || event["source"] != "stream" {
		t.Fatalf("record %v with data %s, want the event keyed by its customer", record, data)
	}

	dlqURL, dlqARN := sqsQueueAllowing(t, router, "eb-kinesis-dlq", "events.amazonaws.com")
	unauthorized := putServiceRole(t, "events-no-kinesis", "events.amazonaws.com")
	ebRuleWithTarget(t, router, "denied", map[string]any{
		"Id": "stream", "Arn": streamARN, "RoleArn": unauthorized, "DeadLetterConfig": map[string]any{"Arn": dlqARN},
	})
	ebPutEvent(t, router, "denied", `{"customer":"c-43"}`)
	got := awaitSQSMessage(t, router, dlqURL, 10*time.Second)
	if got.Attributes["ERROR_CODE"] != "AccessDeniedException" || got.Attributes["TARGET_ARN"] != streamARN {
		t.Fatalf("dead-letter attributes %v, want AccessDeniedException for the stream", got.Attributes)
	}
}
