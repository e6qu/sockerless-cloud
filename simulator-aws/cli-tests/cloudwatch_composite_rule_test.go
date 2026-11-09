package aws_cli_test

import (
	"strings"
	"testing"
)

func putCLIQuietMetricAlarm(t *testing.T, name string) {
	t.Helper()
	runCLI(t, awsCLI("cloudwatch", "put-metric-alarm",
		"--alarm-name", name, "--namespace", "Custom/CompositeRuleCLI", "--metric-name", name,
		"--comparison-operator", "GreaterThanThreshold", "--evaluation-periods", "1",
		"--period", "60", "--threshold", "0", "--statistic", "Sum", "--treat-missing-data", "notBreaching"))
}

// put-composite-alarm refuses a rule that does not parse or names a missing
// alarm, and delete-alarms refuses an alarm a composite alarm depends on.
func TestCloudWatchCLI_CompositeAlarmRuleRefusals(t *testing.T) {
	t.Parallel()
	child, parent := "cli-rule-child", "cli-rule-parent"
	putCLIQuietMetricAlarm(t, child)
	t.Cleanup(func() { runCLIIgnoreErr(awsCLI("cloudwatch", "delete-alarms", "--alarm-names", child)) })

	for rule, want := range map[string]string{
		`ALARM("cli-rule-child") XOR TRUE`: "Error in AlarmRule [Unsupported token 'XOR' at char 24, must be: <EOF>, 'AND', 'OR']",
		`ALARM(cli-rule-missing)`:          "Could not save the composite alarm as alarms [arn:aws:cloudwatch:us-east-1:123456789012:alarm:cli-rule-missing] in the alarm rule do not exist",
	} {
		out := runCLIExpectError(t, awsCLI("cloudwatch", "put-composite-alarm", "--alarm-name", parent, "--alarm-rule", rule))
		if !strings.Contains(out, "ValidationError") || !strings.Contains(out, want) {
			t.Fatalf("put-composite-alarm %s: %s, want ValidationError %q", rule, out, want)
		}
	}

	runCLI(t, awsCLI("cloudwatch", "put-composite-alarm", "--alarm-name", parent, "--alarm-rule", "ALARM(cli-rule-child)"))
	t.Cleanup(func() { runCLIIgnoreErr(awsCLI("cloudwatch", "delete-alarms", "--alarm-names", parent)) })
	out := runCLIExpectError(t, awsCLI("cloudwatch", "delete-alarms", "--alarm-names", child))
	if want := "Cannot delete cli-rule-child as there are composite alarm(s) depending on it."; !strings.Contains(out, want) {
		t.Fatalf("delete-alarms of a child: %s, want %q", out, want)
	}
}

// AND binds tighter than OR: TRUE OR TRUE AND FALSE puts the composite alarm in
// ALARM, which the CLI's composite-alarm-exists waiter waits for.
func TestCloudWatchCLI_CompositeAlarmPrecedence(t *testing.T) {
	t.Parallel()
	name := "cli-rule-precedence"
	runCLI(t, awsCLI("cloudwatch", "put-composite-alarm", "--alarm-name", name, "--alarm-rule", "TRUE OR TRUE AND FALSE"))
	t.Cleanup(func() { runCLIIgnoreErr(awsCLI("cloudwatch", "delete-alarms", "--alarm-names", name)) })
	runCLI(t, awsCLI("cloudwatch", "wait", "composite-alarm-exists",
		"--alarm-names", name, "--alarm-types", "CompositeAlarm", "--state-value", "ALARM"))
	reason := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", name, "--alarm-types", "CompositeAlarm",
		"--query", "CompositeAlarms[0].StateReason", "--output", "text")))
	if !strings.HasSuffix(reason, "was created and its alarm rule evaluates to ALARM") {
		t.Fatalf("StateReason = %q, want the created reason", reason)
	}
}
