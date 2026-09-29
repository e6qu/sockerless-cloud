package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueueSDK_MessageTimeToLiveAndInitialVisibility drives Put Message's
// messagettl and visibilitytimeout: the expiration the service reports is the
// requested time to live, a message put invisible is not dequeued or peeked,
// and the pop receipt Put Message returns deletes it.
func TestQueueSDK_MessageTimeToLiveAndInitialVisibility(t *testing.T) {
	const (
		account   = "sdkqueuettl"
		queueName = "sdkttlqueue"
	)
	serviceClient := queueSDKService(t, account)
	_, err := serviceClient.CreateQueue(ctx, queueName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })
	queueClient := serviceClient.NewQueueClient(queueName)

	short, err := queueClient.EnqueueMessage(ctx, "one hour", &azqueue.EnqueueMessageOptions{TimeToLive: to.Ptr(int32(3600))})
	require.NoError(t, err)
	require.Len(t, short.Messages, 1)
	enqueued := short.Messages[0]
	assert.Equal(t, time.Hour, enqueued.ExpirationTime.Sub(*enqueued.InsertionTime))

	forever, err := queueClient.EnqueueMessage(ctx, "forever", &azqueue.EnqueueMessageOptions{TimeToLive: to.Ptr(int32(-1))})
	require.NoError(t, err)
	assert.Equal(t, 9999, forever.Messages[0].ExpirationTime.Year())

	hidden, err := queueClient.EnqueueMessage(ctx, "later", &azqueue.EnqueueMessageOptions{VisibilityTimeout: to.Ptr(int32(600))})
	require.NoError(t, err)
	assert.True(t, hidden.Messages[0].TimeNextVisible.After(time.Now().Add(5*time.Minute)))

	peeked, err := queueClient.PeekMessages(ctx, &azqueue.PeekMessagesOptions{NumberOfMessages: to.Ptr(int32(32))})
	require.NoError(t, err)
	assert.Len(t, peeked.Messages, 2, "the invisible message is not peeked")
	one, err := queueClient.PeekMessage(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, one.Messages, 1, "a peek without numofmessages returns one message")

	props, err := queueClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(3), *props.ApproximateMessagesCount, "the count includes invisible messages")

	_, err = queueClient.DeleteMessage(ctx, *hidden.Messages[0].MessageID, *hidden.Messages[0].PopReceipt, nil)
	require.NoError(t, err, "the pop receipt Put Message returned deletes the message")
}
