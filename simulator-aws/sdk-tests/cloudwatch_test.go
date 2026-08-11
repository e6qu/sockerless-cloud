package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_FilterLogEventsByStreamPrefix covers logStreamNamePrefix on
// FilterLogEvents: the prefix scopes the searched streams (previously ignored,
// so events from every stream leaked in), and supplying both logStreamNames and
// logStreamNamePrefix is rejected, matching real CloudWatch Logs.
func TestCloudWatch_FilterLogEventsByStreamPrefix(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/prefix-filter"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	for _, s := range []string{"ws-aaa/x", "ws-bbb/y"} {
		_, err := cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
			LogGroupName: aws.String(group), LogStreamName: aws.String(s),
		})
		require.NoError(t, err)
	}
	now := time.Now().UnixMilli()
	put := func(stream, msg string) {
		_, err := cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
			LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
			LogEvents: []cwlogtypes.InputLogEvent{{Timestamp: aws.Int64(now), Message: aws.String(msg)}},
		})
		require.NoError(t, err)
	}
	put("ws-aaa/x", "hello-aaa")
	put("ws-bbb/y", "hello-bbb")

	out, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:        aws.String(group),
		LogStreamNamePrefix: aws.String("ws-aaa"),
	})
	require.NoError(t, err)
	var msgs []string
	for _, e := range out.Events {
		msgs = append(msgs, aws.ToString(e.Message))
	}
	assert.Equal(t, []string{"hello-aaa"}, msgs, "prefix must scope to ws-aaa streams only")

	_, err = cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:        aws.String(group),
		LogStreamNamePrefix: aws.String("ws-aaa"),
		LogStreamNames:      []string{"ws-bbb/y"},
	})
	require.Error(t, err, "logStreamNames and logStreamNamePrefix are mutually exclusive")
}

func TestCloudWatch_GetLogEventsPagination(t *testing.T) {
	cw := cwLogsClient()

	logGroup := "/test/pagination"
	streamName := "test-stream"

	// Create log group and stream
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroup),
	})

	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String(streamName),
	})
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// Put initial batch of events
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String(streamName),
		LogEvents: []cwlogtypes.InputLogEvent{
			{Timestamp: aws.Int64(now), Message: aws.String("event-1")},
			{Timestamp: aws.Int64(now + 1), Message: aws.String("event-2")},
			{Timestamp: aws.Int64(now + 2), Message: aws.String("event-3")},
		},
	})
	require.NoError(t, err)

	// Read all events
	result1, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String(streamName),
		StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, result1.Events, 3)
	assert.Equal(t, "event-1", *result1.Events[0].Message)
	assert.Equal(t, "event-3", *result1.Events[2].Message)

	token := result1.NextForwardToken
	require.NotNil(t, token)

	// Read again with same token — no new events, should return empty
	result2, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String(streamName),
		StartFromHead: aws.Bool(true),
		NextToken:     token,
	})
	require.NoError(t, err)
	assert.Empty(t, result2.Events, "should return no events when no new data")
	assert.Equal(t, *token, *result2.NextForwardToken, "token should stay the same when no new events")

	// Put more events
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String(streamName),
		LogEvents: []cwlogtypes.InputLogEvent{
			{Timestamp: aws.Int64(now + 10), Message: aws.String("event-4")},
			{Timestamp: aws.Int64(now + 11), Message: aws.String("event-5")},
		},
	})
	require.NoError(t, err)

	// Read with the old token — should get only new events
	result3, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String(streamName),
		StartFromHead: aws.Bool(true),
		NextToken:     token,
	})
	require.NoError(t, err)
	require.Len(t, result3.Events, 2, "should return only the 2 new events")
	assert.Equal(t, "event-4", *result3.Events[0].Message)
	assert.Equal(t, "event-5", *result3.Events[1].Message)

	// New token should be different
	assert.NotEqual(t, *token, *result3.NextForwardToken, "token should change when new events exist")
}

func TestCloudWatch_PutRetentionPolicy_SDK(t *testing.T) {
	cw := cwLogsClient()
	logGroup := "/test/retention"

	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
	})

	_, err = cw.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(logGroup),
		RetentionInDays: aws.Int32(14),
	})
	require.NoError(t, err)
	groups, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.Len(t, groups.LogGroups, 1)
	assert.Equal(t, int32(14), aws.ToInt32(groups.LogGroups[0].RetentionInDays))
}

func TestCloudWatch_DescribeLogStreamsOrdering(t *testing.T) {
	cw := cwLogsClient()

	logGroup := "/test/stream-ordering"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroup),
	})

	// Create 3 streams
	streams := []string{"stream-a", "stream-b", "stream-c"}
	for _, s := range streams {
		_, err := cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
			LogGroupName:  aws.String(logGroup),
			LogStreamName: aws.String(s),
		})
		require.NoError(t, err)
	}

	now := time.Now().UnixMilli()

	// Put events at different times: stream-b gets the newest event
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String("stream-a"),
		LogEvents:     []cwlogtypes.InputLogEvent{{Timestamp: aws.Int64(now), Message: aws.String("old")}},
	})
	require.NoError(t, err)

	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String("stream-c"),
		LogEvents:     []cwlogtypes.InputLogEvent{{Timestamp: aws.Int64(now + 100), Message: aws.String("medium")}},
	})
	require.NoError(t, err)

	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: aws.String("stream-b"),
		LogEvents:     []cwlogtypes.InputLogEvent{{Timestamp: aws.Int64(now + 200), Message: aws.String("newest")}},
	})
	require.NoError(t, err)

	// Describe with OrderBy=LastEventTime, Descending=true
	result, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
		OrderBy:      cwlogtypes.OrderByLastEventTime,
		Descending:   aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, result.LogStreams, 3)
	assert.Equal(t, "stream-b", *result.LogStreams[0].LogStreamName, "newest stream should be first")
	assert.Equal(t, "stream-c", *result.LogStreams[1].LogStreamName)
	assert.Equal(t, "stream-a", *result.LogStreams[2].LogStreamName, "oldest stream should be last")

	// With Limit=1, should return only the newest stream
	limited, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
		OrderBy:      cwlogtypes.OrderByLastEventTime,
		Descending:   aws.Bool(true),
		Limit:        aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, limited.LogStreams, 1)
	assert.Equal(t, "stream-b", *limited.LogStreams[0].LogStreamName)

	// Default ordering (by name, ascending)
	byName, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.Len(t, byName.LogStreams, 3)
	assert.Equal(t, "stream-a", *byName.LogStreams[0].LogStreamName)
	assert.Equal(t, "stream-b", *byName.LogStreams[1].LogStreamName)
	assert.Equal(t, "stream-c", *byName.LogStreams[2].LogStreamName)
}

func TestCW_DescribeLogGroups_Pagination(t *testing.T) {
	cw := cwLogsClient()
	names := []string{"/pag/cw/a", "/pag/cw/b", "/pag/cw/c"}
	for _, n := range names {
		_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(n)})
		require.NoError(t, err)
		t.Cleanup(func() {
			cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(n)})
		})
	}

	seen := map[string]bool{}
	pager := cloudwatchlogs.NewDescribeLogGroupsPaginator(cw, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String("/pag/cw/"),
		Limit:              aws.Int32(1),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, g := range page.LogGroups {
			seen[aws.ToString(g.LogGroupName)] = true
		}
	}
	for _, n := range names {
		assert.True(t, seen[n], "log group %s should appear via pagination", n)
	}
}
