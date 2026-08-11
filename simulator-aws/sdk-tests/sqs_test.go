package aws_sdk_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sqsClient() *sqs.Client {
	return sqs.NewFromConfig(sdkConfig(), func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestSQS_ReceiveMessageAttributeSubsetMD5 asserts that requesting a subset of
// message attributes recomputes MD5OfMessageAttributes over exactly the returned
// set. aws-sdk-go-v2's ValidateMessageChecksums middleware fails the call on a
// digest mismatch, so a successful subset receive proves the recompute (the sim
// previously re-emitted the stored full-set digest).
func TestSQS_ReceiveMessageAttributeSubsetMD5(t *testing.T) {
	client := sqsClient()
	create, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("attr-md5-q")})
	require.NoError(t, err)
	url := aws.ToString(create.QueueUrl)

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(url),
		MessageBody: aws.String("hello"),
		MessageAttributes: map[string]sqstypes.MessageAttributeValue{
			"alpha": {DataType: aws.String("String"), StringValue: aws.String("one")},
			"beta":  {DataType: aws.String("String"), StringValue: aws.String("two")},
		},
	})
	require.NoError(t, err)

	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(url),
		MessageAttributeNames: []string{"alpha"},
	})
	require.NoError(t, err, "subset MD5 must validate against the returned attribute set")
	require.Len(t, recv.Messages, 1)
	assert.Len(t, recv.Messages[0].MessageAttributes, 1)
	assert.Contains(t, recv.Messages[0].MessageAttributes, "alpha")
	assert.NotContains(t, recv.Messages[0].MessageAttributes, "beta")
}

// TestSQS_QueueLifecycle exercises the 90th-percentile produce-
// consume-ack flow that real consumers run against SQS.
func TestSQS_QueueLifecycle(t *testing.T) {
	client := sqsClient()

	// CreateQueue + GetQueueUrl round-trip.
	create, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("life-q"),
	})
	require.NoError(t, err)
	require.NotNil(t, create.QueueUrl)
	queueURL := *create.QueueUrl
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})

	urlOut, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{
		QueueName: aws.String("life-q"),
	})
	require.NoError(t, err)
	assert.Equal(t, queueURL, aws.ToString(urlOut.QueueUrl))

	// ListQueues + prefix filter.
	list, err := client.ListQueues(ctx, &sqs.ListQueuesInput{
		QueueNamePrefix: aws.String("life-"),
	})
	require.NoError(t, err)
	assert.Contains(t, list.QueueUrls, queueURL)

	// GetQueueAttributes — empty queue should report 0 messages.
	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{"All"},
	})
	require.NoError(t, err)
	assert.Equal(t, "0", attrs.Attributes["ApproximateNumberOfMessages"])
	assert.NotEmpty(t, attrs.Attributes["QueueArn"])

	_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		Attributes: map[string]string{
			"VisibilityTimeout": "7",
		},
	})
	require.NoError(t, err)
	updatedAttrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameVisibilityTimeout},
	})
	require.NoError(t, err)
	assert.Equal(t, "7", updatedAttrs.Attributes["VisibilityTimeout"])

	// Send + Receive round-trip.
	send, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String("hello sqs"),
	})
	require.NoError(t, err)
	require.NotNil(t, send.MessageId)
	require.NotEmpty(t, aws.ToString(send.MD5OfMessageBody))

	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL),
	})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1)
	assert.Equal(t, "hello sqs", aws.ToString(recv.Messages[0].Body))
	receiptHandle := aws.ToString(recv.Messages[0].ReceiptHandle)
	require.NotEmpty(t, receiptHandle)

	// Visibility timeout: a second ReceiveMessage right away should
	// see nothing (the message is in-flight under the receipt handle).
	recv2, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL),
	})
	require.NoError(t, err)
	assert.Empty(t, recv2.Messages,
		"message should be invisible during the visibility-timeout window")

	// DeleteMessage acks it.
	_, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	})
	require.NoError(t, err)

	// Attributes should show 0 messages again (ack removed it).
	attrsAfter, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{"All"},
	})
	require.NoError(t, err)
	assert.Equal(t, "0", attrsAfter.Attributes["ApproximateNumberOfMessages"])
}

// TestSQS_TagQueueRoundTrip asserts tag CRUD per the SDK shape.
func TestSQS_TagQueueRoundTrip(t *testing.T) {
	client := sqsClient()
	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("tagq")})
	require.NoError(t, err)
	url := aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
	})
	_, err = client.TagQueue(ctx, &sqs.TagQueueInput{
		QueueUrl: aws.String(url),
		Tags:     map[string]string{"env": "test", "team": "runner"},
	})
	require.NoError(t, err)
	listTags, err := client.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	assert.Equal(t, "test", listTags.Tags["env"])
	assert.Equal(t, "runner", listTags.Tags["team"])
	_, err = client.UntagQueue(ctx, &sqs.UntagQueueInput{
		QueueUrl: aws.String(url),
		TagKeys:  []string{"team"},
	})
	require.NoError(t, err)
	listTags2, err := client.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	assert.Equal(t, "test", listTags2.Tags["env"])
	_, untagged := listTags2.Tags["team"]
	assert.False(t, untagged, "team tag should be removed")
}

// TestSQS_NonExistentQueue confirms the canonical error envelope
// real callers handle on a missing queue.
func TestSQS_NonExistentQueue(t *testing.T) {
	client := sqsClient()
	_, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String("https://sqs.us-east-1.amazonaws.com/000000000000/missing"),
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "expected AWS API error, got %T: %v", err, err)
	assert.Equal(t, "AWS.SimpleQueueService.NonExistentQueue", apiErr.ErrorCode())
}

func TestSQS_ReceiveMessageRejectsInvalidMaxNumberOfMessages(t *testing.T) {
	client := sqsClient()
	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("max-invalid-q")})
	require.NoError(t, err)
	queueURL := aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})

	_, err = client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		MaxNumberOfMessages: 25,
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "expected AWS API error, got %T: %v", err, err)
	assert.Equal(t, "InvalidParameterValue", apiErr.ErrorCode())
	assert.Contains(t, apiErr.ErrorMessage(), "must be between 1 and 10")
}

func TestSQS_ReceiveMessageHonorsLongPollingWaitTime(t *testing.T) {
	client := sqsClient()
	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("long-poll-q")})
	require.NoError(t, err)
	queueURL := aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})

	start := time.Now()
	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:        aws.String(queueURL),
		WaitTimeSeconds: 1,
	})
	require.NoError(t, err)
	assert.Empty(t, recv.Messages)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	assert.Less(t, elapsed, 3*time.Second)
}

func TestSQS_ReceiveMessageRejectsInvalidWaitTimeSeconds(t *testing.T) {
	client := sqsClient()
	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("wait-invalid-q")})
	require.NoError(t, err)
	queueURL := aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})

	_, err = client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:        aws.String(queueURL),
		WaitTimeSeconds: 21,
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "expected AWS API error, got %T: %v", err, err)
	assert.Equal(t, "InvalidParameterValue", apiErr.ErrorCode())
	assert.Contains(t, apiErr.ErrorMessage(), "must be between 0 and 20")
}

// TestSQS_VisibilityTimeoutExpiry asserts a message returns to
// visible state after the per-receive timeout elapses.
func TestSQS_VisibilityTimeoutExpiry(t *testing.T) {
	client := sqsClient()
	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("vtq"),
		Attributes: map[string]string{
			"VisibilityTimeout": "1",
		},
	})
	require.NoError(t, err)
	url := aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
	})
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(url), MessageBody: aws.String("vt"),
	})
	require.NoError(t, err)
	_, err = client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	// Wait past the 1-second visibility timeout.
	time.Sleep(1500 * time.Millisecond)
	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1,
		"message should be visible again after the visibility-timeout window elapsed")
	assert.Equal(t, "vt", aws.ToString(recv.Messages[0].Body))
}

// TestSQS_ChangeMessageVisibility asserts that resetting an in-flight message's
// visibility timeout to 0 makes it immediately receivable again. The message is
// invisible right after the first receive (default 30s timeout); a
// ChangeMessageVisibility to 0 returns it to the visible pool.
func TestSQS_ChangeMessageVisibility(t *testing.T) {
	client := sqsClient()
	create, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("cmv-q")})
	require.NoError(t, err)
	url := aws.ToString(create.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}) })

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(url), MessageBody: aws.String("cmv-body"),
	})
	require.NoError(t, err)

	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1)
	handle := aws.ToString(recv.Messages[0].ReceiptHandle)

	// Invisible immediately (30s default timeout).
	recv2, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	assert.Empty(t, recv2.Messages)

	// Reset visibility to 0 → immediately visible again.
	_, err = client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(url),
		ReceiptHandle:     aws.String(handle),
		VisibilityTimeout: 0,
	})
	require.NoError(t, err)

	recv3, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	require.Len(t, recv3.Messages, 1, "message should be visible after timeout reset to 0")
	assert.Equal(t, "cmv-body", aws.ToString(recv3.Messages[0].Body))

	// An unknown receipt handle is ReceiptHandleIsInvalid.
	_, err = client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(url),
		ReceiptHandle:     aws.String("not-a-handle"),
		VisibilityTimeout: 10,
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "ReceiptHandleIsInvalid", apiErr.ErrorCode())
}

// TestSQS_ChangeMessageVisibilityBatch asserts per-entry success/failure for a
// batch visibility change: a valid in-flight handle succeeds, an unknown handle
// fails with ReceiptHandleIsInvalid.
func TestSQS_ChangeMessageVisibilityBatch(t *testing.T) {
	client := sqsClient()
	create, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("cmvb-q")})
	require.NoError(t, err)
	url := aws.ToString(create.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}) })

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(url), MessageBody: aws.String("b1"),
	})
	require.NoError(t, err)
	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1)
	handle := aws.ToString(recv.Messages[0].ReceiptHandle)

	out, err := client.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{
		QueueUrl: aws.String(url),
		Entries: []sqstypes.ChangeMessageVisibilityBatchRequestEntry{
			{Id: aws.String("ok"), ReceiptHandle: aws.String(handle), VisibilityTimeout: 0},
			{Id: aws.String("bad"), ReceiptHandle: aws.String("nope"), VisibilityTimeout: 10},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Successful, 1)
	assert.Equal(t, "ok", aws.ToString(out.Successful[0].Id))
	require.Len(t, out.Failed, 1)
	assert.Equal(t, "bad", aws.ToString(out.Failed[0].Id))
	assert.Equal(t, "ReceiptHandleIsInvalid", aws.ToString(out.Failed[0].Code))

	// The "ok" entry reset visibility to 0 → b1 is receivable again.
	recv2, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url)})
	require.NoError(t, err)
	require.Len(t, recv2.Messages, 1)
}

// TestSQS_DeleteMessageBatch asserts batch delete by receipt handle: two
// messages received then batch-deleted leave the queue empty.
func TestSQS_DeleteMessageBatch(t *testing.T) {
	client := sqsClient()
	create, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("dmb-q")})
	require.NoError(t, err)
	url := aws.ToString(create.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}) })

	for _, b := range []string{"d1", "d2"} {
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl: aws.String(url), MessageBody: aws.String(b),
		})
		require.NoError(t, err)
	}
	recv, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(url), MaxNumberOfMessages: 10,
	})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 2)

	entries := make([]sqstypes.DeleteMessageBatchRequestEntry, 0, 2)
	for i, m := range recv.Messages {
		entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{
			Id:            aws.String("e" + strconv.Itoa(i)),
			ReceiptHandle: m.ReceiptHandle,
		})
	}
	// Append a bogus entry to exercise the Failed path.
	entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{
		Id: aws.String("bad"), ReceiptHandle: aws.String("nope"),
	})
	out, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(url), Entries: entries,
	})
	require.NoError(t, err)
	assert.Len(t, out.Successful, 2)
	require.Len(t, out.Failed, 1)
	assert.Equal(t, "bad", aws.ToString(out.Failed[0].Id))

	// Make the in-flight messages visible again to confirm they're gone.
	for _, m := range recv.Messages {
		_, _ = client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 0,
		})
	}
	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{"All"},
	})
	require.NoError(t, err)
	assert.Equal(t, "0", attrs.Attributes["ApproximateNumberOfMessages"])
}

// TestSQS_AddRemovePermission asserts AddPermission writes a labelled Allow
// statement into the queue's Policy attribute (readable via GetQueueAttributes)
// and RemovePermission strips it back out.
func TestSQS_AddRemovePermission(t *testing.T) {
	client := sqsClient()
	create, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("perm-q")})
	require.NoError(t, err)
	url := aws.ToString(create.QueueUrl)
	t.Cleanup(func() { _, _ = client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}) })

	_, err = client.AddPermission(ctx, &sqs.AddPermissionInput{
		QueueUrl:      aws.String(url),
		Label:         aws.String("grant-send"),
		AWSAccountIds: []string{"123456789012"},
		Actions:       []string{"SendMessage", "ReceiveMessage"},
	})
	require.NoError(t, err)

	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNamePolicy},
	})
	require.NoError(t, err)
	policy := attrs.Attributes["Policy"]
	require.NotEmpty(t, policy, "AddPermission must populate the Policy attribute")
	assert.Contains(t, policy, "grant-send")
	assert.Contains(t, policy, "arn:aws:iam::123456789012:root")
	assert.Contains(t, policy, "SQS:SendMessage")

	// A duplicate label is rejected.
	_, err = client.AddPermission(ctx, &sqs.AddPermissionInput{
		QueueUrl:      aws.String(url),
		Label:         aws.String("grant-send"),
		AWSAccountIds: []string{"123456789012"},
		Actions:       []string{"SendMessage"},
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "InvalidParameterValue", apiErr.ErrorCode())

	_, err = client.RemovePermission(ctx, &sqs.RemovePermissionInput{
		QueueUrl: aws.String(url),
		Label:    aws.String("grant-send"),
	})
	require.NoError(t, err)

	attrs2, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNamePolicy},
	})
	require.NoError(t, err)
	assert.Empty(t, attrs2.Attributes["Policy"], "RemovePermission must clear the last statement")

	// Removing a non-existent label is rejected.
	_, err = client.RemovePermission(ctx, &sqs.RemovePermissionInput{
		QueueUrl: aws.String(url),
		Label:    aws.String("does-not-exist"),
	})
	require.Error(t, err)
}

func TestSQS_ListQueues_Pagination(t *testing.T) {
	client := sqsClient()
	names := []string{"pag-sqs-a", "pag-sqs-b", "pag-sqs-c"}
	for _, n := range names {
		out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(n)})
		require.NoError(t, err)
		url := aws.ToString(out.QueueUrl)
		t.Cleanup(func() { client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}) })
	}

	seen := map[string]bool{}
	pager := sqs.NewListQueuesPaginator(client, &sqs.ListQueuesInput{MaxResults: aws.Int32(1)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, u := range page.QueueUrls {
			seen[u] = true
		}
	}
	for _, n := range names {
		found := false
		for u := range seen {
			if strings.Contains(u, n) {
				found = true
				break
			}
		}
		assert.True(t, found, "queue %s should appear via pagination", n)
	}
}
