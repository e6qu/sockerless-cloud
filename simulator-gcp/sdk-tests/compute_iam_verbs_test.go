package gcp_sdk_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

// testIamPermissions answers the permissions the caller holds on the resource
// under its own policy and the policies it inherits, never the set it was
// asked about.
func TestCompute_TestIamPermissionsAnswersFromThePolicy(t *testing.T) {
	t.Parallel()
	const project = "iam-verbs-held"
	ensureProject(t, project)
	owner := computeService(t)
	_, err := owner.BackendBuckets.Insert(project, &compute.BackendBucket{Name: "held-bb", BucketName: "held-bb-bucket"}).Do()
	require.NoError(t, err)
	_, err = owner.GlobalAddresses.Insert(project, &compute.Address{Name: "held-addr"}).Do()
	require.NoError(t, err)

	_, _, heldKey := mintServiceAccountKeyFile(t, "compute-held-sa")
	_, _, unboundKey := mintServiceAccountKeyFile(t, "compute-unbound-sa")
	const heldMember = "serviceAccount:compute-held-sa@test-project.iam.gserviceaccount.com"
	_, err = owner.BackendBuckets.SetIamPolicy(project, "held-bb", &compute.GlobalSetPolicyRequest{
		Policy: &compute.Policy{Bindings: []*compute.Binding{{Role: "roles/compute.viewer", Members: []string{heldMember}}}},
	}).Do()
	require.NoError(t, err)

	as := func(keyFile map[string]any) *compute.Service {
		svc, err := compute.NewService(ctx, option.WithEndpoint(baseURL+"/compute/v1/"),
			option.WithTokenSource(tokenSourceFromKeyFile(t, keyFile).TokenSource))
		require.NoError(t, err)
		return svc
	}
	asked := &compute.TestPermissionsRequest{Permissions: []string{"compute.backendBuckets.get", "compute.backendBuckets.delete"}}

	held, err := as(heldKey).BackendBuckets.TestIamPermissions(project, "held-bb", asked).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"compute.backendBuckets.get"}, held.Permissions,
		"Compute Viewer on the backend bucket grants the read and not the delete")

	none, err := as(unboundKey).BackendBuckets.TestIamPermissions(project, "held-bb", asked).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Permissions, "a caller no binding names holds nothing")

	// A collection that declares only the permission check answers the same way.
	addressAsk := &compute.TestPermissionsRequest{Permissions: []string{"compute.addresses.get"}}
	unboundAddress, err := as(unboundKey).GlobalAddresses.TestIamPermissions(project, "held-addr", addressAsk).Do()
	require.NoError(t, err)
	assert.Empty(t, unboundAddress.Permissions)
	ownerAddress, err := owner.GlobalAddresses.TestIamPermissions(project, "held-addr", addressAsk).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"compute.addresses.get"}, ownerAddress.Permissions,
		"the bootstrap account holds Owner through the organization")
}

// The IAM verbs Compute Engine mounts beneath a resource. Most collections
// declare only the permission check; a few carry the whole triple, and those
// are the ones where a set can be read back.

func TestCompute_TestIamPermissionsAcrossCollections(t *testing.T) {
	t.Parallel()
	const project, region, zone = "iam-verbs", "us-central1", "us-central1-a"
	ensureProject(t, project)
	svc := computeService(t)
	want := &compute.TestPermissionsRequest{Permissions: []string{"compute.instances.get"}}

	_, err := svc.TargetSslProxies.Insert(project, &compute.TargetSslProxy{Name: "proxy"}).Do()
	require.NoError(t, err)
	_, err = svc.NodeTemplates.Insert(project, region, &compute.NodeTemplate{Name: "template"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetInstances.Insert(project, zone, &compute.TargetInstance{Name: "target"}).Do()
	require.NoError(t, err)
	_, err = svc.HttpHealthChecks.Insert(project, &compute.HttpHealthCheck{Name: "http-check"}).Do()
	require.NoError(t, err)
	_, err = svc.Autoscalers.Insert(project, zone, &compute.Autoscaler{Name: "scaler"}).Do()
	require.NoError(t, err)
	_, err = svc.NetworkEndpointGroups.Insert(project, zone, &compute.NetworkEndpointGroup{Name: "neg"}).Do()
	require.NoError(t, err)
	_, err = svc.VpnGateways.Insert(project, region, &compute.VpnGateway{Name: "gateway"}).Do()
	require.NoError(t, err)

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

	// A resource never created answers the 404 its collection's get answers.
	_, missingGet := svc.TargetSslProxies.Get(project, "never-created").Do()
	requireGoogleAPICode(t, missingGet, http.StatusNotFound)
	_, missingCheck := svc.TargetSslProxies.TestIamPermissions(project, "never-created", want).Do()
	requireGoogleAPICode(t, missingCheck, http.StatusNotFound)
	assert.Equal(t, missingGet.Error(), missingCheck.Error())
	_, err = svc.NodeTemplates.SetIamPolicy(project, region, "never-created",
		&compute.RegionSetPolicyRequest{Policy: &compute.Policy{}}).Do()
	requireGoogleAPICode(t, err, http.StatusNotFound)
	_, err = svc.NodeTemplates.GetIamPolicy(project, region, "never-created").Do()
	requireGoogleAPICode(t, err, http.StatusNotFound)
}

func TestCompute_NodeTemplateIamPolicyRoundTrips(t *testing.T) {
	t.Parallel()
	const project, region, name = "iam-templates", "us-central1", "tenant"
	ensureProject(t, project)
	svc := computeService(t)
	_, err := svc.NodeTemplates.Insert(project, region, &compute.NodeTemplate{Name: name}).Do()
	require.NoError(t, err)

	_, err = svc.NodeTemplates.SetIamPolicy(project, region, name,
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
	t.Parallel()
	const project, name = "iam-images", "golden"
	ensureProject(t, project)
	svc := computeService(t)
	_, err := svc.Images.Insert(project, &compute.Image{
		Name: name, SourceImage: "projects/debian-cloud/global/images/family/debian-12",
	}).Do()
	require.NoError(t, err)

	_, err = svc.Images.SetIamPolicy(project, name, &compute.GlobalSetPolicyRequest{
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
	family, err := svc.Images.GetFromFamily("debian-cloud", "debian-12").Do()
	require.NoError(t, err)
	assert.Contains(t, family.Name, "debian-12")

	// And the permission check, which is a POST and so unambiguous.
	_, err = svc.Images.TestIamPermissions(project, name,
		&compute.TestPermissionsRequest{Permissions: []string{"compute.images.get"}}).Do()
	require.NoError(t, err)

	_, err = svc.Images.GetIamPolicy(project, "never-created").Do()
	requireGoogleAPICode(t, err, http.StatusNotFound)
}
