package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for a site's Resource Health metadata at all four scopes:
//
//	GET .../sites/{name}/resourceHealthMetadata
//	GET .../sites/{name}/resourceHealthMetadata/default
//	GET .../sites/{name}/slots/{slot}/resourceHealthMetadata
//	GET .../sites/{name}/slots/{slot}/resourceHealthMetadata/default
//	GET .../resourceGroups/{rg}/providers/Microsoft.Web/resourceHealthMetadata
//	GET .../providers/Microsoft.Web/resourceHealthMetadata
//
// The listings report the sites their scope actually holds, and the singleton
// is the same resource the site's own listing carries.
func TestSDK_ResourceHealthMetadata(t *testing.T) {
	rg := "sdk-rhm-rg"
	ensureRG(t, rg)

	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := plans.BeginCreateOrUpdate(ctx, rg, "sdk-rhm-plan", armappservice.Plan{
		Location: to.Ptr("eastus"),
		SKU:      &armappservice.SKUDescription{Name: to.Ptr("P1v3"), Tier: to.Ptr("PremiumV3")},
	}, nil)
	require.NoError(t, err)
	plan, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	sitePoller, err := sites.BeginCreateOrUpdate(ctx, rg, "sdk-rhm-site", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{ServerFarmID: plan.ID},
	}, nil)
	require.NoError(t, err)
	_, err = sitePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	slotPoller, err := sites.BeginCreateOrUpdateSlot(ctx, rg, "sdk-rhm-site", "staging", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{ServerFarmID: plan.ID},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	client, err := armappservice.NewResourceHealthMetadataClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// The site's own singleton. Nothing is running behind this site, so
	// Resource Health has no signal to read from it — the simulator reports
	// that rather than claiming one exists.
	single, err := client.GetBySite(ctx, rg, "sdk-rhm-site", nil)
	require.NoError(t, err)
	require.NotNil(t, single.Name)
	assert.Equal(t, "default", *single.Name)
	require.NotNil(t, single.Properties)
	require.NotNil(t, single.Properties.SignalAvailability)
	assert.False(t, *single.Properties.SignalAvailability)
	// The category is the one the site matches in Microsoft's Resource Health
	// Check policy file, which this simulator does not vendor, so it is absent
	// rather than invented.
	assert.Nil(t, single.Properties.Category)

	// The site's listing carries exactly that resource.
	var listed []*armappservice.ResourceHealthMetadata
	pager := client.NewListBySitePager(rg, "sdk-rhm-site", nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		listed = append(listed, page.Value...)
	}
	require.Len(t, listed, 1)
	assert.Equal(t, *single.ID, *listed[0].ID)

	// A slot is addressed in its own right.
	slotMetadata, err := client.GetBySiteSlot(ctx, rg, "sdk-rhm-site", "staging", nil)
	require.NoError(t, err)
	require.NotNil(t, slotMetadata.ID)
	assert.Contains(t, *slotMetadata.ID, "/slots/staging/resourceHealthMetadata/default")

	var slotListed []*armappservice.ResourceHealthMetadata
	slotPager := client.NewListBySiteSlotPager(rg, "sdk-rhm-site", "staging", nil)
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		slotListed = append(slotListed, page.Value...)
	}
	require.Len(t, slotListed, 1)
	assert.Equal(t, *slotMetadata.ID, *slotListed[0].ID)

	// The resource group's listing holds the site. A slot's metadata is
	// reached through its own site, so the scope listing counts the site once.
	var byGroup []*armappservice.ResourceHealthMetadata
	groupPager := client.NewListByResourceGroupPager(rg, nil)
	for groupPager.More() {
		page, err := groupPager.NextPage(ctx)
		require.NoError(t, err)
		byGroup = append(byGroup, page.Value...)
	}
	require.Len(t, byGroup, 1)
	assert.Equal(t, *single.ID, *byGroup[0].ID)

	// The subscription's listing reaches the same site through a wider scope.
	found := false
	subPager := client.NewListPager(nil)
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, item := range page.Value {
			if item.ID != nil && *item.ID == *single.ID {
				found = true
			}
		}
	}
	assert.True(t, found, "the subscription listing did not reach the site's metadata")

	// A site that does not exist is a 404, not an empty metadata resource.
	_, err = client.GetBySite(ctx, rg, "sdk-rhm-absent", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
