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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_EMFExtraction proves CloudWatch auto-extracts metrics from an
// EMF-formatted log event: PutLogEvents writes a message carrying an
// _aws.CloudWatchMetrics block, and the metric becomes queryable through the
// ordinary metric APIs with no PutMetricData call (the standard
// EMF-over-stdout → awslogs → CloudWatch Logs path).
func TestCloudWatch_EMFExtraction(t *testing.T) {
	logs := cwLogsClient()
	cw := cloudwatchClient()
	ns := "edd/emf-sdk"
	group := "/edd/emf-sdk"
	stream := "s1"

	_, err := logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	require.NoError(t, err)

	ts := time.Now().UTC().Truncate(time.Minute)
	tsMillis := ts.UnixMilli()
	emf := fmt.Sprintf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":%q,"Dimensions":[["svc"]],"Metrics":[{"Name":"probe","Unit":"Count"}]}]},"svc":"x","probe":42}`, tsMillis, ns)

	_, err = logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents:     []cwlogtypes.InputLogEvent{{Timestamp: aws.Int64(tsMillis), Message: aws.String(emf)}},
	})
	require.NoError(t, err)

	// The EMF-extracted metric is queryable via GetMetricData (no PutMetricData).
	out, err := cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(ts.Add(-time.Minute)),
		EndTime:   aws.Time(ts.Add(time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("q1"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String(ns),
					MetricName: aws.String("probe"),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("svc"), Value: aws.String("x")}},
				},
				Period: aws.Int32(60),
				Stat:   aws.String("Sum"),
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 1)
	assert.Equal(t, []float64{42}, out.MetricDataResults[0].Values,
		"EMF metric value must be extracted into the metric store")
}
