package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventGrid_EventSubscriptionFilterRoundTrip covers the previously-untested
// event-subscription filter surface (subject filter + advanced filter) that
// azurerm_eventgrid_event_subscription configures and reads back.
func TestEventGrid_EventSubscriptionFilterRoundTrip(t *testing.T) {
	rg := "sdk-eg-filter-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	topics, err := armeventgrid.NewTopicsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	subs, err := armeventgrid.NewEventSubscriptionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	const topicName = "filter-topic"
	tp, err := topics.BeginCreateOrUpdate(ctx, rg, topicName, armeventgrid.Topic{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = tp.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.EventGrid/topics/" + topicName
	subPoller, err := subs.BeginCreateOrUpdate(ctx, scope, "filter-sub", armeventgrid.EventSubscription{
		Properties: &armeventgrid.EventSubscriptionProperties{
			Destination: &armeventgrid.WebHookEventSubscriptionDestination{
				EndpointType: to.Ptr(armeventgrid.EndpointTypeWebHook),
				Properties: &armeventgrid.WebHookEventSubscriptionDestinationProperties{
					EndpointURL: to.Ptr("https://example.test/hook"),
				},
			},
			Filter: &armeventgrid.EventSubscriptionFilter{
				SubjectBeginsWith:  to.Ptr("/blobServices/default/containers/in"),
				SubjectEndsWith:    to.Ptr(".jpg"),
				IncludedEventTypes: []*string{to.Ptr("Microsoft.Storage.BlobCreated")},
				AdvancedFilters: []armeventgrid.AdvancedFilterClassification{
					&armeventgrid.StringInAdvancedFilter{
						OperatorType: to.Ptr(armeventgrid.AdvancedFilterOperatorTypeStringIn),
						Key:          to.Ptr("data.contentType"),
						Values:       []*string{to.Ptr("image/jpeg")},
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = subPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got, err := subs.Get(ctx, scope, "filter-sub", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.Filter, "event subscription filter must round-trip")
	assert.Equal(t, "/blobServices/default/containers/in", *got.Properties.Filter.SubjectBeginsWith)
	assert.Equal(t, ".jpg", *got.Properties.Filter.SubjectEndsWith)
	require.Len(t, got.Properties.Filter.IncludedEventTypes, 1)
	assert.Equal(t, "Microsoft.Storage.BlobCreated", *got.Properties.Filter.IncludedEventTypes[0])
	require.Len(t, got.Properties.Filter.AdvancedFilters, 1, "advanced filter must round-trip")
	adv, ok := got.Properties.Filter.AdvancedFilters[0].(*armeventgrid.StringInAdvancedFilter)
	require.True(t, ok, "advanced filter deserializes to its operatorType (StringIn)")
	assert.Equal(t, "data.contentType", *adv.Key)
}
