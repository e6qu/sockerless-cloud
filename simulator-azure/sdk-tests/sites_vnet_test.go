package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDK_SiteVNetIntegration_RoundTrip: a site joins a Microsoft.Web-delegated
// subnet through regional virtual network integration (the "swift"
// connection), which round-trips on GET and deletes.
func TestSDK_SiteVNetIntegration_RoundTrip(t *testing.T) {
	t.Parallel()
	rg := "sdk-sitevnet-rg"
	site := "sitevnet-app"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// A site that is not Always On and gets no request runs no container, so the
	// integration only records the connection.
	p, err := client.BeginCreateOrUpdate(ctx, rg, site, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + unstartedSiteImage),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = p.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetID := stage5Subnet(t, rg, "sdk-sitevnet", "10.71.0.0/16", "appservice", "10.71.1.0/24")

	put, err := client.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, site, armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{
			SubnetResourceID: to.Ptr(subnetID),
			SwiftSupported:   to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, put.Properties)
	assert.Equal(t, subnetID, *put.Properties.SubnetResourceID)
	// The operation path is .../networkConfig/virtualNetwork; the canonical ID,
	// which terraform-provider-azurerm parses, is .../config/virtualNetwork.
	require.NotNil(t, put.ID)
	assert.True(t, strings.HasSuffix(*put.ID, "/config/virtualNetwork"),
		"swift connection id must be the canonical config sub-resource; got %s", *put.ID)

	got, err := client.GetSwiftVirtualNetworkConnection(ctx, rg, site, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, subnetID, *got.Properties.SubnetResourceID)
	require.NotNil(t, got.ID)
	assert.True(t, strings.HasSuffix(*got.ID, "/config/virtualNetwork"),
		"swift connection id must be the canonical config sub-resource; got %s", *got.ID)

	_, err = client.DeleteSwiftVirtualNetwork(ctx, rg, site, nil)
	require.NoError(t, err)
}
