package aws_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCloudWatchCLI_AlarmSNSActionToSQS_ProcessMode is the CLI regression test
// for issue #745. It starts a fresh simulator-aws subprocess with
// SIM_RUNTIME=process and exercises the full CloudWatch→SNS→SQS chain using the
// real AWS CLI: create topic, queue, subscription, alarm with AlarmActions,
// publish a breaching metric, wait for ALARM, and assert the SQS subscriber
// receives the canonical CloudWatch alarm notification.
func TestCloudWatchCLI_AlarmSNSActionToSQS_ProcessMode(t *testing.T) {
	url := startProcessModeSim(t)
	cli := func(args ...string) *exec.Cmd {
		cmd := exec.Command("aws", args...)
		cmd.Env = append(cmd.Env,
			"AWS_ENDPOINT_URL="+url,
			"AWS_ACCESS_KEY_ID=test",
			"AWS_SECRET_ACCESS_KEY=test",
			"AWS_DEFAULT_REGION=us-east-1",
			"AWS_PAGER=",
		)
		return cmd
	}

	alarmName := "cli-alarm-sns-sqs-process-745"
	ns := "Custom/CLIAlarmProcessRepro"

	topicARN := strings.TrimSpace(runCLI(t, cli("sns", "create-topic", "--name", "cli-repro-t", "--query", "TopicArn", "--output", "text")))
	if topicARN == "" {
		t.Fatal("failed to create SNS topic")
	}
	t.Cleanup(func() {
		_, _ = cli("sns", "delete-topic", "--topic-arn", topicARN).CombinedOutput()
	})

	queueURL := strings.TrimSpace(runCLI(t, cli("sqs", "create-queue", "--queue-name", "cli-repro-q", "--query", "QueueUrl", "--output", "text")))
	if queueURL == "" {
		t.Fatal("failed to create SQS queue")
	}
	t.Cleanup(func() {
		_, _ = cli("sqs", "delete-queue", "--queue-url", queueURL).CombinedOutput()
	})

	queueARN := strings.TrimSpace(runCLI(t, cli("sqs", "get-queue-attributes",
		"--queue-url", queueURL,
		"--attribute-names", "QueueArn",
		"--query", "Attributes.QueueArn",
		"--output", "text")))
	if queueARN == "" {
		t.Fatal("failed to get queue ARN")
	}

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	attrs := fmt.Sprintf(`{"Policy":%q}`, policy)
	runCLI(t, cli("sqs", "set-queue-attributes", "--queue-url", queueURL, "--attributes", attrs))

	runCLI(t, cli("sns", "subscribe",
		"--topic-arn", topicARN,
		"--protocol", "sqs",
		"--notification-endpoint", queueARN))

	// Settle window matching the original CLI probe: ensure the subscription
	// is recorded before the alarm fires.
	time.Sleep(3 * time.Second)

	runCLI(t, cli("cloudwatch", "put-metric-alarm",
		"--alarm-name", alarmName,
		"--namespace", ns,
		"--metric-name", "CPUUtilization",
		"--comparison-operator", "GreaterThanThreshold",
		"--evaluation-periods", "1",
		"--period", "60",
		"--threshold", "50",
		"--statistic", "Average",
		"--alarm-actions", topicARN))
	t.Cleanup(func() {
		_, _ = cli("cloudwatch", "delete-alarms", "--alarm-names", alarmName).CombinedOutput()
	})

	runCLI(t, cli("cloudwatch", "put-metric-data",
		"--namespace", ns,
		"--metric-data", `[{"MetricName":"CPUUtilization","Value":95,"Unit":"Percent"}]`))

	// Poll until DescribeAlarms surfaces ALARM.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state := strings.TrimSpace(runCLI(t, cli("cloudwatch", "describe-alarms",
			"--alarm-names", alarmName,
			"--query", "MetricAlarms[0].StateValue",
			"--output", "text")))
		if state == "ALARM" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Give the background evaluator time to dispatch the ALARM action.
	time.Sleep(2 * time.Second)

	out := runCLI(t, cli("sqs", "receive-message",
		"--queue-url", queueURL,
		"--output", "json"))

	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	if len(recv.Messages) != 1 {
		t.Fatalf("expected exactly one SQS message, got %d", len(recv.Messages))
	}

	var env struct {
		Type    string `json:"Type"`
		Message string `json:"Message"`
	}
	parseJSON(t, recv.Messages[0].Body, &env)
	if env.Type != "Notification" {
		t.Fatalf("expected SNS notification type Notification, got %q", env.Type)
	}

	var body struct {
		AlarmName     string `json:"AlarmName"`
		NewStateValue string `json:"NewStateValue"`
		OldStateValue string `json:"OldStateValue"`
		Region        string `json:"Region"`
	}
	parseJSON(t, env.Message, &body)
	if body.AlarmName != alarmName {
		t.Fatalf("expected AlarmName %q, got %q", alarmName, body.AlarmName)
	}
	if body.NewStateValue != "ALARM" {
		t.Fatalf("expected NewStateValue ALARM, got %q", body.NewStateValue)
	}
	if body.OldStateValue != "INSUFFICIENT_DATA" {
		t.Fatalf("expected OldStateValue INSUFFICIENT_DATA, got %q", body.OldStateValue)
	}
	if body.Region != "us-east-1" {
		t.Fatalf("expected Region us-east-1, got %q", body.Region)
	}
}

// TestCloudWatchCLI_AlarmSNSActionToSQS_ProcessMode_PollLoop is a follow-up
// regression for issue #766. The downstream probe polls receive-message once
// per second for 30 seconds after the alarm reaches ALARM; this test mirrors
// that polling pattern to ensure the message is always discoverable.
func TestCloudWatchCLI_AlarmSNSActionToSQS_ProcessMode_PollLoop(t *testing.T) {
	url := startProcessModeSim(t)
	cli := func(args ...string) *exec.Cmd {
		cmd := exec.Command("aws", args...)
		cmd.Env = append(cmd.Env,
			"AWS_ENDPOINT_URL="+url,
			"AWS_ACCESS_KEY_ID=test",
			"AWS_SECRET_ACCESS_KEY=test",
			"AWS_DEFAULT_REGION=us-east-1",
			"AWS_PAGER=",
		)
		return cmd
	}

	alarmName := "cli-alarm-sns-sqs-process-poll-loop"
	ns := "Custom/CLIAlarmProcessReproPollLoop"

	topicARN := strings.TrimSpace(runCLI(t, cli("sns", "create-topic", "--name", "cli-repro-poll-t", "--query", "TopicArn", "--output", "text")))
	if topicARN == "" {
		t.Fatal("failed to create SNS topic")
	}
	t.Cleanup(func() {
		_, _ = cli("sns", "delete-topic", "--topic-arn", topicARN).CombinedOutput()
	})

	queueURL := strings.TrimSpace(runCLI(t, cli("sqs", "create-queue", "--queue-name", "cli-repro-poll-q", "--query", "QueueUrl", "--output", "text")))
	if queueURL == "" {
		t.Fatal("failed to create SQS queue")
	}
	t.Cleanup(func() {
		_, _ = cli("sqs", "delete-queue", "--queue-url", queueURL).CombinedOutput()
	})

	queueARN := strings.TrimSpace(runCLI(t, cli("sqs", "get-queue-attributes",
		"--queue-url", queueURL,
		"--attribute-names", "QueueArn",
		"--query", "Attributes.QueueArn",
		"--output", "text")))
	if queueARN == "" {
		t.Fatal("failed to get queue ARN")
	}

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"}]}`, queueARN)
	attrs := fmt.Sprintf(`{"Policy":%q}`, policy)
	runCLI(t, cli("sqs", "set-queue-attributes", "--queue-url", queueURL, "--attributes", attrs))

	runCLI(t, cli("sns", "subscribe",
		"--topic-arn", topicARN,
		"--protocol", "sqs",
		"--notification-endpoint", queueARN))

	time.Sleep(3 * time.Second)

	runCLI(t, cli("cloudwatch", "put-metric-alarm",
		"--alarm-name", alarmName,
		"--namespace", ns,
		"--metric-name", "CPUUtilization",
		"--comparison-operator", "GreaterThanThreshold",
		"--evaluation-periods", "1",
		"--period", "60",
		"--threshold", "50",
		"--statistic", "Average",
		"--alarm-actions", topicARN))
	t.Cleanup(func() {
		_, _ = cli("cloudwatch", "delete-alarms", "--alarm-names", alarmName).CombinedOutput()
	})

	runCLI(t, cli("cloudwatch", "put-metric-data",
		"--namespace", ns,
		"--metric-data", `[{"MetricName":"CPUUtilization","Value":95,"Unit":"Percent"}]`))

	// Wait for the alarm to transition, then poll repeatedly (no up-front
	// sleep) exactly like the downstream adversarial probe does.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state := strings.TrimSpace(runCLI(t, cli("cloudwatch", "describe-alarms",
			"--alarm-names", alarmName,
			"--query", "MetricAlarms[0].StateValue",
			"--output", "text")))
		if state == "ALARM" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	pollDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(pollDeadline) {
		out := runCLI(t, cli("sqs", "receive-message",
			"--queue-url", queueURL,
			"--output", "json"))
		parseJSON(t, out, &recv)
		if len(recv.Messages) > 0 {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if len(recv.Messages) != 1 {
		t.Fatalf("expected exactly one SQS message after polling, got %d", len(recv.Messages))
	}

	var env struct {
		Type    string `json:"Type"`
		Message string `json:"Message"`
	}
	parseJSON(t, recv.Messages[0].Body, &env)
	if env.Type != "Notification" {
		t.Fatalf("expected SNS notification type Notification, got %q", env.Type)
	}

	var body struct {
		AlarmName     string `json:"AlarmName"`
		NewStateValue string `json:"NewStateValue"`
	}
	parseJSON(t, env.Message, &body)
	if body.AlarmName != alarmName {
		t.Fatalf("expected AlarmName %q, got %q", alarmName, body.AlarmName)
	}
	if body.NewStateValue != "ALARM" {
		t.Fatalf("expected NewStateValue ALARM, got %q", body.NewStateValue)
	}
}
