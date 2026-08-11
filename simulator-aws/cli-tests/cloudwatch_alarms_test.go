package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchCLI_MetricAlarms exercises the alarm API over the awsJson
// surface the aws CLI uses: put-metric-alarm, describe-alarms (with the
// metric-derived StateValue), and delete-alarms.
func TestCloudWatchCLI_MetricAlarms(t *testing.T) {
	ns := "Custom/AlarmsCLI"
	runCLI(t, awsCLI("cloudwatch", "put-metric-data", "--namespace", ns,
		"--metric-data", `[{"MetricName":"Latency","Value":5,"Unit":"Milliseconds"}]`))

	runCLI(t, awsCLI("cloudwatch", "put-metric-alarm",
		"--alarm-name", "cli-alarm", "--namespace", ns, "--metric-name", "Latency",
		"--comparison-operator", "GreaterThanThreshold", "--evaluation-periods", "1",
		"--period", "60", "--threshold", "0", "--statistic", "Sum",
		"--treat-missing-data", "notBreaching"))

	state := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", "cli-alarm",
		"--query", "MetricAlarms[0].StateValue", "--output", "text")))
	if state != "ALARM" {
		t.Fatalf("describe-alarms StateValue = %q, want ALARM (value 5 > threshold 0)", state)
	}

	runCLI(t, awsCLI("cloudwatch", "delete-alarms", "--alarm-names", "cli-alarm"))
	remaining := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", "cli-alarm",
		"--query", "length(MetricAlarms)", "--output", "text")))
	if remaining != "0" {
		t.Fatalf("alarm should be deleted; MetricAlarms length = %q", remaining)
	}
}
