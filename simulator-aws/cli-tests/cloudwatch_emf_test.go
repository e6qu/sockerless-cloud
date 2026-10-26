package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCloudWatchCLI_EMFExtraction puts an EMF message (an
// _aws.CloudWatchMetrics block) as a log event, and the metric appears in
// list-metrics and get-metric-statistics without put-metric-data.
func TestCloudWatchCLI_EMFExtraction(t *testing.T) {
	t.Parallel()
	group := "/cli/emf"
	stream := "emf-stream"
	ns := "edd/emf-cli"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", group))
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", stream))

	now := time.Now().UnixMilli()
	emf := fmt.Sprintf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":%q,"Dimensions":[["svc"]],"Metrics":[{"Name":"probe","Unit":"Count"}]}]},"svc":"x","probe":42}`, now, ns)
	events, err := json.Marshal([]map[string]any{{"timestamp": now, "message": emf}})
	if err != nil {
		t.Fatal(err)
	}
	runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", group, "--log-stream-name", stream, "--log-events", string(events)))

	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	sum := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-statistics",
		"--namespace", ns, "--metric-name", "probe",
		"--dimensions", "Name=svc,Value=x",
		"--start-time", start, "--end-time", end, "--period", "60",
		"--statistics", "Sum",
		"--query", "Datapoints[0].Sum", "--output", "text")))
	if sum != "42.0" {
		t.Fatalf("EMF-extracted metric Sum = %q, want 42.0", sum)
	}

	metrics := runCLI(t, awsCLI("cloudwatch", "list-metrics", "--namespace", ns))
	if !strings.Contains(metrics, "probe") {
		t.Fatalf("list-metrics did not include the EMF-extracted metric: %s", metrics)
	}
}
