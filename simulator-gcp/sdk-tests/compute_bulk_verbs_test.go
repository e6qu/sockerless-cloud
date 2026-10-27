package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The operation says nothing about the run, so every assertion reads the
// resources back.

func TestCompute_InstancesBulkInsert(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, zone = "bulk-project", "us-central1-a"
	subnet := createInstanceSubnetwork(t, svc, project, "us-central1", "bulk-net", "10.81.0.0/24")

	properties := func() *compute.InstanceProperties {
		return &compute.InstanceProperties{
			MachineType: "e2-micro",
			Disks: []*compute.AttachedDisk{{
				Boot: true, AutoDelete: true,
				InitializeParams: &compute.AttachedDiskInitializeParams{
					SourceImage: "projects/debian-cloud/global/images/family/" + debianFamily(),
				},
			}},
			NetworkInterfaces: []*compute.NetworkInterface{{Name: "nic0", Subnetwork: subnet}},
		}
	}
	t.Cleanup(func() {
		for _, name := range []string{"worker-001", "worker-002", "worker-003"} {
			_, _ = svc.Instances.Delete(project, zone, name).Do()
		}
	})

	op, err := svc.Instances.BulkInsert(project, zone, &compute.BulkInsertInstanceResource{
		Count: 3, MinCount: 3, NamePattern: "worker-###",
		InstanceProperties: properties(),
	}).Do()
	require.NoError(t, err)

	// The run comes up behind the operation, as a single insert does.
	_, err = svc.ZoneOperations.Wait(project, zone, op.Name).Do()
	require.NoError(t, err)

	// Three instances numbered from the pattern, each on the run's machine type
	// and attached to a real network interface.
	for _, name := range []string{"worker-001", "worker-002", "worker-003"} {
		got, err := svc.Instances.Get(project, zone, name).Do()
		require.NoError(t, err, "the bulk insert created %s", name)
		assert.Contains(t, got.MachineType, "e2-micro")
		assert.Equal(t, "RUNNING", got.Status, "%s is running", name)
		require.Len(t, got.NetworkInterfaces, 1, "%s is attached to a network", name)
		assert.NotEmpty(t, got.NetworkInterfaces[0].NetworkIP,
			"%s holds a real address, not a placeholder", name)
	}

	// A pattern with nothing to number by would give every instance in the run
	// the same name.
	_, err = svc.Instances.BulkInsert(project, zone, &compute.BulkInsertInstanceResource{
		Count: 2, NamePattern: "worker", InstanceProperties: properties(),
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no # to number")

	// A run of nothing is not a run.
	_, err = svc.Instances.BulkInsert(project, zone, &compute.BulkInsertInstanceResource{
		NamePattern: "worker-###", InstanceProperties: properties(),
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "count greater than zero")

	// And a run that would overwrite an instance is refused before it writes
	// anything, rather than clobbering half of them.
	_, err = svc.Instances.BulkInsert(project, zone, &compute.BulkInsertInstanceResource{
		Count: 2, NamePattern: "worker-###", InstanceProperties: properties(),
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would overwrite")
}

func TestCompute_DisksBulkInsertAndLabels(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, zone = "bulk-disks", "us-central1-a"

	// A bulk disk insert restores from a group; with nothing named there is
	// nothing to create.
	_, err := svc.Disks.BulkInsert(project, zone, &compute.BulkInsertDiskResource{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "group it restores from")

	_, err = svc.Disks.BulkInsert(project, zone, &compute.BulkInsertDiskResource{
		SourceConsistencyGroupPolicy: "regions/us-central1/resourcePolicies/nightly",
	}).Do()
	require.NoError(t, err)

	_, err = svc.Disks.Insert(project, zone, &compute.Disk{Name: "data", SizeGb: 10}).Do()
	require.NoError(t, err)

	_, err = svc.Disks.BulkSetLabels(project, zone, &compute.BulkZoneSetLabelsRequest{
		Requests: []*compute.BulkSetLabelsRequest{
			{Labels: map[string]string{"tier": "silver", "team": "data"}},
			{Labels: map[string]string{"tier": "gold"}},
		},
	}).Resource("data").Do()
	require.NoError(t, err)

	got, err := svc.Disks.Get(project, zone, "data").Do()
	require.NoError(t, err)
	assert.Equal(t, "gold", got.Labels["tier"], "the label sets apply in order, so the last write stands")
	assert.Equal(t, "data", got.Labels["team"])

	// A disk that is not there cannot be labelled or patched.
	_, err = svc.Disks.BulkSetLabels(project, zone, &compute.BulkZoneSetLabelsRequest{
		Requests: []*compute.BulkSetLabelsRequest{{Labels: map[string]string{"tier": "gold"}}},
	}).Resource("absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
