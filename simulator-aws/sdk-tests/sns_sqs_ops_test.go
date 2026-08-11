package aws_sdk_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSNS_SetTopicAttributes_RoundTrip locks the SNS fix:
// SetTopicAttributes must accept the AttributeName/AttributeValue
// pair, persist it, and subsequent GetTopicAttributes must return
// it. Pre-fix the action returned InvalidAction and tf-provider-aws
// failed to set DeliveryPolicy / Policy on aws_sns_topic.
func TestSNS_SetTopicAttributes_RoundTrip(t *testing.T) {
	c := snsClient()
	ctx := context.Background()

	created, err := c.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String("set-attrs-topic"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: created.TopicArn})
	})

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:Publish"}]}`
	_, err = c.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{
		TopicArn:       created.TopicArn,
		AttributeName:  aws.String("Policy"),
		AttributeValue: aws.String(policy),
	})
	require.NoError(t, err, "SetTopicAttributes must succeed (regression guard)")

	got, err := c.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: created.TopicArn})
	require.NoError(t, err)
	assert.Equal(t, policy, got.Attributes["Policy"],
		"GetTopicAttributes must return the previously-set Policy verbatim")
}

// TestSQS_PurgeQueue_RemovesMessages locks the SQS fix:
// PurgeQueue must delete every queued message without removing the
// queue itself. Pre-fix the action returned UnknownOperationException
// and tests had to delete + recreate the queue between runs.
func TestSQS_PurgeQueue_RemovesMessages(t *testing.T) {
	c := sqsClient()
	ctx := context.Background()

	createOut, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("purge-test-queue"),
	})
	require.NoError(t, err)
	queueURL := createOut.QueueUrl
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queueURL})
	})

	// Send 3 messages.
	for _, body := range []string{"a", "b", "c"} {
		_, err := c.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl:    queueURL,
			MessageBody: aws.String(body),
		})
		require.NoError(t, err)
	}

	// Purge.
	_, err = c.PurgeQueue(ctx, &sqs.PurgeQueueInput{QueueUrl: queueURL})
	require.NoError(t, err, "PurgeQueue must succeed (regression guard)")

	// Receive returns nothing (queue is empty + queue still exists).
	recv, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            queueURL,
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err, "ReceiveMessage must succeed (queue still exists after Purge)")
	assert.Empty(t, recv.Messages, "Purge must remove every message")
}
