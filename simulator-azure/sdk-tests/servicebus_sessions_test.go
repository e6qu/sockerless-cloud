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

// ScheduleMessages holds messages until their enqueue time and
// CancelScheduledMessages removes them before it; a message scheduled a moment
// out reaches a receiver once its time comes.
func TestServiceBus_AMQPSDKScheduleAndCancel(t *testing.T) {
	namespace := uniqueName("sdk-amqp-schedule")
	queue := uniqueName("schedq")
	adminClient := sbAdminClient(t, namespace)
	_, err := adminClient.CreateQueue(ctx, queue, nil)
	require.NoError(t, err)

	client := sbRawAMQPClient(t, namespace)
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	sender, err := client.NewSender(queue, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close(context.Background()) })

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	seqs, err := sender.ScheduleMessages(opCtx, []*azservicebus.Message{
		{Body: []byte("in an hour")}, {Body: []byte("also in an hour")},
	}, time.Now().Add(time.Hour), nil)
	require.NoError(t, err)
	require.Len(t, seqs, 2)
	assert.NotEqual(t, seqs[0], seqs[1])

	props, err := adminClient.GetQueueRuntimeProperties(opCtx, queue, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), props.ScheduledMessageCount)
	assert.Equal(t, int32(0), props.ActiveMessageCount)

	require.NoError(t, sender.CancelScheduledMessages(opCtx, seqs[:1], nil))
	props, err = adminClient.GetQueueRuntimeProperties(opCtx, queue, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), props.ScheduledMessageCount)

	_, err = sender.ScheduleMessages(opCtx, []*azservicebus.Message{{Body: []byte("soon")}}, time.Now().Add(500*time.Millisecond), nil)
	require.NoError(t, err)
	receiver, err := client.NewReceiverForQueue(queue, &azservicebus.ReceiverOptions{ReceiveMode: azservicebus.ReceiveModeReceiveAndDelete})
	require.NoError(t, err)
	t.Cleanup(func() { _ = receiver.Close(context.Background()) })
	messages, err := receiver.ReceiveMessages(opCtx, 1, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, []byte("soon"), messages[0].Body)
	require.NotNil(t, messages[0].ScheduledEnqueueTime)
}

// A session-enabled queue delivers each session only to the receiver that
// accepted it, keeps the session's state, and refuses a second receiver the
// lock of a session already accepted.
func TestServiceBus_AMQPSDKSessions(t *testing.T) {
	namespace := uniqueName("sdk-amqp-sessions")
	queue := uniqueName("sessionq")
	adminClient := sbAdminClient(t, namespace)
	_, err := adminClient.CreateQueue(ctx, queue, &admin.CreateQueueOptions{
		Properties: &admin.QueueProperties{RequiresSession: to.Ptr(true)},
	})
	require.NoError(t, err)

	client := sbRawAMQPClient(t, namespace)
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	sender, err := client.NewSender(queue, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close(context.Background()) })

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, m := range []struct{ session, body string }{{"s1", "first"}, {"s2", "other"}, {"s1", "second"}} {
		require.NoError(t, sender.SendMessage(opCtx, &azservicebus.Message{Body: []byte(m.body), SessionID: to.Ptr(m.session)}, nil))
	}

	err = sender.SendMessage(opCtx, &azservicebus.Message{Body: []byte("no session")}, nil)
	require.ErrorContains(t, err, "amqp:not-allowed", "a session-enabled queue refuses a message without a session id")
	require.ErrorContains(t, err, "The SessionId was not set on a message")

	next, err := client.AcceptNextSessionForQueue(opCtx, queue, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	assert.Equal(t, "s1", next.SessionID(), "the next session is the one holding the earliest message")
	assert.True(t, next.LockedUntil().After(time.Now()))

	var bodies []string
	for len(bodies) < 2 {
		messages, err := next.ReceiveMessages(opCtx, 2-len(bodies), nil)
		require.NoError(t, err)
		for _, m := range messages {
			require.NotNil(t, m.SessionID)
			assert.Equal(t, "s1", *m.SessionID)
			bodies = append(bodies, string(m.Body))
			require.NoError(t, next.CompleteMessage(opCtx, m, nil))
		}
	}
	assert.Equal(t, []string{"first", "second"}, bodies)

	require.NoError(t, next.SetSessionState(opCtx, []byte("checkpoint"), nil))
	state, err := next.GetSessionState(opCtx, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("checkpoint"), state)
	require.NoError(t, next.RenewSessionLock(opCtx, nil))

	other := sbRawAMQPClient(t, namespace)
	t.Cleanup(func() { _ = other.Close(context.Background()) })
	_, err = other.AcceptSessionForQueue(opCtx, queue, "s1", nil)
	require.ErrorContains(t, err, "com.microsoft:session-cannot-be-locked", "a second receiver must not take a locked session")

	named, err := other.AcceptSessionForQueue(opCtx, queue, "s2", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = named.Close(context.Background()) })
	messages, err := named.ReceiveMessages(opCtx, 1, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, []byte("other"), messages[0].Body)
	require.NoError(t, named.CompleteMessage(opCtx, messages[0], nil))

	require.NoError(t, next.Close(opCtx))
	again, err := other.AcceptSessionForQueue(opCtx, queue, "s1", nil)
	require.NoError(t, err, "a closed receiver's session is free to accept")
	t.Cleanup(func() { _ = again.Close(context.Background()) })
	state, err = again.GetSessionState(opCtx, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("checkpoint"), state, "the session state outlives the receiver that set it")
}
