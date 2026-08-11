package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_AlarmActionsDispatchedToSNS locks the fix for issue #705:
// when a metric alarm transitions into ALARM, the simulator's background
// evaluator publishes the canonical CloudWatch alarm notification to each
// AlarmActions SNS topic, which a real SQS subscriber then receives.
//
// The path under test is the real one clients depend on: PutMetricAlarm →
// PutMetricData → evaluator tick → SNS fan-out → SQS ReceiveMessage. There
// are no short-circuits; the notification travels the same SNS publish
// pipeline a client-side Publish uses.
func TestCloudWatch_AlarmActionsDispatchedToSNS(t *testing.T) {
	cw := cloudwatchClient()
	snsC := snsClient()
	sqsC := sqsClient()
	ns := "Custom/AlarmActionsSDK"
	alarmName := "sdk-alarm-action-" + t.Name()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("alarm-actions-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("alarm-actions-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueARN := queueARNOf(t, sqsC, q.QueueUrl)

	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, topicARN)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		AlarmDescription:   aws.String("cpu above 50 \"adversarial\" line1\nline2 \x01"),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilization"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("missing"),
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	})

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(95), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	// Poll DescribeAlarms until the evaluator surfaces ALARM. DescribeAlarms
	// re-derives state from the live metric data, so this turning ALARM proves
	// the same input the evaluator sees has crossed the threshold.
	deadline := time.Now().Add(15 * time.Second)
	var sawAlarm bool
	for time.Now().Before(deadline) {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		if len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm {
			sawAlarm = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.True(t, sawAlarm, "alarm should transition to ALARM after breaching datapoint")

	// Poll the SQS subscriber for the alarm notification envelope. The
	// evaluator ticks every ~2s, so allow a wide margin.
	deadline = time.Now().Add(15 * time.Second)
	var notification map[string]any
	for time.Now().Before(deadline) {
		recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            q.QueueUrl,
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     2,
		})
		require.NoError(t, err)
		for _, m := range recv.Messages {
			bodyStr := aws.ToString(m.Body)
			var env map[string]any
			require.NoError(t, json.Unmarshal([]byte(bodyStr), &env), "SQS Body must be valid JSON: %s", bodyStr)
			if env["Type"] != "Notification" {
				continue
			}
			inner, ok := env["Message"].(string)
			require.True(t, ok, "SNS Message must be a string")
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(inner), &body), "embedded SNS Message must be valid JSON: %s", inner)
			if body["AlarmName"] == alarmName {
				notification = body
				break
			}
		}
		if notification != nil {
			break
		}
	}
	require.NotNil(t, notification, "alarm notification should reach the SQS subscriber via SNS")

	assert.Equal(t, "ALARM", notification["NewStateValue"])
	assert.Equal(t, "INSUFFICIENT_DATA", notification["OldStateValue"])
	assert.Equal(t, alarmName, notification["AlarmName"])
	assert.Equal(t, "us-east-1", notification["Region"])
	assert.Equal(t, "123456789012", notification["AWSAccountId"])
	assert.Contains(t, notification, "Trigger")
	assert.Equal(t, []any{topicARN}, notification["AlarmActions"])
	assert.Equal(t, "cpu above 50 \"adversarial\" line1\nline2 \x01", notification["AlarmDescription"])
}

// TestCloudWatch_OKActionsDispatchedToSNS verifies the OK transition path:
// after an alarm has fired ALARM, a non-breaching datapoint in a fresh
// evaluation period transitions the evaluator to OK and dispatches the
// OKActions topics.
func TestCloudWatch_OKActionsDispatchedToSNS(t *testing.T) {
	cw := cloudwatchClient()
	snsC := snsClient()
	sqsC := sqsClient()
	ns := "Custom/AlarmOKSDK"
	alarmName := "sdk-alarm-ok-" + t.Name()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("alarm-ok-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("alarm-ok-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, topicARN)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	// Period=1 + EvaluationPeriods=1 makes each second its own evaluation
	// bucket, so a breaching datapoint followed a few seconds later by a
	// non-breaching one produces a real ALARM → OK transition.
	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("Latency"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(1),
		Threshold:          aws.Float64(100),
		Statistic:          cwtypes.StatisticAverage,
		// TreatMissingData=breaching pins the alarm to ALARM once the
		// breaching datapoint ages out, so the subsequent non-breaching
		// datapoint produces an ALARM → OK transition (rather than
		// INSUFFICIENT_DATA → OK).
		TreatMissingData: aws.String("breaching"),
		ActionsEnabled:   aws.Bool(true),
		OKActions:        []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	})

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("Latency"), Value: aws.Float64(500), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	// Wait for DescribeAlarms to surface ALARM, then sleep past one
	// evaluator tick so cwAlarmLastState=ALARM is recorded before the
	// non-breaching datapoint arrives (DescribeAlarms evaluates live; the
	// dispatch path keys off the evaluator's remembered state).
	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should reach ALARM with the breaching datapoint")
	time.Sleep(3 * time.Second)

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("Latency"), Value: aws.Float64(1), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueOk
	}, 15*time.Second, 500*time.Millisecond, "alarm should return to OK after the non-breaching datapoint")

	// Drain the queue for the OK notification.
	deadline := time.Now().Add(15 * time.Second)
	var okNotification map[string]any
	for time.Now().Before(deadline) {
		recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            q.QueueUrl,
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     2,
		})
		require.NoError(t, err)
		for _, m := range recv.Messages {
			var env map[string]any
			if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &env); err != nil {
				continue
			}
			if env["Type"] != "Notification" {
				continue
			}
			inner, _ := env["Message"].(string)
			var body map[string]any
			if err := json.Unmarshal([]byte(inner), &body); err == nil &&
				body["AlarmName"] == alarmName &&
				body["NewStateValue"] == "OK" {
				okNotification = body
			}
		}
		if okNotification != nil {
			break
		}
	}
	require.NotNil(t, okNotification, "OK notification should reach the SQS subscriber via SNS")
	assert.Equal(t, "OK", okNotification["NewStateValue"])
	assert.Equal(t, "ALARM", okNotification["OldStateValue"], "OK must follow an ALARM transition")
}

// TestCloudWatch_ActionsDisabledSkipsDispatch verifies that when
// ActionsEnabled is false, state transitions do not publish to action
// topics. This guards the real-CloudWatch invariant the evaluator honours.
func TestCloudWatch_ActionsDisabledSkipsDispatch(t *testing.T) {
	cw := cloudwatchClient()
	snsC := snsClient()
	sqsC := sqsClient()
	ns := "Custom/AlarmDisabledSDK"
	alarmName := fmt.Sprintf("sdk-alarm-disabled-%s", t.Name())

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("alarm-disabled-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("alarm-disabled-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, topicARN)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("Errors"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		ActionsEnabled:     aws.Bool(false),
		AlarmActions:       []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	})

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("Errors"), Value: aws.Float64(10), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should still transition to ALARM (state evaluation is independent of ActionsEnabled)")

	// Allow multiple evaluator ticks to elapse, then confirm no notification arrived.
	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:        q.QueueUrl,
		WaitTimeSeconds: 5,
	})
	require.NoError(t, err)
	assert.Empty(t, recv.Messages, "no notification should be delivered when ActionsEnabled is false")
}
