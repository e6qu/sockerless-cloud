package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for whether an app can be cloned:
//
//	POST .../sites/{name}/iscloneable
//	POST .../sites/{name}/slots/{slot}/iscloneable
//
// The answer is computed from the site, not stored on it: the App Service plan
// the site is placed on decides whether a clone is possible at all, and the
// deployment slots a clone would leave behind decide whether it is partial.
func TestSDK_WebApps_IsCloneable(t *testing.T) {
	rg := "sdk-cloneable-rg"
	ensureRG(t, rg)

	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	newPlan := func(name, sku, tier string) string {
		t.Helper()
		poller, err := plans.BeginCreateOrUpdate(ctx, rg, name, armappservice.Plan{
			Location: to.Ptr("eastus"),
			SKU:      &armappservice.SKUDescription{Name: to.Ptr(sku), Tier: to.Ptr(tier)},
		}, nil)
		require.NoError(t, err)
		created, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return *created.ID
	}

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	newSite := func(name, planID string) {
		t.Helper()
		poller, err := sites.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
			Location:   to.Ptr("eastus"),
			Properties: &armappservice.SiteProperties{ServerFarmID: to.Ptr(planID)},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}

	// A site on a Basic plan cannot be cloned, and the refusal names the tier
	// that blocks it.
	basic := newPlan("sdk-cloneable-basic-plan", "B1", "Basic")
	newSite("sdk-cloneable-basic", basic)

	blocked, err := sites.IsCloneable(ctx, rg, "sdk-cloneable-basic", nil)
	require.NoError(t, err)
	require.NotNil(t, blocked.Result)
	assert.Equal(t, armappservice.CloneAbilityResultNotCloneable, *blocked.Result)
	require.Len(t, blocked.BlockingCharacteristics, 1)
	assert.Contains(t, *blocked.BlockingCharacteristics[0].Description, "Basic")

	// The same site on a Premium plan can be. The plan is read at the time of
	// the question, so scaling the plan up changes the answer without the site
	// being touched.
	premium := newPlan("sdk-cloneable-premium-plan", "P1v3", "PremiumV3")
	newSite("sdk-cloneable-premium", premium)

	ok, err := sites.IsCloneable(ctx, rg, "sdk-cloneable-premium", nil)
	require.NoError(t, err)
	require.NotNil(t, ok.Result)
	assert.Equal(t, armappservice.CloneAbilityResultCloneable, *ok.Result)
	assert.Empty(t, ok.BlockingCharacteristics)
	assert.Empty(t, ok.UnsupportedFeatures)

	// A deployment slot is not copied to the clone, so a site that has one can
	// be cloned only in part, and the slot is named.
	slotPoller, err := sites.BeginCreateOrUpdateSlot(ctx, rg, "sdk-cloneable-premium", "staging",
		armappservice.Site{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	partial, err := sites.IsCloneable(ctx, rg, "sdk-cloneable-premium", nil)
	require.NoError(t, err)
	require.NotNil(t, partial.Result)
	assert.Equal(t, armappservice.CloneAbilityResultPartiallyCloneable, *partial.Result)
	require.Len(t, partial.UnsupportedFeatures, 1)
	assert.Contains(t, *partial.UnsupportedFeatures[0].Description, "staging")

	// The slot itself is inside a slot already, so it has none of its own and
	// is fully cloneable.
	slot, err := sites.IsCloneableSlot(ctx, rg, "sdk-cloneable-premium", "staging", nil)
	require.NoError(t, err)
	require.NotNil(t, slot.Result)
	assert.Equal(t, armappservice.CloneAbilityResultCloneable, *slot.Result)

	// A site that was never created is refused rather than answered.
	_, err = sites.IsCloneable(ctx, rg, "sdk-cloneable-absent", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

// A resource's health category is the one it matches in Microsoft's Resource
// Health Check policy file, which this project does not vendor. All six
// spellings say so rather than matching a site against a policy nobody has.
func TestSDK_ResourceHealthMetadata_DeclaresTheMissingPolicy(t *testing.T) {
	rg, name := "sdk-rhm-rg", "sdk-rhm-app"
	ensureRG(t, rg)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = sites.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armappservice.NewResourceHealthMetadataClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.NewListPager(nil).NextPage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Resource Health Check policy file")

	_, err = client.NewListByResourceGroupPager(rg, nil).NextPage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Resource Health Check policy file")

	_, err = client.NewListBySitePager(rg, name, nil).NextPage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Resource Health Check policy file")

	_, err = client.GetBySite(ctx, rg, name, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Resource Health Check policy file")

	_, err = client.NewListBySiteSlotPager(rg, name, "staging", nil).NextPage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Resource Health Check policy file")

	_, err = client.GetBySiteSlot(ctx, rg, name, "staging", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Resource Health Check policy file")
}
