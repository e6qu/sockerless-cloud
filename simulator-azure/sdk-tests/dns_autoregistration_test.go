package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A virtual machine in a virtual network linked to a private zone with
// registration enabled is registered in the zone under its computer name, and
// the machine itself resolves that name through the resolver its network
// hands it.
func TestPrivateDNS_AutoRegistersAVirtualMachine(t *testing.T) {
	t.Parallel()
	const rg = "dns-reg-rg"
	const vmName = "dns-reg-vm"
	const zone = "reg.internal"
	nicID := vmOperationsFixture(t, rg, "dns-reg", "10.95")

	zonesClient, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zonePoller, err := zonesClient.BeginCreateOrUpdate(ctx, rg, zone, armprivatedns.PrivateZone{Location: to.Ptr("global")}, nil)
	require.NoError(t, err)
	_, err = zonePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	linksClient, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/virtualNetworks/dns-reg-vnet"
	linkPoller, err := linksClient.BeginCreateOrUpdate(ctx, rg, zone, "dns-reg-link", armprivatedns.VirtualNetworkLink{
		Location: to.Ptr("global"),
		Properties: &armprivatedns.VirtualNetworkLinkProperties{
			VirtualNetwork:      &armprivatedns.SubResource{ID: to.Ptr(vnetID)},
			RegistrationEnabled: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	_, err = linkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	createOperationsVM(t, rg, vmName, nicID, nil)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nic, err := nicClient.Get(ctx, rg, "dns-reg-nic", nil)
	require.NoError(t, err)
	require.NotEmpty(t, nic.Properties.IPConfigurations)
	address := derefString(nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress)
	require.NotEmpty(t, address, "the booted machine's interface has an address")

	recordSets, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	record, err := recordSets.Get(ctx, rg, zone, armprivatedns.RecordTypeA, vmName, nil)
	require.NoError(t, err, "the machine is registered under its computer name")
	require.NotNil(t, record.Properties)
	assert.True(t, *record.Properties.IsAutoRegistered)
	require.Len(t, record.Properties.ARecords, 1)
	assert.Equal(t, address, derefString(record.Properties.ARecords[0].IPv4Address))

	extClient, err := armcompute.NewVirtualMachineExtensionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	extPoller, err := extClient.BeginCreateOrUpdate(ctx, rg, vmName, "resolve", armcompute.VirtualMachineExtension{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineExtensionProperties{
			Publisher:          to.Ptr("Microsoft.Azure.Extensions"),
			Type:               to.Ptr("CustomScript"),
			TypeHandlerVersion: to.Ptr("2.1"),
			Settings:           map[string]any{"commandToExecute": "getent hosts " + vmName + "." + zone},
		},
	}, nil)
	require.NoError(t, err)
	ran, err := extPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err, "the guest resolves its own registered name")
	var output []string
	for _, status := range ran.Properties.InstanceView.Statuses {
		output = append(output, derefString(status.Message))
	}
	assert.Contains(t, strings.Join(output, "\n"), address+" ", "the guest's resolver did not answer the registered name")
}
