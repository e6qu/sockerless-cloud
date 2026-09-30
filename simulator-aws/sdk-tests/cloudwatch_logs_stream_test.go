package aws_sdk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cwLogsStreamClient is a stock CloudWatch Logs client pointed at the
// simulator's logs endpoint coordinate. StartLiveTail and GetLogObject carry
// the modeled `@endpoint(hostPrefix: "stream-")` trait, so the SDK sends and
// signs them against `stream-logs.localhost` exactly as it sends them to
// `stream-logs.us-east-1.amazonaws.com` against real AWS.
func cwLogsStreamClient() *cloudwatchlogs.Client {
	return cloudwatchlogs.NewFromConfig(sdkConfig(), func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(simEndpoint("logs"))
	})
}

// seedLogGroupWithEvents creates a log group + stream and ingests events,
// returning the group ARN. The streaming ops surface this stored history.
func seedLogGroupWithEvents(t *testing.T, cw *cloudwatchlogs.Client, group, stream string, messages []string) string {
	t.Helper()
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(group),
	})
	require.NoError(t, err)
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
	})
	require.NoError(t, err)

	now := time.Now().UnixMilli()
	events := make([]cwltypes.InputLogEvent, 0, len(messages))
	for i, m := range messages {
		events = append(events, cwltypes.InputLogEvent{
			Message:   aws.String(m),
			Timestamp: aws.Int64(now + int64(i)),
		})
	}
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents:     events,
	})
	require.NoError(t, err)

	desc, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(group),
	})
	require.NoError(t, err)
	require.NotEmpty(t, desc.LogGroups)
	return aws.ToString(desc.LogGroups[0].Arn)
}

// TestLogs_StartLiveTail opens a Live Tail session over the event stream and
// reassembles its sessionStart and sessionUpdate frames: the session carries
// the events ingested after it started, and not the history stored before it.
func TestLogs_StartLiveTail(t *testing.T) {
	cw := cwLogsStreamClient()
	group := "livetail-group"
	stream := "livetail-stream"
	groupArn := seedLogGroupWithEvents(t, cw, group, stream, []string{"stored before the session"})

	// StartLiveTail carries the modeled `stream-` endpoint host prefix, so the
	// request the SDK signs and sends addresses stream-logs.<endpoint> — the
	// same host shape real AWS serves it on.
	var sent capturedRequest
	out, err := cw.StartLiveTail(ctx, &cloudwatchlogs.StartLiveTailInput{
		LogGroupIdentifiers: []string{groupArn},
	}, func(o *cloudwatchlogs.Options) {
		o.APIOptions = append(o.APIOptions, captureSignedRequest(&sent))
	})
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("stream-logs.localhost:%d", simPort), sent.host)
	assert.Contains(t, sent.signedHeaders(), "host")

	es := out.GetStream()
	defer es.Close()

	start, ok := <-es.Events()
	require.True(t, ok, "stream must open with a sessionStart event")
	sessionStart, ok := start.(*cwltypes.StartLiveTailResponseStreamMemberSessionStart)
	require.True(t, ok, "first event = %T, want sessionStart", start)
	require.NotEmpty(t, aws.ToString(sessionStart.Value.SessionId))
	require.NotEmpty(t, aws.ToString(sessionStart.Value.RequestId))
	assert.Contains(t, sessionStart.Value.LogGroupIdentifiers, groupArn)

	now := time.Now().UnixMilli()
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents: []cwltypes.InputLogEvent{
			{Message: aws.String("first live tail line"), Timestamp: aws.Int64(now)},
			{Message: aws.String("second live tail line"), Timestamp: aws.Int64(now + 1)},
		},
	})
	require.NoError(t, err)

	var collected []string
	for len(collected) < 2 {
		ev, ok := <-es.Events()
		require.True(t, ok, "the session closed before delivering the ingested events: %v", es.Err())
		update, ok := ev.(*cwltypes.StartLiveTailResponseStreamMemberSessionUpdate)
		require.True(t, ok, "event = %T, want sessionUpdate", ev)
		for _, le := range update.Value.SessionResults {
			collected = append(collected, aws.ToString(le.Message))
			assert.Equal(t, groupArn, aws.ToString(le.LogGroupIdentifier))
			assert.Equal(t, stream, aws.ToString(le.LogStreamName))
		}
	}
	assert.Equal(t, []string{"first live tail line", "second live tail line"}, collected,
		"the session must carry the events ingested after it started and nothing stored before it")
}

// awaitLogLine waits for a line containing want to reach a log group and
// returns its message. It opens a Live Tail session first and then reads the
// stored events, so a line written before the session opened is found in the
// history and one written after arrives on the session.
func awaitLogLine(t *testing.T, cw *cloudwatchlogs.Client, group, want string, timeout time.Duration) string {
	t.Helper()
	groups, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(group)})
	require.NoError(t, err)
	var groupArn string
	for _, g := range groups.LogGroups {
		if aws.ToString(g.LogGroupName) == group {
			groupArn = aws.ToString(g.Arn)
		}
	}
	require.NotEmpty(t, groupArn, "log group %s does not exist", group)

	tailCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tail, err := cwLogsStreamClient().StartLiveTail(tailCtx, &cloudwatchlogs.StartLiveTailInput{
		LogGroupIdentifiers: []string{groupArn},
	})
	require.NoError(t, err)
	stream := tail.GetStream()
	defer stream.Close()

	stored, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	for _, e := range stored.Events {
		if strings.Contains(aws.ToString(e.Message), want) {
			return aws.ToString(e.Message)
		}
	}
	for ev := range stream.Events() {
		update, ok := ev.(*cwltypes.StartLiveTailResponseStreamMemberSessionUpdate)
		if !ok {
			continue
		}
		for _, le := range update.Value.SessionResults {
			if strings.Contains(aws.ToString(le.Message), want) {
				return aws.ToString(le.Message)
			}
		}
	}
	t.Fatalf("no line containing %q reached log group %s within %s: %v", want, group, timeout, stream.Err())
	return ""
}

// TestLogs_StartLiveTail_UnknownGroup returns a ResourceNotFoundException
// before the stream opens for an unresolvable log group.
func TestLogs_StartLiveTail_UnknownGroup(t *testing.T) {
	cw := cwLogsStreamClient()
	_, err := cw.StartLiveTail(ctx, &cloudwatchlogs.StartLiveTailInput{
		LogGroupIdentifiers: []string{"arn:aws:logs:us-east-1:123456789012:log-group:no-such-group"},
	})
	requireAWSErrorCode(t, err, "ResourceNotFoundException")
}

// TestLogs_GetLogObject streams a stored log object back over the event stream
// and asserts the FieldsData carries the referenced event's bytes.
func TestLogs_GetLogObject(t *testing.T) {
	cw := cwLogsStreamClient()
	group := "getlogobject-group"
	stream := "getlogobject-stream"
	seedLogGroupWithEvents(t, cw, group, stream, []string{
		"object-line-0",
		"object-line-1",
	})

	// The pointer the sim honors is "<group>:<stream>:<index>".
	pointer := group + ":" + stream + ":1"
	out, err := cw.GetLogObject(ctx, &cloudwatchlogs.GetLogObjectInput{
		LogObjectPointer: aws.String(pointer),
	})
	require.NoError(t, err)

	es := out.GetStream()
	defer es.Close()

	var data []byte
	sawFields := false
	for ev := range es.Events() {
		if f, ok := ev.(*cwltypes.GetLogObjectResponseStreamMemberFields); ok {
			sawFields = true
			data = f.Value.Data
		}
	}
	require.NoError(t, es.Err())
	assert.True(t, sawFields, "stream must carry a fields event")
	assert.Equal(t, "object-line-1", string(data))
}
