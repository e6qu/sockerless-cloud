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
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_AlarmSNSActionToSQS_ProcessMode is the regression test for
// issue #741. It starts a fresh simulator-aws subprocess with SIM_RUNTIME=process
// and exercises the same shape as the adversarial CLI probe:
//   - SQS queue policy with Principal "*" and no aws:SourceArn condition
//   - a short settle window between Subscribe and PutMetricAlarm
//   - PutMetricData breaching the threshold
//   - DescribeAlarms surfacing ALARM
//   - SQS ReceiveMessage returning the alarm notification
//
// This proves delivery works in API-only process mode, independent of the
// shared TestMain simulator's runtime.
func TestCloudWatch_AlarmSNSActionToSQS_ProcessMode(t *testing.T) {
	url := startProcessModeSim(t)

	cw := cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	snsC := sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	sqsC := sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(url)
	})

	ns := "Custom/AlarmProcessRepro"
	alarmName := "process-repro-alarm"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("process-repro-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("process-repro-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	// Same permissive shape as the failing CLI probe: Principal "*", no
	// aws:SourceArn condition. Real AWS allows this; the sim must too.
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	_, err = sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: q.QueueUrl,
		Attributes: map[string]string{
			"Policy": policy,
		},
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	// Settle window matching the CLI probe. This ensures the test does not
	// rely on a race between subscription creation and the alarm firing.
	time.Sleep(3 * time.Second)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		AlarmDescription:   aws.String("Adversarial probe CPU alarm"),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilization"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
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
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should reach ALARM")

	// Give the background evaluator time to dispatch the ALARM action.
	time.Sleep(2 * time.Second)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "SQS subscriber should receive the alarm notification")

	var env map[string]any
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(recv.Messages[0].Body)), &env))
	assert.Equal(t, "Notification", env["Type"])

	inner, ok := env["Message"].(string)
	require.True(t, ok, "SNS Message must be a string")
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(inner), &body), "embedded SNS Message must be valid JSON")
	assert.Equal(t, alarmName, body["AlarmName"])
	assert.Equal(t, "ALARM", body["NewStateValue"])
	assert.Equal(t, "us-east-1", body["Region"])
	assert.Equal(t, "123456789012", body["AWSAccountId"])
}

// TestCloudWatch_AlarmSNSActionToSQS_RecreatedAlarmResetsState is the
// regression test for issue #749. Re-creating (PutMetricAlarm) an alarm that
// previously reached ALARM must reset the evaluator's remembered state so the
// new incarnation transitions from INSUFFICIENT_DATA and dispatches its
// AlarmActions again.
func TestCloudWatch_AlarmSNSActionToSQS_RecreatedAlarmResetsState(t *testing.T) {
	url := startProcessModeSim(t)

	cw := cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	snsC := sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	sqsC := sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(url)
	})

	ns := "Custom/AlarmRecreateRepro"
	alarmName := "process-recreate-alarm"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("process-recreate-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("process-recreate-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	_, err = sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: q.QueueUrl,
		Attributes: map[string]string{
			"Policy": policy,
		},
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	putAlarm := func() {
		_, err := cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
			AlarmName:          aws.String(alarmName),
			AlarmDescription:   aws.String("Adversarial recreate CPU alarm"),
			Namespace:          aws.String(ns),
			MetricName:         aws.String("CPUUtilization"),
			ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
			EvaluationPeriods:  aws.Int32(1),
			Period:             aws.Int32(60),
			Threshold:          aws.Float64(50),
			Statistic:          cwtypes.StatisticAverage,
			TreatMissingData:   aws.String("notBreaching"),
			ActionsEnabled:     aws.Bool(true),
			AlarmActions:       []string{topicARN},
		})
		require.NoError(t, err)
	}

	putAlarm()
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	})

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should reach ALARM")

	time.Sleep(2 * time.Second)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "first transition should deliver one notification")

	// Re-create the same alarm without deleting it, simulating churn from
	// repeated Terraform apply cycles that reuse alarm names.
	_, err = sqsC.PurgeQueue(ctx, &sqs.PurgeQueueInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)

	putAlarm()

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "recreated alarm should reach ALARM again")

	time.Sleep(2 * time.Second)

	recv2, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv2.Messages, 1, "recreated alarm must dispatch AlarmActions again")

	var env map[string]any
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(recv2.Messages[0].Body)), &env))
	inner, ok := env["Message"].(string)
	require.True(t, ok)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(inner), &body))
	assert.Equal(t, alarmName, body["AlarmName"])
	assert.Equal(t, "ALARM", body["NewStateValue"])
	assert.Equal(t, "INSUFFICIENT_DATA", body["OldStateValue"])
}

// TestCloudWatch_AlarmSNSActionToSQS_ResilientToOneBadAlarm is the regression
// test for issue #753. A panic while evaluating or dispatching one alarm must
// not kill the background evaluator goroutine; subsequent alarms must still
// dispatch their AlarmActions.
func TestCloudWatch_AlarmSNSActionToSQS_ResilientToOneBadAlarm(t *testing.T) {
	url := startProcessModeSim(t)

	cw := cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	snsC := sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	sqsC := sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(url)
	})

	ns := "Custom/AlarmResilienceRepro"
	badAlarmName := "bad-alarm-panics"
	goodAlarmName := "good-alarm-still-delivers"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("resilience-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("resilience-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	_, err = sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: q.QueueUrl,
		Attributes: map[string]string{
			"Policy": policy,
		},
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	// Create a "bad" alarm whose action target is an ARN the evaluator cannot
	// resolve to a known SNS topic. Real CloudWatch would silently drop the
	// action; the sim must not panic and must continue evaluating other alarms.
	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(badAlarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("BadMetric"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{"arn:aws:sns:us-east-1:123456789012:does-not-exist"},
	})
	require.NoError(t, err)

	// Create the "good" alarm pointing at the real topic.
	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(goodAlarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("GoodMetric"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{badAlarmName, goodAlarmName}})
	})

	// Breach the good alarm only; the bad alarm has no datapoints and stays OK.
	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("GoodMetric"), Value: aws.Float64(100), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{goodAlarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "good alarm should reach ALARM")

	time.Sleep(2 * time.Second)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "good alarm must still deliver even when a sibling alarm has an invalid action target")
}

// TestCloudWatch_AlarmSNSActionToSQS_AfterDanglingAlarms is the regression test
// for issue #758. It seeds the simulator with leftover alarms whose action
// targets no longer exist (a common result of Terraform apply/destroy churn),
// then creates a fresh alarm and asserts its AlarmActions still dispatch.
func TestCloudWatch_AlarmSNSActionToSQS_AfterDanglingAlarms(t *testing.T) {
	url := startProcessModeSim(t)

	cw := cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	snsC := sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	sqsC := sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(url)
	})

	// Seed dangling alarms: alarms whose SNS topics have been deleted but the
	// alarms themselves remain. This mirrors incomplete Terraform destroys.
	for i := 1; i <= 10; i++ {
		_, err := cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
			AlarmName:          aws.String(fmt.Sprintf("dangling-alarm-%d", i)),
			Namespace:          aws.String("Dangling/Metrics"),
			MetricName:         aws.String("CPUUtilization"),
			ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
			EvaluationPeriods:  aws.Int32(1),
			Period:             aws.Int32(60),
			Threshold:          aws.Float64(80),
			Statistic:          cwtypes.StatisticAverage,
			TreatMissingData:   aws.String("notBreaching"),
			ActionsEnabled:     aws.Bool(true),
			AlarmActions:       []string{fmt.Sprintf("arn:aws:sns:us-east-1:123456789012:deleted-topic-%d", i)},
		})
		require.NoError(t, err)
	}
	_, err := cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Dangling/Metrics"),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(95), Unit: cwtypes.StandardUnitPercent, Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	// Give the evaluator time to tick over the dangling alarms; any panic in
	// this window would kill the goroutine and break the subsequent probe.
	time.Sleep(3 * time.Second)

	// Now run the standard probe.
	ns := "Custom/AlarmAfterDanglingRepro"
	alarmName := "alarm-after-dangling"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("after-dangling-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("after-dangling-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	_, err = sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: q.QueueUrl,
		Attributes: map[string]string{
			"Policy": policy,
		},
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilization"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		dangling := make([]string, 10)
		for i := 1; i <= 10; i++ {
			dangling[i-1] = fmt.Sprintf("dangling-alarm-%d", i)
		}
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: append(dangling, alarmName)})
	})

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Unit: cwtypes.StandardUnitPercent, Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should reach ALARM after dangling alarms")

	time.Sleep(2 * time.Second)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "alarm notification must be delivered even after evaluator processed dangling alarms")

	var env map[string]any
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(recv.Messages[0].Body)), &env))
	inner, ok := env["Message"].(string)
	require.True(t, ok)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(inner), &body))
	assert.Equal(t, alarmName, body["AlarmName"])
	assert.Equal(t, "ALARM", body["NewStateValue"])
	assert.Equal(t, "INSUFFICIENT_DATA", body["OldStateValue"])
}

// TestCloudWatch_AlarmSNSActionToSQS_AfterDeleteAndRecreate verifies that
// deleting an alarm and then recreating it with the same name does not leave
// stale evaluator state that prevents the new alarm from dispatching.
func TestCloudWatch_AlarmSNSActionToSQS_AfterDeleteAndRecreate(t *testing.T) {
	url := startProcessModeSim(t)

	cw := cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	snsC := sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	sqsC := sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(url)
	})

	ns := "Custom/AlarmDeleteRecreateRepro"
	alarmName := "alarm-delete-recreate"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("delete-recreate-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("delete-recreate-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	_, err = sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: q.QueueUrl,
		Attributes: map[string]string{
			"Policy": policy,
		},
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	// First incarnation: create alarm, breach it, receive notification.
	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilization"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{topicARN},
	})
	require.NoError(t, err)

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Unit: cwtypes.StandardUnitPercent, Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "first alarm should reach ALARM")

	time.Sleep(2 * time.Second)

	var recv *sqs.ReceiveMessageOutput
	recv, err = sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "first incarnation must deliver")

	// Delete the alarm, then recreate it with the same name.
	_, err = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	require.NoError(t, err)

	_, err = sqsC.PurgeQueue(ctx, &sqs.PurgeQueueInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)

	// Recreate with a higher threshold and a fresh metric name so the first
	// incarnation's datapoint cannot pollute the second alarm's 60-second
	// evaluation bucket.
	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilizationAfterRecreate"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(150),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
		ActionsEnabled:     aws.Bool(true),
		AlarmActions:       []string{topicARN},
	})
	require.NoError(t, err)

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CPUUtilizationAfterRecreate"), Value: aws.Float64(200), Unit: cwtypes.StandardUnitPercent, Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "recreated alarm should reach ALARM")

	time.Sleep(2 * time.Second)

	recv, err = sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "recreated alarm must deliver after delete+recreate")

	var env2 map[string]any
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(recv.Messages[0].Body)), &env2))
	inner2, ok2 := env2["Message"].(string)
	require.True(t, ok2)
	var body2 map[string]any
	require.NoError(t, json.Unmarshal([]byte(inner2), &body2))
	assert.Equal(t, alarmName, body2["AlarmName"])
	assert.Equal(t, "ALARM", body2["NewStateValue"])
	assert.Equal(t, "INSUFFICIENT_DATA", body2["OldStateValue"])
}

// TestCloudWatch_AlarmSNSActionToSQS_NoSubscription verifies that an alarm
// whose action topic has no subscribers delivers nothing, and that the fan-out
// path logs the empty subscription set rather than silently doing nothing.
func TestCloudWatch_AlarmSNSActionToSQS_NoSubscription(t *testing.T) {
	url := startProcessModeSim(t)

	cw := cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	snsC := sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	sqsC := sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(url)
	})

	ns := "Custom/AlarmNoSubRepro"
	alarmName := "alarm-no-subscription"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("no-sub-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("no-sub-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})

	// Intentionally no subscription.

	time.Sleep(3 * time.Second)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilization"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
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
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Unit: cwtypes.StandardUnitPercent, Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should reach ALARM")

	time.Sleep(2 * time.Second)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Empty(t, recv.Messages, "alarm with no topic subscriptions must not deliver")
}

// TestCloudWatch_AlarmSNSActionToSQS_PolicyDenied verifies that an alarm whose
// subscriber queue policy denies sns:SendMessage delivers nothing. Real AWS
// drops the message; the sim must do the same and must log the denial.
func TestCloudWatch_AlarmSNSActionToSQS_PolicyDenied(t *testing.T) {
	url := startProcessModeSim(t)

	cw := cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	snsC := sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(url)
	})
	sqsC := sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(url)
	})

	ns := "Custom/AlarmPolicyDeniedRepro"
	alarmName := "alarm-policy-denied"

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("policy-denied-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("policy-denied-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	// Policy explicitly denies sns:SendMessage from the action topic.
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:SendMessage","Resource":"%s","Condition":{"ArnEquals":{"aws:SourceArn":"%s"}}}]}`, queueARN, topicARN)
	_, err = sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: q.QueueUrl,
		Attributes: map[string]string{
			"Policy": policy,
		},
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("CPUUtilization"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(50),
		Statistic:          cwtypes.StatisticAverage,
		TreatMissingData:   aws.String("notBreaching"),
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
			{MetricName: aws.String("CPUUtilization"), Value: aws.Float64(100), Unit: cwtypes.StandardUnitPercent, Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		desc, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
		require.NoError(t, err)
		return len(desc.MetricAlarms) == 1 && desc.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 15*time.Second, 500*time.Millisecond, "alarm should reach ALARM")

	time.Sleep(2 * time.Second)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Empty(t, recv.Messages, "alarm with denying queue policy must not deliver")
}
