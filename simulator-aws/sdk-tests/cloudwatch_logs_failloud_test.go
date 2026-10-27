package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatchLogs_FilterAndQueryFailLoud expects a malformed FilterLogEvents
// filterPattern to raise InvalidParameterException and a malformed StartQuery
// query string MalformedQueryException, never an empty result a consumer reads
// as "no matching logs".
func TestCloudWatchLogs_FilterAndQueryFailLoud(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := "/test/failloud"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	errCode := func(err error) string {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			return ae.ErrorCode()
		}
		return ""
	}

	// The comparison lacks its value.
	_, err = cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:  aws.String(group),
		FilterPattern: aws.String(`{ $.level = }`),
	})
	require.Error(t, err, "a malformed filterPattern must fail, not match nothing")
	assert.Equal(t, "InvalidParameterException", errCode(err))

	_, err = cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:  aws.String(group),
		FilterPattern: aws.String(`{ $.level = "NOPE" }`),
	})
	assert.NoError(t, err)

	// The filter stage has an unbalanced parenthesis.
	_, err = cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupName: aws.String(group),
		QueryString:  aws.String(`fields @message | filter (level = "ERROR"`),
		StartTime:    aws.Int64(0),
		EndTime:      aws.Int64(1),
	})
	require.Error(t, err, "a malformed query string must fail, not run as empty")
	assert.Equal(t, "MalformedQueryException", errCode(err))
}

// TestCloudWatchLogs_PutMetricFilterRejectsInvalidPattern expects
// PutMetricFilter to validate FilterPattern up front with
// InvalidParameterException.
func TestCloudWatchLogs_PutMetricFilterRejectsInvalidPattern(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := "/test/put-metric-filter-invalid"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	var ae smithy.APIError
	_, err = cw.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName:  aws.String(group),
		FilterName:    aws.String("bad-filter"),
		FilterPattern: aws.String("{"),
		MetricTransformations: []cwlogtypes.MetricTransformation{{
			MetricName:      aws.String("Bad"),
			MetricNamespace: aws.String("Probe"),
			MetricValue:     aws.String("1"),
		}},
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &ae), "expected smithy API error, got %T", err)
	assert.Equal(t, "InvalidParameterException", ae.ErrorCode())

	_, err = cw.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName:  aws.String(group),
		FilterName:    aws.String("good-filter"),
		FilterPattern: aws.String("ERROR"),
		MetricTransformations: []cwlogtypes.MetricTransformation{{
			MetricName:      aws.String("ErrorCount"),
			MetricNamespace: aws.String("MyApp"),
			MetricValue:     aws.String("1"),
		}},
	})
	assert.NoError(t, err)
}

func TestCloudWatchLogs_PutSubscriptionFilterRejectsInvalidPattern(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := "/test/put-subscription-filter-invalid"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	var ae smithy.APIError
	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName:   aws.String(group),
		FilterName:     aws.String("bad-sub"),
		FilterPattern:  aws.String("{"),
		DestinationArn: aws.String("arn:aws:lambda:us-east-1:123456789012:function:log-sink"),
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &ae), "expected smithy API error, got %T", err)
	assert.Equal(t, "InvalidParameterException", ae.ErrorCode())
}
