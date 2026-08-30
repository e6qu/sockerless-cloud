package aws_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCloudWatchCLI_AlarmSNSActionToSQS_ProcessMode exercises the full
// CloudWatch→SNS→SQS chain through the real AWS CLI against a fresh
// simulator-aws subprocess in SIM_RUNTIME=process: create topic, queue,
// subscription and an alarm with AlarmActions, publish a breaching metric,
// wait for ALARM, and assert the SQS subscriber receives the canonical
// CloudWatch alarm notification.
//
// The notification is collected by polling receive-message, which is how a
// downstream consumer reads one: an empty receive before the alarm dispatches
// must not swallow the message that arrives after it.
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

	awaitCLISubscription(t, cli, queueARN, topicARN)

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
		time.Sleep(50 * time.Millisecond)
	}

	messageBody := awaitCLIQueueMessage(t, cli, queueURL)

	var env struct {
		Type    string `json:"Type"`
		Message string `json:"Message"`
	}
	parseJSON(t, messageBody, &env)
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

// awaitCLISubscription waits until the topic reports the subscription just
// made, so the alarm created next fires against a topic that can already
// deliver. Waiting for the state the test depends on beats sleeping a fixed
// window, which costs its full length every run and can still be too short on
// a loaded machine.
func awaitCLISubscription(t *testing.T, cli func(...string) *exec.Cmd, queueARN, topicARN string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out := runCLI(t, cli("sns", "list-subscriptions-by-topic",
			"--topic-arn", topicARN, "--output", "json"))
		if strings.Contains(out, queueARN) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the topic never reported its subscription to %s", queueARN)
}

// awaitCLIQueueMessage waits for the message an alarm action delivers and
// returns its body.
func awaitCLIQueueMessage(t *testing.T, cli func(...string) *exec.Cmd, queueURL string) string {
	t.Helper()
	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out := runCLI(t, cli("sqs", "receive-message",
			"--queue-url", queueURL, "--output", "json"))
		parseJSON(t, out, &recv)
		if len(recv.Messages) == 1 {
			return recv.Messages[0].Body
		}
		if len(recv.Messages) > 1 {
			t.Fatalf("expected one SQS message, got %d", len(recv.Messages))
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the queue never received the alarm notification")
	return ""
}
