package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cloudwatchClient() *cloudwatch.Client {
	return cloudwatch.NewFromConfig(sdkConfig(), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestCloudWatch_PutGetMetricDataRoundTrip verifies real metrics: data pushed
// via PutMetricData is returned by GetMetricData, bucketed by Period and
// reduced by the requested statistic (the real CloudWatch behaviour).
func TestCloudWatch_PutGetMetricDataRoundTrip(t *testing.T) {
	client := cloudwatchClient()
	ns := "Custom/AuditMetrics"
	metric := "Requests"
	base := time.Now().UTC().Truncate(time.Minute)

	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String(metric), Value: aws.Float64(10), Timestamp: aws.Time(base.Add(1 * time.Second))},
			{MetricName: aws.String(metric), Value: aws.Float64(20), Timestamp: aws.Time(base.Add(2 * time.Second))},
			{MetricName: aws.String(metric), Value: aws.Float64(30), Timestamp: aws.Time(base.Add(3 * time.Second))},
		},
	})
	require.NoError(t, err)

	get := func(stat string) []float64 {
		out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(base.Add(-time.Minute)),
			EndTime:   aws.Time(base.Add(time.Minute)),
			MetricDataQueries: []cwtypes.MetricDataQuery{{
				Id: aws.String("q1"),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String(metric)},
					Period: aws.Int32(60),
					Stat:   aws.String(stat),
				},
			}},
		})
		require.NoError(t, err)
		require.Len(t, out.MetricDataResults, 1)
		return out.MetricDataResults[0].Values
	}

	assert.Equal(t, []float64{20}, get("Average"))
	assert.Equal(t, []float64{60}, get("Sum"))
	assert.Equal(t, []float64{10}, get("Minimum"))
	assert.Equal(t, []float64{30}, get("Maximum"))
	assert.Equal(t, []float64{3}, get("SampleCount"))
}

// TestCloudWatch_ECSMetricsServeStoredData proves ECS/ContainerInsights metrics
// are no longer fabricated: with nothing pushed they report no datapoints, and
// after PutMetricData they return the real pushed value.
func TestCloudWatch_ECSMetricsServeStoredData(t *testing.T) {
	client := cloudwatchClient()
	ns := "ECS/ContainerInsights"
	base := time.Now().UTC().Truncate(time.Minute)
	dims := []cwtypes.Dimension{
		{Name: aws.String("ClusterName"), Value: aws.String("audit-cluster")},
		{Name: aws.String("TaskId"), Value: aws.String("task-abc")},
	}
	query := func() *cloudwatch.GetMetricDataInput {
		return &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(base.Add(-time.Minute)),
			EndTime:   aws.Time(base.Add(time.Minute)),
			MetricDataQueries: []cwtypes.MetricDataQuery{{
				Id: aws.String("q"),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String("CpuUtilized"), Dimensions: dims},
					Period: aws.Int32(60),
					Stat:   aws.String("Average"),
				},
			}},
		}
	}

	empty, err := client.GetMetricData(ctx, query())
	require.NoError(t, err)
	require.Len(t, empty.MetricDataResults, 1)
	assert.Empty(t, empty.MetricDataResults[0].Values, "ECS metrics must not be fabricated when none were pushed")

	_, err = client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("CpuUtilized"), Dimensions: dims, Value: aws.Float64(42.5), Timestamp: aws.Time(base.Add(1 * time.Second))},
		},
	})
	require.NoError(t, err)

	got, err := client.GetMetricData(ctx, query())
	require.NoError(t, err)
	assert.Equal(t, []float64{42.5}, got.MetricDataResults[0].Values)
}
