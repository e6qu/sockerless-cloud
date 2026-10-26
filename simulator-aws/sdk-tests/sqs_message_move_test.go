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

// sqsRedrivePolicyJSON builds the RedrivePolicy that ties a source queue to a
// dead-letter queue.
func sqsRedrivePolicyJSON(t *testing.T, dlqARN string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"deadLetterTargetArn": dlqARN,
		"maxReceiveCount":     "3",
	})
	require.NoError(t, err)
	return string(b)
}

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

func TestSQS_DeadLetterSourceQueues(t *testing.T) {
	t.Parallel()
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

	// A queue without a RedrivePolicy is not listed.
	other, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("dls-other")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: other.QueueUrl}) })

	list, err := client.ListDeadLetterSourceQueues(ctx, &sqs.ListDeadLetterSourceQueuesInput{
		QueueUrl: aws.String(dlqURL),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{srcURL}, list.QueueUrls)
}

// TestSQS_MessageMoveTask expects a message-move task to return a message from
// the dead-letter queue to its source, the listing to report the settled task,
// and a COMPLETED task to refuse cancellation.
func TestSQS_MessageMoveTask(t *testing.T) {
	t.Parallel()
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

	// A message on the dead-letter queue, as after maxReceiveCount receives.
	sent, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(dlqURL),
		MessageBody: aws.String("poisoned"),
	})
	require.NoError(t, err)

	// Without a DestinationArn, messages return to the source queue the
	// RedrivePolicy names.
	start, err := client.StartMessageMoveTask(ctx, &sqs.StartMessageMoveTaskInput{
		SourceArn: aws.String(dlqARN),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(start.TaskHandle))

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

	drained, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(dlqURL),
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	assert.Empty(t, drained.Messages)

	tasks, err := client.ListMessageMoveTasks(ctx, &sqs.ListMessageMoveTasksInput{
		SourceArn: aws.String(dlqARN),
	})
	require.NoError(t, err)
	require.Len(t, tasks.Results, 1)
	assert.Equal(t, "COMPLETED", aws.ToString(tasks.Results[0].Status))
	assert.Equal(t, dlqARN, aws.ToString(tasks.Results[0].SourceArn))
	assert.Equal(t, int64(1), tasks.Results[0].ApproximateNumberOfMessagesMoved)

	_, err = client.CancelMessageMoveTask(ctx, &sqs.CancelMessageMoveTaskInput{
		TaskHandle: start.TaskHandle,
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "ResourceNotFoundException", apiErr.ErrorCode())
}
