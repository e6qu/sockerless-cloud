package azure_sdk_test

import (
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for what an App Service app runs: the connection strings in its
// environment, the stopped state, its persistent /home share, and swap with
// preview with the slot differences read:
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/connectionstrings
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/config/connectionstrings
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/stop
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/start
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/restart
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/applySlotConfig
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/resetSlotConfig
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/resetSlotConfig
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/slotsdiffs
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slotsdiffs

// filesHTTPCommand runs the container-command image as a server over its
// environment and the site's wwwroot.
const filesHTTPCommand = "files-http 80 /home/site/wwwroot"

// createFilesWebApp creates a Linux custom-container web app that runs
// filesHTTPCommand with its persistent /home share mounted.
func createFilesWebApp(t *testing.T, rg, name string, settings map[string]string) *armappservice.WebAppsClient {
	t.Helper()
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, name+"-plan")
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	t.Cleanup(func() { azureDeleteSite(rg, name) })
	all := map[string]string{"WEBSITES_ENABLE_APP_SERVICE_STORAGE": "true"}
	for k, v := range settings {
		all[k] = v
	}
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("app,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + commandImageName),
				AppCommandLine: to.Ptr(filesHTTPCommand),
				AppSettings:    slotSettings(all),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return client
}

// siteGet sends one GET to a site hostname and returns the status and body.
func siteGet(t *testing.T, host, path string) (int, string) {
	t.Helper()
	status, body := azureSiteRequest(t, host, http.MethodGet, path, "")
	return status, string(body)
}

func siteEnv(t *testing.T, host, name string) string {
	t.Helper()
	status, body := siteGet(t, host, "/env/"+name)
	require.Equal(t, http.StatusOK, status, "%s has no %s: %s", host, name, body)
	return body
}

func TestSDK_WebApps_ConnectionStringsReachTheWorkload(t *testing.T) {
	rg, name := "sdk-connstr-rg", "sdk-connstr-app"
	client := createFilesWebApp(t, rg, name, nil)

	_, err := client.UpdateConnectionStrings(ctx, rg, name, armappservice.ConnectionStringDictionary{
		Properties: map[string]*armappservice.ConnStringValueTypePair{
			"Orders":  {Value: to.Ptr("Server=tcp:orders.database.windows.net"), Type: to.Ptr(armappservice.ConnectionStringTypeSQLAzure)},
			"Ledger":  {Value: to.Ptr("Server=ledger;Database=books"), Type: to.Ptr(armappservice.ConnectionStringTypeSQLServer)},
			"Catalog": {Value: to.Ptr("Server=catalog.mysql"), Type: to.Ptr(armappservice.ConnectionStringTypeMySQL)},
			"Events":  {Value: to.Ptr("host=events.postgres"), Type: to.Ptr(armappservice.ConnectionStringTypePostgreSQL)},
			"Queue":   {Value: to.Ptr("Endpoint=sb://queue/"), Type: to.Ptr(armappservice.ConnectionStringTypeServiceBus)},
			"Cache":   {Value: to.Ptr("cache.redis:6380"), Type: to.Ptr(armappservice.ConnectionStringTypeRedisCache)},
			"Plain":   {Value: to.Ptr("anything"), Type: to.Ptr(armappservice.ConnectionStringTypeCustom)},
		},
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, "Server=tcp:orders.database.windows.net", siteEnv(t, name, "SQLAZURECONNSTR_Orders"))
	assert.Equal(t, "Server=ledger;Database=books", siteEnv(t, name, "SQLCONNSTR_Ledger"))
	assert.Equal(t, "Server=catalog.mysql", siteEnv(t, name, "MYSQLCONNSTR_Catalog"))
	assert.Equal(t, "host=events.postgres", siteEnv(t, name, "POSTGRESQLCONNSTR_Events"))
	assert.Equal(t, "Endpoint=sb://queue/", siteEnv(t, name, "SERVICEBUSCONNSTR_Queue"))
	assert.Equal(t, "cache.redis:6380", siteEnv(t, name, "REDISCACHECONNSTR_Cache"))
	assert.Equal(t, "anything", siteEnv(t, name, "CUSTOMCONNSTR_Plain"))

	// A connection-string change restarts the app on the new set.
	_, err = client.UpdateConnectionStrings(ctx, rg, name, armappservice.ConnectionStringDictionary{
		Properties: map[string]*armappservice.ConnStringValueTypePair{
			"Plain": {Value: to.Ptr("changed"), Type: to.Ptr(armappservice.ConnectionStringTypeCustom)},
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "changed", siteEnv(t, name, "CUSTOMCONNSTR_Plain"))
	status, _ := siteGet(t, name, "/env/SQLAZURECONNSTR_Orders")
	assert.Equal(t, http.StatusNotFound, status, "a removed connection string leaves the environment")
}

func TestSDK_WebApps_StoppedAppServesTheStoppedPage(t *testing.T) {
	rg, name := "sdk-stop-rg", "sdk-stop-app"
	client := createFilesWebApp(t, rg, name, nil)
	status, body := siteGet(t, name, "/")
	require.Equal(t, http.StatusOK, status, "%s", body)
	instances := func() int {
		n := 0
		pager := client.NewListInstanceIdentifiersPager(rg, name, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			n += len(page.Value)
		}
		return n
	}
	require.Equal(t, 1, instances())

	_, err := client.Stop(ctx, rg, name, nil)
	require.NoError(t, err)
	site, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "Stopped", *site.Properties.State)
	assert.Equal(t, 0, instances(), "a stopped app runs nothing")
	status, body = siteGet(t, name, "/")
	assert.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, body, "Error 403 - This web app is stopped.")
	assert.Equal(t, 0, instances(), "a request to a stopped app starts nothing")

	// An update leaves a stopped app stopped: state is read-only.
	_, err = client.UpdateConnectionStrings(ctx, rg, name, armappservice.ConnectionStringDictionary{
		Properties: map[string]*armappservice.ConnStringValueTypePair{},
	}, nil)
	require.NoError(t, err)
	status, _ = siteGet(t, name, "/")
	assert.Equal(t, http.StatusForbidden, status)

	_, err = client.Start(ctx, rg, name, nil)
	require.NoError(t, err)
	status, body = siteGet(t, name, "/")
	assert.Equal(t, http.StatusOK, status, "%s", body)
	assert.Equal(t, "files-http", body)
}

func TestSDK_WebApps_FilesTheAppWritesPersist(t *testing.T) {
	rg, name := "sdk-home-rg", "sdk-home-app"
	client := createFilesWebApp(t, rg, name, nil)

	status, body := azureSiteRequest(t, name, http.MethodPut, "/files/notes/written-by-the-app.txt", "kept")
	require.Equal(t, http.StatusCreated, status, "%s", body)

	_, err := client.Restart(ctx, rg, name, nil)
	require.NoError(t, err)
	status, got := siteGet(t, name, "/files/notes/written-by-the-app.txt")
	require.Equal(t, http.StatusOK, status, "%s", got)
	assert.Equal(t, "kept", got, "a file the app wrote survives a restart")

	// Kudu reads the same /home share the app writes.
	scm, auth := kuduSCM(t, client, rg, name)
	resp, data := kuduRequest(t, http.MethodGet, scm+"/api/vfs/site/wwwroot/notes/written-by-the-app.txt", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", data)
	assert.Equal(t, "kept", string(data))

	// A deployment writes beside the app's own files.
	resp, data = kuduRequest(t, http.MethodPost, scm+"/api/publish?type=static&path=deployed.txt",
		[]byte("deployed"), "application/octet-stream", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", data)
	status, got = siteGet(t, name, "/files/deployed.txt")
	require.Equal(t, http.StatusOK, status, "%s", got)
	assert.Equal(t, "deployed", got)
	status, got = siteGet(t, name, "/files/notes/written-by-the-app.txt")
	require.Equal(t, http.StatusOK, status, "%s", got)
	assert.Equal(t, "kept", got)
}

func TestSDK_WebApps_SwapWithPreviewAndSlotDifferences(t *testing.T) {
	rg, name, slot := "sdk-preview-rg", "sdk-preview-app", "staging"
	client := createFilesWebApp(t, rg, name, map[string]string{"STICKY": "production", "SWAPPED": "from-production"})
	slotHost := name + "-" + slot
	createSlot, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, slot, armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + commandImageName),
				AppCommandLine: to.Ptr(filesHTTPCommand),
				AppSettings: slotSettings(map[string]string{
					"WEBSITES_ENABLE_APP_SERVICE_STORAGE": "true", "STICKY": "staging", "SWAPPED": "from-staging",
				}),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = createSlot.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.UpdateConnectionStrings(ctx, rg, name, armappservice.ConnectionStringDictionary{
		Properties: map[string]*armappservice.ConnStringValueTypePair{
			"Db": {Value: to.Ptr("production-db"), Type: to.Ptr(armappservice.ConnectionStringTypeCustom)},
		},
	}, nil)
	require.NoError(t, err)
	_, err = client.UpdateConnectionStringsSlot(ctx, rg, name, slot, armappservice.ConnectionStringDictionary{
		Properties: map[string]*armappservice.ConnStringValueTypePair{
			"Db": {Value: to.Ptr("staging-db"), Type: to.Ptr(armappservice.ConnectionStringTypeCustom)},
		},
	}, nil)
	require.NoError(t, err)
	_, err = client.UpdateSlotConfigurationNames(ctx, rg, name, armappservice.SlotConfigNamesResource{
		Properties: &armappservice.SlotConfigNames{
			AppSettingNames:       []*string{to.Ptr("STICKY")},
			ConnectionStringNames: []*string{to.Ptr("Db")},
		},
	}, nil)
	require.NoError(t, err)
	status, _ := azureSiteRequest(t, slotHost, http.MethodPut, "/files/slot-marker.txt", "staging content")
	require.Equal(t, http.StatusCreated, status)

	toProduction := armappservice.CsmSlotEntity{TargetSlot: to.Ptr("production"), PreserveVnet: to.Ptr(true)}
	diffs := map[string]armappservice.SlotDifferenceProperties{}
	pager := client.NewListSlotDifferencesSlotPager(rg, name, slot, toProduction, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			diffs[*d.Properties.SettingType+"/"+*d.Properties.SettingName] = *d.Properties
		}
	}
	require.Contains(t, diffs, "AppSetting/SWAPPED")
	assert.Equal(t, "SettingsWillBeSwapped", *diffs["AppSetting/SWAPPED"].DiffRule)
	assert.Equal(t, "from-staging", *diffs["AppSetting/SWAPPED"].ValueInCurrentSlot)
	assert.Equal(t, "from-production", *diffs["AppSetting/SWAPPED"].ValueInTargetSlot)
	require.Contains(t, diffs, "AppSetting/STICKY")
	assert.Equal(t, "SettingsWillNotBeSwapped", *diffs["AppSetting/STICKY"].DiffRule)
	require.Contains(t, diffs, "ConnectionString/Db")
	assert.Equal(t, "staging-db", *diffs["ConnectionString/Db"].ValueInCurrentSlot)
	assert.Equal(t, "SettingsWillNotBeSwapped", *diffs["ConnectionString/Db"].DiffRule)
	assert.NotContains(t, diffs, "AppSetting/WEBSITES_ENABLE_APP_SERVICE_STORAGE", "a setting both slots hold alike is no difference")

	fromProduction := 0
	prodPager := client.NewListSlotDifferencesFromProductionPager(rg, name,
		armappservice.CsmSlotEntity{TargetSlot: to.Ptr(slot), PreserveVnet: to.Ptr(true)}, nil)
	for prodPager.More() {
		page, err := prodPager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			if *d.Properties.SettingName == "SWAPPED" {
				fromProduction++
				assert.Equal(t, "from-production", *d.Properties.ValueInCurrentSlot)
			}
		}
	}
	assert.Equal(t, 1, fromProduction)

	// The first phase: the slot runs on production's slot settings.
	_, err = client.ApplySlotConfigurationSlot(ctx, rg, name, slot, toProduction, nil)
	require.NoError(t, err)
	assert.Equal(t, "production", siteEnv(t, slotHost, "STICKY"))
	assert.Equal(t, "production-db", siteEnv(t, slotHost, "CUSTOMCONNSTR_Db"))
	assert.Equal(t, "from-staging", siteEnv(t, slotHost, "SWAPPED"), "a swappable setting stays with the content")
	stagingSettings, err := client.ListApplicationSettingsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	assert.Equal(t, "production", *stagingSettings.Properties["STICKY"])

	// Cancelling restores the slot's own settings.
	_, err = client.ResetSlotConfigurationSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	assert.Equal(t, "staging", siteEnv(t, slotHost, "STICKY"))
	assert.Equal(t, "staging-db", siteEnv(t, slotHost, "CUSTOMCONNSTR_Db"))

	// Preview again, then complete it with the swap; production's reset cancels
	// nothing once the swap completed the preview.
	_, err = client.ApplySlotConfigurationSlot(ctx, rg, name, slot, toProduction, nil)
	require.NoError(t, err)
	swap, err := client.BeginSwapSlot(ctx, rg, name, slot, toProduction, nil)
	require.NoError(t, err)
	_, err = swap.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.ResetProductionSlotConfig(ctx, rg, name, nil)
	require.NoError(t, err)

	status, got := siteGet(t, name, "/files/slot-marker.txt")
	require.Equal(t, http.StatusOK, status, "%s", got)
	assert.Equal(t, "staging content", got, "production serves the slot's content")
	assert.Equal(t, "production", siteEnv(t, name, "STICKY"))
	assert.Equal(t, "production-db", siteEnv(t, name, "CUSTOMCONNSTR_Db"))
	assert.Equal(t, "from-staging", siteEnv(t, name, "SWAPPED"))
	assert.Equal(t, "staging", siteEnv(t, slotHost, "STICKY"))
	assert.Equal(t, "staging-db", siteEnv(t, slotHost, "CUSTOMCONNSTR_Db"))
	assert.Equal(t, "from-production", siteEnv(t, slotHost, "SWAPPED"))
	stagingConns, err := client.ListConnectionStringsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	assert.Equal(t, "staging-db", *stagingConns.Properties["Db"].Value)
}
