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
	labelled, err := svc.Images.Get(moreProject, "sdk-image-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "test", labelled.Labels["env"], "setLabels must be readable back off the image")
	assert.NotEmpty(t, labelled.LabelFingerprint, "a labelled resource carries the fingerprint the next setLabels must present")

	list, err := svc.Images.List(moreProject).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(i *compute.Image) string { return i.Name }, "sdk-image-1")

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
	assertComputeListHasName(t, list.Items, func(s *compute.Snapshot) string { return s.Name }, "sdk-snap-1")
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
	assertComputeListHasName(t, list.Items, func(m *compute.MachineImage) string { return m.Name }, "sdk-mi-1")
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
	labelled, err := svc.RegionDisks.Get(moreProject, region, "sdk-rdisk-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "v", labelled.Labels["k"], "setLabels must be readable back off the regional disk")
	assert.NotEqual(t, got.LabelFingerprint, labelled.LabelFingerprint, "setLabels must re-stamp the fingerprint")
	list, err := svc.RegionDisks.List(moreProject, region).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(d *compute.Disk) string { return d.Name }, "sdk-rdisk-1")
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
	assertComputeListHasName(t, list.Items, func(a *compute.Address) string { return a.Name }, "sdk-gaddr-1")
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
	assertComputeListHasName(t, list.Items, func(rt *compute.Route) string { return rt.Name }, "sdk-route-1")
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
	assertComputeListHasName(t, list.Items, func(p *compute.TargetPool) string { return p.Name }, "sdk-tp-1")
	agg, err := svc.TargetPools.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	require.Contains(t, agg.Items, "regions/"+region)
	assertComputeListHasName(t, agg.Items["regions/"+region].TargetPools, func(p *compute.TargetPool) string { return p.Name }, "sdk-tp-1")
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
	patched, err := svc.RegionHealthChecks.Get(moreProject, region, "sdk-rhc-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "patched", patched.Description, "patch must land on the stored health check")
	assert.Equal(t, "TCP", patched.Type, "a patch must not drop the members it did not name")

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
	assertComputeListHasName(t, ritList.Items, func(tp *compute.InstanceTemplate) string { return tp.Name }, "sdk-rit-1")

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
	require.Contains(t, thspAgg.Items, "global")
	assertComputeListHasName(t, thspAgg.Items["global"].TargetHttpsProxies, func(p *compute.TargetHttpsProxy) string { return p.Name }, "sdk-thsp-1")

	_, err = svc.SslCertificates.Insert(moreProject, &compute.SslCertificate{Name: "sdk-ssl-1"}).Do()
	require.NoError(t, err)
	sslAgg, err := svc.SslCertificates.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	require.Contains(t, sslAgg.Items, "global")
	assertComputeListHasName(t, sslAgg.Items["global"].SslCertificates, func(c *compute.SslCertificate) string { return c.Name }, "sdk-ssl-1")

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
	resized, err := svc.InstanceGroupManagers.Get(moreProject, zone, "sdk-igm-1").Do()
	require.NoError(t, err)
	assert.EqualValues(t, 3, resized.TargetSize, "resize must move the manager's target size")

	_, err = svc.InstanceGroupManagers.SetInstanceTemplate(moreProject, zone, "sdk-igm-1", &compute.InstanceGroupManagersSetInstanceTemplateRequest{
		InstanceTemplate: "projects/test-project/global/instanceTemplates/tmpl-2",
	}).Do()
	require.NoError(t, err)
	retemplated, err := svc.InstanceGroupManagers.Get(moreProject, zone, "sdk-igm-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/global/instanceTemplates/tmpl-2", retemplated.InstanceTemplate,
		"setInstanceTemplate must swap the template the manager creates instances from")
	assert.EqualValues(t, 3, retemplated.TargetSize, "swapping the template must not disturb the target size")

	lmi, err := svc.InstanceGroupManagers.ListManagedInstances(moreProject, zone, "sdk-igm-1").Do()
	require.NoError(t, err)
	assert.NotNil(t, lmi)

	agg, err := svc.InstanceGroupManagers.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	require.Contains(t, agg.Items, "zones/"+zone)
	assertComputeListHasName(t, agg.Items["zones/"+zone].InstanceGroupManagers, func(m *compute.InstanceGroupManager) string { return m.Name }, "sdk-igm-1")

	list, err := svc.InstanceGroupManagers.List(moreProject, zone).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(m *compute.InstanceGroupManager) string { return m.Name }, "sdk-igm-1")

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
	resized, err := svc.RegionInstanceGroupManagers.Get(moreProject, region, "sdk-rigm-1").Do()
	require.NoError(t, err)
	assert.EqualValues(t, 2, resized.TargetSize, "resize must move the regional manager's target size")
	list, err := svc.RegionInstanceGroupManagers.List(moreProject, region).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(m *compute.InstanceGroupManager) string { return m.Name }, "sdk-rigm-1")
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

// Compute Engine records every mutation as an operation in the scope that owns
// the resource it touched. Each scoped listing reports the operations of its own
// scope, the aggregated listing groups the same records under their scope keys,
// and delete acknowledges an operation the service issued while refusing a name
// it never handed out. A zonal disk, a regional disk and a global image are all
// metadata writes, so the three scopes are covered on every host.
func TestCompute_Operations_ListDelete(t *testing.T) {
	svc := computeService(t)
	const zone = "us-central1-a"
	const region = "us-central1"

	zoneOp, err := svc.Disks.Insert(moreProject, zone, &compute.Disk{Name: "op-scope-disk", SizeGb: 10}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Disks.Delete(moreProject, zone, "op-scope-disk").Do() })
	regionOp, err := svc.RegionDisks.Insert(moreProject, region, &compute.Disk{Name: "op-scope-rdisk", SizeGb: 10}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.RegionDisks.Delete(moreProject, region, "op-scope-rdisk").Do() })
	globalOp, err := svc.Images.Insert(moreProject, &compute.Image{Name: "op-scope-image", SourceType: "RAW"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Images.Delete(moreProject, "op-scope-image").Do() })

	zList, err := svc.ZoneOperations.List(moreProject, zone).Do()
	require.NoError(t, err)
	zoneRecord := findComputeOperation(zList.Items, zoneOp.Name)
	require.NotNil(t, zoneRecord, "the zone operation listing does not report the operation the disk insert returned")
	assert.Equal(t, "compute#operation", zoneRecord.Kind)
	assert.Equal(t, "insert", zoneRecord.OperationType)
	assert.Contains(t, zoneRecord.TargetLink, "/zones/"+zone+"/disks/op-scope-disk")

	rList, err := svc.RegionOperations.List(moreProject, region).Do()
	require.NoError(t, err)
	regionRecord := findComputeOperation(rList.Items, regionOp.Name)
	require.NotNil(t, regionRecord, "the region operation listing does not report the operation the regional disk insert returned")
	assert.Equal(t, "insert", regionRecord.OperationType)
	assert.Contains(t, regionRecord.TargetLink, "/regions/"+region+"/disks/op-scope-rdisk")

	gList, err := svc.GlobalOperations.List(moreProject).Do()
	require.NoError(t, err)
	globalRecord := findComputeOperation(gList.Items, globalOp.Name)
	require.NotNil(t, globalRecord, "the global operation listing does not report the operation the image insert returned")
	assert.Equal(t, "insert", globalRecord.OperationType)
	assert.Contains(t, globalRecord.TargetLink, "/global/images/op-scope-image")

	// A listing reports its own scope and no other.
	assert.Nil(t, findComputeOperation(gList.Items, zoneOp.Name), "a zonal operation leaked into the global listing")
	assert.Nil(t, findComputeOperation(zList.Items, globalOp.Name), "a global operation leaked into a zone listing")

	gAgg, err := svc.GlobalOperations.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#operationAggregatedList", gAgg.Kind)
	require.Contains(t, gAgg.Items, "zones/"+zone)
	assert.NotNil(t, findComputeOperation(gAgg.Items["zones/"+zone].Operations, zoneOp.Name),
		"the aggregated listing omits the zonal operation")
	require.Contains(t, gAgg.Items, "regions/"+region)
	assert.NotNil(t, findComputeOperation(gAgg.Items["regions/"+region].Operations, regionOp.Name),
		"the aggregated listing omits the regional operation")
	require.Contains(t, gAgg.Items, "global")
	assert.NotNil(t, findComputeOperation(gAgg.Items["global"].Operations, globalOp.Name),
		"the aggregated listing omits the global operation")

	// Deleting an operation the service issued is accepted.
	require.NoError(t, svc.ZoneOperations.Delete(moreProject, zone, zoneOp.Name).Do())

	// An unknown operation 404s on get/delete/wait — the sim never
	// fabricates a DONE for a name it never handed out.
	assert.Error(t, svc.ZoneOperations.Delete(moreProject, zone, "operation-unknown").Do())
	assert.Error(t, svc.RegionOperations.Delete(moreProject, region, "operation-unknown").Do())
	assert.Error(t, svc.GlobalOperations.Delete(moreProject, "operation-unknown").Do())
	_, err = svc.GlobalOperations.Wait(moreProject, "operation-unknown").Do()
	assert.Error(t, err)
}

// An aggregated list is how a client enumerates one collection across every
// scope of a project in a single call, so each endpoint has to report the
// project's resource under that resource's own scope key, beneath the kind the
// service declares for the response. Reading the endpoints without first
// creating anything proves only that the routes exist.
func TestCompute_AggregatedLists_Existing(t *testing.T) {
	svc := computeService(t)
	const zone = "us-central1-a"

	_, err := svc.HealthChecks.Insert(moreProject, &compute.HealthCheck{Name: "agg-hc", Type: "TCP"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.HealthChecks.Delete(moreProject, "agg-hc").Do() })
	_, err = svc.BackendServices.Insert(moreProject, &compute.BackendService{Name: "agg-bs", Protocol: "HTTP"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.BackendServices.Delete(moreProject, "agg-bs").Do() })
	_, err = svc.UrlMaps.Insert(moreProject, &compute.UrlMap{Name: "agg-um"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.UrlMaps.Delete(moreProject, "agg-um").Do() })
	_, err = svc.TargetHttpProxies.Insert(moreProject, &compute.TargetHttpProxy{Name: "agg-thp"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.TargetHttpProxies.Delete(moreProject, "agg-thp").Do() })
	_, err = svc.GlobalForwardingRules.Insert(moreProject, &compute.ForwardingRule{Name: "agg-fr", PortRange: "80", IPProtocol: "TCP"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.GlobalForwardingRules.Delete(moreProject, "agg-fr").Do() })
	_, err = svc.InstanceGroups.Insert(moreProject, zone, &compute.InstanceGroup{Name: "agg-ig"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.InstanceGroups.Delete(moreProject, zone, "agg-ig").Do() })

	hcAgg, err := svc.HealthChecks.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#healthChecksAggregatedList", hcAgg.Kind)
	require.Contains(t, hcAgg.Items, "global")
	assertComputeListHasName(t, hcAgg.Items["global"].HealthChecks, func(h *compute.HealthCheck) string { return h.Name }, "agg-hc")

	bsAgg, err := svc.BackendServices.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#backendServiceAggregatedList", bsAgg.Kind)
	require.Contains(t, bsAgg.Items, "global")
	assertComputeListHasName(t, bsAgg.Items["global"].BackendServices, func(b *compute.BackendService) string { return b.Name }, "agg-bs")

	umAgg, err := svc.UrlMaps.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#urlMapsAggregatedList", umAgg.Kind)
	require.Contains(t, umAgg.Items, "global")
	assertComputeListHasName(t, umAgg.Items["global"].UrlMaps, func(u *compute.UrlMap) string { return u.Name }, "agg-um")

	thpAgg, err := svc.TargetHttpProxies.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#targetHttpProxyAggregatedList", thpAgg.Kind)
	require.Contains(t, thpAgg.Items, "global")
	assertComputeListHasName(t, thpAgg.Items["global"].TargetHttpProxies, func(p *compute.TargetHttpProxy) string { return p.Name }, "agg-thp")

	frAgg, err := svc.ForwardingRules.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#forwardingRuleAggregatedList", frAgg.Kind)
	require.Contains(t, frAgg.Items, "global")
	assertComputeListHasName(t, frAgg.Items["global"].ForwardingRules, func(f *compute.ForwardingRule) string { return f.Name }, "agg-fr")

	igAgg, err := svc.InstanceGroups.AggregatedList(moreProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#instanceGroupAggregatedList", igAgg.Kind)
	require.Contains(t, igAgg.Items, "zones/"+zone)
	assertComputeListHasName(t, igAgg.Items["zones/"+zone].InstanceGroups, func(g *compute.InstanceGroup) string { return g.Name }, "agg-ig")

	// Subnetworks and addresses are backed by real host networking, so the two
	// aggregated views over them are read wherever that kernel support exists.
	t.Run("scopes backed by real host networking", func(t *testing.T) {
		requireNetworkHost(t)
		const region = "us-central1"

		_, err := svc.Networks.Insert(moreProject, &compute.Network{Name: "agg-net", AutoCreateSubnetworks: false}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Networks.Delete(moreProject, "agg-net").Do() })
		_, err = svc.Subnetworks.Insert(moreProject, region, &compute.Subnetwork{
			Name:        "agg-subnet",
			IpCidrRange: "10.62.0.0/24",
			Network:     "projects/test-project/global/networks/agg-net",
			Region:      region,
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Subnetworks.Delete(moreProject, region, "agg-subnet").Do() })
		_, err = svc.Addresses.Insert(moreProject, region, &compute.Address{
			Name:        "agg-address",
			AddressType: "EXTERNAL",
			IpVersion:   "IPV4",
			NetworkTier: "PREMIUM",
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Addresses.Delete(moreProject, region, "agg-address").Do() })

		subAgg, err := svc.Subnetworks.AggregatedList(moreProject).Do()
		require.NoError(t, err)
		assert.Equal(t, "compute#subnetworkAggregatedList", subAgg.Kind)
		require.Contains(t, subAgg.Items, "regions/"+region)
		assertComputeListHasName(t, subAgg.Items["regions/"+region].Subnetworks, func(s *compute.Subnetwork) string { return s.Name }, "agg-subnet")

		addrAgg, err := svc.Addresses.AggregatedList(moreProject).Do()
		require.NoError(t, err)
		assert.Equal(t, "compute#addressAggregatedList", addrAgg.Kind)
		require.Contains(t, addrAgg.Items, "regions/"+region)
		assertComputeListHasName(t, addrAgg.Items["regions/"+region].Addresses, func(a *compute.Address) string { return a.Name }, "agg-address")
	})
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
	op, err := svc.Instances.Insert(moreProject, zone, inst).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(moreProject, zone, name).Do() })
	// Compute Engine brings the machine up behind the insert operation, so a
	// client that is about to mutate the instance waits for that operation
	// first — as it must against the real service.
	done := awaitZoneOperation(t, svc, moreProject, zone, op.Name)
	require.Nil(t, done.Error, "the insert operation failed: %+v", done.Error)

	// A reset reboots the machine in place: the instance stays, and stays up.
	_, err = svc.Instances.Reset(moreProject, zone, name).Do()
	require.NoError(t, err)
	afterReset, err := svc.Instances.Get(moreProject, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", afterReset.Status)
	bootDisks := len(afterReset.Disks)
	require.NotZero(t, bootDisks, "an instance always carries its boot disk")

	const resizedMachineType = "projects/test-project/zones/" + zone + "/machineTypes/e2-small"
	_, err = svc.Instances.SetMachineType(moreProject, zone, name, &compute.InstancesSetMachineTypeRequest{
		MachineType: resizedMachineType,
	}).Do()
	require.NoError(t, err)
	_, err = svc.Instances.SetMetadata(moreProject, zone, name, &compute.Metadata{
		Items: []*compute.MetadataItems{{Key: "foo", Value: strPtr("bar")}},
	}).Do()
	require.NoError(t, err)

	mutated, err := svc.Instances.Get(moreProject, zone, name).Do()
	require.NoError(t, err)
	assert.Contains(t, mutated.MachineType, "/machineTypes/e2-small",
		"setMachineType must move the instance off the machine type it was created with")
	require.NotNil(t, mutated.Metadata, "setMetadata must leave metadata on the instance")
	assert.Equal(t, "compute#metadata", mutated.Metadata.Kind)
	assert.NotEmpty(t, mutated.Metadata.Fingerprint, "metadata carries the fingerprint the next write must present")
	require.Len(t, mutated.Metadata.Items, 1)
	assert.Equal(t, "foo", mutated.Metadata.Items[0].Key)
	require.NotNil(t, mutated.Metadata.Items[0].Value)
	assert.Equal(t, "bar", *mutated.Metadata.Items[0].Value)

	_, err = svc.Instances.AttachDisk(moreProject, zone, name, &compute.AttachedDisk{
		Source:     "projects/test-project/zones/" + zone + "/disks/extra",
		DeviceName: "extra",
	}).Do()
	require.NoError(t, err)
	attached, err := svc.Instances.Get(moreProject, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, attached.Disks, bootDisks+1, "attachDisk must add the disk to the instance")
	extra := attached.Disks[len(attached.Disks)-1]
	assert.Equal(t, "extra", extra.DeviceName)
	assert.Equal(t, "compute#attachedDisk", extra.Kind)
	assert.Contains(t, extra.Source, "/disks/extra")
	assert.EqualValues(t, bootDisks, extra.Index, "an attached disk takes the next index on the instance")

	_, err = svc.Instances.DetachDisk(moreProject, zone, name, "extra").Do()
	require.NoError(t, err)
	detached, err := svc.Instances.Get(moreProject, zone, name).Do()
	require.NoError(t, err)
	assert.Len(t, detached.Disks, bootDisks, "detachDisk must take the disk back off the instance")
	for _, d := range detached.Disks {
		assert.NotEqual(t, "extra", d.DeviceName, "detachDisk left the disk it named attached")
	}

	out, err := svc.Instances.GetSerialPortOutput(moreProject, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#serialPortOutput", out.Kind)
	assert.Contains(t, out.SelfLink, "/instances/"+name+"/serialPort")

	_, err = svc.Instances.Delete(moreProject, zone, name).Do()
	require.NoError(t, err)
	_, err = svc.Instances.Get(moreProject, zone, name).Do()
	require.Error(t, err, "get after delete must fail")
}

func mustDelete(_ *compute.Operation, err error) error { return err }

func strPtr(s string) *string { return &s }

// assertComputeListHasName fails unless a Compute list method reported the
// resource the calling test created. Every test in this suite shares one
// project, so a listing that dropped the resource under test still carries
// another test's and satisfies a length check; only the name proves the
// collection reported this one. nameOf projects a list item to its name because
// each Compute collection answers with its own item type.
func assertComputeListHasName[T any](t *testing.T, items []T, nameOf func(T) string, want string) {
	t.Helper()
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, nameOf(item))
	}
	assert.Contains(t, names, want, "the listing did not report the resource this test created")
}

// findComputeOperation returns the operation with the given name, or nil when
// the listing does not carry it.
func findComputeOperation(items []*compute.Operation, name string) *compute.Operation {
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	return nil
}
