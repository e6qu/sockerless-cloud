package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The verbs a disk carries beyond its lifecycle, in both scopes: the zonal
// disks are a typed record and the regional ones a map, and the verbs are
// written once over both so the two cannot serve the same verb differently.

func TestCompute_ZonalDiskVerbs(t *testing.T) {
	svc := computeService(t)
	const project, zone, name = "disk-verbs", "us-central1-a", "data"

	_, err := svc.Disks.Insert(project, zone, &compute.Disk{Name: name, SizeGb: 10}).Do()
	require.NoError(t, err)

	const policy = "regions/us-central1/resourcePolicies/nightly"
	_, err = svc.Disks.AddResourcePolicies(project, zone, name,
		&compute.DisksAddResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.NoError(t, err)
	got, err := svc.Disks.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, got.ResourcePolicies, 1)

	// The same schedule twice is refused rather than doubled.
	_, err = svc.Disks.AddResourcePolicies(project, zone, name,
		&compute.DisksAddResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already attached")

	// A snapshot taken from the disk is a real snapshot.
	_, err = svc.Disks.CreateSnapshot(project, zone, name, &compute.Snapshot{Name: "data-snap"}).Do()
	require.NoError(t, err)
	snapshot, err := svc.Snapshots.Get(project, "data-snap").Do()
	require.NoError(t, err)
	assert.Contains(t, snapshot.SourceDisk, "/zones/"+zone+"/disks/"+name)
	assert.Equal(t, int64(10), snapshot.DiskSizeGb, "the snapshot carries the disk's size")

	// Taking the same snapshot twice is a conflict.
	_, err = svc.Disks.CreateSnapshot(project, zone, name, &compute.Snapshot{Name: "data-snap"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, err = svc.Disks.RemoveResourcePolicies(project, zone, name,
		&compute.DisksRemoveResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.NoError(t, err)
	got, err = svc.Disks.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Empty(t, got.ResourcePolicies)
}

func TestCompute_ZonalDiskAsyncReplication(t *testing.T) {
	svc := computeService(t)
	const project, zone, name = "disk-replica", "us-central1-a", "primary"

	_, err := svc.Disks.Insert(project, zone, &compute.Disk{Name: name, SizeGb: 10}).Do()
	require.NoError(t, err)

	// Stopping replication that never started has nothing to stop.
	_, err = svc.Disks.StopAsyncReplication(project, zone, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not replicating")

	_, err = svc.Disks.StartAsyncReplication(project, zone, name,
		&compute.DisksStartAsyncReplicationRequest{
			AsyncSecondaryDisk: "projects/disk-replica/zones/us-east1-b/disks/secondary",
		}).Do()
	require.NoError(t, err)
	got, err := svc.Disks.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.AsyncPrimaryDisk)
	assert.Contains(t, got.AsyncPrimaryDisk.Disk, "secondary")

	_, err = svc.Disks.StopAsyncReplication(project, zone, name).Do()
	require.NoError(t, err)
	got, err = svc.Disks.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Nil(t, got.AsyncPrimaryDisk, "the disk is no longer replicating")
}

func TestCompute_RegionalDiskVerbsMatchTheZonalOnes(t *testing.T) {
	svc := computeService(t)
	const project, region, name = "disk-regional", "us-central1", "shared"

	_, err := svc.RegionDisks.Insert(project, region, &compute.Disk{Name: name, SizeGb: 20}).Do()
	require.NoError(t, err)

	const policy = "regions/us-central1/resourcePolicies/weekly"
	_, err = svc.RegionDisks.AddResourcePolicies(project, region, name,
		&compute.RegionDisksAddResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.NoError(t, err)
	got, err := svc.RegionDisks.Get(project, region, name).Do()
	require.NoError(t, err)
	require.Len(t, got.ResourcePolicies, 1)

	// The same refusal the zonal scope gives.
	_, err = svc.RegionDisks.RemoveResourcePolicies(project, region, name,
		&compute.RegionDisksRemoveResourcePoliciesRequest{
			ResourcePolicies: []string{"regions/us-central1/resourcePolicies/absent"},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not attached")

	_, err = svc.RegionDisks.CreateSnapshot(project, region, name, &compute.Snapshot{Name: "shared-snap"}).Do()
	require.NoError(t, err)
	snapshot, err := svc.Snapshots.Get(project, "shared-snap").Do()
	require.NoError(t, err)
	assert.Contains(t, snapshot.SourceDisk, "/regions/"+region+"/disks/"+name)
}
