package aws_cli_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogs_CreateAndDescribeLogGroup(t *testing.T) {
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/cli/test-group"))

	out := runCLI(t, awsCLI("logs", "describe-log-groups",
		"--log-group-name-prefix", "/cli/test",
		"--output", "json",
	))

	var result struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
			Arn          string `json:"arn"`
		} `json:"logGroups"`
	}
	parseJSON(t, out, &result)
	require.Len(t, result.LogGroups, 1)
	assert.Equal(t, "/cli/test-group", result.LogGroups[0].LogGroupName)

	// Cleanup
	runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", "/cli/test-group"))
}

func TestLogs_CreateLogStream(t *testing.T) {
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/cli/stream-test"))
	runCLI(t, awsCLI("logs", "create-log-stream",
		"--log-group-name", "/cli/stream-test",
		"--log-stream-name", "test-stream",
	))

	out := runCLI(t, awsCLI("logs", "describe-log-streams",
		"--log-group-name", "/cli/stream-test",
		"--output", "json",
	))

	var result struct {
		LogStreams []struct {
			LogStreamName string `json:"logStreamName"`
		} `json:"logStreams"`
	}
	parseJSON(t, out, &result)
	require.Len(t, result.LogStreams, 1)
	assert.Equal(t, "test-stream", result.LogStreams[0].LogStreamName)

	// Cleanup
	runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", "/cli/stream-test"))
}

func TestLogs_PutAndGetLogEvents(t *testing.T) {
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/cli/events-test"))
	runCLI(t, awsCLI("logs", "create-log-stream",
		"--log-group-name", "/cli/events-test",
		"--log-stream-name", "events-stream",
	))

	now := time.Now().UnixMilli()
	events := fmt.Sprintf(`[{"timestamp":%d,"message":"hello from cli"},{"timestamp":%d,"message":"second message"}]`, now, now+1)

	runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", "/cli/events-test",
		"--log-stream-name", "events-stream",
		"--log-events", events,
	))

	out := runCLI(t, awsCLI("logs", "get-log-events",
		"--log-group-name", "/cli/events-test",
		"--log-stream-name", "events-stream",
		"--output", "json",
	))

	var result struct {
		Events []struct {
			Message   string `json:"message"`
			Timestamp int64  `json:"timestamp"`
		} `json:"events"`
	}
	parseJSON(t, out, &result)
	require.Len(t, result.Events, 2)
	assert.Equal(t, "hello from cli", result.Events[0].Message)
	assert.Equal(t, "second message", result.Events[1].Message)

	// Cleanup
	runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", "/cli/events-test"))
}

func TestLogs_FilterLogEvents(t *testing.T) {
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/cli/filter-test"))
	runCLI(t, awsCLI("logs", "create-log-stream",
		"--log-group-name", "/cli/filter-test",
		"--log-stream-name", "filter-stream",
	))

	now := time.Now().UnixMilli()
	events := fmt.Sprintf(`[{"timestamp":%d,"message":"ERROR something bad"},{"timestamp":%d,"message":"INFO all good"}]`, now, now+1)

	runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", "/cli/filter-test",
		"--log-stream-name", "filter-stream",
		"--log-events", events,
	))

	out := runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", "/cli/filter-test",
		"--filter-pattern", "ERROR",
		"--output", "json",
	))

	var result struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &result)
	require.Len(t, result.Events, 1)
	assert.Contains(t, result.Events[0].Message, "ERROR")

	// Cleanup
	runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", "/cli/filter-test"))
}

// TestLogs_FilterLogEventsByStreamPrefix covers --log-stream-name-prefix: it
// scopes the searched streams (previously ignored, so every stream's events
// leaked in).
func TestLogs_FilterLogEventsByStreamPrefix(t *testing.T) {
	group := "/cli/prefix-filter"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", group))
	for _, s := range []string{"ws-aaa/x", "ws-bbb/y"} {
		runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", s))
	}
	now := time.Now().UnixMilli()
	put := func(stream, msg string) {
		runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream,
			"--log-events", fmt.Sprintf(`[{"timestamp":%d,"message":%q}]`, now, msg)))
	}
	put("ws-aaa/x", "hello-aaa")
	put("ws-bbb/y", "hello-bbb")

	out := runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", group, "--log-stream-name-prefix", "ws-aaa",
		"--output", "json"))
	var result struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &result)
	require.Len(t, result.Events, 1, "prefix must scope to ws-aaa streams only")
	assert.Equal(t, "hello-aaa", result.Events[0].Message)
}

func TestLogs_DeleteLogGroup(t *testing.T) {
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/cli/delete-test"))
	runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", "/cli/delete-test"))

	out := runCLI(t, awsCLI("logs", "describe-log-groups",
		"--log-group-name-prefix", "/cli/delete-test",
		"--output", "json",
	))

	var result struct {
		LogGroups []struct{} `json:"logGroups"`
	}
	parseJSON(t, out, &result)
	assert.Empty(t, result.LogGroups)
}
