package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The IAM verbs Compute Engine mounts beneath a resource. Most collections
// declare only the permission check; a few carry the whole triple, and those
// are the ones where a set can be read back.

func TestCompute_TestIamPermissionsAcrossCollections(t *testing.T) {
	svc := computeService(t)
	const project, region, zone = "iam-verbs", "us-central1", "us-central1-a"
	want := &compute.TestPermissionsRequest{Permissions: []string{"compute.instances.get"}}

	// A permission check answers for the resource it names, whatever scope the
	// collection sits in.
	global, err := svc.TargetSslProxies.TestIamPermissions(project, "proxy", want).Do()
	require.NoError(t, err)
	require.NotNil(t, global)

	regional, err := svc.NodeTemplates.TestIamPermissions(project, region, "template", want).Do()
	require.NoError(t, err)
	require.NotNil(t, regional)

	zonal, err := svc.TargetInstances.TestIamPermissions(project, zone, "target", want).Do()
	require.NoError(t, err)
	require.NotNil(t, zonal)

	// The health checks, autoscalers and endpoint groups declare the same
	// check, and each answers on its own path.
	_, err = svc.HttpHealthChecks.TestIamPermissions(project, "http-check", want).Do()
	require.NoError(t, err)
	_, err = svc.Autoscalers.TestIamPermissions(project, zone, "scaler", want).Do()
	require.NoError(t, err)
	_, err = svc.NetworkEndpointGroups.TestIamPermissions(project, zone, "neg", want).Do()
	require.NoError(t, err)
	_, err = svc.VpnGateways.TestIamPermissions(project, region, "gateway", want).Do()
	require.NoError(t, err)
}

func TestCompute_NodeTemplateIamPolicyRoundTrips(t *testing.T) {
	svc := computeService(t)
	const project, region, name = "iam-templates", "us-central1", "tenant"

	_, err := svc.NodeTemplates.SetIamPolicy(project, region, name,
		&compute.RegionSetPolicyRequest{Policy: &compute.Policy{
			Bindings: []*compute.Binding{{
				Role: "roles/compute.admin", Members: []string{"user:admin@example.com"},
			}},
		}}).Do()
	require.NoError(t, err)

	policy, err := svc.NodeTemplates.GetIamPolicy(project, region, name).Do()
	require.NoError(t, err)
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, "roles/compute.admin", policy.Bindings[0].Role)
	assert.Equal(t, []string{"user:admin@example.com"}, policy.Bindings[0].Members)
}

// An image's family lookup and its IAM policy read are one shape to a path
// router, so both go through a single handler. Each still has to answer as its
// own method, and a tail that is neither has to be refused.
func TestCompute_ImageFamilyAndIamShareARouteWithoutShadowingEachOther(t *testing.T) {
	svc := computeService(t)
	const project, name = "iam-images", "golden"

	_, err := svc.Images.SetIamPolicy(project, name, &compute.GlobalSetPolicyRequest{
		Policy: &compute.Policy{Bindings: []*compute.Binding{{
			Role: "roles/compute.imageUser", Members: []string{"user:reader@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)

	policy, err := svc.Images.GetIamPolicy(project, name).Do()
	require.NoError(t, err)
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, "roles/compute.imageUser", policy.Bindings[0].Role)

	// The family lookup still answers on the same two-segment shape.
	family, err := svc.Images.GetFromFamily(project, "debian").Do()
	require.NoError(t, err)
	assert.Contains(t, family.Name, "debian")

	// And the permission check, which is a POST and so unambiguous.
	_, err = svc.Images.TestIamPermissions(project, name,
		&compute.TestPermissionsRequest{Permissions: []string{"compute.images.get"}}).Do()
	require.NoError(t, err)
}
