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

// A site's processes are read from the container it really runs in, so killing
// one removes it from the table the reads beside it return, and the modules
// reported are the ones the process actually loaded.
func TestSDK_WebApps_ProcessModulesAndKill(t *testing.T) {
	rg, name := "sdk-proc-verbs-rg", "sdk-proc-verbs-app"
	host := createSiteForContainers(t, rg, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, name, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(true),
			StartUpCommand: to.Ptr("probe-retry proc-verbs-ok"),
		},
	}, nil)
	require.NoError(t, err)
	// Invoking the site starts its container, exactly as a request to the site
	// does — the processes below are that container's.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/function", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoking the site must start its container: %s", body)

	procs := listWebProcesses(t, client, rg, name)
	require.NotEmpty(t, procs, "a running site has processes")
	pid := *procs[0].Properties.Identifier

	// The modules a process loaded are reported against the process itself.
	modules := client.NewListProcessModulesPager(rg, name, itoa(int(pid)), nil)
	modulePage, err := modules.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, modulePage.Value)
	base := *modulePage.Value[0].Properties.BaseAddress

	one, err := client.GetProcessModule(ctx, rg, name, itoa(int(pid)), base, nil)
	require.NoError(t, err)
	assert.Equal(t, base, *one.Properties.BaseAddress)

	// A module nothing is mapped at is not found.
	_, err = client.GetProcessModule(ctx, rg, name, itoa(int(pid)), "0xdeadbeef", nil)
	require.Error(t, err)

	// A process the site does not have cannot be killed.
	_, err = client.DeleteProcess(ctx, rg, name, "999999", nil)
	require.Error(t, err)
}
