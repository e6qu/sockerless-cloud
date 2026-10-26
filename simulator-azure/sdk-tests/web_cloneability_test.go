package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Whether an app can be cloned is computed, not stored: the plan decides
// whether a clone is possible, and the deployment slots a clone leaves behind
// decide whether it is partial.
//
//	POST .../sites/{name}/iscloneable
//	POST .../sites/{name}/slots/{slot}/iscloneable
func TestSDK_WebApps_IsCloneable(t *testing.T) {
	t.Parallel()
	sdkCloneablePremiumName := uniquename.New("sdk-cloneable-premium")
	sdkCloneableBasicName := uniquename.New("sdk-cloneable-basic")
	rg := uniquename.New("sdk-cloneable-rg")
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

	// A site on a Basic plan cannot be cloned, and the refusal names the tier.
	basic := newPlan("sdk-cloneable-basic-plan", "B1", "Basic")
	newSite(sdkCloneableBasicName, basic)

	blocked, err := sites.IsCloneable(ctx, rg, sdkCloneableBasicName, nil)
	require.NoError(t, err)
	require.NotNil(t, blocked.Result)
	assert.Equal(t, armappservice.CloneAbilityResultNotCloneable, *blocked.Result)
	require.Len(t, blocked.BlockingCharacteristics, 1)
	assert.Contains(t, *blocked.BlockingCharacteristics[0].Description, "Basic")

	// The same site on a Premium plan can be; the answer reads the plan at
	// question time.
	premium := newPlan("sdk-cloneable-premium-plan", "P1v3", "PremiumV3")
	newSite(sdkCloneablePremiumName, premium)

	ok, err := sites.IsCloneable(ctx, rg, sdkCloneablePremiumName, nil)
	require.NoError(t, err)
	require.NotNil(t, ok.Result)
	assert.Equal(t, armappservice.CloneAbilityResultCloneable, *ok.Result)
	assert.Empty(t, ok.BlockingCharacteristics)
	assert.Empty(t, ok.UnsupportedFeatures)

	// A clone does not copy deployment slots, so a site with one is cloneable in
	// part, naming the slot.
	slotPoller, err := sites.BeginCreateOrUpdateSlot(ctx, rg, sdkCloneablePremiumName, "staging",
		armappservice.Site{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	partial, err := sites.IsCloneable(ctx, rg, sdkCloneablePremiumName, nil)
	require.NoError(t, err)
	require.NotNil(t, partial.Result)
	assert.Equal(t, armappservice.CloneAbilityResultPartiallyCloneable, *partial.Result)
	require.Len(t, partial.UnsupportedFeatures, 1)
	assert.Contains(t, *partial.UnsupportedFeatures[0].Description, "staging")

	// The slot has no slots of its own, so it is fully cloneable.
	slot, err := sites.IsCloneableSlot(ctx, rg, sdkCloneablePremiumName, "staging", nil)
	require.NoError(t, err)
	require.NotNil(t, slot.Result)
	assert.Equal(t, armappservice.CloneAbilityResultCloneable, *slot.Result)

	// A site that does not exist is refused.
	_, err = sites.IsCloneable(ctx, rg, "sdk-cloneable-absent", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

// The App Service platform reads the simulator has no primitive for declare
// what is missing, not a bare 404 that reads as "no such API".
func TestSDK_WebApps_PlatformReadsDeclareWhatIsMissing(t *testing.T) {
	t.Parallel()
	rg, name := "sdk-webgap-rg", "sdk-webgap-app"
	ensureRG(t, rg)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)

	// PHP error logging and the process dump read the site's running workload, so
	// a never-started site has nothing to read for either.
	_, err = client.GetSitePhpErrorLogFlag(ctx, rg, name, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound",
		"a site with no running instance has no PHP worker to read a setting from")
	assert.NotContains(t, err.Error(), "NotImplemented")

	_, err = client.GetProcessDump(ctx, rg, name, "1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound",
		"a site with no running instance has no process to dump")
	assert.NotContains(t, err.Error(), "NotImplemented")

	// TestSDK_Web_RuntimeStackCatalogsListTheStacksSitesRun
	// covers the six runtime-stack catalog spellings.
}
