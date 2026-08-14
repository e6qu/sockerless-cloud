package azure_sdk_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Resources_List and Resources_ListByResourceGroup enumerate every tracked
// resource in their scope, whatever provider owns it, and honour the `$filter`
// forms real clients send. The Azure CLI reaches `az resource list -g <rg>`
// through `resourceGroup eq '<name>'` on the subscription-wide route rather
// than the group-scoped one, and terraform-provider-azurerm reads the same
// route with `resourceType eq 'Microsoft.KeyVault/vaults'` while populating
// its Key Vault cache. These tests drive both routes through the canonical
// armresources client.

// drainResourcePager walks a resource-list pager to completion. Both list
// operations answer the same page type, so one drain serves both.
func drainResourcePager[T any](t *testing.T, pager *runtime.Pager[T], values func(T) []*armresources.GenericResourceExpanded) []*armresources.GenericResourceExpanded {
	t.Helper()
	rows := make([]*armresources.GenericResourceExpanded, 0)
	pages := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		rows = append(rows, values(page)...)
		pages++
		require.LessOrEqual(t, pages, 200, "paging must terminate")
	}
	return rows
}

func listResources(t *testing.T, client *armresources.Client, opts *armresources.ClientListOptions) []*armresources.GenericResourceExpanded {
	t.Helper()
	return drainResourcePager(t, client.NewListPager(opts),
		func(p armresources.ClientListResponse) []*armresources.GenericResourceExpanded { return p.Value })
}

func listResourcesByGroup(t *testing.T, client *armresources.Client, rg string, opts *armresources.ClientListByResourceGroupOptions) []*armresources.GenericResourceExpanded {
	t.Helper()
	return drainResourcePager(t, client.NewListByResourceGroupPager(rg, opts),
		func(p armresources.ClientListByResourceGroupResponse) []*armresources.GenericResourceExpanded {
			return p.Value
		})
}

// resourceTypesOf collects the distinct, case-folded resource types of a list.
func resourceTypesOf(t *testing.T, rows []*armresources.GenericResourceExpanded) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, row := range rows {
		require.NotNil(t, row.ID)
		require.NotNil(t, row.Name)
		require.NotNil(t, row.Type)
		seen[strings.ToLower(*row.Type)] = true
	}
	types := make([]string, 0, len(seen))
	for name := range seen {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}

// seedResourceListScope provisions one resource group holding resources of two
// providers, each through its own API.
func seedResourceListScope(t *testing.T, rg string) {
	t.Helper()
	ensureKVResourceGroup(t, rg)
	createKVVault(t, rg, rg+"-vault")

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, rg+"-vnet", armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{to.Ptr("10.91.0.0/16")},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func resourceListVaultID(rg, name string) string {
	return "/subscriptions/" + subscriptionID +
		"/resourceGroups/" + rg + "/providers/Microsoft.KeyVault/vaults/" + name
}

func resourceListIDs(rows []*armresources.GenericResourceExpanded) map[string]bool {
	ids := map[string]bool{}
	for _, row := range rows {
		ids[strings.ToLower(*row.ID)] = true
	}
	return ids
}

func TestResourcesList_ScopesAndFilters(t *testing.T) {
	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const rgA = "reslist-a-rg"
	const rgB = "reslist-b-rg"
	seedResourceListScope(t, rgA)
	seedResourceListScope(t, rgB)

	// The unfiltered subscription list carries both groups' resources, and
	// more than one provider's worth — it is a resource list, not one
	// provider's cache.
	all := listResources(t, client, nil)
	ids := resourceListIDs(all)
	assert.True(t, ids[strings.ToLower(resourceListVaultID(rgA, rgA+"-vault"))])
	assert.True(t, ids[strings.ToLower(resourceListVaultID(rgB, rgB+"-vault"))])
	assert.GreaterOrEqual(t, len(resourceTypesOf(t, all)), 2)

	// resourceGroup eq — the filter `az resource list -g <rg>` sends.
	scoped := listResources(t, client, &armresources.ClientListOptions{
		Filter: to.Ptr("resourceGroup eq '" + rgA + "'"),
	})
	require.NotEmpty(t, scoped)
	for _, row := range scoped {
		assert.Contains(t, strings.ToLower(*row.ID), strings.ToLower("/resourcegroups/"+rgA+"/"),
			"a group-scoped list carries only that group's resources")
	}
	assert.GreaterOrEqual(t, len(resourceTypesOf(t, scoped)), 2,
		"a group-scoped list carries every provider's resources in the group")

	// resourceType eq — the filter terraform-provider-azurerm's Key Vault
	// cache sends.
	vaults := listResources(t, client, &armresources.ClientListOptions{
		Filter: to.Ptr("resourceType eq 'Microsoft.KeyVault/vaults'"),
	})
	require.NotEmpty(t, vaults)
	for _, row := range vaults {
		assert.Equal(t, "microsoft.keyvault/vaults", strings.ToLower(*row.Type))
	}
	assert.True(t, resourceListIDs(vaults)[strings.ToLower(resourceListVaultID(rgA, rgA+"-vault"))])

	// The two compose, the way the CLI composes `-g … --resource-type …`.
	both := listResources(t, client, &armresources.ClientListOptions{
		Filter: to.Ptr("resourceGroup eq '" + rgA + "' and resourceType eq 'Microsoft.KeyVault/vaults'"),
	})
	require.Len(t, both, 1)
	assert.Equal(t, strings.ToLower(resourceListVaultID(rgA, rgA+"-vault")), strings.ToLower(*both[0].ID))

	// name eq, and substringof(value, name).
	named := listResources(t, client, &armresources.ClientListOptions{
		Filter: to.Ptr("name eq '" + rgA + "-vault'"),
	})
	require.Len(t, named, 1)
	substring := listResources(t, client, &armresources.ClientListOptions{
		Filter: to.Ptr("substringof('" + rgA + "-va', name)"),
	})
	require.Len(t, substring, 1)
	assert.Equal(t, strings.ToLower(resourceListVaultID(rgA, rgA+"-vault")), strings.ToLower(*substring[0].ID))

	// ne excludes.
	excluded := listResources(t, client, &armresources.ClientListOptions{
		Filter: to.Ptr("resourceGroup eq '" + rgA + "' and resourceType ne 'Microsoft.KeyVault/vaults'"),
	})
	require.NotEmpty(t, excluded)
	for _, row := range excluded {
		assert.NotEqual(t, "microsoft.keyvault/vaults", strings.ToLower(*row.Type))
	}

	// location eq narrows by region; a region nothing sits in answers empty
	// rather than everything.
	assert.Empty(t, listResources(t, client, &armresources.ClientListOptions{
		Filter: to.Ptr("location eq 'antarctica-south'"),
	}))

	// A filter naming a property ARM does not filter on is refused. An
	// ignored filter answers with everything, which reads as a result.
	_, err = client.NewListPager(&armresources.ClientListOptions{
		Filter: to.Ptr("managedBy eq 'someone'"),
	}).NextPage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidFilterInQueryString")
}

func TestResourcesListByResourceGroup_ScopeExpandAndPaging(t *testing.T) {
	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const rg = "reslist-bygroup-rg"
	const other = "reslist-bygroup-other-rg"
	seedResourceListScope(t, rg)
	seedResourceListScope(t, other)

	rows := listResourcesByGroup(t, client, rg, nil)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Contains(t, strings.ToLower(*row.ID), strings.ToLower("/resourcegroups/"+rg+"/"))
		assert.Nil(t, row.ProvisioningState, "provisioningState is an $expand-only member")
	}
	assert.GreaterOrEqual(t, len(resourceTypesOf(t, rows)), 2)

	// $expand=provisioningState — the expansion terraform-provider-azurerm's
	// resource-group delete poller asks for — reports the state each resource
	// recorded for itself.
	expanded := listResourcesByGroup(t, client, rg, &armresources.ClientListByResourceGroupOptions{
		Expand: to.Ptr("provisioningState"),
	})
	require.NotEmpty(t, expanded)
	states := 0
	for _, row := range expanded {
		if row.ProvisioningState != nil && *row.ProvisioningState != "" {
			states++
		}
	}
	assert.Positive(t, states, "a resource that recorded a provisioning state reports it")

	// $top pages, and the continuation covers the rest of the group exactly
	// once — the paging the delete poller's $top=10 relies on.
	pager := client.NewListByResourceGroupPager(rg, &armresources.ClientListByResourceGroupOptions{
		Top: to.Ptr[int32](1),
	})
	seen := map[string]int{}
	pages := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(page.Value), 1, "a page never exceeds $top")
		for _, row := range page.Value {
			seen[strings.ToLower(*row.ID)]++
		}
		require.LessOrEqual(t, pages, len(rows)+1, "paging must terminate")
	}
	require.Len(t, seen, len(rows), "the paged walk covers the whole group")
	for id, count := range seen {
		assert.Equal(t, 1, count, "resource %s appeared more than once", id)
	}
}

// TestManagedHSMs_ListIsEmptyNotMissing pins the answer for a scope holding no
// Managed HSM pool: real Azure answers the empty collection, where an unrouted
// path answers 404. A provisioned pool then round-trips through its own API
// and appears in the generic resource list like any other tracked resource.
// Routes exercised:
//
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/managedHSMs
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs
//	PUT+GET+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs/{name}
func TestManagedHSMs_ListIsEmptyNotMissing(t *testing.T) {
	const rg = "managedhsm-rg"
	ensureKVResourceGroup(t, rg)

	client, err := armkeyvault.NewManagedHsmsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	byGroup, err := client.NewListByResourceGroupPager(rg, nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, byGroup.Value)

	bySubscription, err := client.NewListBySubscriptionPager(nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, bySubscription.Value)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sim-hsm", armkeyvault.ManagedHsm{
		Location: to.Ptr("eastus"),
		SKU: &armkeyvault.ManagedHsmSKU{
			Family: to.Ptr(armkeyvault.ManagedHsmSKUFamilyB),
			Name:   to.Ptr(armkeyvault.ManagedHsmSKUNameStandardB1),
		},
		Properties: &armkeyvault.ManagedHsmProperties{
			TenantID:              to.Ptr(kvTenantID),
			InitialAdminObjectIDs: []*string{to.Ptr("22222222-2222-2222-2222-222222222222")},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	t.Cleanup(func() {
		if deletePoller, err := client.BeginDelete(ctx, rg, "sim-hsm", nil); err == nil {
			_, _ = deletePoller.PollUntilDone(ctx, nil)
		}
	})

	got, err := client.Get(ctx, rg, "sim-hsm", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.HsmURI)
	assert.Contains(t, *got.Properties.HsmURI, "sim-hsm")

	after, err := client.NewListByResourceGroupPager(rg, nil).NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, after.Value, 1)

	resourcesClient, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	listed := listResources(t, resourcesClient, &armresources.ClientListOptions{
		Filter: to.Ptr("resourceType eq 'Microsoft.KeyVault/managedHSMs'"),
	})
	require.Len(t, listed, 1)
	assert.Equal(t, strings.ToLower(*created.ID), strings.ToLower(*listed[0].ID))
}
