package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	armcomputev8 "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDisksClient(t *testing.T) *armcomputev8.DisksClient {
	t.Helper()
	client, err := armcomputev8.NewDisksClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	return client
}

func createEmptyDisk(t *testing.T, client *armcomputev8.DisksClient, rg, name string, sizeGB int32) armcomputev8.Disk {
	t.Helper()
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armcomputev8.Disk{
		Location: to.Ptr("eastus"),
		SKU:      &armcomputev8.DiskSKU{Name: to.Ptr(armcomputev8.DiskStorageAccountTypesStandardSSDLRS)},
		Properties: &armcomputev8.DiskProperties{
			CreationData: &armcomputev8.CreationData{CreateOption: to.Ptr(armcomputev8.DiskCreateOptionEmpty)},
			DiskSizeGB:   to.Ptr(sizeGB),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	return created.Disk
}

// A managed disk's lifecycle through DisksClient: an Empty disk, a grow, a
// refused shrink, both lists, a Copy, and a delete.
func TestCompute_ManagedDiskLifecycle(t *testing.T) {
	t.Parallel()
	const rg = "disk-life-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	client := newDisksClient(t)

	created := createEmptyDisk(t, client, rg, "disk-life", 4)
	require.NotNil(t, created.Properties)
	assert.Equal(t, "Succeeded", derefString(created.Properties.ProvisioningState))
	assert.Equal(t, armcomputev8.DiskStateUnattached, *created.Properties.DiskState)
	assert.Equal(t, int64(4)<<30, *created.Properties.DiskSizeBytes)
	assert.Equal(t, armcomputev8.DiskStorageAccountTypesStandardSSDLRS, *created.SKU.Name)
	assert.NotEmpty(t, derefString(created.Properties.UniqueID))
	assert.Nil(t, created.ManagedBy)

	grow, err := client.BeginUpdate(ctx, rg, "disk-life", armcomputev8.DiskUpdate{
		Properties: &armcomputev8.DiskUpdateProperties{DiskSizeGB: to.Ptr[int32](8)},
		Tags:       map[string]*string{"stage": to.Ptr("grown")},
	}, nil)
	require.NoError(t, err)
	_, err = grow.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	got, err := client.Get(ctx, rg, "disk-life", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(8), *got.Properties.DiskSizeGB)
	assert.Equal(t, "grown", derefString(got.Tags["stage"]))

	_, err = client.BeginUpdate(ctx, rg, "disk-life", armcomputev8.DiskUpdate{
		Properties: &armcomputev8.DiskUpdateProperties{DiskSizeGB: to.Ptr[int32](2)},
	}, nil)
	requireAzureErrorCode(t, err, "OperationNotAllowed", "shrinking a disk")

	_, err = client.BeginCreateOrUpdate(ctx, rg, "disk-no-size", armcomputev8.Disk{
		Location:   to.Ptr("eastus"),
		Properties: &armcomputev8.DiskProperties{CreationData: &armcomputev8.CreationData{CreateOption: to.Ptr(armcomputev8.DiskCreateOptionEmpty)}},
	}, nil)
	requireAzureErrorCode(t, err, "InvalidParameter", "an Empty disk without a size")

	copyPoller, err := client.BeginCreateOrUpdate(ctx, rg, "disk-life-copy", armcomputev8.Disk{
		Location: to.Ptr("eastus"),
		Properties: &armcomputev8.DiskProperties{CreationData: &armcomputev8.CreationData{
			CreateOption:     to.Ptr(armcomputev8.DiskCreateOptionCopy),
			SourceResourceID: got.ID,
		}},
	}, nil)
	require.NoError(t, err)
	copied, err := copyPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(8), *copied.Properties.DiskSizeGB, "a copy takes its source's size")
	assert.Equal(t, derefString(got.Properties.UniqueID), derefString(copied.Properties.CreationData.SourceUniqueID))

	var listed []string
	pager := client.NewListByResourceGroupPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			listed = append(listed, derefString(d.Name))
		}
	}
	assert.ElementsMatch(t, []string{"disk-life", "disk-life-copy"}, listed)

	found := false
	all := client.NewListPager(nil)
	for all.More() {
		page, err := all.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			found = found || derefString(d.ID) == derefString(copied.ID)
		}
	}
	assert.True(t, found, "the subscription's list omits the copy")

	del, err := client.BeginDelete(ctx, rg, "disk-life-copy", nil)
	require.NoError(t, err)
	_, err = del.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "disk-life-copy", nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "reading a deleted disk")
}

// A data disk is a directory at its LUN under /dev/disk/azure/scsi1 in the
// guest. What the guest writes there leaves with the disk on a detach and
// comes back on the next attach; a machine holds the disk until then, and
// Azure refuses to delete it.
func TestCompute_DataDisksAttachAndDetachWithTheirData(t *testing.T) {
	t.Parallel()
	const rg = "vm-data-disk-rg"
	const vmName = "vm-data-disk"
	nicID := vmOperationsFixture(t, rg, "vm-data-disk", "10.141.0.0/16")
	disks := newDisksClient(t)
	createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.StorageProfile.DataDisks = []*armcompute.DataDisk{{
			Lun:          to.Ptr[int32](0),
			Name:         to.Ptr("vm-data-disk-empty"),
			CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesEmpty),
			DiskSizeGB:   to.Ptr[int32](4),
			ManagedDisk:  &armcompute.ManagedDiskParameters{StorageAccountType: to.Ptr(armcompute.StorageAccountTypesStandardLRS)},
		}}
	})
	vms, err := armcomputev8.NewVirtualMachinesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	vm, err := vms.Get(ctx, rg, vmName, nil)
	require.NoError(t, err)
	require.Len(t, vm.Properties.StorageProfile.DataDisks, 1)
	empty := vm.Properties.StorageProfile.DataDisks[0]
	require.NotNil(t, empty.ManagedDisk)
	emptyID := *empty.ManagedDisk.ID
	t.Cleanup(func() {
		if del, err := disks.BeginDelete(ctx, rg, "vm-data-disk-empty", nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	held, err := disks.Get(ctx, rg, "vm-data-disk-empty", nil)
	require.NoError(t, err)
	assert.Equal(t, armcomputev8.DiskStateAttached, *held.Properties.DiskState)
	assert.Equal(t, derefString(vm.ID), derefString(held.ManagedBy))

	_, err = disks.BeginDelete(ctx, rg, "vm-data-disk-empty", nil)
	requireAzureErrorCode(t, err, "OperationNotAllowed", "deleting an attached disk")

	runInGuest(t, rg, vmName, "write", "echo carried-by-the-disk > /dev/disk/azure/scsi1/lun0/proof")

	detach, err := vms.BeginAttachDetachDataDisks(ctx, rg, vmName, armcomputev8.AttachDetachDataDisksRequest{
		DataDisksToDetach: []*armcomputev8.DataDisksToDetach{{DiskID: to.Ptr(emptyID)}},
	}, nil)
	require.NoError(t, err)
	profile, err := detach.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, profile.DataDisks)
	released, err := disks.Get(ctx, rg, "vm-data-disk-empty", nil)
	require.NoError(t, err)
	assert.Equal(t, armcomputev8.DiskStateUnattached, *released.Properties.DiskState)
	assert.Nil(t, released.ManagedBy)
	assert.Contains(t, runInGuest(t, rg, vmName, "gone", "test -e /dev/disk/azure/scsi1/lun0 || echo detached"), "detached")

	second := createEmptyDisk(t, disks, rg, "vm-data-disk-second", 4)
	attach, err := vms.BeginAttachDetachDataDisks(ctx, rg, vmName, armcomputev8.AttachDetachDataDisksRequest{
		DataDisksToAttach: []*armcomputev8.DataDisksToAttach{
			{DiskID: to.Ptr(emptyID), Lun: to.Ptr[int32](3)},
			{DiskID: second.ID, DeleteOption: to.Ptr(armcomputev8.DiskDeleteOptionTypesDelete)},
		},
	}, nil)
	require.NoError(t, err)
	profile, err = attach.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	luns := map[string]int32{}
	for _, d := range profile.DataDisks {
		luns[derefString(d.Name)] = *d.Lun
	}
	assert.Equal(t, map[string]int32{"vm-data-disk-empty": 3, "vm-data-disk-second": 0}, luns,
		"an attach without a LUN takes the lowest free one")
	assert.Contains(t, runInGuest(t, rg, vmName, "read", "cat /dev/disk/azure/scsi1/lun3/proof"), "carried-by-the-disk",
		"the disk came back without what the guest wrote to it")

	third := createEmptyDisk(t, disks, rg, "vm-data-disk-third", 4)
	_, err = vms.BeginAttachDetachDataDisks(ctx, rg, vmName, armcomputev8.AttachDetachDataDisksRequest{
		DataDisksToAttach: []*armcomputev8.DataDisksToAttach{{DiskID: third.ID}},
	}, nil)
	requireAzureErrorCode(t, err, "OperationNotAllowed", "attaching past the size's data disk count")

	deallocate, err := vms.BeginDeallocate(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = deallocate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	reserved, err := disks.Get(ctx, rg, "vm-data-disk-empty", nil)
	require.NoError(t, err)
	assert.Equal(t, armcomputev8.DiskStateReserved, *reserved.Properties.DiskState)

	del, err := vms.BeginDelete(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = del.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	kept, err := disks.Get(ctx, rg, "vm-data-disk-empty", nil)
	require.NoError(t, err, "a data disk whose deleteOption is Detach outlives its machine")
	assert.Equal(t, armcomputev8.DiskStateUnattached, *kept.Properties.DiskState)
	_, err = disks.Get(ctx, rg, "vm-data-disk-second", nil)
	requireAzureErrorCode(t, err, "ResourceNotFound", "a data disk whose deleteOption is Delete goes with its machine")
}
