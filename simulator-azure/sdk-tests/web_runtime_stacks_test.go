package azure_sdk_test

import (
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createStackWebApp creates a Linux plan and a Linux web app on the built-in
// runtime stack linuxFxVersion names, with the given app settings.
func createStackWebApp(t *testing.T, rg, name, linuxFxVersion string, appSettings map[string]string) *armappservice.WebAppsClient {
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
		Properties: &armappservice.SiteProperties{
			ServerFarmID: plan.ID,
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr(linuxFxVersion),
				AppSettings:    settings,
			},
		},
	}, nil)
	require.NoError(t, err)
	site, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, site.Kind)
	assert.Equal(t, "app,linux", *site.Kind, "a site on a reserved plan is a Linux web app")
	t.Cleanup(func() { azureDeleteSite(rg, name) })
	return client
}

// A Linux web app on NODE|20-lts runs the platform's Node image. With nothing
// deployed it serves the platform's default page; once an MSDeploy package
// lands, the deployment restarts the site and the image runs the deployed
// server.js.
func TestSDK_WebApps_NodeStackRunsTheDeployedContent(t *testing.T) {
	rg, name := "sdk-node-stack-rg", "sdk-node-stack-app"
	client := createStackWebApp(t, rg, name, "NODE|20-lts", nil)

	status, body := azureSiteRequest(t, name, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Contains(t, string(body), "Microsoft Azure App Service - Welcome")
	assert.Contains(t, string(body), "waiting for your content")

	pkg := makeJobsZip(t, map[string]string{
		"server.js": `require("http").createServer((req, res) => res.end("node " + process.version + " " + req.url)).listen(process.env.PORT)`,
	})
	deploy, err := client.BeginCreateMSDeployOperation(ctx, rg, name, armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(servePackage(t, pkg))},
	}, nil)
	require.NoError(t, err)
	_, err = deploy.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	status, body = azureSiteRequest(t, name, http.MethodGet, "/from-the-sdk", "")
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Regexp(t, `^node v20\.\d+\.\d+ /from-the-sdk$`, string(body))
}

// A Linux web app on PYTHON|3.12 whose WEBSITE_RUN_FROM_PACKAGE names a
// package URL runs that package: the image detects app.py and serves its WSGI
// app with gunicorn.
func TestSDK_WebApps_PythonStackRunsFromPackage(t *testing.T) {
	pkg := makeJobsZip(t, map[string]string{
		"app.py": "import sys\n" +
			"def app(environ, start_response):\n" +
			"    start_response('200 OK', [('Content-Type', 'text/plain')])\n" +
			"    return [('python %d.%d %s' % (sys.version_info[0], sys.version_info[1], environ['PATH_INFO'])).encode()]\n",
	})
	rg, name := "sdk-python-stack-rg", "sdk-python-stack-app"
	createStackWebApp(t, rg, name, "PYTHON|3.12", map[string]string{
		"WEBSITE_RUN_FROM_PACKAGE": servePackage(t, pkg),
	})

	status, body := azureSiteRequest(t, name, http.MethodGet, "/run-from-package", "")
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Equal(t, "python 3.12 /run-from-package", string(body))
}

// A site the simulator has nothing to run for — a function app with no
// container image, here — answers 503 naming what it lacks, never a made-up
// success.
func TestSDK_WebApps_SiteWithNothingToRunAnswersServiceUnavailable(t *testing.T) {
	rg, name := "func-nothing-rg", "nothing-func-app"
	azureCreateSite(t, rg, name)
	defer azureDeleteSite(rg, name)

	status, body := azureInvokeFunctionResponse(t, name)
	assert.Equal(t, http.StatusServiceUnavailable, status, "body: %s", body)
	assert.Contains(t, string(body), "does not run the Azure Functions host")
	assert.NotContains(t, siteContainerLog(t, rg, name), "Function invoked")
}
