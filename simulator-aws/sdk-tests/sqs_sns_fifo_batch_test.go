package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQS_FifoNameAttributeCoupling locks the real-SQS coupling between
// the .fifo name suffix and the FifoQueue=true attribute: a .fifo name
// without the attribute (and the attribute without a .fifo name) is
// rejected with InvalidParameterValue.
func TestSQS_FifoNameAttributeCoupling(t *testing.T) {
	c := sqsClient()

	// .fifo name without FifoQueue=true → rejected.
	_, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("coupling-bad.fifo"),
	})
	assert.Equal(t, "InvalidParameterValue", errCode(t, err),
		"a .fifo name requires FifoQueue=true")

	// FifoQueue=true without a .fifo name → rejected.
	_, err = c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String("coupling-bad-noattr"),
		Attributes: map[string]string{"FifoQueue": "true"},
	})
	assert.Equal(t, "InvalidParameterValue", errCode(t, err),
		"FifoQueue=true requires the .fifo suffix")

	// Matching name + attribute → accepted.
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("coupling-ok.fifo"),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl})
	})
	require.NotNil(t, out.QueueUrl)
}

// TestSQS_FifoSendRequiresMessageGroupId asserts that a FIFO queue
// rejects SendMessage without MessageGroupId (MissingParameter) and
// accepts it when supplied.
func TestSQS_FifoSendRequiresMessageGroupId(t *testing.T) {
	c := sqsClient()
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("fifo-group.fifo"),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	require.NoError(t, err)
	url := out.QueueUrl
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: url})
	})

	_, err = c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    url,
		MessageBody: aws.String("no group"),
	})
	assert.Equal(t, "MissingParameter", errCode(t, err),
		"FIFO send without MessageGroupId must fail")

	send, err := c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:       url,
		MessageBody:    aws.String("with group"),
		MessageGroupId: aws.String("g1"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(send.MessageId))
}

// TestSQS_FifoSendRequiresDedup asserts that a FIFO queue without
// ContentBasedDeduplication requires an explicit MessageDeduplicationId.
func TestSQS_FifoSendRequiresDedup(t *testing.T) {
	c := sqsClient()
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String("fifo-dedup.fifo"),
		Attributes: map[string]string{"FifoQueue": "true"},
	})
	require.NoError(t, err)
	url := out.QueueUrl
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: url})
	})

	_, err = c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:       url,
		MessageBody:    aws.String("no dedup"),
		MessageGroupId: aws.String("g1"),
	})
	assert.Equal(t, "InvalidParameterValue", errCode(t, err),
		"FIFO send without ContentBasedDeduplication and without MessageDeduplicationId must fail")

	send, err := c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               url,
		MessageBody:            aws.String("explicit dedup"),
		MessageGroupId:         aws.String("g1"),
		MessageDeduplicationId: aws.String("d1"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(send.MessageId))
}

// TestSQS_SendMessageBatch exercises the happy path plus each batch-level
// error (empty, too many, duplicate Ids) and a per-entry FIFO failure.
func TestSQS_SendMessageBatch(t *testing.T) {
	c := sqsClient()
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("batch-q")})
	require.NoError(t, err)
	url := out.QueueUrl
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: url})
	})

	// Happy path: 2 entries succeed.
	batch, err := c.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: url,
		Entries: []sqstypes.SendMessageBatchRequestEntry{
			{Id: aws.String("a"), MessageBody: aws.String("body-a")},
			{Id: aws.String("b"), MessageBody: aws.String("body-b")},
		},
	})
	require.NoError(t, err)
	require.Len(t, batch.Successful, 2)
	assert.Empty(t, batch.Failed)
	for _, s := range batch.Successful {
		assert.NotEmpty(t, aws.ToString(s.MessageId))
		assert.NotEmpty(t, aws.ToString(s.MD5OfMessageBody))
	}

	// Both messages are now in the queue.
	recv, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            url,
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 2)

	// (The empty-batch path is reachable only via a raw client that can
	// emit a zero-length Entries list — the SDK rejects it client-side —
	// so EmptyBatchRequest is asserted in the CLI test instead.)

	// Too many entries (11).
	tooMany := make([]sqstypes.SendMessageBatchRequestEntry, 11)
	for i := range tooMany {
		tooMany[i] = sqstypes.SendMessageBatchRequestEntry{
			Id:          aws.String(string(rune('a' + i))),
			MessageBody: aws.String("x"),
		}
	}
	_, err = c.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: url, Entries: tooMany})
	assert.Equal(t, "AWS.SimpleQueueService.TooManyEntriesInBatchRequest", errCode(t, err))

	// Duplicate Ids.
	_, err = c.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: url,
		Entries: []sqstypes.SendMessageBatchRequestEntry{
			{Id: aws.String("dup"), MessageBody: aws.String("1")},
			{Id: aws.String("dup"), MessageBody: aws.String("2")},
		},
	})
	assert.Equal(t, "AWS.SimpleQueueService.BatchEntryIdsNotDistinct", errCode(t, err))
}

// TestSQS_SendMessageBatch_FifoPerEntryFailure asserts a per-entry FIFO
// validation failure lands in the Failed array (HTTP 200, not a
// top-level error).
func TestSQS_SendMessageBatch_FifoPerEntryFailure(t *testing.T) {
	c := sqsClient()
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("batch-fifo.fifo"),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	require.NoError(t, err)
	url := out.QueueUrl
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: url})
	})

	batch, err := c.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: url,
		Entries: []sqstypes.SendMessageBatchRequestEntry{
			{Id: aws.String("ok"), MessageBody: aws.String("good"), MessageGroupId: aws.String("g")},
			{Id: aws.String("bad"), MessageBody: aws.String("nogroup")},
		},
	})
	require.NoError(t, err)
	require.Len(t, batch.Successful, 1)
	require.Len(t, batch.Failed, 1)
	assert.Equal(t, "ok", aws.ToString(batch.Successful[0].Id))
	assert.Equal(t, "bad", aws.ToString(batch.Failed[0].Id))
	assert.Equal(t, "MissingParameter", aws.ToString(batch.Failed[0].Code))
	assert.True(t, batch.Failed[0].SenderFault)
}

func TestSQS_FIFODeduplicationAndMessageGroupOrdering(t *testing.T) {
	c := sqsClient()
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("fifo-runtime.fifo"),
		Attributes: map[string]string{
			"FifoQueue": "true",
		},
	})
	require.NoError(t, err)
	url := out.QueueUrl
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: url})
	})

	first, err := c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               url,
		MessageBody:            aws.String("group-a-first"),
		MessageGroupId:         aws.String("group-a"),
		MessageDeduplicationId: aws.String("dedup-a-first"),
	})
	require.NoError(t, err)
	duplicate, err := c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               url,
		MessageBody:            aws.String("a duplicate payload is ignored"),
		MessageGroupId:         aws.String("group-a"),
		MessageDeduplicationId: aws.String("dedup-a-first"),
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(first.MessageId), aws.ToString(duplicate.MessageId))
	assert.Equal(t, aws.ToString(first.SequenceNumber), aws.ToString(duplicate.SequenceNumber))

	second, err := c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               url,
		MessageBody:            aws.String("group-a-second"),
		MessageGroupId:         aws.String("group-a"),
		MessageDeduplicationId: aws.String("dedup-a-second"),
	})
	require.NoError(t, err)
	_, err = c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               url,
		MessageBody:            aws.String("group-b-first"),
		MessageGroupId:         aws.String("group-b"),
		MessageDeduplicationId: aws.String("dedup-b-first"),
	})
	require.NoError(t, err)
	assert.NotEqual(t, aws.ToString(first.SequenceNumber), aws.ToString(second.SequenceNumber))

	receivedFirst, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    url,
		MaxNumberOfMessages:         1,
		VisibilityTimeout:           30,
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
	})
	require.NoError(t, err)
	require.Len(t, receivedFirst.Messages, 1)
	assert.Equal(t, "group-a-first", aws.ToString(receivedFirst.Messages[0].Body))
	assert.Equal(t, "group-a", receivedFirst.Messages[0].Attributes["MessageGroupId"])
	assert.Equal(t, aws.ToString(first.SequenceNumber), receivedFirst.Messages[0].Attributes["SequenceNumber"])

	whileAIsInFlight, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            url,
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	require.Len(t, whileAIsInFlight.Messages, 1)
	assert.Equal(t, "group-b-first", aws.ToString(whileAIsInFlight.Messages[0].Body))

	_, err = c.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      url,
		ReceiptHandle: receivedFirst.Messages[0].ReceiptHandle,
	})
	require.NoError(t, err)
	afterDelete, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            url,
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	require.Len(t, afterDelete.Messages, 1)
	assert.Equal(t, "group-a-second", aws.ToString(afterDelete.Messages[0].Body))
}

func TestSQSDelayMaximumSizeAndRetentionRuntime(t *testing.T) {
	c := sqsClient()
	out, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("runtime-attributes"),
		Attributes: map[string]string{
			"DelaySeconds":           "1",
			"MaximumMessageSize":     "1024",
			"MessageRetentionPeriod": "60",
		},
	})
	require.NoError(t, err)
	url := out.QueueUrl
	t.Cleanup(func() {
		_, _ = c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: url})
	})

	_, err = c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    url,
		MessageBody: aws.String(string(make([]byte, 1025))),
	})
	assert.Equal(t, "InvalidParameterValue", errCode(t, err))

	_, err = c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    url,
		MessageBody: aws.String("delayed"),
	})
	require.NoError(t, err)
	immediate, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            url,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Empty(t, immediate.Messages)
	time.Sleep(1100 * time.Millisecond)
	afterDelay, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            url,
		MaxNumberOfMessages: 1,
		VisibilityTimeout:   0,
	})
	require.NoError(t, err)
	require.Len(t, afterDelay.Messages, 1)
	assert.Equal(t, "delayed", aws.ToString(afterDelay.Messages[0].Body))

	time.Sleep(60 * time.Second)
	afterRetention, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            url,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Empty(t, afterRetention.Messages)
}

// TestSNS_FifoTopicCoupling locks the SNS .fifo-name ↔ FifoTopic=true
// coupling at CreateTopic.
func TestSNS_FifoTopicCoupling(t *testing.T) {
	c := snsClient()

	// .fifo name without FifoTopic=true → rejected.
	_, err := c.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("sns-bad.fifo")})
	assert.Equal(t, "InvalidParameter", errCode(t, err))

	// FifoTopic=true without .fifo → rejected.
	_, err = c.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String("sns-bad-noattr"),
		Attributes: map[string]string{"FifoTopic": "true"},
	})
	assert.Equal(t, "InvalidParameter", errCode(t, err))

	// Matching → accepted.
	out, err := c.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String("sns-ok.fifo"),
		Attributes: map[string]string{
			"FifoTopic":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: out.TopicArn})
	})
	require.NotNil(t, out.TopicArn)
}

// TestSNS_FifoPublishRequiresMessageGroupId asserts a FIFO topic rejects
// Publish without MessageGroupId and accepts it when supplied.
func TestSNS_FifoPublishRequiresMessageGroupId(t *testing.T) {
	c := snsClient()
	out, err := c.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String("sns-fifo-pub.fifo"),
		Attributes: map[string]string{
			"FifoTopic":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: out.TopicArn})
	})

	_, err = c.Publish(ctx, &sns.PublishInput{
		TopicArn: out.TopicArn,
		Message:  aws.String("no group"),
	})
	assert.Equal(t, "InvalidParameter", errCode(t, err))

	pub, err := c.Publish(ctx, &sns.PublishInput{
		TopicArn:       out.TopicArn,
		Message:        aws.String("with group"),
		MessageGroupId: aws.String("g1"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(pub.MessageId))
}

// TestSNS_PublishBatch exercises the happy path, fan-out to an SQS
// subscriber, and the batch-level errors.
func TestSNS_PublishBatch(t *testing.T) {
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("pubbatch-t")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("pubbatch-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	qAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := qAttrs.Attributes["QueueArn"]

	// Real SNS→SQS delivery is gated by the queue's resource policy.
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, aws.ToString(tpc.TopicArn))

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	batch, err := snsC.PublishBatch(ctx, &sns.PublishBatchInput{
		TopicArn: tpc.TopicArn,
		PublishBatchRequestEntries: []snstypes.PublishBatchRequestEntry{
			{Id: aws.String("e1"), Message: aws.String("msg-1")},
			{Id: aws.String("e2"), Message: aws.String("msg-2")},
		},
	})
	require.NoError(t, err)
	require.Len(t, batch.Successful, 2)
	assert.Empty(t, batch.Failed)
	for _, s := range batch.Successful {
		assert.NotEmpty(t, aws.ToString(s.MessageId))
	}

	// Both fanned out to the SQS subscriber.
	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 2)

	// (EmptyBatchRequest is reachable only via a raw client emitting a
	// zero-length entry list — the SDK rejects it client-side — so it's
	// asserted in the CLI test instead.)

	// Duplicate Ids.
	_, err = snsC.PublishBatch(ctx, &sns.PublishBatchInput{
		TopicArn: tpc.TopicArn,
		PublishBatchRequestEntries: []snstypes.PublishBatchRequestEntry{
			{Id: aws.String("dup"), Message: aws.String("1")},
			{Id: aws.String("dup"), Message: aws.String("2")},
		},
	})
	assert.Equal(t, "BatchEntryIdsNotDistinct", errCode(t, err))

	// Too many entries (11).
	tooMany := make([]snstypes.PublishBatchRequestEntry, 11)
	for i := range tooMany {
		tooMany[i] = snstypes.PublishBatchRequestEntry{
			Id:      aws.String(string(rune('a' + i))),
			Message: aws.String("x"),
		}
	}
	_, err = snsC.PublishBatch(ctx, &sns.PublishBatchInput{
		TopicArn:                   tpc.TopicArn,
		PublishBatchRequestEntries: tooMany,
	})
	assert.Equal(t, "TooManyEntriesInBatchRequest", errCode(t, err))
}
