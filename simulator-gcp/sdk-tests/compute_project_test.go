package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The Compute Engine project resource, the verbs that write it, and Shared VPC.

func TestCompute_ProjectDefaultsAndSetVerbs(t *testing.T) {
	svc := computeService(t)
	const project = "project-verbs"

	// A project is not created through Compute Engine, so a read before any
	// write is the defaults rather than a 404.
	got, err := svc.Projects.Get(project).Do()
	require.NoError(t, err)
	assert.Equal(t, project, got.Name)
	assert.Equal(t, "PREMIUM", got.DefaultNetworkTier)
	assert.Equal(t, "CA_STANDARD", got.CloudArmorTier)

	_, err = svc.Projects.SetDefaultNetworkTier(project,
		&compute.ProjectsSetDefaultNetworkTierRequest{NetworkTier: "STANDARD"}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.SetCloudArmorTier(project,
		&compute.ProjectsSetCloudArmorTierRequest{CloudArmorTier: "CA_ENTERPRISE_PAYGO"}).Do()
	require.NoError(t, err)

	got, err = svc.Projects.Get(project).Do()
	require.NoError(t, err)
	assert.Equal(t, "STANDARD", got.DefaultNetworkTier)
	assert.Equal(t, "CA_ENTERPRISE_PAYGO", got.CloudArmorTier)

	// A body without the member the verb sets is refused rather than storing
	// an empty one.
	_, err = svc.Projects.SetDefaultNetworkTier(project,
		&compute.ProjectsSetDefaultNetworkTierRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "networkTier")
}

func TestCompute_ProjectCommonMetadataAndUsageExport(t *testing.T) {
	svc := computeService(t)
	const project = "project-metadata"

	_, err := svc.Projects.SetCommonInstanceMetadata(project, &compute.Metadata{
		Items: []*compute.MetadataItems{{Key: "ssh-keys", Value: stringPtr("ops:ssh-rsa AAAA")}},
	}).Do()
	require.NoError(t, err)

	got, err := svc.Projects.Get(project).Do()
	require.NoError(t, err)
	require.NotNil(t, got.CommonInstanceMetadata)
	require.Len(t, got.CommonInstanceMetadata.Items, 1)
	assert.Equal(t, "ssh-keys", got.CommonInstanceMetadata.Items[0].Key)
	assert.NotEmpty(t, got.CommonInstanceMetadata.Fingerprint,
		"project metadata carries a fingerprint, as an instance's does")

	_, err = svc.Projects.SetUsageExportBucket(project, &compute.UsageExportLocation{
		BucketName: "gs://usage-reports", ReportNamePrefix: "compute",
	}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Get(project).Do()
	require.NoError(t, err)
	require.NotNil(t, got.UsageExportLocation)
	assert.Equal(t, "gs://usage-reports", got.UsageExportLocation.BucketName)

	// An empty bucket turns reporting off, which is how the API says it.
	_, err = svc.Projects.SetUsageExportBucket(project, &compute.UsageExportLocation{}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Get(project).Do()
	require.NoError(t, err)
	assert.Nil(t, got.UsageExportLocation)
}

func TestCompute_SharedVPCHostAndServiceProjects(t *testing.T) {
	svc := computeService(t)
	const host, service = "xpn-host", "xpn-service"

	// Attaching a service project to a project that is not a host is refused.
	_, err := svc.Projects.EnableXpnResource(host, &compute.ProjectsEnableXpnResourceRequest{
		XpnResource: &compute.XpnResourceId{Id: service, Type: "PROJECT"},
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a Shared VPC host")

	_, err = svc.Projects.EnableXpnHost(host).Do()
	require.NoError(t, err)
	_, err = svc.Projects.EnableXpnResource(host, &compute.ProjectsEnableXpnResourceRequest{
		XpnResource: &compute.XpnResourceId{Id: service, Type: "PROJECT"},
	}).Do()
	require.NoError(t, err)

	// The host reports what it lends to, and the service project reports the
	// host it borrows from — the same association read from both ends.
	resources, err := svc.Projects.GetXpnResources(host).Do()
	require.NoError(t, err)
	require.Len(t, resources.Resources, 1)
	assert.Equal(t, service, resources.Resources[0].Id)

	borrowed, err := svc.Projects.GetXpnHost(service).Do()
	require.NoError(t, err)
	assert.Equal(t, host, borrowed.Name)

	hosts, err := svc.Projects.ListXpnHosts(host, &compute.ProjectsListXpnHostsRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, hosts.Items)

	// Detaching one that is not attached says so.
	_, err = svc.Projects.DisableXpnResource(host, &compute.ProjectsDisableXpnResourceRequest{
		XpnResource: &compute.XpnResourceId{Id: "never-attached", Type: "PROJECT"},
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not attached")

	_, err = svc.Projects.DisableXpnResource(host, &compute.ProjectsDisableXpnResourceRequest{
		XpnResource: &compute.XpnResourceId{Id: service, Type: "PROJECT"},
	}).Do()
	require.NoError(t, err)
	resources, err = svc.Projects.GetXpnResources(host).Do()
	require.NoError(t, err)
	assert.Empty(t, resources.Resources)

	// A project that has stopped being a host lends to nobody.
	_, err = svc.Projects.DisableXpnHost(host).Do()
	require.NoError(t, err)
	got, err := svc.Projects.Get(host).Do()
	require.NoError(t, err)
	assert.NotEqual(t, "HOST", got.XpnProjectStatus)
}

// The surfaces that would have to be invented to be served answer with the
// reason instead. An empty list would be a false statement about Google's own
// catalogue, and a client cannot tell one from a real answer.
//
// interconnectLocations was here and is not any more: Google publishes the
// catalogue and it is vendored from that publication, so it is served — see
// TestCompute_InterconnectLocationsAreTheVendoredCatalog, which also holds the
// fields the page does not state to being absent.
//
// interconnects.getDiagnostics was here and is not any more: most of what it
// reports is on the interconnect's own record, and it is served from there —
// see TestCompute_InterconnectDiagnosticsComeFromTheInterconnect, which also
// holds the measurements that genuinely are off the equipment to being absent.
func TestCompute_GooglePublishedCatalogsAreDeclaredGaps(t *testing.T) {
	svc := computeService(t)
	const project = "catalog-project"

	for _, read := range []struct {
		what string
		call func() error
	}{
		{"interconnect remote locations", func() error {
			_, err := svc.InterconnectRemoteLocations.List(project).Do()
			return err
		}},
	} {
		err := read.call()
		require.Error(t, err, "%s must not answer with an invented catalogue", read.what)
		assert.Contains(t, err.Error(), "501")
		assert.Contains(t, err.Error(), "catalogue is Google's own")
	}
}

// A move re-homes a resource onto another zone. It is the same resource
// afterwards — same name, same contents — so it has to be gone from the zone it
// left, which is the half a re-create would get wrong.
func TestCompute_MoveDiskBetweenZones(t *testing.T) {
	svc := computeService(t)
	const project, from, to, name = "move-project", "us-central1-a", "us-central1-b", "workspace"

	_, err := svc.Disks.Insert(project, from, &compute.Disk{
		Name: name, SizeGb: 25, Type: "zones/" + from + "/diskTypes/pd-balanced",
	}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.MoveDisk(project, &compute.DiskMoveRequest{
		TargetDisk:      "projects/" + project + "/zones/" + from + "/disks/" + name,
		DestinationZone: "projects/" + project + "/zones/" + to,
	}).Do()
	require.NoError(t, err)

	moved, err := svc.Disks.Get(project, to, name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(25), moved.SizeGb, "the disk is the same disk in its new zone")

	_, err = svc.Disks.Get(project, from, name).Do()
	require.Error(t, err, "the disk left the zone it was moved out of")

	// Moving it where it already is says so rather than doing nothing.
	_, err = svc.Projects.MoveDisk(project, &compute.DiskMoveRequest{
		TargetDisk:      "projects/" + project + "/zones/" + to + "/disks/" + name,
		DestinationZone: "projects/" + project + "/zones/" + to,
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in zone")

	// And a disk that is not there cannot be moved.
	_, err = svc.Projects.MoveDisk(project, &compute.DiskMoveRequest{
		TargetDisk:      "projects/" + project + "/zones/" + from + "/disks/absent",
		DestinationZone: "projects/" + project + "/zones/" + to,
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCompute_MoveInstanceBetweenZones(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, from, to, name = "move-instance", "us-central1-a", "us-central1-b", "worker"
	createVerbInstance(t, svc, project, from, name)

	_, err := svc.Projects.MoveInstance(project, &compute.InstanceMoveRequest{
		TargetInstance:  "projects/" + project + "/zones/" + from + "/instances/" + name,
		DestinationZone: "projects/" + project + "/zones/" + to,
	}).Do()
	require.NoError(t, err)

	moved, err := svc.Instances.Get(project, to, name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, moved.Name)

	_, err = svc.Instances.Get(project, from, name).Do()
	require.Error(t, err, "the instance left the zone it was moved out of")
}

func stringPtr(s string) *string { return &s }
