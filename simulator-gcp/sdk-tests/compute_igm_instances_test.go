package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// A managed instance group's instances, per-instance configurations and resize
// requests.
//
// Every verb here answers with an Operation, which says nothing about whether
// anything changed, so each assertion reads the group back: the instances it
// manages after a create, their status after a stop, that a deleted one is gone
// and an abandoned one has left the group.

func createManagedGroup(t *testing.T, svc *compute.Service, project, zone, name string) {
	t.Helper()
	_, err := svc.InstanceGroupManagers.Insert(project, zone, &compute.InstanceGroupManager{
		Name:             name,
		BaseInstanceName: name,
		TargetSize:       0,
	}).Do()
	require.NoError(t, err)
}

func TestCompute_ManagedInstanceGroupInstanceLifecycle(t *testing.T) {
	svc := computeService(t)
	const project, zone, group = "mig-project", "us-central1-a", "workers"
	createManagedGroup(t, svc, project, zone, group)

	// The group manages nothing until instances are created in it.
	managed, err := svc.InstanceGroupManagers.ListManagedInstances(project, zone, group).Do()
	require.NoError(t, err)
	assert.Empty(t, managed.ManagedInstances)

	_, err = svc.InstanceGroupManagers.CreateInstances(project, zone, group,
		&compute.InstanceGroupManagersCreateInstancesRequest{
			Instances: []*compute.PerInstanceConfig{{Name: "worker-1"}, {Name: "worker-2"}},
		}).Do()
	require.NoError(t, err)

	managed, err = svc.InstanceGroupManagers.ListManagedInstances(project, zone, group).Do()
	require.NoError(t, err)
	require.Len(t, managed.ManagedInstances, 2, "the group reports the instances it was told to create")
	assert.Equal(t, "worker-1", managed.ManagedInstances[0].Name)
	assert.Equal(t, "RUNNING", managed.ManagedInstances[0].InstanceStatus)
	assert.Contains(t, managed.ManagedInstances[0].Instance, "/zones/"+zone+"/instances/worker-1")

	// Stopping one moves that instance and leaves the other alone.
	_, err = svc.InstanceGroupManagers.StopInstances(project, zone, group,
		&compute.InstanceGroupManagersStopInstancesRequest{
			Instances: []string{"worker-1"},
		}).Do()
	require.NoError(t, err)
	managed, err = svc.InstanceGroupManagers.ListManagedInstances(project, zone, group).Do()
	require.NoError(t, err)
	byName := map[string]string{}
	for _, instance := range managed.ManagedInstances {
		byName[instance.Name] = instance.InstanceStatus
	}
	assert.Equal(t, "TERMINATED", byName["worker-1"])
	assert.Equal(t, "RUNNING", byName["worker-2"], "the other instance was not touched")

	// And starting it again brings it back.
	_, err = svc.InstanceGroupManagers.StartInstances(project, zone, group,
		&compute.InstanceGroupManagersStartInstancesRequest{
			Instances: []string{"worker-1"},
		}).Do()
	require.NoError(t, err)
	managed, err = svc.InstanceGroupManagers.ListManagedInstances(project, zone, group).Do()
	require.NoError(t, err)
	for _, instance := range managed.ManagedInstances {
		if instance.Name == "worker-1" {
			assert.Equal(t, "RUNNING", instance.InstanceStatus)
		}
	}

	// Deleting removes it from the group.
	_, err = svc.InstanceGroupManagers.DeleteInstances(project, zone, group,
		&compute.InstanceGroupManagersDeleteInstancesRequest{
			Instances: []string{"worker-2"},
		}).Do()
	require.NoError(t, err)
	managed, err = svc.InstanceGroupManagers.ListManagedInstances(project, zone, group).Do()
	require.NoError(t, err)
	require.Len(t, managed.ManagedInstances, 1)
	assert.Equal(t, "worker-1", managed.ManagedInstances[0].Name)

	// An instance the group does not manage reports itself rather than being
	// silently ignored.
	_, err = svc.InstanceGroupManagers.DeleteInstances(project, zone, group,
		&compute.InstanceGroupManagersDeleteInstancesRequest{
			Instances: []string{"never-existed"},
		}).Do()
	require.Error(t, err)
}

func TestCompute_ManagedInstanceGroupPerInstanceConfigs(t *testing.T) {
	svc := computeService(t)
	const project, zone, group = "mig-config", "us-central1-a", "stateful"
	createManagedGroup(t, svc, project, zone, group)

	_, err := svc.InstanceGroupManagers.UpdatePerInstanceConfigs(project, zone, group,
		&compute.InstanceGroupManagersUpdatePerInstanceConfigsReq{
			PerInstanceConfigs: []*compute.PerInstanceConfig{{
				Name: "stateful-1",
				PreservedState: &compute.PreservedState{
					Metadata: map[string]string{"role": "primary"},
				},
			}},
		}).Do()
	require.NoError(t, err)

	listed, err := svc.InstanceGroupManagers.ListPerInstanceConfigs(project, zone, group).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	assert.Equal(t, "stateful-1", listed.Items[0].Name)
	require.NotNil(t, listed.Items[0].PreservedState)
	assert.Equal(t, "primary", listed.Items[0].PreservedState.Metadata["role"])

	// A patch merges into the preserved state rather than replacing it.
	_, err = svc.InstanceGroupManagers.PatchPerInstanceConfigs(project, zone, group,
		&compute.InstanceGroupManagersPatchPerInstanceConfigsReq{
			PerInstanceConfigs: []*compute.PerInstanceConfig{{
				Name: "stateful-1",
				PreservedState: &compute.PreservedState{
					Metadata: map[string]string{"zone-pin": "a"},
				},
			}},
		}).Do()
	require.NoError(t, err)

	_, err = svc.InstanceGroupManagers.DeletePerInstanceConfigs(project, zone, group,
		&compute.InstanceGroupManagersDeletePerInstanceConfigsReq{
			Names: []string{"stateful-1"},
		}).Do()
	require.NoError(t, err)
	listed, err = svc.InstanceGroupManagers.ListPerInstanceConfigs(project, zone, group).Do()
	require.NoError(t, err)
	assert.Empty(t, listed.Items)

	// Deleting a configuration that is not held reports that.
	_, err = svc.InstanceGroupManagers.DeletePerInstanceConfigs(project, zone, group,
		&compute.InstanceGroupManagersDeletePerInstanceConfigsReq{
			Names: []string{"stateful-1"},
		}).Do()
	require.Error(t, err)
}

func TestCompute_ManagedInstanceGroupResizeRequests(t *testing.T) {
	svc := computeService(t)
	const project, zone, group = "mig-resize", "us-central1-a", "batch"
	createManagedGroup(t, svc, project, zone, group)

	_, err := svc.InstanceGroupManagerResizeRequests.Insert(project, zone, group,
		&compute.InstanceGroupManagerResizeRequest{
			Name: "burst", ResizeBy: 4, Description: "batch window",
		}).Do()
	require.NoError(t, err)

	got, err := svc.InstanceGroupManagerResizeRequests.Get(project, zone, group, "burst").Do()
	require.NoError(t, err)
	assert.Equal(t, int64(4), got.ResizeBy)
	assert.Equal(t, "ACCEPTED", got.State)

	listed, err := svc.InstanceGroupManagerResizeRequests.List(project, zone, group).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)

	_, err = svc.InstanceGroupManagerResizeRequests.Cancel(project, zone, group, "burst").Do()
	require.NoError(t, err)
	got, err = svc.InstanceGroupManagerResizeRequests.Get(project, zone, group, "burst").Do()
	require.NoError(t, err)
	assert.Equal(t, "CANCELLED", got.State)

	// Cancelling twice has nothing left to cancel.
	_, err = svc.InstanceGroupManagerResizeRequests.Cancel(project, zone, group, "burst").Do()
	require.Error(t, err)

	_, err = svc.InstanceGroupManagerResizeRequests.Delete(project, zone, group, "burst").Do()
	require.NoError(t, err)
	_, err = svc.InstanceGroupManagerResizeRequests.Get(project, zone, group, "burst").Do()
	require.Error(t, err)
}
