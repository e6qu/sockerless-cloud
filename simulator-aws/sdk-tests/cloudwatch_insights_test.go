package aws_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatchLogs_InsightsQuery covers StartQuery / GetQueryResults: the
// Insights query language runs over the log events and returns rows.
func TestCloudWatchLogs_InsightsQuery(t *testing.T) {
	cw := cwLogsClient()
	group, stream := "/test/insights", "s1"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
	require.NoError(t, err)

	now := time.Now().UnixMilli()
	msgs := []string{
		`{"level":"ERROR","code":500,"svc":"api"}`,
		`{"level":"INFO","code":200,"svc":"api"}`,
		`{"level":"ERROR","code":503,"svc":"web"}`,
	}
	var events []cwlogtypes.InputLogEvent
	for i, m := range msgs {
		events = append(events, cwlogtypes.InputLogEvent{Timestamp: aws.Int64(now + int64(i)), Message: aws.String(m)})
	}
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: events})
	require.NoError(t, err)

	start, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupName: aws.String(group),
		QueryString:  aws.String(`fields @message, level | filter level = "ERROR"`),
		StartTime:    aws.Int64(now/1000 - 3600),
		EndTime:      aws.Int64(now/1000 + 3600),
	})
	require.NoError(t, err)

	res, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: start.QueryId})
	require.NoError(t, err)
	assert.Equal(t, cwlogtypes.QueryStatusComplete, res.Status)
	require.Len(t, res.Results, 2, "two ERROR events must match")
	for _, row := range res.Results {
		for _, f := range row {
			if aws.ToString(f.Field) == "level" {
				assert.Equal(t, "ERROR", aws.ToString(f.Value))
			}
		}
	}

	// stats aggregation by group.
	agg, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupName: aws.String(group),
		QueryString:  aws.String(`stats count() as n by level`),
		StartTime:    aws.Int64(now/1000 - 3600),
		EndTime:      aws.Int64(now/1000 + 3600),
	})
	require.NoError(t, err)
	aggRes, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: agg.QueryId})
	require.NoError(t, err)
	counts := map[string]string{}
	for _, row := range aggRes.Results {
		var level, n string
		for _, f := range row {
			switch aws.ToString(f.Field) {
			case "level":
				level = aws.ToString(f.Value)
			case "n":
				n = aws.ToString(f.Value)
			}
		}
		counts[level] = n
	}
	assert.Equal(t, "2", counts["ERROR"], fmt.Sprintf("stats count by level: %v", counts))
	assert.Equal(t, "1", counts["INFO"])

	// DescribeQueries surfaces the runs; StopQuery is accepted.
	dq, err := cw.DescribeQueries(ctx, &cloudwatchlogs.DescribeQueriesInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(dq.Queries), 1, "DescribeQueries must list this group's queries")

	_, err = cw.StopQuery(ctx, &cloudwatchlogs.StopQueryInput{QueryId: agg.QueryId})
	require.NoError(t, err)
}
