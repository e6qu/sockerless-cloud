package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchCLI_Dashboards covers put/get/list/delete-dashboards over the
// CLI surface (issue #608).
func TestCloudWatchCLI_Dashboards(t *testing.T) {
	body := `{"widgets":[{"type":"text","x":0,"y":0,"width":6,"height":2,"properties":{"markdown":"hi"}}]}`
	runCLI(t, awsCLI("cloudwatch", "put-dashboard", "--dashboard-name", "cli-ops", "--dashboard-body", body))
	defer runCLI(t, awsCLI("cloudwatch", "delete-dashboards", "--dashboard-names", "cli-ops"))

	got := runCLI(t, awsCLI("cloudwatch", "get-dashboard", "--dashboard-name", "cli-ops",
		"--query", "DashboardBody", "--output", "text"))
	if !strings.Contains(got, "markdown") {
		t.Fatalf("get-dashboard body = %q", got)
	}
	list := runCLI(t, awsCLI("cloudwatch", "list-dashboards"))
	if !strings.Contains(list, "cli-ops") {
		t.Fatalf("list-dashboards missing cli-ops: %s", list)
	}
}

// TestCloudWatchCLI_AlarmExtendedStatistic covers the percentile round-trip
// over the CLI surface (issue #609).
func TestCloudWatchCLI_AlarmExtendedStatistic(t *testing.T) {
	runCLI(t, awsCLI("cloudwatch", "put-metric-alarm",
		"--alarm-name", "cli-p99", "--namespace", "edd/cli", "--metric-name", "lat",
		"--extended-statistic", "p99", "--period", "300", "--evaluation-periods", "1",
		"--threshold", "120000", "--comparison-operator", "GreaterThanThreshold",
		"--treat-missing-data", "notBreaching"))
	defer runCLI(t, awsCLI("cloudwatch", "delete-alarms", "--alarm-names", "cli-p99"))

	ext := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms", "--alarm-names", "cli-p99",
		"--query", "MetricAlarms[0].ExtendedStatistic", "--output", "text")))
	if ext != "p99" {
		t.Fatalf("ExtendedStatistic = %q, want p99", ext)
	}
}
