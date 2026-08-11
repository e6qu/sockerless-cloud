package azure_sdk_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createSiteForContainers provisions a Function App site and returns its
// default hostname (used to route invocations).
func createSiteForContainers(t *testing.T, rg, name string) string {
	t.Helper()
	ensureRG(t, rg)
	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	p, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/plan"),
		},
	}, nil)
	require.NoError(t, err)
	site, err := p.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return *site.Properties.DefaultHostName
}

func TestSDK_SiteContainers_CRUD(t *testing.T) {
	rg := "sdk-sitecontainers-rg"
	site := "sc-crud-app"
	createSiteForContainers(t, rg, site)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Create the main + one sidecar sitecontainer.
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:  to.Ptr("myapp:latest"),
			IsMain: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)

	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "redis", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:      to.Ptr("redis:7-alpine"),
			IsMain:     to.Ptr(false),
			TargetPort: to.Ptr("6379"),
			EnvironmentVariables: []*armappservice.EnvironmentVariable{
				{Name: to.Ptr("REDIS_ARGS"), Value: to.Ptr("--save 60 1")},
			},
		},
	}, nil)
	require.NoError(t, err)

	// GET the main container back.
	got, err := client.GetSiteContainer(ctx, rg, site, "main", nil)
	require.NoError(t, err)
	assert.Equal(t, "myapp:latest", *got.Properties.Image)
	assert.True(t, *got.Properties.IsMain)

	// LIST returns both.
	pager := client.NewListSiteContainersPager(rg, site, nil)
	names := map[string]bool{}
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			names[*c.Name] = true
		}
	}
	assert.True(t, names["main"], "main sitecontainer should be listed")
	assert.True(t, names["redis"], "redis sitecontainer should be listed")

	// DELETE the sidecar; LIST should drop it.
	_, err = client.DeleteSiteContainer(ctx, rg, site, "redis", nil)
	require.NoError(t, err)
	_, err = client.GetSiteContainer(ctx, rg, site, "redis", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

// TestSDK_AzureFunctions_MultiContainerSharesLocalhost proves the App
// Service multi-container loopback contract: a sidecar sitecontainer that
// binds a port is reachable from the main on localhost:<port>. The main
// runs the localhost-probe HTTP server; on invoke it connects to the
// sidecar (an HTTP server on :9090) over the shared loopback and echoes a
// success token.
func TestSDK_AzureFunctions_MultiContainerSharesLocalhost(t *testing.T) {
	rg := "sdk-sc-localhost-rg"
	site := "sc-localhost-app"
	host := createSiteForContainers(t, rg, site)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Main: probe-retry HTTP server that connects to the sidecar on
	// localhost:9090 when invoked.
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(true),
			StartUpCommand: to.Ptr("probe-retry azf-sidecar-ok"),
		},
	}, nil)
	require.NoError(t, err)

	// Sidecar: HTTP server bound to :9090 inside the shared netns.
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "sidecar", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(false),
			TargetPort:     to.Ptr("9090"),
			StartUpCommand: to.Ptr("server"),
		},
	}, nil)
	require.NoError(t, err)

	// Invoke the function app: the sim starts the main + sidecar sharing one
	// network namespace and posts to the main's HTTP port.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/function", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode, "invoke should succeed: %s", string(body))
	assert.Equal(t, "0", resp.Header.Get("X-Sockerless-Exit-Code"), "probe should reach the sidecar over localhost: %s", string(body))
	assert.Contains(t, string(body), "azf-sidecar-ok",
		"main must reach the sidecar on localhost:9090 over the shared netns")
}
