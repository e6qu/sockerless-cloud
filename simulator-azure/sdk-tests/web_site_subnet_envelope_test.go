package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/require"
)

// Regional VNet integration written through the site envelope's
// virtualNetworkSubnetId — the spelling terraform's
// azurerm_linux_function_app.virtual_network_subnet_id and
// `az webapp vnet-integration add` use — must read back on the site, or a
// client that wrote it plans a change on every refresh (the integration looks
// absent) and the graph is never idempotent.
func TestSDK_WebSite_SubnetInEnvelopeReadsBack(t *testing.T) {
	rg := "site-subnet-envelope-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "sse-plan", "S1", "Standard")
	subnetID := stage5Subnet(t, rg, "sse", "10.62.0.0/16", "appsvc", "10.62.1.0/24")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sse-app", armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID:           to.Ptr(planID),
			VirtualNetworkSubnetID: to.Ptr(subnetID),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.Equal(t, subnetID, derefStr(created.Properties.VirtualNetworkSubnetID),
		"the create response must echo the integration it just made")

	got, err := client.Get(ctx, rg, "sse-app", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.Equal(t, subnetID, derefStr(got.Properties.VirtualNetworkSubnetID),
		"a refresh must still report the integration, or every plan shows drift")

	// A second identical write — what a re-apply performs — keeps it.
	poller, err = client.BeginCreateOrUpdate(ctx, rg, "sse-app", armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID:           to.Ptr(planID),
			VirtualNetworkSubnetID: to.Ptr(subnetID),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	again, err := client.Get(ctx, rg, "sse-app", nil)
	require.NoError(t, err)
	require.Equal(t, subnetID, derefStr(again.Properties.VirtualNetworkSubnetID))
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
