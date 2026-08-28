package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Managed HSM tail: soft delete and the deleted collection, purge and the
// protection that refuses it, name availability, private endpoint connections,
// private-link resources, and the regions listing. The SDK builds these URLs
// internally, so their literal wire paths are written down here:
//
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/deletedManagedHSMs
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedManagedHSMs/{name}
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedManagedHSMs/{name}/purge
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedManagedHSMs/{name}/purge/operation
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/checkMhsmNameAvailability
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs/{name}/privateEndpointConnections
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs/{name}/privateEndpointConnections/{privateEndpointConnectionName}
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs/{name}/privateEndpointConnections/{privateEndpointConnectionName}
//	DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs/{name}/privateEndpointConnections/{privateEndpointConnectionName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs/{name}/privateLinkResources
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs/{name}/regions
func TestManagedHSMs_SoftDeleteRecoverAndPurge(t *testing.T) {
	const rg = "managedhsm-tail-rg"
	ensureKVResourceGroup(t, rg)

	client, err := armkeyvault.NewManagedHsmsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	create := func(name string, purgeProtection bool) {
		poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armkeyvault.ManagedHsm{
			Location: to.Ptr("eastus"),
			SKU: &armkeyvault.ManagedHsmSKU{
				Family: to.Ptr(armkeyvault.ManagedHsmSKUFamilyB),
				Name:   to.Ptr(armkeyvault.ManagedHsmSKUNameStandardB1),
			},
			Properties: &armkeyvault.ManagedHsmProperties{
				TenantID:                  to.Ptr(kvTenantID),
				InitialAdminObjectIDs:     []*string{to.Ptr("22222222-2222-2222-2222-222222222222")},
				EnableSoftDelete:          to.Ptr(true),
				SoftDeleteRetentionInDays: to.Ptr(int32(7)),
				EnablePurgeProtection:     to.Ptr(purgeProtection),
			},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}

	// A name in use is unavailable; a name nothing holds is available.
	create("tail-hsm", false)
	availability, err := client.CheckMhsmNameAvailability(ctx,
		armkeyvault.CheckMhsmNameAvailabilityParameters{Name: to.Ptr("tail-hsm")}, nil)
	require.NoError(t, err)
	require.NotNil(t, availability.NameAvailable)
	assert.False(t, *availability.NameAvailable)

	availability, err = client.CheckMhsmNameAvailability(ctx,
		armkeyvault.CheckMhsmNameAvailabilityParameters{Name: to.Ptr("nothing-holds-this")}, nil)
	require.NoError(t, err)
	require.NotNil(t, availability.NameAvailable)
	assert.True(t, *availability.NameAvailable)

	// A pool exposes one private-link group and the region it lives in.
	links, err := client.NewListByResourceGroupPager(rg, nil).NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, links.Value, 1)

	// Deleting retires the pool: it leaves the live collection and appears in
	// the deleted one with a scheduled purge date.
	deletePoller, err := client.BeginDelete(ctx, rg, "tail-hsm", nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	live, err := client.NewListByResourceGroupPager(rg, nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, live.Value)

	deletedClient, err := armkeyvault.NewManagedHsmsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	deleted, err := deletedClient.NewListDeletedPager(nil).NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, deleted.Value, 1)
	require.NotNil(t, deleted.Value[0].Properties)
	assert.NotEmpty(t, *deleted.Value[0].Properties.ScheduledPurgeDate)

	got, err := deletedClient.GetDeleted(ctx, "tail-hsm", "eastus", nil)
	require.NoError(t, err)
	assert.Equal(t, "tail-hsm", *got.Name)

	// Purging destroys the record.
	purgePoller, err := deletedClient.BeginPurgeDeleted(ctx, "tail-hsm", "eastus", nil)
	require.NoError(t, err)
	_, err = purgePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	deleted, err = deletedClient.NewListDeletedPager(nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, deleted.Value)
}

// Purge protection exists to refuse the purge, so it must.
func TestManagedHSMs_PurgeProtectionRefusesThePurge(t *testing.T) {
	const rg = "managedhsm-protected-rg"
	ensureKVResourceGroup(t, rg)

	client, err := armkeyvault.NewManagedHsmsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "protected-hsm", armkeyvault.ManagedHsm{
		Location: to.Ptr("westus"),
		SKU: &armkeyvault.ManagedHsmSKU{
			Family: to.Ptr(armkeyvault.ManagedHsmSKUFamilyB),
			Name:   to.Ptr(armkeyvault.ManagedHsmSKUNameStandardB1),
		},
		Properties: &armkeyvault.ManagedHsmProperties{
			TenantID:              to.Ptr(kvTenantID),
			InitialAdminObjectIDs: []*string{to.Ptr("22222222-2222-2222-2222-222222222222")},
			EnableSoftDelete:      to.Ptr(true),
			EnablePurgeProtection: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	deletePoller, err := client.BeginDelete(ctx, rg, "protected-hsm", nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	_, err = client.BeginPurgeDeleted(ctx, "protected-hsm", "westus", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "purge protection")
}
