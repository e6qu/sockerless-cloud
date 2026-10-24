package azure_sdk_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A virtual network with two address prefixes carries a subnet in each: a
// machine in the second prefix's subnet boots at an address there, and both
// machines resolve each other's registered name through the private zone the
// network is linked to, from their own prefix. A subnet outside every prefix
// is refused.
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{virtualNetworkName}
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{virtualNetworkName}/subnets/{subnetName}
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/virtualMachines/{vmName}
func TestCompute_VirtualNetworkWithTwoAddressPrefixesCarriesBothSubnets(t *testing.T) {
	t.Parallel()
	const rg, vnetName, zone = "vm-two-prefixes-rg", "vm-two-prefixes-vnet", "two-prefixes.internal"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.114.0.0/16"), to.Ptr("10.115.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	vnet, err := vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnet := func(name, prefix string) string {
		poller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, name, armnetwork.Subnet{
			Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr(prefix)},
		}, nil)
		require.NoError(t, err)
		resp, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return *resp.ID
	}
	firstSubnet := subnet("first", "10.114.1.0/24")
	secondSubnet := subnet("second", "10.115.1.0/24")

	_, err = subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, "outside", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.109.1.0/24")},
	}, nil)
	require.Error(t, err, "a subnet outside every address prefix is refused")
	assert.Contains(t, err.Error(), "10.109.1.0/24")

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nic := func(name, subnetID string) armnetwork.Interface {
		poller, err := nicClient.BeginCreateOrUpdate(ctx, rg, name, armnetwork.Interface{
			Location: to.Ptr("eastus"),
			Properties: &armnetwork.InterfacePropertiesFormat{
				IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
					Name: to.Ptr("ipconfig1"),
					Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
						Subnet:                    &armnetwork.Subnet{ID: to.Ptr(subnetID)},
						PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
						PrivateIPAddressVersion:   to.Ptr(armnetwork.IPVersionIPv4),
						Primary:                   to.Ptr(true),
					},
				}},
			},
		}, nil)
		require.NoError(t, err)
		resp, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return resp.Interface
	}
	serverNIC := nic("vm-two-prefixes-web-nic", firstSubnet)
	clientNIC := nic("vm-two-prefixes-probe-nic", secondSubnet)
	serverIP := derefString(serverNIC.Properties.IPConfigurations[0].Properties.PrivateIPAddress)
	clientIP := derefString(clientNIC.Properties.IPConfigurations[0].Properties.PrivateIPAddress)
	assert.True(t, strings.HasPrefix(serverIP, "10.114.1."), "the first subnet's interface takes its address there: %s", serverIP)
	assert.True(t, strings.HasPrefix(clientIP, "10.115.1."), "the second subnet's interface takes its address there: %s", clientIP)

	zonesClient, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zonePoller, err := zonesClient.BeginCreateOrUpdate(ctx, rg, zone, armprivatedns.PrivateZone{Location: to.Ptr("global")}, nil)
	require.NoError(t, err)
	_, err = zonePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	linksClient, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	linkPoller, err := linksClient.BeginCreateOrUpdate(ctx, rg, zone, "two-prefixes-link", armprivatedns.VirtualNetworkLink{
		Location: to.Ptr("global"),
		Properties: &armprivatedns.VirtualNetworkLinkProperties{
			VirtualNetwork:      &armprivatedns.SubResource{ID: vnet.ID},
			RegistrationEnabled: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = linkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	createOperationsVM(t, rg, "vm-two-prefixes-web", derefString(serverNIC.ID), func(vm *armcompute.VirtualMachine) {
		vm.Properties.OSProfile.CustomData = to.Ptr(base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nhostname -I > /var/tmp/addresses\n")))
	})
	createOperationsVM(t, rg, "vm-two-prefixes-probe", derefString(clientNIC.ID), nil)

	held := runInGuest(t, rg, "vm-two-prefixes-web", "addresses", "cat /var/tmp/addresses")
	assert.Contains(t, held, serverIP, "the first prefix's machine holds its interface's address")
	held = runInGuest(t, rg, "vm-two-prefixes-probe", "addresses", "hostname -I")
	assert.Contains(t, held, clientIP, "the second prefix's machine holds its interface's address")

	resolved := runInGuest(t, rg, "vm-two-prefixes-probe", "resolve", "getent hosts vm-two-prefixes-web."+zone)
	assert.Contains(t, resolved, serverIP+" ", "the second prefix's machine resolves the zone the network is linked to")
	resolved = runInGuest(t, rg, "vm-two-prefixes-web", "resolve", "getent hosts vm-two-prefixes-probe."+zone)
	assert.Contains(t, resolved, clientIP+" ", "the first prefix's machine resolves the second prefix's registered name")
}
