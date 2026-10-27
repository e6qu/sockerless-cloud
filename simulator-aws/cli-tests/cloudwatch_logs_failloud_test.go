package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchLogsCLI_FailLoud expects a malformed filter pattern or query
// string to fail rather than return an empty result. runCLI fails the test on
// a non-zero exit, so the calls use CombinedOutput.
func TestCloudWatchLogsCLI_FailLoud(t *testing.T) {
	t.Parallel()
	group := "/cli/failloud"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() })

	out, err := awsCLI("logs", "filter-log-events",
		"--log-group-name", group,
		"--filter-pattern", `{ $.level = }`).CombinedOutput()
	if err == nil {
		t.Fatalf("filter-log-events with a malformed pattern should fail; got:\n%s", out)
	}
	if !strings.Contains(string(out), "InvalidParameterException") {
		t.Fatalf("expected InvalidParameterException; got:\n%s", out)
	}

	out, err = awsCLI("logs", "start-query",
		"--log-group-name", group,
		"--query-string", `fields @message | filter (level = "ERROR"`,
		"--start-time", "0", "--end-time", "1").CombinedOutput()
	if err == nil {
		t.Fatalf("start-query with a malformed query should fail; got:\n%s", out)
	}
	if !strings.Contains(string(out), "MalformedQueryException") {
		t.Fatalf("expected MalformedQueryException; got:\n%s", out)
	}
}
