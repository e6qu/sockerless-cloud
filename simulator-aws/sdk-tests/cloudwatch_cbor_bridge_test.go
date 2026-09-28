package aws_sdk_test

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Go SDK speaks only rpc-v2-cbor to Amazon CloudWatch, so every operation
// the simulator serves must answer on that protocol, timestamps and blobs in
// their CBOR form.
func TestCloudWatch_EveryOperationAnswersOverCBOR(t *testing.T) {
	client := cloudwatchClient()
	now := time.Now().UTC().Truncate(time.Second)
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Custom/CBORBridge"),
		MetricData: []cwtypes.MetricDatum{{
			MetricName: aws.String("Requests"),
			Dimensions: []cwtypes.Dimension{{Name: aws.String("Service"), Value: aws.String("bridge")}},
			Value:      aws.Float64(4),
			Timestamp:  aws.Time(now),
		}},
	})
	require.NoError(t, err)

	listed, err := client.ListMetrics(ctx, &cloudwatch.ListMetricsInput{Namespace: aws.String("Custom/CBORBridge")})
	require.NoError(t, err)
	require.Len(t, listed.Metrics, 1)
	assert.Equal(t, "Requests", aws.ToString(listed.Metrics[0].MetricName))

	stats, err := client.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("Custom/CBORBridge"),
		MetricName: aws.String("Requests"),
		Dimensions: []cwtypes.Dimension{{Name: aws.String("Service"), Value: aws.String("bridge")}},
		StartTime:  aws.Time(now.Add(-5 * time.Minute)),
		EndTime:    aws.Time(now.Add(5 * time.Minute)),
		Period:     aws.Int32(600),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
	})
	require.NoError(t, err)
	require.Len(t, stats.Datapoints, 1)
	assert.Equal(t, 4.0, aws.ToFloat64(stats.Datapoints[0].Sum))
	assert.False(t, aws.ToTime(stats.Datapoints[0].Timestamp).IsZero(), "the datapoint timestamp decodes from CBOR")

	image, err := client.GetMetricWidgetImage(ctx, &cloudwatch.GetMetricWidgetImageInput{
		MetricWidget: aws.String(`{"metrics":[["Custom/CBORBridge","Requests","Service","bridge"]],"width":320,"height":200}`),
	})
	require.NoError(t, err)
	_, err = png.Decode(bytes.NewReader(image.MetricWidgetImage))
	require.NoError(t, err, "the widget image arrives as CBOR bytes of a PNG")

}
