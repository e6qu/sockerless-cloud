package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetwork_PublicIPDdosAndCloudServiceReservation covers the public-IP
// operations beyond CRUD: the DDoS protection status (the sim does not model
// DDoS plans, so an IP is reported as not workload-protected) and the
// cloud-service reserve / disassociate operations.
func TestNetwork_PublicIPDdosAndCloudServiceReservation(t *testing.T) {
	rg := "pip-ops-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	pipClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := pipClient.BeginCreateOrUpdate(ctx, rg, "ops-pip", armnetwork.PublicIPAddress{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.PublicIPAddressSKU{Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard)},
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
		},
	}, nil)
	require.NoError(t, err)
	pipResp, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, pipResp.ID)

	ddosPoller, err := pipClient.BeginDdosProtectionStatus(ctx, rg, "ops-pip", nil)
	require.NoError(t, err)
	ddosResp, err := ddosPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, ddosResp.PublicIPAddressID)
	assert.Equal(t, *pipResp.ID, *ddosResp.PublicIPAddressID)
	require.NotNil(t, ddosResp.IsWorkloadProtected)
	assert.Equal(t, armnetwork.IsWorkloadProtectedFalse, *ddosResp.IsWorkloadProtected)

	reservePoller, err := pipClient.BeginReserveCloudServicePublicIPAddress(ctx, rg, "ops-pip", armnetwork.ReserveCloudServicePublicIPAddressRequest{}, nil)
	require.NoError(t, err)
	reserveResp, err := reservePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, reserveResp.ID)
	assert.Equal(t, *pipResp.ID, *reserveResp.ID)

	disPoller, err := pipClient.BeginDisassociateCloudServiceReservedPublicIP(ctx, rg, "ops-pip", armnetwork.DisassociateCloudServicePublicIPRequest{
		PublicIPArmID: pipResp.ID,
	}, nil)
	require.NoError(t, err)
	_, err = disPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestNetwork_LoadBalancerExtraOps covers the load-balancer operations beyond
// CRUD that do not require a real network host: the associated network-interface
// list (empty with no NICs), the VIP swap, the inbound-NAT port-mapping query,
// the per-rule health (zero backends → 0 up / 0 down) and the NIC→IP-based pool
// migration.
func TestNetwork_LoadBalancerExtraOps(t *testing.T) {
	rg := "lb-extra-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	pipClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	mkPip := func(name string) *string {
		poller, err := pipClient.BeginCreateOrUpdate(ctx, rg, name, armnetwork.PublicIPAddress{
			Location:   to.Ptr("eastus"),
			SKU:        &armnetwork.PublicIPAddressSKU{Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard)},
			Properties: &armnetwork.PublicIPAddressPropertiesFormat{PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic)},
		}, nil)
		require.NoError(t, err)
		resp, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return resp.ID
	}
	pip1 := mkPip("extra-pip1")
	pip2 := mkPip("extra-pip2")

	lbID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/loadBalancers/extra-lb"
	frontendID := lbID + "/frontendIPConfigurations/fe"
	poolID := lbID + "/backendAddressPools/pool"
	probeID := lbID + "/probes/probe"

	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	lbPoller, err := lbClient.BeginCreateOrUpdate(ctx, rg, "extra-lb", armnetwork.LoadBalancer{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.LoadBalancerSKU{Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard)},
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			FrontendIPConfigurations: []*armnetwork.FrontendIPConfiguration{{
				Name:       to.Ptr("fe"),
				Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{PublicIPAddress: &armnetwork.PublicIPAddress{ID: pip1}},
			}},
			BackendAddressPools: []*armnetwork.BackendAddressPool{{Name: to.Ptr("pool")}},
			Probes: []*armnetwork.Probe{{
				Name:       to.Ptr("probe"),
				Properties: &armnetwork.ProbePropertiesFormat{Protocol: to.Ptr(armnetwork.ProbeProtocolTCP), Port: to.Ptr[int32](80)},
			}},
			LoadBalancingRules: []*armnetwork.LoadBalancingRule{{
				Name: to.Ptr("rule"),
				Properties: &armnetwork.LoadBalancingRulePropertiesFormat{
					Protocol:                to.Ptr(armnetwork.TransportProtocolTCP),
					FrontendPort:            to.Ptr[int32](80),
					BackendPort:             to.Ptr[int32](8080),
					FrontendIPConfiguration: &armnetwork.SubResource{ID: to.Ptr(frontendID)},
					BackendAddressPool:      &armnetwork.SubResource{ID: to.Ptr(poolID)},
					Probe:                   &armnetwork.SubResource{ID: to.Ptr(probeID)},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = lbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// LoadBalancerNetworkInterfaces_List — no NICs reference the pool.
	nicListClient, err := armnetwork.NewLoadBalancerNetworkInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicPager := nicListClient.NewListPager(rg, "extra-lb", nil)
	require.True(t, nicPager.More())
	nicPage, err := nicPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, nicPage.Value)

	// LoadBalancers_SwapPublicIpAddresses — reassign the frontend's public IP.
	swapPoller, err := lbClient.BeginSwapPublicIPAddresses(ctx, "eastus", armnetwork.LoadBalancerVipSwapRequest{
		FrontendIPConfigurations: []*armnetwork.LoadBalancerVipSwapRequestFrontendIPConfiguration{{
			ID: to.Ptr(frontendID),
			Properties: &armnetwork.LoadBalancerVipSwapRequestFrontendIPConfigurationProperties{
				PublicIPAddress: &armnetwork.SubResource{ID: pip2},
			},
		}},
	}, nil)
	require.NoError(t, err)
	_, err = swapPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	gotLB, err := lbClient.Get(ctx, rg, "extra-lb", nil)
	require.NoError(t, err)
	require.NotNil(t, gotLB.Properties)
	require.Len(t, gotLB.Properties.FrontendIPConfigurations, 1)
	require.NotNil(t, gotLB.Properties.FrontendIPConfigurations[0].Properties.PublicIPAddress)
	assert.Equal(t, *pip2, *gotLB.Properties.FrontendIPConfigurations[0].Properties.PublicIPAddress.ID)

	// LoadBalancers_ListInboundNatRulePortMappings — no inbound NAT rules.
	mapPoller, err := lbClient.BeginListInboundNatRulePortMappings(ctx, rg, "extra-lb", "pool", armnetwork.QueryInboundNatRulePortMappingRequest{
		IPAddress: to.Ptr("10.0.0.9"),
	}, nil)
	require.NoError(t, err)
	mapResp, err := mapPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, mapResp.InboundNatRulePortMappings)

	// LoadBalancerLoadBalancingRules_Health — zero backends → 0 up / 0 down.
	ruleClient, err := armnetwork.NewLoadBalancerLoadBalancingRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	healthPoller, err := ruleClient.BeginHealth(ctx, rg, "extra-lb", "rule", nil)
	require.NoError(t, err)
	healthResp, err := healthPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, healthResp.Up)
	require.NotNil(t, healthResp.Down)
	assert.Equal(t, int32(0), *healthResp.Up)
	assert.Equal(t, int32(0), *healthResp.Down)

	// LoadBalancers_MigrateToIpBased — the named pool exists, so it migrates.
	migResp, err := lbClient.MigrateToIPBased(ctx, rg, "extra-lb", &armnetwork.LoadBalancersClientMigrateToIPBasedOptions{
		Parameters: &armnetwork.MigrateLoadBalancerToIPBasedRequest{Pools: []*string{to.Ptr("pool")}},
	})
	require.NoError(t, err)
	require.Len(t, migResp.MigratedPools.MigratedPools, 1)
	assert.Equal(t, "pool", *migResp.MigratedPools.MigratedPools[0])
}

// TestNetwork_SubnetLinksAndVnetDdos covers the subnet resource/service
// association link reads (empty for a subnet with no delegated services) and
// the virtual-network DDoS protection status (empty with no attached public
// IPs). Neither requires a real network host.
func TestNetwork_SubnetLinksAndVnetDdos(t *testing.T) {
	rg := "links-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	navClient, err := armnetwork.NewResourceNavigationLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	navResp, err := navClient.List(ctx, rg, "links-vnet", "links-subnet", nil)
	require.NoError(t, err)
	assert.Empty(t, navResp.Value)

	svcClient, err := armnetwork.NewServiceAssociationLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	svcResp, err := svcClient.List(ctx, rg, "links-vnet", "links-subnet", nil)
	require.NoError(t, err)
	assert.Empty(t, svcResp.Value)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	ddosPoller, err := vnetClient.BeginListDdosProtectionStatus(ctx, rg, "links-vnet", nil)
	require.NoError(t, err)
	ddosPager, err := ddosPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, ddosPager)
}

// TestNetwork_InterfaceLoadBalancerAssociations exercises the bidirectional
// load-balancer ⇄ network-interface association lists with a real network
// interface whose IP configuration joins the load balancer's backend pool.
func TestNetwork_InterfaceLoadBalancerAssociations(t *testing.T) {
	requireNetworkHost(t)
	rg := "assoc-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, "assoc-vnet", armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.70.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, "assoc-vnet", "assoc-subnet", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.70.1.0/24")},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	lbID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/loadBalancers/assoc-lb"
	poolID := lbID + "/backendAddressPools/assoc-pool"
	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	lbPoller, err := lbClient.BeginCreateOrUpdate(ctx, rg, "assoc-lb", armnetwork.LoadBalancer{
		Location: to.Ptr("eastus"),
		SKU:      &armnetwork.LoadBalancerSKU{Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard)},
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			BackendAddressPools: []*armnetwork.BackendAddressPool{{Name: to.Ptr("assoc-pool")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = lbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "assoc-nic", armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet:                          &armnetwork.Subnet{ID: subnetResp.ID},
					LoadBalancerBackendAddressPools: []*armnetwork.BackendAddressPool{{ID: to.Ptr(poolID)}},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// NetworkInterfaceLoadBalancers_List — the LB whose pool the NIC joined.
	nicLBClient, err := armnetwork.NewInterfaceLoadBalancersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicLBPager := nicLBClient.NewListPager(rg, "assoc-nic", nil)
	require.True(t, nicLBPager.More())
	nicLBPage, err := nicLBPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, nicLBPage.Value, 1)
	assert.Equal(t, "assoc-lb", *nicLBPage.Value[0].Name)

	// LoadBalancerNetworkInterfaces_List — the NIC referencing the LB's pool.
	lbNICClient, err := armnetwork.NewLoadBalancerNetworkInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	lbNICPager := lbNICClient.NewListPager(rg, "assoc-lb", nil)
	require.True(t, lbNICPager.More())
	lbNICPage, err := lbNICPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, lbNICPage.Value, 1)
	assert.Equal(t, "assoc-nic", *lbNICPage.Value[0].Name)
}
