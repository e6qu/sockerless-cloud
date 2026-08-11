package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCloudWatchCLI_LogAlarms covers put-log-alarm creating a log alarm over a
// CloudWatch Logs scheduled query, describe-alarms --alarm-types LogAlarm
// reading it back in the state the query results imply, and delete-alarms
// removing it along with the service-managed scheduled query.
func TestCloudWatchCLI_LogAlarms(t *testing.T) {
	group := "/cli/log-alarm/" + fmt.Sprint(time.Now().UnixNano())
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() }()
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", "s1"))

	nowMillis := time.Now().UTC().UnixMilli()
	events := []map[string]any{
		{"timestamp": nowMillis - 2000, "message": `{"level":"INFO","msg":"fine"}`},
		{"timestamp": nowMillis - 1000, "message": `{"level":"ERROR","msg":"boom"}`},
		{"timestamp": nowMillis, "message": `{"level":"ERROR","msg":"boom again"}`},
	}
	eventsFile := writeJSONDoc(t, "log-events.json", events)
	runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", group, "--log-stream-name", "s1",
		"--log-events", "file://"+eventsFile))

	name := "cli-log-alarm"
	queryConfig := map[string]any{
		"QueryString":           `fields @message | filter level = "ERROR"`,
		"LogGroupIdentifiers":   []string{group},
		"ScheduledQueryRoleARN": "arn:aws:iam::000000000000:role/CWScheduledQueryRole",
		"AggregationExpression": "count(*) as hits",
		"ScheduleConfiguration": map[string]any{
			"ScheduleExpression": "rate(5 minutes)",
			"StartTimeOffset":    600,
			"EndTimeOffset":      0,
		},
	}
	configFile := writeJSONDoc(t, "scheduled-query.json", queryConfig)

	runCLI(t, awsCLI("cloudwatch", "put-log-alarm",
		"--alarm-name", name,
		"--alarm-description", "errors in the last window",
		"--scheduled-query-configuration", "file://"+configFile,
		"--query-results-to-evaluate", "1",
		"--query-results-to-alarm", "1",
		"--threshold", "1",
		"--comparison-operator", "GreaterThanThreshold",
		"--treat-missing-data", "missing"))
	defer func() { _ = awsCLI("cloudwatch", "delete-alarms", "--alarm-names", name).Run() }()

	state := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", name, "--alarm-types", "LogAlarm",
		"--query", "LogAlarms[0].StateValue", "--output", "text")))
	if state != "ALARM" {
		t.Fatalf("LogAlarms[0].StateValue = %q, want ALARM", state)
	}
	agg := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", name, "--alarm-types", "LogAlarm",
		"--query", "LogAlarms[0].ScheduledQueryConfiguration.AggregationExpression", "--output", "text")))
	if agg != "count(*) as hits" {
		t.Fatalf("AggregationExpression = %q", agg)
	}

	// The QueryARN the alarm reports resolves to a real scheduled query.
	queryArn := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", name, "--alarm-types", "LogAlarm",
		"--query", "LogAlarms[0].ScheduledQueryConfiguration.QueryARN", "--output", "text")))
	if queryArn == "" {
		t.Fatal("log alarm reported no QueryARN")
	}
	schedule := strings.TrimSpace(runCLI(t, awsCLI("logs", "get-scheduled-query",
		"--identifier", queryArn, "--query", "scheduleExpression", "--output", "text")))
	if schedule != "rate(5 minutes)" {
		t.Fatalf("scheduled query scheduleExpression = %q", schedule)
	}

	// An empty AlarmTypes filter means metric alarms only — the log alarm is
	// not returned unless it is asked for.
	metricOnly := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", name, "--query", "length(MetricAlarms)", "--output", "text")))
	if metricOnly != "0" {
		t.Fatalf("MetricAlarms length = %q, want 0", metricOnly)
	}

	runCLI(t, awsCLI("cloudwatch", "delete-alarms", "--alarm-names", name))
	remaining := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", name, "--alarm-types", "LogAlarm",
		"--query", "length(LogAlarms)", "--output", "text")))
	if remaining != "0" {
		t.Fatalf("after delete, LogAlarms length = %q, want 0", remaining)
	}
	if err := awsCLI("logs", "get-scheduled-query", "--identifier", queryArn).Run(); err == nil {
		t.Fatal("the service-managed scheduled query must be deleted with the alarm")
	}
}

// writeJSONDoc marshals v into a temp file and returns its path, for the CLI's
// file:// argument form.
func writeJSONDoc(t *testing.T, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}
