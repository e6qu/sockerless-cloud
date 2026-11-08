package aws_cli_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
)

// sfnExecutionWatch receives a state machine's "Step Functions Execution
// Status Change" events, which Step Functions sends to the default event bus
// so a client need not call DescribeExecution repeatedly
// (sfn-eventbridge-integration.md.gz), through a rule that targets a queue.
type sfnExecutionWatch struct {
	queueURL string
	seen     map[string]string
}

func watchSFNExecutionsCLI(t *testing.T, stateMachineArn string) *sfnExecutionWatch {
	t.Helper()
	name := uniquename.New("sfn-cli-status")
	var q struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", name)), &q)
	t.Cleanup(func() { _ = awsCLI("sqs", "delete-queue", "--queue-url", q.QueueUrl).Run() })
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, runCLI(t, awsCLI("sqs", "get-queue-attributes", "--queue-url", q.QueueUrl, "--attribute-names", "QueueArn")), &attrs)
	queueARN := attrs.Attributes["QueueArn"]
	pattern, err := json.Marshal(map[string]any{
		"source":      []string{"aws.states"},
		"detail-type": []string{"Step Functions Execution Status Change"},
		"detail":      map[string]any{"stateMachineArn": []string{stateMachineArn}},
	})
	require.NoError(t, err)
	var rule struct {
		RuleArn string `json:"RuleArn"`
	}
	parseJSON(t, runCLI(t, awsCLI("events", "put-rule", "--name", name, "--event-pattern", string(pattern))), &rule)
	t.Cleanup(func() {
		_ = awsCLI("events", "remove-targets", "--rule", name, "--ids", "queue").Run()
		_ = awsCLI("events", "delete-rule", "--name", name).Run()
	})
	runCLI(t, awsCLI("events", "put-targets", "--rule", name, "--targets", `[{"Id":"queue","Arn":"`+queueARN+`"}]`))
	setEBQueuePolicyCLI(t, q.QueueUrl, queueARN, rule.RuleArn)
	return &sfnExecutionWatch{queueURL: q.QueueUrl, seen: map[string]string{}}
}

// await long-polls the queue until an event reports the execution in one of
// statuses, and returns that status.
func (w *sfnExecutionWatch) await(t *testing.T, executionArn string, statuses ...string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		for _, status := range statuses {
			if w.seen[executionArn] == status {
				return status
			}
		}
		require.True(t, time.Now().Before(deadline), "no event reported %s as %v; last seen %q", executionArn, statuses, w.seen[executionArn])
		var received struct {
			Messages []struct {
				Body          string `json:"Body"`
				ReceiptHandle string `json:"ReceiptHandle"`
			} `json:"Messages"`
		}
		out := runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", w.queueURL,
			"--max-number-of-messages", "10", "--wait-time-seconds", "20"))
		if out == "" {
			continue
		}
		parseJSON(t, out, &received)
		for _, message := range received.Messages {
			var event struct {
				Detail struct {
					ExecutionArn string `json:"executionArn"`
					Status       string `json:"status"`
				} `json:"detail"`
			}
			parseJSON(t, message.Body, &event)
			if event.Detail.Status != "RUNNING" || w.seen[event.Detail.ExecutionArn] == "" {
				w.seen[event.Detail.ExecutionArn] = event.Detail.Status
			}
			runCLI(t, awsCLI("sqs", "delete-message", "--queue-url", w.queueURL, "--receipt-handle", message.ReceiptHandle))
		}
	}
}
