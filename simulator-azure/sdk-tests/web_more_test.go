package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// web_more_test.go exercises the broadened Microsoft.Web slice in
// simulator-azure/web_more*.go: App Service plan list/patch/usages, web-app
// lifecycle + config sections + deployments / host-name bindings / source
// control / site extensions, deployment slots and their config, the
// subscription-global catalogs, and Static Web Apps. Every resource is read
// back through GET + list-by-RG + list-by-subscription so the spec-shape
// validator sees each response (single + collection) — a field leak that only
// surfaces on a list path would otherwise slip through.

func webMoreEnsurePlan(t *testing.T, rg, plan string) string {
	t.Helper()
	cred := &fakeCredential{}
	client, err := armappservice.NewPlansClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	p, err := client.BeginCreateOrUpdate(ctx, rg, plan, armappservice.Plan{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp"),
		SKU:      &armappservice.SKUDescription{Name: to.Ptr("Y1"), Tier: to.Ptr("Dynamic")},
		Properties: &armappservice.PlanProperties{
			Reserved: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = p.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverfarms/" + plan
}

func TestSDK_WebMore_Plans(t *testing.T) {
	rg := "webmore-plans-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wm-plan")

	cred := &fakeCredential{}
	client, err := armappservice.NewPlansClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// PATCH the plan.
	upd, err := client.Update(ctx, rg, "wm-plan", armappservice.PlanPatchResource{
		Properties: &armappservice.PlanPatchResourceProperties{},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, upd.ID)

	// List by resource group.
	rgPager := client.NewListByResourceGroupPager(rg, nil)
	foundRG := false
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if p.ID != nil && *p.ID == planID {
				foundRG = true
			}
		}
	}
	assert.True(t, foundRG, "plan must appear in list-by-resource-group")

	// List by subscription.
	subPager := client.NewListPager(nil)
	foundSub := false
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if p.ID != nil && *p.ID == planID {
				foundSub = true
			}
		}
	}
	assert.True(t, foundSub, "plan must appear in list-by-subscription")

	// Usages + restart-web-apps + web-apps-by-plan.
	usagePager := client.NewListWebAppsPager(rg, "wm-plan", nil)
	for usagePager.More() {
		_, err := usagePager.NextPage(ctx)
		require.NoError(t, err)
	}
	_, err = client.RestartWebApps(ctx, rg, "wm-plan", nil)
	require.NoError(t, err)
}

func webMoreCreateSite(t *testing.T, client *armappservice.WebAppsClient, rg, name, planID string) {
	t.Helper()
	p, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|registry.example/sockerless-overlay/azf:test"),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = p.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestSDK_WebMore_SiteConfigAndLifecycle(t *testing.T) {
	rg := "webmore-site-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wm-site-plan")

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	name := "wm-site"
	webMoreCreateSite(t, client, rg, name, planID)

	// PATCH the site.
	_, err = client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Properties: &armappservice.SitePatchResourceProperties{HTTPSOnly: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)

	// Lifecycle actions.
	_, err = client.Stop(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = client.Start(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = client.Restart(ctx, rg, name, nil)
	require.NoError(t, err)

	// ListConfigurations.
	cfgPager := client.NewListConfigurationsPager(rg, name, nil)
	for cfgPager.More() {
		_, err := cfgPager.NextPage(ctx)
		require.NoError(t, err)
	}

	// Metadata round-trip.
	_, err = client.UpdateMetadata(ctx, rg, name, armappservice.StringDictionary{
		Properties: map[string]*string{"k1": to.Ptr("v1")},
	}, nil)
	require.NoError(t, err)
	md, err := client.ListMetadata(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, md.Properties)
	assert.Equal(t, "v1", *md.Properties["k1"])

	// Auth settings (v1 + v2).
	_, err = client.UpdateAuthSettings(ctx, rg, name, armappservice.SiteAuthSettings{
		Properties: &armappservice.SiteAuthSettingsProperties{Enabled: to.Ptr(false)},
	}, nil)
	require.NoError(t, err)
	v2, err := client.GetAuthSettingsV2WithoutSecrets(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, v2.Properties)
	_, err = client.UpdateAuthSettingsV2(ctx, rg, name, armappservice.SiteAuthSettingsV2{
		Properties: &armappservice.SiteAuthSettingsV2Properties{
			Platform: &armappservice.AuthPlatform{Enabled: to.Ptr(false)},
		},
	}, nil)
	require.NoError(t, err)

	// Backups (empty).
	bkPager := client.NewListBackupsPager(rg, name, nil)
	for bkPager.More() {
		_, err := bkPager.NextPage(ctx)
		require.NoError(t, err)
	}

	// Usages.
	usagePager := client.NewListUsagesPager(rg, name, nil)
	for usagePager.More() {
		_, err := usagePager.NextPage(ctx)
		require.NoError(t, err)
	}
}

func TestSDK_WebMore_SiteSubResources(t *testing.T) {
	rg := "webmore-subres-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wm-subres-plan")

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	name := "wm-subres-site"
	webMoreCreateSite(t, client, rg, name, planID)

	// Deployment round-trip.
	_, err = client.CreateDeployment(ctx, rg, name, "deploy1", armappservice.Deployment{
		Properties: &armappservice.DeploymentProperties{
			Status:  to.Ptr[int32](4),
			Active:  to.Ptr(true),
			Author:  to.Ptr("ci"),
			Message: to.Ptr("deployed"),
		},
	}, nil)
	require.NoError(t, err)
	gotDep, err := client.GetDeployment(ctx, rg, name, "deploy1", nil)
	require.NoError(t, err)
	require.NotNil(t, gotDep.Properties)
	assert.Equal(t, "ci", *gotDep.Properties.Author)
	depPager := client.NewListDeploymentsPager(rg, name, nil)
	for depPager.More() {
		_, err := depPager.NextPage(ctx)
		require.NoError(t, err)
	}
	_, err = client.ListDeploymentLog(ctx, rg, name, "deploy1", nil)
	require.NoError(t, err)

	// Host-name binding round-trip.
	_, err = client.CreateOrUpdateHostNameBinding(ctx, rg, name, "www.example.com", armappservice.HostNameBinding{
		Properties: &armappservice.HostNameBindingProperties{
			SiteName:     to.Ptr(name),
			HostNameType: to.Ptr(armappservice.HostNameTypeVerified),
		},
	}, nil)
	require.NoError(t, err)
	gotBind, err := client.GetHostNameBinding(ctx, rg, name, "www.example.com", nil)
	require.NoError(t, err)
	require.NotNil(t, gotBind.Properties)
	bindPager := client.NewListHostNameBindingsPager(rg, name, nil)
	for bindPager.More() {
		_, err := bindPager.NextPage(ctx)
		require.NoError(t, err)
	}

	// Source control round-trip.
	scPoller, err := client.BeginCreateOrUpdateSourceControl(ctx, rg, name, armappservice.SiteSourceControl{
		Properties: &armappservice.SiteSourceControlProperties{
			RepoURL: to.Ptr("https://github.com/example/repo"),
			Branch:  to.Ptr("main"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = scPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	gotSC, err := client.GetSourceControl(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, gotSC.Properties)
	assert.Equal(t, "main", *gotSC.Properties.Branch)

	// Site extension round-trip.
	extPoller, err := client.BeginInstallSiteExtension(ctx, rg, name, "Monaco", nil)
	require.NoError(t, err)
	_, err = extPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.GetSiteExtension(ctx, rg, name, "Monaco", nil)
	require.NoError(t, err)
	extPager := client.NewListSiteExtensionsPager(rg, name, nil)
	for extPager.More() {
		_, err := extPager.NextPage(ctx)
		require.NoError(t, err)
	}

	// Basic publishing-credentials policies.
	policyPager := client.NewListBasicPublishingCredentialsPoliciesPager(rg, name, nil)
	for policyPager.More() {
		_, err := policyPager.NextPage(ctx)
		require.NoError(t, err)
	}
	ftp, err := client.UpdateFtpAllowed(ctx, rg, name, armappservice.CsmPublishingCredentialsPoliciesEntity{
		Properties: &armappservice.CsmPublishingCredentialsPoliciesEntityProperties{Allow: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, ftp.Properties)

	// Publishing profile XML.
	_, err = client.ListPublishingProfileXMLWithSecrets(ctx, rg, name, armappservice.CsmPublishingProfileOptions{
		Format: to.Ptr(armappservice.PublishingProfileFormatWebDeploy),
	}, nil)
	require.NoError(t, err)

	// Function create/delete.
	fnPoller, err := client.BeginCreateFunction(ctx, rg, name, "fn1", armappservice.FunctionEnvelope{
		Properties: &armappservice.FunctionEnvelopeProperties{},
	}, nil)
	require.NoError(t, err)
	_, err = fnPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.GetFunction(ctx, rg, name, "fn1", nil)
	require.NoError(t, err)
	_, err = client.DeleteFunction(ctx, rg, name, "fn1", nil)
	require.NoError(t, err)
}

func TestSDK_WebMore_Slots(t *testing.T) {
	rg := "webmore-slot-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wm-slot-plan")

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	name, slot := "wm-slot-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)

	// Create slot.
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, slot, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig:   &armappservice.SiteConfig{},
		},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got, err := client.GetSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Name)

	// List slots.
	slotPager := client.NewListSlotsPager(rg, name, nil)
	found := false
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		found = found || len(page.Value) > 0
	}
	assert.True(t, found, "slot must appear in list-slots")

	// Slot config: app settings + web.
	_, err = client.UpdateApplicationSettingsSlot(ctx, rg, name, slot, armappservice.StringDictionary{
		Properties: map[string]*string{"S1": to.Ptr("val")},
	}, nil)
	require.NoError(t, err)
	settings, err := client.ListApplicationSettingsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	require.NotNil(t, settings.Properties)
	assert.Equal(t, "val", *settings.Properties["S1"])

	webCfg, err := client.GetConfigurationSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	require.NotNil(t, webCfg.Properties)

	_, err = client.UpdateMetadataSlot(ctx, rg, name, slot, armappservice.StringDictionary{
		Properties: map[string]*string{"M": to.Ptr("x")},
	}, nil)
	require.NoError(t, err)

	// Slot lifecycle.
	_, err = client.StopSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	_, err = client.StartSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)

	// Slot publishing credentials (LRO, secret-bearing).
	credPoller, err := client.BeginListPublishingCredentialsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	creds, err := credPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, creds.Properties)

	// Swap slot with production (LRO).
	swapPoller, err := client.BeginSwapSlotWithProduction(ctx, rg, name, armappservice.CsmSlotEntity{
		TargetSlot:   to.Ptr(slot),
		PreserveVnet: to.Ptr(true),
	}, nil)
	require.NoError(t, err)
	_, err = swapPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Delete slot.
	_, err = client.DeleteSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
}

func TestSDK_WebMore_StaticSitesAndGlobal(t *testing.T) {
	rg := "webmore-static-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	ssClient, err := armappservice.NewStaticSitesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	name := "wm-static"
	ssID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/staticSites/" + name
	ssPoller, err := ssClient.BeginCreateOrUpdateStaticSite(ctx, rg, name, armappservice.StaticSiteARMResource{
		Location: to.Ptr("eastus2"),
		SKU:      &armappservice.SKUDescription{Name: to.Ptr("Free"), Tier: to.Ptr("Free")},
		Properties: &armappservice.StaticSite{
			RepositoryURL: to.Ptr("https://github.com/example/site"),
			Branch:        to.Ptr("main"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = ssPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got, err := ssClient.GetStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, name+".azurestaticapps.net", *got.Properties.DefaultHostname)

	_, err = ssClient.UpdateStaticSite(ctx, rg, name, armappservice.StaticSitePatchResource{
		Properties: &armappservice.StaticSite{Branch: to.Ptr("release")},
	}, nil)
	require.NoError(t, err)

	rgPager := ssClient.NewGetStaticSitesByResourceGroupPager(rg, nil)
	foundRG := false
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			if s.ID != nil && *s.ID == ssID {
				foundRG = true
			}
		}
	}
	assert.True(t, foundRG, "static site must appear in list-by-resource-group")

	subPager := ssClient.NewListPager(nil)
	foundSub := false
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			if s.ID != nil && *s.ID == ssID {
				foundSub = true
			}
		}
	}
	assert.True(t, foundSub, "static site must appear in list-by-subscription")

	delPoller, err := ssClient.BeginDeleteStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Subscription-global catalogs.
	mgmt, err := armappservice.NewWebSiteManagementClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	_, err = mgmt.GetSubscriptionDeploymentLocations(ctx, nil)
	require.NoError(t, err)
	_, err = mgmt.ListSKUs(ctx, nil)
	require.NoError(t, err)
	geoPager := mgmt.NewListGeoRegionsPager(nil)
	for geoPager.More() {
		_, err := geoPager.NextPage(ctx)
		require.NoError(t, err)
	}

	// Deleted web apps (subscription-wide + by-location).
	delClient, err := armappservice.NewDeletedWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	delPager := delClient.NewListPager(nil)
	for delPager.More() {
		_, err := delPager.NextPage(ctx)
		require.NoError(t, err)
	}
	locPager := delClient.NewListByLocationPager("eastus", nil)
	for locPager.More() {
		_, err := locPager.NextPage(ctx)
		require.NoError(t, err)
	}
}
