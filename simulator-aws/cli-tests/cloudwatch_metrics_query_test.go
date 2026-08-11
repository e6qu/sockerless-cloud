package aws_cli_test

import (
	"strings"
	"testing"
	"time"
)

// TestCloudWatchCLI_QueryMetrics exercises the CloudWatch query-protocol
// metrics surface used by botocore: PutMetricData,
// GetMetricStatistics, ListMetrics. The Go SDK uses rpc-v2-cbor for these;
// the aws CLI uses the legacy query protocol, which previously returned
// InvalidAction.
func TestCloudWatchCLI_QueryMetrics(t *testing.T) {
	runCLI(t, awsCLI("cloudwatch", "put-metric-data",
		"--namespace", "MyApp/CLI",
		"--metric-data", `[{"MetricName":"Requests","Value":42,"Unit":"Count","Dimensions":[{"Name":"svc","Value":"api"}]}]`))

	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	sum := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-statistics",
		"--namespace", "MyApp/CLI", "--metric-name", "Requests",
		"--dimensions", "Name=svc,Value=api",
		"--start-time", start, "--end-time", end, "--period", "60",
		"--statistics", "Sum", "Average",
		"--query", "Datapoints[0].Sum", "--output", "text")))
	if sum != "42.0" {
		t.Fatalf("get-metric-statistics Sum = %q, want 42.0", sum)
	}

	metrics := runCLI(t, awsCLI("cloudwatch", "list-metrics", "--namespace", "MyApp/CLI"))
	if !strings.Contains(metrics, "Requests") {
		t.Fatalf("list-metrics did not include the Requests metric: %s", metrics)
	}
}
