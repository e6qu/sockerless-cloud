package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"strings"
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

// alarmNotificationQueue subscribes a new queue to a new topic and answers the
// topic's ARN and the queue's URL.
func alarmNotificationQueue(t *testing.T, prefix string) (string, *string) {
	t.Helper()
	snsC, sqsC := snsClient(), sqsClient()
	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(prefix + "-t")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })
	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(prefix + "-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })
	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, aws.ToString(tpc.TopicArn))
	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)
	return aws.ToString(tpc.TopicArn), q.QueueUrl
}

// receiveAlarmNotification long-polls the queue for the next notification
// about alarmName and answers its decoded Message.
func receiveAlarmNotification(t *testing.T, queueURL *string, alarmName string) map[string]any {
	t.Helper()
	sqsC := sqsClient()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: queueURL, MaxNumberOfMessages: 1, WaitTimeSeconds: 5,
		})
		require.NoError(t, err)
		for _, m := range recv.Messages {
			_, err := sqsC.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: queueURL, ReceiptHandle: m.ReceiptHandle})
			require.NoError(t, err)
			var env struct {
				Type    string `json:"Type"`
				Message string `json:"Message"`
			}
			require.NoError(t, json.Unmarshal([]byte(aws.ToString(m.Body)), &env))
			if env.Type != "Notification" {
				continue
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(env.Message), &body))
			if body["AlarmName"] == alarmName {
				return body
			}
		}
	}
	t.Fatalf("no notification for %s reached the queue", alarmName)
	return nil
}

// TestCloudWatch_SetAlarmStateRunsActions expects SetAlarmState to invoke the
// new state's actions and the metric alarm to return to its actual state at
// the next evaluation, invoking that state's actions in turn.
func TestCloudWatch_SetAlarmStateRunsActions(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	id := nextCloudWatchProbeID()
	name := fmt.Sprintf("sdk-set-state-%d", id)
	topicARN, queueURL := alarmNotificationQueue(t, fmt.Sprintf("set-state-%d", id))

	_, err := cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		Namespace:          aws.String(fmt.Sprintf("Custom/SetState-%d", id)),
		MetricName:         aws.String("M"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		TreatMissingData:   aws.String("notBreaching"),
		AlarmActions:       []string{topicARN},
		OKActions:          []string{topicARN},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) })
	// The first evaluation moves the new alarm to OK and runs its OK actions.
	first := receiveAlarmNotification(t, queueURL, name)
	require.Equal(t, "OK", first["NewStateValue"])

	_, err = cw.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName:   aws.String(name),
		StateValue:  cwtypes.StateValueAlarm,
		StateReason: aws.String("testing the alarm actions"),
	})
	require.NoError(t, err)

	set := receiveAlarmNotification(t, queueURL, name)
	assert.Equal(t, "ALARM", set["NewStateValue"])
	assert.Equal(t, "OK", set["OldStateValue"])
	assert.Equal(t, "testing the alarm actions", set["NewStateReason"])

	back := receiveAlarmNotification(t, queueURL, name)
	assert.Equal(t, "OK", back["NewStateValue"], "the alarm returns to its actual state")
	assert.Equal(t, "ALARM", back["OldStateValue"])

	hist, err := cw.DescribeAlarmHistory(ctx, &cloudwatch.DescribeAlarmHistoryInput{
		AlarmName: aws.String(name), HistoryItemType: cwtypes.HistoryItemTypeStateUpdate,
	})
	require.NoError(t, err)
	var summaries []string
	for _, item := range hist.AlarmHistoryItems {
		summaries = append(summaries, aws.ToString(item.HistorySummary))
	}
	assert.Contains(t, summaries, "Alarm updated from OK to ALARM")
	assert.Contains(t, summaries, "Alarm updated from ALARM to OK")
}

// TestCloudWatch_CompositeAlarmEvaluatesItsRule expects a composite alarm to
// follow its AlarmRule over its children's states, run each new state's
// actions, name the triggering child, and hold a SetAlarmState until a child
// changes state.
func TestCloudWatch_CompositeAlarmEvaluatesItsRule(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	id := nextCloudWatchProbeID()
	ns := fmt.Sprintf("Custom/Composite-%d", id)
	cpu := fmt.Sprintf("sdk-composite-cpu-%d", id)
	deploy := fmt.Sprintf("sdk-composite-deploy-%d", id)
	composite := fmt.Sprintf("sdk-composite-%d", id)
	topicARN, queueURL := alarmNotificationQueue(t, fmt.Sprintf("composite-%d", id))

	for _, child := range []string{cpu, deploy} {
		_, err := cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
			AlarmName:          aws.String(child),
			Namespace:          aws.String(ns),
			MetricName:         aws.String(child),
			ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
			EvaluationPeriods:  aws.Int32(1),
			Period:             aws.Int32(60),
			Threshold:          aws.Float64(0),
			Statistic:          cwtypes.StatisticSum,
			TreatMissingData:   aws.String("notBreaching"),
		})
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{composite}})
		_, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{cpu, deploy}})
	})
	rule := fmt.Sprintf("ALARM(%s) AND NOT ALARM(%q)", cpu, deploy)
	_, err := cw.PutCompositeAlarm(ctx, &cloudwatch.PutCompositeAlarmInput{
		AlarmName:    aws.String(composite),
		AlarmRule:    aws.String(rule),
		AlarmActions: []string{topicARN},
		OKActions:    []string{topicARN},
	})
	require.NoError(t, err)

	created := receiveAlarmNotification(t, queueURL, composite)
	assert.Equal(t, "OK", created["NewStateValue"])
	assert.Equal(t, "INSUFFICIENT_DATA", created["OldStateValue"])
	assert.True(t, strings.HasSuffix(created["NewStateReason"].(string), "was created and its alarm rule evaluates to OK"),
		"reason: %v", created["NewStateReason"])
	assert.Equal(t, rule, created["AlarmRule"])

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace:  aws.String(ns),
		MetricData: []cwtypes.MetricDatum{{MetricName: aws.String(cpu), Value: aws.Float64(1), Timestamp: aws.Time(time.Now().UTC())}},
	})
	require.NoError(t, err)

	fired := receiveAlarmNotification(t, queueURL, composite)
	assert.Equal(t, "ALARM", fired["NewStateValue"])
	assert.Equal(t, "OK", fired["OldStateValue"])
	cpuARN := fmt.Sprintf("arn:aws:cloudwatch:us-east-1:123456789012:alarm:%s", cpu)
	assert.Equal(t, []any{cpuARN}, fired["TriggeringChildren"])
	assert.True(t, strings.HasPrefix(fired["NewStateReason"].(string), cpuARN+" transitioned to ALARM at "),
		"reason: %v", fired["NewStateReason"])

	// A SetAlarmState holds while the children keep their states.
	_, err = cw.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName: aws.String(composite), StateValue: cwtypes.StateValueOk, StateReason: aws.String("silenced for the test"),
	})
	require.NoError(t, err)
	silenced := receiveAlarmNotification(t, queueURL, composite)
	assert.Equal(t, "OK", silenced["NewStateValue"])
	out, err := cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{composite}, AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeCompositeAlarm},
	})
	require.NoError(t, err)
	require.Len(t, out.CompositeAlarms, 1)
	assert.Equal(t, cwtypes.StateValueOk, out.CompositeAlarms[0].StateValue)
	assert.Equal(t, "silenced for the test", aws.ToString(out.CompositeAlarms[0].StateReason))

	// A child that changes state ends the hold. With no data and
	// TreatMissingData=missing the deploy alarm moves to INSUFFICIENT_DATA, which
	// leaves the rule true, so the composite returns to ALARM.
	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(deploy),
		Namespace:          aws.String(ns),
		MetricName:         aws.String(deploy),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		TreatMissingData:   aws.String("missing"),
	})
	require.NoError(t, err)
	refired := receiveAlarmNotification(t, queueURL, composite)
	assert.Equal(t, "ALARM", refired["NewStateValue"])
	assert.Equal(t, "OK", refired["OldStateValue"])
	deployARN := fmt.Sprintf("arn:aws:cloudwatch:us-east-1:123456789012:alarm:%s", deploy)
	assert.Equal(t, []any{deployARN}, refired["TriggeringChildren"])
}
