package azure_sdk_test

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"net/url"
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

// A capture records the frames crossing the machine's interface, through the
// filters it names, and Network Watcher moves the file to the storage account
// and to the machine's local path when the session stops.
func TestNetworkWatcher_PacketCaptureRecordsTheMachinesTraffic(t *testing.T) {
	t.Parallel()
	const (
		rg       = "pcap-rg"
		vmName   = "pcap-vm"
		watcher  = "NetworkWatcher_eastus"
		account  = "pcapcapturesdk"
		capture  = "pcap-session"
		timed    = "pcap-timed"
		filePath = "/var/captures/pcap-session.cap"
	)

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vmID, vmAddress := packetCaptureFixtureVM(t, rg, "pcap-vnet", "pcap-subnet", "pcap-nic", vmName)
	createStorageAccountForARM(t, rg, account)
	storageID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s",
		subscriptionID, rg, account)

	watcherClient, err := armnetwork.NewWatchersClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = watcherClient.CreateOrUpdate(ctx, rg, watcher, armnetwork.Watcher{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.WatcherPropertiesFormat{},
	}, nil)
	require.NoError(t, err)

	captureClient, err := armnetwork.NewPacketCapturesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	start := func(name string, params armnetwork.PacketCaptureParameters) (armnetwork.PacketCaptureResult, error) {
		poller, err := captureClient.BeginCreate(ctx, rg, watcher, name, armnetwork.PacketCapture{Properties: &params}, nil)
		if err != nil {
			return armnetwork.PacketCaptureResult{}, err
		}
		done, err := poller.PollUntilDone(ctx, nil)
		return done.PacketCaptureResult, err
	}
	httpToIMDS := armnetwork.PacketCaptureParameters{
		Target:     to.Ptr(vmID),
		TargetType: to.Ptr(armnetwork.PacketCaptureTargetTypeAzureVM),
		StorageLocation: &armnetwork.PacketCaptureStorageLocation{
			StorageID: to.Ptr(storageID),
			FilePath:  to.Ptr(filePath),
		},
		Filters: []*armnetwork.PacketCaptureFilter{{
			Protocol:       to.Ptr(armnetwork.PcProtocolTCP),
			LocalIPAddress: to.Ptr(vmAddress),
			RemotePort:     to.Ptr("80"),
		}},
	}

	// Packet capture needs the Network Watcher agent on the machine.
	_, err = start(capture, httpToIMDS)
	requireAzureErrorCode(t, err, "PacketCaptureTargetNotReady", "a machine without the agent cannot be captured")
	assert.Contains(t, err.Error(), "NetworkWatcherAgentLinux")

	extClient, err := armcompute.NewVirtualMachineExtensionsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	extPoller, err := extClient.BeginCreateOrUpdate(ctx, rg, vmName, "AzureNetworkWatcherExtension", armcompute.VirtualMachineExtension{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineExtensionProperties{
			Publisher:          to.Ptr("Microsoft.Azure.NetworkWatcher"),
			Type:               to.Ptr("NetworkWatcherAgentLinux"),
			TypeHandlerVersion: to.Ptr("1.4"),
		},
	}, nil)
	require.NoError(t, err)
	ext, err := extPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Succeeded", derefString(ext.Properties.ProvisioningState))

	created, err := start(capture, httpToIMDS)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, armnetwork.ProvisioningStateSucceeded, *created.Properties.ProvisioningState)
	assert.EqualValues(t, 18000, *created.Properties.TimeLimitInSeconds, "the declared default time limit")
	assert.EqualValues(t, 1073741824, *created.Properties.TotalBytesPerSession, "the declared default session size")
	storagePath := derefString(created.Properties.StorageLocation.StoragePath)
	assert.Contains(t, storagePath, "/network-watcher-logs/subscriptions/"+strings.ToLower(subscriptionID)+
		"/resourcegroups/"+rg+"/providers/microsoft.compute/virtualmachines/"+vmName+"/")

	status := packetCaptureStatus(t, captureClient, rg, watcher, capture)
	assert.Equal(t, armnetwork.PcStatusRunning, *status.PacketCaptureStatus)

	// Talk to the Instance Metadata Service over TCP port 80, which the filter
	// takes, and resolve a name over UDP, which it does not.
	guestRun(t, extClient, rg, vmName, "traffic",
		`exec 3<>/dev/tcp/169.254.169.254/80; `+
			`printf 'GET /metadata/instance?api-version=2021-02-01 HTTP/1.0\r\nMetadata: true\r\n\r\n' >&3; cat <&3 >/dev/null; `+
			`echo probe > /dev/udp/168.63.129.16/53`)

	stopPoller, err := captureClient.BeginStop(ctx, rg, watcher, capture, nil)
	require.NoError(t, err)
	_, err = stopPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	status = packetCaptureStatus(t, captureClient, rg, watcher, capture)
	assert.Equal(t, armnetwork.PcStatusStopped, *status.PacketCaptureStatus)
	assert.Empty(t, status.PacketCaptureError)

	u, err := url.Parse(storagePath)
	require.NoError(t, err)
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
	require.Len(t, parts, 2)
	download, err := newBlobTestClient(t, account).DownloadStream(ctx, parts[0], parts[1], nil)
	require.NoError(t, err)
	file, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	_ = download.Body.Close()

	local := netip.MustParseAddr(vmAddress)
	var outgoing, incoming int
	for _, frame := range readPcap(t, file) {
		src, dst, proto, srcPort, dstPort := ipv4Tuple(t, frame)
		require.Equal(t, uint8(6), proto, "the filter takes TCP only")
		switch {
		case src == local && dstPort == 80:
			outgoing++
		case dst == local && srcPort == 80:
			incoming++
		default:
			t.Errorf("a captured segment %s:%d -> %s:%d falls outside the filter", src, srcPort, dst, dstPort)
		}
	}
	assert.Positive(t, outgoing, "the request to the Instance Metadata Service was captured")
	assert.Positive(t, incoming, "its answer was captured")

	// The same file landed at the local path in the machine.
	size := guestRun(t, extClient, rg, vmName, "size", "wc -c < "+filePath)
	assert.Contains(t, size, fmt.Sprintf("%d", len(file)))

	// A session ends by itself at its time limit.
	_, err = start(timed, armnetwork.PacketCaptureParameters{
		Target:             to.Ptr(vmID),
		TimeLimitInSeconds: to.Ptr[int32](1),
		StorageLocation:    &armnetwork.PacketCaptureStorageLocation{StorageID: to.Ptr(storageID)},
	})
	require.NoError(t, err)
	// Network Watcher offers no wait for a session's end; read its status.
	deadline := time.Now().Add(time.Minute)
	for status = packetCaptureStatus(t, captureClient, rg, watcher, timed); *status.PacketCaptureStatus == armnetwork.PcStatusRunning; status = packetCaptureStatus(t, captureClient, rg, watcher, timed) {
		require.True(t, time.Now().Before(deadline), "the session outlived its time limit")
		time.Sleep(time.Second)
	}
	assert.Equal(t, armnetwork.PcStatusStopped, *status.PacketCaptureStatus)
	assert.Equal(t, "TimeExceeded", derefString(status.StopReason))

	// Deleting the resource leaves the file in the account.
	delPoller, err := captureClient.BeginDelete(ctx, rg, watcher, capture, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = captureClient.Get(ctx, rg, watcher, capture, nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "a deleted capture is gone")
	_, err = newBlobTestClient(t, account).DownloadStream(ctx, parts[0], parts[1], nil)
	require.NoError(t, err, "the capture file outlives the capture resource")
}

// A target that does not exist is refused, never answered with an empty file.
func TestNetworkWatcher_PacketCaptureRefusesAMissingTarget(t *testing.T) {
	t.Parallel()
	const rg = "pcap-missing-rg"
	const watcher = "NetworkWatcher_eastus"

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	watcherClient, err := armnetwork.NewWatchersClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = watcherClient.CreateOrUpdate(ctx, rg, watcher, armnetwork.Watcher{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.WatcherPropertiesFormat{},
	}, nil)
	require.NoError(t, err)

	captureClient, err := armnetwork.NewPacketCapturesClient(subscriptionID, &clientCredential{}, clientOpts())
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
	requireAzureErrorCode(t, err, "PacketCaptureTargetNotReady", "a missing machine cannot be captured")
	assert.Contains(t, err.Error(), "was not found")

	_, err = captureClient.Get(ctx, rg, watcher, "doomed", nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "a refused capture is not a resource")
}

func packetCaptureStatus(t *testing.T, client *armnetwork.PacketCapturesClient, rg, watcher, name string) armnetwork.PacketCaptureQueryStatusResult {
	t.Helper()
	poller, err := client.BeginGetStatus(ctx, rg, watcher, name, nil)
	require.NoError(t, err)
	status, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, status.PacketCaptureStatus)
	return status.PacketCaptureQueryStatusResult
}

// guestRun runs command in the machine through the Custom Script extension
// and returns what the extension reported.
func guestRun(t *testing.T, client *armcompute.VirtualMachineExtensionsClient, rg, vmName, name, command string) string {
	t.Helper()
	poller, err := client.BeginCreateOrUpdate(ctx, rg, vmName, name, armcompute.VirtualMachineExtension{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineExtensionProperties{
			Publisher:          to.Ptr("Microsoft.Azure.Extensions"),
			Type:               to.Ptr("CustomScript"),
			TypeHandlerVersion: to.Ptr("2.1"),
			Settings:           map[string]any{"commandToExecute": "bash -c '" + strings.ReplaceAll(command, "'", `'\''`) + "'"},
		},
	}, nil)
	require.NoError(t, err)
	ext, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err, "the command failed in the guest")
	var messages []string
	for _, status := range ext.Properties.InstanceView.Statuses {
		messages = append(messages, derefString(status.Message))
	}
	return strings.Join(messages, "\n")
}

// readPcap returns the frames of a libpcap file with Ethernet link headers.
func readPcap(t *testing.T, file []byte) [][]byte {
	t.Helper()
	require.GreaterOrEqual(t, len(file), 24, "a capture file starts with the libpcap global header")
	require.Equal(t, uint32(0xa1b2c3d4), binary.LittleEndian.Uint32(file[0:]), "libpcap magic")
	require.Equal(t, uint32(1), binary.LittleEndian.Uint32(file[20:]), "Ethernet link type")
	var frames [][]byte
	for rest := file[24:]; len(rest) > 0; {
		require.GreaterOrEqual(t, len(rest), 16, "a truncated record header")
		size := int(binary.LittleEndian.Uint32(rest[8:]))
		require.GreaterOrEqual(t, len(rest), 16+size, "a truncated record")
		frames = append(frames, rest[16:16+size])
		rest = rest[16+size:]
	}
	return frames
}

func ipv4Tuple(t *testing.T, frame []byte) (src, dst netip.Addr, proto uint8, srcPort, dstPort uint16) {
	t.Helper()
	require.GreaterOrEqual(t, len(frame), 14+20+4)
	require.Equal(t, uint16(0x0800), binary.BigEndian.Uint16(frame[12:]), "an IPv4 frame")
	ip := frame[14:]
	ihl := int(ip[0]&0x0f) * 4
	src = netip.AddrFrom4([4]byte(ip[12:16]))
	dst = netip.AddrFrom4([4]byte(ip[16:20]))
	return src, dst, ip[9], binary.BigEndian.Uint16(ip[ihl:]), binary.BigEndian.Uint16(ip[ihl+2:])
}

// packetCaptureFixtureVM builds the network and the running machine a capture
// reads frames from, and returns the machine's resource ID and private
// address.
func packetCaptureFixtureVM(t *testing.T, rg, vnetName, subnetName, nicName, vmName string) (string, string) {
	t.Helper()

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &clientCredential{}, clientOpts())
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

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, subnetName, armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr("10.105.1.0/24")},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &clientCredential{}, clientOpts())
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
	require.NotEmpty(t, nicResp.Properties.IPConfigurations)
	address := derefString(nicResp.Properties.IPConfigurations[0].Properties.PrivateIPAddress)
	require.NotEmpty(t, address)

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &clientCredential{}, clientOpts())
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
	return *vm.ID, address
}
