package azure_sdk_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moveResourceToGroup runs Resources_ValidateMoveResources followed by
// Resources_MoveResources for one resource, the sequence a client performs
// before re-homing a resource across resource groups.
func moveResourceToGroup(t *testing.T, srcRG, dstRG, resourceID string) {
	t.Helper()
	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	move := armresources.MoveInfo{
		Resources:           []*string{to.Ptr(resourceID)},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + dstRG),
	}
	valPoller, err := client.BeginValidateMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = valPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	movePoller, err := client.BeginMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = movePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestAzureResources_MoveEventHubNamespace drives the Microsoft.EventHub move
// hook end to end. An Event Hubs namespace's Shared Access Signature keys are
// derived from each authorization rule's resource ID, which embeds the
// resource group, so the move pins them: the connection string captured before
// the move still sends and receives over AMQP afterwards, which is what a
// hook that only re-homed the ARM records would break.
func TestAzureResources_MoveEventHubNamespace(t *testing.T) {
	srcRG, dstRG := "eh-move-src-rg", "eh-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const namespace = "sdk-eh-move-ns"
	const hub = "eh-move-hub"
	const group = "$Default"

	factory, err := armeventhub.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := factory.NewNamespacesClient()
	createPoller, err := namespaces.BeginCreateOrUpdate(ctx, srcRG, namespace, armeventhub.EHNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armeventhub.SKU{
			Name: to.Ptr(armeventhub.SKUNameStandard),
			Tier: to.Ptr(armeventhub.SKUTierStandard),
		},
	}, nil)
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	namespaceID := *created.ID

	hubs := factory.NewEventHubsClient()
	_, err = hubs.CreateOrUpdate(ctx, srcRG, namespace, hub, armeventhub.Eventhub{
		Properties: &armeventhub.Properties{PartitionCount: to.Ptr[int64](1)},
	}, nil)
	require.NoError(t, err)
	groups := factory.NewConsumerGroupsClient()
	_, err = groups.CreateOrUpdate(ctx, srcRG, namespace, hub, group, armeventhub.ConsumerGroup{}, nil)
	require.NoError(t, err)

	// The credential an operator holds, captured before the move.
	keysBefore, err := namespaces.ListKeys(ctx, srcRG, namespace, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, keysBefore.PrimaryKey)
	require.NotNil(t, keysBefore.PrimaryConnectionString)
	connBefore := messagingConnectionString(namespace, namespace+".servicebus.localhost", *keysBefore.PrimaryKey, "EntityPath="+hub)

	producer := eventHubMoveProducer(t, connBefore)
	batch, err := producer.NewEventDataBatch(ctx, &azeventhubs.EventDataBatchOptions{PartitionID: to.Ptr("0")})
	require.NoError(t, err)
	require.NoError(t, batch.AddEventData(&azeventhubs.EventData{Body: []byte("sent before the move")}, nil))
	require.NoError(t, producer.SendEventDataBatch(ctx, batch, nil))

	moveResourceToGroup(t, srcRG, dstRG, namespaceID)

	moved, err := namespaces.Get(ctx, dstRG, namespace, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = namespaces.Get(ctx, srcRG, namespace, nil)
	require.Error(t, err, "the moved namespace must no longer resolve under the source group")

	movedHub, err := hubs.Get(ctx, dstRG, namespace, hub, nil)
	require.NoError(t, err)
	require.NotNil(t, movedHub.ID)
	assert.Contains(t, *movedHub.ID, "/resourceGroups/"+dstRG+"/")
	_, err = hubs.Get(ctx, srcRG, namespace, hub, nil)
	require.Error(t, err, "the moved event hub must no longer resolve under the source group")
	movedGroup, err := groups.Get(ctx, dstRG, namespace, hub, group, nil)
	require.NoError(t, err)
	require.NotNil(t, movedGroup.ID)

	keysAfter, err := namespaces.ListKeys(ctx, dstRG, namespace, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	assert.Equal(t, *keysBefore.PrimaryKey, *keysAfter.PrimaryKey,
		"a cross-group move must not rotate the namespace's primary key")
	assert.Equal(t, *keysBefore.PrimaryConnectionString, *keysAfter.PrimaryConnectionString,
		"the connection string an operator holds must survive the move verbatim")

	// The proof that matters: the pre-move credential still authenticates
	// against the data plane, reading the event published before the move and
	// completing a fresh round trip after it.
	consumer, err := azeventhubs.NewConsumerClientFromConnectionString(connBefore, "", group,
		&azeventhubs.ConsumerClientOptions{
			CustomEndpoint: sbAMQPEndpoint,
			TLSConfig: &tls.Config{
				InsecureSkipVerify: true, // #nosec G402 -- test-only self-signed simulator certificate.
			},
		})
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumer.Close(context.Background()) })
	partition, err := consumer.NewPartitionClient("0", &azeventhubs.PartitionClientOptions{
		StartPosition: azeventhubs.StartPosition{Earliest: to.Ptr(true)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = partition.Close(context.Background()) })
	receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	events, err := partition.ReceiveEvents(receiveCtx, 1, nil)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, []byte("sent before the move"), events[0].Body)

	after := eventHubMoveProducer(t, connBefore)
	afterBatch, err := after.NewEventDataBatch(ctx, &azeventhubs.EventDataBatchOptions{PartitionID: to.Ptr("0")})
	require.NoError(t, err)
	require.NoError(t, afterBatch.AddEventData(&azeventhubs.EventData{Body: []byte("sent after the move")}, nil))
	require.NoError(t, after.SendEventDataBatch(ctx, afterBatch, nil),
		"the pre-move connection string must still authenticate a send after the move")
}

func eventHubMoveProducer(t *testing.T, connectionString string) *azeventhubs.ProducerClient {
	t.Helper()
	producer, err := azeventhubs.NewProducerClientFromConnectionString(connectionString, "",
		&azeventhubs.ProducerClientOptions{
			CustomEndpoint: sbAMQPEndpoint,
			TLSConfig: &tls.Config{
				InsecureSkipVerify: true, // #nosec G402 -- test-only self-signed simulator certificate.
			},
		})
	require.NoError(t, err)
	t.Cleanup(func() { _ = producer.Close(context.Background()) })
	return producer
}

// TestAzureResources_MoveRedisCache drives the Microsoft.Cache move hook.
// Azure Cache for Redis exposes no data plane in this simulator, so the
// credential proof reaches as far as the surface that serves it: the access
// keys — the password a Redis client authenticates with — read back identical
// after the move, and the cache's children re-home with it.
func TestAzureResources_MoveRedisCache(t *testing.T) {
	srcRG, dstRG := "redis-move-src-rg", "redis-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const cache = "sdk-redis-move"
	client := newRedisCacheForTest(t, srcRG, cache)

	firewall, err := armredis.NewFirewallRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = firewall.CreateOrUpdate(ctx, srcRG, cache, "office", armredis.FirewallRule{
		Properties: &armredis.FirewallRuleProperties{
			StartIP: to.Ptr("203.0.113.1"),
			EndIP:   to.Ptr("203.0.113.9"),
		},
	}, nil)
	require.NoError(t, err)

	before, err := client.ListKeys(ctx, srcRG, cache, nil)
	require.NoError(t, err)
	require.NotNil(t, before.PrimaryKey)
	require.NotNil(t, before.SecondaryKey)

	got, err := client.Get(ctx, srcRG, cache, nil)
	require.NoError(t, err)
	require.NotNil(t, got.ID)

	moveResourceToGroup(t, srcRG, dstRG, *got.ID)

	moved, err := client.Get(ctx, dstRG, cache, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = client.Get(ctx, srcRG, cache, nil)
	require.Error(t, err, "the moved cache must no longer resolve under the source group")

	movedRule, err := firewall.Get(ctx, dstRG, cache, "office", nil)
	require.NoError(t, err)
	require.NotNil(t, movedRule.ID)
	assert.Contains(t, *movedRule.ID, "/resourceGroups/"+dstRG+"/")

	after, err := client.ListKeys(ctx, dstRG, cache, nil)
	require.NoError(t, err)
	assert.Equal(t, *before.PrimaryKey, *after.PrimaryKey,
		"a cross-group move must not rotate the cache's primary access key")
	assert.Equal(t, *before.SecondaryKey, *after.SecondaryKey,
		"a cross-group move must not rotate the cache's secondary access key")

	// The rotation contract is unchanged by the move.
	rotated, err := client.RegenerateKey(ctx, dstRG, cache, armredis.RegenerateKeyParameters{
		KeyType: to.Ptr(armredis.RedisKeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, rotated.PrimaryKey)
	assert.NotEqual(t, *before.PrimaryKey, *rotated.PrimaryKey)
}

// TestAzureResources_MoveContainerRegistry drives the
// Microsoft.ContainerRegistry move hook. The registry's admin passwords are
// derived from its resource ID, so the move pins them: listCredentials serves
// the same username and passwords afterwards, and the child subtree re-homes.
func TestAzureResources_MoveContainerRegistry(t *testing.T) {
	srcRG, dstRG := "acr-move-src-rg", "acr-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const registry = "sdkacrmove"
	registries, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	createPoller, err := registries.BeginCreate(ctx, srcRG, registry, armcontainerregistry.Registry{
		Location: to.Ptr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameBasic)},
		Properties: &armcontainerregistry.RegistryProperties{
			AdminUserEnabled: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	registryID := *created.ID

	rules, err := armcontainerregistry.NewCacheRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rulePoller, err := rules.BeginCreate(ctx, srcRG, registry, "alpine", armcontainerregistry.CacheRule{
		Properties: &armcontainerregistry.CacheRuleProperties{
			SourceRepository: to.Ptr("docker.io/library/alpine"),
			TargetRepository: to.Ptr("alpine"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = rulePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	before, err := registries.ListCredentials(ctx, srcRG, registry, nil)
	require.NoError(t, err)
	require.NotNil(t, before.Username)
	require.Len(t, before.Passwords, 2)

	moveResourceToGroup(t, srcRG, dstRG, registryID)

	moved, err := registries.Get(ctx, dstRG, registry, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = registries.Get(ctx, srcRG, registry, nil)
	require.Error(t, err, "the moved registry must no longer resolve under the source group")
	assert.NotNil(t, moved.Properties.LoginServer)

	movedRule, err := rules.Get(ctx, dstRG, registry, "alpine", nil)
	require.NoError(t, err)
	require.NotNil(t, movedRule.ID)
	assert.Contains(t, *movedRule.ID, "/resourceGroups/"+dstRG+"/")

	after, err := registries.ListCredentials(ctx, dstRG, registry, nil)
	require.NoError(t, err)
	assert.Equal(t, *before.Username, *after.Username, "a move must not change the admin username")
	require.Len(t, after.Passwords, 2)
	for i := range before.Passwords {
		assert.Equal(t, *before.Passwords[i].Value, *after.Passwords[i].Value,
			"a cross-group move must not rotate an admin password")
	}
}

// TestAzureResources_MoveEventGridTopic drives the Microsoft.EventGrid move
// hook. A topic's access keys are derived from its resource ID and its event
// subscriptions record that ID in their own `topic` property, so the move both
// pins the keys and rewrites the inbound reference: the key captured before
// the move still publishes afterwards, and the subscription answers under the
// destination scope.
func TestAzureResources_MoveEventGridTopic(t *testing.T) {
	srcRG, dstRG := "eg-move-src-rg", "eg-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const topicName = "sdk-eg-move"
	topics, err := armeventgrid.NewTopicsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	createPoller, err := topics.BeginCreateOrUpdate(ctx, srcRG, topicName, armeventgrid.Topic{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"origin": to.Ptr("resource-put")},
	}, nil)
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.Endpoint)
	topicID := *created.ID
	advertised := *created.Properties.Endpoint

	subs, err := armeventgrid.NewEventSubscriptionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subPoller, err := subs.BeginCreateOrUpdate(ctx, topicID, "eg-move-sub", armeventgrid.EventSubscription{
		Properties: &armeventgrid.EventSubscriptionProperties{
			Destination: &armeventgrid.WebHookEventSubscriptionDestination{
				EndpointType: to.Ptr(armeventgrid.EndpointTypeWebHook),
				Properties: &armeventgrid.WebHookEventSubscriptionDestinationProperties{
					EndpointURL: to.Ptr("http://127.0.0.1:1/unused"),
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	subCreated, err := subPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, subCreated.Properties)
	assert.Equal(t, topicID, *subCreated.Properties.Topic)

	keysBefore, err := topics.ListSharedAccessKeys(ctx, srcRG, topicName, nil)
	require.NoError(t, err)
	require.NotNil(t, keysBefore.Key1)

	publisher, err := azeventgrid.NewClientWithSharedKeyCredential(eventGridPublishEndpoint(t, advertised),
		azcore.NewKeyCredential(*keysBefore.Key1), eventGridPublisherOptions())
	require.NoError(t, err)
	_, err = publisher.PublishEvents(ctx, []azeventgrid.Event{eventGridTestEvent("eg-move-before")}, nil)
	require.NoError(t, err)

	moveResourceToGroup(t, srcRG, dstRG, topicID)

	moved, err := topics.Get(ctx, dstRG, topicName, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	assert.Equal(t, map[string]string{"origin": "resource-put"}, tagsOf(moved.Tags))
	_, err = topics.Get(ctx, srcRG, topicName, nil)
	require.Error(t, err, "the moved topic must no longer resolve under the source group")

	// The inbound reference the subscription holds moved with it.
	movedSub, err := subs.Get(ctx, *moved.ID, "eg-move-sub", nil)
	require.NoError(t, err)
	require.NotNil(t, movedSub.Properties)
	assert.Equal(t, *moved.ID, *movedSub.Properties.Topic,
		"the event subscription must name the moved topic, not its pre-move ID")
	_, err = subs.Get(ctx, topicID, "eg-move-sub", nil)
	require.Error(t, err, "the subscription must no longer answer under the pre-move scope")

	keysAfter, err := topics.ListSharedAccessKeys(ctx, dstRG, topicName, nil)
	require.NoError(t, err)
	assert.Equal(t, *keysBefore.Key1, *keysAfter.Key1,
		"a cross-group move must not rotate the topic's access key")
	assert.Equal(t, *keysBefore.Key2, *keysAfter.Key2)

	// The proof that matters: the key captured before the move still
	// authenticates a publish to the same endpoint afterwards.
	_, err = publisher.PublishEvents(ctx, []azeventgrid.Event{eventGridTestEvent("eg-move-after")}, nil)
	require.NoError(t, err, "the pre-move publishing credential must still authorize after the move")

	// The endpoint is derived from the topic's globally unique name, so it is
	// unchanged by the move.
	assert.Equal(t, advertised, *moved.Properties.Endpoint)

	t.Cleanup(func() {
		if deletePoller, err := topics.BeginDelete(context.Background(), dstRG, topicName, nil); err == nil {
			_, _ = deletePoller.PollUntilDone(context.Background(), nil)
		}
	})
}
