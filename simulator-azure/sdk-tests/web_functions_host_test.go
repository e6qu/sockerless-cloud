package azure_sdk_test

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const functionsHostNodeImage = "mcr.microsoft.com/azure-functions/node:4.1054.250-4-node22-appservice"

// identifiableFunctionsKey is the shape of a key the Functions host generates:
// 33 random bytes, the AzFu signature and a checksum, URL-safe base64.
var identifiableFunctionsKey = regexp.MustCompile(`^[A-Za-z0-9_-]{44}AzFu[A-Za-z0-9_-]{6}==$`)

// createStackFunctionApp creates a Linux plan and a Linux function app on the
// built-in stack linuxFxVersion names, with the given app settings.
func createStackFunctionApp(t *testing.T, rg, name, linuxFxVersion string, appSettings map[string]string) *armappservice.WebAppsClient {
	t.Helper()
	ensureRG(t, rg)
	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	planPoller, err := plans.BeginCreateOrUpdate(ctx, rg, name+"-plan", armappservice.Plan{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("linux"),
		SKU:        &armappservice.SKUDescription{Name: to.Ptr("B1")},
		Properties: &armappservice.PlanProperties{Reserved: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	plan, err := planPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	var settings []*armappservice.NameValuePair
	for k, v := range appSettings {
		settings = append(settings, &armappservice.NameValuePair{Name: to.Ptr(k), Value: to.Ptr(v)})
	}
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: plan.ID,
			Reserved:     to.Ptr(true),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr(linuxFxVersion),
				AppSettings:    settings,
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { azureDeleteSite(rg, name) })
	return client
}

// functionsHostRequest sends a request to a function app through the App
// Service front end, with headers set on it.
func functionsHostRequest(t *testing.T, siteName, path string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	require.NoError(t, err)
	req.Host = siteName + ".azurewebsites.net"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// A Linux function app on Node|22 runs the Azure Functions host image on its
// deployed content. The host serves the deployed function and answers 404 for
// a route no function declares, and it enforces the function's authLevel
// against the keys the ARM key operations serve, since those operations read
// and write the host's own file secret store.
func TestSDK_FunctionApps_NodeHostRunsTheDeployedFunctions(t *testing.T) {
	rg, name := "sdk-func-host-rg", "sdk-func-host-app"
	client := createStackFunctionApp(t, rg, name, "Node|22", map[string]string{
		"FUNCTIONS_WORKER_RUNTIME":      "node",
		"FUNCTIONS_EXTENSION_VERSION":   "~4",
		"AzureWebJobsSecretStorageType": "files",
	})

	pkg := makeJobsZip(t, map[string]string{
		"host.json": `{"version":"2.0"}`,
		"hello/function.json": `{"bindings":[` +
			`{"type":"httpTrigger","direction":"in","name":"req","authLevel":"function","methods":["get"]},` +
			`{"type":"http","direction":"out","name":"res"}]}`,
		"hello/index.js": `module.exports = async function (context, req) {` +
			` context.res = { body: "hello " + (req.query.name || "world") + " from " + process.env.WEBSITE_SITE_NAME + " on node " + process.version }; };`,
	})
	deploy, err := client.BeginCreateMSDeployOperation(ctx, rg, name, armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(servePackage(t, pkg))},
	}, nil)
	require.NoError(t, err)
	_, err = deploy.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	status, body := functionsHostRequest(t, name, "/api/nothing-declares-this", nil)
	assert.Equal(t, http.StatusNotFound, status, "body: %s", body)
	status, body = functionsHostRequest(t, name, "/api/hello", nil)
	assert.Equal(t, http.StatusUnauthorized, status, "body: %s", body)

	// The function the content declares is a function of the app, with the
	// default key the host generates for it.
	var listed []string
	pager := client.NewListFunctionsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, fn := range page.Value {
			listed = append(listed, *fn.Name)
			require.NotNil(t, fn.Properties.InvokeURLTemplate)
			assert.Equal(t, "https://"+name+".azurewebsites.net/api/hello", *fn.Properties.InvokeURLTemplate)
		}
	}
	assert.Equal(t, []string{name + "/hello"}, listed)

	fnKeys, err := client.ListFunctionKeys(ctx, rg, name, "hello", nil)
	require.NoError(t, err)
	require.Contains(t, fnKeys.Properties, "default")
	defaultKey := *fnKeys.Properties["default"]
	assert.Regexp(t, identifiableFunctionsKey, defaultKey)
	status, body = functionsHostRequest(t, name, "/api/hello?code="+defaultKey+"&name=sdk", nil)
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Regexp(t, `^hello sdk from `+name+` on node v22\.\d+\.\d+$`, body)

	hostKeys, err := client.ListHostKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	status, body = functionsHostRequest(t, name, "/api/hello", map[string]string{"x-functions-key": *hostKeys.FunctionKeys["default"]})
	assert.Equal(t, http.StatusOK, status, "a host function key opens every function: %s", body)
	status, body = functionsHostRequest(t, name, "/api/hello?code="+*hostKeys.MasterKey, nil)
	assert.Equal(t, http.StatusOK, status, "the master key opens every function: %s", body)

	// A key set through ARM reaches the running host, and stops working once
	// deleted through ARM.
	_, err = client.CreateOrUpdateFunctionSecret(ctx, rg, name, "hello", "ci", armappservice.KeyInfo{Value: to.Ptr("sdk-ci-function-key")}, nil)
	require.NoError(t, err)
	status, body = functionsHostRequest(t, name, "/api/hello?code=sdk-ci-function-key", nil)
	assert.Equal(t, http.StatusOK, status, "body: %s", body)
	_, err = client.DeleteFunctionSecret(ctx, rg, name, "hello", "ci", nil)
	require.NoError(t, err)
	status, body = functionsHostRequest(t, name, "/api/hello?code=sdk-ci-function-key", nil)
	assert.Equal(t, http.StatusUnauthorized, status, "body: %s", body)

	created, err := client.CreateOrUpdateHostSecret(ctx, rg, name, "functionKeys", "deploy", armappservice.KeyInfo{}, nil)
	require.NoError(t, err)
	assert.Regexp(t, identifiableFunctionsKey, *created.Value)
	status, body = functionsHostRequest(t, name, "/api/hello", map[string]string{"x-functions-key": *created.Value})
	assert.Equal(t, http.StatusOK, status, "body: %s", body)

	// The host accepts the admin token as an admin credential.
	token, err := client.GetFunctionsAdminToken(ctx, rg, name, nil)
	require.NoError(t, err)
	status, body = functionsHostRequest(t, name, "/admin/host/status", map[string]string{"Authorization": "Bearer " + *token.Value})
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Contains(t, body, `"state":"Running"`)
	forged := *token.Value
	forged = forged[:strings.LastIndex(forged, ".")+1] + "c2lnbmF0dXJl"
	status, _ = functionsHostRequest(t, name, "/admin/host/status", map[string]string{"Authorization": "Bearer " + forged})
	assert.Equal(t, http.StatusUnauthorized, status, "a token with a forged signature is refused")
}
