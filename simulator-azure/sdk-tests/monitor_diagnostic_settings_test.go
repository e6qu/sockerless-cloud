package azure_sdk_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/monitor/azquery"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Monitor diagnostic settings route a resource's platform logs to the
// Log Analytics workspace they name, category by category.
//
//	PUT    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings/{name}
//	GET    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings/{name}
//	GET    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings
//	DELETE /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings/{name}
//	GET    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettingsCategories
//	GET    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettingsCategories/{name}

// queryWorkspaceRows runs a KQL query against a workspace through the azquery
// Logs client and returns the first table's rows.
func queryWorkspaceRows(t *testing.T, customerID, kql string) []azquery.Row {
	t.Helper()
	client, err := azquery.NewLogsClient(&fakeCredential{}, logsClientOpts())
	require.NoError(t, err)
	resp, err := client.QueryWorkspace(ctx, customerID, azquery.Body{Query: to.Ptr(kql)}, nil)
	require.NoError(t, err)
	if len(resp.Tables) == 0 {
		return nil
	}
	return resp.Tables[0].Rows
}

// A site's console lines reach the AppServiceConsoleLogs table of the
// workspace a diagnostic setting with that category names, and the setting
// round-trips with the categories the site offers.
func TestMonitor_DiagnosticSettingRoutesSiteConsoleLogs(t *testing.T) {
	t.Parallel()
	const rg, site = "monitor-diag-site-rg", "monitor-diag-site"
	ws := createLogWorkspace(t, rg, "monitor-diag-site-ws")
	// The container writes one access-log line per request to its stdout.
	azureCreateContainerSite(t, rg, site, commandImageName, "serve 80 diag-ok", nil)
	t.Cleanup(func() { azureDeleteSite(rg, site) })
	siteID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/sites/" + site

	categories, err := armmonitor.NewDiagnosticSettingsCategoryClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	var offered []string
	for pager := categories.NewListPager(siteID, nil); pager.More(); {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			offered = append(offered, *c.Name)
			assert.Equal(t, armmonitor.CategoryTypeLogs, *c.Properties.CategoryType)
		}
	}
	assert.Contains(t, offered, "AppServiceConsoleLogs")
	assert.Contains(t, offered, "AppServiceHTTPLogs")
	console, err := categories.Get(ctx, siteID, "AppServiceConsoleLogs", nil)
	require.NoError(t, err)
	assert.Equal(t, "AppServiceConsoleLogs", *console.Name)

	settings, err := armmonitor.NewDiagnosticSettingsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = settings.CreateOrUpdate(ctx, siteID, "unknown-category", armmonitor.DiagnosticSettingsResource{
		Properties: &armmonitor.DiagnosticSettings{
			WorkspaceID: to.Ptr(ws.id),
			Logs:        []*armmonitor.LogSettings{{Category: to.Ptr("NotACategory"), Enabled: to.Ptr(true)}},
		},
	}, nil)
	require.Error(t, err, "a category the site does not offer is refused")

	created, err := settings.CreateOrUpdate(ctx, siteID, "to-workspace", armmonitor.DiagnosticSettingsResource{
		Properties: &armmonitor.DiagnosticSettings{
			WorkspaceID: to.Ptr(ws.id),
			Logs: []*armmonitor.LogSettings{
				{Category: to.Ptr("AppServiceConsoleLogs"), Enabled: to.Ptr(true)},
				{Category: to.Ptr("AppServiceHTTPLogs"), Enabled: to.Ptr(false)},
			},
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, siteID+"/providers/Microsoft.Insights/diagnosticSettings/to-workspace", *created.ID)
	assert.Equal(t, "Microsoft.Insights/diagnosticSettings", *created.Type)

	got, err := settings.Get(ctx, siteID, "to-workspace", nil)
	require.NoError(t, err)
	assert.Equal(t, ws.id, *got.Properties.WorkspaceID)
	require.Len(t, got.Properties.Logs, 2)
	var names []string
	for pager := settings.NewListPager(siteID, nil); pager.More(); {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			names = append(names, *s.Name)
		}
	}
	assert.Equal(t, []string{"to-workspace"}, names)

	status, _ := azureSiteRequest(t, site, http.MethodGet, "/diag-request", "")
	require.Equal(t, http.StatusOK, status)
	// The platform's warm-up request reaches the container first, so the
	// query names the request this test sent.
	kql := fmt.Sprintf(`AppServiceConsoleLogs | where _ResourceId == "%s" and ResultDescription == "GET /diag-request" | project ResultDescription, Category, Type`, siteID)
	require.Eventually(t, func() bool { return len(queryWorkspaceRows(t, ws.customerID, kql)) > 0 },
		2*time.Minute, time.Second, "the console line never reached the workspace")
	row := queryWorkspaceRows(t, ws.customerID, kql)[0]
	assert.Equal(t, "GET /diag-request", row[0], "the access-log line the container wrote")
	assert.Equal(t, "AppServiceConsoleLogs", row[1])
	assert.Equal(t, "AppServiceConsoleLogs", row[2])

	_, err = settings.Delete(ctx, siteID, "to-workspace", nil)
	require.NoError(t, err)
	_, err = settings.Get(ctx, siteID, "to-workspace", nil)
	require.Error(t, err, "a deleted setting is gone")
}

// A Container Apps environment whose logs destination is azure-monitor sends
// its console and system logs to the resource-specific tables of the
// workspace its diagnostic setting names.
func TestMonitor_DiagnosticSettingRoutesEnvironmentLogs(t *testing.T) {
	t.Parallel()
	const rg, envName, app = "monitor-diag-env-rg", "monitor-diag-env", "monitor-diag-app"
	ws := createLogWorkspace(t, rg, "monitor-diag-env-ws")
	cred := &fakeCredential{}
	envs, err := armappcontainers.NewManagedEnvironmentsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	envPoller, err := envs.BeginCreateOrUpdate(ctx, rg, envName, armappcontainers.ManagedEnvironment{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ManagedEnvironmentProperties{
			AppLogsConfiguration: &armappcontainers.AppLogsConfiguration{Destination: to.Ptr("azure-monitor")},
		},
	}, nil)
	require.NoError(t, err)
	env, err := envPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	settings, err := armmonitor.NewDiagnosticSettingsClient(cred, clientOpts())
	require.NoError(t, err)
	_, err = settings.CreateOrUpdate(ctx, *env.ID, "to-workspace", armmonitor.DiagnosticSettingsResource{
		Properties: &armmonitor.DiagnosticSettings{
			WorkspaceID: to.Ptr(ws.id),
			Logs: []*armmonitor.LogSettings{
				{Category: to.Ptr("ContainerAppConsoleLogs"), Enabled: to.Ptr(true)},
				{Category: to.Ptr("ContainerAppSystemLogs"), Enabled: to.Ptr(true)},
			},
		},
	}, nil)
	require.NoError(t, err)

	apps, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	appPoller, err := apps.BeginCreateOrUpdate(ctx, rg, app, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: env.ID,
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{Name: to.Ptr("main"), Image: to.Ptr(evalImageName), Args: []*string{to.Ptr("6 * 7")}}},
				Scale:      &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = appPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if poller, err := apps.BeginDelete(ctx, rg, app, nil); err == nil {
			_, _ = poller.PollUntilDone(ctx, nil)
		}
	})

	consoleKQL := fmt.Sprintf(`ContainerAppConsoleLogs | where ContainerAppName == "%s" and Log == "42" | project Stream, EnvironmentName, Type`, app)
	require.Eventually(t, func() bool { return len(queryWorkspaceRows(t, ws.customerID, consoleKQL)) > 0 },
		2*time.Minute, time.Second, "the container's output never reached ContainerAppConsoleLogs")
	row := queryWorkspaceRows(t, ws.customerID, consoleKQL)[0]
	assert.Equal(t, "stdout", row[0])
	assert.Equal(t, envName, row[1])
	assert.Equal(t, "ContainerAppConsoleLogs", row[2])

	systemKQL := fmt.Sprintf(`ContainerAppSystemLogs | where ContainerAppName == "%s" and Reason == "AssigningReplica" | project EventSource, Type`, app)
	rows := queryWorkspaceRows(t, ws.customerID, systemKQL)
	require.NotEmpty(t, rows, "the replica's scheduling event never reached ContainerAppSystemLogs")
	assert.Equal(t, "ContainerAppController", rows[0][0])
	assert.Equal(t, "Normal", rows[0][1])

	logs, err := azquery.NewLogsClient(cred, logsClientOpts())
	require.NoError(t, err)
	_, err = logs.QueryWorkspace(ctx, ws.customerID, azquery.Body{Query: to.Ptr(`ContainerAppConsoleLogs_CL | take 1`)}, nil)
	require.Error(t, err, "an azure-monitor destination fills the resource-specific tables; the workspace has no custom log table")
}
