package aws_cli_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func putCLIBaseline(t *testing.T, ns, metric string) {
	t.Helper()
	now := time.Now().UTC()
	var data []map[string]any
	for i := range 20 {
		data = append(data, map[string]any{
			"MetricName": metric,
			"Value":      10 + float64(i%3) - 1,
			"Timestamp":  now.Add(-time.Duration(10+i) * time.Minute).Format(time.RFC3339),
		})
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	runCLI(t, awsCLI("cloudwatch", "put-metric-data", "--namespace", ns, "--metric-data", string(raw)))
}

func TestCloudWatchCLI_AnomalyBandAlarmAndGraph(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/AnomalyCLI")
	putCLIBaseline(t, ns, "Errors")
	runCLI(t, awsCLI("cloudwatch", "put-anomaly-detector", "--single-metric-anomaly-detector",
		fmt.Sprintf(`{"Namespace":"%s","MetricName":"Errors","Stat":"Average"}`, ns)))
	require.Eventually(t, func() bool {
		return strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-anomaly-detectors", "--namespace", ns,
			"--query", "AnomalyDetectors[0].StateValue", "--output", "text"))) == "TRAINED"
	}, 30*time.Second, 500*time.Millisecond)

	metrics := fmt.Sprintf(`[{"Id":"m1","MetricStat":{"Metric":{"Namespace":"%s","MetricName":"Errors"},"Period":60,"Stat":"Average"},"ReturnData":true},{"Id":"ad1","Expression":"ANOMALY_DETECTION_BAND(m1, 3)"}]`, ns)
	end := time.Now().UTC()
	out := runCLI(t, awsCLI("cloudwatch", "get-metric-data", "--metric-data-queries", metrics,
		"--start-time", end.Add(-time.Hour).Format(time.RFC3339), "--end-time", end.Format(time.RFC3339), "--output", "json"))
	var data struct {
		MetricDataResults []struct {
			Id     string    `json:"Id"`
			Values []float64 `json:"Values"`
		} `json:"MetricDataResults"`
	}
	parseJSON(t, out, &data)
	require.Len(t, data.MetricDataResults, 3)
	assert.Equal(t, "ad1", data.MetricDataResults[1].Id)
	require.NotEmpty(t, data.MetricDataResults[1].Values)
	assert.Greater(t, data.MetricDataResults[2].Values[0], data.MetricDataResults[1].Values[0], "the band answers its lower edge, then its upper")

	name := uniquename.New("cli-anomaly-alarm")
	runCLI(t, awsCLI("cloudwatch", "put-metric-alarm", "--alarm-name", name,
		"--comparison-operator", "GreaterThanUpperThreshold", "--evaluation-periods", "1",
		"--threshold-metric-id", "ad1", "--metrics", metrics, "--treat-missing-data", "notBreaching"))
	t.Cleanup(func() { _ = awsCLI("cloudwatch", "delete-alarms", "--alarm-names", name).Run() })
	assert.Equal(t, "ad1", strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms", "--alarm-names", name,
		"--query", "MetricAlarms[0].ThresholdMetricId", "--output", "text"))))
	runCLI(t, awsCLI("cloudwatch", "put-metric-data", "--namespace", ns, "--metric-name", "Errors", "--value", "500"))
	require.Eventually(t, func() bool {
		return strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms", "--alarm-names", name,
			"--query", "MetricAlarms[0].StateValue", "--output", "text"))) == "ALARM"
	}, 30*time.Second, 500*time.Millisecond)

	widget := fmt.Sprintf(`{"metrics":[["%s","Errors",{"id":"m1","color":"#9467bd"}],[{"expression":"ANOMALY_DETECTION_BAND(m1, 2)","id":"ad1"}]],"period":60,"start":"-PT1H","width":320,"height":200}`, ns)
	b64 := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-widget-image", "--metric-widget", widget,
		"--query", "MetricWidgetImage", "--output", "text")))
	raw, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	purple := 0
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 == 0x94 && g>>8 == 0x67 && b>>8 == 0xbd {
				purple++
			}
		}
	}
	assert.Greater(t, purple, 50, "the metric's line is drawn")
}

func TestCloudWatchCLI_InsightRuleReportFromLogs(t *testing.T) {
	t.Parallel()
	group := uniquename.New("/cli/clf")
	name := uniquename.New("cli-clf-rule")
	def := `{"Schema":{"Name":"CloudWatchLogRule","Version":1},"LogGroupNames":["` + group + `"],"LogFormat":"CLF","Fields":{"1":"IpAddress","6":"StatusCode"},` +
		`"Contribution":{"Keys":["IpAddress"],"Filters":[{"Match":"StatusCode","EqualTo":404}]},"AggregateOn":"Count"}`
	runCLI(t, awsCLI("cloudwatch", "put-insight-rule", "--rule-name", name, "--rule-definition", def))
	t.Cleanup(func() { _ = awsCLI("cloudwatch", "delete-insight-rules", "--rule-names", name).Run() })
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() })
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", "s"))
	now := time.Now().UTC()
	var events []map[string]any
	for i, line := range []string{
		`192.0.2.1 - - [10/Oct/2024:13:55:36 +0000] "GET /a HTTP/1.1" 404 10`,
		`192.0.2.1 - - [10/Oct/2024:13:55:37 +0000] "GET /b HTTP/1.1" 404 10`,
		`192.0.2.2 - - [10/Oct/2024:13:55:38 +0000] "GET /c HTTP/1.1" 404 10`,
		`192.0.2.3 - - [10/Oct/2024:13:55:39 +0000] "GET /d HTTP/1.1" 200 10`,
	} {
		events = append(events, map[string]any{"timestamp": now.UnixMilli() + int64(i), "message": line})
	}
	raw, err := json.Marshal(events)
	require.NoError(t, err)
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", "s", "--log-events", string(raw)))

	out := runCLI(t, awsCLI("cloudwatch", "get-insight-rule-report", "--rule-name", name,
		"--start-time", now.Add(-time.Minute).Format(time.RFC3339), "--end-time", now.Add(time.Minute).Format(time.RFC3339),
		"--period", "60", "--max-contributor-count", "1", "--output", "json"))
	var rep struct {
		AggregateValue         float64 `json:"AggregateValue"`
		ApproximateUniqueCount int64   `json:"ApproximateUniqueCount"`
		Contributors           []struct {
			Keys                      []string `json:"Keys"`
			ApproximateAggregateValue float64  `json:"ApproximateAggregateValue"`
		} `json:"Contributors"`
	}
	parseJSON(t, out, &rep)
	assert.Equal(t, 3.0, rep.AggregateValue)
	assert.Equal(t, int64(2), rep.ApproximateUniqueCount)
	require.Len(t, rep.Contributors, 1, "MaxContributorCount caps the list")
	assert.Equal(t, []string{"192.0.2.1"}, rep.Contributors[0].Keys)
	assert.Equal(t, 2.0, rep.Contributors[0].ApproximateAggregateValue)
}

func TestLogsCLI_AnomalyDetectorFindsNewPattern(t *testing.T) {
	t.Parallel()
	group := uniquename.New("/cli/anomaly")
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() })
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", "s"))
	put := func(at time.Time, lines ...string) {
		var events []map[string]any
		for i, l := range lines {
			events = append(events, map[string]any{"timestamp": at.UnixMilli() + int64(i), "message": l})
		}
		raw, err := json.Marshal(events)
		require.NoError(t, err)
		runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", "s", "--log-events", string(raw)))
	}
	var baseline []string
	for i := range 10 {
		baseline = append(baseline, fmt.Sprintf("worker started job %d", i))
	}
	put(time.Now().Add(-time.Hour), baseline...)
	groupArn := strings.TrimSpace(runCLI(t, awsCLI("logs", "describe-log-groups", "--log-group-name-prefix", group,
		"--query", "logGroups[0].logGroupArn", "--output", "text")))
	arn := strings.TrimSpace(runCLI(t, awsCLI("logs", "create-log-anomaly-detector", "--log-group-arn-list", groupArn,
		"--detector-name", "cli-anomalies", "--evaluation-frequency", "ONE_MIN", "--query", "anomalyDetectorArn", "--output", "text")))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-log-anomaly-detector", "--anomaly-detector-arn", arn).Run() })
	require.Eventually(t, func() bool {
		return strings.TrimSpace(runCLI(t, awsCLI("logs", "get-log-anomaly-detector", "--anomaly-detector-arn", arn,
			"--query", "anomalyDetectorStatus", "--output", "text"))) == "ANALYZING"
	}, 30*time.Second, 500*time.Millisecond)

	put(time.Now(), "FATAL worker crashed with exit status 137")
	var anomalyID string
	require.Eventually(t, func() bool {
		anomalyID = strings.TrimSpace(runCLI(t, awsCLI("logs", "list-anomalies", "--anomaly-detector-arn", arn,
			"--query", "anomalies[0].anomalyId", "--output", "text")))
		return anomalyID != "" && anomalyID != "None"
	}, 3*time.Minute, 2*time.Second, "the detector runs once a minute")
	assert.Equal(t, "FATAL worker crashed with exit status <*>", strings.TrimSpace(runCLI(t, awsCLI("logs", "list-anomalies",
		"--anomaly-detector-arn", arn, "--query", "anomalies[0].patternString", "--output", "text"))))

	runCLI(t, awsCLI("logs", "update-anomaly", "--anomaly-detector-arn", arn, "--anomaly-id", anomalyID, "--suppression-type", "INFINITE"))
	assert.Equal(t, anomalyID, strings.TrimSpace(runCLI(t, awsCLI("logs", "list-anomalies", "--anomaly-detector-arn", arn,
		"--suppression-state", "SUPPRESSED", "--query", "anomalies[0].anomalyId", "--output", "text"))))
}

func TestLogsCLI_ScheduledQueryHistory(t *testing.T) {
	t.Parallel()
	group := uniquename.New("/cli/scheduled")
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() })
	role := createCLIECSRole(t, awsCLI, uniquename.New("cli-sq-exec"), "logs.amazonaws.com", "logs:StartQuery", "logs:GetQueryResults")
	arn := strings.TrimSpace(runCLI(t, awsCLI("logs", "create-scheduled-query", "--name", uniquename.New("cli-sq"),
		"--query-language", "CWLI", "--query-string", "stats count(*) as n",
		"--log-group-identifiers", group, "--schedule-expression", "cron(* * * * ? *)",
		"--execution-role-arn", role, "--query", "scheduledQueryArn", "--output", "text")))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-scheduled-query", "--identifier", arn).Run() })
	require.Eventually(t, func() bool {
		status := strings.TrimSpace(runCLI(t, awsCLI("logs", "get-scheduled-query-history", "--identifier", arn,
			"--start-time", "0", "--end-time", fmt.Sprint(time.Now().Add(time.Hour).UnixMilli()),
			"--query", "triggerHistory[0].executionStatus", "--output", "text")))
		return status == "Complete"
	}, 2*time.Minute, 2*time.Second)
	assert.Equal(t, "Complete", strings.TrimSpace(runCLI(t, awsCLI("logs", "get-scheduled-query", "--identifier", arn,
		"--query", "lastExecutionStatus", "--output", "text"))))
}

func TestLogsCLI_ImportTaskFromEventDataStore(t *testing.T) {
	t.Parallel()
	eds := strings.TrimSpace(runCLI(t, awsCLI("cloudtrail", "create-event-data-store", "--name", uniquename.New("cli-import-eds"),
		"--retention-period", "7", "--no-termination-protection-enabled", "--query", "EventDataStoreArn", "--output", "text")))
	t.Cleanup(func() { _ = awsCLI("cloudtrail", "delete-event-data-store", "--event-data-store", eds).Run() })
	runCLI(t, awsCLI("ec2", "describe-vpcs"))
	role := createCLIECSRole(t, awsCLI, uniquename.New("cli-import"), "cloudtrail.amazonaws.com",
		"cloudtrail:GetEventDataStoreData", "logs:CreateLogGroup", "logs:CreateLogStream")
	id := strings.TrimSpace(runCLI(t, awsCLI("logs", "create-import-task", "--import-source-arn", eds, "--import-role-arn", role,
		"--query", "importId", "--output", "text")))
	t.Cleanup(func() {
		_ = awsCLI("logs", "delete-log-group", "--log-group-name", "aws/cloudtrail/"+eds[strings.LastIndex(eds, "/")+1:]).Run()
	})
	require.Eventually(t, func() bool {
		return strings.TrimSpace(runCLI(t, awsCLI("logs", "describe-import-tasks", "--import-id", id,
			"--query", "imports[0].importStatus", "--output", "text"))) == "COMPLETED"
	}, 30*time.Second, 500*time.Millisecond)
	assert.NotEqual(t, "0", strings.TrimSpace(runCLI(t, awsCLI("logs", "describe-import-tasks", "--import-id", id,
		"--query", "imports[0].importStatistics.bytesImported", "--output", "text"))))
	assert.Equal(t, "COMPLETED", strings.TrimSpace(runCLI(t, awsCLI("logs", "describe-import-task-batches", "--import-id", id,
		"--batch-import-status", "COMPLETED", "--query", "importBatches[0].status", "--output", "text"))))
}
