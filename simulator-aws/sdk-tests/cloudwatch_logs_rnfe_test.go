package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCWLogs_ReadOpsRejectMissingGroup covers the group-existence contract for
// the log-reading ops: FilterLogEvents / DescribeLogStreams / GetLogEvents must
// return ResourceNotFoundException (a declared error for each) when the group
// does not exist, not an empty result that masks misconfiguration.
func TestCWLogs_ReadOpsRejectMissingGroup(t *testing.T) {
	c := cwLogsClient()
	const missing = "/does-not-exist-rnfe"

	assertRNFE := func(op string, err error) {
		t.Helper()
		require.Error(t, err, "%s on a missing group must error", op)
		var nf *cwlogtypes.ResourceNotFoundException
		assert.True(t, errors.As(err, &nf),
			"%s must return ResourceNotFoundException; got %T: %v", op, err, err)
	}

	_, err := c.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(missing)})
	assertRNFE("FilterLogEvents", err)

	_, err = c.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{LogGroupName: aws.String(missing)})
	assertRNFE("DescribeLogStreams", err)

	_, err = c.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(missing),
		LogStreamName: aws.String("any-stream"),
	})
	assertRNFE("GetLogEvents", err)

	// Positive: an existing but empty group returns empty results, not an error
	// (the contract the consumer relies on to distinguish empty from absent).
	const groupName = "/rnfe/exists"
	_, err = c.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(groupName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(groupName)})
	})

	fle, err := c.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(groupName)})
	require.NoError(t, err, "FilterLogEvents on an existing empty group returns empty, not error")
	assert.Empty(t, fle.Events)

	dls, err := c.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{LogGroupName: aws.String(groupName)})
	require.NoError(t, err)
	assert.Empty(t, dls.LogStreams)
}
