package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

// Exercises the Compute Engine (compute v1) control-plane CRUD surface
// added in compute_more.go via the official google.golang.org/api/compute/v1
// client. Every resource here is metadata-only — insert returns an
// Operation (DONE synchronously) and get/list/aggregatedList read the
// stored resource back — so these run on every host (no real-exec network).
//
// The catalog/operations/aggregated/instance-action handlers are mounted at
// these literal wire routes (the SDK calls below invoke them by method name):
//
//	GET    /compute/v1/projects/{project}/regions
//	GET    /compute/v1/projects/{project}/regions/{region}
//	GET    /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes
//	GET    /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes/{acceleratorType}
//	GET    /compute/v1/projects/{project}/aggregated/acceleratorTypes
//	GET    /compute/v1/projects/{project}/zones/{zone}/diskTypes
//	GET    /compute/v1/projects/{project}/aggregated/diskTypes
//	GET    /compute/v1/projects/{project}/aggregated/machineTypes
//	GET    /compute/v1/projects/{project}/zones/{zone}/operations
//	GET    /compute/v1/projects/{project}/regions/{region}/operations
//	GET    /compute/v1/projects/{project}/global/operations
//	DELETE /compute/v1/projects/{project}/zones/{zone}/operations/{name}
//	DELETE /compute/v1/projects/{project}/regions/{region}/operations/{name}
//	DELETE /compute/v1/projects/{project}/global/operations/{name}
//	POST   /compute/v1/projects/{project}/global/operations/{name}/wait
//	GET    /compute/v1/projects/{project}/aggregated/operations
//	GET    /compute/v1/projects/{project}/aggregated/subnetworks
//	GET    /compute/v1/projects/{project}/aggregated/addresses
//	GET    /compute/v1/projects/{project}/aggregated/backendServices
//	GET    /compute/v1/projects/{project}/aggregated/healthChecks
//	GET    /compute/v1/projects/{project}/aggregated/urlMaps
//	GET    /compute/v1/projects/{project}/aggregated/targetHttpProxies
//	GET    /compute/v1/projects/{project}/aggregated/forwardingRules
//	GET    /compute/v1/projects/{project}/aggregated/instanceGroups
//	POST   /compute/v1/projects/{project}/zones/{zone}/instances/{name}/reset
//	POST   /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMachineType
//	POST   /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMetadata
//	POST   /compute/v1/projects/{project}/zones/{zone}/instances/{name}/attachDisk
//	POST   /compute/v1/projects/{project}/zones/{zone}/instances/{name}/detachDisk
//	GET    /compute/v1/projects/{project}/zones/{zone}/instances/{name}/serialPort

const moreProject = "test-project"

func TestCompute_Images_CRUD(t *testing.T) {
	svc := computeService(t)
	img := &compute.Image{Name: "sdk-image-1", SourceType: "RAW", DiskSizeGb: 10}
	_, err := svc.Images.Insert(moreProject, img).Do()
	require.NoError(t, err)

	got, err := svc.Images.Get(moreProject, "sdk-image-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-image-1", got.Name)

	_, err = svc.Images.SetLabels(moreProject, "sdk-image-1", &compute.GlobalSetLabelsRequest{Labels: map[string]string{"env": "test"}}).Do()
	require.NoError(t, err)

	list, err := svc.Images.List(moreProject).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)

	_, err = svc.Images.Delete(moreProject, "sdk-image-1").Do()
	require.NoError(t, err)
}

func TestCompute_Snapshots_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Snapshots.Insert(moreProject, &compute.Snapshot{Name: "sdk-snap-1"}).Do()
	require.NoError(t, err)
	got, err := svc.Snapshots.Get(moreProject, "sdk-snap-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-snap-1", got.Name)
	list, err := svc.Snapshots.List(moreProject).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.Snapshots.Delete(moreProject, "sdk-snap-1").Do()
	require.NoError(t, err)
}

func TestCompute_MachineImages_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.MachineImages.Insert(moreProject, &compute.MachineImage{Name: "sdk-mi-1"}).Do()
	require.NoError(t, err)
	got, err := svc.MachineImages.Get(moreProject, "sdk-mi-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-mi-1", got.Name)
	list, err := svc.MachineImages.List(moreProject).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.MachineImages.Delete(moreProject, "sdk-mi-1").Do()
	require.NoError(t, err)
}

func TestCompute_RegionDisks_CRUD(t *testing.T) {
	svc := computeService(t)
	const region = "us-central1"
	_, err := svc.RegionDisks.Insert(moreProject, region, &compute.Disk{Name: "sdk-rdisk-1", SizeGb: 20}).Do()
	require.NoError(t, err)
	got, err := svc.RegionDisks.Get(moreProject, region, "sdk-rdisk-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rdisk-1", got.Name)
	_, err = svc.RegionDisks.SetLabels(moreProject, region, "sdk-rdisk-1", &compute.RegionSetLabelsRequest{Labels: map[string]string{"k": "v"}}).Do()
	require.NoError(t, err)
	list, err := svc.RegionDisks.List(moreProject, region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.RegionDisks.Delete(moreProject, region, "sdk-rdisk-1").Do()
	require.NoError(t, err)
}

func TestCompute_GlobalAddresses_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.GlobalAddresses.Insert(moreProject, &compute.Address{Name: "sdk-gaddr-1", AddressType: "EXTERNAL"}).Do()
	require.NoError(t, err)
	got, err := svc.GlobalAddresses.Get(moreProject, "sdk-gaddr-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-gaddr-1", got.Name)
	list, err := svc.GlobalAddresses.List(moreProject).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.GlobalAddresses.Delete(moreProject, "sdk-gaddr-1").Do()
	require.NoError(t, err)
}

func TestCompute_Routes_CRUD(t *testing.T) {
	svc := computeService(t)
	route := &compute.Route{
		Name:           "sdk-route-1",
		Network:        "projects/test-project/global/networks/default",
		DestRange:      "10.20.0.0/16",
		NextHopGateway: "projects/test-project/global/gateways/default-internet-gateway",
		Priority:       1000,
	}
	_, err := svc.Routes.Insert(moreProject, route).Do()
	require.NoError(t, err)
	got, err := svc.Routes.Get(moreProject, "sdk-route-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-route-1", got.Name)
	list, err := svc.Routes.List(moreProject).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.Routes.Delete(moreProject, "sdk-route-1").Do()
	require.NoError(t, err)
}

func TestCompute_TargetPools_CRUD_Aggregated(t *testing.T) {
	svc := computeService(t)
	const region = "us-central1"
	_, err := svc.TargetPools.Insert(moreProject, region, &compute.TargetPool{Name: "sdk-tp-1"}).Do()
	require.NoError(t, err)
	got, err := svc.TargetPools.Get(moreProject, region, "sdk-tp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-tp-1", got.Name)
	list, err := svc.TargetPools.List(moreProject, region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	agg, err := svc.TargetPools.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "regions/"+region)
	_, err = svc.TargetPools.Delete(moreProject, region, "sdk-tp-1").Do()
	require.NoError(t, err)
}

func TestCompute_RegionLBResources_CRUD(t *testing.T) {
	svc := computeService(t)
	const region = "us-central1"

	_, err := svc.RegionHealthChecks.Insert(moreProject, region, &compute.HealthCheck{Name: "sdk-rhc-1", Type: "TCP"}).Do()
	require.NoError(t, err)
	rhc, err := svc.RegionHealthChecks.Get(moreProject, region, "sdk-rhc-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rhc-1", rhc.Name)
	_, err = svc.RegionHealthChecks.Patch(moreProject, region, "sdk-rhc-1", &compute.HealthCheck{Description: "patched"}).Do()
	require.NoError(t, err)

	_, err = svc.RegionBackendServices.Insert(moreProject, region, &compute.BackendService{Name: "sdk-rbs-1"}).Do()
	require.NoError(t, err)
	rbs, err := svc.RegionBackendServices.Get(moreProject, region, "sdk-rbs-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rbs-1", rbs.Name)

	_, err = svc.RegionUrlMaps.Insert(moreProject, region, &compute.UrlMap{Name: "sdk-rum-1"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionUrlMaps.Get(moreProject, region, "sdk-rum-1").Do()
	require.NoError(t, err)

	_, err = svc.RegionTargetHttpProxies.Insert(moreProject, region, &compute.TargetHttpProxy{Name: "sdk-rthp-1"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionTargetHttpProxies.Get(moreProject, region, "sdk-rthp-1").Do()
	require.NoError(t, err)

	_, err = svc.RegionInstanceTemplates.Insert(moreProject, region, &compute.InstanceTemplate{Name: "sdk-rit-1", Properties: &compute.InstanceProperties{MachineType: "e2-micro"}}).Do()
	require.NoError(t, err)
	rit, err := svc.RegionInstanceTemplates.Get(moreProject, region, "sdk-rit-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rit-1", rit.Name)
	ritList, err := svc.RegionInstanceTemplates.List(moreProject, region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(ritList.Items), 1)

	require.NoError(t, mustDelete(svc.RegionHealthChecks.Delete(moreProject, region, "sdk-rhc-1").Do()))
	require.NoError(t, mustDelete(svc.RegionBackendServices.Delete(moreProject, region, "sdk-rbs-1").Do()))
	require.NoError(t, mustDelete(svc.RegionUrlMaps.Delete(moreProject, region, "sdk-rum-1").Do()))
	require.NoError(t, mustDelete(svc.RegionTargetHttpProxies.Delete(moreProject, region, "sdk-rthp-1").Do()))
	require.NoError(t, mustDelete(svc.RegionInstanceTemplates.Delete(moreProject, region, "sdk-rit-1").Do()))
}

func TestCompute_GlobalLBResources_CRUD_Aggregated(t *testing.T) {
	svc := computeService(t)

	_, err := svc.HttpHealthChecks.Insert(moreProject, &compute.HttpHealthCheck{Name: "sdk-hhc-1"}).Do()
	require.NoError(t, err)
	_, err = svc.HttpHealthChecks.Get(moreProject, "sdk-hhc-1").Do()
	require.NoError(t, err)

	_, err = svc.HttpsHealthChecks.Insert(moreProject, &compute.HttpsHealthCheck{Name: "sdk-shc-1"}).Do()
	require.NoError(t, err)
	_, err = svc.HttpsHealthChecks.Get(moreProject, "sdk-shc-1").Do()
	require.NoError(t, err)

	_, err = svc.TargetHttpsProxies.Insert(moreProject, &compute.TargetHttpsProxy{Name: "sdk-thsp-1"}).Do()
	require.NoError(t, err)
	thspAgg, err := svc.TargetHttpsProxies.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.NotNil(t, thspAgg.Items)

	_, err = svc.SslCertificates.Insert(moreProject, &compute.SslCertificate{Name: "sdk-ssl-1"}).Do()
	require.NoError(t, err)
	sslAgg, err := svc.SslCertificates.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.NotNil(t, sslAgg.Items)

	_, err = svc.TargetTcpProxies.Insert(moreProject, &compute.TargetTcpProxy{Name: "sdk-ttp-1"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetTcpProxies.Get(moreProject, "sdk-ttp-1").Do()
	require.NoError(t, err)

	_, err = svc.TargetGrpcProxies.Insert(moreProject, &compute.TargetGrpcProxy{Name: "sdk-tgp-1"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetGrpcProxies.Get(moreProject, "sdk-tgp-1").Do()
	require.NoError(t, err)

	require.NoError(t, mustDelete(svc.HttpHealthChecks.Delete(moreProject, "sdk-hhc-1").Do()))
	require.NoError(t, mustDelete(svc.HttpsHealthChecks.Delete(moreProject, "sdk-shc-1").Do()))
	require.NoError(t, mustDelete(svc.TargetHttpsProxies.Delete(moreProject, "sdk-thsp-1").Do()))
	require.NoError(t, mustDelete(svc.SslCertificates.Delete(moreProject, "sdk-ssl-1").Do()))
	require.NoError(t, mustDelete(svc.TargetTcpProxies.Delete(moreProject, "sdk-ttp-1").Do()))
	require.NoError(t, mustDelete(svc.TargetGrpcProxies.Delete(moreProject, "sdk-tgp-1").Do()))
}

func TestCompute_InstanceGroupManagers_CRUD(t *testing.T) {
	svc := computeService(t)
	const zone = "us-central1-a"
	igm := &compute.InstanceGroupManager{
		Name:             "sdk-igm-1",
		BaseInstanceName: "sdk-igm",
		TargetSize:       0,
		InstanceTemplate: "projects/test-project/global/instanceTemplates/tmpl-1",
	}
	_, err := svc.InstanceGroupManagers.Insert(moreProject, zone, igm).Do()
	require.NoError(t, err)
	got, err := svc.InstanceGroupManagers.Get(moreProject, zone, "sdk-igm-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-igm-1", got.Name)

	_, err = svc.InstanceGroupManagers.Resize(moreProject, zone, "sdk-igm-1", 3).Do()
	require.NoError(t, err)
	_, err = svc.InstanceGroupManagers.SetInstanceTemplate(moreProject, zone, "sdk-igm-1", &compute.InstanceGroupManagersSetInstanceTemplateRequest{
		InstanceTemplate: "projects/test-project/global/instanceTemplates/tmpl-2",
	}).Do()
	require.NoError(t, err)
	lmi, err := svc.InstanceGroupManagers.ListManagedInstances(moreProject, zone, "sdk-igm-1").Do()
	require.NoError(t, err)
	assert.NotNil(t, lmi)

	agg, err := svc.InstanceGroupManagers.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "zones/"+zone)

	list, err := svc.InstanceGroupManagers.List(moreProject, zone).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)

	_, err = svc.InstanceGroupManagers.Delete(moreProject, zone, "sdk-igm-1").Do()
	require.NoError(t, err)
}

func TestCompute_RegionInstanceGroupManagers_CRUD(t *testing.T) {
	svc := computeService(t)
	const region = "us-central1"
	igm := &compute.InstanceGroupManager{Name: "sdk-rigm-1", BaseInstanceName: "sdk-rigm"}
	_, err := svc.RegionInstanceGroupManagers.Insert(moreProject, region, igm).Do()
	require.NoError(t, err)
	got, err := svc.RegionInstanceGroupManagers.Get(moreProject, region, "sdk-rigm-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rigm-1", got.Name)
	_, err = svc.RegionInstanceGroupManagers.Resize(moreProject, region, "sdk-rigm-1", 2).Do()
	require.NoError(t, err)
	list, err := svc.RegionInstanceGroupManagers.List(moreProject, region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.RegionInstanceGroupManagers.Delete(moreProject, region, "sdk-rigm-1").Do()
	require.NoError(t, err)
}

func TestCompute_Catalog_Regions_DiskTypes_Accelerators(t *testing.T) {
	svc := computeService(t)
	const zone = "us-central1-a"

	regions, err := svc.Regions.List(moreProject).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(regions.Items), 1)
	region, err := svc.Regions.Get(moreProject, "us-central1").Do()
	require.NoError(t, err)
	assert.Equal(t, "us-central1", region.Name)

	dts, err := svc.DiskTypes.List(moreProject, zone).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(dts.Items), 1)
	dtAgg, err := svc.DiskTypes.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.NotNil(t, dtAgg.Items)

	mtAgg, err := svc.MachineTypes.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.NotNil(t, mtAgg.Items)

	acc, err := svc.AcceleratorTypes.List(moreProject, zone).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(acc.Items), 1)
	accGet, err := svc.AcceleratorTypes.Get(moreProject, zone, "nvidia-tesla-t4").Do()
	require.NoError(t, err)
	assert.Equal(t, "nvidia-tesla-t4", accGet.Name)
	accAgg, err := svc.AcceleratorTypes.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.NotNil(t, accAgg.Items)
}

func TestCompute_Operations_ListDelete(t *testing.T) {
	svc := computeService(t)
	const zone = "us-central1-a"
	const region = "us-central1"

	zList, err := svc.ZoneOperations.List(moreProject, zone).Do()
	require.NoError(t, err)
	assert.NotNil(t, zList)
	rList, err := svc.RegionOperations.List(moreProject, region).Do()
	require.NoError(t, err)
	assert.NotNil(t, rList)
	gList, err := svc.GlobalOperations.List(moreProject).Do()
	require.NoError(t, err)
	assert.NotNil(t, gList)
	gAgg, err := svc.GlobalOperations.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.NotNil(t, gAgg)

	// An unknown operation 404s on get/delete/wait — the sim never
	// fabricates a DONE for a name it never handed out.
	assert.Error(t, svc.ZoneOperations.Delete(moreProject, zone, "operation-unknown").Do())
	assert.Error(t, svc.RegionOperations.Delete(moreProject, region, "operation-unknown").Do())
	assert.Error(t, svc.GlobalOperations.Delete(moreProject, "operation-unknown").Do())
	_, err = svc.GlobalOperations.Wait(moreProject, "operation-unknown").Do()
	assert.Error(t, err)
}

func TestCompute_AggregatedLists_Existing(t *testing.T) {
	svc := computeService(t)
	for _, do := range []func() error{
		func() error { _, e := svc.Subnetworks.AggregatedList(moreProject).Do(); return e },
		func() error { _, e := svc.Addresses.AggregatedList(moreProject).Do(); return e },
		func() error { _, e := svc.BackendServices.AggregatedList(moreProject).Do(); return e },
		func() error { _, e := svc.HealthChecks.AggregatedList(moreProject).Do(); return e },
		func() error { _, e := svc.UrlMaps.AggregatedList(moreProject).Do(); return e },
		func() error { _, e := svc.TargetHttpProxies.AggregatedList(moreProject).Do(); return e },
		func() error { _, e := svc.ForwardingRules.AggregatedList(moreProject).Do(); return e },
		func() error { _, e := svc.InstanceGroups.AggregatedList(moreProject).Do(); return e },
	} {
		require.NoError(t, do())
	}
}

func TestCompute_InstanceActions(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const zone = "us-central1-a"
	const name = "sdk-inst-actions"

	inst := &compute.Instance{
		Name:        name,
		MachineType: "projects/test-project/zones/" + zone + "/machineTypes/e2-micro",
		Disks: []*compute.AttachedDisk{{
			Boot:       true,
			AutoDelete: true,
			InitializeParams: &compute.AttachedDiskInitializeParams{
				SourceImage: "projects/debian-cloud/global/images/family/debian-12",
			},
		}},
		NetworkInterfaces: []*compute.NetworkInterface{{Network: "projects/test-project/global/networks/default"}},
	}
	_, err := svc.Instances.Insert(moreProject, zone, inst).Do()
	require.NoError(t, err)

	_, err = svc.Instances.Reset(moreProject, zone, name).Do()
	require.NoError(t, err)
	_, err = svc.Instances.SetMachineType(moreProject, zone, name, &compute.InstancesSetMachineTypeRequest{
		MachineType: "projects/test-project/zones/" + zone + "/machineTypes/e2-small",
	}).Do()
	require.NoError(t, err)
	_, err = svc.Instances.SetMetadata(moreProject, zone, name, &compute.Metadata{
		Items: []*compute.MetadataItems{{Key: "foo", Value: strPtr("bar")}},
	}).Do()
	require.NoError(t, err)
	_, err = svc.Instances.AttachDisk(moreProject, zone, name, &compute.AttachedDisk{
		Source:     "projects/test-project/zones/" + zone + "/disks/extra",
		DeviceName: "extra",
	}).Do()
	require.NoError(t, err)
	_, err = svc.Instances.DetachDisk(moreProject, zone, name, "extra").Do()
	require.NoError(t, err)

	out, err := svc.Instances.GetSerialPortOutput(moreProject, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#serialPortOutput", out.Kind)

	_, err = svc.Instances.Delete(moreProject, zone, name).Do()
	require.NoError(t, err)
}

func mustDelete(_ *compute.Operation, err error) error { return err }

func strPtr(s string) *string { return &s }
