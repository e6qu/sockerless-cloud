package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventRuleQueue routes the default bus's events that match pattern to a new
// queue and answers the queue's URL.
func eventRuleQueue(t *testing.T, name string, pattern map[string]any) *string {
	t.Helper()
	eb, sqsC := eventbridgeClient(), sqsClient()
	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })
	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	body, err := json.Marshal(pattern)
	require.NoError(t, err)
	rule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(name), EventPattern: aws.String(string(body))})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String(name), Ids: []string{"q"}})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(name)})
	})
	setEBQueuePolicy(t, sqsC, q.QueueUrl, queueARN, aws.ToString(rule.RuleArn))
	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String(name), Targets: []ebtypes.Target{{Id: aws.String("q"), Arn: aws.String(queueARN)}},
	})
	require.NoError(t, err)
	return q.QueueUrl
}

// TestCloudWatch_AlarmInvokesLambdaAction expects an alarm action naming an AWS
// Lambda function that grants lambda.alarms.cloudwatch.amazonaws.com
// lambda:InvokeFunction to invoke it asynchronously with the alarm payload
// the User Guide lays out.
func TestCloudWatch_AlarmInvokesLambdaAction(t *testing.T) {
	t.Parallel()
	cw, lambdaAPI, sqsC := cloudwatchClient(), lambdaClient(), sqsClient()
	id := nextCloudWatchProbeID()
	name := fmt.Sprintf("sdk-alarm-lambda-%d", id)
	alarmARN := fmt.Sprintf("arn:aws:cloudwatch:us-east-1:123456789012:alarm:%s", name)
	functionName := fmt.Sprintf("alarm-action-%d", id)

	created, err := lambdaAPI.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String(lambdaRole(t, "sqs:SendMessage")),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaNodeSourceZip(t, `exports.handler = async (event) => ({ seen: event.alarmData.alarmName });`)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaAPI.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})
	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(fmt.Sprintf("alarm-lambda-dest-%d", id))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })
	_, err = lambdaAPI.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName:         aws.String(functionName),
		MaximumRetryAttempts: aws.Int32(0),
		DestinationConfig: &lambdatypes.DestinationConfig{
			OnSuccess: &lambdatypes.OnSuccess{Destination: aws.String(queueARNOf(t, sqsC, queue.QueueUrl))},
		},
	})
	require.NoError(t, err)
	_, err = lambdaAPI.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName:  aws.String(functionName),
		StatementId:   aws.String("AlarmAction"),
		Action:        aws.String("lambda:InvokeFunction"),
		Principal:     aws.String("lambda.alarms.cloudwatch.amazonaws.com"),
		SourceAccount: aws.String("123456789012"),
		SourceArn:     aws.String(alarmARN),
	})
	require.NoError(t, err)

	_, err = cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		Namespace:          aws.String(fmt.Sprintf("Custom/AlarmLambda-%d", id)),
		MetricName:         aws.String("Errors"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		TreatMissingData:   aws.String("notBreaching"),
		AlarmActions:       []string{aws.ToString(created.FunctionArn)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) })
	_, err = cw.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName: aws.String(name), StateValue: cwtypes.StateValueAlarm, StateReason: aws.String("lambda action test"),
	})
	require.NoError(t, err)

	body := aws.ToString(receiveSQSMessages(t, sqsC, queue.QueueUrl, 1, 60*time.Second)[0].Body)
	var record struct {
		RequestPayload struct {
			Source    string `json:"source"`
			AlarmArn  string `json:"alarmArn"`
			AccountID string `json:"accountId"`
			Region    string `json:"region"`
			AlarmData struct {
				AlarmName     string `json:"alarmName"`
				State         struct{ Value, Reason string }
				PreviousState struct{ Value string } `json:"previousState"`
				Configuration struct {
					Metrics []map[string]any `json:"metrics"`
				}
			} `json:"alarmData"`
		} `json:"requestPayload"`
		ResponsePayload map[string]any `json:"responsePayload"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &record), body)
	p := record.RequestPayload
	assert.Equal(t, "aws.cloudwatch", p.Source)
	assert.Equal(t, alarmARN, p.AlarmArn)
	assert.Equal(t, "123456789012", p.AccountID)
	assert.Equal(t, "us-east-1", p.Region)
	assert.Equal(t, name, p.AlarmData.AlarmName)
	assert.Equal(t, "ALARM", p.AlarmData.State.Value)
	assert.Equal(t, "lambda action test", p.AlarmData.State.Reason)
	assert.Equal(t, "OK", p.AlarmData.PreviousState.Value)
	assert.Len(t, p.AlarmData.Configuration.Metrics, 1)
	assert.Equal(t, name, record.ResponsePayload["seen"])
}

// TestCloudWatch_LogAlarmRunsActions expects a log alarm's change of state to
// run its Amazon SNS action with the log alarm notification.
func TestCloudWatch_LogAlarmRunsActions(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	id := nextCloudWatchProbeID()
	name := fmt.Sprintf("sdk-log-alarm-actions-%d", id)
	topicARN, queueURL := alarmNotificationQueue(t, fmt.Sprintf("log-alarm-actions-%d", id))
	group := seedLogAlarmLogGroup(t, cwLogsClient(), fmt.Sprintf("/sdk/log-alarm-actions/%d", id), nil)

	input := putLogAlarmInput(name, group, "count(*) as hits", 0, cwtypes.ComparisonOperatorGreaterThanThreshold)
	input.AlarmActions = []string{topicARN}
	_, err := cw.PutLogAlarm(ctx, input)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) })

	_, err = cw.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName: aws.String(name), StateValue: cwtypes.StateValueAlarm, StateReason: aws.String("log alarm action test"),
	})
	require.NoError(t, err)

	note := receiveAlarmNotification(t, queueURL, name)
	assert.Equal(t, "ALARM", note["NewStateValue"])
	// The query counts no ERROR lines, 0, under the threshold.
	assert.Equal(t, "OK", note["OldStateValue"])
	assert.Equal(t, "log alarm action test", note["NewStateReason"])
	assert.Equal(t, []any{group}, note["LogGroups"])
	assert.Equal(t, "fields @message | filter level = \"ERROR\"", note["QueryString"])
	assert.Equal(t, "count(*) as hits", note["AggregationExpression"])
}

// TestCloudWatch_AlarmConfigurationChangeEvents expects creating, updating and
// deleting an alarm to send "CloudWatch Alarm Configuration Change" events,
// the update carrying the previous configuration, and an update to leave the
// alarm's state unchanged.
func TestCloudWatch_AlarmConfigurationChangeEvents(t *testing.T) {
	t.Parallel()
	cw, sqsC := cloudwatchClient(), sqsClient()
	id := nextCloudWatchProbeID()
	name := fmt.Sprintf("sdk-alarm-config-events-%d", id)
	alarmARN := fmt.Sprintf("arn:aws:cloudwatch:us-east-1:123456789012:alarm:%s", name)
	queueURL := eventRuleQueue(t, fmt.Sprintf("alarm-config-events-%d", id), map[string]any{
		"source": []string{"aws.cloudwatch"}, "detail-type": []string{"CloudWatch Alarm Configuration Change"}, "resources": []string{alarmARN},
	})
	put := func(threshold float64) {
		_, err := cw.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
			AlarmName:          aws.String(name),
			AlarmDescription:   aws.String("configuration change events"),
			Namespace:          aws.String(fmt.Sprintf("Custom/AlarmConfig-%d", id)),
			MetricName:         aws.String("Errors"),
			ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
			EvaluationPeriods:  aws.Int32(1),
			Period:             aws.Int32(60),
			Threshold:          aws.Float64(threshold),
			Statistic:          cwtypes.StatisticSum,
			TreatMissingData:   aws.String("notBreaching"),
		})
		require.NoError(t, err)
	}
	type configEvent struct {
		DetailType string   `json:"detail-type"`
		Resources  []string `json:"resources"`
		Detail     struct {
			AlarmName     string `json:"alarmName"`
			Operation     string `json:"operation"`
			State         struct{ Value string }
			Configuration map[string]any `json:"configuration"`
			Previous      map[string]any `json:"previousConfiguration"`
		} `json:"detail"`
	}
	next := func() configEvent {
		t.Helper()
		var ev configEvent
		msg := receiveSQSMessages(t, sqsC, queueURL, 1, 30*time.Second)[0]
		_, err := sqsC.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: queueURL, ReceiptHandle: msg.ReceiptHandle})
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal([]byte(aws.ToString(msg.Body)), &ev))
		return ev
	}

	put(10)
	created := next()
	assert.Equal(t, "CloudWatch Alarm Configuration Change", created.DetailType)
	assert.Equal(t, []string{alarmARN}, created.Resources)
	assert.Equal(t, "create", created.Detail.Operation)
	assert.Equal(t, "INSUFFICIENT_DATA", created.Detail.State.Value)
	assert.Equal(t, float64(10), created.Detail.Configuration["threshold"])
	assert.Equal(t, "configuration change events", created.Detail.Configuration["description"])
	assert.Nil(t, created.Detail.Previous)

	// The first evaluation moves the alarm to OK; an update keeps that state.
	awaitAlarmState(ctx, t, cw, name, cwtypes.StateValueOk, "the new alarm evaluates to OK")
	awaitRecordedTransition(ctx, t, cw, name, "OK")
	put(20)
	updated := next()
	assert.Equal(t, "update", updated.Detail.Operation)
	assert.Equal(t, "OK", updated.Detail.State.Value, "an update leaves the alarm's state unchanged")
	assert.Equal(t, float64(20), updated.Detail.Configuration["threshold"])
	assert.Equal(t, float64(10), updated.Detail.Previous["threshold"])

	_, err := cw.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	deleted := next()
	assert.Equal(t, "delete", deleted.Detail.Operation)
	assert.Equal(t, float64(20), deleted.Detail.Configuration["threshold"])
}
