package azure_sdk_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for App Service deployment slots running their own instances,
// and for the swap that exchanges what two slots run:
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/slotconfignames
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/config/web
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/slotsswap
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slotsswap
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/deploymentStatus/{deploymentStatusId}
//
// Each slot answers on its own hostname with its own container; a swap moves
// the content and the swappable settings, leaves the sticky ones, and fails —
// leaving both slots as they were — when the destination does not start.

// slotSettings builds an appSettings list.
func slotSettings(kv map[string]string) []*armappservice.NameValuePair {
	var out []*armappservice.NameValuePair
	for k, v := range kv {
		out = append(out, &armappservice.NameValuePair{Name: to.Ptr(k), Value: to.Ptr(v)})
	}
	return out
}

func slotServes(t *testing.T, host string) string {
	t.Helper()
	status, body := azureSiteRequest(t, host, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, status, "%s answered: %s", host, body)
	return string(body)
}

func TestSDK_WebApps_SlotRunsItsOwnInstanceAndSwaps(t *testing.T) {
	rg, name, slot := "sdk-slot-swap-rg", "sdk-slot-swap-app", "staging"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "sdk-slot-swap-plan")
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	t.Cleanup(func() { azureDeleteSite(rg, name) })

	create, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("app,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + commandImageName),
				AppCommandLine: to.Ptr("http 80 production-content"),
				AppSettings:    slotSettings(map[string]string{"SWAPPED": "from-production", "STICKY": "production"}),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = create.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	createSlot, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, slot, armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + commandImageName),
				AppCommandLine: to.Ptr("http 80 staging-content"),
				AppSettings:    slotSettings(map[string]string{"SWAPPED": "from-staging", "STICKY": "staging"}),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = createSlot.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, "production-content", slotServes(t, name))
	assert.Equal(t, "staging-content", slotServes(t, name+"-"+slot), "the slot's hostname reaches the slot's own container")

	// A deployment to the slot settles once the slot's own instance restarts.
	credsPoller, err := client.BeginListPublishingCredentialsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	creds, err := credsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotSite, err := client.GetSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	var slotScm string
	for _, s := range slotSite.Properties.HostNameSSLStates {
		if s.HostType != nil && *s.HostType == armappservice.HostTypeRepository {
			slotScm = "http://" + *s.Name
		}
	}
	require.NotEmpty(t, slotScm)
	basic := func(r *http.Request) {
		r.SetBasicAuth(*creds.Properties.PublishingUserName, *creds.Properties.PublishingPassword)
	}
	resp, body := kuduRequest(t, http.MethodPost, slotScm+"/api/publish?type=static&path=marker.txt",
		[]byte("deployed to staging"), "application/octet-stream", basic)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	deployment := kuduLatest(t, slotScm, basic)
	statusPoller, err := client.BeginGetSlotSiteDeploymentStatusSlot(ctx, rg, name, slot, deployment.ID, nil)
	require.NoError(t, err)
	runtime, err := statusPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.DeploymentBuildStatusRuntimeSuccessful, *runtime.Properties.Status)

	_, err = client.UpdateSlotConfigurationNames(ctx, rg, name, armappservice.SlotConfigNamesResource{
		Properties: &armappservice.SlotConfigNames{AppSettingNames: []*string{to.Ptr("STICKY")}},
	}, nil)
	require.NoError(t, err)

	// The swap the Azure CLI sends: the slot into production.
	swap, err := client.BeginSwapSlot(ctx, rg, name, slot, armappservice.CsmSlotEntity{
		TargetSlot: to.Ptr("production"), PreserveVnet: to.Ptr(true),
	}, nil)
	require.NoError(t, err)
	_, err = swap.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, "staging-content", slotServes(t, name), "production's hostname serves what the slot ran")
	assert.Equal(t, "production-content", slotServes(t, name+"-"+slot))
	prodSettings, err := client.ListApplicationSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "from-staging", *prodSettings.Properties["SWAPPED"], "a swappable setting moves with the content")
	assert.Equal(t, "production", *prodSettings.Properties["STICKY"], "a sticky setting stays with its slot")
	stagingSettings, err := client.ListApplicationSettingsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	assert.Equal(t, "from-production", *stagingSettings.Properties["SWAPPED"])
	assert.Equal(t, "staging", *stagingSettings.Properties["STICKY"])
	prod, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, prod.Properties.SlotSwapStatus)
	assert.Equal(t, slot, *prod.Properties.SlotSwapStatus.SourceSlotName)
	assert.Equal(t, "production", *prod.Properties.SlotSwapStatus.DestinationSlotName)
	assert.NotNil(t, prod.Properties.SlotSwapStatus.TimestampUTC)

	// The slot's deployed content moved to production with the rest of it.
	prodCredsPoller, err := client.BeginListPublishingCredentials(ctx, rg, name, nil)
	require.NoError(t, err)
	prodCreds, err := prodCredsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	prodScm := strings.Replace(slotScm, name+"-"+slot+".scm.", name+".scm.", 1)
	prodBasic := func(r *http.Request) {
		r.SetBasicAuth(*prodCreds.Properties.PublishingUserName, *prodCreds.Properties.PublishingPassword)
	}
	resp, body = kuduRequest(t, http.MethodGet, prodScm+"/api/vfs/site/wwwroot/marker.txt", nil, "", prodBasic)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Equal(t, "deployed to staging", string(body))
	resp, _ = kuduRequest(t, http.MethodGet, slotScm+"/api/vfs/site/wwwroot/marker.txt", nil, "", basic)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "the slot now holds production's former content")

	// The app-level spelling swaps the named slot into production: back again.
	swapBack, err := client.BeginSwapSlotWithProduction(ctx, rg, name, armappservice.CsmSlotEntity{
		TargetSlot: to.Ptr(slot), PreserveVnet: to.Ptr(true),
	}, nil)
	require.NoError(t, err)
	_, err = swapBack.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "production-content", slotServes(t, name))
	assert.Equal(t, "staging-content", slotServes(t, name+"-"+slot))

	// A slot whose container exits before it answers fails the swap, and
	// production keeps serving what it served.
	_, err = client.UpdateConfigurationSlot(ctx, rg, name, slot, armappservice.SiteConfigResource{
		Properties: &armappservice.SiteConfig{
			LinuxFxVersion: to.Ptr("DOCKER|" + commandImageName),
			AppCommandLine: to.Ptr("log exiting-at-once"),
		},
	}, nil)
	require.NoError(t, err)
	failing, err := client.BeginSwapSlot(ctx, rg, name, slot, armappservice.CsmSlotEntity{
		TargetSlot: to.Ptr("production"), PreserveVnet: to.Ptr(true),
	}, nil)
	require.NoError(t, err)
	_, err = failing.PollUntilDone(ctx, nil)
	require.Error(t, err, "a swap whose destination does not start fails")
	assert.Contains(t, err.Error(), "did not start")
	assert.Equal(t, "production-content", slotServes(t, name), "a failed swap leaves production as it was")
	prodSettings, err = client.ListApplicationSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "from-production", *prodSettings.Properties["SWAPPED"])
}
