package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setQueuePolicyAllowingSNSCLI grants sns.amazonaws.com sqs:SendMessage scoped
// to the topic, which Amazon SNS delivery to the queue requires.
func setQueuePolicyAllowingSNSCLI(t *testing.T, queueURL, queueARN, topicARN string) {
	t.Helper()
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":%q,"Condition":{"ArnEquals":{"aws:SourceArn":%q}}}]}`, queueARN, topicARN)
	attrs, err := json.Marshal(map[string]string{"Policy": policy})
	require.NoError(t, err)
	runCLI(t, awsCLI("sqs", "set-queue-attributes",
		"--queue-url", queueURL,
		"--attributes", string(attrs)))
}

func TestSQSCLI_QueueLifecycle(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-sqs-q"))
	var created struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.QueueUrl)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", created.QueueUrl).Run()
	})

	out = runCLI(t, awsCLI("sqs", "get-queue-url", "--queue-name", "cli-sqs-q"))
	var located struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &located)
	assert.Equal(t, created.QueueUrl, located.QueueUrl)

	runCLI(t, awsCLI("sqs", "send-message",
		"--queue-url", created.QueueUrl,
		"--message-body", "hello direct sqs cli"))

	out = runCLI(t, awsCLI("sqs", "receive-message",
		"--queue-url", created.QueueUrl,
		"--max-number-of-messages", "1"))
	var recv struct {
		Messages []struct {
			Body          string `json:"Body"`
			ReceiptHandle string `json:"ReceiptHandle"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 1)
	assert.Equal(t, "hello direct sqs cli", recv.Messages[0].Body)

	runCLI(t, awsCLI("sqs", "delete-message",
		"--queue-url", created.QueueUrl,
		"--receipt-handle", recv.Messages[0].ReceiptHandle))
}

func TestSNSCLI_TopicSQSFanout(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-sns-topic"))
	var topic struct {
		TopicArn string `json:"TopicArn"`
	}
	parseJSON(t, out, &topic)
	require.NotEmpty(t, topic.TopicArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	out = runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-sns-q"))
	var queue struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &queue)
	require.NotEmpty(t, queue.QueueUrl)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", queue.QueueUrl).Run()
	})

	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", queue.QueueUrl,
		"--attribute-names", "QueueArn"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	setQueuePolicyAllowingSNSCLI(t, queue.QueueUrl, queueARN, topic.TopicArn)

	out = runCLI(t, awsCLI("sns", "subscribe",
		"--topic-arn", topic.TopicArn,
		"--protocol", "sqs",
		"--notification-endpoint", queueARN))
	var sub struct {
		SubscriptionArn string `json:"SubscriptionArn"`
	}
	parseJSON(t, out, &sub)
	require.NotEmpty(t, sub.SubscriptionArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "unsubscribe", "--subscription-arn", sub.SubscriptionArn).Run()
	})

	runCLI(t, awsCLI("sns", "publish",
		"--topic-arn", topic.TopicArn,
		"--message", `{"source":"sns-cli"}`))

	out = runCLI(t, awsCLI("sqs", "receive-message",
		"--queue-url", queue.QueueUrl,
		"--max-number-of-messages", "1"))
	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 1)
	assert.Contains(t, recv.Messages[0].Body, `"Message":"{\"source\":\"sns-cli\"}"`)
}

// TestSQSCLI_FifoCouplingAndGroupId expects create-queue to couple the .fifo
// name with FifoQueue and send-message to require MessageGroupId.
func TestSQSCLI_FifoCouplingAndGroupId(t *testing.T) {
	t.Parallel()
	errOut := runCLIExpectError(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-bad.fifo"))
	assert.Contains(t, errOut, "InvalidParameterValue")

	out := runCLI(t, awsCLI("sqs", "create-queue",
		"--queue-name", "cli-fifo.fifo",
		"--attributes", "FifoQueue=true,ContentBasedDeduplication=true"))
	var created struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.QueueUrl)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", created.QueueUrl).Run()
	})

	errOut = runCLIExpectError(t, awsCLI("sqs", "send-message",
		"--queue-url", created.QueueUrl,
		"--message-body", "nogroup"))
	assert.Contains(t, errOut, "MissingParameter")

	// The same content twice within the deduplication window returns the original
	// identifiers and enqueues one message.
	firstOut := runCLI(t, awsCLI("sqs", "send-message",
		"--queue-url", created.QueueUrl,
		"--message-body", "withgroup",
		"--message-group-id", "g1"))
	secondOut := runCLI(t, awsCLI("sqs", "send-message",
		"--queue-url", created.QueueUrl,
		"--message-body", "withgroup",
		"--message-group-id", "g1"))
	var first, second struct {
		MessageID      string `json:"MessageId"`
		SequenceNumber string `json:"SequenceNumber"`
	}
	parseJSON(t, firstOut, &first)
	parseJSON(t, secondOut, &second)
	assert.Equal(t, first.MessageID, second.MessageID)
	assert.Equal(t, first.SequenceNumber, second.SequenceNumber)
	require.NotEmpty(t, first.SequenceNumber)

	receivedOut := runCLI(t, awsCLI("sqs", "receive-message",
		"--queue-url", created.QueueUrl,
		"--max-number-of-messages", "10",
		"--message-system-attribute-names", "All"))
	var received struct {
		Messages []struct {
			Body       string            `json:"Body"`
			Attributes map[string]string `json:"Attributes"`
		} `json:"Messages"`
	}
	parseJSON(t, receivedOut, &received)
	require.Len(t, received.Messages, 1)
	assert.Equal(t, "withgroup", received.Messages[0].Body)
	assert.Equal(t, first.SequenceNumber, received.Messages[0].Attributes["SequenceNumber"])
}

func TestSQSCLI_SendMessageBatch(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-batch-q"))
	var created struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &created)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", created.QueueUrl).Run()
	})

	out = runCLI(t, awsCLI("sqs", "send-message-batch",
		"--queue-url", created.QueueUrl,
		"--entries",
		`[{"Id":"a","MessageBody":"body-a"},{"Id":"b","MessageBody":"body-b"}]`))
	var batch struct {
		Successful []struct {
			Id        string `json:"Id"`
			MessageId string `json:"MessageId"`
		} `json:"Successful"`
		Failed []struct {
			Id   string `json:"Id"`
			Code string `json:"Code"`
		} `json:"Failed"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.Successful, 2)
	assert.Empty(t, batch.Failed)

	out = runCLI(t, awsCLI("sqs", "receive-message",
		"--queue-url", created.QueueUrl,
		"--max-number-of-messages", "10"))
	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 2)

	errOut := runCLIExpectError(t, awsCLI("sqs", "send-message-batch",
		"--queue-url", created.QueueUrl,
		"--entries",
		`[{"Id":"dup","MessageBody":"1"},{"Id":"dup","MessageBody":"2"}]`))
	assert.Contains(t, errOut, "BatchEntryIdsNotDistinct")
}

// TestSQSCLI_ChangeMessageVisibility expects a zero visibility timeout to
// return an in-flight message to the visible pool, and a bogus receipt handle
// to be rejected.
func TestSQSCLI_ChangeMessageVisibility(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-cmv-q"))
	var created struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &created)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", created.QueueUrl).Run()
	})

	runCLI(t, awsCLI("sqs", "send-message",
		"--queue-url", created.QueueUrl, "--message-body", "cmv-cli"))

	out = runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", created.QueueUrl))
	var recv struct {
		Messages []struct {
			ReceiptHandle string `json:"ReceiptHandle"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 1)

	runCLI(t, awsCLI("sqs", "change-message-visibility",
		"--queue-url", created.QueueUrl,
		"--receipt-handle", recv.Messages[0].ReceiptHandle,
		"--visibility-timeout", "0"))

	out = runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", created.QueueUrl))
	var recv2 struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv2)
	require.Len(t, recv2.Messages, 1, "message should be visible after visibility reset to 0")

	errOut := runCLIExpectError(t, awsCLI("sqs", "change-message-visibility",
		"--queue-url", created.QueueUrl,
		"--receipt-handle", "not-a-handle",
		"--visibility-timeout", "10"))
	assert.Contains(t, errOut, "ReceiptHandleIsInvalid")
}

func TestSQSCLI_DeleteMessageBatch(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-dmb-q"))
	var created struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &created)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", created.QueueUrl).Run()
	})

	for _, b := range []string{"db1", "db2"} {
		runCLI(t, awsCLI("sqs", "send-message", "--queue-url", created.QueueUrl, "--message-body", b))
	}
	out = runCLI(t, awsCLI("sqs", "receive-message",
		"--queue-url", created.QueueUrl, "--max-number-of-messages", "10"))
	var recv struct {
		Messages []struct {
			ReceiptHandle string `json:"ReceiptHandle"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 2)

	entries, err := json.Marshal([]map[string]string{
		{"Id": "e0", "ReceiptHandle": recv.Messages[0].ReceiptHandle},
		{"Id": "e1", "ReceiptHandle": recv.Messages[1].ReceiptHandle},
	})
	require.NoError(t, err)
	out = runCLI(t, awsCLI("sqs", "delete-message-batch",
		"--queue-url", created.QueueUrl, "--entries", string(entries)))
	var batch struct {
		Successful []struct {
			Id string `json:"Id"`
		} `json:"Successful"`
		Failed []any `json:"Failed"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.Successful, 2)
	assert.Empty(t, batch.Failed)

	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", created.QueueUrl, "--attribute-names", "ApproximateNumberOfMessagesNotVisible"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	assert.Equal(t, "0", attrs.Attributes["ApproximateNumberOfMessagesNotVisible"])
}

// TestSQSCLI_AddRemovePermission expects add-permission to write a labelled
// statement into the Policy attribute and remove-permission to strip it.
func TestSQSCLI_AddRemovePermission(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-perm-q"))
	var created struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &created)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", created.QueueUrl).Run()
	})

	runCLI(t, awsCLI("sqs", "add-permission",
		"--queue-url", created.QueueUrl,
		"--label", "cli-grant",
		"--aws-account-ids", "123456789012",
		"--actions", "SendMessage"))

	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", created.QueueUrl, "--attribute-names", "Policy"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	require.NotEmpty(t, attrs.Attributes["Policy"])
	assert.Contains(t, attrs.Attributes["Policy"], "cli-grant")
	assert.Contains(t, attrs.Attributes["Policy"], "arn:aws:iam::123456789012:root")

	runCLI(t, awsCLI("sqs", "remove-permission",
		"--queue-url", created.QueueUrl, "--label", "cli-grant"))

	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", created.QueueUrl, "--attribute-names", "Policy"))
	var attrs2 struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs2)
	assert.Empty(t, attrs2.Attributes["Policy"], "remove-permission must clear the last statement")
}

// TestSNSCLI_FifoCouplingAndGroupId expects create-topic to couple the .fifo
// name with FifoTopic and publish to require MessageGroupId.
func TestSNSCLI_FifoCouplingAndGroupId(t *testing.T) {
	t.Parallel()
	errOut := runCLIExpectError(t, awsCLI("sns", "create-topic", "--name", "cli-sns-bad.fifo"))
	assert.Contains(t, errOut, "InvalidParameter")

	out := runCLI(t, awsCLI("sns", "create-topic",
		"--name", "cli-sns-fifo.fifo",
		"--attributes", "FifoTopic=true,ContentBasedDeduplication=true"))
	var topic struct {
		TopicArn string `json:"TopicArn"`
	}
	parseJSON(t, out, &topic)
	require.NotEmpty(t, topic.TopicArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	errOut = runCLIExpectError(t, awsCLI("sns", "publish",
		"--topic-arn", topic.TopicArn,
		"--message", "nogroup"))
	assert.Contains(t, errOut, "InvalidParameter")

	runCLI(t, awsCLI("sns", "publish",
		"--topic-arn", topic.TopicArn,
		"--message", "withgroup",
		"--message-group-id", "g1"))
}

func TestSNSCLI_PublishBatch(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-pubbatch-t"))
	var topic struct {
		TopicArn string `json:"TopicArn"`
	}
	parseJSON(t, out, &topic)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	out = runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-pubbatch-q"))
	var queue struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &queue)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", queue.QueueUrl).Run()
	})
	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", queue.QueueUrl,
		"--attribute-names", "QueueArn"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)
	setQueuePolicyAllowingSNSCLI(t, queue.QueueUrl, queueARN, topic.TopicArn)
	runCLI(t, awsCLI("sns", "subscribe",
		"--topic-arn", topic.TopicArn,
		"--protocol", "sqs",
		"--notification-endpoint", queueARN))

	out = runCLI(t, awsCLI("sns", "publish-batch",
		"--topic-arn", topic.TopicArn,
		"--publish-batch-request-entries",
		`[{"Id":"e1","Message":"msg-1"},{"Id":"e2","Message":"msg-2"}]`))
	var batch struct {
		Successful []struct {
			Id        string `json:"Id"`
			MessageId string `json:"MessageId"`
		} `json:"Successful"`
		Failed []any `json:"Failed"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.Successful, 2)
	assert.Empty(t, batch.Failed)

	out = runCLI(t, awsCLI("sqs", "receive-message",
		"--queue-url", queue.QueueUrl,
		"--max-number-of-messages", "10"))
	var recv struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, out, &recv)
	require.Len(t, recv.Messages, 2)
	assert.True(t,
		strings.Contains(recv.Messages[0].Body, `"Message":"msg-1"`) ||
			strings.Contains(recv.Messages[0].Body, `"Message":"msg-2"`))

	errOut := runCLIExpectError(t, awsCLI("sns", "publish-batch",
		"--topic-arn", topic.TopicArn,
		"--publish-batch-request-entries",
		`[{"Id":"dup","Message":"1"},{"Id":"dup","Message":"2"}]`))
	assert.Contains(t, errOut, "BatchEntryIdsNotDistinct")
}
