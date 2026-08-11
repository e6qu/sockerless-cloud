package azure_sdk_test

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A packet capture's whole content is the traffic it records off a machine's
// interface, so this test proves the composition rather than the pieces: a real
// virtual machine boots, a capture opens a socket on the interface carrying its
// traffic, the capture stops, and the recorded frames are downloaded back out
// of the storage account through the Blob data plane a client really reads.
//
// The engine and the operations are each covered on their own — capture against
// live traffic in realexec, and the operation surface below — but
// nothing except this asserts that a capture started through Network Watcher
// against an Azure virtual machine produces a file a client can fetch.

func TestNetworkWatcher_PacketCaptureRecordsToStorage(t *testing.T) {
	requireNetworkHost(t)
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

	createPoller, err := captureClient.BeginCreate(ctx, rg, watcher, capture, armnetwork.PacketCapture{
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
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, vmID, *created.Properties.Target)
	assert.Equal(t, armnetwork.ProvisioningStateSucceeded, *created.Properties.ProvisioningState)

	// The session is really running: status says so before anything stops it.
	statusPoller, err := captureClient.BeginGetStatus(ctx, rg, watcher, capture, nil)
	require.NoError(t, err)
	status, err := statusPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, status.PacketCaptureStatus)
	assert.Equal(t, armnetwork.PcStatusRunning, *status.PacketCaptureStatus,
		"a capture that has not stopped reports Running")
	assert.NotEmpty(t, status.CaptureStartTime, "a running capture reports when it started")

	// Read it back as a resource and find it in the watcher's list.
	got, err := captureClient.Get(ctx, rg, watcher, capture, nil)
	require.NoError(t, err)
	assert.Equal(t, capture, *got.Name)

	pager := captureClient.NewListPager(rg, watcher, nil)
	var listed []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			listed = append(listed, *c.Name)
		}
	}
	assert.Contains(t, listed, capture)

	// Stop finalises the recording into the storage account it named.
	stopPoller, err := captureClient.BeginStop(ctx, rg, watcher, capture, nil)
	require.NoError(t, err)
	_, err = stopPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	statusPoller, err = captureClient.BeginGetStatus(ctx, rg, watcher, capture, nil)
	require.NoError(t, err)
	stopped, err := statusPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, stopped.PacketCaptureStatus)
	assert.Equal(t, armnetwork.PcStatusStopped, *stopped.PacketCaptureStatus)
	require.NotNil(t, stopped.StopReason)
	assert.Equal(t, "Requested", *stopped.StopReason)

	// The artifact is downloadable through the Blob data plane, and it is a
	// real capture file — the libpcap magic is what every reader looks for.
	blobClient := newBlobTestClient(t, account)
	downloaded, err := blobClient.DownloadStream(ctx, "captures", capture+".cap", nil)
	require.NoError(t, err, "the capture must be readable from the storage account it named")
	body, err := io.ReadAll(downloaded.Body)
	require.NoError(t, err)
	require.NoError(t, downloaded.Body.Close())
	require.GreaterOrEqual(t, len(body), 24, "a capture file carries at least its 24-byte header")
	assert.Equal(t, []byte{0xd4, 0xc3, 0xb2, 0xa1}, body[:4],
		"the downloaded bytes must be a libpcap capture, not an opaque blob")

	// Delete removes the capture.
	deletePoller, err := captureClient.BeginDelete(ctx, rg, watcher, capture, nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = captureClient.Get(ctx, rg, watcher, capture, nil)
	require.Error(t, err, "a deleted capture is gone")
}

// TestNetworkWatcher_PacketCaptureRefusesATargetItCannotRecord pins the
// behaviour that keeps a capture honest: a target with no interface to read
// frames from is an error, never an empty capture file. A caller cannot
// distinguish a fabricated empty capture from a real one that saw no traffic,
// so the simulator must not produce one.
func TestNetworkWatcher_PacketCaptureRefusesATargetItCannotRecord(t *testing.T) {
	requireNetworkHost(t)
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

// packetCaptureFixtureVM builds the network and the running virtual machine a
// capture reads frames from, and returns the machine's resource id.
func packetCaptureFixtureVM(t *testing.T, rg, vnetName, subnetName, nicName, vmName string) string {
	t.Helper()

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.91.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, subnetName, armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.91.1.0/24")},
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
					Offer:     to.Ptr("0001-com-ubuntu-server-jammy"),
					SKU:       to.Ptr("22_04-lts"),
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

	// A capture reads frames off a running machine's interface, so give the
	// interface a moment to come up before one is started against it.
	time.Sleep(500 * time.Millisecond)
	return *vm.ID
}
