package azure_sdk_test

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads(t *testing.T) {
	rg := "sb-sdk-rg"
	ns := "sdk-servicebus-ns"

	clientFactory, err := armservicebus.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := clientFactory.NewNamespacesClient()
	queues := clientFactory.NewQueuesClient()

	poller, err := namespaces.BeginCreateOrUpdate(ctx, rg, ns, armservicebus.SBNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armservicebus.SBSKU{
			Name: to.Ptr(armservicebus.SKUNameStandard),
			Tier: to.Ptr(armservicebus.SKUTierStandard),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Name)
	assert.Equal(t, ns, *created.Name)
	t.Cleanup(func() {
		deletePoller, err := namespaces.BeginDelete(context.Background(), rg, ns, nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(context.Background(), nil)
		}
	})

	createdQueue, err := queues.CreateOrUpdate(ctx, rg, ns, "sdkqueue", armservicebus.SBQueue{
		Properties: &armservicebus.SBQueueProperties{
			MaxSizeInMegabytes: to.Ptr[int32](1024),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, createdQueue.Name)
	assert.Equal(t, "sdkqueue", *createdQueue.Name)

	defaultRules, err := namespaces.GetNetworkRuleSet(ctx, rg, ns, nil)
	require.NoError(t, err)
	require.NotNil(t, defaultRules.Properties)
	require.NotNil(t, defaultRules.Properties.DefaultAction)
	require.NotNil(t, defaultRules.Properties.PublicNetworkAccess)
	assert.Equal(t, armservicebus.DefaultActionAllow, *defaultRules.Properties.DefaultAction)
	assert.Equal(t, armservicebus.PublicNetworkAccessFlagEnabled, *defaultRules.Properties.PublicNetworkAccess)
	assert.Empty(t, defaultRules.Properties.IPRules)
	assert.Empty(t, defaultRules.Properties.VirtualNetworkRules)

	updatedRules, err := namespaces.CreateOrUpdateNetworkRuleSet(ctx, rg, ns, armservicebus.NetworkRuleSet{
		Properties: &armservicebus.NetworkRuleSetProperties{
			DefaultAction:               to.Ptr(armservicebus.DefaultActionDeny),
			PublicNetworkAccess:         to.Ptr(armservicebus.PublicNetworkAccessFlagDisabled),
			TrustedServiceAccessEnabled: to.Ptr(true),
			IPRules: []*armservicebus.NWRuleSetIPRules{
				{
					Action: to.Ptr(armservicebus.NetworkRuleIPActionAllow),
					IPMask: to.Ptr("203.0.113.0/24"),
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updatedRules.Properties)
	assert.Equal(t, armservicebus.DefaultActionDeny, *updatedRules.Properties.DefaultAction)
	assert.Equal(t, armservicebus.PublicNetworkAccessFlagDisabled, *updatedRules.Properties.PublicNetworkAccess)
	require.Len(t, updatedRules.Properties.IPRules, 1)
	assert.Equal(t, "203.0.113.0/24", *updatedRules.Properties.IPRules[0].IPMask)

	pager := namespaces.NewListNetworkRuleSetsPager(rg, ns, nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Value, 1)
	require.NotNil(t, page.Value[0].Name)
	assert.Equal(t, "default", *page.Value[0].Name)

	drPager := clientFactory.NewDisasterRecoveryConfigsClient().NewListPager(rg, ns, nil)
	require.True(t, drPager.More())
	drPage, err := drPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, drPage.Value)

	migrationPager := clientFactory.NewMigrationConfigsClient().NewListPager(rg, ns, nil)
	require.True(t, migrationPager.More())
	migrationPage, err := migrationPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, migrationPage.Value)

	_, err = clientFactory.NewMigrationConfigsClient().Get(ctx, rg, ns, armservicebus.MigrationConfigurationNameDefault, nil)
	require.Error(t, err)
	var responseErr *azcore.ResponseError
	require.ErrorAs(t, err, &responseErr)
	assert.Equal(t, "ResourceNotFound", responseErr.ErrorCode)
	assert.Equal(t, 404, responseErr.StatusCode)
}
