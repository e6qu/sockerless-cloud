package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchCLI_ResourceMetricsConfiguration drives a resource's detailed
// metrics configuration and enrichment's filters through the aws CLI.
func TestCloudWatchCLI_ResourceMetricsConfiguration(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	stream := "cli-cw-resource-metrics"
	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", stream, "--shard-count", "1"))
	t.Cleanup(func() { _ = awsCLI("kinesis", "delete-stream", "--stream-name", stream).Run() })
	arn := q("kinesis", "describe-stream-summary", "--stream-name", stream,
		"--query", "StreamDescriptionSummary.StreamARN", "--output", "text")

	runCLI(t, awsCLI("cloudwatch", "create-resource-metrics-configuration", "--resource-arn", arn,
		"--metric-selections", "IncludeMetrics=IncomingRecords"))
	if got := q("cloudwatch", "get-resource-metrics-configuration", "--resource-arn", arn,
		"--query", "ResourceMetricsConfiguration.MetricSelections[0].IncludeMetrics[0]", "--output", "text"); got != "IncomingRecords" {
		t.Fatalf("selected metric = %q, want IncomingRecords", got)
	}
	out := runCLIExpectError(t, awsCLI("cloudwatch", "create-resource-metrics-configuration", "--resource-arn", arn))
	if !strings.Contains(out, "ConflictException") {
		t.Fatalf("a second configuration answered %q, want ConflictException", out)
	}
	runCLI(t, awsCLI("cloudwatch", "update-resource-metrics-configuration", "--resource-arn", arn))
	runCLI(t, awsCLI("cloudwatch", "delete-resource-metrics-configuration", "--resource-arn", arn))
	out = runCLIExpectError(t, awsCLI("cloudwatch", "get-resource-metrics-configuration", "--resource-arn", arn))
	if !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("a deleted configuration answered %q, want ResourceNotFoundException", out)
	}

	runCLI(t, awsCLI("cloudwatch", "start-otel-enrichment", "--include-filters", "Namespace=AWS/EC2"))
	t.Cleanup(func() { _ = awsCLI("cloudwatch", "stop-otel-enrichment").Run() })
	runCLI(t, awsCLI("cloudwatch", "update-otel-enrichment", "--include-filters", "Namespace=AWS/Lambda"))
	if got := q("cloudwatch", "get-otel-enrichment", "--query", "IncludeFilters[0].Namespace", "--output", "text"); got != "AWS/Lambda" {
		t.Fatalf("include filter after update = %q, want AWS/Lambda", got)
	}
}
