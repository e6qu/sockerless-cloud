package aws_cli_test

import (
	"strings"
	"testing"
)

func createCLICloudWatchMetricStreamDestination(t *testing.T, suffix string) (string, string) {
	t.Helper()
	bucket := "cli-cloudwatch-metric-stream-" + suffix
	deliveryRole := "cli-cloudwatch-firehose-" + suffix
	cloudWatchRole := "cli-cloudwatch-metrics-" + suffix
	deliveryStream := "cli-cloudwatch-destination-" + suffix

	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	t.Cleanup(func() {
		objects := runCLI(t, awsCLI("s3api", "list-objects-v2", "--bucket", bucket, "--output", "json"))
		var listed struct {
			Contents []struct {
				Key string `json:"Key"`
			} `json:"Contents"`
		}
		parseJSON(t, objects, &listed)
		for _, object := range listed.Contents {
			runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", object.Key))
		}
		runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", bucket))
	})

	deliveryRoleOutput := runCLI(t, awsCLI("iam", "create-role",
		"--role-name", deliveryRole,
		"--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"firehose.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		"--output", "json"))
	var delivery struct {
		Role struct {
			ARN string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, deliveryRoleOutput, &delivery)
	runCLI(t, awsCLI("iam", "put-role-policy",
		"--role-name", deliveryRole,
		"--policy-name", "delivery",
		"--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket","s3:PutObject"],"Resource":["arn:aws:s3:::`+bucket+`","arn:aws:s3:::`+bucket+`/*"]}]}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("iam", "delete-role-policy", "--role-name", deliveryRole, "--policy-name", "delivery"))
		runCLI(t, awsCLI("iam", "delete-role", "--role-name", deliveryRole))
	})

	createdStream := runCLI(t, awsCLI("firehose", "create-delivery-stream",
		"--delivery-stream-name", deliveryStream,
		"--extended-s3-destination-configuration",
		`{"RoleARN":"`+delivery.Role.ARN+`","BucketARN":"arn:aws:s3:::`+bucket+`","BufferingHints":{"SizeInMBs":1,"IntervalInSeconds":0}}`,
		"--output", "json"))
	var stream struct {
		DeliveryStreamARN string `json:"DeliveryStreamARN"`
	}
	parseJSON(t, createdStream, &stream)
	t.Cleanup(func() {
		runCLI(t, awsCLI("firehose", "delete-delivery-stream",
			"--delivery-stream-name", deliveryStream, "--allow-force-delete"))
	})

	cloudWatchRoleOutput := runCLI(t, awsCLI("iam", "create-role",
		"--role-name", cloudWatchRole,
		"--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"streams.metrics.cloudwatch.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		"--output", "json"))
	var cloudWatch struct {
		Role struct {
			ARN string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, cloudWatchRoleOutput, &cloudWatch)
	runCLI(t, awsCLI("iam", "put-role-policy",
		"--role-name", cloudWatchRole,
		"--policy-name", "delivery",
		"--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"firehose:PutRecord","Resource":"`+stream.DeliveryStreamARN+`"}]}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("iam", "delete-role-policy", "--role-name", cloudWatchRole, "--policy-name", "delivery"))
		runCLI(t, awsCLI("iam", "delete-role", "--role-name", cloudWatchRole))
	})

	return stream.DeliveryStreamARN, cloudWatch.Role.ARN
}

// TestCloudWatchCLI_AlarmActionsAndState covers enable/disable-alarm-actions,
// set-alarm-state and describe-alarm-history over the CLI surface.
func TestCloudWatchCLI_AlarmActionsAndState(t *testing.T) {
	name := "cli-alarm-actions"
	runCLI(t, awsCLI("cloudwatch", "put-metric-alarm",
		"--alarm-name", name, "--namespace", "Custom/ActionsCLI", "--metric-name", "M",
		"--comparison-operator", "GreaterThanThreshold", "--evaluation-periods", "1",
		"--period", "60", "--threshold", "0", "--statistic", "Sum", "--treat-missing-data", "notBreaching"))
	defer func() { _ = awsCLI("cloudwatch", "delete-alarms", "--alarm-names", name).Run() }()

	runCLI(t, awsCLI("cloudwatch", "disable-alarm-actions", "--alarm-names", name))
	enabled := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms", "--alarm-names", name,
		"--query", "MetricAlarms[0].ActionsEnabled", "--output", "text")))
	if enabled != "False" {
		t.Fatalf("after disable, ActionsEnabled = %q, want False", enabled)
	}
	runCLI(t, awsCLI("cloudwatch", "enable-alarm-actions", "--alarm-names", name))

	runCLI(t, awsCLI("cloudwatch", "set-alarm-state", "--alarm-name", name,
		"--state-value", "ALARM", "--state-reason", "manual cli override"))
	state := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms", "--alarm-names", name,
		"--query", "MetricAlarms[0].StateValue", "--output", "text")))
	if state != "ALARM" {
		t.Fatalf("after set-alarm-state, StateValue = %q, want ALARM", state)
	}

	histType := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarm-history",
		"--alarm-name", name, "--history-item-type", "StateUpdate",
		"--query", "AlarmHistoryItems[0].HistoryItemType", "--output", "text")))
	if histType != "StateUpdate" {
		t.Fatalf("describe-alarm-history first item type = %q, want StateUpdate", histType)
	}
}

// TestCloudWatchCLI_DescribeAlarmsForMetric covers the metric→alarm reverse lookup.
func TestCloudWatchCLI_DescribeAlarmsForMetric(t *testing.T) {
	ns := "Custom/ReverseCLI"
	runCLI(t, awsCLI("cloudwatch", "put-metric-alarm",
		"--alarm-name", "cli-reverse", "--namespace", ns, "--metric-name", "L",
		"--comparison-operator", "GreaterThanThreshold", "--evaluation-periods", "1",
		"--period", "60", "--threshold", "1", "--statistic", "Average"))
	defer func() { _ = awsCLI("cloudwatch", "delete-alarms", "--alarm-names", "cli-reverse").Run() }()

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms-for-metric",
		"--namespace", ns, "--metric-name", "L",
		"--query", "MetricAlarms[0].AlarmName", "--output", "text")))
	if got != "cli-reverse" {
		t.Fatalf("describe-alarms-for-metric = %q, want cli-reverse", got)
	}
}

// TestCloudWatchCLI_CompositeAlarm covers put-composite-alarm and its appearance
// in describe-alarms --alarm-types CompositeAlarm.
func TestCloudWatchCLI_CompositeAlarm(t *testing.T) {
	name := "cli-composite"
	runCLI(t, awsCLI("cloudwatch", "put-composite-alarm",
		"--alarm-name", name, "--alarm-rule", "ALARM(a) OR ALARM(b)"))
	defer func() { _ = awsCLI("cloudwatch", "delete-alarms", "--alarm-names", name).Run() }()

	got := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-alarms",
		"--alarm-names", name, "--alarm-types", "CompositeAlarm",
		"--query", "CompositeAlarms[0].AlarmRule", "--output", "text")))
	if got != "ALARM(a) OR ALARM(b)" {
		t.Fatalf("composite AlarmRule = %q", got)
	}
}

// TestCloudWatchCLI_MetricStreams covers the stream lifecycle over the CLI.
func TestCloudWatchCLI_MetricStreams(t *testing.T) {
	name := "cli-stream"
	firehoseARN, roleARN := createCLICloudWatchMetricStreamDestination(t, "lifecycle")
	runCLI(t, awsCLI("cloudwatch", "put-metric-stream",
		"--name", name,
		"--firehose-arn", firehoseARN,
		"--role-arn", roleARN,
		"--output-format", "json",
		"--include-filters", `[{"Namespace":"AWS/EC2"}]`))
	defer func() { _ = awsCLI("cloudwatch", "delete-metric-stream", "--name", name).Run() }()

	state := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-stream", "--name", name,
		"--query", "State", "--output", "text")))
	if state != "running" {
		t.Fatalf("get-metric-stream State = %q, want running", state)
	}

	runCLI(t, awsCLI("cloudwatch", "stop-metric-streams", "--names", name))
	state = strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-stream", "--name", name,
		"--query", "State", "--output", "text")))
	if state != "stopped" {
		t.Fatalf("after stop, State = %q, want stopped", state)
	}
	runCLI(t, awsCLI("cloudwatch", "start-metric-streams", "--names", name))

	listed := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "list-metric-streams",
		"--query", "length(Entries[?Name=='"+name+"'])", "--output", "text")))
	if listed != "1" {
		t.Fatalf("list-metric-streams found %q entries for %s, want 1", listed, name)
	}
}

// TestCloudWatchCLI_AnomalyDetector covers put / describe / delete.
func TestCloudWatchCLI_AnomalyDetector(t *testing.T) {
	ns := "Custom/AnomalyCLI"
	runCLI(t, awsCLI("cloudwatch", "put-anomaly-detector",
		"--single-metric-anomaly-detector",
		`{"Namespace":"`+ns+`","MetricName":"CPU","Stat":"Average"}`))

	st := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-anomaly-detectors",
		"--namespace", ns, "--metric-name", "CPU",
		"--query", "AnomalyDetectors[0].StateValue", "--output", "text")))
	if st != "PENDING_TRAINING" {
		t.Fatalf("anomaly detector StateValue = %q, want PENDING_TRAINING", st)
	}

	runCLI(t, awsCLI("cloudwatch", "delete-anomaly-detector",
		"--single-metric-anomaly-detector",
		`{"Namespace":"`+ns+`","MetricName":"CPU","Stat":"Average"}`))
	count := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-anomaly-detectors",
		"--namespace", ns, "--metric-name", "CPU",
		"--query", "length(AnomalyDetectors)", "--output", "text")))
	if count != "0" {
		t.Fatalf("after delete, AnomalyDetectors length = %q, want 0", count)
	}
}

// TestCloudWatchCLI_InsightRules covers put / describe / disable / enable / delete.
func TestCloudWatchCLI_InsightRules(t *testing.T) {
	name := "cli-insight"
	def := `{"Schema":{"Name":"CloudWatchLogRule","Version":1},"LogGroupNames":["/aws/lambda/x"],"Contribution":{"Keys":["$.id"]},"AggregateOn":"Count"}`
	runCLI(t, awsCLI("cloudwatch", "put-insight-rule", "--rule-name", name, "--rule-definition", def))
	defer func() { _ = awsCLI("cloudwatch", "delete-insight-rules", "--rule-names", name).Run() }()

	state := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-insight-rules",
		"--query", "InsightRules[?Name=='"+name+"'].State | [0]", "--output", "text")))
	if state != "ENABLED" {
		t.Fatalf("insight rule State = %q, want ENABLED", state)
	}

	runCLI(t, awsCLI("cloudwatch", "disable-insight-rules", "--rule-names", name))
	state = strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "describe-insight-rules",
		"--query", "InsightRules[?Name=='"+name+"'].State | [0]", "--output", "text")))
	if state != "DISABLED" {
		t.Fatalf("after disable, State = %q, want DISABLED", state)
	}
	runCLI(t, awsCLI("cloudwatch", "enable-insight-rules", "--rule-names", name))
}

// TestCloudWatchCLI_GetMetricData verifies get-metric-data evaluates the queries
// against pushed metrics (real datapoints; empty for a metric with no data).
func TestCloudWatchCLI_GetMetricData(t *testing.T) {
	ns := "Custom/GetMetricDataCLI"
	runCLI(t, awsCLI("cloudwatch", "put-metric-data", "--namespace", ns,
		"--metric-data", `[{"MetricName":"Hits","Value":3},{"MetricName":"Hits","Value":5}]`))

	q := `[{"Id":"q1","MetricStat":{"Metric":{"Namespace":"` + ns + `","MetricName":"Hits"},"Period":60,"Stat":"Sum"}}]`
	got := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-data",
		"--metric-data-queries", q,
		"--start-time", "2000-01-01T00:00:00Z", "--end-time", "2100-01-01T00:00:00Z",
		"--query", "MetricDataResults[0].Values[0]", "--output", "text")))
	if got != "8.0" && got != "8" {
		t.Fatalf("get-metric-data Sum = %q, want 8", got)
	}

	// A metric with no data returns an empty Values list — never a fabricated point.
	qEmpty := `[{"Id":"q2","MetricStat":{"Metric":{"Namespace":"` + ns + `","MetricName":"NoData"},"Period":60,"Stat":"Sum"}}]`
	count := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-data",
		"--metric-data-queries", qEmpty,
		"--start-time", "2000-01-01T00:00:00Z", "--end-time", "2100-01-01T00:00:00Z",
		"--query", "length(MetricDataResults[0].Values)", "--output", "text")))
	if count != "0" {
		t.Fatalf("get-metric-data for empty metric Values length = %q, want 0", count)
	}
}

// TestCloudWatchCLI_AlarmMuteRules covers put / get / list / delete.
func TestCloudWatchCLI_AlarmMuteRules(t *testing.T) {
	name := "cli-mute"
	runCLI(t, awsCLI("cloudwatch", "put-alarm-mute-rule",
		"--name", name, "--description", "maint",
		"--rule", `{"Schedule":{"Expression":"cron(0 2 * * ? *)","Duration":"PT1H"}}`,
		"--mute-targets", `{"AlarmNames":["target-alarm"]}`))
	defer func() { _ = awsCLI("cloudwatch", "delete-alarm-mute-rule", "--alarm-mute-rule-name", name).Run() }()

	expr := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-alarm-mute-rule",
		"--alarm-mute-rule-name", name,
		"--query", "Rule.Schedule.Expression", "--output", "text")))
	if expr != "cron(0 2 * * ? *)" {
		t.Fatalf("get-alarm-mute-rule Schedule.Expression = %q", expr)
	}
	status := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-alarm-mute-rule",
		"--alarm-mute-rule-name", name, "--query", "Status", "--output", "text")))
	if status != "SCHEDULED" {
		t.Fatalf("mute rule Status = %q, want SCHEDULED", status)
	}

	listed := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "list-alarm-mute-rules",
		"--alarm-name", "target-alarm",
		"--query", "length(AlarmMuteRuleSummaries)", "--output", "text")))
	if listed != "1" {
		t.Fatalf("list-alarm-mute-rules length = %q, want 1", listed)
	}

	runCLI(t, awsCLI("cloudwatch", "delete-alarm-mute-rule", "--alarm-mute-rule-name", name))
	gone := awsCLI("cloudwatch", "get-alarm-mute-rule", "--alarm-mute-rule-name", name).Run()
	if gone == nil {
		t.Fatalf("get-alarm-mute-rule should fail after delete")
	}
}

// TestCloudWatchCLI_TagResource covers the cross-resource tagging API over a
// metric-stream ARN.
func TestCloudWatchCLI_TagResource(t *testing.T) {
	name := "cli-stream-tagged"
	firehoseARN, roleARN := createCLICloudWatchMetricStreamDestination(t, "tags")
	runCLI(t, awsCLI("cloudwatch", "put-metric-stream",
		"--name", name,
		"--firehose-arn", firehoseARN,
		"--role-arn", roleARN,
		"--output-format", "json"))
	defer func() { _ = awsCLI("cloudwatch", "delete-metric-stream", "--name", name).Run() }()
	arn := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-stream", "--name", name,
		"--query", "Arn", "--output", "text")))

	runCLI(t, awsCLI("cloudwatch", "tag-resource", "--resource-arn", arn,
		"--tags", "Key=env,Value=prod", "Key=owner,Value=a"))
	count := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "list-tags-for-resource",
		"--resource-arn", arn, "--query", "length(Tags)", "--output", "text")))
	if count != "2" {
		t.Fatalf("after tagging, Tags length = %q, want 2", count)
	}

	runCLI(t, awsCLI("cloudwatch", "untag-resource", "--resource-arn", arn, "--tag-keys", "owner"))
	remaining := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "list-tags-for-resource",
		"--resource-arn", arn, "--query", "Tags[0].Key", "--output", "text")))
	if remaining != "env" {
		t.Fatalf("after untag, remaining tag key = %q, want env", remaining)
	}
}
