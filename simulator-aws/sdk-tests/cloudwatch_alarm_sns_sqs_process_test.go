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

// TestCloudWatch_AlarmSNSActionToSQS_ProcessMode runs, against a simulator in
// SIM_RUNTIME=process, an alarm whose action reaches an Amazon SQS queue whose
// policy allows Principal "*" with no aws:SourceArn condition: a breaching
// metric drives the alarm to ALARM and the queue receives the notification.
func TestCloudWatch_AlarmSNSActionToSQS_ProcessMode(t *testing.T) {
	t.Parallel()
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

	// AWS accepts a Principal "*" policy with no aws:SourceArn condition.
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

	awaitSubscription(ctx, t, snsC, topicARN, queueARN)

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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should reach ALARM")

	recv := awaitQueueMessages(ctx, t, sqsC, q.QueueUrl, 1)
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

// TestCloudWatch_AlarmSNSActionToSQS_RecreatedAlarmResetsState expects
// PutMetricAlarm over an alarm that reached ALARM to restart it from
// INSUFFICIENT_DATA, so it dispatches its AlarmActions again.
func TestCloudWatch_AlarmSNSActionToSQS_RecreatedAlarmResetsState(t *testing.T) {
	t.Parallel()
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

	awaitSubscription(ctx, t, snsC, topicARN, queueARN)

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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should reach ALARM")

	recv := awaitQueueMessages(ctx, t, sqsC, q.QueueUrl, 1)
	require.Len(t, recv.Messages, 1, "first transition should deliver one notification")

	// Re-create the alarm without deleting it, as repeated Terraform applies that
	// reuse alarm names do.
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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "recreated alarm should reach ALARM again")

	recv2 := awaitQueueMessages(ctx, t, sqsC, q.QueueUrl, 1)
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

// TestCloudWatch_AlarmSNSActionToSQS_ResilientToOneBadAlarm expects other
// alarms to keep dispatching after one alarm's action fails.
func TestCloudWatch_AlarmSNSActionToSQS_ResilientToOneBadAlarm(t *testing.T) {
	t.Parallel()
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

	awaitSubscription(ctx, t, snsC, topicARN, queueARN)

	// The bad alarm's action names an ARN that resolves to no SNS topic; CloudWatch
	// drops such an action and keeps evaluating other alarms.
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

	// Breach only the good alarm; the bad one has no datapoints.
	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("GoodMetric"), Value: aws.Float64(100), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	awaitAlarmState(ctx, t, cw, goodAlarmName, cwtypes.StateValueAlarm, "good alarm should reach ALARM")

	recv := awaitQueueMessages(ctx, t, sqsC, q.QueueUrl, 1)
	require.Len(t, recv.Messages, 1, "good alarm must still deliver even when a sibling alarm has an invalid action target")
}

// TestCloudWatch_AlarmSNSActionToSQS_AfterDanglingAlarms leaves alarms whose
// action topics are deleted, as incomplete Terraform destroys do, and expects
// a new alarm's AlarmActions to dispatch.
func TestCloudWatch_AlarmSNSActionToSQS_AfterDanglingAlarms(t *testing.T) {
	t.Parallel()
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

	// The probe below reaches ALARM only if the evaluator survived the dangling
	// alarms.

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

	awaitSubscription(ctx, t, snsC, topicARN, queueARN)

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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should reach ALARM after dangling alarms")

	recv := awaitQueueMessages(ctx, t, sqsC, q.QueueUrl, 1)
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

// TestCloudWatch_AlarmSNSActionToSQS_AfterDeleteAndRecreate expects an alarm
// deleted and recreated under its name to dispatch again.
func TestCloudWatch_AlarmSNSActionToSQS_AfterDeleteAndRecreate(t *testing.T) {
	t.Parallel()
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

	awaitSubscription(ctx, t, snsC, topicARN, queueARN)

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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "first alarm should reach ALARM")

	recv := awaitQueueMessages(ctx, t, sqsC, q.QueueUrl, 1)
	require.Len(t, recv.Messages, 1, "first incarnation must deliver")

	_, err = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{alarmName}})
	require.NoError(t, err)

	_, err = sqsC.PurgeQueue(ctx, &sqs.PurgeQueueInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)

	// A higher threshold and a fresh metric keep the first incarnation's datapoint
	// out of the second alarm's 60-second bucket.
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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "recreated alarm should reach ALARM")

	recv = awaitQueueMessages(ctx, t, sqsC, q.QueueUrl, 1)
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

// TestCloudWatch_AlarmSNSActionToSQS_NoSubscription expects an alarm whose
// topic has no subscribers to deliver nothing, and the fan-out to log the
// empty subscription set.
func TestCloudWatch_AlarmSNSActionToSQS_NoSubscription(t *testing.T) {
	t.Parallel()
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

	// No subscription, so nothing to wait for.

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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should reach ALARM")

	requireQueueStaysEmpty(ctx, t, sqsC, q.QueueUrl, "alarm with no topic subscriptions must not deliver")
}

// TestCloudWatch_AlarmSNSActionToSQS_PolicyDenied expects nothing delivered to
// a queue whose policy denies the topic's sqs:SendMessage, with the denial
// logged.
func TestCloudWatch_AlarmSNSActionToSQS_PolicyDenied(t *testing.T) {
	t.Parallel()
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

	awaitSubscription(ctx, t, snsC, topicARN, queueARN)

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

	awaitAlarmState(ctx, t, cw, alarmName, cwtypes.StateValueAlarm, "alarm should reach ALARM")

	requireQueueStaysEmpty(ctx, t, sqsC, q.QueueUrl, "alarm with denying queue policy must not deliver")
}
