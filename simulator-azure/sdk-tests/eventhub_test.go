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

func TestEventHubsSDK_ARMAndAMQPRoundTrip(t *testing.T) {
	rg := "eh-sdk-rg"
	ns := "sdk-eventhub-ns"
	hub := "sdkhub"
	group := "sdkcg"

	nsClient, err := armeventhub.NewNamespacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	hubsClient, err := armeventhub.NewEventHubsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	groupsClient, err := armeventhub.NewConsumerGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := nsClient.BeginCreateOrUpdate(ctx, rg, ns, armeventhub.EHNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armeventhub.SKU{
			Name:     to.Ptr(armeventhub.SKUNameStandard),
			Tier:     to.Ptr(armeventhub.SKUTierStandard),
			Capacity: to.Ptr[int32](1),
		},
		Tags: map[string]*string{"env": to.Ptr("sdk")},
	}, nil)
	require.NoError(t, err)
	createdNS, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createdNS.Name)
	assert.Equal(t, ns, *createdNS.Name)
	require.NotNil(t, createdNS.Properties.ServiceBusEndpoint)
	assert.Contains(t, *createdNS.Properties.ServiceBusEndpoint, ns+".servicebus.")
	require.NotNil(t, createdNS.Properties.ProvisioningState)
	assert.Equal(t, "Succeeded", *createdNS.Properties.ProvisioningState)
	require.NotNil(t, createdNS.Properties.Status)
	assert.Equal(t, "Active", *createdNS.Properties.Status)
	t.Cleanup(func() {
		deletePoller, err := nsClient.BeginDelete(context.Background(), rg, ns, nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(context.Background(), nil)
		}
	})

	createdHub, err := hubsClient.CreateOrUpdate(ctx, rg, ns, hub, armeventhub.Eventhub{
		Properties: &armeventhub.Properties{
			PartitionCount:         to.Ptr[int64](1),
			MessageRetentionInDays: to.Ptr[int64](1),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, createdHub.Properties.PartitionIDs)
	assert.Equal(t, []string{"0"}, ptrStrings(createdHub.Properties.PartitionIDs))

	createdGroup, err := groupsClient.CreateOrUpdate(ctx, rg, ns, hub, group, armeventhub.ConsumerGroup{
		Properties: &armeventhub.ConsumerGroupProperties{
			UserMetadata: to.Ptr("sdk group"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, group, *createdGroup.Name)

	conn := messagingConnectionString(ns, ns+".servicebus.localhost",
		eventHubsNamespaceKey(t, ns), "EntityPath="+hub)
	producer, err := azeventhubs.NewProducerClientFromConnectionString(conn, "", &azeventhubs.ProducerClientOptions{
		CustomEndpoint: sbAMQPEndpoint,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- test-only self-signed simulator certificate.
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = producer.Close(context.Background()) })

	props, err := producer.GetEventHubProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, hub, props.Name)
	assert.Equal(t, []string{"0"}, props.PartitionIDs)

	batch, err := producer.NewEventDataBatch(ctx, &azeventhubs.EventDataBatchOptions{
		PartitionID: to.Ptr("0"),
	})
	require.NoError(t, err)
	require.NoError(t, batch.AddEventData(&azeventhubs.EventData{Body: []byte("hello event hubs")}, nil))
	require.NoError(t, producer.SendEventDataBatch(ctx, batch, nil))

	consumer, err := azeventhubs.NewConsumerClientFromConnectionString(conn, "", group, &azeventhubs.ConsumerClientOptions{
		CustomEndpoint: sbAMQPEndpoint,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- test-only self-signed simulator certificate.
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumer.Close(context.Background()) })

	part, err := consumer.GetPartitionProperties(ctx, "0", nil)
	require.NoError(t, err)
	assert.False(t, part.IsEmpty)
	assert.Equal(t, int64(0), part.LastEnqueuedSequenceNumber)

	partitionClient, err := consumer.NewPartitionClient("0", &azeventhubs.PartitionClientOptions{
		StartPosition: azeventhubs.StartPosition{Earliest: to.Ptr(true)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = partitionClient.Close(context.Background()) })

	// Generous receive window: the event is already enqueued, so this returns as
	// soon as the partition delivers it; the wide timeout only guards against slow
	// AMQP link establishment on a loaded CI runner.
	receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	events, err := partitionClient.ReceiveEvents(receiveCtx, 1, nil)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, []byte("hello event hubs"), events[0].Body)
}

func ptrStrings(in []*string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, *v)
	}
	return out
}
