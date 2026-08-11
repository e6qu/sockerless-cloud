package azure_sdk_test

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventHubsARM_SDKNamespaceLifecycleAndMessagingSurfaces exercises the
// Event Hubs namespace list-by-subscription / PATCH / private endpoint
// connection / network rule set surfaces plus the authorization-rule key
// operations through the official armeventhub SDK.
func TestEventHubsARM_SDKNamespaceLifecycleAndMessagingSurfaces(t *testing.T) {
	rg := "eh-msg-rg"
	ns := "sdk-eh-msg-ns"

	clientFactory, err := armeventhub.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := clientFactory.NewNamespacesClient()

	createPoller, err := namespaces.BeginCreateOrUpdate(ctx, rg, ns, armeventhub.EHNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armeventhub.SKU{
			Name: to.Ptr(armeventhub.SKUNameStandard),
			Tier: to.Ptr(armeventhub.SKUTierStandard),
		},
		Tags: map[string]*string{"env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		deletePoller, err := namespaces.BeginDelete(context.Background(), rg, ns, nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(context.Background(), nil)
		}
	})

	// List by subscription includes the namespace just created.
	subPager := namespaces.NewListPager(nil)
	foundInSub := false
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, n := range page.Value {
			if n.Name != nil && *n.Name == ns {
				foundInSub = true
			}
		}
	}
	assert.True(t, foundInSub, "namespace should appear in list-by-subscription")

	// PATCH the namespace tags.
	updated, err := namespaces.Update(ctx, rg, ns, armeventhub.EHNamespace{
		Tags: map[string]*string{"env": to.Ptr("prod"), "team": to.Ptr("messaging")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Tags)
	assert.Equal(t, "prod", *updated.Tags["env"])

	// Authorization-rule key surfaces on the auto-provisioned root rule.
	keys, err := namespaces.ListKeys(ctx, rg, ns, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, keys.PrimaryConnectionString)
	assert.Equal(t, "RootManageSharedAccessKey", *keys.KeyName)

	regen, err := namespaces.RegenerateKeys(ctx, rg, ns, "RootManageSharedAccessKey", armeventhub.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armeventhub.KeyTypePrimaryKey),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, regen.PrimaryKey)

	// Network rule set: default read, PUT update, list.
	defaultRules, err := namespaces.GetNetworkRuleSet(ctx, rg, ns, nil)
	require.NoError(t, err)
	require.NotNil(t, defaultRules.Properties)
	assert.Equal(t, armeventhub.DefaultActionAllow, *defaultRules.Properties.DefaultAction)

	updatedRules, err := namespaces.CreateOrUpdateNetworkRuleSet(ctx, rg, ns, armeventhub.NetworkRuleSet{
		Properties: &armeventhub.NetworkRuleSetProperties{
			DefaultAction:               to.Ptr(armeventhub.DefaultActionDeny),
			TrustedServiceAccessEnabled: to.Ptr(true),
			IPRules: []*armeventhub.NWRuleSetIPRules{
				{Action: to.Ptr(armeventhub.NetworkRuleIPActionAllow), IPMask: to.Ptr("203.0.113.0/24")},
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updatedRules.Properties)
	assert.Equal(t, armeventhub.DefaultActionDeny, *updatedRules.Properties.DefaultAction)
	require.Len(t, updatedRules.Properties.IPRules, 1)

	listRules, err := namespaces.ListNetworkRuleSet(ctx, rg, ns, nil)
	require.NoError(t, err)
	require.Len(t, listRules.Value, 1)
	assert.Equal(t, armeventhub.DefaultActionDeny, *listRules.Value[0].Properties.DefaultAction)

	// Private endpoint connection round-trip.
	pecClient := clientFactory.NewPrivateEndpointConnectionsClient()
	pec, err := pecClient.CreateOrUpdate(ctx, rg, ns, "pec1", armeventhub.PrivateEndpointConnection{
		Properties: &armeventhub.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armeventhub.ConnectionState{
				Status:      to.Ptr(armeventhub.PrivateLinkConnectionStatusApproved),
				Description: to.Ptr("approved by test"),
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, pec.Properties)
	assert.Equal(t, armeventhub.EndPointProvisioningStateSucceeded, *pec.Properties.ProvisioningState)

	gotPEC, err := pecClient.Get(ctx, rg, ns, "pec1", nil)
	require.NoError(t, err)
	require.NotNil(t, gotPEC.Properties.PrivateLinkServiceConnectionState)
	assert.Equal(t, armeventhub.PrivateLinkConnectionStatusApproved, *gotPEC.Properties.PrivateLinkServiceConnectionState.Status)

	pecPager := pecClient.NewListPager(rg, ns, nil)
	require.True(t, pecPager.More())
	pecPage, err := pecPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, pecPage.Value, 1)

	delPECPoller, err := pecClient.BeginDelete(ctx, rg, ns, "pec1", nil)
	require.NoError(t, err)
	_, err = delPECPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// DR alias surfaces the primary namespace's authorization rules.
	drClient := clientFactory.NewDisasterRecoveryConfigsClient()
	drRulePager := drClient.NewListAuthorizationRulesPager(rg, ns, "sdkdr", nil)
	require.True(t, drRulePager.More())
	drRulePage, err := drRulePager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, drRulePage.Value)

	drRule, err := drClient.GetAuthorizationRule(ctx, rg, ns, "sdkdr", "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, drRule.Name)
	assert.Equal(t, "RootManageSharedAccessKey", *drRule.Name)

	drKeys, err := drClient.ListKeys(ctx, rg, ns, "sdkdr", "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, drKeys.PrimaryKey)
}
