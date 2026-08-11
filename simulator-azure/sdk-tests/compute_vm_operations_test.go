package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vmOperationsFixture provisions the network a machine needs and returns the
// NIC id to attach it to, so each operation test starts from a machine that
// really boots rather than a record of one.
func vmOperationsFixture(t *testing.T, rg, prefix, cidr string) string {
	t.Helper()
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, prefix+"-vnet", armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr(cidr + ".0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, prefix+"-vnet", prefix+"-subnet", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr(cidr + ".1.0/24")},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, prefix+"-nic", armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet:                    &armnetwork.Subnet{ID: subnetResp.ID},
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
					PrivateIPAddressVersion:   to.Ptr(armnetwork.IPVersionIPv4),
					Primary:                   to.Ptr(true),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	nicResp, err := nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, nicResp.ID)
	return *nicResp.ID
}

// createOperationsVM boots a machine attached to nicID and returns its client.
func createOperationsVM(t *testing.T, rg, vmName, nicID string, mutate func(*armcompute.VirtualMachine)) *armcompute.VirtualMachinesClient {
	t.Helper()
	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vm := armcompute.VirtualMachine{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes("Standard_B1s"))},
			StorageProfile: &armcompute.StorageProfile{
				OSDisk: &armcompute.OSDisk{
					Name:         to.Ptr(vmName + "-osdisk"),
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
					ManagedDisk:  &armcompute.ManagedDiskParameters{StorageAccountType: to.Ptr(armcompute.StorageAccountTypesStandardLRS)},
				},
			},
			OSProfile: &armcompute.OSProfile{
				ComputerName:  to.Ptr(vmName),
				AdminUsername: to.Ptr("azureuser"),
				AdminPassword: to.Ptr("Sockerless!Pass1"),
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{ID: to.Ptr(nicID)}},
			},
		},
	}
	if mutate != nil {
		mutate(&vm)
	}
	poller, err := vmClient.BeginCreateOrUpdate(ctx, rg, vmName, vm, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if delPoller, err := vmClient.BeginDelete(ctx, rg, vmName, nil); err == nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	})
	return vmClient
}

// The reads that answer off the machine's own state.
func TestCompute_VirtualMachineStateOperations(t *testing.T) {
	requireNetworkHost(t)
	const rg = "vm-ops-rg"
	const vmName = "vm-ops"
	nicID := vmOperationsFixture(t, rg, "vm-ops", "10.91")
	vmClient := createOperationsVM(t, rg, vmName, nicID, nil)

	t.Run("ListAvailableSizes offers the sizes the machine can be resized to", func(t *testing.T) {
		pager := vmClient.NewListAvailableSizesPager(rg, vmName, nil)
		require.True(t, pager.More())
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, page.Value, "a machine offers at least one size to resize to")
		var names []string
		for _, size := range page.Value {
			require.NotNil(t, size.Name)
			names = append(names, *size.Name)
			assert.NotNil(t, size.NumberOfCores, "%s reports no core count", *size.Name)
			assert.NotNil(t, size.MemoryInMB, "%s reports no memory", *size.Name)
		}
		assert.Contains(t, names, "Standard_B1s")
	})

	t.Run("ListByLocation finds the machine in its own location", func(t *testing.T) {
		pager := vmClient.NewListByLocationPager("eastus", nil)
		found := false
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			for _, vm := range page.Value {
				if vm.Name != nil && *vm.Name == vmName {
					found = true
				}
			}
		}
		assert.True(t, found, "ListByLocation omitted %s from eastus", vmName)
	})

	t.Run("ListByLocation does not find it in another location", func(t *testing.T) {
		pager := vmClient.NewListByLocationPager("westeurope", nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			for _, vm := range page.Value {
				assert.NotEqual(t, vmName, derefString(vm.Name),
					"a machine in eastus was listed under westeurope")
			}
		}
	})

	t.Run("Generalize is refused while the machine runs, and reported once stopped", func(t *testing.T) {
		_, err := vmClient.Generalize(ctx, rg, vmName, nil)
		require.Error(t, err, "generalizing a running machine must be refused")

		offPoller, err := vmClient.BeginPowerOff(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = offPoller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		_, err = vmClient.Generalize(ctx, rg, vmName, nil)
		require.NoError(t, err, "generalizing a stopped machine must succeed")

		view, err := vmClient.InstanceView(ctx, rg, vmName, nil)
		require.NoError(t, err)
		var codes []string
		for _, status := range view.Statuses {
			codes = append(codes, derefString(status.Code))
		}
		assert.Contains(t, codes, "OSState/generalized",
			"a generalized machine reports it, which is how a caller knows an image can be captured")
	})

	t.Run("ConvertToManagedDisks succeeds for a machine already on managed disks", func(t *testing.T) {
		poller, err := vmClient.BeginConvertToManagedDisks(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	})
}

// The operations that move the real guest. Each ends with the machine running
// again, which is what the operation means on real hardware.
func TestCompute_VirtualMachineGuestOperations(t *testing.T) {
	requireNetworkHost(t)
	const rg = "vm-guest-rg"
	const vmName = "vm-guest"
	nicID := vmOperationsFixture(t, rg, "vm-guest", "10.92")
	vmClient := createOperationsVM(t, rg, vmName, nicID, nil)

	runningAfter := func(t *testing.T, what string) {
		t.Helper()
		view, err := vmClient.InstanceView(ctx, rg, vmName, nil)
		require.NoError(t, err)
		var codes []string
		for _, status := range view.Statuses {
			codes = append(codes, derefString(status.Code))
		}
		assert.Contains(t, codes, "PowerState/running",
			"the machine is not running after %s, so the guest was not brought back up", what)
	}

	t.Run("Redeploy", func(t *testing.T) {
		poller, err := vmClient.BeginRedeploy(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		runningAfter(t, "Redeploy")
	})

	t.Run("Reimage", func(t *testing.T) {
		poller, err := vmClient.BeginReimage(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		runningAfter(t, "Reimage")
	})

	t.Run("Reapply", func(t *testing.T) {
		poller, err := vmClient.BeginReapply(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		runningAfter(t, "Reapply")
	})

	t.Run("PerformMaintenance", func(t *testing.T) {
		poller, err := vmClient.BeginPerformMaintenance(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		runningAfter(t, "PerformMaintenance")
	})

	t.Run("SimulateEviction is refused for a machine that is not Spot", func(t *testing.T) {
		_, err := vmClient.SimulateEviction(ctx, rg, vmName, nil)
		require.Error(t, err, "only a Spot machine is ever evicted")
	})
}

// A Spot machine is evicted, and its eviction policy decides what that leaves
// behind.
func TestCompute_VirtualMachineSimulateEvictionDeallocatesASpotMachine(t *testing.T) {
	requireNetworkHost(t)
	const rg = "vm-spot-rg"
	const vmName = "vm-spot"
	nicID := vmOperationsFixture(t, rg, "vm-spot", "10.93")
	vmClient := createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.Priority = to.Ptr(armcompute.VirtualMachinePriorityTypesSpot)
		vm.Properties.EvictionPolicy = to.Ptr(armcompute.VirtualMachineEvictionPolicyTypesDeallocate)
	})

	_, err := vmClient.SimulateEviction(ctx, rg, vmName, nil)
	require.NoError(t, err, "a Spot machine is evictable")

	view, err := vmClient.InstanceView(ctx, rg, vmName, nil)
	require.NoError(t, err)
	var codes []string
	for _, status := range view.Statuses {
		codes = append(codes, derefString(status.Code))
	}
	assert.Contains(t, codes, "PowerState/deallocated",
		"an evicted Spot machine with a Deallocate policy is left deallocated")
}

// RetrieveBootDiagnosticsData returns the console output the guest really
// produced, stored in the account the machine names and readable back through
// the Blob data plane.
func TestCompute_VirtualMachineBootDiagnostics(t *testing.T) {
	requireNetworkHost(t)
	const rg = "vm-bootdiag-rg"
	const vmName = "vm-bootdiag"
	nicID := vmOperationsFixture(t, rg, "vm-bootdiag", "10.94")

	t.Run("a machine without boot diagnostics is refused", func(t *testing.T) {
		vmClient := createOperationsVM(t, rg, vmName+"-off", nicID, nil)
		_, err := vmClient.RetrieveBootDiagnosticsData(ctx, rg, vmName+"-off", nil)
		require.Error(t, err, "boot diagnostics is not enabled, so there is nothing to retrieve")
	})

	vmClient := createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.DiagnosticsProfile = &armcompute.DiagnosticsProfile{
			BootDiagnostics: &armcompute.BootDiagnostics{
				Enabled:    to.Ptr(true),
				StorageURI: to.Ptr("https://simbootdiag.blob.core.windows.net/"),
			},
		}
	})

	got, err := vmClient.RetrieveBootDiagnosticsData(ctx, rg, vmName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.SerialConsoleLogBlobURI,
		"a machine with boot diagnostics returns its serial console log")
	uri := *got.SerialConsoleLogBlobURI
	assert.True(t, strings.HasPrefix(uri, "https://simbootdiag.blob.core.windows.net/"),
		"the log is stored in the account the machine names, got %s", uri)
	assert.Contains(t, uri, "bootdiagnostics-")

	// No console screenshot is claimed: the guest is a serial-console machine
	// with no framebuffer, and a URI pointing at an image that does not exist
	// would be worse than its absence.
	assert.Nil(t, got.ConsoleScreenshotBlobURI)
}

// derefString reads an optional string the SDK models as a pointer.
func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
