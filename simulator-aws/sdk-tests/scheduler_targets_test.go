package aws_sdk_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createSchedulerTestQueue creates a queue and returns its URL and ARN.
func createSchedulerTestQueue(t *testing.T, name string) (*string, string) {
	t.Helper()
	sqsC := sqsClient()
	created, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl}) })
	attributes, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: created.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	return created.QueueUrl, attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}

// createOneTimeSchedule creates an at() schedule due two seconds from now.
func createOneTimeSchedule(t *testing.T, name string, target *schedtypes.Target) {
	t.Helper()
	sched := schedulerClient()
	_, err := sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(name),
		ScheduleExpression: aws.String("at(" + time.Now().UTC().Add(2*time.Second).Format("2006-01-02T15:04:05") + ")"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target:             target,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(name)}) })
}

// An EventBridge PutEvents target puts the schedule's Input on the bus as the
// event detail, under the target's DetailType and Source, where a rule routes
// it on to an Amazon SQS queue.
func TestScheduler_EventBridgeTargetPutsTheEvent_SDK(t *testing.T) {
	const source = "scheduler.sdk-events"
	eb := eventbridgeClient()
	queueURL, queueARN := createSchedulerTestQueue(t, "scheduler-sdk-bus-events")
	rule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(source), EventPattern: aws.String(`{"source":["` + source + `"]}`)})
	require.NoError(t, err)
	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(source), Targets: []ebtypes.Target{{Id: aws.String("queue"), Arn: aws.String(queueARN)}}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String(source), Ids: []string{"queue"}})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(source)})
	})
	setEBQueuePolicy(t, sqsClient(), queueURL, queueARN, aws.ToString(rule.RuleArn))
	bus, err := eb.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{})
	require.NoError(t, err)

	createOneTimeSchedule(t, "sdk-put-events", &schedtypes.Target{
		Arn:     bus.Arn,
		RoleArn: aws.String(createServiceRole(t, "scheduler-sdk-put-events", "scheduler.amazonaws.com", "events:PutEvents")),
		Input:   aws.String(`{"run":"nightly"}`),
		EventBridgeParameters: &schedtypes.EventBridgeParameters{
			DetailType: aws.String("Nightly Run"), Source: aws.String(source),
		},
	})

	message := receiveWithAttributes(t, sqsClient(), queueURL, 30*time.Second)
	var event struct {
		Source     string         `json:"source"`
		DetailType string         `json:"detail-type"`
		Detail     map[string]any `json:"detail"`
	}
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(message.Body)), &event))
	assert.Equal(t, source, event.Source)
	assert.Equal(t, "Nightly Run", event.DetailType)
	assert.Equal(t, "nightly", event.Detail["run"])
}

// A universal target calls the API action its ARN names, as the execution
// role, with the Input as the request.
func TestScheduler_UniversalTargetCallsTheAction_SDK(t *testing.T) {
	queueURL, _ := createSchedulerTestQueue(t, "scheduler-sdk-universal")
	input, err := json.Marshal(map[string]string{"QueueUrl": aws.ToString(queueURL), "MessageBody": "universal-target"})
	require.NoError(t, err)

	createOneTimeSchedule(t, "sdk-universal", &schedtypes.Target{
		Arn:     aws.String("arn:aws:scheduler:::aws-sdk:sqs:sendMessage"),
		RoleArn: aws.String(createServiceRole(t, "scheduler-sdk-universal", "scheduler.amazonaws.com", "sqs:SendMessage")),
		Input:   aws.String(string(input)),
	})

	message := receiveWithAttributes(t, sqsClient(), queueURL, 30*time.Second)
	assert.Equal(t, "universal-target", aws.ToString(message.Body))
}

// A target whose execution role does not allow the call its invocation makes
// never reaches the target; the refusal goes to the dead-letter queue.
func TestScheduler_TargetRunsAsTheExecutionRole_SDK(t *testing.T) {
	queueURL, queueARN := createSchedulerTestQueue(t, "scheduler-sdk-role-target")
	dlqURL, dlqARN := createSchedulerTestQueue(t, "scheduler-sdk-role-dlq")
	role := createServiceRole(t, "scheduler-sdk-no-send", "scheduler.amazonaws.com", "sns:Publish")
	iamC := iamClient()
	_, err := iamC.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String("scheduler-sdk-no-send"), PolicyName: aws.String("dead-letter"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"` + dlqARN + `"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamC.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String("scheduler-sdk-no-send"), PolicyName: aws.String("dead-letter")})
	})

	createOneTimeSchedule(t, "sdk-role-denied", &schedtypes.Target{
		Arn: aws.String(queueARN), RoleArn: aws.String(role), Input: aws.String("denied"),
		DeadLetterConfig: &schedtypes.DeadLetterConfig{Arn: aws.String(dlqARN)},
	})

	message := receiveWithAttributes(t, sqsClient(), dlqURL, 30*time.Second)
	assert.Equal(t, "denied", aws.ToString(message.Body))
	assert.Equal(t, "AccessDeniedException", messageAttribute(message, "ERROR_CODE"))
	out, err := sqsClient().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: queueURL, WaitTimeSeconds: 1})
	require.NoError(t, err)
	assert.Empty(t, out.Messages, "the target queue received a message the role may not send")
}

// CreateSchedule and UpdateSchedule refuse a RetryPolicy outside the ranges
// the model declares.
func TestScheduler_RetryPolicyRanges_SDK(t *testing.T) {
	sched := schedulerClient()
	target := func(policy *schedtypes.RetryPolicy) *schedtypes.Target {
		return &schedtypes.Target{
			Arn:         aws.String("arn:aws:sqs:us-east-1:123456789012:scheduler-sdk-retry-ranges"),
			RoleArn:     aws.String("arn:aws:iam::123456789012:role/scheduler-sdk-retry-ranges"),
			RetryPolicy: policy,
		}
	}
	window := &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff}
	for name, policy := range map[string]*schedtypes.RetryPolicy{
		"attempts above 185": {MaximumRetryAttempts: aws.Int32(186)},
		"age below 60":       {MaximumEventAgeInSeconds: aws.Int32(59)},
		"age above 86400":    {MaximumEventAgeInSeconds: aws.Int32(86401)},
	} {
		_, err := sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
			Name: aws.String("sdk-retry-ranges"), ScheduleExpression: aws.String("rate(1 hour)"),
			FlexibleTimeWindow: window, Target: target(policy),
		})
		var invalid *schedtypes.ValidationException
		require.True(t, errors.As(err, &invalid), "%s: %v, want a ValidationException", name, err)
	}

	_, err := sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name: aws.String("sdk-retry-ranges"), ScheduleExpression: aws.String("rate(1 hour)"), FlexibleTimeWindow: window,
		Target: target(&schedtypes.RetryPolicy{MaximumRetryAttempts: aws.Int32(185), MaximumEventAgeInSeconds: aws.Int32(60)}),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String("sdk-retry-ranges")})
	})
	_, err = sched.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{
		Name: aws.String("sdk-retry-ranges"), ScheduleExpression: aws.String("rate(1 hour)"), FlexibleTimeWindow: window,
		Target: target(&schedtypes.RetryPolicy{MaximumRetryAttempts: aws.Int32(186)}),
	})
	var invalid *schedtypes.ValidationException
	require.True(t, errors.As(err, &invalid), "UpdateSchedule: %v, want a ValidationException", err)
}
