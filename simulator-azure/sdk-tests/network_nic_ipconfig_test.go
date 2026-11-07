package azure_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nicIPConfig(name, subnetID, static string) *armnetwork.InterfaceIPConfiguration {
	props := &armnetwork.InterfaceIPConfigurationPropertiesFormat{
		Subnet:                    &armnetwork.Subnet{ID: to.Ptr(subnetID)},
		PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
	}
	if static != "" {
		props.PrivateIPAllocationMethod = to.Ptr(armnetwork.IPAllocationMethodStatic)
		props.PrivateIPAddress = to.Ptr(static)
	}
	return &armnetwork.InterfaceIPConfiguration{Name: to.Ptr(name), Properties: props}
}

func nicAddresses(nic armnetwork.Interface) map[string]string {
	out := map[string]string{}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		out[*ipcfg.Name] = *ipcfg.Properties.PrivateIPAddress
	}
	return out
}

// TestNetwork_InterfaceSecondaryIPConfigurations: each secondary IP
// configuration holds its own subnet address, an update keeps the kept
// configurations' addresses and returns the dropped ones, and the subnet
// refuses an address another interface holds.
func TestNetwork_InterfaceSecondaryIPConfigurations(t *testing.T) {
	t.Parallel()
	rg, vnet, nicName, otherName := uniquename.New("ipcfg-rg"), uniquename.New("ipcfg-vnet"), uniquename.New("ipcfg-nic"), uniquename.New("ipcfg-other")
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnet, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.71.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if poller, err := vnetClient.BeginDelete(ctx, rg, vnet, nil); err == nil {
			_, _ = poller.PollUntilDone(ctx, nil)
		}
	})

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetID := func(name, prefix string) string {
		poller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnet, name, armnetwork.Subnet{
			Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr(prefix)},
		}, nil)
		require.NoError(t, err)
		resp, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return *resp.ID
	}
	subnet, otherSubnet := subnetID("ipcfg-subnet", "10.71.1.0/24"), subnetID("ipcfg-subnet-2", "10.71.2.0/24")

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	put := func(name string, ipcfgs ...*armnetwork.InterfaceIPConfiguration) (armnetwork.Interface, error) {
		poller, err := nicClient.BeginCreateOrUpdate(ctx, rg, name, armnetwork.Interface{
			Location:   to.Ptr("eastus"),
			Properties: &armnetwork.InterfacePropertiesFormat{IPConfigurations: ipcfgs},
		}, nil)
		if err != nil {
			return armnetwork.Interface{}, err
		}
		resp, err := poller.PollUntilDone(ctx, nil)
		return resp.Interface, err
	}
	t.Cleanup(func() {
		for _, name := range []string{nicName, otherName} {
			if poller, err := nicClient.BeginDelete(ctx, rg, name, nil); err == nil {
				_, _ = poller.PollUntilDone(ctx, nil)
			}
		}
	})

	primary := nicIPConfig("ipconfig1", subnet, "")
	primary.Properties.Primary = to.Ptr(true)
	nic, err := put(nicName, primary, nicIPConfig("ipconfig2", subnet, ""), nicIPConfig("ipconfig3", subnet, "10.71.1.20"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ipconfig1": "10.71.1.4", "ipconfig2": "10.71.1.5", "ipconfig3": "10.71.1.20"}, nicAddresses(nic),
		"every IP configuration holds an address of its own, the dynamic ones from the first Azure leaves to tenants")
	for _, ipcfg := range nic.Properties.IPConfigurations {
		assert.Equal(t, *ipcfg.Name == "ipconfig1", ipcfg.Properties.Primary != nil && *ipcfg.Properties.Primary,
			"only %s is primary", "ipconfig1")
	}

	read, err := nicClient.Get(ctx, rg, nicName, nil)
	require.NoError(t, err)
	assert.Equal(t, nicAddresses(nic), nicAddresses(read.Interface))

	// The virtual network's usages count each subnet's held addresses against
	// the 251 a /24 leaves to tenants.
	usages := map[string][2]float64{}
	usagePager := vnetClient.NewListUsagePager(rg, vnet, nil)
	for usagePager.More() {
		page, err := usagePager.NextPage(ctx)
		require.NoError(t, err)
		for _, u := range page.Value {
			usages[*u.ID] = [2]float64{*u.CurrentValue, *u.Limit}
			assert.Equal(t, "Count", *u.Unit)
		}
	}
	assert.Equal(t, map[string][2]float64{subnet: {3, 251}, otherSubnet: {0, 251}}, usages)

	taken, err := vnetClient.CheckIPAddressAvailability(ctx, rg, vnet, "10.71.1.4", nil)
	require.NoError(t, err)
	assert.False(t, *taken.Available)
	assert.Equal(t, []string{"10.71.1.6", "10.71.1.7", "10.71.1.8", "10.71.1.9", "10.71.1.10"}, stringValues(taken.AvailableIPAddresses))
	free, err := vnetClient.CheckIPAddressAvailability(ctx, rg, vnet, "10.71.1.30", nil)
	require.NoError(t, err)
	assert.True(t, *free.Available)
	gateway, err := vnetClient.CheckIPAddressAvailability(ctx, rg, vnet, "10.71.1.1", nil)
	require.NoError(t, err)
	assert.False(t, *gateway.Available)
	assert.True(t, *gateway.IsPlatformReserved, "the subnet's gateway address is the platform's")

	// Dropping ipconfig2 frees its address before the new dynamic configuration
	// takes the lowest free one.
	nic, err = put(nicName, primary, nicIPConfig("ipconfig3", subnet, "10.71.1.20"), nicIPConfig("ipconfig4", subnet, ""))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ipconfig1": "10.71.1.4", "ipconfig3": "10.71.1.20", "ipconfig4": "10.71.1.5"}, nicAddresses(nic))

	_, err = put(otherName, nicIPConfig("ipconfig1", subnet, ""), nicIPConfig("ipconfig2", subnet, "10.71.1.20"))
	var inUse *azcore.ResponseError
	require.True(t, errors.As(err, &inUse), "a secondary address another interface holds must be refused, got %v", err)
	assert.Equal(t, http.StatusBadRequest, inUse.StatusCode)
	assert.Equal(t, "PrivateIPAddressInUse", inUse.ErrorCode)

	_, err = put(otherName, nicIPConfig("ipconfig1", subnet, ""), nicIPConfig("ipconfig2", otherSubnet, ""))
	var split *azcore.ResponseError
	require.True(t, errors.As(err, &split), "IP configurations in two subnets must be refused, got %v", err)
	assert.Equal(t, http.StatusBadRequest, split.StatusCode)
	assert.Equal(t, "IpConfigurationsOnSameNicCannotUseDifferentSubnets", split.ErrorCode)

	// Deleting the interface returns every address it held.
	deletePoller, err := nicClient.BeginDelete(ctx, rg, nicName, nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	other, err := put(otherName, nicIPConfig("ipconfig1", subnet, "10.71.1.5"), nicIPConfig("ipconfig2", subnet, "10.71.1.20"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ipconfig1": "10.71.1.5", "ipconfig2": "10.71.1.20"}, nicAddresses(other))
}
