package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/delivery"
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
	role := putServiceRole(t, "scheduler-kinesis", "scheduler.amazonaws.com", "kinesis:PutRecord")
	target := schedulerParseTarget(json.RawMessage(`{"Arn":"` + kinesisStreamARN("ticks") + `","RoleArn":"` + role + `","Input":"tick","KinesisParameters":{"PartitionKey":"clock"}}`))
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

	missing := schedulerParseTarget(json.RawMessage(`{"Arn":"` + kinesisStreamARN("gone") + `","RoleArn":"` + role + `","Input":"tick","KinesisParameters":{"PartitionKey":"clock"}}`))
	if outcome := schedulerAttemptTarget(missing); outcome.OK() || outcome.Retry || outcome.Err.(ebTargetError).Code != "ResourceNotFoundException" {
		t.Fatalf("PutRecord to a missing stream: %+v, want a final ResourceNotFoundException", outcome)
	}
}

// Every target invocation runs as the schedule's execution role: a role that
// does not trust scheduler.amazonaws.com, or does not allow the call, fails
// the invocation without reaching the target.
func TestSchedulerTargetsRunAsTheExecutionRole(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	queueURL, queueARN := testSQSQueue(t, router, "scheduler-role-queue")
	allowed := putServiceRole(t, "scheduler-sends", "scheduler.amazonaws.com", "sqs:SendMessage")
	unpermitted := putServiceRole(t, "scheduler-publishes", "scheduler.amazonaws.com", "sns:Publish")
	untrusting := putServiceRole(t, "events-sends", "events.amazonaws.com", "sqs:SendMessage")

	for name, role := range map[string]string{
		"unpermitted": unpermitted, "untrusting": untrusting, "missing": "arn:aws:iam::" + awsAccountID() + ":role/absent",
	} {
		outcome := schedulerAttemptTarget(schedulerTarget{Arn: queueARN, RoleArn: role, Input: "denied"})
		if outcome.OK() || outcome.Retry || outcome.Err.(ebTargetError).Code != "AccessDeniedException" {
			t.Fatalf("%s role: %+v, want a final AccessDeniedException", name, outcome)
		}
	}
	if !sqsQueueEmpty(t, router, queueURL) {
		t.Fatal("a role that may not send reached the queue")
	}

	if outcome := schedulerAttemptTarget(schedulerTarget{Arn: queueARN, RoleArn: allowed, Input: "allowed"}); !outcome.OK() {
		t.Fatalf("SendMessage as the permitted role: %v", outcome.Err)
	}
	if got := awaitSQSMessage(t, router, queueURL, 5*time.Second); got.Body != "allowed" {
		t.Fatalf("queue received %q, want the target's input", got.Body)
	}
}

// An EventBridge PutEvents target puts the Input on the bus as the detail,
// under the target's DetailType and Source, where the bus's rules route it.
func TestSchedulerEventBridgeTargetPutsTheEvent(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	queueURL, queueARN := sqsQueueAllowing(t, router, "scheduler-bus-events", "events.amazonaws.com")
	ebRuleWithTarget(t, router, "scheduler.nightly", map[string]any{"Id": "queue", "Arn": queueARN})
	role := putServiceRole(t, "scheduler-puts-events", "scheduler.amazonaws.com", "events:PutEvents")
	target := schedulerParseTarget(json.RawMessage(`{"Arn":"` + ebBusArn("default") + `","RoleArn":"` + role +
		`","Input":"{\"run\":\"nightly\"}","EventBridgeParameters":{"DetailType":"Nightly Run","Source":"scheduler.nightly"}}`))

	if outcome := schedulerAttemptTarget(target); !outcome.OK() {
		t.Fatalf("PutEvents outcome %v", outcome.Err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(awaitSQSMessage(t, router, queueURL, 10*time.Second).Body), &event); err != nil {
		t.Fatal(err)
	}
	detail, _ := event["detail"].(map[string]any)
	if event["source"] != "scheduler.nightly" || event["detail-type"] != "Nightly Run" || detail["run"] != "nightly" {
		t.Fatalf("event %v, want the target's source, detail type and input", event)
	}

	target.Arn = ebBusArn("absent")
	if outcome := schedulerAttemptTarget(target); outcome.OK() || outcome.Err.(ebTargetError).Code != "ResourceNotFoundException" {
		t.Fatalf("PutEvents to a missing bus: %+v, want ResourceNotFoundException", outcome)
	}
}

// A universal target calls the API action its ARN names with the Input as the
// request, authorized as the execution role against the request's resource.
func TestSchedulerUniversalTargetCallsTheAction(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	queueURL, queueARN := testSQSQueue(t, router, "scheduler-universal")
	input, _ := json.Marshal(map[string]any{"QueueUrl": queueURL, "MessageBody": "universal"})
	role := putServiceRole(t, "scheduler-universal", "scheduler.amazonaws.com")
	iamRolePolicies.Put("scheduler-universal/targets", IAMRolePolicy{
		RoleName: "scheduler-universal", PolicyName: "targets",
		PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"` + queueARN + `"}]}`,
	})
	target := schedulerTarget{Arn: "arn:aws:scheduler:::aws-sdk:sqs:sendMessage", RoleArn: role, Input: string(input)}

	if outcome := schedulerAttemptTarget(target); !outcome.OK() {
		t.Fatalf("sqs:sendMessage outcome %v", outcome.Err)
	}
	if got := awaitSQSMessage(t, router, queueURL, 5*time.Second); got.Body != "universal" {
		t.Fatalf("queue received %q, want the request's MessageBody", got.Body)
	}

	otherURL, _ := testSQSQueue(t, router, "scheduler-universal-other")
	other, _ := json.Marshal(map[string]any{"QueueUrl": otherURL, "MessageBody": "elsewhere"})
	target.Input = string(other)
	if outcome := schedulerAttemptTarget(target); outcome.OK() || outcome.Err.(ebTargetError).Code != "AccessDeniedException" {
		t.Fatalf("sqs:sendMessage to a queue the role may not send to: %+v, want AccessDeniedException", outcome)
	}
	if !sqsQueueEmpty(t, router, otherURL) {
		t.Fatal("the role reached a queue its policy does not name")
	}

	target.Arn = "arn:aws:scheduler:::aws-sdk:sagemaker:startPipelineExecution"
	if outcome := schedulerAttemptTarget(target); outcome.OK() || outcome.Err.(ebTargetError).Code != "UnsupportedTarget" {
		t.Fatalf("a service the simulator does not implement: %+v, want UnsupportedTarget", outcome)
	}
}

// CreateSchedule and UpdateSchedule refuse integer members outside the ranges
// the model declares.
func TestSchedulerValidatesModelRanges(t *testing.T) {
	for name, testCase := range map[string]struct {
		target, window string
		accepted       bool
	}{
		"within":          {`{"RetryPolicy":{"MaximumRetryAttempts":185,"MaximumEventAgeInSeconds":60},"EcsParameters":{"TaskCount":10}}`, `{"Mode":"FLEXIBLE","MaximumWindowInMinutes":1440}`, true},
		"retries above":   {`{"RetryPolicy":{"MaximumRetryAttempts":186}}`, `{"Mode":"OFF"}`, false},
		"retries below":   {`{"RetryPolicy":{"MaximumRetryAttempts":-1}}`, `{"Mode":"OFF"}`, false},
		"event age below": {`{"RetryPolicy":{"MaximumEventAgeInSeconds":59}}`, `{"Mode":"OFF"}`, false},
		"event age above": {`{"RetryPolicy":{"MaximumEventAgeInSeconds":86401}}`, `{"Mode":"OFF"}`, false},
		"task count":      {`{"EcsParameters":{"TaskCount":0}}`, `{"Mode":"OFF"}`, false},
		"weight":          {`{"EcsParameters":{"CapacityProviderStrategy":[{"capacityProvider":"FARGATE","weight":1001}]}}`, `{"Mode":"OFF"}`, false},
		"base":            {`{"EcsParameters":{"CapacityProviderStrategy":[{"capacityProvider":"FARGATE","base":100001}]}}`, `{"Mode":"OFF"}`, false},
		"window":          {`{}`, `{"Mode":"FLEXIBLE","MaximumWindowInMinutes":1441}`, false},
	} {
		recorder := httptest.NewRecorder()
		if accepted := schedulerValidateRanges(recorder, json.RawMessage(testCase.target), json.RawMessage(testCase.window)); accepted != testCase.accepted {
			t.Errorf("%s: accepted %v, want %v", name, accepted, testCase.accepted)
			continue
		}
		if !testCase.accepted && (recorder.Code != 400 || recorder.Header().Get("X-Amzn-Errortype") != "ValidationException") {
			t.Errorf("%s: %d %s, want a 400 ValidationException", name, recorder.Code, recorder.Header().Get("X-Amzn-Errortype"))
		}
	}
}

// A RunTask target is authorized against the RunTask request it sends, so a
// role policy that scopes ecs:RunTask to one cluster with an ecs:cluster
// condition allows a target on that cluster and refuses one on any other,
// for EventBridge Scheduler and EventBridge rules alike.
func TestECSTargetRoleIsAuthorizedAgainstTheTargetCluster(t *testing.T) {
	buildConformanceSimulator(t)
	taskDefinition := ecsArn("task-definition", "reconciler:1")
	allowedCluster, otherCluster := ecsArn("cluster", "workspaces"), ecsArn("cluster", "elsewhere")
	for _, service := range []string{"scheduler.amazonaws.com", "events.amazonaws.com"} {
		name := strings.TrimSuffix(service, ".amazonaws.com") + "-runs-reconciler"
		role := putServiceRole(t, name, service)
		iamRolePolicies.Put(name+"/run", IAMRolePolicy{
			RoleName: name, PolicyName: "run",
			PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:RunTask",` +
				`"Resource":"` + taskDefinition + `","Condition":{"ArnLike":{"ecs:cluster":"` + allowedCluster + `"}}}]}`,
		})
		fire := func(cluster string) delivery.Outcome {
			if service == "scheduler.amazonaws.com" {
				return schedulerAttemptTarget(schedulerTarget{Arn: cluster, RoleArn: role, EcsParameters: &schedulerEcsParams{TaskDefinitionArn: taskDefinition}})
			}
			return ebInvokeECSTarget("arn:aws:events:"+awsRegion()+":"+awsAccountID()+":rule/run", EBTarget{
				Arn: cluster, RoleArn: role, EcsParameters: json.RawMessage(`{"TaskDefinitionArn":"` + taskDefinition + `"}`),
			}, "")
		}
		if outcome := fire(otherCluster); outcome.OK() || outcome.Err.(ebTargetError).Code != "AccessDeniedException" {
			t.Fatalf("%s: RunTask on a cluster the policy does not name: %+v, want AccessDeniedException", service, outcome)
		}
		// The task definition is not registered, so the call the role is
		// allowed to make reaches ECS and fails there.
		if outcome := fire(allowedCluster); !outcome.OK() && outcome.Err.(ebTargetError).Code == "AccessDeniedException" {
			t.Fatalf("%s: RunTask on the cluster the policy names was refused: %v", service, outcome.Err)
		}
	}
}
