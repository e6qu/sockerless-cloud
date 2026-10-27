package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A site's logs configuration reads back as written, and a site with
// file-system retention still serves the container output it keeps.
func TestWebApps_LogsConfigurationRoundTripsAndKeepsTheDockerLog(t *testing.T) {
	t.Parallel()
	rg, name := "web-logs-cfg-rg", "logs-cfg-app"
	azureCreateContainerSite(t, rg, name, commandImageName, "log kept-under-retention", nil)
	defer azureDeleteSite(rg, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.UpdateDiagnosticLogsConfig(ctx, rg, name, armappservice.SiteLogsConfig{
		Properties: &armappservice.SiteLogsConfigProperties{
			HTTPLogs: &armappservice.HTTPLogsConfig{FileSystem: &armappservice.FileSystemHTTPLogsConfig{
				Enabled: to.Ptr(true), RetentionInMb: to.Ptr[int32](35), RetentionInDays: to.Ptr[int32](3),
			}},
		},
	}, nil)
	require.NoError(t, err)

	got, err := client.GetDiagnosticLogsConfiguration(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.HTTPLogs)
	require.NotNil(t, got.Properties.HTTPLogs.FileSystem)
	assert.Equal(t, int32(35), *got.Properties.HTTPLogs.FileSystem.RetentionInMb)
	assert.Equal(t, int32(3), *got.Properties.HTTPLogs.FileSystem.RetentionInDays)

	// The site's container starts on its first request.
	azureInvokeFunctionResponse(t, name)
	assert.Contains(t, siteContainerLog(t, rg, name), "kept-under-retention")
}
