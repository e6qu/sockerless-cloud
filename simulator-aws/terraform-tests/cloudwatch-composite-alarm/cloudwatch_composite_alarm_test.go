package cloudwatchcompositealarm_test

import (
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
)

func rule(r string) []string { return []string{"-no-color", "-var", "alarm_rule=" + r} }

// diagnostic joins the lines Terraform wraps an error diagnostic over.
func diagnostic(out []byte) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(string(out), "│", " ")), " ")
}

// TestCloudWatchCompositeAlarmTerraform applies aws_cloudwatch_composite_alarm
// with rules CloudWatch refuses — one that does not parse and one naming an
// alarm that does not exist — then with a valid one, and destroys the child
// metric alarm while the composite alarm, removed from the state, still
// depends on it, which CloudWatch refuses too.
func TestCloudWatchCompositeAlarmTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")

	out := env.TerraformFails(t, append([]string{"apply", "-auto-approve"}, rule(`ALARM("tf-composite-rule-child") XOR TRUE`)...)...)
	assert.Contains(t, diagnostic(out), "ValidationError")
	assert.Contains(t, diagnostic(out), "Error in AlarmRule [Unsupported token 'XOR' at char 33, must be: <EOF>, 'AND', 'OR']")

	out = env.TerraformFails(t, append([]string{"apply", "-auto-approve"}, rule("ALARM(tf-composite-rule-missing)")...)...)
	assert.Contains(t, diagnostic(out), "Could not save the composite alarm as alarms [arn:aws:cloudwatch:us-east-1:123456789012:alarm:tf-composite-rule-missing] in the alarm rule do not exist")

	valid := rule("ALARM(tf-composite-rule-child) OR TRUE AND FALSE")
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, valid...)...)
	env.Terraform(t, "state", "rm", "-state="+env.State, "aws_cloudwatch_composite_alarm.parent")
	out = env.TerraformFails(t, append([]string{"destroy", "-auto-approve"}, valid...)...)
	assert.Contains(t, diagnostic(out), "Cannot delete tf-composite-rule-child as there are composite alarm(s) depending on it.")

	env.Terraform(t, append([]string{"import", "-state=" + env.State}, append(valid, "aws_cloudwatch_composite_alarm.parent", "tf-composite-rule-parent")...)...)
	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, valid...)...)
}
