package azure_sdk_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The container backend running a machine exposes no tap to read frames from,
// so Network Watcher refuses to start a capture rather than produce an empty
// file a caller could not tell from a real one, and keeps nothing behind.
func TestNetworkWatcher_PacketCaptureRefusesAMachineWithoutATap(t *testing.T) {
	t.Parallel()
	const (
		rg         = "pcap-rg"
		vnetName   = "pcap-vnet"
		subnetName = "pcap-subnet"
		nicName    = "pcap-nic"
		vmName     = "pcap-vm"
		watcher    = "NetworkWatcher_eastus"
		account    = "pcapstorage"
		capture    = "pcap-session"
	)

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vmID := packetCaptureFixtureVM(t, rg, vnetName, subnetName, nicName, vmName)

	watcherClient, err := armnetwork.NewWatchersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = watcherClient.CreateOrUpdate(ctx, rg, watcher, armnetwork.Watcher{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.WatcherPropertiesFormat{},
	}, nil)
	require.NoError(t, err)

	storageID := fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s",
		subscriptionID, rg, account)

	captureClient, err := armnetwork.NewPacketCapturesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := captureClient.BeginCreate(ctx, rg, watcher, capture, armnetwork.PacketCapture{
		Properties: &armnetwork.PacketCaptureParameters{
			Target:                  to.Ptr(vmID),
			TargetType:              to.Ptr(armnetwork.PacketCaptureTargetTypeAzureVM),
			BytesToCapturePerPacket: to.Ptr[int64](0),
			TotalBytesPerSession:    to.Ptr[int64](1 << 20),
			TimeLimitInSeconds:      to.Ptr[int32](60),
			StorageLocation: &armnetwork.PacketCaptureStorageLocation{
				StorageID:   to.Ptr(storageID),
				StoragePath: to.Ptr(fmt.Sprintf("https://%s.blob.core.windows.net/captures/%s.cap", account, capture)),
			},
		},
	}, nil)
	if err == nil {
		_, err = poller.PollUntilDone(ctx, nil)
	}
	requireAzureErrorCode(t, err, "PacketCaptureTargetNotReady",
		"a machine the backend exposes no tap for cannot be captured")
	assert.Contains(t, err.Error(), "no interface a capture can tap",
		"the refusal names why the capture cannot run")

	// The refusal left no resource to read and nothing listed.
	_, err = captureClient.Get(ctx, rg, watcher, capture, nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "a refused capture is not a resource")

	pager := captureClient.NewListPager(rg, watcher, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			assert.NotEqual(t, capture, *c.Name, "a refused capture must not appear in the watcher's list")
		}
	}
}

// TestNetworkWatcher_PacketCaptureRefusesATargetItCannotRecord: a target with
// no interface to read frames from is an error, never an empty capture file.
func TestNetworkWatcher_PacketCaptureRefusesATargetItCannotRecord(t *testing.T) {
	t.Parallel()
	const rg = "pcap-missing-rg"
	const watcher = "NetworkWatcher_eastus"

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	watcherClient, err := armnetwork.NewWatchersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = watcherClient.CreateOrUpdate(ctx, rg, watcher, armnetwork.Watcher{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.WatcherPropertiesFormat{},
	}, nil)
	require.NoError(t, err)

	captureClient, err := armnetwork.NewPacketCapturesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	missing := fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/no-such-vm",
		subscriptionID, rg)
	poller, err := captureClient.BeginCreate(ctx, rg, watcher, "doomed", armnetwork.PacketCapture{
		Properties: &armnetwork.PacketCaptureParameters{
			Target: to.Ptr(missing),
			StorageLocation: &armnetwork.PacketCaptureStorageLocation{
				StorageID: to.Ptr(fmt.Sprintf(
					"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/nope",
					subscriptionID, rg)),
			},
		},
	}, nil)
	if err == nil {
		_, err = poller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "capturing a machine that does not exist must fail rather than record nothing")
	assert.True(t, strings.Contains(err.Error(), "was not found") ||
		strings.Contains(err.Error(), "no running interface"),
		"the error names why the capture cannot run, got: %v", err)
}

// packetCaptureFixtureVM builds the network and the running machine a capture
// reads frames from, and returns the machine's resource ID.
func packetCaptureFixtureVM(t *testing.T, rg, vnetName, subnetName, nicName, vmName string) string {
	t.Helper()

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.105.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, subnetName, armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.105.1.0/24")},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, nicName, armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet:                    &armnetwork.Subnet{ID: subnetResp.ID},
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
					Primary:                   to.Ptr(true),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	nicResp, err := nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vmPoller, err := vmClient.BeginCreateOrUpdate(ctx, rg, vmName, armcompute.VirtualMachine{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: to.Ptr(armcompute.VirtualMachineSizeTypesStandardB1S),
			},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{
					Publisher: to.Ptr("Canonical"),
					Offer:     to.Ptr("ubuntu-24_04-lts"),
					SKU:       to.Ptr("server"),
					Version:   to.Ptr("latest"),
				},
				OSDisk: &armcompute.OSDisk{
					Name:         to.Ptr(vmName + "-osdisk"),
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
					DiskSizeGB:   to.Ptr[int32](30),
				},
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{ID: nicResp.ID}},
			},
		},
	}, nil)
	require.NoError(t, err)
	vm, err := vmPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, vm.ID)
	return *vm.ID
}
