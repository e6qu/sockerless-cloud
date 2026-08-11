package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqsCLIQueueARN reads a queue's ARN via the CLI, the value needed to wire a
// RedrivePolicy or to address a queue in a message-move task.
func sqsCLIQueueARN(t *testing.T, queueURL string) string {
	t.Helper()
	out := runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", queueURL, "--attribute-names", "QueueArn"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	arn := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, arn)
	return arn
}

// TestSQSCLI_DeadLetterSourceQueues asserts `aws sqs list-dead-letter-source-queues`
// returns the source queues wired to a dead-letter queue via their RedrivePolicy.
func TestSQSCLI_DeadLetterSourceQueues(t *testing.T) {
	dlqOut := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-dls-dlq"))
	var dlq struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, dlqOut, &dlq)
	t.Cleanup(func() { _ = awsCLI("sqs", "delete-queue", "--queue-url", dlq.QueueUrl).Run() })
	dlqARN := sqsCLIQueueARN(t, dlq.QueueUrl)

	redrive, err := json.Marshal(map[string]string{"deadLetterTargetArn": dlqARN, "maxReceiveCount": "3"})
	require.NoError(t, err)
	attrs, err := json.Marshal(map[string]string{"RedrivePolicy": string(redrive)})
	require.NoError(t, err)

	srcOut := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-dls-src",
		"--attributes", string(attrs)))
	var src struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, srcOut, &src)
	t.Cleanup(func() { _ = awsCLI("sqs", "delete-queue", "--queue-url", src.QueueUrl).Run() })

	listOut := runCLI(t, awsCLI("sqs", "list-dead-letter-source-queues",
		"--queue-url", dlq.QueueUrl))
	var list struct {
		QueueUrls []string `json:"queueUrls"`
	}
	parseJSON(t, listOut, &list)
	assert.Equal(t, []string{src.QueueUrl}, list.QueueUrls)
}

// TestSQSCLI_MessageMoveTask exercises the DLQ-redrive task lifecycle via the
// CLI: a message in a dead-letter queue is redriven to its source queue with
// `start-message-move-task`, then listed with `list-message-move-tasks`, and a
// `cancel-message-move-task` against the settled task is rejected.
func TestSQSCLI_MessageMoveTask(t *testing.T) {
	dlqOut := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-mmt-dlq"))
	var dlq struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, dlqOut, &dlq)
	t.Cleanup(func() { _ = awsCLI("sqs", "delete-queue", "--queue-url", dlq.QueueUrl).Run() })
	dlqARN := sqsCLIQueueARN(t, dlq.QueueUrl)

	redrive, err := json.Marshal(map[string]string{"deadLetterTargetArn": dlqARN, "maxReceiveCount": "3"})
	require.NoError(t, err)
	attrs, err := json.Marshal(map[string]string{"RedrivePolicy": string(redrive)})
	require.NoError(t, err)
	srcOut := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-mmt-src",
		"--attributes", string(attrs)))
	var src struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, srcOut, &src)
	t.Cleanup(func() { _ = awsCLI("sqs", "delete-queue", "--queue-url", src.QueueUrl).Run() })

	sentOut := runCLI(t, awsCLI("sqs", "send-message", "--queue-url", dlq.QueueUrl,
		"--message-body", "poisoned"))
	var sent struct {
		MessageID string `json:"MessageId"`
	}
	parseJSON(t, sentOut, &sent)

	startOut := runCLI(t, awsCLI("sqs", "start-message-move-task", "--source-arn", dlqARN))
	var start struct {
		TaskHandle string `json:"TaskHandle"`
	}
	parseJSON(t, startOut, &start)
	require.NotEmpty(t, start.TaskHandle)

	// The redriven message is now on the source queue.
	recvOut := runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", src.QueueUrl,
		"--max-number-of-messages", "10", "--message-system-attribute-names", "All"))
	var recv struct {
		Messages []struct {
			Body       string            `json:"Body"`
			MessageID  string            `json:"MessageId"`
			Attributes map[string]string `json:"Attributes"`
		} `json:"Messages"`
	}
	parseJSON(t, recvOut, &recv)
	require.Len(t, recv.Messages, 1)
	assert.Equal(t, "poisoned", recv.Messages[0].Body)
	assert.NotEqual(t, sent.MessageID, recv.Messages[0].MessageID,
		"Amazon SQS redrive assigns a new message ID")
	assert.NotEmpty(t, recv.Messages[0].Attributes["SentTimestamp"])

	listOut := runCLI(t, awsCLI("sqs", "list-message-move-tasks", "--source-arn", dlqARN))
	var list struct {
		Results []struct {
			Status                           string `json:"Status"`
			SourceArn                        string `json:"SourceArn"`
			ApproximateNumberOfMessagesMoved int64  `json:"ApproximateNumberOfMessagesMoved"`
		} `json:"Results"`
	}
	parseJSON(t, listOut, &list)
	require.Len(t, list.Results, 1)
	assert.Equal(t, "COMPLETED", list.Results[0].Status)
	assert.Equal(t, dlqARN, list.Results[0].SourceArn)
	assert.Equal(t, int64(1), list.Results[0].ApproximateNumberOfMessagesMoved)

	// Cancelling a COMPLETED task is rejected.
	cancelErr := runCLIExpectError(t, awsCLI("sqs", "cancel-message-move-task",
		"--task-handle", start.TaskHandle))
	assert.Contains(t, cancelErr, "ResourceNotFoundException")
}
