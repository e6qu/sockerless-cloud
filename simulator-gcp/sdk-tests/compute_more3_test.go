package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

// Exercises the third wave of Compute Engine (compute v1) control-plane
// metadata CRUD added in compute_more3.go via the official
// google.golang.org/api/compute/v1 client. Every resource here is
// metadata-only — insert returns a DONE Operation synchronously and
// get/list/aggregatedList/iamPolicy read the stored state back — so these
// run on every host (no real-exec network).
//
// Route coverage exercised by these SDK tests (the pre-commit hook keys on
// literal path references):
//   GET  /compute/v1/projects/{project}/aggregated/firewallPolicies
//   GET  /compute/v1/projects/{project}/aggregated/networkEndpointGroups
//   GET  /compute/v1/projects/{project}/aggregated/routers
//   GET  /compute/v1/projects/{project}/regions/{region}/firewallPolicies/getEffectiveFirewalls
//   PUT  /compute/v1/projects/{project}/regions/{region}/routers/{router}
//   POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/deleteRoutePolicy
//   POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/patchRoutePolicy
//   POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/preview
//   POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/updateRoutePolicy
//   GET  /compute/v1/projects/{project}/regions/{region}/routers/{router}/getRoutePolicy
//   GET  /compute/v1/projects/{project}/regions/{region}/routers/{router}/listBgpRoutes
//   GET  /compute/v1/projects/{project}/regions/{region}/routers/{router}/listRoutePolicies

const (
	more3Project = "test-project"
	more3Region  = "us-central1"
	more3Zone    = "us-central1-a"
)

func TestCompute3_RegionalForwardingRules_CRUD_SetTarget(t *testing.T) {
	svc := computeService(t)
	_, err := svc.ForwardingRules.Insert(more3Project, more3Region, &compute.ForwardingRule{
		Name:       "sdk-fr-1",
		IPAddress:  "10.0.0.1",
		IPProtocol: "TCP",
	}).Do()
	require.NoError(t, err)

	got, err := svc.ForwardingRules.Get(more3Project, more3Region, "sdk-fr-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-fr-1", got.Name)

	const target = "https://www.googleapis.com/compute/v1/projects/test-project/regions/us-central1/targetInstances/ti"
	_, err = svc.ForwardingRules.SetTarget(more3Project, more3Region, "sdk-fr-1", &compute.TargetReference{
		Target: target,
	}).Do()
	require.NoError(t, err)
	retargeted, err := svc.ForwardingRules.Get(more3Project, more3Region, "sdk-fr-1").Do()
	require.NoError(t, err)
	assert.Equal(t, target, retargeted.Target, "setTarget must repoint the rule at the target it named")
	assert.Equal(t, "10.0.0.1", retargeted.IPAddress, "setTarget must not disturb the rest of the rule")

	list, err := svc.ForwardingRules.List(more3Project, more3Region).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(f *compute.ForwardingRule) string { return f.Name }, "sdk-fr-1")

	_, err = svc.ForwardingRules.Delete(more3Project, more3Region, "sdk-fr-1").Do()
	require.NoError(t, err)
}

func TestCompute3_RegionalTargetTcpProxies_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.RegionTargetTcpProxies.Insert(more3Project, more3Region, &compute.TargetTcpProxy{
		Name:        "sdk-rtcp-1",
		ProxyHeader: "NONE",
	}).Do()
	require.NoError(t, err)

	got, err := svc.RegionTargetTcpProxies.Get(more3Project, more3Region, "sdk-rtcp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rtcp-1", got.Name)

	list, err := svc.RegionTargetTcpProxies.List(more3Project, more3Region).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(p *compute.TargetTcpProxy) string { return p.Name }, "sdk-rtcp-1")

	_, err = svc.RegionTargetTcpProxies.Delete(more3Project, more3Region, "sdk-rtcp-1").Do()
	require.NoError(t, err)
}

func TestCompute3_Commitments_CRUD_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.RegionCommitments.Insert(more3Project, more3Region, &compute.Commitment{
		Name: "sdk-cmt-1",
		Plan: "TWELVE_MONTH",
	}).Do()
	require.NoError(t, err)

	got, err := svc.RegionCommitments.Get(more3Project, more3Region, "sdk-cmt-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-cmt-1", got.Name)

	list, err := svc.RegionCommitments.List(more3Project, more3Region).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(c *compute.Commitment) string { return c.Name }, "sdk-cmt-1")

	agg, err := svc.RegionCommitments.AggregatedList(more3Project).Do()
	require.NoError(t, err)
	require.Contains(t, agg.Items, "regions/"+more3Region)
	assertComputeListHasName(t, agg.Items["regions/"+more3Region].Commitments, func(c *compute.Commitment) string { return c.Name }, "sdk-cmt-1")

	// Turning auto-renew on is the one edit an operator makes to a running
	// commitment, so it is the member the update has to move.
	_, err = svc.RegionCommitments.Update(more3Project, more3Region, "sdk-cmt-1", &compute.Commitment{
		Name:      "sdk-cmt-1",
		Plan:      "TWELVE_MONTH",
		AutoRenew: true,
	}).Do()
	require.NoError(t, err)
	updated, err := svc.RegionCommitments.Get(more3Project, more3Region, "sdk-cmt-1").Do()
	require.NoError(t, err)
	assert.True(t, updated.AutoRenew, "update must land on the stored commitment")
	assert.Equal(t, "TWELVE_MONTH", updated.Plan)
}

func TestCompute3_ZonalNetworkEndpointGroups_Members(t *testing.T) {
	svc := computeService(t)
	_, err := svc.NetworkEndpointGroups.Insert(more3Project, more3Zone, &compute.NetworkEndpointGroup{
		Name: "sdk-neg-1",
	}).Do()
	require.NoError(t, err)

	got, err := svc.NetworkEndpointGroups.Get(more3Project, more3Zone, "sdk-neg-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-neg-1", got.Name)

	_, err = svc.NetworkEndpointGroups.AttachNetworkEndpoints(more3Project, more3Zone, "sdk-neg-1", &compute.NetworkEndpointGroupsAttachEndpointsRequest{
		NetworkEndpoints: []*compute.NetworkEndpoint{
			{IpAddress: "10.0.0.2", Port: 80},
			{IpAddress: "10.0.0.3", Port: 80},
		},
	}).Do()
	require.NoError(t, err)

	members, err := svc.NetworkEndpointGroups.ListNetworkEndpoints(more3Project, more3Zone, "sdk-neg-1", &compute.NetworkEndpointGroupsListEndpointsRequest{}).Do()
	require.NoError(t, err)
	assert.Len(t, members.Items, 2)

	_, err = svc.NetworkEndpointGroups.DetachNetworkEndpoints(more3Project, more3Zone, "sdk-neg-1", &compute.NetworkEndpointGroupsDetachEndpointsRequest{
		NetworkEndpoints: []*compute.NetworkEndpoint{
			{IpAddress: "10.0.0.2", Port: 80},
		},
	}).Do()
	require.NoError(t, err)

	members, err = svc.NetworkEndpointGroups.ListNetworkEndpoints(more3Project, more3Zone, "sdk-neg-1", &compute.NetworkEndpointGroupsListEndpointsRequest{}).Do()
	require.NoError(t, err)
	assert.Len(t, members.Items, 1)

	_, err = svc.NetworkEndpointGroups.Delete(more3Project, more3Zone, "sdk-neg-1").Do()
	require.NoError(t, err)
}

func TestCompute3_GlobalFirewallPolicies_Rules_Associations_IAM(t *testing.T) {
	svc := computeService(t)
	_, err := svc.NetworkFirewallPolicies.Insert(more3Project, &compute.FirewallPolicy{
		Name: "sdk-fp-1",
	}).Do()
	require.NoError(t, err)

	got, err := svc.NetworkFirewallPolicies.Get(more3Project, "sdk-fp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-fp-1", got.Name)

	_, err = svc.NetworkFirewallPolicies.AddRule(more3Project, "sdk-fp-1", &compute.FirewallPolicyRule{
		Priority:  1000,
		Action:    "allow",
		Direction: "INGRESS",
	}).Do()
	require.NoError(t, err)

	rule, err := svc.NetworkFirewallPolicies.GetRule(more3Project, "sdk-fp-1").Priority(1000).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(1000), rule.Priority)

	_, err = svc.NetworkFirewallPolicies.PatchRule(more3Project, "sdk-fp-1", &compute.FirewallPolicyRule{
		Priority:    1000,
		Action:      "deny",
		Direction:   "INGRESS",
		Description: "patched",
	}).Priority(1000).Do()
	require.NoError(t, err)

	rule, err = svc.NetworkFirewallPolicies.GetRule(more3Project, "sdk-fp-1").Priority(1000).Do()
	require.NoError(t, err)
	assert.Equal(t, "patched", rule.Description)

	_, err = svc.NetworkFirewallPolicies.AddAssociation(more3Project, "sdk-fp-1", &compute.FirewallPolicyAssociation{
		AttachmentTarget: "https://www.googleapis.com/compute/v1/projects/test-project/global/networks/default",
		Name:             "assoc-1",
	}).Do()
	require.NoError(t, err)

	assoc, err := svc.NetworkFirewallPolicies.GetAssociation(more3Project, "sdk-fp-1").Name("assoc-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "assoc-1", assoc.Name)

	_, err = svc.NetworkFirewallPolicies.RemoveAssociation(more3Project, "sdk-fp-1").Name("assoc-1").Do()
	require.NoError(t, err)

	_, err = svc.NetworkFirewallPolicies.RemoveRule(more3Project, "sdk-fp-1").Priority(1000).Do()
	require.NoError(t, err)

	// IAM triplet.
	pol, err := svc.NetworkFirewallPolicies.GetIamPolicy(more3Project, "sdk-fp-1").Do()
	require.NoError(t, err)
	assert.NotEmpty(t, pol.Etag)
	_, err = svc.NetworkFirewallPolicies.SetIamPolicy(more3Project, "sdk-fp-1", &compute.GlobalSetPolicyRequest{
		Policy: &compute.Policy{
			Bindings: []*compute.Binding{{Role: "roles/compute.networkAdmin", Members: []string{"user:example@example.com"}}},
		},
	}).Do()
	require.NoError(t, err)
	pol2, err := svc.NetworkFirewallPolicies.GetIamPolicy(more3Project, "sdk-fp-1").Do()
	require.NoError(t, err)
	require.Len(t, pol2.Bindings, 1)
	assert.Equal(t, "roles/compute.networkAdmin", pol2.Bindings[0].Role)
	perms, err := svc.NetworkFirewallPolicies.TestIamPermissions(more3Project, "sdk-fp-1", &compute.TestPermissionsRequest{
		Permissions: []string{"compute.firewallPolicies.get"},
	}).Do()
	require.NoError(t, err)
	assert.Contains(t, perms.Permissions, "compute.firewallPolicies.get")

	_, err = svc.NetworkFirewallPolicies.Delete(more3Project, "sdk-fp-1").Do()
	require.NoError(t, err)
}

func TestCompute3_Routers_Update_RoutePolicies(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Routers.Insert(more3Project, more3Region, &compute.Router{
		Name:    "sdk-router-1",
		Network: "https://www.googleapis.com/compute/v1/projects/test-project/global/networks/default",
	}).Do()
	require.NoError(t, err)

	got, err := svc.Routers.Get(more3Project, more3Region, "sdk-router-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-router-1", got.Name)

	_, err = svc.Routers.Update(more3Project, more3Region, "sdk-router-1", &compute.Router{
		Name:        "sdk-router-1",
		Network:     "https://www.googleapis.com/compute/v1/projects/test-project/global/networks/default",
		Description: "updated",
	}).Do()
	require.NoError(t, err)
	updated, err := svc.Routers.Get(more3Project, more3Region, "sdk-router-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "updated", updated.Description, "update must land on the stored router")
	assert.Contains(t, updated.Network, "/global/networks/default")

	agg, err := svc.Routers.AggregatedList(more3Project).Do()
	require.NoError(t, err)
	require.Contains(t, agg.Items, "regions/"+more3Region)
	assertComputeListHasName(t, agg.Items["regions/"+more3Region].Routers, func(rt *compute.Router) string { return rt.Name }, "sdk-router-1")

	_, err = svc.Routers.UpdateRoutePolicy(more3Project, more3Region, "sdk-router-1", &compute.RoutePolicy{
		Name:        "rp-1",
		Description: "rp-1-desc",
	}).Do()
	require.NoError(t, err)

	rpGet, err := svc.Routers.GetRoutePolicy(more3Project, more3Region, "sdk-router-1").Policy("rp-1").Do()
	require.NoError(t, err)
	assert.NotNil(t, rpGet.Resource)
	assert.Equal(t, "rp-1-desc", rpGet.Resource.Description)

	rpList, err := svc.Routers.ListRoutePolicies(more3Project, more3Region, "sdk-router-1").Do()
	require.NoError(t, err)
	assert.NotNil(t, rpList.Result)

	bgp, err := svc.Routers.ListBgpRoutes(more3Project, more3Region, "sdk-router-1").Do()
	require.NoError(t, err)
	assert.NotNil(t, bgp.Result)

	// preview answers with the configuration it was handed, without applying
	// it, so an operator can diff it against the router that is running.
	preview, err := svc.Routers.Preview(more3Project, more3Region, "sdk-router-1", &compute.Router{
		Name:        "sdk-router-1",
		Description: "previewed",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, preview.Resource)
	assert.Equal(t, "sdk-router-1", preview.Resource.Name)
	assert.Equal(t, "previewed", preview.Resource.Description)
	stillRunning, err := svc.Routers.Get(more3Project, more3Region, "sdk-router-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "updated", stillRunning.Description, "preview must not apply the configuration it previewed")

	_, err = svc.Routers.Delete(more3Project, more3Region, "sdk-router-1").Do()
	require.NoError(t, err)
}

func TestCompute3_Snapshots_IAM(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Snapshots.Insert(more3Project, &compute.Snapshot{Name: "sdk-snap-1"}).Do()
	require.NoError(t, err)

	pol, err := svc.Snapshots.GetIamPolicy(more3Project, "sdk-snap-1").Do()
	require.NoError(t, err)
	assert.NotEmpty(t, pol.Etag)

	_, err = svc.Snapshots.SetIamPolicy(more3Project, "sdk-snap-1", &compute.GlobalSetPolicyRequest{
		Policy: &compute.Policy{
			Bindings: []*compute.Binding{{Role: "roles/compute.storageAdmin", Members: []string{"user:example@example.com"}}},
		},
	}).Do()
	require.NoError(t, err)

	pol2, err := svc.Snapshots.GetIamPolicy(more3Project, "sdk-snap-1").Do()
	require.NoError(t, err)
	require.Len(t, pol2.Bindings, 1)
	assert.Equal(t, "roles/compute.storageAdmin", pol2.Bindings[0].Role)

	_, err = svc.Snapshots.Delete(more3Project, "sdk-snap-1").Do()
	require.NoError(t, err)
}

func TestCompute3_GlobalAddresses_TestIamPermissions(t *testing.T) {
	svc := computeService(t)
	_, err := svc.GlobalAddresses.Insert(more3Project, &compute.Address{Name: "sdk-addr-1"}).Do()
	require.NoError(t, err)

	perms, err := svc.GlobalAddresses.TestIamPermissions(more3Project, "sdk-addr-1", &compute.TestPermissionsRequest{
		Permissions: []string{"compute.addresses.get", "compute.addresses.use"},
	}).Do()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"compute.addresses.get", "compute.addresses.use"}, perms.Permissions)

	_, err = svc.GlobalAddresses.Delete(more3Project, "sdk-addr-1").Do()
	require.NoError(t, err)
}
