package aws_sdk_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqsRedrivePolicyJSON builds the RedrivePolicy attribute string SQS stores on a
// source queue to wire it to a dead-letter queue.
func sqsRedrivePolicyJSON(t *testing.T, dlqARN string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"deadLetterTargetArn": dlqARN,
		"maxReceiveCount":     "3",
	})
	require.NoError(t, err)
	return string(b)
}

// sqsQueueARNFor reads the QueueArn attribute the way a real DLQ wiring flow does.
func sqsQueueARNFor(t *testing.T, client *sqs.Client, url string) string {
	t.Helper()
	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	arn := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, arn)
	return arn
}

// TestSQS_DeadLetterSourceQueues asserts ListDeadLetterSourceQueues returns the
// source queues whose RedrivePolicy targets a given dead-letter queue.
func TestSQS_DeadLetterSourceQueues(t *testing.T) {
	client := sqsClient()

	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("dls-dlq")})
	require.NoError(t, err)
	dlqURL := aws.ToString(dlq.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := sqsQueueARNFor(t, client, dlqURL)

	src, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String("dls-src"),
		Attributes: map[string]string{"RedrivePolicy": sqsRedrivePolicyJSON(t, dlqARN)},
	})
	require.NoError(t, err)
	srcURL := aws.ToString(src.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: src.QueueUrl}) })

	// A queue with no RedrivePolicy must NOT be listed.
	other, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("dls-other")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: other.QueueUrl}) })

	list, err := client.ListDeadLetterSourceQueues(ctx, &sqs.ListDeadLetterSourceQueuesInput{
		QueueUrl: aws.String(dlqURL),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{srcURL}, list.QueueUrls)
}

// TestSQS_MessageMoveTask exercises the full DLQ-redrive lifecycle: a message
// landed in a dead-letter queue is redriven back to its source queue via a
// message-move task, then the task is listed and its already-settled state is
// reported. It also asserts that cancelling a COMPLETED task is rejected.
func TestSQS_MessageMoveTask(t *testing.T) {
	client := sqsClient()

	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("mmt-dlq")})
	require.NoError(t, err)
	dlqURL := aws.ToString(dlq.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := sqsQueueARNFor(t, client, dlqURL)

	src, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String("mmt-src"),
		Attributes: map[string]string{"RedrivePolicy": sqsRedrivePolicyJSON(t, dlqARN)},
	})
	require.NoError(t, err)
	srcURL := aws.ToString(src.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: src.QueueUrl}) })

	// Simulate a message that has landed in the DLQ (e.g. after exceeding
	// maxReceiveCount on the source queue).
	sent, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(dlqURL),
		MessageBody: aws.String("poisoned"),
	})
	require.NoError(t, err)

	// Start a move task with no DestinationArn — messages redrive back to the
	// original source queue derived from the RedrivePolicy.
	start, err := client.StartMessageMoveTask(ctx, &sqs.StartMessageMoveTaskInput{
		SourceArn: aws.String(dlqARN),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(start.TaskHandle))

	// The message is now receivable on the source queue.
	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(srcURL),
		MaxNumberOfMessages: 10,
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{
			sqstypes.MessageSystemAttributeNameAll,
		},
	})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1)
	assert.Equal(t, "poisoned", aws.ToString(recv.Messages[0].Body))
	assert.NotEqual(t, aws.ToString(sent.MessageId), aws.ToString(recv.Messages[0].MessageId),
		"Amazon SQS redrive assigns a new message ID")
	assert.NotEmpty(t, recv.Messages[0].Attributes["SentTimestamp"])

	// The DLQ is now empty.
	drained, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(dlqURL),
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	assert.Empty(t, drained.Messages)

	// ListMessageMoveTasks reports the settled task for the source ARN.
	tasks, err := client.ListMessageMoveTasks(ctx, &sqs.ListMessageMoveTasksInput{
		SourceArn: aws.String(dlqARN),
	})
	require.NoError(t, err)
	require.Len(t, tasks.Results, 1)
	assert.Equal(t, "COMPLETED", aws.ToString(tasks.Results[0].Status))
	assert.Equal(t, dlqARN, aws.ToString(tasks.Results[0].SourceArn))
	assert.Equal(t, int64(1), tasks.Results[0].ApproximateNumberOfMessagesMoved)

	// A COMPLETED task can't be cancelled.
	_, err = client.CancelMessageMoveTask(ctx, &sqs.CancelMessageMoveTaskInput{
		TaskHandle: start.TaskHandle,
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "ResourceNotFoundException", apiErr.ErrorCode())
}
