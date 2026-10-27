package azure_sdk_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The App Service networking surfaces: the classic virtualNetworkConnections
// spelling of virtual network integration, coherent with the swift
// networkConfig/virtualNetwork spelling; connection gateways; private access;
// networkFeatures; site private endpoint connections and private link
// resources; both hybrid connection families; and the plan networking and
// worker operations.
//
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/virtualNetworkConnections (+ slot)
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/virtualNetworkConnections/{vnetName} (+ slot)
//	GET+PUT+PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/virtualNetworkConnections/{vnetName}/gateways/{gatewayName} (+ slot)
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/networkConfig/virtualNetwork
//	PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/networkConfig/virtualNetwork
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/privateAccess/virtualNetworks (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/networkFeatures/{view} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/privateEndpointConnections (+ slot)
//	GET+PUT+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/privateEndpointConnections/{privateEndpointConnectionName} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/privateLinkResources (+ slot)
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/hybridConnectionRelays (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/hybridconnection (+ slot)
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/hybridconnection/{entityName} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}/routes
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections/{vnetName}/routes/{routeName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionRelays
//	GET+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}/listKeys
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}/sites
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/hybridConnectionPlanLimits/limit
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/capabilities
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/skus
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/listinstances
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/workers/{workerName}/reboot
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/workers/{workerName}/recycleinstance
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/getrdppassword

func stage5EnsurePlan(t *testing.T, rg, plan, skuName, skuTier string) string {
	t.Helper()
	client, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	p, err := client.BeginCreateOrUpdate(ctx, rg, plan, armappservice.Plan{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("linux"),
		SKU:      &armappservice.SKUDescription{Name: to.Ptr(skuName), Tier: to.Ptr(skuTier)},
		Properties: &armappservice.PlanProperties{
			Reserved: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	created, err := p.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return *created.ID
}

// unstartedSiteImage belongs to a site that is not Always On and gets no
// request, so its container never starts and the image is never pulled.
const unstartedSiteImage = "registry.example/functions/azf:test"

// stage5CreateSite creates a function app on planID; a non-empty image becomes
// its Linux container, appCommandLine its startup command.
func stage5CreateSite(t *testing.T, rg, name, planID, image, appCommandLine string, alwaysOn bool) string {
	t.Helper()
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	siteConfig := &armappservice.SiteConfig{AlwaysOn: to.Ptr(alwaysOn)}
	if image != "" {
		siteConfig.LinuxFxVersion = to.Ptr("DOCKER|" + image)
	}
	if appCommandLine != "" {
		siteConfig.AppCommandLine = to.Ptr(appCommandLine)
	}
	p, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig:   siteConfig,
		},
	}, nil)
	require.NoError(t, err)
	created, err := p.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return *created.ID
}

// stage5Subnet creates a virtual network and a subnet delegated to
// Microsoft.Web/serverFarms through armnetwork, and returns the subnet's ID.
func stage5Subnet(t *testing.T, rg, vnetName, vnetCIDR, subnetName, subnetCIDR string) string {
	t.Helper()
	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vp, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr(vnetCIDR)}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vp.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	sp, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, subnetName, armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix: to.Ptr(subnetCIDR),
			Delegations: []*armnetwork.Delegation{{
				Name: to.Ptr("appservice"),
				Properties: &armnetwork.ServiceDelegationPropertiesFormat{
					ServiceName: to.Ptr("Microsoft.Web/serverFarms"),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	subnet, err := sp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, subnet.ID)
	return *subnet.ID
}

// TestSDK_WebVnet_SwiftClassicCoherence: the two spellings describe one state.
// A swift PUT shows in the classic list (`az webapp vnet-integration list`), a
// classic PUT shows on the swift GET, and a classic DELETE clears the swift
// view. It also covers connection gateways and the slot spellings.
func TestSDK_WebVnet_SwiftClassicCoherence(t *testing.T) {
	t.Parallel()
	rg := "stage5-vnet-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-vnet-plan", "S1", "Standard")
	stage5CreateSite(t, rg, "s5-vnet-app", planID, unstartedSiteImage, "", false)
	subnetID := stage5Subnet(t, rg, "s5-vnet", "10.60.0.0/16", "appsvc", "10.60.1.0/24")

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	swiftPut, err := web.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-vnet-app", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{
			SubnetResourceID: to.Ptr(subnetID),
			SwiftSupported:   to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, swiftPut.Properties)
	assert.Equal(t, subnetID, *swiftPut.Properties.SubnetResourceID)

	// The classic list shows the same integration: named <vnet>_<subnet>, isSwift,
	// vnetResourceId the subnet.
	list, err := web.ListVnetConnections(ctx, rg, "s5-vnet-app", nil)
	require.NoError(t, err)
	require.Len(t, list.VnetInfoResourceArray, 1)
	classic := list.VnetInfoResourceArray[0]
	assert.Equal(t, "s5-vnet_appsvc", *classic.Name)
	require.NotNil(t, classic.Properties)
	assert.Equal(t, subnetID, *classic.Properties.VnetResourceID)
	assert.True(t, *classic.Properties.IsSwift)

	got, err := web.GetVnetConnection(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", nil)
	require.NoError(t, err)
	assert.Equal(t, subnetID, *got.Properties.VnetResourceID)

	// UpdateVnetConnection keeps the target and adds DNS servers.
	patched, err := web.UpdateVnetConnection(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", armappservice.VnetInfoResource{
		Properties: &armappservice.VnetInfo{DNSServers: to.Ptr("10.60.0.53")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, subnetID, *patched.Properties.VnetResourceID)
	assert.Equal(t, "10.60.0.53", *patched.Properties.DNSServers)

	// A connection gateway is an ARM record on the connection.
	gwPut, err := web.CreateOrUpdateVnetConnectionGateway(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "primary", armappservice.VnetGateway{
		Properties: &armappservice.VnetGatewayProperties{VPNPackageURI: to.Ptr("https://vpn.example/pkg.zip")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "primary", *gwPut.Name)
	assert.Equal(t, "s5-vnet_appsvc", *gwPut.Properties.VnetName)

	gwGot, err := web.GetVnetConnectionGateway(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "primary", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://vpn.example/pkg.zip", *gwGot.Properties.VPNPackageURI)

	_, err = web.UpdateVnetConnectionGateway(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "primary", armappservice.VnetGateway{
		Properties: &armappservice.VnetGatewayProperties{VPNPackageURI: to.Ptr("https://vpn.example/pkg2.zip")},
	}, nil)
	require.NoError(t, err)

	// A classic DELETE clears the swift view too.
	_, err = web.DeleteVnetConnection(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", nil)
	require.NoError(t, err)
	swiftGot, err := web.GetSwiftVirtualNetworkConnection(ctx, rg, "s5-vnet-app", nil)
	require.NoError(t, err)
	if swiftGot.Properties != nil {
		assert.Nil(t, swiftGot.Properties.SubnetResourceID)
	}

	// A classic PUT referencing the subnet shows on the swift GET.
	_, err = web.CreateOrUpdateVnetConnection(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", armappservice.VnetInfoResource{
		Properties: &armappservice.VnetInfo{VnetResourceID: to.Ptr(subnetID)},
	}, nil)
	require.NoError(t, err)
	swiftGot, err = web.GetSwiftVirtualNetworkConnection(ctx, rg, "s5-vnet-app", nil)
	require.NoError(t, err)
	require.NotNil(t, swiftGot.Properties)
	require.NotNil(t, swiftGot.Properties.SubnetResourceID)
	assert.Equal(t, subnetID, *swiftGot.Properties.SubnetResourceID)

	_, err = web.UpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-vnet-app", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID)},
	}, nil)
	require.NoError(t, err)

	// The slot carries its own independent integration.
	slotPoller, err := web.BeginCreateOrUpdateSlot(ctx, rg, "s5-vnet-app", "stage", armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + unstartedSiteImage),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	_, err = web.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheckSlot(ctx, rg, "s5-vnet-app", "stage", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID), SwiftSupported: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	slotSwift, err := web.GetSwiftVirtualNetworkConnectionSlot(ctx, rg, "s5-vnet-app", "stage", nil)
	require.NoError(t, err)
	require.NotNil(t, slotSwift.Properties)
	assert.Equal(t, subnetID, *slotSwift.Properties.SubnetResourceID)
	slotList, err := web.ListVnetConnectionsSlot(ctx, rg, "s5-vnet-app", "stage", nil)
	require.NoError(t, err)
	require.Len(t, slotList.VnetInfoResourceArray, 1)
	slotConn, err := web.GetVnetConnectionSlot(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "stage", nil)
	require.NoError(t, err)
	assert.Equal(t, subnetID, *slotConn.Properties.VnetResourceID)
	_, err = web.UpdateVnetConnectionSlot(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "stage", armappservice.VnetInfoResource{
		Properties: &armappservice.VnetInfo{DNSServers: to.Ptr("10.60.0.54")},
	}, nil)
	require.NoError(t, err)
	_, err = web.CreateOrUpdateVnetConnectionGatewaySlot(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "primary", "stage", armappservice.VnetGateway{
		Properties: &armappservice.VnetGatewayProperties{VPNPackageURI: to.Ptr("https://vpn.example/slot.zip")},
	}, nil)
	require.NoError(t, err)
	slotGw, err := web.GetVnetConnectionGatewaySlot(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "primary", "stage", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://vpn.example/slot.zip", *slotGw.Properties.VPNPackageURI)
	_, err = web.UpdateVnetConnectionGatewaySlot(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "primary", "stage", armappservice.VnetGateway{
		Properties: &armappservice.VnetGatewayProperties{VPNPackageURI: to.Ptr("https://vpn.example/slot2.zip")},
	}, nil)
	require.NoError(t, err)
	_, err = web.UpdateSwiftVirtualNetworkConnectionWithCheckSlot(ctx, rg, "s5-vnet-app", "stage", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID)},
	}, nil)
	require.NoError(t, err)
	_, err = web.DeleteVnetConnectionSlot(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "stage", nil)
	require.NoError(t, err)
	_, err = web.CreateOrUpdateVnetConnectionSlot(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", "stage", armappservice.VnetInfoResource{
		Properties: &armappservice.VnetInfo{VnetResourceID: to.Ptr(subnetID)},
	}, nil)
	require.NoError(t, err)
	_, err = web.DeleteSwiftVirtualNetworkSlot(ctx, rg, "s5-vnet-app", "stage", nil)
	require.NoError(t, err)
	slotListAfter, err := web.ListVnetConnectionsSlot(ctx, rg, "s5-vnet-app", "stage", nil)
	require.NoError(t, err)
	assert.Empty(t, slotListAfter.VnetInfoResourceArray)
}

// TestSDK_WebVnet_PlanNetworkingTail: the plan-level views assemble from the
// connections the plan's sites have, and the instance surface answers from
// container state; this plan runs none.
func TestSDK_WebVnet_PlanNetworkingTail(t *testing.T) {
	t.Parallel()
	rg := "stage5-plan-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-tail-plan", "S1", "Standard")
	stage5CreateSite(t, rg, "s5-tail-app", planID, unstartedSiteImage, "", false)
	subnetID := stage5Subnet(t, rg, "s5-tail-vnet", "10.61.0.0/16", "appsvc", "10.61.1.0/24")

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = web.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-tail-app", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID), SwiftSupported: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)

	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	vnets, err := plans.ListVnets(ctx, rg, "s5-tail-plan", nil)
	require.NoError(t, err)
	require.Len(t, vnets.VnetInfoResourceArray, 1)
	assert.Equal(t, "s5-tail-vnet_appsvc", *vnets.VnetInfoResourceArray[0].Name)
	assert.Equal(t, subnetID, *vnets.VnetInfoResourceArray[0].Properties.VnetResourceID)

	planConn, err := plans.GetVnetFromServerFarm(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", nil)
	require.NoError(t, err)
	assert.Equal(t, subnetID, *planConn.Properties.VnetResourceID)

	// Routes on the plan connection show on the site connection read.
	route, err := plans.CreateOrUpdateVnetRoute(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "corp", armappservice.VnetRoute{
		Properties: &armappservice.VnetRouteProperties{
			StartAddress: to.Ptr("10.200.0.0"),
			EndAddress:   to.Ptr("10.200.255.255"),
			RouteType:    to.Ptr(armappservice.RouteTypeSTATIC),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "corp", *route.Name)

	routes, err := plans.ListRoutesForVnet(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", nil)
	require.NoError(t, err)
	require.Len(t, routes.VnetRouteArray, 1)

	// The single-route read answers an array, per the wire contract.
	one, err := plans.GetRouteForVnet(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "corp", nil)
	require.NoError(t, err)
	require.Len(t, one.VnetRouteArray, 1)
	assert.Equal(t, "10.200.0.0", *one.VnetRouteArray[0].Properties.StartAddress)

	_, err = plans.UpdateVnetRoute(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "corp", armappservice.VnetRoute{
		Properties: &armappservice.VnetRouteProperties{StartAddress: to.Ptr("10.201.0.0")},
	}, nil)
	require.NoError(t, err)

	siteConn, err := web.GetVnetConnection(ctx, rg, "s5-tail-app", "s5-tail-vnet_appsvc", nil)
	require.NoError(t, err)
	require.Len(t, siteConn.Properties.Routes, 1)
	assert.Equal(t, "10.201.0.0", *siteConn.Properties.Routes[0].Properties.StartAddress)

	_, err = plans.DeleteVnetRoute(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "corp", nil)
	require.NoError(t, err)

	_, err = plans.UpdateVnetGateway(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "primary", armappservice.VnetGateway{
		Properties: &armappservice.VnetGatewayProperties{VPNPackageURI: to.Ptr("https://vpn.example/plan.zip")},
	}, nil)
	require.NoError(t, err)
	planGw, err := plans.GetVnetGateway(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "primary", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://vpn.example/plan.zip", *planGw.Properties.VPNPackageURI)

	// The capabilities of the simulator's plan.
	caps, err := plans.ListCapabilities(ctx, rg, "s5-tail-plan", nil)
	require.NoError(t, err)
	capByName := map[string]string{}
	for _, c := range caps.CapabilityArray {
		capByName[*c.Name] = *c.Value
	}
	assert.Equal(t, "true", capByName["LinuxWorkers"])
	assert.Equal(t, "true", capByName["VnetIntegration"])

	// The selectable SKUs match the SKU the plan runs.
	skus, err := plans.GetServerFarmSKUs(ctx, rg, "s5-tail-plan", nil)
	require.NoError(t, err)
	skuDoc, ok := skus.Interface.(map[string]any)
	require.True(t, ok, "GetServerFarmSkus must answer a JSON object")
	assert.Equal(t, "Microsoft.Web/serverfarms", skuDoc["resourceType"])
	skuList, ok := skuDoc["skus"].([]any)
	require.True(t, ok)
	require.Len(t, skuList, 1)
	assert.Equal(t, "S1", skuList[0].(map[string]any)["name"])

	// No site on this plan runs a container, so the instance list is empty and
	// worker actions answer 404.
	details, err := plans.GetServerFarmInstanceDetails(ctx, rg, "s5-tail-plan", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(0), *details.InstanceCount)
	assert.Empty(t, details.Instances)

	_, err = plans.RebootWorker(ctx, rg, "s5-tail-plan", "deadbeef0000", nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

	_, err = plans.RecycleManagedInstanceWorker(ctx, rg, "s5-tail-plan", "deadbeef0000", nil)
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

	// The simulator's workers are Linux containers, so RDP reports that.
	_, err = plans.GetServerFarmRdpPassword(ctx, rg, "s5-tail-plan", nil)
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusBadRequest, respErr.StatusCode)
}

// TestSDK_WebVnet_JoinsRealNetwork: an Always On redis site joined through the
// classic spelling runs its container on the virtual network's Docker network,
// a function site joined through the swift spelling reaches it by name, and
// deleting the classic connection disconnects it.
func TestSDK_WebVnet_JoinsRealNetwork(t *testing.T) {
	t.Parallel()
	rg := "stage5-join-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-join-plan", "S1", "Standard")
	subnetID := stage5Subnet(t, rg, "s5-join-vnet", "10.62.0.0/16", "appsvc", "10.62.1.0/24")

	// A redis container group injected into a second subnet of the network holds
	// a private address there.
	subnets, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	aciSubnetPoller, err := subnets.BeginCreateOrUpdate(ctx, rg, "s5-join-vnet", "aci", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix: to.Ptr("10.62.2.0/24"),
			Delegations: []*armnetwork.Delegation{{
				Name:       to.Ptr("aci"),
				Properties: &armnetwork.ServiceDelegationPropertiesFormat{ServiceName: to.Ptr("Microsoft.ContainerInstance/containerGroups")},
			}},
		},
	}, nil)
	require.NoError(t, err)
	aciSubnet, err := aciSubnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	groups, err := armcontainerinstance.NewContainerGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	cpu, mem := 1.0, 1.0
	groupPoller, err := groups.BeginCreateOrUpdate(ctx, rg, "s5-join-redis", armcontainerinstance.ContainerGroup{
		Location: to.Ptr("eastus"),
		Properties: &armcontainerinstance.ContainerGroupProperties{
			OSType:    to.Ptr(armcontainerinstance.OperatingSystemTypesLinux),
			SubnetIDs: []*armcontainerinstance.ContainerGroupSubnetID{{ID: aciSubnet.ID}},
			IPAddress: &armcontainerinstance.IPAddress{
				Type:  to.Ptr(armcontainerinstance.ContainerGroupIPAddressTypePrivate),
				Ports: []*armcontainerinstance.Port{{Port: to.Ptr[int32](6379), Protocol: to.Ptr(armcontainerinstance.ContainerGroupNetworkProtocolTCP)}},
			},
			Containers: []*armcontainerinstance.Container{{
				Name: to.Ptr("redis"),
				Properties: &armcontainerinstance.ContainerProperties{
					Image: to.Ptr("mirror.gcr.io/library/redis:7-alpine"),
					Ports: []*armcontainerinstance.ContainerPort{{Port: to.Ptr[int32](6379)}},
					Resources: &armcontainerinstance.ResourceRequirements{
						Requests: &armcontainerinstance.ResourceRequests{CPU: &cpu, MemoryInGB: &mem},
					},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	group, err := groupPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := groups.BeginDelete(ctx, rg, "s5-join-redis", nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	require.NotNil(t, group.Properties.IPAddress)
	redisAddress := derefString(group.Properties.IPAddress.IP)
	require.True(t, strings.HasPrefix(redisAddress, "10.62.2."), "the group holds an address in its subnet, got %q", redisAddress)

	// The network's private zone names the group.
	zones, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zonePoller, err := zones.BeginCreateOrUpdate(ctx, rg, "join.internal", armprivatedns.PrivateZone{Location: to.Ptr("global")}, nil)
	require.NoError(t, err)
	_, err = zonePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	links, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/virtualNetworks/s5-join-vnet"
	linkPoller, err := links.BeginCreateOrUpdate(ctx, rg, "join.internal", "s5-join-link", armprivatedns.VirtualNetworkLink{
		Location:   to.Ptr("global"),
		Properties: &armprivatedns.VirtualNetworkLinkProperties{VirtualNetwork: &armprivatedns.SubResource{ID: to.Ptr(vnetID)}},
	}, nil)
	require.NoError(t, err)
	_, err = linkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	records, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = records.CreateOrUpdate(ctx, rg, "join.internal", armprivatedns.RecordTypeA, "redis", armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL:      to.Ptr[int64](10),
			ARecords: []*armprivatedns.ARecord{{IPv4Address: to.Ptr(redisAddress)}},
		},
	}, nil)
	require.NoError(t, err)

	// The probe site speaks the redis protocol to the service by its private zone
	// name.
	stage5CreateSite(t, rg, "s5-join-probe", planID, commandImageName,
		"relay-http 80 redis.join.internal:6379 PING", false)
	defer azureDeleteSite(rg, "s5-join-probe")

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = web.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-join-probe", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID), SwiftSupported: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)

	// The probe resolves redis through the network's resolver and gets +PONG. The
	// group's redis opens 6379 a moment after the group reports running, and App
	// Service reports no readiness for a raw service, so wait for that answer.
	var lastStatus int
	var lastBody []byte
	require.Eventually(t, func() bool {
		lastStatus, lastBody = azureInvokeFunctionResponse(t, "s5-join-probe")
		return lastStatus == http.StatusOK && strings.Contains(string(lastBody), "+PONG")
	}, 60*time.Second, 250*time.Millisecond,
		"the probe never reached redis by name over the VNet; last answer %d: %s", lastStatus, lastBody)

	// Deleting the integration restarts the probe off the network, and the same
	// probe fails.
	_, err = web.DeleteSwiftVirtualNetwork(ctx, rg, "s5-join-probe", nil)
	require.NoError(t, err)
	status, body := azureInvokeFunctionResponse(t, "s5-join-probe")
	assert.Equal(t, http.StatusBadGateway, status, "the probe can no longer reach redis: %s", body)

	// The site's running container is the plan's live worker, and rebooting it
	// through the plan API tears it down.
	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	details, err := plans.GetServerFarmInstanceDetails(ctx, rg, "s5-join-plan", nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), *details.InstanceCount)
	require.Len(t, details.Instances, 1)
	workerName := *details.Instances[0].InstanceName
	require.NotEmpty(t, workerName)
	assert.Equal(t, "Ready", *details.Instances[0].Status)

	_, err = plans.RebootWorker(ctx, rg, "s5-join-plan", workerName, nil)
	require.NoError(t, err)
	details, err = plans.GetServerFarmInstanceDetails(ctx, rg, "s5-join-plan", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(0), *details.InstanceCount)
}

// TestSDK_WebHybridConnections_SiteAndPlanViews covers both hybrid connection
// families on a site and the plan-level views of the same store.
func TestSDK_WebHybridConnections_SiteAndPlanViews(t *testing.T) {
	t.Parallel()
	rg := "stage5-hybrid-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-hyb-plan", "S1", "Standard")
	siteID := stage5CreateSite(t, rg, "s5-hyb-app", planID, unstartedSiteImage, "", false)

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// The Service Bus relay send key is kept but never returned on an ARM read;
	// only the plan's listKeys returns it.
	put, err := web.CreateOrUpdateHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay", armappservice.HybridConnection{
		Properties: &armappservice.HybridConnectionProperties{
			Hostname:     to.Ptr("onprem.internal"),
			Port:         to.Ptr(int32(1433)),
			SendKeyName:  to.Ptr("defaultSender"),
			SendKeyValue: to.Ptr("s5-secret-send-key"),
			RelayArmURI:  to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Relay/namespaces/s5-relay-ns/hybridConnections/s5-relay"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "s5-relay-ns", *put.Properties.ServiceBusNamespace)
	assert.Equal(t, "s5-relay", *put.Properties.RelayName)
	assert.Nil(t, put.Properties.SendKeyValue, "sendKeyValue must not ride an ARM response")

	got, err := web.GetHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay", nil)
	require.NoError(t, err)
	assert.Equal(t, "onprem.internal", *got.Properties.Hostname)
	assert.Nil(t, got.Properties.SendKeyValue)

	_, err = web.UpdateHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay", armappservice.HybridConnection{
		Properties: &armappservice.HybridConnectionProperties{Port: to.Ptr(int32(1434))},
	}, nil)
	require.NoError(t, err)

	// The site view has the single-resource contract shape.
	relays, err := web.ListHybridConnections(ctx, rg, "s5-hyb-app", nil)
	require.NoError(t, err)
	require.NotNil(t, relays.ID)
	assert.True(t, strings.HasSuffix(*relays.ID, "/hybridConnectionNamespaces/s5-relay-ns/relays/s5-relay"))

	// The classic (BizTalk) relay service connection, a distinct family.
	_, err = web.CreateOrUpdateRelayServiceConnection(ctx, rg, "s5-hyb-app", "s5-biztalk", armappservice.RelayServiceConnectionEntity{
		Properties: &armappservice.RelayServiceConnectionEntityProperties{
			Hostname:     to.Ptr("legacy.internal"),
			Port:         to.Ptr(int32(8080)),
			ResourceType: to.Ptr("Microsoft.Web/sites"),
		},
	}, nil)
	require.NoError(t, err)
	entity, err := web.GetRelayServiceConnection(ctx, rg, "s5-hyb-app", "s5-biztalk", nil)
	require.NoError(t, err)
	assert.Equal(t, "legacy.internal", *entity.Properties.Hostname)
	entityList, err := web.ListRelayServiceConnections(ctx, rg, "s5-hyb-app", nil)
	require.NoError(t, err)
	require.NotNil(t, entityList.ID)
	assert.True(t, strings.HasSuffix(*entityList.ID, "/hybridconnection/s5-biztalk"))
	_, err = web.UpdateRelayServiceConnection(ctx, rg, "s5-hyb-app", "s5-biztalk", armappservice.RelayServiceConnectionEntity{
		Properties: &armappservice.RelayServiceConnectionEntityProperties{Hostname: to.Ptr("legacy2.internal"), Port: to.Ptr(int32(8080))},
	}, nil)
	require.NoError(t, err)

	// networkFeatures reports both families.
	features, err := web.ListNetworkFeatures(ctx, rg, "s5-hyb-app", "summary", nil)
	require.NoError(t, err)
	require.Len(t, features.Properties.HybridConnectionsV2, 1)
	assert.Equal(t, "s5-relay", *features.Properties.HybridConnectionsV2[0].Properties.RelayName)
	require.Len(t, features.Properties.HybridConnections, 1)
	assert.Equal(t, "legacy2.internal", *features.Properties.HybridConnections[0].Properties.Hostname)

	// The plan-level views assemble from the site-level state.
	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := plans.NewListHybridConnectionsPager(rg, "s5-hyb-plan", nil)
	var planRelays []*armappservice.HybridConnection
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		planRelays = append(planRelays, page.Value...)
	}
	require.Len(t, planRelays, 1)
	assert.Nil(t, planRelays[0].Properties.SendKeyValue)

	planGot, err := plans.GetHybridConnection(ctx, rg, "s5-hyb-plan", "s5-relay-ns", "s5-relay", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1434), *planGot.Properties.Port)

	keys, err := plans.ListHybridConnectionKeys(ctx, rg, "s5-hyb-plan", "s5-relay-ns", "s5-relay", nil)
	require.NoError(t, err)
	assert.Equal(t, "defaultSender", *keys.Properties.SendKeyName)
	assert.Equal(t, "s5-secret-send-key", *keys.Properties.SendKeyValue)

	appsPager := plans.NewListWebAppsByHybridConnectionPager(rg, "s5-hyb-plan", "s5-relay-ns", "s5-relay", nil)
	var apps []*string
	for appsPager.More() {
		page, err := appsPager.NextPage(ctx)
		require.NoError(t, err)
		apps = append(apps, page.Value...)
	}
	require.Len(t, apps, 1)
	assert.True(t, strings.EqualFold(siteID, *apps[0]))

	limit, err := plans.GetHybridConnectionPlanLimit(ctx, rg, "s5-hyb-plan", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), *limit.Properties.Current)
	assert.Equal(t, int32(25), *limit.Properties.Maximum, "a Standard plan allows 25 hybrid connections")

	// A plan-level delete removes the site-level connection.
	_, err = plans.DeleteHybridConnection(ctx, rg, "s5-hyb-plan", "s5-relay-ns", "s5-relay", nil)
	require.NoError(t, err)
	_, err = web.GetHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay", nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

	_, err = web.CreateOrUpdateHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay2", armappservice.HybridConnection{
		Properties: &armappservice.HybridConnectionProperties{Hostname: to.Ptr("onprem.internal"), Port: to.Ptr(int32(5671))},
	}, nil)
	require.NoError(t, err)
	_, err = web.DeleteHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay2", nil)
	require.NoError(t, err)
	_, err = web.DeleteRelayServiceConnection(ctx, rg, "s5-hyb-app", "s5-biztalk", nil)
	require.NoError(t, err)

	slotPoller, err := web.BeginCreateOrUpdateSlot(ctx, rg, "s5-hyb-app", "stage", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{ServerFarmID: to.Ptr(planID)},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = web.CreateOrUpdateHybridConnectionSlot(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-slot-relay", "stage", armappservice.HybridConnection{
		Properties: &armappservice.HybridConnectionProperties{Hostname: to.Ptr("onprem.internal"), Port: to.Ptr(int32(5671))},
	}, nil)
	require.NoError(t, err)
	slotGot, err := web.GetHybridConnectionSlot(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-slot-relay", "stage", nil)
	require.NoError(t, err)
	assert.Equal(t, "s5-slot-relay", *slotGot.Properties.RelayName)
	_, err = web.UpdateHybridConnectionSlot(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-slot-relay", "stage", armappservice.HybridConnection{
		Properties: &armappservice.HybridConnectionProperties{Port: to.Ptr(int32(5672))},
	}, nil)
	require.NoError(t, err)
	slotRelays, err := web.ListHybridConnectionsSlot(ctx, rg, "s5-hyb-app", "stage", nil)
	require.NoError(t, err)
	require.NotNil(t, slotRelays.ID)
	_, err = web.CreateOrUpdateRelayServiceConnectionSlot(ctx, rg, "s5-hyb-app", "s5-slot-biztalk", "stage", armappservice.RelayServiceConnectionEntity{
		Properties: &armappservice.RelayServiceConnectionEntityProperties{Hostname: to.Ptr("legacy.internal"), Port: to.Ptr(int32(8081))},
	}, nil)
	require.NoError(t, err)
	_, err = web.GetRelayServiceConnectionSlot(ctx, rg, "s5-hyb-app", "s5-slot-biztalk", "stage", nil)
	require.NoError(t, err)
	_, err = web.UpdateRelayServiceConnectionSlot(ctx, rg, "s5-hyb-app", "s5-slot-biztalk", "stage", armappservice.RelayServiceConnectionEntity{
		Properties: &armappservice.RelayServiceConnectionEntityProperties{Hostname: to.Ptr("legacy3.internal"), Port: to.Ptr(int32(8081))},
	}, nil)
	require.NoError(t, err)
	_, err = web.ListRelayServiceConnectionsSlot(ctx, rg, "s5-hyb-app", "stage", nil)
	require.NoError(t, err)
	slotFeatures, err := web.ListNetworkFeaturesSlot(ctx, rg, "s5-hyb-app", "summary", "stage", nil)
	require.NoError(t, err)
	require.Len(t, slotFeatures.Properties.HybridConnectionsV2, 1)
	_, err = web.DeleteHybridConnectionSlot(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-slot-relay", "stage", nil)
	require.NoError(t, err)
	_, err = web.DeleteRelayServiceConnectionSlot(ctx, rg, "s5-hyb-app", "s5-slot-biztalk", "stage", nil)
	require.NoError(t, err)
}

// TestSDK_WebPrivateAccess_RoundTrip: an absent private access record reads as
// disabled with no virtual networks, and a PUT round-trips, on site and slot.
func TestSDK_WebPrivateAccess_RoundTrip(t *testing.T) {
	t.Parallel()
	s5PaAppName := uniquename.New("s5-pa-app")
	rg := uniquename.New("stage5-pa-rg")
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-pa-plan", "S1", "Standard")
	stage5CreateSite(t, rg, s5PaAppName, planID, unstartedSiteImage, "", false)

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	initial, err := web.GetPrivateAccess(ctx, rg, s5PaAppName, nil)
	require.NoError(t, err)
	require.NotNil(t, initial.Properties)
	assert.False(t, *initial.Properties.Enabled)
	assert.Empty(t, initial.Properties.VirtualNetworks)

	vnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/virtualNetworks/s5-pa-vnet"
	put, err := web.PutPrivateAccessVnet(ctx, rg, s5PaAppName, armappservice.PrivateAccess{
		Properties: &armappservice.PrivateAccessProperties{
			Enabled: to.Ptr(true),
			VirtualNetworks: []*armappservice.PrivateAccessVirtualNetwork{{
				Name:       to.Ptr("s5-pa-vnet"),
				Key:        to.Ptr(int32(1)),
				ResourceID: to.Ptr(vnetID),
				Subnets: []*armappservice.PrivateAccessSubnet{{
					Name: to.Ptr("appsvc"),
					Key:  to.Ptr(int32(1)),
				}},
			}},
		},
	}, nil)
	require.NoError(t, err)
	assert.True(t, *put.Properties.Enabled)
	require.Len(t, put.Properties.VirtualNetworks, 1)
	assert.Equal(t, vnetID, *put.Properties.VirtualNetworks[0].ResourceID)

	got, err := web.GetPrivateAccess(ctx, rg, s5PaAppName, nil)
	require.NoError(t, err)
	require.Len(t, got.Properties.VirtualNetworks, 1)
	require.Len(t, got.Properties.VirtualNetworks[0].Subnets, 1)
	assert.Equal(t, "appsvc", *got.Properties.VirtualNetworks[0].Subnets[0].Name)

	slotPoller, err := web.BeginCreateOrUpdateSlot(ctx, rg, s5PaAppName, "stage", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{ServerFarmID: to.Ptr(planID)},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotInitial, err := web.GetPrivateAccessSlot(ctx, rg, s5PaAppName, "stage", nil)
	require.NoError(t, err)
	assert.False(t, *slotInitial.Properties.Enabled)
	_, err = web.PutPrivateAccessVnetSlot(ctx, rg, s5PaAppName, "stage", armappservice.PrivateAccess{
		Properties: &armappservice.PrivateAccessProperties{Enabled: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
}

// TestSDK_WebSitePrivateEndpointConnections: the site's connection collection
// serves the connection a Microsoft.Network private endpoint opens on the site,
// through approval and LRO delete, plus the private link resource catalog.
func TestSDK_WebSitePrivateEndpointConnections(t *testing.T) {
	t.Parallel()
	// The private endpoint needs a network namespace, like every private endpoint
	// test.
	rg := "stage5-pec-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-pec-plan", "S1", "Standard")
	siteID := stage5CreateSite(t, rg, "s5-pec-app", planID, unstartedSiteImage, "", false)
	subnetID := stage5Subnet(t, rg, "s5-pec-vnet", "10.63.0.0/16", "pe-subnet", "10.63.1.0/24")

	// Same subscription, so the connection is auto-approved.
	peClient, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "s5-site-pe", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID)},
			PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name: to.Ptr("s5-site-pe"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: to.Ptr(siteID),
					GroupIDs:             []*string{to.Ptr("sites")},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	pe, err := pePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Len(t, pe.Properties.CustomDNSConfigs, 1)
	assert.Equal(t, "s5-pec-app.azurewebsites.net", *pe.Properties.CustomDNSConfigs[0].Fqdn)

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := web.NewGetPrivateEndpointConnectionListPager(rg, "s5-pec-app", nil)
	var conns []*armappservice.RemotePrivateEndpointConnectionARMResource
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		conns = append(conns, page.Value...)
	}
	require.Len(t, conns, 1)
	assert.Equal(t, "s5-site-pe", *conns[0].Name)
	require.NotNil(t, conns[0].Properties.PrivateLinkServiceConnectionState)
	assert.Equal(t, "Approved", *conns[0].Properties.PrivateLinkServiceConnectionState.Status)
	require.NotNil(t, conns[0].Properties.PrivateEndpoint)
	assert.NotEmpty(t, *conns[0].Properties.PrivateEndpoint.ID)

	got, err := web.GetPrivateEndpointConnection(ctx, rg, "s5-pec-app", "s5-site-pe", nil)
	require.NoError(t, err)
	assert.Equal(t, "Approved", *got.Properties.PrivateLinkServiceConnectionState.Status)

	rejectPoller, err := web.BeginApproveOrRejectPrivateEndpointConnection(ctx, rg, "s5-pec-app", "s5-site-pe",
		armappservice.RemotePrivateEndpointConnectionARMResource{
			Properties: &armappservice.RemotePrivateEndpointConnectionARMResourceProperties{
				PrivateLinkServiceConnectionState: &armappservice.PrivateLinkConnectionState{
					Status:      to.Ptr("Rejected"),
					Description: to.Ptr("not welcome"),
				},
			},
		}, nil)
	require.NoError(t, err)
	rejected, err := rejectPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Rejected", *rejected.Properties.PrivateLinkServiceConnectionState.Status)

	// The endpoint's own read shows the owner's decision: one object.
	peGot, err := peClient.Get(ctx, rg, "s5-site-pe", nil)
	require.NoError(t, err)
	peState := peGot.Properties.PrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState
	require.NotNil(t, peState)
	assert.Equal(t, "Rejected", *peState.Status)

	// The private link resource catalog names the sites group and its zone.
	plr, err := web.GetPrivateLinkResources(ctx, rg, "s5-pec-app", nil)
	require.NoError(t, err)
	require.Len(t, plr.Value, 1)
	assert.Equal(t, "sites", *plr.Value[0].Properties.GroupID)
	require.Len(t, plr.Value[0].Properties.RequiredZoneNames, 1)
	assert.Equal(t, "privatelink.azurewebsites.net", *plr.Value[0].Properties.RequiredZoneNames[0])

	delPoller, err := web.BeginDeletePrivateEndpointConnection(ctx, rg, "s5-pec-app", "s5-site-pe", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = web.GetPrivateEndpointConnection(ctx, rg, "s5-pec-app", "s5-site-pe", nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

	// The slot publishes its slot-scoped group.
	slotPoller, err := web.BeginCreateOrUpdateSlot(ctx, rg, "s5-pec-app", "stage", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{ServerFarmID: to.Ptr(planID)},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotPLR, err := web.GetPrivateLinkResourcesSlot(ctx, rg, "s5-pec-app", "stage", nil)
	require.NoError(t, err)
	require.Len(t, slotPLR.Value, 1)
	assert.Equal(t, "sites-stage", *slotPLR.Value[0].Properties.GroupID)
	slotPECs := web.NewGetPrivateEndpointConnectionListSlotPager(rg, "s5-pec-app", "stage", nil)
	for slotPECs.More() {
		page, err := slotPECs.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value)
	}
}
