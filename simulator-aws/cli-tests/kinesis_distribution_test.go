package aws_cli_test

import (
	"strings"
	"testing"
)

// TestKinesisCLI_RecordDistributionStrategy sets an on-demand stream's record
// distribution strategy and reads it back, and is refused AUTO on a
// provisioned stream.
func TestKinesisCLI_RecordDistributionStrategy(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	stream := "cli-kinesis-distribution"
	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", stream,
		"--stream-mode-details", "StreamMode=ON_DEMAND"))
	t.Cleanup(func() { _ = awsCLI("kinesis", "delete-stream", "--stream-name", stream).Run() })
	arn := q("kinesis", "describe-stream-summary", "--stream-name", stream,
		"--query", "StreamDescriptionSummary.StreamARN", "--output", "text")

	runCLI(t, awsCLI("kinesis", "update-stream-record-distribution-strategy",
		"--stream-arn", arn, "--record-distribution-strategy", "AUTO"))
	if got := q("kinesis", "describe-stream-summary", "--stream-name", stream,
		"--query", "StreamDescriptionSummary.RecordDistributionStrategy", "--output", "text"); got != "AUTO" {
		t.Fatalf("RecordDistributionStrategy = %q, want AUTO", got)
	}

	provisioned := "cli-kinesis-distribution-provisioned"
	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", provisioned, "--shard-count", "1"))
	t.Cleanup(func() { _ = awsCLI("kinesis", "delete-stream", "--stream-name", provisioned).Run() })
	provisionedARN := q("kinesis", "describe-stream-summary", "--stream-name", provisioned,
		"--query", "StreamDescriptionSummary.StreamARN", "--output", "text")
	out := runCLIExpectError(t, awsCLI("kinesis", "update-stream-record-distribution-strategy",
		"--stream-arn", provisionedARN, "--record-distribution-strategy", "AUTO"))
	if !strings.Contains(out, "InvalidArgumentException") {
		t.Fatalf("AUTO on a provisioned stream answered %q, want InvalidArgumentException", out)
	}
}
