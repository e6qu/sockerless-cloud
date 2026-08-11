package azure_sdk_test

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServiceBusARM_SDKNamespaceLifecycleAndMessagingSurfaces exercises the
// namespace list-by-subscription / PATCH / private endpoint connection, the
// disaster recovery + migration configuration, and the authorization-rule
// key surfaces through the official armservicebus SDK.
func TestServiceBusARM_SDKNamespaceLifecycleAndMessagingSurfaces(t *testing.T) {
	rg := "sb-msg-rg"
	ns := "sdk-sb-msg-ns"

	clientFactory, err := armservicebus.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := clientFactory.NewNamespacesClient()

	createPoller, err := namespaces.BeginCreateOrUpdate(ctx, rg, ns, armservicebus.SBNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armservicebus.SBSKU{
			Name: to.Ptr(armservicebus.SKUNameStandard),
			Tier: to.Ptr(armservicebus.SKUTierStandard),
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
	updated, err := namespaces.Update(ctx, rg, ns, armservicebus.SBNamespaceUpdateParameters{
		Tags: map[string]*string{"env": to.Ptr("prod"), "team": to.Ptr("messaging")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Tags)
	assert.Equal(t, "prod", *updated.Tags["env"])

	// RootManageSharedAccessKey is auto-provisioned; listKeys + regenerateKeys
	// return canonical AccessKeys shapes.
	keys, err := namespaces.ListKeys(ctx, rg, ns, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, keys.PrimaryKey)
	require.NotNil(t, keys.PrimaryConnectionString)
	assert.Equal(t, "RootManageSharedAccessKey", *keys.KeyName)

	regen, err := namespaces.RegenerateKeys(ctx, rg, ns, "RootManageSharedAccessKey", armservicebus.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armservicebus.KeyTypePrimaryKey),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, regen.PrimaryKey)
	require.NotNil(t, regen.SecondaryKey)

	// Private endpoint connection round-trip.
	pecClient := clientFactory.NewPrivateEndpointConnectionsClient()
	pec, err := pecClient.CreateOrUpdate(ctx, rg, ns, "pec1", armservicebus.PrivateEndpointConnection{
		Properties: &armservicebus.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armservicebus.ConnectionState{
				Status:      to.Ptr(armservicebus.PrivateLinkConnectionStatusApproved),
				Description: to.Ptr("approved by test"),
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, pec.Properties)
	assert.Equal(t, armservicebus.EndPointProvisioningStateSucceeded, *pec.Properties.ProvisioningState)

	gotPEC, err := pecClient.Get(ctx, rg, ns, "pec1", nil)
	require.NoError(t, err)
	require.NotNil(t, gotPEC.Properties.PrivateLinkServiceConnectionState)
	assert.Equal(t, armservicebus.PrivateLinkConnectionStatusApproved, *gotPEC.Properties.PrivateLinkServiceConnectionState.Status)

	pecPager := pecClient.NewListPager(rg, ns, nil)
	require.True(t, pecPager.More())
	pecPage, err := pecPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, pecPage.Value, 1)

	delPECPoller, err := pecClient.BeginDelete(ctx, rg, ns, "pec1", nil)
	require.NoError(t, err)
	_, err = delPECPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Disaster recovery configuration lifecycle.
	drClient := clientFactory.NewDisasterRecoveryConfigsClient()
	partner := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.ServiceBus/namespaces/sdk-sb-msg-secondary"
	dr, err := drClient.CreateOrUpdate(ctx, rg, ns, "sdkdr", armservicebus.ArmDisasterRecovery{
		Properties: &armservicebus.ArmDisasterRecoveryProperties{
			PartnerNamespace: to.Ptr(partner),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, dr.Properties)
	assert.Equal(t, armservicebus.ProvisioningStateDRSucceeded, *dr.Properties.ProvisioningState)
	assert.Equal(t, partner, *dr.Properties.PartnerNamespace)

	gotDR, err := drClient.Get(ctx, rg, ns, "sdkdr", nil)
	require.NoError(t, err)
	assert.Equal(t, armservicebus.RoleDisasterRecoveryPrimary, *gotDR.Properties.Role)

	drPager := drClient.NewListPager(rg, ns, nil)
	require.True(t, drPager.More())
	drPage, err := drPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, drPage.Value, 1)

	// DR alias surfaces the primary namespace's authorization rules.
	drRulePager := drClient.NewListAuthorizationRulesPager(rg, ns, "sdkdr", nil)
	require.True(t, drRulePager.More())
	drRulePage, err := drRulePager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, drRulePage.Value)

	drKeys, err := drClient.ListKeys(ctx, rg, ns, "sdkdr", "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, drKeys.PrimaryKey)

	_, err = drClient.BreakPairing(ctx, rg, ns, "sdkdr", nil)
	require.NoError(t, err)
	brokeDR, err := drClient.Get(ctx, rg, ns, "sdkdr", nil)
	require.NoError(t, err)
	assert.Equal(t, armservicebus.RoleDisasterRecoveryPrimaryNotReplicating, *brokeDR.Properties.Role)

	_, err = drClient.FailOver(ctx, rg, ns, "sdkdr", nil)
	require.NoError(t, err)

	_, err = drClient.Delete(ctx, rg, ns, "sdkdr", nil)
	require.NoError(t, err)

	// Migration configuration lifecycle.
	migClient := clientFactory.NewMigrationConfigsClient()
	migPoller, err := migClient.BeginCreateAndStartMigration(ctx, rg, ns, armservicebus.MigrationConfigurationNameDefault, armservicebus.MigrationConfigProperties{
		Properties: &armservicebus.MigrationConfigPropertiesProperties{
			TargetNamespace:   to.Ptr(partner),
			PostMigrationName: to.Ptr("sdk-sb-msg-premium"),
		},
	}, nil)
	require.NoError(t, err)
	mig, err := migPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, mig.Properties)
	assert.Equal(t, "sdk-sb-msg-premium", *mig.Properties.PostMigrationName)

	gotMig, err := migClient.Get(ctx, rg, ns, armservicebus.MigrationConfigurationNameDefault, nil)
	require.NoError(t, err)
	assert.Equal(t, partner, *gotMig.Properties.TargetNamespace)

	migPager := migClient.NewListPager(rg, ns, nil)
	require.True(t, migPager.More())
	migPage, err := migPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, migPage.Value, 1)

	_, err = migClient.Revert(ctx, rg, ns, armservicebus.MigrationConfigurationNameDefault, nil)
	require.NoError(t, err)

	_, err = migClient.CompleteMigration(ctx, rg, ns, armservicebus.MigrationConfigurationNameDefault, nil)
	require.NoError(t, err)

	_, err = migClient.Delete(ctx, rg, ns, armservicebus.MigrationConfigurationNameDefault, nil)
	require.NoError(t, err)
}
