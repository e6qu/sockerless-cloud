package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// A bulk insert creates a run of resources from one request. What a caller
// depends on afterwards is the run existing, so every assertion reads the
// resources back rather than the operation.

func TestCompute_InstancesBulkInsert(t *testing.T) {
	// Every member of the run is a real virtual machine on a real network
	// interface, so the run needs the host that can boot one — the same
	// requirement a single instances.insert has.
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone = "bulk-project", "us-central1-a"

	properties := func() *compute.InstanceProperties {
		return &compute.InstanceProperties{
			MachineType:       "e2-micro",
			NetworkInterfaces: []*compute.NetworkInterface{{Name: "nic0", Network: "global/networks/default"}},
		}
	}

	op, err := svc.Instances.BulkInsert(project, zone, &compute.BulkInsertInstanceResource{
		Count: 3, MinCount: 3, NamePattern: "worker-###",
		InstanceProperties: properties(),
	}).Do()
	require.NoError(t, err)

	// The run comes up behind the operation, exactly as a single insert does,
	// so the caller waits on it before reading the instances back.
	_, err = svc.ZoneOperations.Wait(project, zone, op.Name).Do()
	require.NoError(t, err)

	// The run is three real instances, numbered from the pattern, each on the
	// machine type the run's properties named and each attached to a real
	// network interface — which is what makes them instances rather than
	// records shaped like instances.
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
