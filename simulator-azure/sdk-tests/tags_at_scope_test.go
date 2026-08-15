package azure_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tagsOf flattens the SDK's map[string]*string into a comparable map.
func tagsOf(tags map[string]*string) map[string]string {
	out := map[string]string{}
	for k, v := range tags {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

// TestAzureResources_TagsAtResourceScopeAreTheResourcesOwnTags holds the
// invariant Azure Resource Manager guarantees for tags: a resource has exactly
// one set of them, reachable both through Microsoft.Resources/tags/default at
// its scope and through the resource's own API, and a resource list reports
// the same set either way. The two surfaces are driven through their own
// canonical clients — armresources.TagsClient and armkeyvault.VaultsClient —
// so a divergence between the planes shows up as a client-visible
// disagreement, which is exactly how it would surface against real Azure.
func TestAzureResources_TagsAtResourceScopeAreTheResourcesOwnTags(t *testing.T) {
	rg := "tags-scope-rg"
	ensureRG(t, rg)

	const vault = "tags-scope-vault"
	vaults, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := vaults.BeginCreateOrUpdate(ctx, rg, vault, armkeyvault.VaultCreateOrUpdateParameters{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"origin": to.Ptr("resource-put")},
		Properties: &armkeyvault.VaultProperties{
			TenantID: to.Ptr(kvTenantID),
			SKU: &armkeyvault.SKU{
				Family: to.Ptr(armkeyvault.SKUFamilyA),
				Name:   to.Ptr(armkeyvault.SKUNameStandard),
			},
			EnableRbacAuthorization: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	vaultID := *created.ID

	tags, err := armresources.NewTagsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// The tag the resource's own create wrote is visible through tags/default.
	atScope, err := tags.GetAtScope(ctx, vaultID, nil)
	require.NoError(t, err)
	require.NotNil(t, atScope.Properties)
	assert.Equal(t, map[string]string{"origin": "resource-put"}, tagsOf(atScope.Properties.Tags),
		"tags/default must report the tags the resource itself holds")

	// Merge through tags/default; the resource's own GET sees it.
	merged, err := tags.UpdateAtScope(ctx, vaultID, armresources.TagsPatchResource{
		Operation:  to.Ptr(armresources.TagsPatchOperationMerge),
		Properties: &armresources.Tags{Tags: map[string]*string{"team": to.Ptr("platform")}},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"origin": "resource-put", "team": "platform"},
		tagsOf(merged.Properties.Tags))
	got, err := vaults.Get(ctx, rg, vault, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"origin": "resource-put", "team": "platform"}, tagsOf(got.Tags),
		"the resource's own GET must report the tags written through tags/default")

	// The generic resource list reads the same set.
	assert.Equal(t, map[string]string{"origin": "resource-put", "team": "platform"},
		listedResourceTags(t, rg, vaultID),
		"the resource list must report the tags written through tags/default")

	// A retag through the resource's own PATCH is visible through tags/default.
	_, err = vaults.Update(ctx, rg, vault, armkeyvault.VaultPatchParameters{
		Tags: map[string]*string{"origin": to.Ptr("resource-patch"), "team": to.Ptr("platform")},
	}, nil)
	require.NoError(t, err)
	atScope, err = tags.GetAtScope(ctx, vaultID, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"origin": "resource-patch", "team": "platform"},
		tagsOf(atScope.Properties.Tags),
		"tags/default must report a retag written through the resource's own API")

	// Replace drops what it does not name; Delete removes only the named keys.
	replaced, err := tags.UpdateAtScope(ctx, vaultID, armresources.TagsPatchResource{
		Operation:  to.Ptr(armresources.TagsPatchOperationReplace),
		Properties: &armresources.Tags{Tags: map[string]*string{"only": to.Ptr("this")}},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"only": "this"}, tagsOf(replaced.Properties.Tags))
	deleted, err := tags.UpdateAtScope(ctx, vaultID, armresources.TagsPatchResource{
		Operation:  to.Ptr(armresources.TagsPatchOperationDelete),
		Properties: &armresources.Tags{Tags: map[string]*string{"only": to.Ptr("this")}},
	}, nil)
	require.NoError(t, err)
	assert.Empty(t, tagsOf(deleted.Properties.Tags))

	// PUT sets the whole set; DELETE clears it, on the resource too.
	put, err := tags.CreateOrUpdateAtScope(ctx, vaultID, armresources.TagsResource{
		Properties: &armresources.Tags{Tags: map[string]*string{"env": to.Ptr("prod")}},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "prod"}, tagsOf(put.Properties.Tags))
	got, err = vaults.Get(ctx, rg, vault, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "prod"}, tagsOf(got.Tags))

	_, err = tags.DeleteAtScope(ctx, vaultID, nil)
	require.NoError(t, err)
	got, err = vaults.Get(ctx, rg, vault, nil)
	require.NoError(t, err)
	assert.Empty(t, tagsOf(got.Tags), "a tags/default DELETE must clear the resource's own tags")
	assert.Empty(t, listedResourceTags(t, rg, vaultID),
		"the resource list must agree that the tags are gone")
}

// TestAzureResources_TagsAtResourceGroupScopeAreTheGroupsOwnTags covers the
// other scope that owns a record of its own: `az tag` on a resource group and
// `az group show` are two spellings of one set.
func TestAzureResources_TagsAtResourceGroupScopeAreTheGroupsOwnTags(t *testing.T) {
	rg := "tags-scope-group-rg"
	groups, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created, err := groups.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"origin": to.Ptr("group-put")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.ID)
	scope := *created.ID

	tags, err := armresources.NewTagsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	atScope, err := tags.GetAtScope(ctx, scope, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"origin": "group-put"}, tagsOf(atScope.Properties.Tags),
		"tags/default must report the tags the group itself holds")

	_, err = tags.CreateOrUpdateAtScope(ctx, scope, armresources.TagsResource{
		Properties: &armresources.Tags{Tags: map[string]*string{"cost": to.Ptr("42")}},
	}, nil)
	require.NoError(t, err)
	got, err := groups.Get(ctx, rg, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"cost": "42"}, tagsOf(got.Tags),
		"the group's own GET must report the tags written through tags/default")
}

// TestAzureResources_TagsAtScopeRefusesAScopeHoldingNoResource pins the 404 a
// scope naming no resource answers. A scope resolves to exactly one holder of
// its tags or to none at all, so a write against a scope with no holder is
// refused rather than accepted into a plane nothing else reads.
func TestAzureResources_TagsAtScopeRefusesAScopeHoldingNoResource(t *testing.T) {
	rg := "tags-scope-404-rg"
	ensureRG(t, rg)

	tags, err := armresources.NewTagsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	for name, scope := range map[string]string{
		"no such resource": "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
			"/providers/Microsoft.KeyVault/vaults/tags-scope-never-created",
		"no such resource group": "/subscriptions/" + subscriptionID + "/resourceGroups/tags-scope-never-created",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tags.GetAtScope(ctx, scope, nil)
			require.Error(t, err, "reading tags at a scope holding no resource must fail")
			var respErr *azcore.ResponseError
			require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
			assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

			_, err = tags.CreateOrUpdateAtScope(ctx, scope, armresources.TagsResource{
				Properties: &armresources.Tags{Tags: map[string]*string{"a": to.Ptr("b")}},
			}, nil)
			require.Error(t, err, "writing tags at a scope holding no resource must fail")
			require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
			assert.Equal(t, http.StatusNotFound, respErr.StatusCode)
		})
	}
}

// listedResourceTags reads one resource's tags out of the generic
// resource-group-scoped list.
func listedResourceTags(t *testing.T, rg, resourceID string) map[string]string {
	t.Helper()
	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pager := client.NewListByResourceGroupPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, row := range page.Value {
			if row.ID != nil && *row.ID == resourceID {
				return tagsOf(row.Tags)
			}
		}
	}
	t.Fatalf("the resource list did not report %s", resourceID)
	return nil
}
