package azure_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
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

// azureCreateSite creates a resource group and function app, optionally with a
// command supplied via the real SOCKERLESS_CMD app-setting contract.
func azureCreateSite(t *testing.T, rg, name string, command []string) {
	azureCreateSiteWithImage(t, rg, name, command, "")
}

// azureCreateSiteWithImage creates a function app with a Docker image and an
// optional command. The command is delivered exactly as the azure-functions
// backend delivers it: a SOCKERLESS_CMD app setting carrying
// base64(json.Marshal(argv)) — the same contract a real invocation uses.
func azureCreateSiteWithImage(t *testing.T, rg, name string, command []string, image string) {
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
	if len(command) > 0 {
		cmdJSON, _ := json.Marshal(command)
		siteConfig["appSettings"] = []map[string]any{
			{"name": "SOCKERLESS_CMD", "value": base64.StdEncoding.EncodeToString(cmdJSON)},
		}
	}
	if image != "" {
		siteConfig["linuxFxVersion"] = "DOCKER|" + image
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

// azureInvokeFunction connects to the sim's TCP port but sets the Host header
// to the site's `<name>.azurewebsites.net` (real Azure routing).
func azureInvokeFunction(t *testing.T, siteName string) []byte {
	t.Helper()
	invokeReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/api/function",
		strings.NewReader("{}"))
	invokeReq.Header.Set("Content-Type", "application/json")
	invokeReq.Host = siteName + ".azurewebsites.net"
	invokeResp, err := http.DefaultClient.Do(invokeReq)
	require.NoError(t, err)
	defer invokeResp.Body.Close()
	require.Equal(t, http.StatusOK, invokeResp.StatusCode)
	body, _ := io.ReadAll(invokeResp.Body)
	return body
}

func azureInvokeFunctionExpectError(t *testing.T, siteName string) {
	t.Helper()
	invokeReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/api/function",
		strings.NewReader("{}"))
	invokeReq.Header.Set("Content-Type", "application/json")
	invokeReq.Host = siteName + ".azurewebsites.net"
	invokeResp, err := http.DefaultClient.Do(invokeReq)
	require.NoError(t, err)
	invokeResp.Body.Close()
	require.Equal(t, http.StatusInternalServerError, invokeResp.StatusCode)
}

func TestAzureFunctions_InvokeInjectsLogEntries(t *testing.T) {
	rg, name := "func-log-rg", "log-func-app"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	azureInvokeFunction(t, name)

	kql := `AppTraces | where AppRoleName == "log-func-app"`
	result := queryWorkspace(t, "default", kql)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]
	require.GreaterOrEqual(t, len(table.Rows), 1, "should have at least one log entry from invocation")

	msgIdx := -1
	roleIdx := -1
	for i, col := range table.Columns {
		if col.Name == "Message" {
			msgIdx = i
		}
		if col.Name == "AppRoleName" {
			roleIdx = i
		}
	}
	require.GreaterOrEqual(t, msgIdx, 0, "Message column not found")
	require.GreaterOrEqual(t, roleIdx, 0, "AppRoleName column not found")

	lastRow := table.Rows[len(table.Rows)-1]
	assert.Equal(t, "Function invoked", lastRow[msgIdx])
	assert.Equal(t, "log-func-app", lastRow[roleIdx])
}

func TestAzureFunctions_InvokeExecutesCommand(t *testing.T) {
	rg, name := "func-exec-rg", "exec-func-app"
	azureCreateSiteWithImage(t, rg, name, []string{"echo", "hello-from-azure"}, "public.ecr.aws/docker/library/alpine:latest")
	defer azureDeleteSite(rg, name)

	respBody := azureInvokeFunction(t, name)
	assert.Contains(t, string(respBody), "hello-from-azure")
}

func TestAzureFunctions_InvokeNonZeroExit(t *testing.T) {
	rg, name := "func-fail-rg", "fail-func-app"
	azureCreateSiteWithImage(t, rg, name, []string{"sh", "-c", "exit 1"}, "public.ecr.aws/docker/library/alpine:latest")
	defer azureDeleteSite(rg, name)

	azureInvokeFunctionExpectError(t, name)

	kql := `AppTraces | where AppRoleName == "fail-func-app"`
	result := queryWorkspace(t, "default", kql)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]
	require.GreaterOrEqual(t, len(table.Rows), 1, "should have log entries from execution")

	msgIdx := -1
	for i, col := range table.Columns {
		if col.Name == "Message" {
			msgIdx = i
		}
	}
	require.GreaterOrEqual(t, msgIdx, 0, "Message column not found")

	found := false
	for _, row := range table.Rows {
		msg, ok := row[msgIdx].(string)
		if ok && strings.Contains(msg, "error") && strings.Contains(msg, "exit") {
			found = true
		}
	}
	assert.True(t, found, "expected error log entry about non-zero exit")
}

func TestAzureFunctions_InvokeLogsRealOutput(t *testing.T) {
	rg, name := "func-out-rg", "out-func-app"
	azureCreateSiteWithImage(t, rg, name, []string{"echo", "real-azure-output"}, "public.ecr.aws/docker/library/alpine:latest")
	defer azureDeleteSite(rg, name)

	azureInvokeFunction(t, name)

	kql := `AppTraces | where AppRoleName == "out-func-app"`
	result := queryWorkspace(t, "default", kql)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]
	require.GreaterOrEqual(t, len(table.Rows), 1, "should have log entries from execution")

	msgIdx := -1
	for i, col := range table.Columns {
		if col.Name == "Message" {
			msgIdx = i
		}
	}
	require.GreaterOrEqual(t, msgIdx, 0, "Message column not found")

	found := false
	for _, row := range table.Rows {
		msg, ok := row[msgIdx].(string)
		if ok && msg == "real-azure-output" {
			found = true
		}
	}
	assert.True(t, found, "process stdout should appear in AppTraces")
}

func TestAzureFunctions_DefaultHostNameReachability(t *testing.T) {
	rg, name := "func-host-rg", "host-func-app"
	azureCreateSite(t, rg, name, nil)
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

	assert.Equal(t, http.StatusOK, invokeResp.StatusCode)
}

// --- SDK-level tests using armappservice ---

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

// --- Error path tests ---

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
