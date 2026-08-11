package aws_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCloudWatchCLI_ManagedInsightRules covers put-managed-insight-rules creating
// a managed rule over a resource ARN and list-managed-insight-rules reading it
// back with its template name and rule state.
func TestCloudWatchCLI_ManagedInsightRules(t *testing.T) {
	resourceARN := "arn:aws:ec2:us-east-1:000000000000:vpc/vpc-cli12345"
	rules := []map[string]any{{
		"TemplateName": "VPC-Flow-Logs-By-Source",
		"ResourceARN":  resourceARN,
	}}
	b, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("marshal managed rules: %v", err)
	}
	f := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}

	runCLI(t, awsCLI("cloudwatch", "put-managed-insight-rules", "--managed-rules", "file://"+f))

	template := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "list-managed-insight-rules",
		"--resource-arn", resourceARN,
		"--query", "ManagedRules[0].TemplateName", "--output", "text")))
	if template != "VPC-Flow-Logs-By-Source" {
		t.Fatalf("ManagedRules[0].TemplateName = %q, want VPC-Flow-Logs-By-Source", template)
	}
	state := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "list-managed-insight-rules",
		"--resource-arn", resourceARN,
		"--query", "ManagedRules[0].RuleState.State", "--output", "text")))
	if state != "ENABLED" {
		t.Fatalf("ManagedRules[0].RuleState.State = %q, want ENABLED", state)
	}
}

// TestCloudWatchCLI_InsightRuleReport covers get-insight-rule-report over an
// existing insight rule: an honestly-empty report (zero aggregate, no
// contributors) with the real shape.
func TestCloudWatchCLI_InsightRuleReport(t *testing.T) {
	name := "cli-report-rule"
	def := `{"Schema":{"Name":"CloudWatchLogRule","Version":1},"LogGroupNames":["/aws/lambda/x"],"Contribution":{"Keys":["$.requestId"]},"AggregateOn":"Count"}`
	runCLI(t, awsCLI("cloudwatch", "put-insight-rule", "--rule-name", name, "--rule-definition", def))
	defer func() { _ = awsCLI("cloudwatch", "delete-insight-rules", "--rule-names", name).Run() }()

	agg := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-insight-rule-report",
		"--rule-name", name,
		"--start-time", "2024-01-01T00:00:00Z",
		"--end-time", "2024-01-01T00:05:00Z",
		"--period", "60",
		"--query", "AggregateValue", "--output", "text")))
	if agg != "0.0" && agg != "0" {
		t.Fatalf("AggregateValue = %q, want 0", agg)
	}
	count := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-insight-rule-report",
		"--rule-name", name,
		"--start-time", "2024-01-01T00:00:00Z",
		"--end-time", "2024-01-01T00:05:00Z",
		"--period", "60",
		"--query", "length(MetricDatapoints)", "--output", "text")))
	if count != "5" {
		t.Fatalf("MetricDatapoints length = %q, want 5", count)
	}
}

// TestCloudWatchCLI_MetricWidgetImage covers get-metric-widget-image returning a
// base64-encoded PNG that decodes to the expected PNG magic bytes.
func TestCloudWatchCLI_MetricWidgetImage(t *testing.T) {
	widget := `{"metrics":[["AWS/EC2","CPUUtilization","InstanceId","i-1234567890abcdef0"]],"width":250,"height":150,"stat":"Average"}`
	// --output text emits the MetricWidgetImage blob as base64 text.
	b64 := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-widget-image",
		"--metric-widget", widget, "--output-format", "png",
		"--query", "MetricWidgetImage", "--output", "text")))
	if b64 == "" {
		t.Fatal("get-metric-widget-image returned no image bytes")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode base64 image: %v", err)
	}
	if len(raw) < 8 || string(raw[1:4]) != "PNG" {
		t.Fatalf("image is not a PNG (first bytes %v)", raw)
	}
}
