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

func TestCompute_VirtualMachineLifecycle(t *testing.T) {
	requireNetworkHost(t)
	const rg = "compute-rg"
	const vnetName = "compute-vnet"
	const subnetName = "compute-subnet"
	const nicName = "compute-nic"
	const vmName = "compute-vm"

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{to.Ptr("10.90.0.0/16")},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, subnetName, armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix: to.Ptr("10.90.1.0/24"),
		},
	}, nil)
	require.NoError(t, err)
	subnetResp, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, subnetResp.ID)

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

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	sizeClient, err := armcompute.NewVirtualMachineSizesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	sizePager := sizeClient.NewListPager("eastus", nil)
	require.True(t, sizePager.More())
	sizePage, err := sizePager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, sizePage.Value)

	skuClient, err := armcompute.NewResourceSKUsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	skuPager := skuClient.NewListPager(nil)
	require.True(t, skuPager.More())
	skuPage, err := skuPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, skuPage.Value)

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
					Name:         to.Ptr("compute-vm-osdisk"),
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
					Caching:      to.Ptr(armcompute.CachingTypesReadWrite),
					DeleteOption: to.Ptr(armcompute.DiskDeleteOptionTypesDelete),
					DiskSizeGB:   to.Ptr[int32](30),
				},
			},
			OSProfile: &armcompute.OSProfile{
				ComputerName:  to.Ptr(vmName),
				AdminUsername: to.Ptr("azureuser"),
				AdminPassword: to.Ptr("Str0ng-password-12345!"),
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{
					ID: nicResp.ID,
					Properties: &armcompute.NetworkInterfaceReferenceProperties{
						Primary: to.Ptr(true),
					},
				}},
			},
		},
		Tags: map[string]*string{"env": to.Ptr("sdk")},
	}, nil)
	require.NoError(t, err)
	vmResp, err := vmPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, vmResp.ID)
	assert.Equal(t, vmName, *vmResp.Name)

	got, err := vmClient.Get(ctx, rg, vmName, &armcompute.VirtualMachinesClientGetOptions{
		Expand: to.Ptr(armcompute.InstanceViewTypesInstanceView),
	})
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.InstanceView)
	require.NotEmpty(t, got.Properties.InstanceView.Statuses)

	// VirtualMachines_ListAll — the subscription-wide inventory read
	// (`GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/virtualMachines`),
	// the one `az vm list` without --resource-group and every
	// subscription-scoped console blade issues. Distinct from the
	// resource-group-scoped NewListPager below it.
	listAllPager := vmClient.NewListAllPager(nil)
	var allVMs []*armcompute.VirtualMachine
	for listAllPager.More() {
		page, err := listAllPager.NextPage(ctx)
		require.NoError(t, err)
		allVMs = append(allVMs, page.Value...)
	}
	require.NotEmpty(t, allVMs)
	foundAll := false
	for _, vm := range allVMs {
		require.NotNil(t, vm.ID)
		require.NotNil(t, vm.Name)
		if *vm.Name == vmName {
			foundAll = true
			require.NotNil(t, vm.Properties)
			require.NotNil(t, vm.Properties.HardwareProfile)
			assert.Equal(t, "eastus", *vm.Location)
			assert.Equal(t, "Microsoft.Compute/virtualMachines", *vm.Type)
		}
	}
	assert.True(t, foundAll, "VirtualMachines_ListAll omitted %s", vmName)

	// statusOnly=true asks the same operation for the instance view rather
	// than the model view — the shape the portal's VM list reads power state
	// from.
	statusPager := vmClient.NewListAllPager(&armcompute.VirtualMachinesClientListAllOptions{StatusOnly: to.Ptr("true")})
	require.True(t, statusPager.More())
	statusPage, err := statusPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, statusPage.Value)
	sawInstanceView := false
	for _, vm := range statusPage.Value {
		if vm.Name != nil && *vm.Name == vmName {
			require.NotNil(t, vm.Properties)
			require.NotNil(t, vm.Properties.InstanceView)
			assert.True(t, containsVMStatus(vm.Properties.InstanceView.Statuses, "PowerState/running"))
			sawInstanceView = true
		}
	}
	assert.True(t, sawInstanceView, "statusOnly ListAll omitted %s", vmName)

	// VirtualMachines_Update — the PATCH carrying a VirtualMachineUpdate. A
	// tags-only update replaces the tags dictionary and must leave every other
	// property (here the hardware profile) exactly as it was.
	updatePoller, err := vmClient.BeginUpdate(ctx, rg, vmName, armcompute.VirtualMachineUpdate{
		Tags: map[string]*string{"env": to.Ptr("sdk"), "owner": to.Ptr("platform")},
	}, nil)
	require.NoError(t, err)
	updated, err := updatePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Tags["owner"])
	assert.Equal(t, "platform", *updated.Tags["owner"])
	require.NotNil(t, updated.Properties)
	require.NotNil(t, updated.Properties.HardwareProfile)
	assert.Equal(t, armcompute.VirtualMachineSizeTypesStandardB1S, *updated.Properties.HardwareProfile.VMSize)

	afterUpdate, err := vmClient.Get(ctx, rg, vmName, nil)
	require.NoError(t, err)
	require.NotNil(t, afterUpdate.Tags["owner"])
	assert.Equal(t, "platform", *afterUpdate.Tags["owner"])
	require.NotNil(t, afterUpdate.Properties.OSProfile)
	assert.Equal(t, vmName, *afterUpdate.Properties.OSProfile.ComputerName)
	// The write-only admin password is never echoed back, on update as on
	// create.
	assert.Nil(t, afterUpdate.Properties.OSProfile.AdminPassword)

	// The resource-group-scoped list stays its own operation and still only
	// reports this group's machines.
	rgPager := vmClient.NewListPager(rg, nil)
	require.True(t, rgPager.More())
	rgPage, err := rgPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rgPage.Value)

	powerOff, err := vmClient.BeginPowerOff(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = powerOff.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	view, err := vmClient.InstanceView(ctx, rg, vmName, nil)
	require.NoError(t, err)
	assert.True(t, containsVMStatus(view.Statuses, "PowerState/stopped"))

	start, err := vmClient.BeginStart(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = start.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	view, err = vmClient.InstanceView(ctx, rg, vmName, nil)
	require.NoError(t, err)
	assert.True(t, containsVMStatus(view.Statuses, "PowerState/running"))

	del, err := vmClient.BeginDelete(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = del.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func containsVMStatus(statuses []*armcompute.InstanceViewStatus, code string) bool {
	for _, status := range statuses {
		if status.Code != nil && strings.EqualFold(*status.Code, code) {
			return true
		}
	}
	return false
}

// TestCompute_VirtualMachinesSubscriptionScope covers the two subscription- and
// resource-scoped virtual-machine operations that need no booted machine, so
// they are exercised on every host rather than only where the real-execution
// network capabilities the lifecycle test needs are available:
// VirtualMachines_ListAll (the subscription-wide inventory read) and
// VirtualMachines_Update against a machine that does not exist, which must
// come back as ARM's own ResourceNotFound rather than silently succeeding.
func TestCompute_VirtualMachinesSubscriptionScope(t *testing.T) {
	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := vmClient.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, vm := range page.Value {
			require.NotNil(t, vm.ID)
			require.NotNil(t, vm.Name)
			require.NotNil(t, vm.Type)
			assert.Equal(t, "Microsoft.Compute/virtualMachines", *vm.Type)
			assert.True(t, strings.HasPrefix(*vm.ID, "/subscriptions/"+subscriptionID+"/"))
		}
	}

	// armcompute surfaces ARM's 404 from BeginUpdate itself rather than from
	// the poller, because the operation never starts.
	_, err = vmClient.BeginUpdate(ctx, "compute-absent-rg", "no-such-vm", armcompute.VirtualMachineUpdate{
		Tags: map[string]*string{"env": to.Ptr("sdk")},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
	assert.Contains(t, err.Error(), "404")
}
