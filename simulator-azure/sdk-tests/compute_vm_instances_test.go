package azure_sdk_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The marketplace catalog the Compute resource provider publishes under
// locations/{location}/publishers: what it lists is what a machine's
// imageReference resolves against, so a client discovers an image the same
// way it does in Azure, and an image the catalog does not carry is refused
// with the resource provider's own NotFound.
func TestCompute_VirtualMachineImagesCatalog(t *testing.T) {
	t.Parallel()
	images, err := armcompute.NewVirtualMachineImagesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	publishers, err := images.ListPublishers(ctx, "eastus", nil)
	require.NoError(t, err)
	var names []string
	for _, p := range publishers.VirtualMachineImageResourceArray {
		names = append(names, derefString(p.Name))
	}
	assert.Contains(t, names, "Canonical")
	assert.Contains(t, names, "Debian")

	offers, err := images.ListOffers(ctx, "eastus", "Canonical", nil)
	require.NoError(t, err)
	names = names[:0]
	for _, o := range offers.VirtualMachineImageResourceArray {
		names = append(names, derefString(o.Name))
	}
	assert.Contains(t, names, "ubuntu-24_04-lts")

	skus, err := images.ListSKUs(ctx, "eastus", "Canonical", "ubuntu-24_04-lts", nil)
	require.NoError(t, err)
	names = names[:0]
	for _, s := range skus.VirtualMachineImageResourceArray {
		names = append(names, derefString(s.Name))
	}
	assert.ElementsMatch(t, []string{"server", "server-arm64"}, names)

	versions, err := images.List(ctx, "eastus", "Debian", "debian-12", "12", nil)
	require.NoError(t, err)
	require.NotEmpty(t, versions.VirtualMachineImageResourceArray)
	version := derefString(versions.VirtualMachineImageResourceArray[0].Name)
	got, err := images.Get(ctx, "eastus", "Debian", "debian-12", "12", version, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.Architecture)
	assert.Equal(t, armcompute.ArchitectureTypesX64, *got.Properties.Architecture)
	assert.Equal(t, armcompute.OperatingSystemTypesLinux, *got.Properties.OSDiskImage.OperatingSystem)

	_, err = images.ListSKUs(ctx, "eastus", "Canonical", "0001-com-ubuntu-server-jammy", nil)
	require.Error(t, err, "an offer the catalog does not carry is not found")
	assert.Contains(t, err.Error(), "NotFound")
}

// A machine the catalog cannot resolve is refused at request validation, as
// the Compute resource provider refuses it, never as a failed operation.
func TestCompute_VirtualMachineWithUnknownImageIsRefused(t *testing.T) {
	t.Parallel()
	const rg = "vm-noimage-rg"
	nicID := vmOperationsFixture(t, rg, "vm-noimage", "10.100")
	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = vmClient.BeginCreateOrUpdate(ctx, rg, "vm-noimage", armcompute.VirtualMachine{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{VMSize: to.Ptr(armcompute.VirtualMachineSizeTypesStandardB1S)},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{
					Publisher: to.Ptr("Canonical"), Offer: to.Ptr("0001-com-ubuntu-server-jammy"), SKU: to.Ptr("22_04-lts"), Version: to.Ptr("latest"),
				},
				OSDisk: &armcompute.OSDisk{CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage)},
			},
			OSProfile:      &armcompute.OSProfile{ComputerName: to.Ptr("vm-noimage"), AdminUsername: to.Ptr("azureuser"), AdminPassword: to.Ptr("Sockerless!Pass1")},
			NetworkProfile: &armcompute.NetworkProfile{NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{ID: to.Ptr(nicID)}}},
		},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PlatformImageNotFound")
}

// runInGuest runs a command in the machine through the Custom Script
// extension and returns what it wrote.
func runInGuest(t *testing.T, rg, vmName, name, command string) string {
	t.Helper()
	extClient, err := armcompute.NewVirtualMachineExtensionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := extClient.BeginCreateOrUpdate(ctx, rg, vmName, name, armcompute.VirtualMachineExtension{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineExtensionProperties{
			Publisher:          to.Ptr("Microsoft.Azure.Extensions"),
			Type:               to.Ptr("CustomScript"),
			TypeHandlerVersion: to.Ptr("2.1"),
			Settings:           map[string]any{"commandToExecute": command},
		},
	}, nil)
	require.NoError(t, err)
	ran, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err, "the command failed in the guest")
	var output []string
	for _, status := range ran.Properties.InstanceView.Statuses {
		output = append(output, derefString(status.Message))
	}
	return strings.Join(output, "\n")
}

// A machine runs the script its customData carries once at boot, and reaches
// the Instance Metadata Service through its own network, which answers with
// the machine's own identity and interface.
func TestCompute_VirtualMachineRunsCustomDataAndReadsItsMetadata(t *testing.T) {
	t.Parallel()
	const rg = "vm-init-rg"
	const vmName = "vm-init"
	nicID := vmOperationsFixture(t, rg, "vm-init", "10.101")
	script := "#!/bin/sh\necho custom-data-ran > /var/tmp/custom-data\n"
	createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.OSProfile.CustomData = to.Ptr(base64.StdEncoding.EncodeToString([]byte(script)))
		vm.Properties.UserData = to.Ptr(base64.StdEncoding.EncodeToString([]byte("for-imds-only")))
	})

	ran := runInGuest(t, rg, vmName, "custom-data", "cat /var/tmp/custom-data; cat /var/log/cloud-init-output.log")
	assert.Contains(t, ran, "custom-data-ran", "customData did not run at boot")
	assert.Contains(t, ran, "user data exited 0")

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nic, err := nicClient.Get(ctx, rg, "vm-init-nic", nil)
	require.NoError(t, err)
	address := derefString(nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress)

	// The guest reaches the Instance Metadata Service at the endpoint its
	// environment names, over plain TCP from a shell with no HTTP client.
	imds := runInGuest(t, rg, vmName, "imds", `bash -c 'host=${AZURE_INSTANCE_METADATA_ENDPOINT#http://}; host=${host%%/*}; `+
		`exec 3<>/dev/tcp/$host/80; printf "GET /metadata/instance?api-version=2021-02-01 HTTP/1.0\r\nHost: 169.254.169.254\r\nMetadata: true\r\n\r\n" >&3; cat <&3'`)
	assert.Contains(t, imds, `"name":"`+vmName+`"`, "the metadata service did not answer with the machine's own identity")
	assert.Contains(t, imds, `"privateIpAddress":"`+address+`"`, "the metadata service did not answer with the machine's own interface")
	assert.Contains(t, imds, `"userData":"`+base64.StdEncoding.EncodeToString([]byte("for-imds-only"))+`"`)
	assert.Contains(t, imds, `"offer":"debian-12"`)
}

// A deallocated machine keeps its OS disk and starts again on it; an image
// created from the generalized machine carries that disk, and a machine
// created from the image boots it.
func TestCompute_VirtualMachineDeallocateKeepsTheOSDiskAndAnImageCapturesIt(t *testing.T) {
	t.Parallel()
	const rg = "vm-disk-rg"
	const vmName = "vm-disk"
	nicID := vmOperationsFixture(t, rg, "vm-disk", "10.102")
	vmClient := createOperationsVM(t, rg, vmName, nicID, nil)

	runInGuest(t, rg, vmName, "write", "echo kept-across-deallocate > /var/tmp/kept")

	deallocate, err := vmClient.BeginDeallocate(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = deallocate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	view, err := vmClient.InstanceView(ctx, rg, vmName, nil)
	require.NoError(t, err)
	assert.True(t, containsVMStatus(view.Statuses, "PowerState/deallocated"))

	start, err := vmClient.BeginStart(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = start.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Contains(t, runInGuest(t, rg, vmName, "read", "cat /var/tmp/kept"), "kept-across-deallocate",
		"the machine did not start again on the disk it was deallocated with")

	deallocate, err = vmClient.BeginDeallocate(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = deallocate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = vmClient.Generalize(ctx, rg, vmName, nil)
	require.NoError(t, err)

	imagesClient, err := armcompute.NewImagesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vmID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s", subscriptionID, rg, vmName)
	imagePoller, err := imagesClient.BeginCreateOrUpdate(ctx, rg, "vm-disk-image", armcompute.Image{
		Location:   to.Ptr("eastus"),
		Properties: &armcompute.ImageProperties{SourceVirtualMachine: &armcompute.SubResource{ID: to.Ptr(vmID)}},
	}, nil)
	require.NoError(t, err)
	image, err := imagePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, image.ID)
	assert.Equal(t, "Succeeded", derefString(image.Properties.ProvisioningState))
	t.Cleanup(func() {
		if del, err := imagesClient.BeginDelete(ctx, rg, "vm-disk-image", nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	listed := imagesClient.NewListByResourceGroupPager(rg, nil)
	require.True(t, listed.More())
	page, err := listed.NextPage(ctx)
	require.NoError(t, err)
	var names []string
	for _, i := range page.Value {
		names = append(names, derefString(i.Name))
	}
	assert.Contains(t, names, "vm-disk-image")

	// A machine from the image boots the captured disk: the file written
	// into the source machine is in it.
	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/vm-disk-vnet/subnets/vm-disk-subnet", subscriptionID, rg)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "vm-disk-nic-2", armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet:                    &armnetwork.Subnet{ID: to.Ptr(subnetID)},
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
					Primary:                   to.Ptr(true),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	nic2, err := nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	createOperationsVM(t, rg, vmName+"-2", *nic2.ID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.StorageProfile.ImageReference = &armcompute.ImageReference{ID: image.ID}
	})
	assert.Contains(t, runInGuest(t, rg, vmName+"-2", "read", "cat /var/tmp/kept"), "kept-across-deallocate",
		"the machine created from the image does not carry the captured disk")
}
