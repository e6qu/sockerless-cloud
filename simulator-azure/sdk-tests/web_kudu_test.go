package azure_sdk_test

import (
	"bytes"
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

// SDK coverage for the App Service SCM (Kudu) deployment API a web app's
// Repository hostname serves, reached the way a deployment client reaches it:
// the SCM host from the site's hostNameSslStates, authenticated with the
// publishing credentials WebApps_ListPublishingCredentials returns, or with a
// Microsoft Entra token once the scm basic publishing credentials policy
// forbids basic auth:
//
//	GET /deployments
//	POST /api/zipdeploy
//	POST /api/publish
//	GET /api/deployments
//	GET /api/deployments/latest
//	GET /api/deployments/{id}
//	GET /api/deployments/{id}/log
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/deployments
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/deploymentStatus/{deploymentStatusId}
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/basicPublishingCredentialsPolicies/scm

// kuduFileServer is a Node server that answers each request with the deployed
// wwwroot file its path names.
const kuduFileServer = `const fs = require("fs"), path = require("path");
require("http").createServer((req, res) => {
  fs.readFile(path.join(__dirname, req.url), (err, data) => {
    if (err) { res.statusCode = 404; res.end(String(err)); return; }
    res.end(data);
  });
}).listen(process.env.PORT);
`

type kuduDeploymentRecord struct {
	ID       string `json:"id"`
	Status   int    `json:"status"`
	Deployer string `json:"deployer"`
	Complete bool   `json:"complete"`
	Active   bool   `json:"active"`
	URL      string `json:"url"`
	LogURL   string `json:"log_url"`
}

func kuduRequest(t *testing.T, method, rawURL string, body []byte, contentType string, auth func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Cache-Control", "no-cache")
	if auth != nil {
		auth(req)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, data
}

func kuduLatest(t *testing.T, scm string, auth func(*http.Request)) kuduDeploymentRecord {
	t.Helper()
	resp, body := kuduRequest(t, http.MethodGet, scm+"/api/deployments/latest", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "latest deployment: %s", body)
	var rec kuduDeploymentRecord
	require.NoError(t, json.Unmarshal(body, &rec), "%s", body)
	return rec
}

// awaitDeploymentRuntime waits on the deployment's runtime status with the
// SDK's own long-running-operation poller.
func awaitDeploymentRuntime(t *testing.T, client *armappservice.WebAppsClient, rg, name, id string) armappservice.CsmDeploymentStatus {
	t.Helper()
	poller, err := client.BeginGetProductionSiteDeploymentStatus(ctx, rg, name, id, nil)
	require.NoError(t, err)
	final, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return final.CsmDeploymentStatus
}

func TestSDK_WebApps_KuduDeploymentAPI(t *testing.T) {
	rg, name := "sdk-kudu-rg", "sdk-kudu-app"
	client := createStackWebApp(t, rg, name, "NODE|20-lts", nil)

	site, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	var scmHost string
	for _, s := range site.Properties.HostNameSSLStates {
		if s.HostType != nil && *s.HostType == armappservice.HostTypeRepository {
			scmHost = *s.Name
		}
	}
	require.True(t, strings.HasPrefix(scmHost, name+".scm.shim.localhost:"),
		"the SCM site is advertised at the configured coordinate, got %q", scmHost)
	var enabled []string
	for _, h := range site.Properties.EnabledHostNames {
		enabled = append(enabled, *h)
	}
	assert.Contains(t, enabled, scmHost)
	scm := "http://" + scmHost

	credsPoller, err := client.BeginListPublishingCredentials(ctx, rg, name, nil)
	require.NoError(t, err)
	creds, err := credsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	user, password := *creds.Properties.PublishingUserName, *creds.Properties.PublishingPassword
	assert.Equal(t, "$"+name, user)
	assert.Contains(t, *creds.Properties.ScmURI, "@"+scmHost)
	basic := func(u, p string) func(*http.Request) {
		return func(r *http.Request) { r.SetBasicAuth(u, p) }
	}

	resp, _ := kuduRequest(t, http.MethodGet, scm+"/deployments?warmup=true", nil, "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Kudu refuses an anonymous request")
	assert.Equal(t, `Basic realm="site"`, resp.Header.Get("WWW-Authenticate"))
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/deployments?warmup=true", nil, "", basic(user, password+"x"))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Kudu refuses a wrong password")
	resp, body := kuduRequest(t, http.MethodGet, scm+"/deployments?warmup=true", nil, "", basic(user, password))
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.JSONEq(t, `[]`, string(body), "a site nothing was deployed to has no deployments")

	// Asynchronous zip deploy, the request terraform-provider-azurerm's
	// zip_deploy_file and az webapp deployment source config-zip send.
	pkg := makeJobsZip(t, map[string]string{
		"server.js":    kuduFileServer,
		"greeting.txt": "zip deploy v1",
		"obsolete.txt": "removed by the next zip deploy",
	})
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/zipdeploy?isAsync=true", pkg, "application/octet-stream", basic(user, password))
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "%s", body)
	location := resp.Header.Get("Location")
	require.True(t, strings.HasPrefix(location, scm+"/api/deployments/latest"), "Location %q", location)

	resp, body = kuduRequest(t, http.MethodGet, location, nil, "", basic(user, password))
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var first kuduDeploymentRecord
	require.NoError(t, json.Unmarshal(body, &first))
	require.NotEmpty(t, first.ID)
	assert.Equal(t, "ZipDeploy", first.Deployer)

	runtime := awaitDeploymentRuntime(t, client, rg, name, first.ID)
	assert.Equal(t, armappservice.DeploymentBuildStatusRuntimeSuccessful, *runtime.Properties.Status)

	first = kuduLatest(t, scm, basic(user, password))
	assert.Equal(t, 4, first.Status, "Kudu reports the deployment succeeded")
	assert.True(t, first.Complete)
	assert.True(t, first.Active)
	assert.Equal(t, scm+"/api/deployments/"+first.ID, first.URL)
	resp, body = kuduRequest(t, http.MethodGet, first.LogURL, nil, "", basic(user, password))
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Contains(t, string(body), "Deployment successful.")

	status, served := azureSiteRequest(t, name, http.MethodGet, "/greeting.txt", "")
	require.Equal(t, http.StatusOK, status, "%s", served)
	assert.Equal(t, "zip deploy v1", string(served))

	// Azure Resource Manager's deployment list proxies Kudu's.
	var armDeployments []*armappservice.Deployment
	pager := client.NewListDeploymentsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		armDeployments = append(armDeployments, page.Value...)
	}
	require.Len(t, armDeployments, 1)
	assert.Equal(t, first.ID, *armDeployments[0].Name)
	assert.Equal(t, "ZipDeploy", *armDeployments[0].Properties.Deployer)
	assert.Equal(t, int32(4), *armDeployments[0].Properties.Status)

	// A second zip deploy replaces what the first one wrote: KuduSync deletes
	// the files the previous package had and this one lacks.
	pkg = makeJobsZip(t, map[string]string{
		"server.js":    kuduFileServer,
		"greeting.txt": "zip deploy v2",
	})
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/zipdeploy", pkg, "application/zip", basic(user, password))
	require.Equal(t, http.StatusOK, resp.StatusCode, "a synchronous zip deploy answers once deployed: %s", body)
	second := kuduLatest(t, scm, basic(user, password))
	require.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, 4, second.Status)
	awaitDeploymentRuntime(t, client, rg, name, second.ID)
	status, served = azureSiteRequest(t, name, http.MethodGet, "/greeting.txt", "")
	require.Equal(t, http.StatusOK, status, "%s", served)
	assert.Equal(t, "zip deploy v2", string(served))
	status, _ = azureSiteRequest(t, name, http.MethodGet, "/obsolete.txt", "")
	assert.Equal(t, http.StatusNotFound, status, "the file the new package lacks is gone")

	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/deployments", nil, "", basic(user, password))
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var all []kuduDeploymentRecord
	require.NoError(t, json.Unmarshal(body, &all))
	require.Len(t, all, 2)
	assert.Equal(t, second.ID, all[0].ID, "the newest deployment lists first")
	assert.True(t, all[0].Active)
	assert.False(t, all[1].Active, "only the latest successful deployment is active")

	// With basic auth forbidden on the SCM site, the publishing credentials
	// stop working and a Microsoft Entra token for App Service is the way in.
	_, err = client.UpdateScmAllowed(ctx, rg, name, armappservice.CsmPublishingCredentialsPoliciesEntity{
		Properties: &armappservice.CsmPublishingCredentialsPoliciesEntityProperties{Allow: to.Ptr(false)},
	}, nil)
	require.NoError(t, err)
	policy, err := client.GetScmAllowed(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.False(t, *policy.Properties.Allow)
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/api/deployments", nil, "", basic(user, password))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "basic auth is refused once the policy forbids it")

	token, _, err := fetchSimAccessToken("https://appservice.azure.com/.default")
	require.NoError(t, err)
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }

	// OneDeploy of a single static file, the request az webapp deploy --type
	// static sends.
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/publish?type=static&path=greeting.txt", []byte("static v3"),
		"application/octet-stream", bearer)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	third := kuduLatest(t, scm, bearer)
	assert.Equal(t, "OneDeploy", third.Deployer)
	assert.Equal(t, 4, third.Status)
	awaitDeploymentRuntime(t, client, rg, name, third.ID)
	status, served = azureSiteRequest(t, name, http.MethodGet, "/greeting.txt", "")
	require.Equal(t, http.StatusOK, status, "%s", served)
	assert.Equal(t, "static v3", string(served))
	status, served = azureSiteRequest(t, name, http.MethodGet, "/server.js", "")
	require.Equal(t, http.StatusOK, status, "a static deployment keeps the rest of wwwroot: %s", served)

	// OneDeploy of a zip with clean=true (the default) replaces wwwroot.
	pkg = makeJobsZip(t, map[string]string{"server.js": kuduFileServer, "other.txt": "onedeploy zip"})
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/publish?type=zip&async=true", pkg, "application/octet-stream", bearer)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "%s", body)
	fourth := kuduLatest(t, scm, bearer)
	runtime = awaitDeploymentRuntime(t, client, rg, name, fourth.ID)
	assert.Equal(t, armappservice.DeploymentBuildStatusRuntimeSuccessful, *runtime.Properties.Status)
	status, served = azureSiteRequest(t, name, http.MethodGet, "/other.txt", "")
	require.Equal(t, http.StatusOK, status, "%s", served)
	assert.Equal(t, "onedeploy zip", string(served))
	status, _ = azureSiteRequest(t, name, http.MethodGet, "/greeting.txt", "")
	assert.Equal(t, http.StatusNotFound, status, "a clean zip OneDeploy empties wwwroot first")

	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/publish?type=static", []byte("x"), "application/octet-stream", bearer)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "Path must be defined for static file deployments")

	// A deployment slot has an SCM site of its own, and its publishing user
	// is $<app>__<slot>.
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, "staging", armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	slotSite, err := slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	var slotScm string
	for _, s := range slotSite.Properties.HostNameSSLStates {
		if s.HostType != nil && *s.HostType == armappservice.HostTypeRepository {
			slotScm = "http://" + *s.Name
		}
	}
	require.True(t, strings.HasPrefix(slotScm, "http://"+name+"-staging.scm.shim.localhost:"), "slot SCM site %q", slotScm)
	slotCredsPoller, err := client.BeginListPublishingCredentialsSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	slotCreds, err := slotCredsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotAuth := basic(*slotCreds.Properties.PublishingUserName, *slotCreds.Properties.PublishingPassword)
	resp, _ = kuduRequest(t, http.MethodGet, slotScm+"/api/deployments", nil, "", basic(user, password))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the app's credentials do not open its slot's SCM site")
	resp, body = kuduRequest(t, http.MethodPost, slotScm+"/api/zipdeploy",
		makeJobsZip(t, map[string]string{"greeting.txt": "staging"}), "application/zip", slotAuth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	slotDeployment := kuduLatest(t, slotScm, slotAuth)
	assert.Equal(t, 4, slotDeployment.Status)
	var slotDeployments []*armappservice.Deployment
	slotPager := client.NewListDeploymentsSlotPager(rg, name, "staging", nil)
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		slotDeployments = append(slotDeployments, page.Value...)
	}
	require.Len(t, slotDeployments, 1)
	assert.Equal(t, slotDeployment.ID, *slotDeployments[0].Name)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/deployments", nil, "", bearer)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.NotContains(t, string(body), slotDeployment.ID, "the app's deployments are its own")
}
