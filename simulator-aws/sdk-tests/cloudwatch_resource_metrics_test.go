package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_ResourceMetricsConfiguration takes a resource's detailed
// metrics configuration through its life: one per resource, only for a
// resource that exists, its selections replaced (not merged) on update.
func TestCloudWatch_ResourceMetricsConfiguration(t *testing.T) {
	client := cloudwatchClient()
	streams := kinesisClient()
	stream := "sdk-cw-resource-metrics"
	_, err := streams.CreateStream(ctx, &kinesis.CreateStreamInput{StreamName: aws.String(stream), ShardCount: aws.Int32(1)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = streams.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(stream)}) })
	summary, err := streams.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(stream)})
	require.NoError(t, err)
	arn := summary.StreamDescriptionSummary.StreamARN

	created, err := client.CreateResourceMetricsConfiguration(ctx, &cloudwatch.CreateResourceMetricsConfigurationInput{
		ResourceArn:      arn,
		MetricSelections: []cwtypes.ResourceMetricSelection{{IncludeMetrics: []string{"IncomingRecords", "IncomingBytes"}}},
	})
	require.NoError(t, err)
	config := created.ResourceMetricsConfiguration
	require.Equal(t, aws.ToString(arn), aws.ToString(config.ResourceArn))
	require.Equal(t, []string{"IncomingRecords", "IncomingBytes"}, config.MetricSelections[0].IncludeMetrics)
	require.Equal(t, aws.ToTime(config.CreatedAt), aws.ToTime(config.UpdatedAt))

	_, err = client.CreateResourceMetricsConfiguration(ctx, &cloudwatch.CreateResourceMetricsConfigurationInput{ResourceArn: arn})
	var conflict *cwtypes.ConflictException
	require.True(t, errors.As(err, &conflict), "a second configuration for one resource: %v", err)

	updated, err := client.UpdateResourceMetricsConfiguration(ctx, &cloudwatch.UpdateResourceMetricsConfigurationInput{ResourceArn: arn})
	require.NoError(t, err)
	require.Empty(t, updated.ResourceMetricsConfiguration.MetricSelections, "omitting the selections collects every metric")

	got, err := client.GetResourceMetricsConfiguration(ctx, &cloudwatch.GetResourceMetricsConfigurationInput{ResourceArn: arn})
	require.NoError(t, err)
	require.Empty(t, got.ResourceMetricsConfiguration.MetricSelections)

	_, err = client.DeleteResourceMetricsConfiguration(ctx, &cloudwatch.DeleteResourceMetricsConfigurationInput{ResourceArn: arn})
	require.NoError(t, err)
	_, err = client.GetResourceMetricsConfiguration(ctx, &cloudwatch.GetResourceMetricsConfigurationInput{ResourceArn: arn})
	var notFound *cwtypes.ResourceNotFoundException
	require.True(t, errors.As(err, &notFound), "a deleted configuration: %v", err)

	absent := aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/does-not-exist")
	_, err = client.CreateResourceMetricsConfiguration(ctx, &cloudwatch.CreateResourceMetricsConfigurationInput{ResourceArn: absent})
	require.True(t, errors.As(err, &notFound), "a configuration for a resource that does not exist: %v", err)
}

// TestCloudWatch_OTelEnrichmentFilters holds enrichment's include and exclude
// filters to the documented rules: set at start, replaced as a pair on update,
// and update refused while enrichment is stopped.
func TestCloudWatch_OTelEnrichmentFilters(t *testing.T) {
	client := cloudwatchClient()
	_, err := client.StopOTelEnrichment(ctx, &cloudwatch.StopOTelEnrichmentInput{})
	require.NoError(t, err)
	_, err = client.UpdateOTelEnrichment(ctx, &cloudwatch.UpdateOTelEnrichmentInput{})
	var notFound *cwtypes.ResourceNotFoundException
	require.True(t, errors.As(err, &notFound), "update while stopped: %v", err)

	started, err := client.StartOTelEnrichment(ctx, &cloudwatch.StartOTelEnrichmentInput{
		IncludeFilters: []cwtypes.OTelEnrichmentMetricSelector{{Namespace: aws.String("AWS/EC2")}},
		ExcludeFilters: []cwtypes.OTelEnrichmentMetricSelector{{Namespace: aws.String("AWS/EC2"), MetricNames: []string{"CPUCreditBalance"}}},
	})
	require.NoError(t, err)
	require.Len(t, started.IncludeFilters, 1)
	require.Len(t, started.ExcludeFilters, 1)

	updated, err := client.UpdateOTelEnrichment(ctx, &cloudwatch.UpdateOTelEnrichmentInput{
		IncludeFilters: []cwtypes.OTelEnrichmentMetricSelector{{Namespace: aws.String("AWS/Lambda")}},
	})
	require.NoError(t, err)
	require.Equal(t, "AWS/Lambda", aws.ToString(updated.IncludeFilters[0].Namespace))
	require.Empty(t, updated.ExcludeFilters, "the filters are replaced as a pair")

	got, err := client.GetOTelEnrichment(ctx, &cloudwatch.GetOTelEnrichmentInput{})
	require.NoError(t, err)
	require.Equal(t, cwtypes.OTelEnrichmentStatusRunning, got.Status)
	require.Len(t, got.IncludeFilters, 1)
	require.Empty(t, got.ExcludeFilters)

	_, err = client.StopOTelEnrichment(ctx, &cloudwatch.StopOTelEnrichmentInput{})
	require.NoError(t, err)
}
