package azure_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the App Service networking surfaces: the classic
// virtualNetworkConnections spelling of VNet integration (coherent with the
// swift networkConfig/virtualNetwork spelling), connection gateways, private
// access, networkFeatures, site private endpoint connections + private link
// resources, both hybrid connection families, and the App Service plan
// networking/worker tail:
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

// stage5EnsurePlan creates an App Service plan with the given SKU and returns
// its resource id.
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

// stage5CreateSite creates a function app bound to planID. A non-empty image
// becomes the site's Linux container; a non-empty command rides the real
// SOCKERLESS_CMD app-setting contract, exactly as a sockerless invocation
// delivers it.
func stage5CreateSite(t *testing.T, rg, name, planID, image string, command []string) string {
	t.Helper()
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	siteConfig := &armappservice.SiteConfig{}
	if image != "" {
		siteConfig.LinuxFxVersion = to.Ptr("DOCKER|" + image)
	}
	if len(command) > 0 {
		cmdJSON, err := json.Marshal(command)
		require.NoError(t, err)
		siteConfig.AppSettings = []*armappservice.NameValuePair{{
			Name:  to.Ptr("SOCKERLESS_CMD"),
			Value: to.Ptr(base64.StdEncoding.EncodeToString(cmdJSON)),
		}}
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

// stage5Subnet creates a VNet + subnet delegated to Microsoft.Web/serverFarms
// through armnetwork — the real Microsoft.Network store the connection
// handlers resolve against — and returns the subnet's ARM id.
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

// TestSDK_WebVnet_SwiftClassicCoherence proves the two ARM spellings of VNet
// integration describe ONE state: a swift PUT surfaces in the classic
// virtualNetworkConnections list (the read `az webapp vnet-integration list`
// does), a classic PUT surfaces on the swift GET, and a classic DELETE clears
// the swift view. Also covers connection gateways and the slot spellings.
func TestSDK_WebVnet_SwiftClassicCoherence(t *testing.T) {
	rg := "stage5-vnet-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-vnet-plan", "S1", "Standard")
	stage5CreateSite(t, rg, "s5-vnet-app", planID, "registry.example/sockerless-overlay/azf:test", nil)
	subnetID := stage5Subnet(t, rg, "s5-vnet", "10.60.0.0/16", "appsvc", "10.60.1.0/24")

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Swift PUT — the regional integration.
	swiftPut, err := web.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-vnet-app", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{
			SubnetResourceID: to.Ptr(subnetID),
			SwiftSupported:   to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, swiftPut.Properties)
	assert.Equal(t, subnetID, *swiftPut.Properties.SubnetResourceID)

	// The classic list shows the SAME integration: named <vnet>_<subnet>,
	// isSwift, vnetResourceId = the subnet.
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

	// PATCH (UpdateVnetConnection) keeps the target and adds DNS servers.
	patched, err := web.UpdateVnetConnection(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", armappservice.VnetInfoResource{
		Properties: &armappservice.VnetInfo{DNSServers: to.Ptr("10.60.0.53")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, subnetID, *patched.Properties.VnetResourceID)
	assert.Equal(t, "10.60.0.53", *patched.Properties.DNSServers)

	// Connection gateway: a pure ARM record on the connection.
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

	// Classic DELETE clears the swift view too.
	_, err = web.DeleteVnetConnection(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", nil)
	require.NoError(t, err)
	swiftGot, err := web.GetSwiftVirtualNetworkConnection(ctx, rg, "s5-vnet-app", nil)
	require.NoError(t, err)
	if swiftGot.Properties != nil {
		assert.Nil(t, swiftGot.Properties.SubnetResourceID)
	}

	// Classic PUT (subnet-referencing) surfaces on the swift GET.
	_, err = web.CreateOrUpdateVnetConnection(ctx, rg, "s5-vnet-app", "s5-vnet_appsvc", armappservice.VnetInfoResource{
		Properties: &armappservice.VnetInfo{VnetResourceID: to.Ptr(subnetID)},
	}, nil)
	require.NoError(t, err)
	swiftGot, err = web.GetSwiftVirtualNetworkConnection(ctx, rg, "s5-vnet-app", nil)
	require.NoError(t, err)
	require.NotNil(t, swiftGot.Properties)
	require.NotNil(t, swiftGot.Properties.SubnetResourceID)
	assert.Equal(t, subnetID, *swiftGot.Properties.SubnetResourceID)

	// Swift PATCH (UpdateSwiftVirtualNetworkConnectionWithCheck).
	_, err = web.UpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-vnet-app", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID)},
	}, nil)
	require.NoError(t, err)

	// Slot spellings: the slot carries its own independent integration.
	slotPoller, err := web.BeginCreateOrUpdateSlot(ctx, rg, "s5-vnet-app", "stage", armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|registry.example/sockerless-overlay/azf:test"),
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

// TestSDK_WebVnet_PlanNetworkingTail exercises the App Service plan networking
// and worker tail. The plan-level VNet views assemble from the connections the
// plan's sites really have; the instance surface answers from real container
// state (this plan runs none, so it answers the honest empty set).
func TestSDK_WebVnet_PlanNetworkingTail(t *testing.T) {
	rg := "stage5-plan-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-tail-plan", "S1", "Standard")
	stage5CreateSite(t, rg, "s5-tail-app", planID, "registry.example/sockerless-overlay/azf:test", nil)
	subnetID := stage5Subnet(t, rg, "s5-tail-vnet", "10.61.0.0/16", "appsvc", "10.61.1.0/24")

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = web.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-tail-app", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID), SwiftSupported: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)

	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// The plan-level view lists the site's integration.
	vnets, err := plans.ListVnets(ctx, rg, "s5-tail-plan", nil)
	require.NoError(t, err)
	require.Len(t, vnets.VnetInfoResourceArray, 1)
	assert.Equal(t, "s5-tail-vnet_appsvc", *vnets.VnetInfoResourceArray[0].Name)
	assert.Equal(t, subnetID, *vnets.VnetInfoResourceArray[0].Properties.VnetResourceID)

	planConn, err := plans.GetVnetFromServerFarm(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", nil)
	require.NoError(t, err)
	assert.Equal(t, subnetID, *planConn.Properties.VnetResourceID)

	// Routes CRUD on the plan connection; the site connection read surfaces
	// the plan's routes.
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

	// The single-route read answers an array — the wire contract's shape.
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

	// Plan-level connection gateway.
	_, err = plans.UpdateVnetGateway(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "primary", armappservice.VnetGateway{
		Properties: &armappservice.VnetGatewayProperties{VPNPackageURI: to.Ptr("https://vpn.example/plan.zip")},
	}, nil)
	require.NoError(t, err)
	planGw, err := plans.GetVnetGateway(ctx, rg, "s5-tail-plan", "s5-tail-vnet_appsvc", "primary", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://vpn.example/plan.zip", *planGw.Properties.VPNPackageURI)

	// Capabilities: what the simulator's plan really provides.
	caps, err := plans.ListCapabilities(ctx, rg, "s5-tail-plan", nil)
	require.NoError(t, err)
	capByName := map[string]string{}
	for _, c := range caps.CapabilityArray {
		capByName[*c.Name] = *c.Value
	}
	assert.Equal(t, "true", capByName["LinuxWorkers"])
	assert.Equal(t, "true", capByName["VnetIntegration"])

	// The selectable SKUs are consistent with the SKU the plan runs.
	skus, err := plans.GetServerFarmSKUs(ctx, rg, "s5-tail-plan", nil)
	require.NoError(t, err)
	skuDoc, ok := skus.Interface.(map[string]any)
	require.True(t, ok, "GetServerFarmSkus must answer a JSON object")
	assert.Equal(t, "Microsoft.Web/serverfarms", skuDoc["resourceType"])
	skuList, ok := skuDoc["skus"].([]any)
	require.True(t, ok)
	require.Len(t, skuList, 1)
	assert.Equal(t, "S1", skuList[0].(map[string]any)["name"])

	// No site on this plan runs a container, so the instance surface answers
	// the honest empty set, and worker actions answer 404.
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

	// The simulator's workers are Linux containers; RDP reports that truthfully.
	_, err = plans.GetServerFarmRdpPassword(ctx, rg, "s5-tail-plan", nil)
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusBadRequest, respErr.StatusCode)
}

// TestSDK_WebVnet_JoinsRealNetwork proves a VNet connection is a real join,
// not a record: a redis `services:` site put on the VNet through the CLASSIC
// spelling starts its real container on the VNet's Docker network, a function
// site integrated through the swift spelling reaches it BY NAME from its
// per-invocation container, and deleting the classic connection really
// disconnects — the same probe then fails.
func TestSDK_WebVnet_JoinsRealNetwork(t *testing.T) {
	rg := "stage5-join-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-join-plan", "S1", "Standard")
	subnetID := stage5Subnet(t, rg, "s5-join-vnet", "10.62.0.0/16", "appsvc", "10.62.1.0/24")

	// A redis `services:` site: no function bootstrap, so VNet integration is
	// its run trigger.
	stage5CreateSite(t, rg, "s5-join-redis", planID, "public.ecr.aws/docker/library/redis:7-alpine", nil)
	defer azureDeleteSite(rg, "s5-join-redis")

	// The probe function site: speaks the redis protocol to the service BY
	// SITE NAME over the VNet.
	stage5CreateSite(t, rg, "s5-join-probe", planID, "public.ecr.aws/docker/library/alpine:latest",
		[]string{"sh", "-c", "printf 'PING\\r\\n' | nc -w 5 s5-join-redis 6379"})
	defer azureDeleteSite(rg, "s5-join-probe")

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Classic PUT joins the redis site — its container starts on the network.
	_, err = web.CreateOrUpdateVnetConnection(ctx, rg, "s5-join-redis", "s5-join-vnet_appsvc", armappservice.VnetInfoResource{
		Properties: &armappservice.VnetInfo{VnetResourceID: to.Ptr(subnetID)},
	}, nil)
	require.NoError(t, err)

	// Swift PUT integrates the probe site.
	_, err = web.CreateOrUpdateSwiftVirtualNetworkConnectionWithCheck(ctx, rg, "s5-join-probe", armappservice.SwiftVirtualNetwork{
		Properties: &armappservice.SwiftVirtualNetworkProperties{SubnetResourceID: to.Ptr(subnetID), SwiftSupported: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)

	// The probe's invocation container runs on the VNet and reaches redis by
	// name: the wire answer is redis's own +PONG.
	respBody := azureInvokeFunction(t, "s5-join-probe")
	assert.Contains(t, string(respBody), "+PONG")

	// Deleting the classic connection really disconnects the redis container:
	// the same probe now fails.
	_, err = web.DeleteVnetConnection(ctx, rg, "s5-join-redis", "s5-join-vnet_appsvc", nil)
	require.NoError(t, err)
	azureInvokeFunctionExpectError(t, "s5-join-probe")

	// The plan's worker-instance surface answers from the same real container
	// state: the redis site's persistent container is the plan's one live
	// worker, and rebooting it through the plan API really tears it down.
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
// families on a site and the plan-level views assembled from the same store.
func TestSDK_WebHybridConnections_SiteAndPlanViews(t *testing.T) {
	rg := "stage5-hybrid-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-hyb-plan", "S1", "Standard")
	siteID := stage5CreateSite(t, rg, "s5-hyb-app", planID, "registry.example/sockerless-overlay/azf:test", nil)

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Service Bus relay hybrid connection: the send key is carried as state
	// but never returned on an ARM read — only the plan's listKeys action.
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

	// The site view (single-resource contract shape).
	relays, err := web.ListHybridConnections(ctx, rg, "s5-hyb-app", nil)
	require.NoError(t, err)
	require.NotNil(t, relays.ID)
	assert.True(t, strings.HasSuffix(*relays.ID, "/hybridConnectionNamespaces/s5-relay-ns/relays/s5-relay"))

	// Classic (BizTalk) relay service connection — the second, distinct family.
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

	// networkFeatures assembles the REAL connection state: both families.
	features, err := web.ListNetworkFeatures(ctx, rg, "s5-hyb-app", "summary", nil)
	require.NoError(t, err)
	require.Len(t, features.Properties.HybridConnectionsV2, 1)
	assert.Equal(t, "s5-relay", *features.Properties.HybridConnectionsV2[0].Properties.RelayName)
	require.Len(t, features.Properties.HybridConnections, 1)
	assert.Equal(t, "legacy2.internal", *features.Properties.HybridConnections[0].Properties.Hostname)

	// Plan-level views assemble from the same site-level state.
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

	// Plan-level delete removes the site-level connection.
	_, err = plans.DeleteHybridConnection(ctx, rg, "s5-hyb-plan", "s5-relay-ns", "s5-relay", nil)
	require.NoError(t, err)
	_, err = web.GetHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay", nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

	// Site-level lifecycle tail: recreate, then delete through the site.
	_, err = web.CreateOrUpdateHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay2", armappservice.HybridConnection{
		Properties: &armappservice.HybridConnectionProperties{Hostname: to.Ptr("onprem.internal"), Port: to.Ptr(int32(5671))},
	}, nil)
	require.NoError(t, err)
	_, err = web.DeleteHybridConnection(ctx, rg, "s5-hyb-app", "s5-relay-ns", "s5-relay2", nil)
	require.NoError(t, err)
	_, err = web.DeleteRelayServiceConnection(ctx, rg, "s5-hyb-app", "s5-biztalk", nil)
	require.NoError(t, err)

	// Slot spellings of both families.
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

// TestSDK_WebPrivateAccess_RoundTrip covers the private access record on site
// and slot: absent reads as disabled with no Virtual Networks, and a PUT
// round-trips exactly.
func TestSDK_WebPrivateAccess_RoundTrip(t *testing.T) {
	rg := "stage5-pa-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-pa-plan", "S1", "Standard")
	stage5CreateSite(t, rg, "s5-pa-app", planID, "registry.example/sockerless-overlay/azf:test", nil)

	web, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	initial, err := web.GetPrivateAccess(ctx, rg, "s5-pa-app", nil)
	require.NoError(t, err)
	require.NotNil(t, initial.Properties)
	assert.False(t, *initial.Properties.Enabled)
	assert.Empty(t, initial.Properties.VirtualNetworks)

	vnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/virtualNetworks/s5-pa-vnet"
	put, err := web.PutPrivateAccessVnet(ctx, rg, "s5-pa-app", armappservice.PrivateAccess{
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

	got, err := web.GetPrivateAccess(ctx, rg, "s5-pa-app", nil)
	require.NoError(t, err)
	require.Len(t, got.Properties.VirtualNetworks, 1)
	require.Len(t, got.Properties.VirtualNetworks[0].Subnets, 1)
	assert.Equal(t, "appsvc", *got.Properties.VirtualNetworks[0].Subnets[0].Name)

	// Slot spelling.
	slotPoller, err := web.BeginCreateOrUpdateSlot(ctx, rg, "s5-pa-app", "stage", armappservice.Site{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("functionapp,linux,container"),
		Properties: &armappservice.SiteProperties{ServerFarmID: to.Ptr(planID)},
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotInitial, err := web.GetPrivateAccessSlot(ctx, rg, "s5-pa-app", "stage", nil)
	require.NoError(t, err)
	assert.False(t, *slotInitial.Properties.Enabled)
	_, err = web.PutPrivateAccessVnetSlot(ctx, rg, "s5-pa-app", "stage", armappservice.PrivateAccess{
		Properties: &armappservice.PrivateAccessProperties{Enabled: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
}

// TestSDK_WebSitePrivateEndpointConnections proves the site PEC surface serves
// the SAME connection object a Microsoft.Network private endpoint opens
// against the site — approval lifecycle and LRO delete included — plus the
// site's private link resource catalog.
func TestSDK_WebSitePrivateEndpointConnections(t *testing.T) {
	// The connection is opened by a real Microsoft.Network private endpoint,
	// which the simulator provisions into a network namespace — the same host
	// capability every other private-endpoint test requires.
	requireNetworkHost(t)
	rg := "stage5-pec-rg"
	ensureRG(t, rg)
	planID := stage5EnsurePlan(t, rg, "s5-pec-plan", "S1", "Standard")
	siteID := stage5CreateSite(t, rg, "s5-pec-app", planID, "registry.example/sockerless-overlay/azf:test", nil)
	subnetID := stage5Subnet(t, rg, "s5-pec-vnet", "10.63.0.0/16", "pe-subnet", "10.63.1.0/24")

	// A private endpoint targeting the site opens the connection on the
	// site's own collection (auto-approved: same subscription).
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

	// The site owner rejects the connection.
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

	// The endpoint's own read observes the owner's decision — one object.
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

	// LRO delete removes the connection.
	delPoller, err := web.BeginDeletePrivateEndpointConnection(ctx, rg, "s5-pec-app", "s5-site-pe", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = web.GetPrivateEndpointConnection(ctx, rg, "s5-pec-app", "s5-site-pe", nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)

	// Slot catalog: the slot publishes its slot-scoped group.
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
