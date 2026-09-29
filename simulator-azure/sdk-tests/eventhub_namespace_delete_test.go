package azure_sdk_test

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deleting an Event Hubs namespace deletes the events its hubs held, so a
// namespace created again under the same name serves empty partitions.
func TestEventHubsSDK_NamespaceDeleteDropsItsEvents(t *testing.T) {
	rg, ns, hub := "eh-nsdelete-rg", "sdk-eventhub-nsdelete", "dropped"
	nsClient, err := armeventhub.NewNamespacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	hubsClient, err := armeventhub.NewEventHubsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	tlsConfig := &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- test-only self-signed simulator certificate.

	create := func() *azeventhubs.ProducerClient {
		t.Helper()
		poller, err := nsClient.BeginCreateOrUpdate(ctx, rg, ns, armeventhub.EHNamespace{
			Location: to.Ptr("eastus"),
			SKU:      &armeventhub.SKU{Name: to.Ptr(armeventhub.SKUNameStandard), Tier: to.Ptr(armeventhub.SKUTierStandard)},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		_, err = hubsClient.CreateOrUpdate(ctx, rg, ns, hub, armeventhub.Eventhub{
			Properties: &armeventhub.Properties{PartitionCount: to.Ptr[int64](1), MessageRetentionInDays: to.Ptr[int64](1)},
		}, nil)
		require.NoError(t, err)
		conn := messagingConnectionString(ns, ns+".servicebus.localhost", eventHubsNamespaceKey(t, ns), "EntityPath="+hub)
		producer, err := azeventhubs.NewProducerClientFromConnectionString(conn, "", &azeventhubs.ProducerClientOptions{CustomEndpoint: sbAMQPEndpoint, TLSConfig: tlsConfig})
		require.NoError(t, err)
		t.Cleanup(func() { _ = producer.Close(context.Background()) })
		return producer
	}
	deleteNamespace := func() {
		t.Helper()
		poller, err := nsClient.BeginDelete(ctx, rg, ns, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}

	producer := create()
	batch, err := producer.NewEventDataBatch(ctx, &azeventhubs.EventDataBatchOptions{PartitionID: to.Ptr("0")})
	require.NoError(t, err)
	require.NoError(t, batch.AddEventData(&azeventhubs.EventData{Body: []byte("before delete")}, nil))
	require.NoError(t, producer.SendEventDataBatch(ctx, batch, nil))
	props, err := producer.GetPartitionProperties(ctx, "0", nil)
	require.NoError(t, err)
	require.False(t, props.IsEmpty, "the partition holds the event just sent")
	require.Equal(t, int64(0), props.LastEnqueuedSequenceNumber)
	require.NoError(t, producer.Close(ctx))

	deleteNamespace()
	producer = create()
	t.Cleanup(deleteNamespace)
	props, err = producer.GetPartitionProperties(ctx, "0", nil)
	require.NoError(t, err)
	assert.True(t, props.IsEmpty, "the recreated hub's partition is empty")
	assert.Equal(t, int64(-1), props.LastEnqueuedSequenceNumber)
	assert.Equal(t, int64(0), props.BeginningSequenceNumber)
}
