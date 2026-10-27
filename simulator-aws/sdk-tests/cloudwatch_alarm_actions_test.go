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

// TestCloudWatch_AlarmActionsDispatchedToSNS expects a metric alarm entering
// ALARM to publish the CloudWatch alarm notification to each AlarmActions
// topic through the ordinary Amazon SNS publish path, which an Amazon SQS
// subscriber receives.
func TestCloudWatch_AlarmActionsDispatchedToSNS(t *testing.T) {
	t.Parallel()
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

	// DescribeAlarms derives state from the live metric data, so ALARM there means
	// the evaluator's input crossed the threshold.
	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should transition to ALARM after breaching datapoint")

	deadline := time.Now().Add(15 * time.Second)
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

// TestCloudWatch_OKActionsDispatchedToSNS expects a non-breaching datapoint in
// a later evaluation period to move a fired alarm to OK and dispatch its
// OKActions.
func TestCloudWatch_OKActionsDispatchedToSNS(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	snsC := snsClient()
	sqsC := sqsClient()
	// A namespace per run: metric data outlives the alarm that read it, so a
	// shared namespace lets one run's recovery datapoint satisfy the next run's
	// breach precondition.
	ns := fmt.Sprintf("Custom/AlarmOKSDK-%d", nextCloudWatchProbeID())
	alarmName := fmt.Sprintf("sdk-alarm-ok-%s-%d", t.Name(), nextCloudWatchProbeID())

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

	// The datapoints' timestamps put the breach and the recovery in different
	// evaluation buckets. The one-minute period also sets how long OK lasts:
	// TreatMissingData=breaching turns the bucket after the recovery back into a
	// breach, and a one-second period would leave OK alive for under a second.
	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("Latency"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(100),
		Statistic:          cwtypes.StatisticAverage,
		// TreatMissingData=breaching keeps the alarm in ALARM once the breaching
		// datapoint ages out, so the recovery transitions ALARM to OK rather than
		// INSUFFICIENT_DATA to OK.
		TreatMissingData: aws.String("breaching"),
		ActionsEnabled:   aws.Bool(true),
		OKActions:        []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	})

	// The breach goes two periods back, so it and the recovery cannot average into
	// one still-breaching bucket.
	breachAt := time.Now().UTC().Add(-2 * time.Minute)
	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("Latency"), Value: aws.Float64(500), Timestamp: aws.Time(breachAt)},
		},
	})
	require.NoError(t, err)

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should reach ALARM with the breaching datapoint")
	// The recovery must follow a recorded ALARM, or its transition skips a state
	// and the notification reports the wrong one.
	awaitRecordedTransition(ctx, t, cw, alarmName, "ALARM")

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("Latency"), Value: aws.Float64(1), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)
	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueOk, "alarm should return to OK after the non-breaching datapoint")

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

// TestCloudWatch_ActionsDisabledSkipsDispatch expects no action dispatch on
// transitions while ActionsEnabled is false.
func TestCloudWatch_ActionsDisabledSkipsDispatch(t *testing.T) {
	t.Parallel()
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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should still transition to ALARM (state evaluation is independent of ActionsEnabled)")

	// The alarm has transitioned, so a dispatch would have happened by now.
	requireQueueStaysEmpty(ctx, t, sqsC, q.QueueUrl,
		"no notification should be delivered when ActionsEnabled is false")
}
