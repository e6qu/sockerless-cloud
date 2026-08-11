package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCloudWatchLogsCLI_InsightsQuery covers start-query / get-query-results
// over the CLI surface.
func TestCloudWatchLogsCLI_InsightsQuery(t *testing.T) {
	group, stream := "/cli/insights", "s1"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", group))
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", stream))

	now := time.Now().UnixMilli()
	events, _ := json.Marshal([]map[string]any{
		{"timestamp": now, "message": `{"level":"ERROR","code":500}`},
		{"timestamp": now + 1, "message": `{"level":"INFO","code":200}`},
	})
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream, "--log-events", string(events)))

	qid := strings.TrimSpace(runCLI(t, awsCLI("logs", "start-query",
		"--log-group-name", group,
		"--query-string", `fields @message | filter level = "ERROR"`,
		"--start-time", fmt.Sprint(now/1000-3600),
		"--end-time", fmt.Sprint(now/1000+3600),
		"--query", "queryId", "--output", "text")))
	if qid == "" {
		t.Fatal("start-query returned no queryId")
	}

	status := strings.TrimSpace(runCLI(t, awsCLI("logs", "get-query-results", "--query-id", qid,
		"--query", "status", "--output", "text")))
	if status != "Complete" {
		t.Fatalf("get-query-results status = %q, want Complete", status)
	}
	cnt := strings.TrimSpace(runCLI(t, awsCLI("logs", "get-query-results", "--query-id", qid,
		"--query", "length(results)", "--output", "text")))
	if cnt != "1" {
		t.Fatalf("results length = %q, want 1 (one ERROR event)", cnt)
	}
}
