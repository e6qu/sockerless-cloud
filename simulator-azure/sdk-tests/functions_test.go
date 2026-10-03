package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/applicationinsights/armapplicationinsights"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// azureDeleteSite deletes a site, ignoring errors.
func azureDeleteSite(rg, name string) {
	url := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/sites/" + name + "?api-version=2023-12-01"
	req, _ := http.NewRequest("DELETE", url, nil)
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// azureCreateSite creates a resource group and a function app that names no
// container image.
func azureCreateSite(t *testing.T, rg, name string) {
	azureCreateContainerSite(t, rg, name, "", "", nil)
}

// azureCreateContainerSite creates a function app that runs image as a Linux
// custom container ("DOCKER|<image>"), with appCommandLine as its startup
// command and appSettings as its application settings; an empty value leaves
// the field out.
func azureCreateContainerSite(t *testing.T, rg, name, image, appCommandLine string, appSettings map[string]string) {
	t.Helper()
	rgBody := `{"location":"eastus"}`
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(rgBody))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	props := map[string]any{
		"serverFarmId": "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/test-plan",
	}
	siteConfig := map[string]any{}
	if image != "" {
		siteConfig["linuxFxVersion"] = "DOCKER|" + image
	}
	if appCommandLine != "" {
		siteConfig["appCommandLine"] = appCommandLine
	}
	if len(appSettings) > 0 {
		var settings []map[string]any
		for name, value := range appSettings {
			settings = append(settings, map[string]any{"name": name, "value": value})
		}
		siteConfig["appSettings"] = settings
	}
	if len(siteConfig) > 0 {
		props["siteConfig"] = siteConfig
	}
	site := map[string]any{
		"location":   "eastus",
		"kind":       "functionapp",
		"properties": props,
	}
	siteBody, _ := json.Marshal(site)
	siteReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/"+name+"?api-version=2023-12-01",
		strings.NewReader(string(siteBody)))
	siteReq.Header.Set("Content-Type", "application/json")
	siteReq.Header.Set("Authorization", simARMBearer)
	siteResp, err := http.DefaultClient.Do(siteReq)
	require.NoError(t, err)
	siteResp.Body.Close()
	require.Equal(t, http.StatusOK, siteResp.StatusCode)
}

// azureInvokeFunction POSTs to the site's /api/function and requires 200. The
// request goes to the simulator's TCP port with the Host header set to the
// site's `<name>.azurewebsites.net`, which is how App Service routes it.
func azureInvokeFunction(t *testing.T, siteName string) []byte {
	t.Helper()
	status, body := azureInvokeFunctionResponse(t, siteName)
	require.Equal(t, http.StatusOK, status, "invoke answered: %s", body)
	return body
}

// azureInvokeFunctionResponse invokes a function app and returns the status
// and body as they came, for a caller that waits on what the function answers.
func azureInvokeFunctionResponse(t *testing.T, siteName string) (int, []byte) {
	t.Helper()
	return azureSiteRequest(t, siteName, http.MethodPost, "/api/function", "{}")
}

// azureSiteRequest sends one request to the site's hostname.
func azureSiteRequest(t *testing.T, siteName, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = siteName + ".azurewebsites.net"
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, respBody
}

// siteContainerLog reads the site's retained container output through
// WebApps_GetWebSiteContainerLogs.
func siteContainerLog(t *testing.T, rg, name string) string {
	t.Helper()
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := client.GetWebSiteContainerLogs(ctx, rg, name, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(text)
}

// createAppInsightsComponent creates an Application Insights component,
// workspace-based when workspaceID names a workspace.
func createAppInsightsComponent(t *testing.T, rg, name, workspaceID string) armapplicationinsights.Component {
	t.Helper()
	ensureRG(t, rg)
	client, err := armapplicationinsights.NewComponentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	props := &armapplicationinsights.ComponentProperties{ApplicationType: to.Ptr(armapplicationinsights.ApplicationTypeWeb)}
	if workspaceID != "" {
		props.WorkspaceResourceID = to.Ptr(workspaceID)
	}
	resp, err := client.CreateOrUpdate(ctx, rg, name, armapplicationinsights.Component{
		Location: to.Ptr("eastus"), Kind: to.Ptr("web"), Properties: props,
	}, nil)
	require.NoError(t, err)
	return resp.Component
}

// The startup command (siteConfig.appCommandLine) is the container's command,
// and the image's own entrypoint runs it: container-command's ENTRYPOINT is
// the binary, and "serve 80 …" are its arguments. The request reaches the
// container verbatim on port 80, App Service's default.
func TestAzureFunctions_StartupCommandRunsOnTheImageEntrypoint(t *testing.T) {
	rg, name := "func-startup-rg", "startup-func-app"
	azureCreateContainerSite(t, rg, name, commandImageName, "serve 80 hello-from-azure", nil)
	defer azureDeleteSite(rg, name)

	assert.Equal(t, "hello-from-azure", string(azureInvokeFunction(t, name)))

	// The front end forwards every path and method on the site's hostname,
	// not one invoke route.
	status, body := azureSiteRequest(t, name, http.MethodGet, "/any/path?x=1", "")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "hello-from-azure", string(body))
}

// A site with no startup command runs its image's own ENTRYPOINT and CMD; a
// startup command set afterwards through the configuration API restarts the
// site with that command in place of the CMD.
func TestAzureFunctions_ImageCMDUntilAStartupCommandReplacesIt(t *testing.T) {
	rg, name := "func-imagecmd-rg", "imagecmd-func-app"
	azureCreateContainerSite(t, rg, name, servingImageName, "", nil)
	defer azureDeleteSite(rg, name)

	assert.Equal(t, "from-image-cmd", string(azureInvokeFunction(t, name)))

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	updated, err := client.UpdateConfiguration(ctx, rg, name, armappservice.SiteConfigResource{
		Properties: &armappservice.SiteConfig{AppCommandLine: to.Ptr("serve 80 from-startup-command")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Properties)
	assert.Equal(t, "serve 80 from-startup-command", *updated.Properties.AppCommandLine)
	assert.Equal(t, "DOCKER|"+servingImageName, *updated.Properties.LinuxFxVersion,
		"a configuration PATCH keeps the fields it does not carry")

	assert.Equal(t, "from-startup-command", string(azureInvokeFunction(t, name)))
}

// WEBSITES_PORT names the port the front end forwards to, and the platform
// tells the container that port in PORT.
func TestAzureFunctions_WebsitesPortRoutesRequests(t *testing.T) {
	rg, name := "func-port-rg", "port-func-app"
	azureCreateContainerSite(t, rg, name, commandImageName, "serve 8080 on-8080",
		map[string]string{"WEBSITES_PORT": "8080"})
	defer azureDeleteSite(rg, name)

	assert.Equal(t, "on-8080", string(azureInvokeFunction(t, name)))
}

// A container that exits without answering on its port fails the site's
// start: the front end answers 503, and the container's own output is in the
// site's log.
func TestAzureFunctions_ContainerThatExitsFailsTheSiteStart(t *testing.T) {
	rg, name := "func-fail-rg", "fail-func-app"
	azureCreateContainerSite(t, rg, name, commandImageName, "log site-start-output", nil)
	defer azureDeleteSite(rg, name)

	status, body := azureInvokeFunctionResponse(t, name)
	assert.Equal(t, http.StatusServiceUnavailable, status, "body: %s", body)
	assert.Contains(t, string(body), "exited before it answered on port 80")
	assert.Contains(t, siteContainerLog(t, rg, name), "site-start-output")
}

// What the site's container writes to stdout reaches the AppTraces of the
// Application Insights component its connection string names, in the
// workspace that component names, and no other workspace.
func TestAzureFunctions_ContainerOutputReachesTheConnectedComponent(t *testing.T) {
	rg, name := "func-out-rg", "out-func-app"
	ws := createLogWorkspace(t, rg, "func-out-ws")
	other := createLogWorkspace(t, rg, "func-out-other-ws")
	component := createAppInsightsComponent(t, rg, "func-out-insights", ws.id)
	azureCreateContainerSite(t, rg, name, commandImageName, "serve 80 real-azure-output", map[string]string{
		"APPLICATIONINSIGHTS_CONNECTION_STRING": *component.Properties.ConnectionString,
	})
	defer azureDeleteSite(rg, name)

	azureInvokeFunction(t, name)

	// The engine's log stream delivers the line after the response; App
	// Service offers no event for its arrival, so read the table until it does.
	query := `AppTraces | where AppRoleName == "` + name + `" and Message == "POST /api/function" | project Message`
	require.Eventually(t, func() bool {
		return len(queryWorkspace(t, ws.customerID, query).Tables[0].Rows) > 0
	}, 30*time.Second, 200*time.Millisecond, "the container's access-log line should reach the component's workspace")

	assert.Empty(t, queryWorkspace(t, other.customerID, query).Tables[0].Rows,
		"a workspace nothing names holds none of the site's traces")
	byApp := insightsRead(t, http.MethodPost, "/v1/apps/"+*component.Properties.AppID+"/query",
		`{"query":"AppTraces | where AppRoleName == \"`+name+`\" | project Message"}`)
	tables, _ := byApp["tables"].([]any)
	require.NotEmpty(t, tables)
	rows, _ := tables[0].(map[string]any)["rows"].([]any)
	assert.NotEmpty(t, rows, "the component's app id reads its traces from its workspace")
	assert.Contains(t, siteContainerLog(t, rg, name), "POST /api/function", "the site's own log keeps the line too")
}

func TestAzureFunctions_DefaultHostNameReachability(t *testing.T) {
	rg, name := "func-host-rg", "host-func-app"
	azureCreateContainerSite(t, rg, name, commandImageName, "serve 80 reached-by-hostname", nil)
	defer azureDeleteSite(rg, name)

	// Get function app to extract DefaultHostName
	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/"+name+"?api-version=2023-12-01",
		nil)
	getReq.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	var result map[string]any
	data, _ := io.ReadAll(getResp.Body)
	require.NoError(t, json.Unmarshal(data, &result))
	props := result["properties"].(map[string]any)
	defaultHostName := props["defaultHostName"].(string)
	require.NotEmpty(t, defaultHostName, "DefaultHostName should be set")

	// Real Azure: invoke goes to https://<site>.azurewebsites.net/api/function.
	// The sim hosts every site on the same port, so we connect to the sim's
	// TCP address but set Host = the site's hostname so the routing matches.
	invokeReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/api/function", strings.NewReader("{}"))
	invokeReq.Header.Set("Content-Type", "application/json")
	invokeReq.Host = defaultHostName
	invokeResp, err := http.DefaultClient.Do(invokeReq)
	require.NoError(t, err)
	defer invokeResp.Body.Close()
	invokeBody, err := io.ReadAll(invokeResp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, invokeResp.StatusCode)
	assert.Equal(t, "reached-by-hostname", string(invokeBody))
}

func TestSDK_Functions_CreateAndGet(t *testing.T) {
	rg := "sdk-func-create-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-func-app", armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/test-plan"),
			HTTPSOnly:    to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)

	site, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "sdk-func-app", *site.Name)
	assert.Equal(t, "eastus", *site.Location)

	// GET the same site
	getResp, err := client.Get(ctx, rg, "sdk-func-app", nil)
	require.NoError(t, err)
	assert.Equal(t, "sdk-func-app", *getResp.Name)
	assert.Equal(t, "Running", *getResp.Properties.State)
}

func TestSDK_Functions_ListByResourceGroup(t *testing.T) {
	rg := "sdk-func-list-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Create two function apps
	for _, name := range []string{"sdk-list-func-a", "sdk-list-func-b"} {
		p, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
			Location: to.Ptr("eastus"),
			Kind:     to.Ptr("functionapp"),
			Properties: &armappservice.SiteProperties{
				ServerFarmID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/plan"),
			},
		}, nil)
		require.NoError(t, err)
		_, err = p.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}

	// List sites by resource group
	pager := client.NewListByResourceGroupPager(rg, nil)
	var sites []*armappservice.Site
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		sites = append(sites, page.Value...)
	}

	names := make(map[string]bool)
	for _, s := range sites {
		names[*s.Name] = true
	}
	assert.True(t, names["sdk-list-func-a"], "func A should be in list")
	assert.True(t, names["sdk-list-func-b"], "func B should be in list")
}

func TestSDK_Functions_Delete(t *testing.T) {
	rg := "sdk-func-del-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Create site
	p, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-del-func", armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/plan"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = p.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Delete site
	_, err = client.Delete(ctx, rg, "sdk-del-func", nil)
	require.NoError(t, err)

	// GET should 404
	_, err = client.Get(ctx, rg, "sdk-del-func", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

func TestSDK_Functions_GetNonExistentSite(t *testing.T) {
	rg := "sdk-func-err-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	_, err = client.Get(ctx, rg, "nonexistent-site", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
