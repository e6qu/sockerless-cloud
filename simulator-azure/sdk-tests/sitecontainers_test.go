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

// createSiteForContainers creates a function app and returns its default
// hostname.
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
	t.Parallel()
	rg := "sdk-sitecontainers-rg"
	site := "sc-crud-app"
	createSiteForContainers(t, rg, site)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:  to.Ptr("myapp:latest"),
			IsMain: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)

	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "redis", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:      to.Ptr("mirror.gcr.io/library/redis:7-alpine"),
			IsMain:     to.Ptr(false),
			TargetPort: to.Ptr("6379"),
			EnvironmentVariables: []*armappservice.EnvironmentVariable{
				{Name: to.Ptr("REDIS_ARGS"), Value: to.Ptr("--save 60 1")},
			},
		},
	}, nil)
	require.NoError(t, err)

	got, err := client.GetSiteContainer(ctx, rg, site, "main", nil)
	require.NoError(t, err)
	assert.Equal(t, "myapp:latest", *got.Properties.Image)
	assert.True(t, *got.Properties.IsMain)

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

	_, err = client.DeleteSiteContainer(ctx, rg, site, "redis", nil)
	require.NoError(t, err)
	_, err = client.GetSiteContainer(ctx, rg, site, "redis", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

// TestSDK_AzureFunctions_MultiContainerSharesLocalhost: a sidecar's port is
// reachable from the main on localhost. On invoke, the main's probe server
// connects to the sidecar's :9090 and echoes a success token.
func TestSDK_AzureFunctions_MultiContainerSharesLocalhost(t *testing.T) {
	t.Parallel()
	rg := "sdk-sc-localhost-rg"
	site := "sc-localhost-app"
	host := createSiteForContainers(t, rg, site)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(true),
			TargetPort:     to.Ptr("8080"),
			StartUpCommand: to.Ptr("probe-retry azf-sidecar-ok"),
		},
	}, nil)
	require.NoError(t, err)

	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "sidecar", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(false),
			TargetPort:     to.Ptr("9090"),
			StartUpCommand: to.Ptr("server"),
		},
	}, nil)
	require.NoError(t, err)

	// The invoke starts the main and sidecar in one network namespace.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/function", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode, "main should reach the sidecar over localhost: %s", string(body))
	assert.Contains(t, string(body), "azf-sidecar-ok",
		"main must reach the sidecar on localhost:9090 over the shared netns")
}

// A sidecar that cannot start fails the site start: the invoke is refused,
// naming the sidecar, rather than running the main without it.
func TestSDK_AzureFunctions_SidecarThatCannotStartFailsTheSite(t *testing.T) {
	t.Parallel()
	rg := "sdk-sc-badsidecar-rg"
	site := "sc-badsidecar-app"
	host := createSiteForContainers(t, rg, site)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(true),
			TargetPort:     to.Ptr("8080"),
			StartUpCommand: to.Ptr("probe-retry azf-sidecar-ok"),
		},
	}, nil)
	require.NoError(t, err)
	// No registry listens on 127.0.0.1:1.
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "unpullable", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:  to.Ptr("127.0.0.1:1/sockerless/absent-sidecar:none"),
			IsMain: to.Ptr(false),
		},
	}, nil)
	require.NoError(t, err)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/function", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.GreaterOrEqual(t, resp.StatusCode, 500, "a site whose sidecar cannot start must not serve: %s", string(body))
	assert.Contains(t, string(body), "unpullable", "the refusal names the sidecar that failed: %s", string(body))
	assert.NotContains(t, string(body), "azf-sidecar-ok", "the main must not have run without its sidecar")
}
