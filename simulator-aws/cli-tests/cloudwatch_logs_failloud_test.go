package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchLogsCLI_FailLoud is the CloudWatch arm of the #652 prevention
// work (BUG-2170), over the aws CLI: a malformed filter-pattern / query-string
// fails loudly instead of returning an empty result. Uses CombinedOutput because
// the calls are expected to exit non-zero (runCLI fatals on non-zero exit).
func TestCloudWatchLogsCLI_FailLoud(t *testing.T) {
	group := "/cli/failloud"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() })

	// Malformed filter pattern.
	out, err := awsCLI("logs", "filter-log-events",
		"--log-group-name", group,
		"--filter-pattern", `{ $.level = }`).CombinedOutput()
	if err == nil {
		t.Fatalf("filter-log-events with a malformed pattern should fail; got:\n%s", out)
	}
	if !strings.Contains(string(out), "InvalidParameterException") {
		t.Fatalf("expected InvalidParameterException; got:\n%s", out)
	}

	// Malformed Insights query.
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
