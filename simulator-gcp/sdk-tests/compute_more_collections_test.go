package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The Compute Engine collections whose whole surface is the standard resource
// lifecycle, driven through the generated client. Each asserts that the
// resource is remembered — created, read back with what was sent, listed, and
// gone after a delete — because a collection that answers without storing
// anything is indistinguishable from one that does until something reads it
// back.
//
// The routes these drive, including the one the global VM extension policy
// departs from the standard lifecycle with:
//
//	POST /compute/v1/projects/{project}/global/vmExtensionPolicies/{name}/delete

func TestCompute_CrossSiteNetworkLifecycle(t *testing.T) {
	svc := computeService(t)
	const project = "xsn-project"

	_, err := svc.CrossSiteNetworks.Insert(project, &compute.CrossSiteNetwork{
		Name: "trunk", Description: "two-site trunk",
	}).Do()
	require.NoError(t, err)

	got, err := svc.CrossSiteNetworks.Get(project, "trunk").Do()
	require.NoError(t, err)
	assert.Equal(t, "trunk", got.Name)
	assert.Equal(t, "two-site trunk", got.Description)
	assert.NotEmpty(t, got.SelfLink, "the resource names itself")

	listed, err := svc.CrossSiteNetworks.List(project).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)

	_, err = svc.CrossSiteNetworks.Delete(project, "trunk").Do()
	require.NoError(t, err)
	_, err = svc.CrossSiteNetworks.Get(project, "trunk").Do()
	require.Error(t, err, "the network was deleted, so reading it must fail")
}

func TestCompute_RegionalSnapshotLifecycle(t *testing.T) {
	svc := computeService(t)
	const project, region = "regional-snap", "us-central1"

	_, err := svc.RegionSnapshots.Insert(project, region, &compute.Snapshot{
		Name: "nightly", Description: "regional snapshot",
	}).Do()
	require.NoError(t, err)

	got, err := svc.RegionSnapshots.Get(project, region, "nightly").Do()
	require.NoError(t, err)
	assert.Equal(t, "nightly", got.Name)
	assert.Contains(t, got.SelfLink, "/regions/"+region+"/snapshots/nightly")

	// A snapshot in another region is a different resource.
	_, err = svc.RegionSnapshots.Get(project, "europe-west1", "nightly").Do()
	require.Error(t, err)

	listed, err := svc.RegionSnapshots.List(project, region).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
}

func TestCompute_RegionalBackendBucketCarriesItsUsableSubset(t *testing.T) {
	svc := computeService(t)
	const project, region = "regional-bb", "us-central1"

	_, err := svc.RegionBackendBuckets.Insert(project, region, &compute.BackendBucket{
		Name: "assets", BucketName: "static-assets",
	}).Do()
	require.NoError(t, err)

	usable, err := svc.RegionBackendBuckets.ListUsable(project, region).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#usableBackendBucketList", usable.Kind)
	require.Len(t, usable.Items, 1)
	assert.Equal(t, "assets", usable.Items[0].Name)
}

func TestCompute_InterconnectAndItsGroups(t *testing.T) {
	svc := computeService(t)
	const project = "interconnect-project"

	_, err := svc.Interconnects.Insert(project, &compute.Interconnect{
		Name: "link-1", Location: "iad-zone1-1", InterconnectType: "DEDICATED",
	}).Do()
	require.NoError(t, err)
	link, err := svc.Interconnects.Get(project, "link-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "DEDICATED", link.InterconnectType)

	// The MACsec configuration is the caller's own keychain read back, with the
	// key name and key the service generates for each entry. An interconnect
	// configured with none has an empty keychain rather than no answer.
	empty, err := svc.Interconnects.GetMacsecConfig(project, "link-1").Do()
	require.NoError(t, err)
	require.NotNil(t, empty.Result)
	assert.Empty(t, empty.Result.PreSharedKeys)

	_, err = svc.Interconnects.Patch(project, "link-1", &compute.Interconnect{
		Macsec: &compute.InterconnectMacsec{
			PreSharedKeys: []*compute.InterconnectMacsecPreSharedKey{
				{Name: "primary", StartTime: "2026-01-01T00:00:00Z"},
				{Name: "standby", StartTime: "2026-07-01T00:00:00Z"},
			},
		},
	}).Do()
	require.NoError(t, err)

	configured, err := svc.Interconnects.GetMacsecConfig(project, "link-1").Do()
	require.NoError(t, err)
	require.Len(t, configured.Result.PreSharedKeys, 2)
	assert.Equal(t, "primary", configured.Result.PreSharedKeys[0].Name)
	assert.Equal(t, "2026-01-01T00:00:00Z", configured.Result.PreSharedKeys[0].StartTime)
	// A CKN is 32 bytes and a CAK 16, both hex, and the two keys differ.
	assert.Len(t, configured.Result.PreSharedKeys[0].Ckn, 64)
	assert.Len(t, configured.Result.PreSharedKeys[0].Cak, 32)
	assert.NotEqual(t, configured.Result.PreSharedKeys[0].Ckn,
		configured.Result.PreSharedKeys[1].Ckn, "each key gets its own")

	// Reading it twice returns the same keychain: a caller configuring a router
	// from it cannot be handed a different key each time.
	again, err := svc.Interconnects.GetMacsecConfig(project, "link-1").Do()
	require.NoError(t, err)
	assert.Equal(t, configured.Result.PreSharedKeys[0].Cak, again.Result.PreSharedKeys[0].Cak)

	// An interconnect that is not there is a 404, not an empty keychain.
	_, err = svc.Interconnects.GetMacsecConfig(project, "absent-link").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = svc.InterconnectGroups.Insert(project, &compute.InterconnectGroup{
		Name: "bundle", Description: "the pair that carries the trunk",
	}).Do()
	require.NoError(t, err)
	group, err := svc.InterconnectGroups.Get(project, "bundle").Do()
	require.NoError(t, err)
	assert.Equal(t, "the pair that carries the trunk", group.Description)

	// The group's IAM policy is readable and writable, which the document
	// declares for this collection and not for every one beside it.
	_, err = svc.InterconnectGroups.SetIamPolicy(project, "bundle", &compute.GlobalSetPolicyRequest{
		Policy: &compute.Policy{Bindings: []*compute.Binding{{
			Role: "roles/compute.viewer", Members: []string{"user:reader@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	policy, err := svc.InterconnectGroups.GetIamPolicy(project, "bundle").Do()
	require.NoError(t, err)
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, "roles/compute.viewer", policy.Bindings[0].Role)
}

func TestCompute_RegionalHealthSourceAndCompositeCheck(t *testing.T) {
	svc := computeService(t)
	const project, region = "health-project", "us-central1"

	_, err := svc.RegionHealthSources.Insert(project, region, &compute.HealthSource{
		Name: "source-1", Description: "backend health",
	}).Do()
	require.NoError(t, err)
	source, err := svc.RegionHealthSources.Get(project, region, "source-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "backend health", source.Description)

	_, err = svc.RegionCompositeHealthChecks.Insert(project, region, &compute.CompositeHealthCheck{
		Name: "composite-1",
	}).Do()
	require.NoError(t, err)

	// The aggregated read spans the project's regions.
	aggregated, err := svc.RegionHealthSources.AggregatedList(project).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, aggregated.Items)
}

func TestCompute_VmExtensionPolicyZonalAndGlobal(t *testing.T) {
	svc := computeService(t)
	const project, zone = "vmext-project", "us-central1-a"

	_, err := svc.ZoneVmExtensionPolicies.Insert(project, zone, &compute.VmExtensionPolicy{
		Name: "zonal-policy", Description: "zone scoped",
	}).Do()
	require.NoError(t, err)
	zonal, err := svc.ZoneVmExtensionPolicies.Get(project, zone, "zonal-policy").Do()
	require.NoError(t, err)
	assert.Equal(t, "zone scoped", zonal.Description)

	_, err = svc.GlobalVmExtensionPolicies.Insert(project, &compute.GlobalVmExtensionPolicy{
		Name: "global-policy", Description: "project wide",
	}).Do()
	require.NoError(t, err)
	global, err := svc.GlobalVmExtensionPolicies.Get(project, "global-policy").Do()
	require.NoError(t, err)
	assert.Equal(t, "project wide", global.Description)

	// The global collection retires a policy through a POST rather than a
	// DELETE, which is the one place it departs from the standard lifecycle.
	_, err = svc.GlobalVmExtensionPolicies.Delete(project, "global-policy",
		&compute.GlobalVmExtensionPolicyRolloutOperationRolloutInput{}).Do()
	require.NoError(t, err)
	_, err = svc.GlobalVmExtensionPolicies.Get(project, "global-policy").Do()
	require.Error(t, err, "the policy was retired, so reading it must fail")
}

// A project's enrolment in a Compute Engine preview feature. Which features
// exist is Google's to say and is not vendored here; what this project has done
// about one is the caller's own.
func TestCompute_PreviewFeatureEnrolment(t *testing.T) {
	svc := computeService(t)
	const project = "preview-project"

	// Nothing has been said about any feature yet.
	empty, err := svc.PreviewFeatures.List(project).Do()
	require.NoError(t, err)
	assert.Empty(t, empty.Items)

	_, err = svc.PreviewFeatures.Get(project, "unspoken-for").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	op, err := svc.PreviewFeatures.Update(project, "beta-networking",
		&compute.PreviewFeature{ActivationStatus: "ENABLED"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "update", op.OperationType)

	got, err := svc.PreviewFeatures.Get(project, "beta-networking").Do()
	require.NoError(t, err)
	assert.Equal(t, "ENABLED", got.ActivationStatus)
	assert.Equal(t, "beta-networking", got.Name)
	// The description is Google's account of the feature, and this simulator
	// does not vendor the catalogue it comes from.
	assert.Empty(t, got.Description)

	// Turning it off is the same write, and the read follows it.
	_, err = svc.PreviewFeatures.Update(project, "beta-networking",
		&compute.PreviewFeature{ActivationStatus: "DISABLED"}).Do()
	require.NoError(t, err)
	off, err := svc.PreviewFeatures.Get(project, "beta-networking").Do()
	require.NoError(t, err)
	assert.Equal(t, "DISABLED", off.ActivationStatus)

	listed, err := svc.PreviewFeatures.List(project).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1, "the features this project has spoken for")
	assert.Equal(t, "beta-networking", listed.Items[0].Name)

	// Another project's enrolment is its own.
	other, err := svc.PreviewFeatures.List("preview-other-project").Do()
	require.NoError(t, err)
	assert.Empty(t, other.Items)
}

// The policy a project puts on a licence code. The code identifies an image
// Google publishes and reading it means reading that catalogue, but the binding
// on it is the caller's own.
func TestCompute_LicenceCodePolicy(t *testing.T) {
	svc := computeService(t)
	const project, code = "licence-project", "1000205"

	// Reading the code itself still means reading Google's catalogue.
	_, err := svc.LicenseCodes.Get(project, code).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catalogue is Google's own")

	set, err := svc.LicenseCodes.SetIamPolicy(project, code, &compute.GlobalSetPolicyRequest{
		Policy: &compute.Policy{
			Bindings: []*compute.Binding{
				{Role: "roles/compute.imageUser", Members: []string{"user:builder@example.com"}},
			},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	got, err := svc.LicenseCodes.GetIamPolicy(project, code).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	assert.Equal(t, "roles/compute.imageUser", got.Bindings[0].Role)
	assert.Equal(t, []string{"user:builder@example.com"}, got.Bindings[0].Members)

	perms, err := svc.LicenseCodes.TestIamPermissions(project, code,
		&compute.TestPermissionsRequest{
			Permissions: []string{"compute.licenseCodes.use"},
		}).Do()
	require.NoError(t, err)
	require.NotNil(t, perms)

	// A code nobody has bound has no policy of its own.
	empty, err := svc.LicenseCodes.GetIamPolicy(project, "1000999").Do()
	require.NoError(t, err)
	assert.Empty(t, empty.Bindings)
}
