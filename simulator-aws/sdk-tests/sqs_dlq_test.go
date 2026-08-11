package aws_sdk_test

import (
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQS_DLQ_RedriveOnMaxReceiveCount asserts that a message on a source queue
// configured with a RedrivePolicy is delivered to callers up to maxReceiveCount
// times and then silently redriven to the dead-letter queue on the next receive,
// matching real SQS behavior. Visibility timeouts are reset to zero between
// receives so the message reappears for each pick-up within the test.
func TestSQS_DLQ_RedriveOnMaxReceiveCount(t *testing.T) {
	client := sqsClient()

	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("dlq-redrive-dlq")})
	require.NoError(t, err)
	dlqURL := aws.ToString(dlq.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := sqsQueueARNFor(t, client, dlqURL)

	src, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("dlq-redrive-src"),
		Attributes: map[string]string{
			"VisibilityTimeout": "0",
			"RedrivePolicy":     sqsRedrivePolicyJSONCount(t, dlqARN, 2),
		},
	})
	require.NoError(t, err)
	srcURL := aws.ToString(src.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: src.QueueUrl}) })

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(srcURL),
		MessageBody: aws.String("poison"),
	})
	require.NoError(t, err)

	// First two receives return the message; the third would push its
	// ApproximateReceiveCount past maxReceiveCount, so it is redriven to
	// the DLQ and the source queue returns empty.
	for i := 1; i <= 2; i++ {
		recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(srcURL),
			MaxNumberOfMessages:         10,
			VisibilityTimeout:           0,
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		})
		require.NoError(t, err)
		require.Len(t, recv.Messages, 1, "receive %d should still return the message", i)
		assert.Equal(t, "poison", aws.ToString(recv.Messages[0].Body))
		assert.Equal(t, strconv.Itoa(i),
			recv.Messages[0].Attributes["ApproximateReceiveCount"],
			"ApproximateReceiveCount must increment per receive")
	}

	// Third receive: count would reach 3 (> maxReceiveCount=2) → redrived.
	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(srcURL),
		MaxNumberOfMessages: 10,
		VisibilityTimeout:   0,
	})
	require.NoError(t, err)
	assert.Empty(t, recv.Messages, "source queue must be empty after the redrive")

	// The message now lives on the DLQ with a fresh id and reset count.
	dlqRecv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(dlqURL),
		MaxNumberOfMessages:         10,
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
	})
	require.NoError(t, err)
	require.Len(t, dlqRecv.Messages, 1)
	assert.Equal(t, "poison", aws.ToString(dlqRecv.Messages[0].Body))
	assert.Equal(t, "1",
		dlqRecv.Messages[0].Attributes["ApproximateReceiveCount"],
		"DLQ must track its own receive count from zero")

	// Source-queue depth attribute must reflect the absence.
	srcAttrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(srcURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages},
	})
	require.NoError(t, err)
	assert.Equal(t, "0", srcAttrs.Attributes["ApproximateNumberOfMessages"])
}

// sqsRedrivePolicyJSONCount builds a RedrivePolicy attribute string with an
// explicit maxReceiveCount, since the SDK stores both fields as JSON strings.
func sqsRedrivePolicyJSONCount(t *testing.T, dlqARN string, maxReceiveCount int) string {
	t.Helper()
	return `{"deadLetterTargetArn":"` + dlqARN +
		`","maxReceiveCount":"` + strconv.Itoa(maxReceiveCount) + `"}`
}
