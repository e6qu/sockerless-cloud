package aws_sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_MetricAlarms covers PutMetricAlarm / DescribeAlarms /
// DeleteAlarms over the Go SDK (rpc-v2-cbor): an alarm's StateValue is
// evaluated from the live metric data — ALARM when a breaching datapoint
// exists, OK when missing data is treated as not-breaching — and tags round-trip.
func TestCloudWatch_MetricAlarms(t *testing.T) {
	client := cloudwatchClient()
	ns := "Custom/AlarmsSDK"

	// A breaching datapoint (value 5 > threshold 0).
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("Errors"), Value: aws.Float64(5), Timestamp: aws.Time(time.Now().UTC())},
		},
	})
	require.NoError(t, err)

	_, err = client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String("sdk-alarm"),
		AlarmDescription:   aws.String("errors breaching"),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("Errors"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		TreatMissingData:   aws.String("notBreaching"),
		Tags:               []cwtypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)

	out, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{"sdk-alarm"}})
	require.NoError(t, err)
	require.Len(t, out.MetricAlarms, 1)
	a := out.MetricAlarms[0]
	assert.Equal(t, "sdk-alarm", aws.ToString(a.AlarmName))
	assert.Contains(t, aws.ToString(a.AlarmArn), ":alarm:sdk-alarm")
	assert.Equal(t, cwtypes.StateValueAlarm, a.StateValue, "value 5 > threshold 0 → ALARM")
	assert.Equal(t, "GreaterThanThreshold", string(a.ComparisonOperator))
	assert.InDelta(t, 0, aws.ToFloat64(a.Threshold), 0.001)

	// Tags round-trip via the transparent-tagging read path.
	tags, err := client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: a.AlarmArn})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "env", aws.ToString(tags.Tags[0].Key))
	assert.Equal(t, "test", aws.ToString(tags.Tags[0].Value))

	// An alarm whose metric has no data + notBreaching evaluates to OK.
	_, err = client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String("sdk-alarm-ok"),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("NoData"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		TreatMissingData:   aws.String("notBreaching"),
	})
	require.NoError(t, err)
	okOut, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{"sdk-alarm-ok"}})
	require.NoError(t, err)
	require.Len(t, okOut.MetricAlarms, 1)
	assert.Equal(t, cwtypes.StateValueOk, okOut.MetricAlarms[0].StateValue)

	// DeleteAlarms removes them.
	_, err = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{"sdk-alarm", "sdk-alarm-ok"}})
	require.NoError(t, err)
	gone, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{"sdk-alarm"}})
	require.NoError(t, err)
	assert.Empty(t, gone.MetricAlarms)
}

// TestCloudWatch_MetricAlarmValidation asserts PutMetricAlarm rejects the
// required numeric parameters CloudWatch validates server-side rather than
// silently defaulting them to 0: EvaluationPeriods must be >= 1, and a
// single-metric alarm (MetricName set) requires Period. EvaluationPeriods/Period
// are not model-required, so the SDK passes a 0 straight through to the service.
func TestCloudWatch_MetricAlarmValidation(t *testing.T) {
	client := cloudwatchClient()
	ns := "Custom/AlarmValidation"

	apiCode := func(err error) string {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return apiErr.ErrorCode()
		}
		return ""
	}

	_, err := client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String("bad-eval"),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("M"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(0),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
	})
	require.Error(t, err)
	assert.Equal(t, "MissingParameter", apiCode(err))
	assert.Contains(t, err.Error(), "EvaluationPeriods")

	_, err = client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String("bad-period"),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("M"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(0),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
	})
	require.Error(t, err)
	assert.Equal(t, "MissingParameter", apiCode(err))
	assert.Contains(t, err.Error(), "Period")
}
