package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_GetMetricDataPagingAndLabelsSDK pages GetMetricData by
// MaxDatapoints and NextToken, and expands dynamic labels with times in
// LabelOptions.Timezone.
func TestCloudWatch_GetMetricDataPagingAndLabelsSDK(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/Paging")
	base := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	var data []cwtypes.MetricDatum
	for i, v := range []float64{4, 9, 2, 7, 5} {
		data = append(data, cwtypes.MetricDatum{MetricName: aws.String("Depth"), Value: aws.Float64(v), Timestamp: aws.Time(base.Add(time.Duration(i) * time.Minute))})
	}
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: data})
	require.NoError(t, err)

	query := cwtypes.MetricDataQuery{
		Id:    aws.String("depth"),
		Label: aws.String("${PROP('MetricName')} max ${MAX} last ${LAST} at ${MAX_TIME}"),
		MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String("Depth")},
			Period: aws.Int32(60), Stat: aws.String("Maximum"),
		},
	}
	in := &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(base), EndTime: aws.Time(base.Add(5 * time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{query},
		MaxDatapoints:     aws.Int32(2),
		ScanBy:            cwtypes.ScanByTimestampAscending,
		LabelOptions:      &cwtypes.LabelOptions{Timezone: aws.String("+0130")},
	}
	var values []float64
	pages := 0
	for {
		out, err := client.GetMetricData(ctx, in)
		require.NoError(t, err)
		pages++
		require.Len(t, out.MetricDataResults, 1)
		res := out.MetricDataResults[0]
		assert.LessOrEqual(t, len(res.Values), 2)
		values = append(values, res.Values...)
		wantLabel := "Depth max 9 last 5 at " + base.Add(time.Minute).In(time.FixedZone("", 90*60)).Format("2006-01-02 15:04:05 -0700")
		assert.Equal(t, wantLabel, aws.ToString(res.Label))
		if out.NextToken == nil {
			assert.Equal(t, cwtypes.StatusCodeComplete, res.StatusCode)
			break
		}
		assert.Equal(t, cwtypes.StatusCodePartialData, res.StatusCode)
		in.NextToken = out.NextToken
	}
	assert.Equal(t, 3, pages)
	assert.Equal(t, []float64{4, 9, 2, 7, 5}, values)

	in.NextToken = aws.String("bm90LWEtdG9rZW4")
	_, err = client.GetMetricData(ctx, in)
	var invalid *cwtypes.InvalidNextToken
	require.ErrorAs(t, err, &invalid)

	in.NextToken = nil
	in.MaxDatapoints = nil
	in.LabelOptions = &cwtypes.LabelOptions{Timezone: aws.String("UTC")}
	_, err = client.GetMetricData(ctx, in)
	require.Error(t, err, "a time zone is + or - and four digits")
}
