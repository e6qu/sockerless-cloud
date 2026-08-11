package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNetwork_CreateVirtualNetwork(t *testing.T) {
	requireNetworkHost(t)
	// Create resource group first
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "net-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, "net-rg", "test-vnet", armnetwork.VirtualNetwork{
		Location: ptrStr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{ptrStr("10.0.0.0/16")},
			},
		},
	}, nil)
	require.NoError(t, err)
	resp, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-vnet", *resp.Name)
}

func TestNetwork_CreateSubnet(t *testing.T) {
	requireNetworkHost(t)
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "subnet-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, "subnet-rg", "subnet-vnet", armnetwork.VirtualNetwork{
		Location: ptrStr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{ptrStr("10.1.0.0/16")},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, "subnet-rg", "subnet-vnet", "test-subnet", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefixes: []*string{ptrStr("10.1.1.0/24")},
		},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-subnet", *subnetResp.Name)
	require.NotNil(t, subnetResp.Properties)
	require.Len(t, subnetResp.Properties.AddressPrefixes, 1)
	assert.Equal(t, "10.1.1.0/24", *subnetResp.Properties.AddressPrefixes[0])
}

func TestNetwork_CreateNSG(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "nsg-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armnetwork.NewSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, "nsg-rg", "test-nsg", armnetwork.SecurityGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	resp, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-nsg", *resp.Name)
}

// TestNetwork_NSGSecurityRulesCRUD covers the `securityRules` sub-
// resource endpoints. Creates an NSG, then creates/gets/lists/deletes
// a security rule via the per-rule client, and confirms the rule is
// reflected in the parent NSG's properties.
func TestNetwork_NSGSecurityRulesCRUD(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "nsg-rules-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	nsgClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := nsgClient.BeginCreateOrUpdate(ctx, "nsg-rules-rg", "rules-nsg", armnetwork.SecurityGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	rulesClient, err := armnetwork.NewSecurityRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Create rule
	rulePoller, err := rulesClient.BeginCreateOrUpdate(ctx, "nsg-rules-rg", "rules-nsg", "allow-http",
		armnetwork.SecurityRule{
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Protocol:                 ptrProto(armnetwork.SecurityRuleProtocolTCP),
				SourcePortRange:          ptrStr("*"),
				DestinationPortRange:     ptrStr("80"),
				SourceAddressPrefix:      ptrStr("*"),
				DestinationAddressPrefix: ptrStr("*"),
				Access:                   ptrAccess(armnetwork.SecurityRuleAccessAllow),
				Priority:                 ptrInt32(100),
				Direction:                ptrDir(armnetwork.SecurityRuleDirectionInbound),
			},
		}, nil)
	require.NoError(t, err)
	ruleResp, err := rulePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "allow-http", *ruleResp.Name)
	require.NotNil(t, ruleResp.Properties)
	assert.Equal(t, int32(100), *ruleResp.Properties.Priority)

	// Get rule
	getResp, err := rulesClient.Get(ctx, "nsg-rules-rg", "rules-nsg", "allow-http", nil)
	require.NoError(t, err)
	assert.Equal(t, "allow-http", *getResp.Name)

	// Parent NSG Get should now include the rule in its Properties.
	nsgResp, err := nsgClient.Get(ctx, "nsg-rules-rg", "rules-nsg", nil)
	require.NoError(t, err)
	require.NotNil(t, nsgResp.Properties)
	require.Len(t, nsgResp.Properties.SecurityRules, 1)
	assert.Equal(t, "allow-http", *nsgResp.Properties.SecurityRules[0].Name)

	// Delete rule
	delPoller, err := rulesClient.BeginDelete(ctx, "nsg-rules-rg", "rules-nsg", "allow-http", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Parent NSG should no longer have the rule.
	nsgResp, err = nsgClient.Get(ctx, "nsg-rules-rg", "rules-nsg", nil)
	require.NoError(t, err)
	assert.Empty(t, nsgResp.Properties.SecurityRules)
}

// TestNetwork_NSGRule_RejectsDuplicatePriority pins the priority+
// direction uniqueness constraint that real Azure enforces. Two rules
// in the same NSG can share a priority only when they differ in
// Direction; otherwise the second create returns
// SecurityRuleParameterPriorityAlreadyTaken (HTTP 400).
func TestNetwork_NSGRule_RejectsDuplicatePriority(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "nsg-prio-rg", armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	nsgClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := nsgClient.BeginCreateOrUpdate(ctx, "nsg-prio-rg", "prio-nsg", armnetwork.SecurityGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	rulesClient, err := armnetwork.NewSecurityRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	makeRule := func(priority int32, direction armnetwork.SecurityRuleDirection) armnetwork.SecurityRule {
		return armnetwork.SecurityRule{
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Protocol:                 ptrProto(armnetwork.SecurityRuleProtocolTCP),
				SourcePortRange:          ptrStr("*"),
				DestinationPortRange:     ptrStr("80"),
				SourceAddressPrefix:      ptrStr("*"),
				DestinationAddressPrefix: ptrStr("*"),
				Access:                   ptrAccess(armnetwork.SecurityRuleAccessAllow),
				Priority:                 ptrInt32(priority),
				Direction:                ptrDir(direction),
			},
		}
	}

	// First rule: priority 200, INGRESS — should succeed.
	p1, err := rulesClient.BeginCreateOrUpdate(ctx, "nsg-prio-rg", "prio-nsg", "first",
		makeRule(200, armnetwork.SecurityRuleDirectionInbound), nil)
	require.NoError(t, err)
	_, err = p1.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Second rule: same priority + direction — must fail.
	_, err = rulesClient.BeginCreateOrUpdate(ctx, "nsg-prio-rg", "prio-nsg", "duplicate",
		makeRule(200, armnetwork.SecurityRuleDirectionInbound), nil)
	require.Error(t, err, "duplicate Priority+Direction must fail like real Azure")

	// Third rule: same priority but EGRESS — should succeed (priority
	// space is per-direction, not global).
	p3, err := rulesClient.BeginCreateOrUpdate(ctx, "nsg-prio-rg", "prio-nsg", "egress-same-prio",
		makeRule(200, armnetwork.SecurityRuleDirectionOutbound), nil)
	require.NoError(t, err)
	_, err = p3.PollUntilDone(ctx, nil)
	require.NoError(t, err, "same priority across different directions must be accepted")
}

func TestNetwork_LoadBalancerLifecycle(t *testing.T) {
	rg := "lb-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	pipClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pipPoller, err := pipClient.BeginCreateOrUpdate(ctx, rg, "lb-pip", armnetwork.PublicIPAddress{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.PublicIPAddressSKU{Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard)},
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
			PublicIPAddressVersion:   to.Ptr(armnetwork.IPVersionIPv4),
		},
	}, nil)
	require.NoError(t, err)
	pipResp, err := pipPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, pipResp.ID)

	frontendName := "frontend"
	backendName := "backend"
	probeName := "tcp-probe"
	ruleName := "http-rule"
	lbID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/loadBalancers/sdk-lb"
	backendID := lbID + "/backendAddressPools/" + backendName
	frontendID := lbID + "/frontendIPConfigurations/" + frontendName
	probeID := lbID + "/probes/" + probeName

	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	lbPoller, err := lbClient.BeginCreateOrUpdate(ctx, rg, "sdk-lb", armnetwork.LoadBalancer{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.LoadBalancerSKU{Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard)},
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			FrontendIPConfigurations: []*armnetwork.FrontendIPConfiguration{{
				Name: to.Ptr(frontendName),
				Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
					PublicIPAddress: &armnetwork.PublicIPAddress{ID: pipResp.ID},
				},
			}},
			BackendAddressPools: []*armnetwork.BackendAddressPool{{Name: to.Ptr(backendName)}},
			Probes: []*armnetwork.Probe{{
				Name: to.Ptr(probeName),
				Properties: &armnetwork.ProbePropertiesFormat{
					Protocol: to.Ptr(armnetwork.ProbeProtocolTCP),
					Port:     to.Ptr[int32](80),
				},
			}},
			LoadBalancingRules: []*armnetwork.LoadBalancingRule{{
				Name: to.Ptr(ruleName),
				Properties: &armnetwork.LoadBalancingRulePropertiesFormat{
					Protocol:                to.Ptr(armnetwork.TransportProtocolTCP),
					FrontendPort:            to.Ptr[int32](80),
					BackendPort:             to.Ptr[int32](80),
					FrontendIPConfiguration: &armnetwork.SubResource{ID: to.Ptr(frontendID)},
					BackendAddressPool:      &armnetwork.SubResource{ID: to.Ptr(backendID)},
					Probe:                   &armnetwork.SubResource{ID: to.Ptr(probeID)},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	lbResp, err := lbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, lbResp.Properties)
	require.Len(t, lbResp.Properties.FrontendIPConfigurations, 1)
	require.Len(t, lbResp.Properties.BackendAddressPools, 1)
	require.Len(t, lbResp.Properties.Probes, 1)
	require.Len(t, lbResp.Properties.LoadBalancingRules, 1)

	got, err := lbClient.Get(ctx, rg, "sdk-lb", nil)
	require.NoError(t, err)
	assert.Equal(t, "sdk-lb", *got.Name)
	require.NotNil(t, got.Properties)
	assert.Equal(t, backendName, *got.Properties.BackendAddressPools[0].Name)

	delPoller, err := lbClient.BeginDelete(ctx, rg, "sdk-lb", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestNetwork_PublicIPPrefixNatGatewaySubnetAssociation(t *testing.T) {
	requireNetworkHost(t)
	rg := "nat-rg"
	location := "eastus"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr(location)}, nil)
	require.NoError(t, err)

	prefixClient, err := armnetwork.NewPublicIPPrefixesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	prefixPoller, err := prefixClient.BeginCreateOrUpdate(ctx, rg, "sdk-prefix", armnetwork.PublicIPPrefix{
		Location: to.Ptr(location),
		SKU:      &armnetwork.PublicIPPrefixSKU{Name: to.Ptr(armnetwork.PublicIPPrefixSKUNameStandard)},
		Properties: &armnetwork.PublicIPPrefixPropertiesFormat{
			PrefixLength:           to.Ptr[int32](28),
			PublicIPAddressVersion: to.Ptr(armnetwork.IPVersionIPv4),
		},
	}, nil)
	require.NoError(t, err)
	prefixResp, err := prefixPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, prefixResp.ID)
	require.NotNil(t, prefixResp.Properties)
	require.NotEmpty(t, *prefixResp.Properties.IPPrefix)

	natClient, err := armnetwork.NewNatGatewaysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	natPoller, err := natClient.BeginCreateOrUpdate(ctx, rg, "sdk-nat", armnetwork.NatGateway{
		Location: to.Ptr(location),
		SKU:      &armnetwork.NatGatewaySKU{Name: to.Ptr(armnetwork.NatGatewaySKUNameStandard)},
		Properties: &armnetwork.NatGatewayPropertiesFormat{
			IdleTimeoutInMinutes: to.Ptr[int32](10),
			PublicIPPrefixes:     []*armnetwork.SubResource{{ID: prefixResp.ID}},
		},
	}, nil)
	require.NoError(t, err)
	natResp, err := natPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, natResp.ID)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, "sdk-nat-vnet", armnetwork.VirtualNetwork{
		Location: to.Ptr(location),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.91.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, "sdk-nat-vnet", "sdk-nat-subnet", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix: to.Ptr("10.91.1.0/24"),
			NatGateway:    &armnetwork.SubResource{ID: natResp.ID},
		},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, subnetResp.Properties)
	require.NotNil(t, subnetResp.Properties.NatGateway)
	assert.Equal(t, *natResp.ID, *subnetResp.Properties.NatGateway.ID)

	gotNat, err := natClient.Get(ctx, rg, "sdk-nat", nil)
	require.NoError(t, err)
	require.NotNil(t, gotNat.Properties)
	require.Len(t, gotNat.Properties.Subnets, 1)
	assert.Equal(t, *subnetResp.ID, *gotNat.Properties.Subnets[0].ID)

	pager := natClient.NewListPager(rg, nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
}

// TestNetwork_VirtualNetworkPeering exercises the virtualNetworkPeerings
// sub-resource: it peers two virtual networks and confirms the peering's
// remoteVirtualNetwork cross-reference reads back as the remote VNet's ARM
// ID, then lists and deletes the peering.
func TestNetwork_VirtualNetworkPeering(t *testing.T) {
	rg := "peering-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	mkVnet := func(name, prefix string) *string {
		poller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, name, armnetwork.VirtualNetwork{
			Location: ptrStr("eastus"),
			Properties: &armnetwork.VirtualNetworkPropertiesFormat{
				AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{ptrStr(prefix)}},
			},
		}, nil)
		require.NoError(t, err)
		resp, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, resp.ID)
		return resp.ID
	}
	_ = mkVnet("peer-local", "10.20.0.0/16")
	remoteID := mkVnet("peer-remote", "10.21.0.0/16")

	peerClient, err := armnetwork.NewVirtualNetworkPeeringsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	peerPoller, err := peerClient.BeginCreateOrUpdate(ctx, rg, "peer-local", "to-remote", armnetwork.VirtualNetworkPeering{
		Properties: &armnetwork.VirtualNetworkPeeringPropertiesFormat{
			AllowVirtualNetworkAccess: to.Ptr(true),
			AllowForwardedTraffic:     to.Ptr(true),
			RemoteVirtualNetwork:      &armnetwork.SubResource{ID: remoteID},
		},
	}, nil)
	require.NoError(t, err)
	peerResp, err := peerPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "to-remote", *peerResp.Name)

	got, err := peerClient.Get(ctx, rg, "peer-local", "to-remote", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.RemoteVirtualNetwork)
	assert.Equal(t, *remoteID, *got.Properties.RemoteVirtualNetwork.ID)
	assert.True(t, *got.Properties.AllowVirtualNetworkAccess)

	pager := peerClient.NewListPager(rg, "peer-local", nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Value, 1)

	delPoller, err := peerClient.BeginDelete(ctx, rg, "peer-local", "to-remote", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestNetwork_RouteTableAndRoutes covers the route table UpdateTags (PATCH)
// path and the routes sub-resource CRUD, confirming a route's next-hop
// configuration reads back through both the per-route client and the parent
// route table.
func TestNetwork_RouteTableAndRoutes(t *testing.T) {
	rg := "rt-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	rtClient, err := armnetwork.NewRouteTablesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rtPoller, err := rtClient.BeginCreateOrUpdate(ctx, rg, "sdk-rt", armnetwork.RouteTable{
		Location:   ptrStr("eastus"),
		Properties: &armnetwork.RouteTablePropertiesFormat{},
	}, nil)
	require.NoError(t, err)
	_, err = rtPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	tagsResp, err := rtClient.UpdateTags(ctx, rg, "sdk-rt", armnetwork.TagsObject{
		Tags: map[string]*string{"env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, tagsResp.Tags["env"])
	assert.Equal(t, "test", *tagsResp.Tags["env"])

	routesClient, err := armnetwork.NewRoutesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	routePoller, err := routesClient.BeginCreateOrUpdate(ctx, rg, "sdk-rt", "to-appliance", armnetwork.Route{
		Properties: &armnetwork.RoutePropertiesFormat{
			AddressPrefix:    ptrStr("10.40.0.0/16"),
			NextHopType:      to.Ptr(armnetwork.RouteNextHopTypeVirtualAppliance),
			NextHopIPAddress: ptrStr("10.0.0.4"),
		},
	}, nil)
	require.NoError(t, err)
	routeResp, err := routePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "to-appliance", *routeResp.Name)
	require.NotNil(t, routeResp.Properties)
	assert.Equal(t, armnetwork.RouteNextHopTypeVirtualAppliance, *routeResp.Properties.NextHopType)

	gotRoute, err := routesClient.Get(ctx, rg, "sdk-rt", "to-appliance", nil)
	require.NoError(t, err)
	require.NotNil(t, gotRoute.Properties)
	assert.Equal(t, "10.40.0.0/16", *gotRoute.Properties.AddressPrefix)
	assert.Equal(t, "10.0.0.4", *gotRoute.Properties.NextHopIPAddress)

	// Parent route table now reflects the route.
	gotRT, err := rtClient.Get(ctx, rg, "sdk-rt", nil)
	require.NoError(t, err)
	require.NotNil(t, gotRT.Properties)
	require.Len(t, gotRT.Properties.Routes, 1)
	assert.Equal(t, "to-appliance", *gotRT.Properties.Routes[0].Name)

	routePager := routesClient.NewListPager(rg, "sdk-rt", nil)
	require.True(t, routePager.More())
	routePage, err := routePager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, routePage.Value, 1)

	delRoute, err := routesClient.BeginDelete(ctx, rg, "sdk-rt", "to-appliance", nil)
	require.NoError(t, err)
	_, err = delRoute.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	gotRT, err = rtClient.Get(ctx, rg, "sdk-rt", nil)
	require.NoError(t, err)
	assert.Empty(t, gotRT.Properties.Routes)
}

// TestNetwork_NatGatewayUpdateTags covers the NAT gateway UpdateTags (PATCH)
// path on a gateway with no attached subnets or public IPs (so it needs no
// real-network host).
func TestNetwork_NatGatewayUpdateTags(t *testing.T) {
	rg := "nat-tags-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	natClient, err := armnetwork.NewNatGatewaysClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	natPoller, err := natClient.BeginCreateOrUpdate(ctx, rg, "tag-nat", armnetwork.NatGateway{
		Location: ptrStr("eastus"),
		SKU:      &armnetwork.NatGatewaySKU{Name: to.Ptr(armnetwork.NatGatewaySKUNameStandard)},
		Properties: &armnetwork.NatGatewayPropertiesFormat{
			IdleTimeoutInMinutes: to.Ptr[int32](5),
		},
	}, nil)
	require.NoError(t, err)
	_, err = natPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	tagsResp, err := natClient.UpdateTags(ctx, rg, "tag-nat", armnetwork.TagsObject{
		Tags: map[string]*string{"team": to.Ptr("net")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, tagsResp.Tags["team"])
	assert.Equal(t, "net", *tagsResp.Tags["team"])
}

// TestNetwork_LoadBalancerBackendPoolClient exercises the standalone
// backendAddressPools client (CreateOrUpdate / Get / List) against an
// existing load balancer.
func TestNetwork_LoadBalancerBackendPoolClient(t *testing.T) {
	rg := "lb-pool-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	lbPoller, err := lbClient.BeginCreateOrUpdate(ctx, rg, "pool-lb", armnetwork.LoadBalancer{
		Location:   to.Ptr("eastus"),
		SKU:        &armnetwork.LoadBalancerSKU{Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard)},
		Properties: &armnetwork.LoadBalancerPropertiesFormat{},
	}, nil)
	require.NoError(t, err)
	_, err = lbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	poolClient, err := armnetwork.NewLoadBalancerBackendAddressPoolsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poolPoller, err := poolClient.BeginCreateOrUpdate(ctx, rg, "pool-lb", "pool-a", armnetwork.BackendAddressPool{
		Name: to.Ptr("pool-a"),
	}, nil)
	require.NoError(t, err)
	poolResp, err := poolPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "pool-a", *poolResp.Name)

	gotPool, err := poolClient.Get(ctx, rg, "pool-lb", "pool-a", nil)
	require.NoError(t, err)
	assert.Equal(t, "pool-a", *gotPool.Name)

	pager := poolClient.NewListPager(rg, "pool-lb", nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
}

func ptrProto(p armnetwork.SecurityRuleProtocol) *armnetwork.SecurityRuleProtocol { return &p }
func ptrAccess(a armnetwork.SecurityRuleAccess) *armnetwork.SecurityRuleAccess    { return &a }
func ptrDir(d armnetwork.SecurityRuleDirection) *armnetwork.SecurityRuleDirection { return &d }
func ptrInt32(v int32) *int32                                                     { return &v }
