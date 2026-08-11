package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_MetricFilterPublishesMetric proves PutLogEvents evaluates
// metric filters against incoming events and publishes their
// MetricTransformations to the CloudWatch metric store: an event matching the
// filter pattern produces a queryable datapoint, while a non-matching event
// does not.
func TestCloudWatch_MetricFilterPublishesMetric(t *testing.T) {
	logs := cwLogsClient()
	cw := cloudwatchClient()

	ns := "FilterApp"
	metric := "ErrorCount"
	group := "/test/metric-filter-publish"
	stream := "errors"

	_, err := logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	require.NoError(t, err)

	_, err = logs.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName:  aws.String(group),
		FilterName:    aws.String("error-filter"),
		FilterPattern: aws.String(`"ERROR"`),
		MetricTransformations: []cwlogtypes.MetricTransformation{{
			MetricName:      aws.String(metric),
			MetricNamespace: aws.String(ns),
			MetricValue:     aws.String("1"),
			DefaultValue:    aws.Float64(0),
		}},
	})
	require.NoError(t, err)

	ts := time.Now().UTC().Truncate(time.Minute)
	tsMillis := ts.UnixMilli()

	_, err = logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents: []cwlogtypes.InputLogEvent{
			{Timestamp: aws.Int64(tsMillis), Message: aws.String("[ERROR] boom")},
			{Timestamp: aws.Int64(tsMillis), Message: aws.String("all good")},
			{Timestamp: aws.Int64(tsMillis), Message: aws.String("ERROR again")},
		},
	})
	require.NoError(t, err)

	out, err := cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(ts.Add(-time.Minute)),
		EndTime:   aws.Time(ts.Add(2 * time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("q1"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String(ns),
					MetricName: aws.String(metric),
				},
				Period: aws.Int32(60),
				Stat:   aws.String("Sum"),
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 1)
	require.NotEmpty(t, out.MetricDataResults[0].Values, "metric filter must publish a datapoint for matching events")
	var sum float64
	for _, v := range out.MetricDataResults[0].Values {
		sum += v
	}
	assert.Equal(t, 2.0, sum, "two ERROR events must aggregate to a Sum of 2")
}

// TestCloudWatch_MetricFilterJSONValue proves a MetricTransformation can pull
// the published value out of the matched event via a "$." JSON selector.
func TestCloudWatch_MetricFilterJSONValue(t *testing.T) {
	logs := cwLogsClient()
	cw := cloudwatchClient()

	ns := "FilterJSON"
	metric := "LatencyMs"
	group := "/test/metric-filter-json"
	stream := "lat"

	_, err := logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	require.NoError(t, err)

	_, err = logs.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName:  aws.String(group),
		FilterName:    aws.String("slow"),
		FilterPattern: aws.String(`{ $.level = "slow" }`),
		MetricTransformations: []cwlogtypes.MetricTransformation{{
			MetricName:      aws.String(metric),
			MetricNamespace: aws.String(ns),
			MetricValue:     aws.String("$.latency"),
		}},
	})
	require.NoError(t, err)

	ts := time.Now().UTC().Truncate(time.Minute)
	tsMillis := ts.UnixMilli()
	_, err = logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents: []cwlogtypes.InputLogEvent{{
			Timestamp: aws.Int64(tsMillis),
			Message:   aws.String(`{"level":"slow","latency":412}`),
		}},
	})
	require.NoError(t, err)

	out, err := cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(ts.Add(-time.Minute)),
		EndTime:   aws.Time(ts.Add(2 * time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("q1"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String(ns),
					MetricName: aws.String(metric),
				},
				Period: aws.Int32(60),
				Stat:   aws.String("Maximum"),
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 1)
	require.NotEmpty(t, out.MetricDataResults[0].Values)

	var max float64
	for _, v := range out.MetricDataResults[0].Values {
		if v > max {
			max = v
		}
	}
	assert.Equal(t, 412.0, max, "$.latency JSON selector must source the published metric value")
}
