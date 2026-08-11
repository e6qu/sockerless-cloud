package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventGrid_ARMControlPlaneMore exercises the broader Microsoft.EventGrid
// ARM control plane the simulator serves beyond basic topic CRUD: the
// operations + topic-type registries, resource updates (PATCH) and key
// regeneration, the per-scope EventSubscription extensions (update /
// getFullUrl / getDeliveryAttributes), the subscription-/resource-group-/
// regional-/topic-type-scoped event subscription list views, event types by
// resource, and private link resources / private endpoint connections.
func TestEventGrid_ARMControlPlaneMore(t *testing.T) {
	rg := "sdk-eventgrid-more-rg"
	ensureRG(t, rg)
	cred := &fakeCredential{}
	const location = "eastus"

	// ---- Operations + topic types registries ----
	ops, err := armeventgrid.NewOperationsClient(cred, clientOpts())
	require.NoError(t, err)
	opPager := ops.NewListPager(nil)
	opCount := 0
	for opPager.More() {
		page, err := opPager.NextPage(ctx)
		require.NoError(t, err)
		opCount += len(page.Value)
	}
	assert.Greater(t, opCount, 0, "Operations_List returns the EventGrid operation catalog")

	topicTypes, err := armeventgrid.NewTopicTypesClient(cred, clientOpts())
	require.NoError(t, err)
	ttPager := topicTypes.NewListPager(nil)
	ttCount := 0
	for ttPager.More() {
		page, err := ttPager.NextPage(ctx)
		require.NoError(t, err)
		ttCount += len(page.Value)
	}
	assert.Greater(t, ttCount, 0)
	ttGet, err := topicTypes.Get(ctx, "Microsoft.EventGrid.Topics", nil)
	require.NoError(t, err)
	require.NotNil(t, ttGet.Properties)
	assert.Equal(t, "Microsoft.EventGrid", *ttGet.Properties.Provider)
	etPager := topicTypes.NewListEventTypesPager("Microsoft.Storage.StorageAccounts", nil)
	etCount := 0
	for etPager.More() {
		page, err := etPager.NextPage(ctx)
		require.NoError(t, err)
		etCount += len(page.Value)
	}
	assert.Greater(t, etCount, 0, "Microsoft.Storage.StorageAccounts has system event types")

	// ---- Topic: create → update → regenerate key ----
	topics, err := armeventgrid.NewTopicsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	topicName := "sdk-more-topic"
	createPoller, err := topics.BeginCreateOrUpdate(ctx, rg, topicName, armeventgrid.Topic{Location: to.Ptr(location)}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if p, err := topics.BeginDelete(ctx, rg, topicName, nil); err == nil {
			_, _ = p.PollUntilDone(ctx, nil)
		}
	})

	updPoller, err := topics.BeginUpdate(ctx, rg, topicName, armeventgrid.TopicUpdateParameters{
		Tags: map[string]*string{"team": to.Ptr("platform")},
	}, nil)
	require.NoError(t, err)
	updResp, err := updPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, updResp.Tags["team"])
	assert.Equal(t, "platform", *updResp.Tags["team"])

	regenPoller, err := topics.BeginRegenerateKey(ctx, rg, topicName, armeventgrid.TopicRegenerateKeyRequest{KeyName: to.Ptr("key1")}, nil)
	require.NoError(t, err)
	regenResp, err := regenPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, regenResp.Key1)
	require.NotNil(t, regenResp.Key2)
	// listKeys reflects the rotation, exactly as real EventGrid does: key1 is
	// the regenerated value, key2 is unchanged.
	keys, err := topics.ListSharedAccessKeys(ctx, rg, topicName, nil)
	require.NoError(t, err)
	assert.Equal(t, *keys.Key2, *regenResp.Key2)
	assert.Equal(t, *keys.Key1, *regenResp.Key1)

	// Topics_ListEventTypes (event types under the topic resource).
	tePager := topics.NewListEventTypesPager(rg, "Microsoft.EventGrid", "topics", topicName, nil)
	for tePager.More() {
		_, err := tePager.NextPage(ctx)
		require.NoError(t, err)
	}

	// ---- Domain: create → update → regenerate key ----
	domains, err := armeventgrid.NewDomainsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	domainName := "sdk-more-domain"
	dPoller, err := domains.BeginCreateOrUpdate(ctx, rg, domainName, armeventgrid.Domain{Location: to.Ptr(location)}, nil)
	require.NoError(t, err)
	_, err = dPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if p, err := domains.BeginDelete(ctx, rg, domainName, nil); err == nil {
			_, _ = p.PollUntilDone(ctx, nil)
		}
	})
	dUpd, err := domains.BeginUpdate(ctx, rg, domainName, armeventgrid.DomainUpdateParameters{
		Tags: map[string]*string{"env": to.Ptr("prod")},
	}, nil)
	require.NoError(t, err)
	dUpdResp, err := dUpd.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "prod", *dUpdResp.Tags["env"])
	dKeys, err := domains.RegenerateKey(ctx, rg, domainName, armeventgrid.DomainRegenerateKeyRequest{KeyName: to.Ptr("key2")}, nil)
	require.NoError(t, err)
	require.NotNil(t, dKeys.Key1)
	require.NotNil(t, dKeys.Key2)

	// ---- System topic: create → update ----
	systemTopics, err := armeventgrid.NewSystemTopicsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	systemTopicName := "sdk-more-systemtopic"
	source := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Storage/storageAccounts/sdkmorestg"
	stPoller, err := systemTopics.BeginCreateOrUpdate(ctx, rg, systemTopicName, armeventgrid.SystemTopic{
		Location: to.Ptr(location),
		Properties: &armeventgrid.SystemTopicProperties{
			Source:    to.Ptr(source),
			TopicType: to.Ptr("Microsoft.Storage.StorageAccounts"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = stPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if p, err := systemTopics.BeginDelete(ctx, rg, systemTopicName, nil); err == nil {
			_, _ = p.PollUntilDone(ctx, nil)
		}
	})
	stUpd, err := systemTopics.BeginUpdate(ctx, rg, systemTopicName, armeventgrid.SystemTopicUpdateParameters{
		Tags: map[string]*string{"owner": to.Ptr("eg")},
	}, nil)
	require.NoError(t, err)
	stUpdResp, err := stUpd.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "eg", *stUpdResp.Tags["owner"])

	// ---- Event subscription on a topic scope: create → update → getFullUrl
	//      → getDeliveryAttributes, then the scoped list views ----
	subs, err := armeventgrid.NewEventSubscriptionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.EventGrid/topics/" + topicName
	subName := "sdk-more-sub"
	const hookURL = "https://example.com/eventgrid-hook"
	esPoller, err := subs.BeginCreateOrUpdate(ctx, scope, subName, armeventgrid.EventSubscription{
		Properties: &armeventgrid.EventSubscriptionProperties{
			Destination: &armeventgrid.WebHookEventSubscriptionDestination{
				EndpointType: to.Ptr(armeventgrid.EndpointTypeWebHook),
				Properties: &armeventgrid.WebHookEventSubscriptionDestinationProperties{
					EndpointURL: to.Ptr(hookURL),
				},
			},
			EventDeliverySchema: to.Ptr(armeventgrid.EventDeliverySchemaEventGridSchema),
		},
	}, nil)
	require.NoError(t, err)
	_, err = esPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if p, err := subs.BeginDelete(ctx, scope, subName, nil); err == nil {
			_, _ = p.PollUntilDone(ctx, nil)
		}
	})

	esUpd, err := subs.BeginUpdate(ctx, scope, subName, armeventgrid.EventSubscriptionUpdateParameters{
		Labels: []*string{to.Ptr("blue")},
	}, nil)
	require.NoError(t, err)
	esUpdResp, err := esUpd.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, esUpdResp.Properties)
	require.Len(t, esUpdResp.Properties.Labels, 1)
	assert.Equal(t, "blue", *esUpdResp.Properties.Labels[0])

	fullURL, err := subs.GetFullURL(ctx, scope, subName, nil)
	require.NoError(t, err)
	require.NotNil(t, fullURL.EndpointURL)
	assert.Equal(t, hookURL, *fullURL.EndpointURL)

	_, err = subs.GetDeliveryAttributes(ctx, scope, subName, nil)
	require.NoError(t, err)

	// Scoped list views: each pager must surface the created subscription.
	globalSub := []*armeventgrid.EventSubscription{}
	for p := subs.NewListGlobalBySubscriptionPager(nil); p.More(); {
		page, err := p.NextPage(ctx)
		require.NoError(t, err)
		globalSub = append(globalSub, page.Value...)
	}
	assertSubPresent(t, "global-by-subscription", subName, globalSub)

	globalRG := []*armeventgrid.EventSubscription{}
	for p := subs.NewListGlobalByResourceGroupPager(rg, nil); p.More(); {
		page, err := p.NextPage(ctx)
		require.NoError(t, err)
		globalRG = append(globalRG, page.Value...)
	}
	assertSubPresent(t, "global-by-resource-group", subName, globalRG)

	regionalSub := []*armeventgrid.EventSubscription{}
	for p := subs.NewListRegionalBySubscriptionPager(location, nil); p.More(); {
		page, err := p.NextPage(ctx)
		require.NoError(t, err)
		regionalSub = append(regionalSub, page.Value...)
	}
	assertSubPresent(t, "regional-by-subscription", subName, regionalSub)

	regionalRG := []*armeventgrid.EventSubscription{}
	for p := subs.NewListRegionalByResourceGroupPager(rg, location, nil); p.More(); {
		page, err := p.NextPage(ctx)
		require.NoError(t, err)
		regionalRG = append(regionalRG, page.Value...)
	}
	assertSubPresent(t, "regional-by-resource-group", subName, regionalRG)

	forTopicType := []*armeventgrid.EventSubscription{}
	for p := subs.NewListGlobalBySubscriptionForTopicTypePager("Microsoft.EventGrid.Topics", nil); p.More(); {
		page, err := p.NextPage(ctx)
		require.NoError(t, err)
		forTopicType = append(forTopicType, page.Value...)
	}
	assertSubPresent(t, "global-by-subscription-for-topic-type", subName, forTopicType)

	byResource := []*armeventgrid.EventSubscription{}
	for p := subs.NewListByResourcePager(rg, "Microsoft.EventGrid", "topics", topicName, nil); p.More(); {
		page, err := p.NextPage(ctx)
		require.NoError(t, err)
		byResource = append(byResource, page.Value...)
	}
	assertSubPresent(t, "by-resource", subName, byResource)

	// ---- Private link resources + private endpoint connections ----
	plr, err := armeventgrid.NewPrivateLinkResourcesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	plrGet, err := plr.Get(ctx, rg, "topics", topicName, "topic", nil)
	require.NoError(t, err)
	require.NotNil(t, plrGet.Properties)
	assert.Equal(t, "topic", *plrGet.Properties.GroupID)
	plrPager := plr.NewListByResourcePager(rg, "topics", topicName, nil)
	plrCount := 0
	for plrPager.More() {
		page, err := plrPager.NextPage(ctx)
		require.NoError(t, err)
		plrCount += len(page.Value)
	}
	assert.Equal(t, 1, plrCount)

	pec, err := armeventgrid.NewPrivateEndpointConnectionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	pecPager := pec.NewListByResourcePager(rg, armeventgrid.PrivateEndpointConnectionsParentTypeTopics, topicName, nil)
	for pecPager.More() {
		page, err := pecPager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value, "topic has no private endpoint connections")
	}
}

func assertSubPresent(t *testing.T, view, subName string, subs []*armeventgrid.EventSubscription) {
	t.Helper()
	for _, s := range subs {
		if s.Name != nil && *s.Name == subName {
			return
		}
	}
	t.Fatalf("%s: event subscription %q not found in list view", view, subName)
}
