package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogs_MetricFilterRoundTrip covers PutMetricFilter, DescribeMetricFilters,
// TestMetricFilter, and DeleteMetricFilter against a real log group.
func TestLogs_MetricFilterRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/metric-filter"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	_, err = cw.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName:  aws.String(group),
		FilterName:    aws.String("errors"),
		FilterPattern: aws.String("ERROR"),
		MetricTransformations: []cwlogtypes.MetricTransformation{{
			MetricName:      aws.String("ErrorCount"),
			MetricNamespace: aws.String("MyApp"),
			MetricValue:     aws.String("1"),
			DefaultValue:    aws.Float64(0),
		}},
	})
	require.NoError(t, err)

	desc, err := cw.DescribeMetricFilters(ctx, &cloudwatchlogs.DescribeMetricFiltersInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, desc.MetricFilters, 1)
	assert.Equal(t, "errors", aws.ToString(desc.MetricFilters[0].FilterName))
	assert.Equal(t, "ERROR", aws.ToString(desc.MetricFilters[0].FilterPattern))
	require.Len(t, desc.MetricFilters[0].MetricTransformations, 1)
	assert.Equal(t, "ErrorCount", aws.ToString(desc.MetricFilters[0].MetricTransformations[0].MetricName))

	// Filter by metric name/namespace.
	descByMetric, err := cw.DescribeMetricFilters(ctx, &cloudwatchlogs.DescribeMetricFiltersInput{
		MetricName:      aws.String("ErrorCount"),
		MetricNamespace: aws.String("MyApp"),
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(descByMetric.MetricFilters), 1)

	test, err := cw.TestMetricFilter(ctx, &cloudwatchlogs.TestMetricFilterInput{
		FilterPattern:    aws.String("ERROR"),
		LogEventMessages: []string{"ERROR boom", "all good", "another ERROR"},
	})
	require.NoError(t, err)
	assert.Len(t, test.Matches, 2)

	_, err = cw.DeleteMetricFilter(ctx, &cloudwatchlogs.DeleteMetricFilterInput{
		LogGroupName: aws.String(group),
		FilterName:   aws.String("errors"),
	})
	require.NoError(t, err)

	desc2, err := cw.DescribeMetricFilters(ctx, &cloudwatchlogs.DescribeMetricFiltersInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Empty(t, desc2.MetricFilters)
}

// TestLogs_SubscriptionFilterRoundTrip covers PutSubscriptionFilter,
// DescribeSubscriptionFilters, and DeleteSubscriptionFilter.
func TestLogs_SubscriptionFilterRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/sub-filter"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	destArn := "arn:aws:lambda:us-east-1:123456789012:function:log-sink"
	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName:   aws.String(group),
		FilterName:     aws.String("to-lambda"),
		FilterPattern:  aws.String(""),
		DestinationArn: aws.String(destArn),
		Distribution:   cwlogtypes.DistributionByLogStream,
	})
	require.NoError(t, err)

	desc, err := cw.DescribeSubscriptionFilters(ctx, &cloudwatchlogs.DescribeSubscriptionFiltersInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, desc.SubscriptionFilters, 1)
	assert.Equal(t, "to-lambda", aws.ToString(desc.SubscriptionFilters[0].FilterName))
	assert.Equal(t, destArn, aws.ToString(desc.SubscriptionFilters[0].DestinationArn))
	assert.Equal(t, cwlogtypes.DistributionByLogStream, desc.SubscriptionFilters[0].Distribution)

	_, err = cw.DeleteSubscriptionFilter(ctx, &cloudwatchlogs.DeleteSubscriptionFilterInput{
		LogGroupName: aws.String(group),
		FilterName:   aws.String("to-lambda"),
	})
	require.NoError(t, err)

	desc2, err := cw.DescribeSubscriptionFilters(ctx, &cloudwatchlogs.DescribeSubscriptionFiltersInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Empty(t, desc2.SubscriptionFilters)
}

// TestLogs_RetentionAndTags covers PutRetentionPolicy/DeleteRetentionPolicy and
// the legacy TagLogGroup/UntagLogGroup/ListTagsLogGroup verbs plus the newer
// TagResource/UntagResource ARN-based verbs.
func TestLogs_RetentionAndTags(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/retention-tags"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	_, err = cw.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(group),
		RetentionInDays: aws.Int32(30),
	})
	require.NoError(t, err)
	g, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, g.LogGroups, 1)
	assert.Equal(t, int32(30), aws.ToInt32(g.LogGroups[0].RetentionInDays))

	_, err = cw.DeleteRetentionPolicy(ctx, &cloudwatchlogs.DeleteRetentionPolicyInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	g2, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, g2.LogGroups, 1)
	assert.Nil(t, g2.LogGroups[0].RetentionInDays)

	// Legacy tag verbs (keyed by log-group name).
	_, err = cw.TagLogGroup(ctx, &cloudwatchlogs.TagLogGroupInput{
		LogGroupName: aws.String(group),
		Tags:         map[string]string{"env": "test", "team": "infra"},
	})
	require.NoError(t, err)
	lt, err := cw.ListTagsLogGroup(ctx, &cloudwatchlogs.ListTagsLogGroupInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Equal(t, "test", lt.Tags["env"])
	assert.Equal(t, "infra", lt.Tags["team"])

	_, err = cw.UntagLogGroup(ctx, &cloudwatchlogs.UntagLogGroupInput{
		LogGroupName: aws.String(group),
		Tags:         []string{"team"},
	})
	require.NoError(t, err)
	lt2, err := cw.ListTagsLogGroup(ctx, &cloudwatchlogs.ListTagsLogGroupInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	_, hasTeam := lt2.Tags["team"]
	assert.False(t, hasTeam)
	assert.Equal(t, "test", lt2.Tags["env"])

	// ARN-based UntagResource.
	arn := aws.ToString(g.LogGroups[0].Arn)
	_, err = cw.UntagResource(ctx, &cloudwatchlogs.UntagResourceInput{
		ResourceArn: aws.String(arn),
		TagKeys:     []string{"env"},
	})
	require.NoError(t, err)
	lt3, err := cw.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{
		ResourceArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Empty(t, lt3.Tags)
}

// TestLogs_DeleteLogStream covers CreateLogStream then DeleteLogStream removing
// the stream and its events.
func TestLogs_DeleteLogStream(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/delete-stream"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String("s1"),
	})
	require.NoError(t, err)

	_, err = cw.DeleteLogStream(ctx, &cloudwatchlogs.DeleteLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String("s1"),
	})
	require.NoError(t, err)

	streams, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Empty(t, streams.LogStreams)

	// Deleting a now-missing stream is a ResourceNotFoundException.
	_, err = cw.DeleteLogStream(ctx, &cloudwatchlogs.DeleteLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String("s1"),
	})
	require.Error(t, err)
}

// TestLogs_ExportTaskRoundTrip covers CreateExportTask, DescribeExportTasks, and
// CancelExportTask.
func TestLogs_ExportTaskRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/export-task"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	out, err := cw.CreateExportTask(ctx, &cloudwatchlogs.CreateExportTaskInput{
		LogGroupName: aws.String(group),
		TaskName:     aws.String("export-1"),
		From:         aws.Int64(1000),
		To:           aws.Int64(2000),
		Destination:  aws.String("my-export-bucket"),
	})
	require.NoError(t, err)
	taskId := aws.ToString(out.TaskId)
	require.NotEmpty(t, taskId)

	desc, err := cw.DescribeExportTasks(ctx, &cloudwatchlogs.DescribeExportTasksInput{
		TaskId: aws.String(taskId),
	})
	require.NoError(t, err)
	require.Len(t, desc.ExportTasks, 1)
	assert.Equal(t, group, aws.ToString(desc.ExportTasks[0].LogGroupName))
	assert.Equal(t, "my-export-bucket", aws.ToString(desc.ExportTasks[0].Destination))
	require.NotNil(t, desc.ExportTasks[0].Status)
	assert.Equal(t, cwlogtypes.ExportTaskStatusCodeCompleted, desc.ExportTasks[0].Status.Code)

	// A completed task cannot be cancelled.
	_, err = cw.CancelExportTask(ctx, &cloudwatchlogs.CancelExportTaskInput{
		TaskId: aws.String(taskId),
	})
	require.Error(t, err)
}

// TestLogs_DataProtectionPolicy covers Put/Get/DeleteDataProtectionPolicy.
func TestLogs_DataProtectionPolicy(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/data-protection"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	doc := `{"Name":"policy","Version":"2021-06-01","Statement":[{"Sid":"audit","DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Audit":{"FindingsDestination":{}}}}]}`
	put, err := cw.PutDataProtectionPolicy(ctx, &cloudwatchlogs.PutDataProtectionPolicyInput{
		LogGroupIdentifier: aws.String(group),
		PolicyDocument:     aws.String(doc),
	})
	require.NoError(t, err)
	assert.Equal(t, doc, aws.ToString(put.PolicyDocument))

	get, err := cw.GetDataProtectionPolicy(ctx, &cloudwatchlogs.GetDataProtectionPolicyInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.NoError(t, err)
	assert.Equal(t, doc, aws.ToString(get.PolicyDocument))
	assert.Equal(t, group, aws.ToString(get.LogGroupIdentifier))

	_, err = cw.DeleteDataProtectionPolicy(ctx, &cloudwatchlogs.DeleteDataProtectionPolicyInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.NoError(t, err)

	_, err = cw.GetDataProtectionPolicy(ctx, &cloudwatchlogs.GetDataProtectionPolicyInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.Error(t, err)
}
