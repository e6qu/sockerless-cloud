package aws_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/require"
)

// putLogAlarmInput builds a PutLogAlarm request over one log group, with the
// M-out-of-N and threshold knobs the caller wants to exercise.
func putLogAlarmInput(name, logGroupArn, aggregation string, threshold float64, op cwtypes.ComparisonOperator) *cloudwatch.PutLogAlarmInput {
	return &cloudwatch.PutLogAlarmInput{
		AlarmName:        aws.String(name),
		AlarmDescription: aws.String("errors in the last window"),
		ScheduledQueryConfiguration: &cwtypes.ScheduledQueryConfiguration{
			QueryString:           aws.String("fields @message | filter level = \"ERROR\""),
			LogGroupIdentifiers:   []string{logGroupArn},
			ScheduledQueryRoleARN: aws.String("arn:aws:iam::000000000000:role/CWScheduledQueryRole"),
			AggregationExpression: aws.String(aggregation),
			ScheduleConfiguration: &cwtypes.ScheduleConfiguration{
				ScheduleExpression: aws.String("rate(5 minutes)"),
				StartTimeOffset:    aws.Int64(600),
				EndTimeOffset:      aws.Int64(0),
			},
		},
		QueryResultsToEvaluate: aws.Int32(1),
		QueryResultsToAlarm:    aws.Int32(1),
		Threshold:              aws.Float64(threshold),
		ComparisonOperator:     op,
		TreatMissingData:       aws.String("missing"),
	}
}

// seedLogAlarmLogGroup creates a log group + stream and ingests messages, then
// returns the group name. The log alarm's scheduled query runs over these.
func seedLogAlarmLogGroup(t *testing.T, logs *cloudwatchlogs.Client, group string, messages []string) string {
	t.Helper()
	_, err := logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	stream := "s1"
	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
	})
	require.NoError(t, err)
	if len(messages) == 0 {
		return group
	}
	now := time.Now().UTC().UnixMilli()
	events := make([]cwlogtypes.InputLogEvent, 0, len(messages))
	for i, m := range messages {
		events = append(events, cwlogtypes.InputLogEvent{
			Timestamp: aws.Int64(now - int64(i*1000)),
			Message:   aws.String(m),
		})
	}
	_, err = logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents:     events,
	})
	require.NoError(t, err)
	return group
}

// TestCloudWatch_PutLogAlarm covers the log-alarm lifecycle: PutLogAlarm creates
// the alarm and the service-managed scheduled query behind it, DescribeAlarms
// reads it back under the LogAlarm alarm type with the state its scheduled
// query's results imply, and DeleteAlarms removes both.
func TestCloudWatch_PutLogAlarm(t *testing.T) {
	client := cloudwatchClient()
	logs := cwLogsClient()
	name := "sdk-log-alarm"
	group := "/sdk/log-alarm/" + fmt.Sprint(time.Now().UnixNano())
	seedLogAlarmLogGroup(t, logs, group, []string{
		`{"level":"ERROR","msg":"boom"}`,
		`{"level":"ERROR","msg":"boom again"}`,
		`{"level":"INFO","msg":"fine"}`,
	})
	defer func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	}()

	// Two ERROR lines are in the window; a threshold of 1 is crossed.
	_, err := client.PutLogAlarm(ctx, putLogAlarmInput(name, group, "count(*) as hits", 1,
		cwtypes.ComparisonOperatorGreaterThanThreshold))
	require.NoError(t, err)
	defer func() { _, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) }()

	out, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{name},
		AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeLogAlarm},
	})
	require.NoError(t, err)
	require.Len(t, out.LogAlarms, 1)
	la := out.LogAlarms[0]
	require.Equal(t, name, aws.ToString(la.AlarmName))
	require.Contains(t, aws.ToString(la.AlarmArn), ":alarm:"+name)
	require.Equal(t, int32(1), aws.ToInt32(la.QueryResultsToEvaluate))
	require.Equal(t, cwtypes.ComparisonOperatorGreaterThanThreshold, la.ComparisonOperator)
	require.NotNil(t, la.ScheduledQueryConfiguration)
	require.Equal(t, "count(*) as hits", aws.ToString(la.ScheduledQueryConfiguration.AggregationExpression))
	require.Equal(t, cwtypes.StateValueAlarm, la.StateValue)

	// The QueryARN the alarm reports resolves to a real CloudWatch Logs
	// scheduled query — PutLogAlarm provisions the service-managed resource.
	queryArn := aws.ToString(la.ScheduledQueryConfiguration.QueryARN)
	require.NotEmpty(t, queryArn)
	sq, err := logs.GetScheduledQuery(ctx, &cloudwatchlogs.GetScheduledQueryInput{
		Identifier: aws.String(queryArn),
	})
	require.NoError(t, err)
	require.Equal(t, "rate(5 minutes)", aws.ToString(sq.ScheduleExpression))
	require.Equal(t, []string{group}, sq.LogGroupIdentifiers)

	// A threshold no window crosses puts the alarm back in OK.
	_, err = client.PutLogAlarm(ctx, putLogAlarmInput(name, group, "count(*) as hits", 100,
		cwtypes.ComparisonOperatorGreaterThanThreshold))
	require.NoError(t, err)
	out, err = client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{name},
		AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeLogAlarm},
	})
	require.NoError(t, err)
	require.Len(t, out.LogAlarms, 1)
	require.Equal(t, cwtypes.StateValueOk, out.LogAlarms[0].StateValue)

	// DeleteAlarms removes the log alarm and the query it provisioned.
	_, err = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	out, err = client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{name},
		AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeLogAlarm},
	})
	require.NoError(t, err)
	require.Empty(t, out.LogAlarms)
	_, err = logs.GetScheduledQuery(ctx, &cloudwatchlogs.GetScheduledQueryInput{Identifier: aws.String(queryArn)})
	require.Error(t, err, "the service-managed scheduled query must go with the alarm")
}

// TestCloudWatch_PutLogAlarmRejectsBadRequest pins the modeled validation:
// QueryResultsToAlarm may not exceed QueryResultsToEvaluate, and a name already
// held by a metric alarm conflicts.
func TestCloudWatch_PutLogAlarmRejectsBadRequest(t *testing.T) {
	client := cloudwatchClient()
	logs := cwLogsClient()
	group := "/sdk/log-alarm-invalid/" + fmt.Sprint(time.Now().UnixNano())
	seedLogAlarmLogGroup(t, logs, group, nil)
	defer func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	}()

	in := putLogAlarmInput("sdk-log-alarm-invalid", group, "count(*) as hits", 1,
		cwtypes.ComparisonOperatorGreaterThanThreshold)
	in.QueryResultsToEvaluate = aws.Int32(2)
	in.QueryResultsToAlarm = aws.Int32(5)
	_, err := client.PutLogAlarm(ctx, in)
	require.Error(t, err)

	metricName := "sdk-log-alarm-conflict"
	_, err = client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(metricName),
		Namespace:          aws.String("Custom/LogAlarmConflict"),
		MetricName:         aws.String("M"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{metricName}})
	}()

	_, err = client.PutLogAlarm(ctx, putLogAlarmInput(metricName, group, "count(*) as hits", 1,
		cwtypes.ComparisonOperatorGreaterThanThreshold))
	require.Error(t, err, "an alarm name already held by a metric alarm must conflict")
}
