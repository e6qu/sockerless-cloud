package aws_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// receiveWithAttributes long-polls a queue for one message and its message
// attributes.
func receiveWithAttributes(t *testing.T, sqsC *sqs.Client, queueURL *string, within time.Duration) sqstypes.Message {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              queueURL,
			MaxNumberOfMessages:   1,
			WaitTimeSeconds:       2,
			MessageAttributeNames: []string{"All"},
		})
		require.NoError(t, err)
		if len(out.Messages) == 1 {
			return out.Messages[0]
		}
	}
	t.Fatalf("no message reached %s within %s", aws.ToString(queueURL), within)
	return sqstypes.Message{}
}

func messageAttribute(m sqstypes.Message, name string) string {
	if v, ok := m.MessageAttributes[name]; ok {
		return aws.ToString(v.StringValue)
	}
	return ""
}

// TestEventBridge_TargetDeadLetterConfigSDK proves a target EventBridge cannot
// deliver to sends the event to the target's DeadLetterConfig queue with the
// RULE_ARN, TARGET_ARN and ERROR_CODE attributes.
func TestEventBridge_TargetDeadLetterConfigSDK(t *testing.T) {
	eb := eventbridgeClient()
	sqsC := sqsClient()
	dlq, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("eb-sdk-target-dlq")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := queueARNOf(t, sqsC, dlq.QueueUrl)

	rule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String("eb-sdk-dlq-rule"),
		EventPattern: aws.String(`{"source":["sockerless.dlq"]}`),
	})
	require.NoError(t, err)
	ruleARN := aws.ToString(rule.RuleArn)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String("eb-sdk-dlq-rule"), Ids: []string{"gone"}})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String("eb-sdk-dlq-rule")})
	})
	setEBQueuePolicy(t, sqsC, dlq.QueueUrl, dlqARN, ruleARN)
	missingQueue := "arn:aws:sqs:us-east-1:123456789012:eb-sdk-queue-that-does-not-exist"
	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String("eb-sdk-dlq-rule"),
		Targets: []ebtypes.Target{{
			Id:               aws.String("gone"),
			Arn:              aws.String(missingQueue),
			RetryPolicy:      &ebtypes.RetryPolicy{MaximumRetryAttempts: aws.Int32(2), MaximumEventAgeInSeconds: aws.Int32(60)},
			DeadLetterConfig: &ebtypes.DeadLetterConfig{Arn: aws.String(dlqARN)},
		}},
	})
	require.NoError(t, err)
	_, err = eb.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
		Source: aws.String("sockerless.dlq"), DetailType: aws.String("undeliverable"), Detail: aws.String(`{"n":1}`),
	}}})
	require.NoError(t, err)

	got := receiveWithAttributes(t, sqsC, dlq.QueueUrl, 20*time.Second)
	assert.Equal(t, ruleARN, messageAttribute(got, "RULE_ARN"))
	assert.Equal(t, missingQueue, messageAttribute(got, "TARGET_ARN"))
	assert.Equal(t, "AWS.SimpleQueueService.NonExistentQueue", messageAttribute(got, "ERROR_CODE"))
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(got.Body)), &event))
	assert.Equal(t, "undeliverable", event["detail-type"])
}

// TestEventBridge_ScheduledRuleValidationSDK pins the PutRule rules for
// ScheduleExpression: a schedule only on the default bus, and a rate() whose
// unit agrees with its value.
func TestEventBridge_ScheduledRuleValidationSDK(t *testing.T) {
	eb := eventbridgeClient()
	_, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name: aws.String("eb-sdk-bad-rate"), ScheduleExpression: aws.String("rate(1 minutes)"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ValidationException")

	_, err = eb.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String("eb-sdk-schedule-bus")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String("eb-sdk-schedule-bus")})
	})
	_, err = eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name: aws.String("eb-sdk-custom-bus-schedule"), EventBusName: aws.String("eb-sdk-schedule-bus"),
		ScheduleExpression: aws.String("rate(5 minutes)"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default event bus")

	_, err = eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name: aws.String("eb-sdk-schedule"), ScheduleExpression: aws.String("cron(0 12 * * ? *)"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String("eb-sdk-schedule")}) })
}

// TestScheduler_ExpressionTimezoneValidationSDK proves CreateSchedule reads
// ScheduleExpressionTimezone: an IANA zone round-trips, an unknown one and a
// cron() naming both day fields are refused.
func TestScheduler_ExpressionTimezoneValidationSDK(t *testing.T) {
	c := schedulerClient()
	target := &schedtypes.Target{
		Arn:     aws.String("arn:aws:sqs:us-east-1:123456789012:scheduler-tz-target"),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler"),
	}
	window := &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff}
	_, err := c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name: aws.String("tz-unknown"), ScheduleExpression: aws.String("cron(0 9 * * ? *)"),
		ScheduleExpressionTimezone: aws.String("Mars/Olympus_Mons"), Target: target, FlexibleTimeWindow: window,
	})
	require.Error(t, err)
	_, err = c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name: aws.String("tz-both-days"), ScheduleExpression: aws.String("cron(0 9 * * * *)"),
		Target: target, FlexibleTimeWindow: window,
	})
	require.Error(t, err)
	_, err = c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name: aws.String("tz-tokyo"), ScheduleExpression: aws.String("cron(0 9 * * ? *)"),
		ScheduleExpressionTimezone: aws.String("Asia/Tokyo"), Target: target, FlexibleTimeWindow: window,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String("tz-tokyo")}) })
	got, err := c.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String("tz-tokyo")})
	require.NoError(t, err)
	assert.Equal(t, "Asia/Tokyo", aws.ToString(got.ScheduleExpressionTimezone))
}

// TestSNS_HTTPDeliveryPolicyAndRedrivePolicySDK proves an HTTP subscription's
// DeliveryPolicy governs its retries — numRetries after the first attempt —
// and that the message then reaches the RedrivePolicy dead-letter queue.
func TestSNS_HTTPDeliveryPolicyAndRedrivePolicySDK(t *testing.T) {
	var notifications atomic.Int32
	confirmations := make(chan map[string]any, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("x-amz-sns-message-type") == "SubscriptionConfirmation" {
			var envelope map[string]any
			_ = json.Unmarshal(body, &envelope)
			confirmations <- envelope
			w.WriteHeader(http.StatusOK)
			return
		}
		notifications.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer endpoint.Close()

	client := snsClient()
	sqsC := sqsClient()
	topic, err := client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("http-redrive-topic")})
	require.NoError(t, err)
	topicARN := aws.ToString(topic.TopicArn)
	t.Cleanup(func() { _, _ = client.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)}) })
	dlq, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("sns-http-redrive-dlq")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := queueARNOf(t, sqsC, dlq.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(dlq.QueueUrl), dlqARN, topicARN)

	subscribed, err := client.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topicARN), Protocol: aws.String("http"), Endpoint: aws.String(endpoint.URL),
		ReturnSubscriptionArn: true,
		Attributes: map[string]string{
			"DeliveryPolicy": `{"healthyRetryPolicy":{"minDelayTarget":1,"maxDelayTarget":1,"numRetries":1}}`,
			"RedrivePolicy":  `{"deadLetterTargetArn":"` + dlqARN + `"}`,
		},
	})
	require.NoError(t, err)
	var confirmation map[string]any
	select {
	case confirmation = <-confirmations:
	case <-time.After(10 * time.Second):
		t.Fatal("no subscription confirmation arrived")
	}
	_, err = client.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: aws.String(topicARN), Token: aws.String(confirmation["Token"].(string)),
	})
	require.NoError(t, err)
	attributes, err := client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: subscribed.SubscriptionArn})
	require.NoError(t, err)
	assert.Contains(t, attributes.Attributes["EffectiveDeliveryPolicy"], `"numRetries":1`)

	_, err = client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicARN), Message: aws.String("undeliverable")})
	require.NoError(t, err)
	got := receiveWithAttributes(t, sqsC, dlq.QueueUrl, 30*time.Second)
	assert.Equal(t, int32(2), notifications.Load(), "the first attempt plus numRetries=1")
	assert.Equal(t, "503", messageAttribute(got, "ErrorCode"))
	assert.NotEmpty(t, messageAttribute(got, "RequestID"))
	var envelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(got.Body)), &envelope))
	assert.Equal(t, "undeliverable", envelope["Message"])

	_, err = client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
		SubscriptionArn: subscribed.SubscriptionArn, AttributeName: aws.String("DeliveryPolicy"),
		AttributeValue: aws.String(`{"healthyRetryPolicy":{"minDelayTarget":30,"maxDelayTarget":5,"numRetries":1}}`),
	})
	require.Error(t, err, "a minDelayTarget above maxDelayTarget is refused")
}

// TestApplicationAutoScaling_ScheduledActionRunsSDK proves a scheduled action
// takes effect at its time: an at() action a few seconds out sets the
// scalable target's MinCapacity and MaxCapacity.
func TestApplicationAutoScaling_ScheduledActionRunsSDK(t *testing.T) {
	c := appAutoScalingClient()
	const (
		ns         = aastypes.ServiceNamespaceEcs
		resourceID = "service/sched-run-cluster/sched-run-svc"
		dim        = aastypes.ScalableDimensionECSServiceDesiredCount
	)
	appASRegisterTarget(t, c, ns, resourceID, dim)
	at := time.Now().UTC().Add(3 * time.Second).Format("2006-01-02T15:04:05")
	_, err := c.PutScheduledAction(ctx, &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
		ScheduledActionName: aws.String("burst"), Schedule: aws.String("at(" + at + ")"),
		ScalableTargetAction: &aastypes.ScalableTargetAction{MinCapacity: aws.Int32(4), MaxCapacity: aws.Int32(12)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
			ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
			ScheduledActionName: aws.String("burst"),
		})
	})
	require.Eventually(t, func() bool {
		out, err := c.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
			ServiceNamespace: ns, ResourceIds: []string{resourceID}, ScalableDimension: dim,
		})
		return err == nil && len(out.ScalableTargets) == 1 &&
			aws.ToInt32(out.ScalableTargets[0].MinCapacity) == 4 && aws.ToInt32(out.ScalableTargets[0].MaxCapacity) == 12
	}, 20*time.Second, 500*time.Millisecond, "the scheduled action never set the target's capacity")

	_, err = c.PutScheduledAction(ctx, &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
		ScheduledActionName: aws.String("bad"), Schedule: aws.String("cron(0 8 * * * *)"),
	})
	var invalid *aastypes.ValidationException
	require.ErrorAs(t, err, &invalid)
}
