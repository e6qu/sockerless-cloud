package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// sqsQueueAllowing creates a queue whose policy lets service send to it.
func sqsQueueAllowing(t *testing.T, router *AWSRouter, name, service string) (string, string) {
	t.Helper()
	url, arn := testSQSQueue(t, router, name)
	policy, _ := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect": "Allow", "Principal": map[string]any{"Service": service},
			"Action": "sqs:SendMessage", "Resource": arn,
		}},
	})
	status, out := awsJSONCall(t, router, "AmazonSQS.SetQueueAttributes", map[string]any{
		"QueueUrl": url, "Attributes": map[string]string{"Policy": string(policy)},
	})
	if status != 200 {
		t.Fatalf("SetQueueAttributes: %d %v", status, out)
	}
	return url, arn
}

// TestSNSHTTPDeliveryRetriesThenDeadLetters proves an HTTP subscription is
// retried under its DeliveryPolicy — numRetries retries after the first
// attempt — and that the message then lands in the RedrivePolicy queue with
// the attributes Amazon SNS adds.
func TestSNSHTTPDeliveryRetriesThenDeadLetters(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	var attempts atomic.Int32
	var lastBody atomic.Value
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastBody.Store(string(body))
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer endpoint.Close()
	dlqURL, dlqARN := sqsQueueAllowing(t, router, "sns-http-dlq", "sns.amazonaws.com")

	topicARN := snsTopicARN("http-retry-topic")
	snsTopics.Put("http-retry-topic", SNSTopic{Name: "http-retry-topic", ARN: topicARN, Attributes: map[string]string{}})
	sub := SNSSubscription{
		ARN: topicARN + ":sub", TopicARN: topicARN, Protocol: "http", Endpoint: endpoint.URL, Confirmed: true,
		ControlPlaneOrigin: "http://localhost:4566",
		Attributes: map[string]string{
			"DeliveryPolicy": `{"healthyRetryPolicy":{"minDelayTarget":1,"maxDelayTarget":1,"numRetries":2}}`,
			"RedrivePolicy":  `{"deadLetterTargetArn":"` + dlqARN + `"}`,
		},
	}
	if err := snsValidateDeliveryAttributes(sub.Attributes); err != nil {
		t.Fatalf("the delivery attributes were refused: %v", err)
	}
	snsSubscriptions.Put(sub.ARN, sub)
	snsFanout(topicARN, "message-1", "", "hello", nil)

	got := awaitSQSMessage(t, router, dlqURL, 15*time.Second)
	if n := attempts.Load(); n != 3 {
		t.Fatalf("endpoint saw %d attempts, want the first plus numRetries=2", n)
	}
	if got.Body != lastBody.Load() {
		t.Fatalf("dead-letter body %q differs from what the endpoint was sent %q", got.Body, lastBody.Load())
	}
	if got.Attributes["ErrorCode"] != "503" || got.Attributes["RequestID"] == "" || got.Attributes["ErrorMessage"] == "" {
		t.Fatalf("dead-letter attributes = %v", got.Attributes)
	}
	if policy := snsEffectiveDeliveryPolicy(sub); !strings.Contains(policy, `"numRetries":2`) {
		t.Fatalf("EffectiveDeliveryPolicy = %s", policy)
	}
}

func TestSNSRetryPolicyPhases(t *testing.T) {
	policy := snsRetryPolicy{
		MinDelayTarget: 2, MaxDelayTarget: 10, NumRetries: 8,
		NumNoDelayRetries: 1, NumMinDelayRetries: 2, NumMaxDelayRetries: 1, BackoffFunction: "linear",
	}
	want := []time.Duration{0, 2 * time.Second, 2 * time.Second, 4 * time.Second, 6 * time.Second,
		8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := policy.delay(i + 1); got != w {
			t.Errorf("retry %d waits %v, want %v", i+1, got, w)
		}
	}
	if err := (snsRetryPolicy{MinDelayTarget: 5, MaxDelayTarget: 1, NumRetries: 1, BackoffFunction: "linear"}).validate(); err == nil {
		t.Error("minDelayTarget above maxDelayTarget was accepted")
	}
	if err := snsValidateDeliveryAttributes(map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"arn:aws:sns:us-east-1:1:t"}`}); err == nil {
		t.Error("a RedrivePolicy naming a topic was accepted")
	}
}

// TestEventBridgeTargetFailureGoesToItsDeadLetterQueue proves a target
// EventBridge cannot deliver to is not retried when the failure is permanent
// and reaches the target's DeadLetterConfig queue with the documented
// attributes.
func TestEventBridgeTargetFailureGoesToItsDeadLetterQueue(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	dlqURL, dlqARN := sqsQueueAllowing(t, router, "eb-target-dlq", "events.amazonaws.com")
	missingQueue := "arn:aws:sqs:" + awsRegion() + ":" + awsAccountID() + ":eb-missing-queue"

	status, out := awsJSONCall(t, router, "AWSEvents.PutRule", map[string]any{
		"Name": "orders", "EventPattern": `{"source":["shop"]}`,
	})
	if status != 200 {
		t.Fatalf("PutRule: %d %v", status, out)
	}
	ruleArn, _ := out["RuleArn"].(string)
	status, out = awsJSONCall(t, router, "AWSEvents.PutTargets", map[string]any{
		"Rule": "orders",
		"Targets": []map[string]any{{
			"Id": "gone", "Arn": missingQueue,
			"RetryPolicy":      map[string]any{"MaximumRetryAttempts": 3, "MaximumEventAgeInSeconds": 60},
			"DeadLetterConfig": map[string]any{"Arn": dlqARN},
		}},
	})
	if status != 200 {
		t.Fatalf("PutTargets: %d %v", status, out)
	}
	status, out = awsJSONCall(t, router, "AWSEvents.PutEvents", map[string]any{
		"Entries": []map[string]any{{"Source": "shop", "DetailType": "Order Placed", "Detail": `{"id":7}`}},
	})
	if status != 200 {
		t.Fatalf("PutEvents: %d %v", status, out)
	}
	got := awaitSQSMessage(t, router, dlqURL, 10*time.Second)
	var event map[string]any
	if err := json.Unmarshal([]byte(got.Body), &event); err != nil || event["detail-type"] != "Order Placed" {
		t.Fatalf("dead-letter body %q is not the event", got.Body)
	}
	for name, want := range map[string]string{
		"RULE_ARN": ruleArn, "TARGET_ARN": missingQueue,
		"ERROR_CODE": "AWS.SimpleQueueService.NonExistentQueue", "RETRY_ATTEMPTS": "0",
	} {
		if got.Attributes[name] != want {
			t.Errorf("attribute %s = %q, want %q", name, got.Attributes[name], want)
		}
	}
}

// TestEventBridgeScheduledRuleSendsScheduledEvents drives the rule ticker at
// chosen instants: a rate() rule sends a "Scheduled Event" from aws.events,
// naming the rule, to its targets at each occurrence.
func TestEventBridgeScheduledRuleSendsScheduledEvents(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	queueURL, queueARN := sqsQueueAllowing(t, router, "eb-scheduled-queue", "events.amazonaws.com")
	if status, out := awsJSONCall(t, router, "AWSEvents.PutRule", map[string]any{
		"Name": "bad-rate", "ScheduleExpression": "rate(1 minutes)",
	}); status != 400 {
		t.Fatalf("PutRule accepted rate(1 minutes): %d %v", status, out)
	}
	status, out := awsJSONCall(t, router, "AWSEvents.PutRule", map[string]any{
		"Name": "every-five", "ScheduleExpression": "rate(5 minutes)",
	})
	if status != 200 {
		t.Fatalf("PutRule: %d %v", status, out)
	}
	ruleArn, _ := out["RuleArn"].(string)
	if status, out := awsJSONCall(t, router, "AWSEvents.PutTargets", map[string]any{
		"Rule": "every-five", "Targets": []map[string]any{{"Id": "q", "Arn": queueARN}},
	}); status != 200 {
		t.Fatalf("PutTargets: %d %v", status, out)
	}
	rule, _ := ebRules.Get(ebRuleKey("", "every-five"))
	created := time.Unix(rule.CreatedAt, 0).UTC()

	ticker := cron.NewTicker(sim.NewStateStore[cron.Record](), func() []cron.Entry { return ebScheduledRuleEntries(ebRules, ebTargets) })
	ticker.Tick(created.Add(time.Minute))
	if !sqsQueueEmpty(t, router, queueURL) {
		t.Fatal("the rule fired before its first occurrence")
	}
	ticker.Tick(created.Add(5*time.Minute + time.Second))
	got := awaitSQSMessage(t, router, queueURL, 10*time.Second)
	var event struct {
		Source     string   `json:"source"`
		DetailType string   `json:"detail-type"`
		Resources  []string `json:"resources"`
		Time       string   `json:"time"`
	}
	if err := json.Unmarshal([]byte(got.Body), &event); err != nil {
		t.Fatalf("scheduled event %q: %v", got.Body, err)
	}
	if event.Source != "aws.events" || event.DetailType != "Scheduled Event" ||
		len(event.Resources) != 1 || event.Resources[0] != ruleArn ||
		event.Time != created.Add(5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("scheduled event = %+v", event)
	}
}

// TestApplicationAutoScalingScheduledActionSetsCapacity drives the scheduled
// action ticker: at the occurrence the scalable target takes the action's
// MinCapacity and MaxCapacity, and the change is recorded as an activity.
func TestApplicationAutoScalingScheduledActionSetsCapacity(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	const resource = "service/sched-cluster/sched-svc"
	if status, out := awsJSONCall(t, router, "AnyScaleFrontendService.RegisterScalableTarget", map[string]any{
		"ServiceNamespace": "ecs", "ResourceId": resource, "ScalableDimension": "ecs:service:DesiredCount",
		"MinCapacity": 1, "MaxCapacity": 5,
	}); status != 200 {
		t.Fatalf("RegisterScalableTarget: %d %v", status, out)
	}
	if status, _ := awsJSONCall(t, router, "AnyScaleFrontendService.PutScheduledAction", map[string]any{
		"ServiceNamespace": "ecs", "ResourceId": resource, "ScalableDimension": "ecs:service:DesiredCount",
		"ScheduledActionName": "bad", "Schedule": "cron(0 8 * * * *)",
	}); status != 400 {
		t.Fatalf("PutScheduledAction accepted a cron without ?: %d", status)
	}
	if status, out := awsJSONCall(t, router, "AnyScaleFrontendService.PutScheduledAction", map[string]any{
		"ServiceNamespace": "ecs", "ResourceId": resource, "ScalableDimension": "ecs:service:DesiredCount",
		"ScheduledActionName": "mornings", "Schedule": "cron(0 8 * * ? *)", "Timezone": "America/New_York",
		"ScalableTargetAction": map[string]any{"MinCapacity": 3, "MaxCapacity": 9},
	}); status != 200 {
		t.Fatalf("PutScheduledAction: %d %v", status, out)
	}
	ticker := cron.NewTicker(sim.NewStateStore[cron.Record](), func() []cron.Entry {
		return appScheduledActionEntries(appScheduledActions, appScalableTargets)
	})
	ticker.Tick(time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC))
	target, _ := appScalableTargets.Get(appScalableTargetKey("ecs", resource, "ecs:service:DesiredCount"))
	if target.MinCapacity != 1 || target.MaxCapacity != 5 {
		t.Fatalf("capacity changed before 08:00 New York: %d-%d", target.MinCapacity, target.MaxCapacity)
	}
	ticker.Tick(time.Date(2026, 6, 10, 12, 0, 30, 0, time.UTC))
	target, _ = appScalableTargets.Get(appScalableTargetKey("ecs", resource, "ecs:service:DesiredCount"))
	if target.MinCapacity != 3 || target.MaxCapacity != 9 {
		t.Fatalf("capacity after 08:00 New York = %d-%d, want 3-9", target.MinCapacity, target.MaxCapacity)
	}
	_, out := awsJSONCall(t, router, "AnyScaleFrontendService.DescribeScalingActivities", map[string]any{
		"ServiceNamespace": "ecs", "ResourceId": resource,
	})
	activities, _ := out["ScalingActivities"].([]any)
	if len(activities) != 1 || !strings.Contains(activities[0].(map[string]any)["Cause"].(string), "mornings") {
		t.Fatalf("activities = %v", out)
	}
}
