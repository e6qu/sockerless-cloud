package azure_sdk_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	armcomputev8 "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type galleryClients struct {
	galleries *armcomputev8.GalleriesClient
	images    *armcomputev8.GalleryImagesClient
	versions  *armcomputev8.GalleryImageVersionsClient
}

func newGalleryClients(t *testing.T) galleryClients {
	t.Helper()
	galleries, err := armcomputev8.NewGalleriesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	images, err := armcomputev8.NewGalleryImagesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	versions, err := armcomputev8.NewGalleryImageVersionsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	return galleryClients{galleries: galleries, images: images, versions: versions}
}

// createGalleryImage creates a gallery and one Linux image definition in it,
// removed again when the test ends.
func createGalleryImage(t *testing.T, c galleryClients, rg, gallery, image string, osState armcomputev8.OperatingSystemStateTypes) armcomputev8.GalleryImage {
	t.Helper()
	gp, err := c.galleries.BeginCreateOrUpdate(ctx, rg, gallery, armcomputev8.Gallery{
		Location:   to.Ptr("eastus"),
		Properties: &armcomputev8.GalleryProperties{Description: to.Ptr("images built by the tests")},
	}, nil)
	require.NoError(t, err)
	created, err := gp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armcomputev8.GalleryProvisioningStateSucceeded, *created.Properties.ProvisioningState)
	assert.Equal(t, subscriptionID+"-"+strings.ToUpper(gallery), derefString(created.Properties.Identifier.UniqueName),
		"the unique name is the subscription and the upper-cased gallery name")
	t.Cleanup(func() {
		if del, err := c.galleries.BeginDelete(ctx, rg, gallery, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	ip, err := c.images.BeginCreateOrUpdate(ctx, rg, gallery, image, armcomputev8.GalleryImage{
		Location: to.Ptr("eastus"),
		Properties: &armcomputev8.GalleryImageProperties{
			OSType:           to.Ptr(armcomputev8.OperatingSystemTypesLinux),
			OSState:          to.Ptr(osState),
			HyperVGeneration: to.Ptr(armcomputev8.HyperVGenerationV2),
			Identifier:       &armcomputev8.GalleryImageIdentifier{Publisher: to.Ptr("sockerless"), Offer: to.Ptr(image), SKU: to.Ptr("1")},
		},
	}, nil)
	require.NoError(t, err)
	definition, err := ip.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armcomputev8.ArchitectureX64, *definition.Properties.Architecture, "an image definition defaults to x64")
	t.Cleanup(func() {
		if del, err := c.images.BeginDelete(ctx, rg, gallery, image, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	return definition.GalleryImage
}

func createGalleryImageVersion(t *testing.T, c galleryClients, rg, gallery, image, version string, source armcomputev8.GalleryArtifactVersionFullSource, excludeFromLatest bool) armcomputev8.GalleryImageVersion {
	t.Helper()
	vp, err := c.versions.BeginCreateOrUpdate(ctx, rg, gallery, image, version, armcomputev8.GalleryImageVersion{
		Location: to.Ptr("eastus"),
		Properties: &armcomputev8.GalleryImageVersionProperties{
			PublishingProfile: &armcomputev8.GalleryImageVersionPublishingProfile{ExcludeFromLatest: to.Ptr(excludeFromLatest)},
			StorageProfile:    &armcomputev8.GalleryImageVersionStorageProfile{Source: &source},
		},
	}, nil)
	require.NoError(t, err)
	created, err := vp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := c.versions.BeginDelete(ctx, rg, gallery, image, version, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	return created.GalleryImageVersion
}

// galleryNIC creates another interface in the subnet vmOperationsFixture made.
func galleryNIC(t *testing.T, rg, prefix, name string) string {
	t.Helper()
	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s-vnet/subnets/%s-subnet", subscriptionID, rg, prefix, prefix)
	poller, err := nicClient.BeginCreateOrUpdate(ctx, rg, name, armnetwork.Interface{
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
	nic, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return *nic.ID
}

// A generalized machine captured into a gallery image version boots a new
// machine carrying its disk, named by the image definition, which resolves to
// the newest version latest admits. Galleries, definitions and versions list,
// update and refuse what Azure refuses.
func TestCompute_GalleryImageVersionFromAGeneralizedMachineBootsAMachine(t *testing.T) {
	t.Parallel()
	const rg, prefix, vmName = "gallery-gen-rg", "gal-gen", "gal-gen-vm"
	const gallery, image = "galgen", "gen-image"
	nicID := vmOperationsFixture(t, rg, prefix, "10.160.0.0/16")
	vmClient := createOperationsVM(t, rg, vmName, nicID, nil)
	runInGuest(t, rg, vmName, "write", "echo captured-into-the-gallery > /var/tmp/gallery-proof")
	c := newGalleryClients(t)
	definition := createGalleryImage(t, c, rg, gallery, image, armcomputev8.OperatingSystemStateTypesGeneralized)
	vmID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s", subscriptionID, rg, vmName)

	_, err := c.versions.BeginCreateOrUpdate(ctx, rg, gallery, image, "1.0.0", armcomputev8.GalleryImageVersion{
		Location: to.Ptr("eastus"),
		Properties: &armcomputev8.GalleryImageVersionProperties{
			StorageProfile: &armcomputev8.GalleryImageVersionStorageProfile{Source: &armcomputev8.GalleryArtifactVersionFullSource{VirtualMachineID: to.Ptr(vmID)}},
		},
	}, nil)
	requireAzureErrorCode(t, err, "OperationNotAllowed", "a Generalized version from a machine never generalized")

	deallocate, err := vmClient.BeginDeallocate(ctx, rg, vmName, nil)
	require.NoError(t, err)
	_, err = deallocate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = vmClient.Generalize(ctx, rg, vmName, nil)
	require.NoError(t, err)

	first := createGalleryImageVersion(t, c, rg, gallery, image, "1.0.0",
		armcomputev8.GalleryArtifactVersionFullSource{VirtualMachineID: to.Ptr(vmID)}, false)
	assert.Equal(t, armcomputev8.GalleryProvisioningStateSucceeded, *first.Properties.ProvisioningState)
	require.NotNil(t, first.Properties.PublishingProfile)
	require.Len(t, first.Properties.PublishingProfile.TargetRegions, 1, "the version replicates to the gallery's own region by default")
	assert.Equal(t, "eastus", derefString(first.Properties.PublishingProfile.TargetRegions[0].Name))
	assert.NotNil(t, first.Properties.PublishingProfile.PublishedDate)
	assert.Equal(t, vmID, derefString(first.Properties.StorageProfile.Source.VirtualMachineID))

	withStatus, err := c.versions.Get(ctx, rg, gallery, image, "1.0.0",
		&armcomputev8.GalleryImageVersionsClientGetOptions{Expand: to.Ptr(armcomputev8.ReplicationStatusTypesReplicationStatus)})
	require.NoError(t, err)
	require.NotNil(t, withStatus.Properties.ReplicationStatus)
	assert.Equal(t, armcomputev8.AggregatedReplicationStateCompleted, *withStatus.Properties.ReplicationStatus.AggregatedState)
	plain, err := c.versions.Get(ctx, rg, gallery, image, "1.0.0", nil)
	require.NoError(t, err)
	assert.Nil(t, plain.Properties.ReplicationStatus, "replicationStatus comes only with $expand=ReplicationStatus")

	copied := createGalleryImageVersion(t, c, rg, gallery, image, "1.0.1",
		armcomputev8.GalleryArtifactVersionFullSource{ID: first.ID}, true)
	assert.Equal(t, armcomputev8.GalleryProvisioningStateSucceeded, *copied.Properties.ProvisioningState)

	versionPager := c.versions.NewListByGalleryImagePager(rg, gallery, image, nil)
	var versionNames []string
	for versionPager.More() {
		page, err := versionPager.NextPage(ctx)
		require.NoError(t, err)
		for _, v := range page.Value {
			versionNames = append(versionNames, derefString(v.Name))
		}
	}
	assert.ElementsMatch(t, []string{"1.0.0", "1.0.1"}, versionNames)

	updated, err := c.versions.BeginUpdate(ctx, rg, gallery, image, "1.0.1", armcomputev8.GalleryImageVersionUpdate{
		Tags: map[string]*string{"channel": to.Ptr("canary")},
		Properties: &armcomputev8.GalleryImageVersionProperties{
			PublishingProfile: &armcomputev8.GalleryImageVersionPublishingProfile{ExcludeFromLatest: to.Ptr(true)},
		},
	}, nil)
	require.NoError(t, err)
	afterUpdate, err := updated.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "canary", derefString(afterUpdate.Tags["channel"]))
	assert.Equal(t, *copied.Properties.PublishingProfile.PublishedDate, *afterUpdate.Properties.PublishingProfile.PublishedDate,
		"an update keeps the version's publishedDate")

	// The definition id resolves to 1.0.0: 1.0.1 is newer but excluded from latest.
	nic2 := galleryNIC(t, rg, prefix, "gal-gen-nic-2")
	createOperationsVM(t, rg, vmName+"-2", nic2, func(vm *armcompute.VirtualMachine) {
		vm.Properties.StorageProfile.ImageReference = &armcompute.ImageReference{ID: definition.ID}
	})
	assert.Contains(t, runInGuest(t, rg, vmName+"-2", "read", "cat /var/tmp/gallery-proof"), "captured-into-the-gallery",
		"the machine created from the gallery image does not carry the captured disk")

	imageUpdate, err := c.images.BeginUpdate(ctx, rg, gallery, image, armcomputev8.GalleryImageUpdate{
		Properties: &armcomputev8.GalleryImageProperties{Description: to.Ptr("debian with a proof file")},
	}, nil)
	require.NoError(t, err)
	updatedImage, err := imageUpdate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "debian with a proof file", derefString(updatedImage.Properties.Description))

	_, err = c.images.BeginCreateOrUpdate(ctx, rg, gallery, image, armcomputev8.GalleryImage{
		Location: to.Ptr("eastus"),
		Properties: &armcomputev8.GalleryImageProperties{
			OSType:     to.Ptr(armcomputev8.OperatingSystemTypesLinux),
			OSState:    to.Ptr(armcomputev8.OperatingSystemStateTypesSpecialized),
			Identifier: definition.Properties.Identifier,
		},
	}, nil)
	requireAzureErrorCode(t, err, "PropertyChangeNotAllowed", "an image definition's osState is fixed at creation")

	_, err = c.galleries.BeginDelete(ctx, rg, gallery, nil)
	requireAzureErrorCode(t, err, "CannotDeleteResource", "a gallery holding image definitions")
	_, err = c.images.BeginDelete(ctx, rg, gallery, image, nil)
	requireAzureErrorCode(t, err, "CannotDeleteResource", "an image definition holding versions")

	galleryUpdate, err := c.galleries.BeginUpdate(ctx, rg, gallery, armcomputev8.GalleryUpdate{
		Properties: &armcomputev8.GalleryProperties{Description: to.Ptr("updated")},
	}, nil)
	require.NoError(t, err)
	updatedGallery, err := galleryUpdate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "updated", derefString(updatedGallery.Properties.Description))

	byGroup := c.galleries.NewListByResourceGroupPager(rg, nil)
	page, err := byGroup.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Value, 1)
	assert.Equal(t, gallery, derefString(page.Value[0].Name))
	var inSubscription []string
	for all := c.galleries.NewListPager(nil); all.More(); {
		page, err := all.NextPage(ctx)
		require.NoError(t, err)
		for _, g := range page.Value {
			inSubscription = append(inSubscription, derefString(g.ID))
		}
	}
	assert.Contains(t, inSubscription, derefString(updatedGallery.ID))
	imagePager := c.images.NewListByGalleryPager(rg, gallery, nil)
	images, err := imagePager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, images.Value, 1)
	assert.Equal(t, image, derefString(images.Value[0].Name))

	_, err = c.versions.Get(ctx, rg, gallery, image, "9.9.9", nil)
	requireAzureErrorCode(t, err, "NotFound", "a version the definition does not hold")
}

// A specialized version keeps the source machine as it ran, so Azure refuses
// to provision an OS profile onto a machine created from it.
func TestCompute_SpecializedGalleryImageVersionRefusesAnOSProfile(t *testing.T) {
	t.Parallel()
	const rg, prefix, vmName = "gallery-spec-rg", "gal-spec", "gal-spec-vm"
	const gallery, image = "galspec", "spec-image"
	nicID := vmOperationsFixture(t, rg, prefix, "10.161.0.0/16")
	createOperationsVM(t, rg, vmName, nicID, nil)
	runInGuest(t, rg, vmName, "write", "echo specialized-state > /var/tmp/gallery-proof")
	c := newGalleryClients(t)
	createGalleryImage(t, c, rg, gallery, image, armcomputev8.OperatingSystemStateTypesSpecialized)
	vmID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s", subscriptionID, rg, vmName)
	version := createGalleryImageVersion(t, c, rg, gallery, image, "2.0.0",
		armcomputev8.GalleryArtifactVersionFullSource{VirtualMachineID: to.Ptr(vmID)}, false)
	assert.Equal(t, armcomputev8.GalleryProvisioningStateSucceeded, *version.Properties.ProvisioningState)

	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	nic2 := galleryNIC(t, rg, prefix, "gal-spec-nic-2")
	_, err = vmClient.BeginCreateOrUpdate(ctx, rg, vmName+"-osprofile", armcompute.VirtualMachine{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{VMSize: to.Ptr(armcompute.VirtualMachineSizeTypesStandardB1S)},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: &armcompute.ImageReference{ID: version.ID},
				OSDisk:         &armcompute.OSDisk{CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage)},
			},
			OSProfile:      &armcompute.OSProfile{ComputerName: to.Ptr("osprofile"), AdminUsername: to.Ptr("azureuser"), AdminPassword: to.Ptr("Sockerless!Pass1")},
			NetworkProfile: &armcompute.NetworkProfile{NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{ID: to.Ptr(nic2)}}},
		},
	}, nil)
	requireAzureErrorCode(t, err, "InvalidParameter", "an OS profile on a machine from a specialized image")

	createOperationsVM(t, rg, vmName+"-2", nic2, func(vm *armcompute.VirtualMachine) {
		vm.Properties.StorageProfile.ImageReference = &armcompute.ImageReference{ID: version.ID}
		vm.Properties.OSProfile = nil
	})
	assert.Contains(t, runInGuest(t, rg, vmName+"-2", "read", "cat /var/tmp/gallery-proof"), "specialized-state",
		"the machine created from the specialized version does not carry the source machine's disk")
}

// Gallery applications and gallery sharing answer a declared 501.
func TestCompute_GalleryApplicationsAndSharingAreDeclaredUnserved(t *testing.T) {
	t.Parallel()
	const rg, gallery = "gallery-unserved-rg", "galunserved"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	c := newGalleryClients(t)
	gp, err := c.galleries.BeginCreateOrUpdate(ctx, rg, gallery, armcomputev8.Gallery{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = gp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := c.galleries.BeginDelete(ctx, rg, gallery, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	apps, err := armcomputev8.NewGalleryApplicationsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = apps.BeginCreateOrUpdate(ctx, rg, gallery, "app", armcomputev8.GalleryApplication{
		Location:   to.Ptr("eastus"),
		Properties: &armcomputev8.GalleryApplicationProperties{SupportedOSType: to.Ptr(armcomputev8.OperatingSystemTypesLinux)},
	}, nil)
	requireAzureErrorCode(t, err, "NotImplemented", "a gallery application")

	sharing, err := armcomputev8.NewGallerySharingProfileClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = sharing.BeginUpdate(ctx, rg, gallery, armcomputev8.SharingUpdate{OperationType: to.Ptr(armcomputev8.SharingUpdateOperationTypesAdd)}, nil)
	requireAzureErrorCode(t, err, "NotImplemented", "sharing a gallery")
}
