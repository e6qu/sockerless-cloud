package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// A schedule's target reads its RetryPolicy, and the defaults are 185 retries
// within 86400 seconds.
func TestSchedulerRetryPolicyReadsTheTarget(t *testing.T) {
	for name, testCase := range map[string]struct {
		target   string
		attempts int
		age      time.Duration
	}{
		"defaults": {`{"Arn":"arn:aws:sqs:us-east-1:123456789012:q"}`, 186, 24 * time.Hour},
		"declared": {`{"Arn":"arn:aws:sqs:us-east-1:123456789012:q","RetryPolicy":{"MaximumRetryAttempts":0,"MaximumEventAgeInSeconds":60}}`, 1, time.Minute},
	} {
		policy := schedulerRetryPolicy(schedulerParseTarget(json.RawMessage(testCase.target)))
		if policy.MaxAttempts != testCase.attempts || policy.MaxAge != testCase.age {
			t.Errorf("%s: %d attempts within %s, want %d within %s", name, policy.MaxAttempts, policy.MaxAge, testCase.attempts, testCase.age)
		}
	}
}

// An invocation the target refuses reaches the target's DeadLetterConfig
// queue, sent as the schedule's execution role, with the attributes naming
// the schedule, the target and the error.
func TestSchedulerTargetFailureGoesToItsDeadLetterQueue(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	dlqURL, dlqARN := testSQSQueue(t, router, "scheduler-dlq")
	role := putServiceRole(t, "scheduler-execution", "scheduler.amazonaws.com", "sqs:SendMessage")
	missingQueue := "arn:aws:sqs:" + awsRegion() + ":" + awsAccountID() + ":scheduler-missing-queue"
	scheduleArn := "arn:aws:scheduler:" + awsRegion() + ":" + awsAccountID() + ":schedule/default/nightly"
	target, _ := json.Marshal(map[string]any{
		"Arn": missingQueue, "RoleArn": role, "Input": `{"run":"nightly"}`,
		"RetryPolicy":      map[string]any{"MaximumRetryAttempts": 3, "MaximumEventAgeInSeconds": 60},
		"DeadLetterConfig": map[string]any{"Arn": dlqARN},
	})
	fireSchedule(Schedule{Name: "nightly", GroupName: "default", Arn: scheduleArn, Target: target})

	got := awaitSQSMessage(t, router, dlqURL, 10*time.Second)
	if got.Body != `{"run":"nightly"}` {
		t.Fatalf("dead-letter body %q, want the target's input", got.Body)
	}
	for name, want := range map[string]string{
		"SCHEDULE_ARN": scheduleArn, "TARGET_ARN": missingQueue,
		"ERROR_CODE": "AWS.SimpleQueueService.NonExistentQueue", "RETRY_ATTEMPTS": "0",
	} {
		if got.Attributes[name] != want {
			t.Errorf("attribute %s = %q, want %q", name, got.Attributes[name], want)
		}
	}

	// Without sqs:SendMessage the execution role cannot reach the queue.
	iamRolePolicies.Delete("scheduler-execution/targets")
	fireSchedule(Schedule{Name: "nightly", GroupName: "default", Arn: scheduleArn, Target: target})
	status, out := awsJSONCall(t, router, "AmazonSQS.ReceiveMessage", map[string]any{"QueueUrl": dlqURL, "WaitTimeSeconds": 1})
	if messages, _ := out["Messages"].([]any); status != 200 || len(messages) != 0 {
		t.Fatalf("a role without sqs:SendMessage reached the dead-letter queue: %d %v", status, out)
	}
}

// A Kinesis target puts the schedule's input on the stream under the target's
// PartitionKey.
func TestSchedulerKinesisTargetPutsTheInput(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	if status, out := awsJSONCall(t, router, "Kinesis_20131202.CreateStream", map[string]any{"StreamName": "ticks", "ShardCount": 1}); status != 200 {
		t.Fatalf("CreateStream: %d %v", status, out)
	}
	target := schedulerParseTarget(json.RawMessage(`{"Arn":"` + kinesisStreamARN("ticks") + `","Input":"tick","KinesisParameters":{"PartitionKey":"clock"}}`))
	if outcome := schedulerAttemptTarget(target); !outcome.OK() {
		t.Fatalf("PutRecord outcome %v", outcome.Err)
	}
	_, iterator := awsJSONCall(t, router, "Kinesis_20131202.GetShardIterator", map[string]any{
		"StreamName": "ticks", "ShardId": "shardId-000000000000", "ShardIteratorType": "TRIM_HORIZON",
	})
	_, records := awsJSONCall(t, router, "Kinesis_20131202.GetRecords", map[string]any{"ShardIterator": iterator["ShardIterator"]})
	list, _ := records["Records"].([]any)
	if len(list) != 1 {
		t.Fatalf("stream records %v, want the input", records)
	}
	record := list[0].(map[string]any)
	if data, _ := base64.StdEncoding.DecodeString(record["Data"].(string)); string(data) != "tick" || record["PartitionKey"] != "clock" {
		t.Fatalf("record %v, want tick under partition key clock", record)
	}

	missing := schedulerParseTarget(json.RawMessage(`{"Arn":"` + kinesisStreamARN("gone") + `","Input":"tick","KinesisParameters":{"PartitionKey":"clock"}}`))
	if outcome := schedulerAttemptTarget(missing); outcome.OK() || outcome.Retry || outcome.Err.(ebTargetError).Code != "ResourceNotFoundException" {
		t.Fatalf("PutRecord to a missing stream: %+v, want a final ResourceNotFoundException", outcome)
	}
}
