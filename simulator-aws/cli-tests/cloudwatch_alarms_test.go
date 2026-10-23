package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchCLI_MetricAlarms exercises the alarm API over the awsJson
// surface the aws CLI uses: put-metric-alarm, describe-alarms (with the
// metric-derived StateValue), and delete-alarms.
func TestCloudWatchCLI_MetricAlarms(t *testing.T) {
	t.Parallel()
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

// TestCloudWatchCLI_AlarmActionConfiguration writes an alarm's
// ActionConfiguration through put-metric-alarm and put-composite-alarm and reads
// it back through describe-alarms.
func TestCloudWatchCLI_AlarmActionConfiguration(t *testing.T) {
	t.Parallel()
	runCLI(t, awsCLI("cloudwatch", "put-metric-alarm",
		"--alarm-name", "cli-ac-metric", "--namespace", "Custom/ActionConfigurationCLI", "--metric-name", "Errors",
		"--comparison-operator", "GreaterThanThreshold", "--evaluation-periods", "1",
		"--period", "60", "--threshold", "1", "--statistic", "Sum",
		"--tags", "Key=team,Value=payments",
		"--action-configuration", `{"AlarmTagsPropagation":{"Enabled":true,"IncludedTagKeys":["team"]}}`))
	t.Cleanup(func() { runCLIIgnoreErr(awsCLI("cloudwatch", "delete-alarms", "--alarm-names", "cli-ac-metric")) })
	t.Cleanup(func() { runCLIIgnoreErr(awsCLI("cloudwatch", "delete-alarms", "--alarm-names", "cli-ac-composite")) })
	runCLI(t, awsCLI("cloudwatch", "put-composite-alarm",
		"--alarm-name", "cli-ac-composite", "--alarm-rule", `ALARM("cli-ac-metric")`,
		"--action-configuration", `{"AlarmTagsPropagation":{"Enabled":true,"ExcludedTagKeys":["internal"]}}`))

	included := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", "cli-ac-metric",
		"--query", "MetricAlarms[0].ActionConfiguration.AlarmTagsPropagation.[Enabled, IncludedTagKeys[0]]", "--output", "text")))
	if included != "True\tteam" {
		t.Fatalf("metric alarm ActionConfiguration = %q, want enabled with IncludedTagKeys [team]", included)
	}
	excluded := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", "cli-ac-composite", "--alarm-types", "CompositeAlarm",
		"--query", "CompositeAlarms[0].ActionConfiguration.AlarmTagsPropagation.ExcludedTagKeys[0]", "--output", "text")))
	if excluded != "internal" {
		t.Fatalf("composite alarm ExcludedTagKeys = %q, want internal", excluded)
	}

	out := runCLIExpectError(t, awsCLI("cloudwatch", "put-composite-alarm",
		"--alarm-name", "cli-ac-composite", "--alarm-rule", `ALARM("cli-ac-metric")`,
		"--action-configuration", `{"AlarmTagsPropagation":{"Enabled":true,"IncludedTagKeys":["team"],"ExcludedTagKeys":["internal"]}}`))
	if !strings.Contains(out, "InvalidParameterCombination") {
		t.Fatalf("both key lists: %s, want InvalidParameterCombination", out)
	}
}
