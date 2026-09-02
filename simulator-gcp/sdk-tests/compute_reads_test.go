package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// Reads that answer from what the project already holds, and the writes beside
// them. Each is derived rather than stored separately, so none can disagree
// with the resources it describes.

func TestCompute_RegionZonesAndImageFamilyView(t *testing.T) {
	svc := computeService(t)
	const project, region, zone = "reads-project", "us-central1", "us-central1-a"

	zones, err := svc.RegionZones.List(project, region).Do()
	require.NoError(t, err)
	require.NotEmpty(t, zones.Items)
	for _, z := range zones.Items {
		assert.Contains(t, z.Name, region, "a region's zones are named after it")
		assert.Equal(t, "UP", z.Status)
	}

	// The family view resolves to the same image the catalogue read does, so a
	// client cannot be told two different things about one family.
	view, err := svc.ImageFamilyViews.Get(project, zone, "debian").Do()
	require.NoError(t, err)
	require.NotNil(t, view.Image)
	assert.Contains(t, view.Image.Name, "debian")

	fromFamily, err := svc.Images.GetFromFamily(project, "debian").Do()
	require.NoError(t, err)
	assert.Equal(t, fromFamily.Name, view.Image.Name)
}

func TestCompute_StoragePoolListsTheDisksThatNameIt(t *testing.T) {
	svc := computeService(t)
	const project, zone, pool = "pool-reads", "us-central1-a", "fast"

	_, err := svc.StoragePools.Insert(project, zone, &compute.StoragePool{
		Name: pool, StoragePoolType: "zones/" + zone + "/storagePoolTypes/hyperdisk-balanced",
	}).Do()
	require.NoError(t, err)

	// A pool with no disks pointing at it holds none.
	listed, err := svc.StoragePools.ListDisks(project, zone, pool).Do()
	require.NoError(t, err)
	assert.Empty(t, listed.Items)

	_, err = svc.Disks.Insert(project, zone, &compute.Disk{
		Name: "in-pool", SizeGb: 20,
		StoragePool: "projects/" + project + "/zones/" + zone + "/storagePools/" + pool,
	}).Do()
	require.NoError(t, err)
	_, err = svc.Disks.Insert(project, zone, &compute.Disk{Name: "standalone", SizeGb: 20}).Do()
	require.NoError(t, err)

	listed, err = svc.StoragePools.ListDisks(project, zone, pool).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1, "only the disk that names the pool is in it")
	assert.Equal(t, "in-pool", listed.Items[0].Name)
}

func TestCompute_UsableSubnetworks(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, region = "usable-subnets", "us-central1"

	_, err := svc.Networks.Insert(project, &compute.Network{Name: "custom"}).Do()
	require.NoError(t, err)
	_, err = svc.Subnetworks.Insert(project, region, &compute.Subnetwork{
		Name: "workers", IpCidrRange: "10.42.0.0/24",
		Network: "projects/" + project + "/global/networks/custom",
	}).Do()
	require.NoError(t, err)

	usable, err := svc.Subnetworks.ListUsable(project).Do()
	require.NoError(t, err)
	require.NotEmpty(t, usable.Items)
	found := false
	for _, item := range usable.Items {
		if item.IpCidrRange == "10.42.0.0/24" {
			found = true
		}
	}
	assert.True(t, found, "a subnetwork the project holds is usable in it")
}

func TestCompute_SnapshotKeyImageDeprecationAndRegionalResize(t *testing.T) {
	svc := computeService(t)
	const project, region = "writes-project", "us-central1"

	_, err := svc.Snapshots.Insert(project, &compute.Snapshot{Name: "nightly"}).Do()
	require.NoError(t, err)
	_, err = svc.Snapshots.UpdateKmsKey(project, "nightly", &compute.SnapshotUpdateKmsKeyRequest{
		KmsKeyName: "projects/p/locations/global/keyRings/r/cryptoKeys/k",
	}).Do()
	require.NoError(t, err)
	snapshot, err := svc.Snapshots.Get(project, "nightly").Do()
	require.NoError(t, err)
	require.NotNil(t, snapshot.SnapshotEncryptionKey)
	assert.Contains(t, snapshot.SnapshotEncryptionKey.KmsKeyName, "cryptoKeys/k")

	// A key is required: re-encrypting under nothing is not a request.
	_, err = svc.Snapshots.UpdateKmsKey(project, "nightly",
		&compute.SnapshotUpdateKmsKeyRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs the key")

	_, err = svc.Images.Insert(project, &compute.Image{Name: "base-v1"}).Do()
	require.NoError(t, err)
	_, err = svc.Images.Deprecate(project, "base-v1", &compute.DeprecationStatus{
		State: "DEPRECATED", Replacement: "global/images/base-v2",
	}).Do()
	require.NoError(t, err)
	image, err := svc.Images.Get(project, "base-v1").Do()
	require.NoError(t, err)
	require.NotNil(t, image.Deprecated)
	assert.Equal(t, "DEPRECATED", image.Deprecated.State)
	assert.Contains(t, image.Deprecated.Replacement, "base-v2")

	// A deprecation with no state says nothing about where the image stands.
	_, err = svc.Images.Deprecate(project, "base-v1", &compute.DeprecationStatus{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state the image is being moved to")

	// A regional disk only grows.
	_, err = svc.RegionDisks.Insert(project, region, &compute.Disk{Name: "shared", SizeGb: 50}).Do()
	require.NoError(t, err)
	_, err = svc.RegionDisks.Resize(project, region, "shared",
		&compute.RegionDisksResizeRequest{SizeGb: 100}).Do()
	require.NoError(t, err)
	disk, err := svc.RegionDisks.Get(project, region, "shared").Do()
	require.NoError(t, err)
	assert.Equal(t, int64(100), disk.SizeGb)

	_, err = svc.RegionDisks.Resize(project, region, "shared",
		&compute.RegionDisksResizeRequest{SizeGb: 10}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only grow")
}
