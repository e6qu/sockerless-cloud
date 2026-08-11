package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kvTenantID = "11111111-1111-1111-1111-111111111111"

func ensureKVResourceGroup(t *testing.T, name string) {
	t.Helper()
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, name, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)
}

func createKVVault(t *testing.T, rg, name string) {
	t.Helper()
	client, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armkeyvault.VaultCreateOrUpdateParameters{
		Location: ptrStr("eastus"),
		Properties: &armkeyvault.VaultProperties{
			TenantID: ptrStr(kvTenantID),
			SKU: &armkeyvault.SKU{
				Family: ptr(armkeyvault.SKUFamilyA),
				Name:   ptr(armkeyvault.SKUNameStandard),
			},
			EnableRbacAuthorization: ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	resp, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, name, *resp.Name)
}

func TestKeyVaultMgmt_CheckNameAvailability(t *testing.T) {
	rg := "kv-checkname-rg"
	ensureKVResourceGroup(t, rg)
	client, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	resp, err := client.CheckNameAvailability(ctx, armkeyvault.VaultCheckNameAvailabilityParameters{
		Name: ptrStr("kv-fresh-unused-name"),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.NameAvailable)
	assert.True(t, *resp.NameAvailable)

	createKVVault(t, rg, "kv-name-taken")
	resp2, err := client.CheckNameAvailability(ctx, armkeyvault.VaultCheckNameAvailabilityParameters{
		Name: ptrStr("kv-name-taken"),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, resp2.NameAvailable)
	assert.False(t, *resp2.NameAvailable, "an existing vault name must be reported unavailable")
}

func TestKeyVaultMgmt_PrivateEndpointConnections(t *testing.T) {
	rg := "kv-pe-rg"
	ensureKVResourceGroup(t, rg)
	createKVVault(t, rg, "kv-pe-vault")

	pecClient, err := armkeyvault.NewPrivateEndpointConnectionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	putResp, err := pecClient.Put(ctx, rg, "kv-pe-vault", "pec1", armkeyvault.PrivateEndpointConnection{
		Properties: &armkeyvault.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armkeyvault.PrivateLinkServiceConnectionState{
				Status:      ptr(armkeyvault.PrivateEndpointServiceConnectionStatusApproved),
				Description: ptrStr("approved by test"),
			},
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "pec1", *putResp.Name)

	getResp, err := pecClient.Get(ctx, rg, "kv-pe-vault", "pec1", nil)
	require.NoError(t, err)
	assert.Equal(t, "pec1", *getResp.Name)

	listPager := pecClient.NewListByResourcePager(rg, "kv-pe-vault", nil)
	count := 0
	for listPager.More() {
		page, err := listPager.NextPage(ctx)
		require.NoError(t, err)
		count += len(page.Value)
	}
	assert.GreaterOrEqual(t, count, 1)

	delPoller, err := pecClient.BeginDelete(ctx, rg, "kv-pe-vault", "pec1", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestKeyVaultMgmt_PrivateLinkResources(t *testing.T) {
	rg := "kv-plr-rg"
	ensureKVResourceGroup(t, rg)
	createKVVault(t, rg, "kv-plr-vault")

	plrClient, err := armkeyvault.NewPrivateLinkResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := plrClient.ListByVault(ctx, rg, "kv-plr-vault", nil)
	require.NoError(t, err)
	groupFound := false
	for _, g := range resp.Value {
		if g.Name != nil && *g.Name == "vault" {
			groupFound = true
		}
	}
	assert.True(t, groupFound, "private link resource group 'vault' must be present")
}
