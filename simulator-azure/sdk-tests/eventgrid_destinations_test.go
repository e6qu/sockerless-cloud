package azure_sdk_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Event Grid delivers a topic's events to a Storage queue and to a Service Bus
// queue, one event per message: the queue message's text and the Service Bus
// message's body are the event's JSON.
func TestEventGrid_DeliversToAStorageQueueAndAServiceBusQueueSDK(t *testing.T) {
	t.Parallel()
	rg := uniqueName("sdk-eventgrid-dest-rg")
	ensureRG(t, rg)
	cred := &fakeCredential{}

	account := uniqueAlnumName("egdest")
	require.NotEmpty(t, storageAccountPrimaryKey(account))
	queues := queueSDKService(t, account)
	_, err := queues.CreateQueue(ctx, "events", nil)
	require.NoError(t, err)

	namespace := uniqueName("egdest")
	serviceBusNamespaceKey(t, namespace)
	sbQueues, err := armservicebus.NewQueuesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	_, err = sbQueues.CreateOrUpdate(ctx, "messaging-dataplane-rg", namespace, "events", armservicebus.SBQueue{}, nil)
	require.NoError(t, err)

	topics, err := armeventgrid.NewTopicsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	topicName := uniqueName("egdest-topic")
	topicPoller, err := topics.BeginCreateOrUpdate(ctx, rg, topicName, armeventgrid.Topic{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	topic, err := topicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	keys, err := topics.ListSharedAccessKeys(ctx, rg, topicName, nil)
	require.NoError(t, err)

	subs, err := armeventgrid.NewEventSubscriptionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.EventGrid/topics/" + topicName
	subscribe := func(name string, destination armeventgrid.EventSubscriptionDestinationClassification) {
		t.Helper()
		poller, err := subs.BeginCreateOrUpdate(ctx, scope, name, armeventgrid.EventSubscription{
			Properties: &armeventgrid.EventSubscriptionProperties{Destination: destination},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}
	subscribe("to-storage-queue", &armeventgrid.StorageQueueEventSubscriptionDestination{
		EndpointType: to.Ptr(armeventgrid.EndpointTypeStorageQueue),
		Properties: &armeventgrid.StorageQueueEventSubscriptionDestinationProperties{
			ResourceID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + storageCredResGroup +
				"/providers/Microsoft.Storage/storageAccounts/" + account),
			QueueName: to.Ptr("events"),
		},
	})
	subscribe("to-service-bus", &armeventgrid.ServiceBusQueueEventSubscriptionDestination{
		EndpointType: to.Ptr(armeventgrid.EndpointTypeServiceBusQueue),
		Properties: &armeventgrid.ServiceBusQueueEventSubscriptionDestinationProperties{
			ResourceID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/messaging-dataplane-rg" +
				"/providers/Microsoft.ServiceBus/namespaces/" + namespace + "/queues/events"),
		},
	})

	body := `[{"id":"evt-a","eventType":"sockerless.dest","subject":"/a","eventTime":"2026-05-27T00:00:00Z","data":{"n":1},"dataVersion":"1"},` +
		`{"id":"evt-b","eventType":"sockerless.dest","subject":"/b","eventTime":"2026-05-27T00:00:01Z","data":{"n":2},"dataVersion":"1"}]`
	req, err := http.NewRequest(http.MethodPost, *topic.Properties.Endpoint+"?api-version=2018-01-01", bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("aeg-sas-key", *keys.Key1)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	eventID := func(raw []byte) string {
		var event struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(raw, &event), string(raw))
		return event.ID
	}
	// Service Bus receives block until a message arrives.
	var fromServiceBus []string
	for range 2 {
		received := sbReq(t, http.MethodDelete, namespace, "/events/messages/head?timeout=30", nil, nil)
		require.Equal(t, http.StatusOK, received.StatusCode)
		raw, err := io.ReadAll(received.Body)
		require.NoError(t, err)
		_ = received.Body.Close()
		fromServiceBus = append(fromServiceBus, eventID(raw))
	}
	assert.ElementsMatch(t, []string{"evt-a", "evt-b"}, fromServiceBus)

	// The Queue service offers no wait, so its messages are read until both
	// arrive.
	var fromQueue []string
	require.Eventually(t, func() bool {
		got, err := queues.NewQueueClient("events").DequeueMessages(ctx, &azqueue.DequeueMessagesOptions{NumberOfMessages: to.Ptr[int32](32)})
		require.NoError(t, err)
		for _, m := range got.Messages {
			fromQueue = append(fromQueue, eventID([]byte(*m.MessageText)))
		}
		return len(fromQueue) == 2
	}, time.Minute, 200*time.Millisecond)
	assert.ElementsMatch(t, []string{"evt-a", "evt-b"}, fromQueue)
}
