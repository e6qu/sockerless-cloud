package azure_sdk_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventHubsSDK_StartPositions proves a partition consumer starts where its
// StartPosition says — the latest event, or a sequence number — and receives
// events published after it attached.
func TestEventHubsSDK_StartPositions(t *testing.T) {
	rg, ns, hub := "eh-start-rg", "sdk-eventhub-start", "starthub"
	nsClient, err := armeventhub.NewNamespacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	hubsClient, err := armeventhub.NewEventHubsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := nsClient.BeginCreateOrUpdate(ctx, rg, ns, armeventhub.EHNamespace{
		Location: to.Ptr("eastus"),
		SKU:      &armeventhub.SKU{Name: to.Ptr(armeventhub.SKUNameStandard), Tier: to.Ptr(armeventhub.SKUTierStandard)},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		deletePoller, err := nsClient.BeginDelete(context.Background(), rg, ns, nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(context.Background(), nil)
		}
	})
	_, err = hubsClient.CreateOrUpdate(ctx, rg, ns, hub, armeventhub.Eventhub{
		Properties: &armeventhub.Properties{PartitionCount: to.Ptr[int64](1), MessageRetentionInDays: to.Ptr[int64](1)},
	}, nil)
	require.NoError(t, err)

	conn := messagingConnectionString(ns, ns+".servicebus.localhost", eventHubsNamespaceKey(t, ns), "EntityPath="+hub)
	tlsConfig := &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- test-only self-signed simulator certificate.
	producer, err := azeventhubs.NewProducerClientFromConnectionString(conn, "", &azeventhubs.ProducerClientOptions{CustomEndpoint: sbAMQPEndpoint, TLSConfig: tlsConfig})
	require.NoError(t, err)
	t.Cleanup(func() { _ = producer.Close(context.Background()) })
	send := func(body string) {
		t.Helper()
		batch, err := producer.NewEventDataBatch(ctx, &azeventhubs.EventDataBatchOptions{PartitionID: to.Ptr("0")})
		require.NoError(t, err)
		require.NoError(t, batch.AddEventData(&azeventhubs.EventData{Body: []byte(body)}, nil))
		require.NoError(t, producer.SendEventDataBatch(ctx, batch, nil))
	}
	send("zero")
	send("one")

	consumer, err := azeventhubs.NewConsumerClientFromConnectionString(conn, "", azeventhubs.DefaultConsumerGroup, &azeventhubs.ConsumerClientOptions{CustomEndpoint: sbAMQPEndpoint, TLSConfig: tlsConfig})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumer.Close(context.Background()) })
	receiveOne := func(pc *azeventhubs.PartitionClient) *azeventhubs.ReceivedEventData {
		t.Helper()
		receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		events, err := pc.ReceiveEvents(receiveCtx, 1, nil)
		require.NoError(t, err)
		require.Len(t, events, 1)
		return events[0]
	}

	fromSeq, err := consumer.NewPartitionClient("0", &azeventhubs.PartitionClientOptions{
		StartPosition: azeventhubs.StartPosition{SequenceNumber: to.Ptr[int64](1), Inclusive: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = fromSeq.Close(context.Background()) })
	got := receiveOne(fromSeq)
	assert.Equal(t, "one", string(got.Body))
	assert.Equal(t, int64(1), got.SequenceNumber)

	latest, err := consumer.NewPartitionClient("0", &azeventhubs.PartitionClientOptions{
		StartPosition: azeventhubs.StartPosition{Latest: to.Ptr(true)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = latest.Close(context.Background()) })
	received := make(chan *azeventhubs.ReceivedEventData, 1)
	go func() {
		receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		events, err := latest.ReceiveEvents(receiveCtx, 1, nil)
		if err == nil && len(events) == 1 {
			received <- events[0]
		}
		close(received)
	}()
	time.Sleep(500 * time.Millisecond)
	send("two")
	event, ok := <-received
	require.True(t, ok, "a consumer starting at the latest event receives the one published after it attached")
	assert.Equal(t, "two", string(event.Body))
	assert.Equal(t, int64(2), event.SequenceNumber)
}
