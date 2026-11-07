package azure_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/admin"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A queue that auto-forwards hands every message to the topic it names and
// accepts no receiver of its own; one that forwards dead letters sends them to
// the queue it names; a destination that does not exist refuses the source.
func TestServiceBus_AutoForwarding(t *testing.T) {
	t.Parallel()
	namespace := uniquename.New("sdk-sb-forward")
	adminClient := sbAdminClient(t, namespace)
	_, err := adminClient.CreateTopic(ctx, "orders", nil)
	require.NoError(t, err)
	_, err = adminClient.CreateSubscription(ctx, "orders", "audit", nil)
	require.NoError(t, err)
	_, err = adminClient.CreateQueue(ctx, "parked", nil)
	require.NoError(t, err)
	_, err = adminClient.CreateQueue(ctx, "intake", &admin.CreateQueueOptions{Properties: &admin.QueueProperties{ForwardTo: to.Ptr("orders")}})
	require.NoError(t, err)
	_, err = adminClient.CreateQueue(ctx, "work", &admin.CreateQueueOptions{Properties: &admin.QueueProperties{ForwardDeadLetteredMessagesTo: to.Ptr("parked")}})
	require.NoError(t, err)
	_, err = adminClient.CreateQueue(ctx, "nowhere", &admin.CreateQueueOptions{Properties: &admin.QueueProperties{ForwardTo: to.Ptr("missing")}})
	require.Error(t, err, "a forwarding destination must exist when the source is created")

	client := sbAMQPClient(t, namespace)
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	send := func(entity, body string) {
		sender, err := client.NewSender(entity, nil)
		require.NoError(t, err)
		defer func() { _ = sender.Close(context.Background()) }()
		require.NoError(t, sender.SendMessage(ctx, &azservicebus.Message{Body: []byte(body)}, nil))
	}
	receiveOne := func(receiver *azservicebus.Receiver) *azservicebus.ReceivedMessage {
		receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		messages, err := receiver.ReceiveMessages(receiveCtx, 1, nil)
		require.NoError(t, err)
		require.Len(t, messages, 1)
		return messages[0]
	}

	send("intake", "forwarded order")
	audit, err := client.NewReceiverForSubscription("orders", "audit", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = audit.Close(context.Background()) })
	got := receiveOne(audit)
	assert.Equal(t, []byte("forwarded order"), got.Body, "the topic's subscription receives what the queue forwarded")
	require.NoError(t, audit.CompleteMessage(ctx, got, nil))

	source, err := client.NewReceiverForQueue("intake", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	refusedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = source.ReceiveMessages(refusedCtx, 1, nil)
	require.Error(t, err, "Service Bus allows no receiver on an entity that auto-forwards")

	send("work", "poison")
	work, err := client.NewReceiverForQueue("work", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = work.Close(context.Background()) })
	require.NoError(t, work.DeadLetterMessage(ctx, receiveOne(work), &azservicebus.DeadLetterOptions{Reason: to.Ptr("bad payload")}))
	parked, err := client.NewReceiverForQueue("parked", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = parked.Close(context.Background()) })
	dead := receiveOne(parked)
	assert.Equal(t, []byte("poison"), dead.Body, "the dead letter reaches the queue the source forwards dead letters to")
	require.NotNil(t, dead.DeadLetterReason)
	assert.Equal(t, "bad payload", *dead.DeadLetterReason)
}
