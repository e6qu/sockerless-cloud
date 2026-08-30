package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The Compute Engine verbs that manage a membership: a target pool's instances
// and health checks, a network's peerings, and a sole-tenant node group's
// nodes. Each writes a list the resource carries, so the read beside it is what
// proves the write landed.

func TestCompute_TargetPoolMembership(t *testing.T) {
	svc := computeService(t)
	const project, region, pool = "pool-project", "us-central1", "web"

	_, err := svc.TargetPools.Insert(project, region, &compute.TargetPool{Name: pool}).Do()
	require.NoError(t, err)

	const instance = "https://www.googleapis.com/compute/v1/projects/pool-project/zones/us-central1-a/instances/web-1"
	_, err = svc.TargetPools.AddInstance(project, region, pool,
		&compute.TargetPoolsAddInstanceRequest{
			Instances: []*compute.InstanceReference{{Instance: instance}},
		}).Do()
	require.NoError(t, err)

	got, err := svc.TargetPools.Get(project, region, pool).Do()
	require.NoError(t, err)
	require.Len(t, got.Instances, 1)
	assert.Equal(t, instance, got.Instances[0])

	// The pool reports health for an instance it holds.
	health, err := svc.TargetPools.GetHealth(project, region, pool,
		&compute.InstanceReference{Instance: instance}).Do()
	require.NoError(t, err)
	require.Len(t, health.HealthStatus, 1)
	assert.Equal(t, "HEALTHY", health.HealthStatus[0].HealthState)

	// And refuses one it does not.
	_, err = svc.TargetPools.GetHealth(project, region, pool,
		&compute.InstanceReference{Instance: instance + "-other"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in this target pool")

	// Adding the same instance twice is refused rather than doubled.
	_, err = svc.TargetPools.AddInstance(project, region, pool,
		&compute.TargetPoolsAddInstanceRequest{
			Instances: []*compute.InstanceReference{{Instance: instance}},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in this target pool")

	_, err = svc.TargetPools.RemoveInstance(project, region, pool,
		&compute.TargetPoolsRemoveInstanceRequest{
			Instances: []*compute.InstanceReference{{Instance: instance}},
		}).Do()
	require.NoError(t, err)
	got, err = svc.TargetPools.Get(project, region, pool).Do()
	require.NoError(t, err)
	assert.Empty(t, got.Instances)
}

func TestCompute_NetworkPeering(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project = "peering-project"

	for _, name := range []string{"left", "right"} {
		_, err := svc.Networks.Insert(project, &compute.Network{
			Name: name, AutoCreateSubnetworks: true,
		}).Do()
		require.NoError(t, err)
	}
	right, err := svc.Networks.Get(project, "right").Do()
	require.NoError(t, err)

	// The first side is inactive: nothing has peered back yet.
	_, err = svc.Networks.AddPeering(project, "left", &compute.NetworksAddPeeringRequest{
		NetworkPeering: &compute.NetworkPeering{
			Name: "to-right", Network: right.SelfLink, ExchangeSubnetRoutes: true,
		},
	}).Do()
	require.NoError(t, err)

	left, err := svc.Networks.Get(project, "left").Do()
	require.NoError(t, err)
	require.Len(t, left.Peerings, 1)
	assert.Equal(t, "INACTIVE", left.Peerings[0].State)

	// Peering back brings the second side up.
	_, err = svc.Networks.AddPeering(project, "right", &compute.NetworksAddPeeringRequest{
		NetworkPeering: &compute.NetworkPeering{
			Name: "to-left", Network: left.SelfLink, ExchangeSubnetRoutes: true,
		},
	}).Do()
	require.NoError(t, err)
	right, err = svc.Networks.Get(project, "right").Do()
	require.NoError(t, err)
	require.Len(t, right.Peerings, 1)
	assert.Equal(t, "ACTIVE", right.Peerings[0].State)

	_, err = svc.Networks.RemovePeering(project, "left", &compute.NetworksRemovePeeringRequest{
		Name: "to-right",
	}).Do()
	require.NoError(t, err)
	left, err = svc.Networks.Get(project, "left").Do()
	require.NoError(t, err)
	assert.Empty(t, left.Peerings)

	// Removing one that is not there reports itself.
	_, err = svc.Networks.RemovePeering(project, "left", &compute.NetworksRemovePeeringRequest{
		Name: "to-right",
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no peering named")
}

func TestCompute_NetworkSwitchToCustomMode(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, name = "custom-mode", "auto"

	_, err := svc.Networks.Insert(project, &compute.Network{
		Name: name, AutoCreateSubnetworks: true,
	}).Do()
	require.NoError(t, err)

	_, err = svc.Networks.SwitchToCustomMode(project, name).Do()
	require.NoError(t, err)
	got, err := svc.Networks.Get(project, name).Do()
	require.NoError(t, err)
	assert.False(t, got.AutoCreateSubnetworks, "the network is in custom subnet mode now")

	// Switching a custom-mode network again has nothing to switch.
	_, err = svc.Networks.SwitchToCustomMode(project, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in custom subnet mode")
}

func TestCompute_NodeGroupNodes(t *testing.T) {
	svc := computeService(t)
	const project, zone, group = "node-project", "us-central1-a", "tenant"

	_, err := svc.NodeGroups.Insert(project, zone, 0, &compute.NodeGroup{
		Name: group, NodeTemplate: "regions/us-central1/nodeTemplates/base",
	}).Do()
	require.NoError(t, err)

	_, err = svc.NodeGroups.AddNodes(project, zone, group,
		&compute.NodeGroupsAddNodesRequest{AdditionalNodeCount: 2}).Do()
	require.NoError(t, err)

	listed, err := svc.NodeGroups.ListNodes(project, zone, group).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 2, "the group reports the nodes it was told to add")
	first := listed.Items[0].Name

	_, err = svc.NodeGroups.DeleteNodes(project, zone, group,
		&compute.NodeGroupsDeleteNodesRequest{Nodes: []string{first}}).Do()
	require.NoError(t, err)
	listed, err = svc.NodeGroups.ListNodes(project, zone, group).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)

	// A node the group does not hold reports itself.
	_, err = svc.NodeGroups.DeleteNodes(project, zone, group,
		&compute.NodeGroupsDeleteNodesRequest{Nodes: []string{first}}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no node named")

	// Adding a non-positive count is refused.
	_, err = svc.NodeGroups.AddNodes(project, zone, group,
		&compute.NodeGroupsAddNodesRequest{AdditionalNodeCount: 0}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "greater than zero")
}
