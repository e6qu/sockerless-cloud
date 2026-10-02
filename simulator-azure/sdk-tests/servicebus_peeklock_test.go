package azure_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/admin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sbReceiveOne(t *testing.T, receiver *azservicebus.Receiver) *azservicebus.ReceivedMessage {
	t.Helper()
	receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	messages, err := receiver.ReceiveMessages(receiveCtx, 1, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	return messages[0]
}

// TestServiceBus_AMQPPeekLockSettlement proves peek-lock over AMQP: a
// message stays in the queue under the queue's lockDuration until the
// receiver settles it, an abandon redelivers it with a higher DeliveryCount,
// maxDeliveryCount moves it to the dead-letter sub-queue, and an explicit
// dead-letter carries its reason there.
func TestServiceBus_AMQPPeekLockSettlement(t *testing.T) {
	namespace := uniqueName("sdk-amqp-peeklock")
	queue := uniqueName("locked")
	adminClient := sbAdminClient(t, namespace)
	_, err := adminClient.CreateQueue(ctx, queue, &admin.CreateQueueOptions{Properties: &admin.QueueProperties{
		LockDuration:     to.Ptr("PT20S"),
		MaxDeliveryCount: to.Ptr[int32](2),
	}})
	require.NoError(t, err)

	client := sbRawAMQPClient(t, namespace)
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	sender, err := client.NewSender(queue, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close(context.Background()) })
	receiver, err := client.NewReceiverForQueue(queue, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = receiver.Close(context.Background()) })

	require.NoError(t, sender.SendMessage(ctx, &azservicebus.Message{Body: []byte("complete-me"), ApplicationProperties: map[string]any{"kind": "a"}}, nil))
	first := sbReceiveOne(t, receiver)
	assert.Equal(t, uint32(1), first.DeliveryCount)
	assert.Equal(t, "a", first.ApplicationProperties["kind"], "application properties survive the broker")
	require.NotNil(t, first.LockedUntil)
	assert.WithinDuration(t, time.Now().Add(20*time.Second), *first.LockedUntil, 5*time.Second, "the lock lasts the queue's lockDuration")
	require.NoError(t, receiver.RenewMessageLock(ctx, first, nil))
	require.NoError(t, receiver.CompleteMessage(ctx, first, nil))

	require.NoError(t, sender.SendMessage(ctx, &azservicebus.Message{Body: []byte("poison")}, nil))
	attempt := sbReceiveOne(t, receiver)
	require.NoError(t, receiver.AbandonMessage(ctx, attempt, nil))
	attempt = sbReceiveOne(t, receiver)
	assert.Equal(t, uint32(2), attempt.DeliveryCount, "an abandoned message comes back with its delivery counted")
	require.NoError(t, receiver.AbandonMessage(ctx, attempt, nil))

	require.NoError(t, sender.SendMessage(ctx, &azservicebus.Message{Body: []byte("reject-me")}, nil))
	rejected := sbReceiveOne(t, receiver)
	require.Equal(t, "reject-me", string(rejected.Body), "the message past maxDeliveryCount is not delivered again")
	require.NoError(t, receiver.DeadLetterMessage(ctx, rejected, &azservicebus.DeadLetterOptions{
		Reason: to.Ptr("bad-input"), ErrorDescription: to.Ptr("could not parse"),
	}))

	deadReceiver, err := client.NewReceiverForQueue(queue, &azservicebus.ReceiverOptions{
		SubQueue: azservicebus.SubQueueDeadLetter, ReceiveMode: azservicebus.ReceiveModeReceiveAndDelete,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = deadReceiver.Close(context.Background()) })
	reasons := map[string]string{}
	for range 2 {
		m := sbReceiveOne(t, deadReceiver)
		require.NotNil(t, m.DeadLetterReason)
		reasons[string(m.Body)] = *m.DeadLetterReason
	}
	assert.Equal(t, map[string]string{"poison": "MaxDeliveryCountExceeded", "reject-me": "bad-input"}, reasons)

	props, err := adminClient.GetQueueRuntimeProperties(ctx, queue, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(0), props.DeadLetterMessageCount)
	assert.Equal(t, int64(0), props.TotalMessageCount)
}

// TestServiceBus_DuplicateDetection proves a queue that requires duplicate
// detection accepts one message per MessageId inside its history window.
func TestServiceBus_DuplicateDetection(t *testing.T) {
	namespace := uniqueName("sdk-amqp-dedup")
	queue := uniqueName("dedup")
	adminClient := sbAdminClient(t, namespace)
	_, err := adminClient.CreateQueue(ctx, queue, &admin.CreateQueueOptions{Properties: &admin.QueueProperties{
		RequiresDuplicateDetection:          to.Ptr(true),
		DuplicateDetectionHistoryTimeWindow: to.Ptr("PT5M"),
	}})
	require.NoError(t, err)

	client := sbRawAMQPClient(t, namespace)
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	sender, err := client.NewSender(queue, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close(context.Background()) })
	for _, body := range []string{"first", "resend"} {
		require.NoError(t, sender.SendMessage(ctx, &azservicebus.Message{MessageID: to.Ptr("order-1"), Body: []byte(body)}, nil))
	}
	require.NoError(t, sender.SendMessage(ctx, &azservicebus.Message{MessageID: to.Ptr("order-2"), Body: []byte("second")}, nil))

	receiver, err := client.NewReceiverForQueue(queue, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = receiver.Close(context.Background()) })
	peeked, err := receiver.PeekMessages(ctx, 10, nil)
	require.NoError(t, err)
	var bodies []string
	for _, m := range peeked {
		bodies = append(bodies, string(m.Body))
	}
	assert.Equal(t, []string{"first", "second"}, bodies)
}
