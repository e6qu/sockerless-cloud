package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

// Exercises the second wave of Compute Engine (compute v1) control-plane
// metadata CRUD added in compute_more2.go via the official
// google.golang.org/api/compute/v1 client. Every resource here is
// metadata-only — insert returns a DONE Operation synchronously and
// get/list/aggregatedList/iamPolicy read the stored state back — so these
// run on every host (no real-exec network).

const (
	more2Project = "test-project"
	more2Region  = "us-central1"
	more2Zone    = "us-central1-a"
)

// --- Global resources -------------------------------------------------

func TestCompute2_BackendBuckets_CRUD_IAM(t *testing.T) {
	svc := computeService(t)
	_, err := svc.BackendBuckets.Insert(more2Project, &compute.BackendBucket{Name: "sdk-bb-1", BucketName: "gcs-bucket"}).Do()
	require.NoError(t, err)
	got, err := svc.BackendBuckets.Get(more2Project, "sdk-bb-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-bb-1", got.Name)
	assert.Equal(t, "gcs-bucket", got.BucketName)

	_, err = svc.BackendBuckets.Patch(more2Project, "sdk-bb-1", &compute.BackendBucket{Description: "patched"}).Do()
	require.NoError(t, err)

	list, err := svc.BackendBuckets.List(more2Project).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)

	// IAM triplet.
	pol, err := svc.BackendBuckets.GetIamPolicy(more2Project, "sdk-bb-1").Do()
	require.NoError(t, err)
	assert.NotEmpty(t, pol.Etag)
	_, err = svc.BackendBuckets.SetIamPolicy(more2Project, "sdk-bb-1", &compute.GlobalSetPolicyRequest{
		Policy: &compute.Policy{
			Bindings: []*compute.Binding{{Role: "roles/compute.storageAdmin", Members: []string{"user:example@example.com"}}},
		},
	}).Do()
	require.NoError(t, err)
	pol2, err := svc.BackendBuckets.GetIamPolicy(more2Project, "sdk-bb-1").Do()
	require.NoError(t, err)
	require.Len(t, pol2.Bindings, 1)
	assert.Equal(t, "roles/compute.storageAdmin", pol2.Bindings[0].Role)
	perms, err := svc.BackendBuckets.TestIamPermissions(more2Project, "sdk-bb-1", &compute.TestPermissionsRequest{Permissions: []string{"compute.backendBuckets.get"}}).Do()
	require.NoError(t, err)
	assert.Contains(t, perms.Permissions, "compute.backendBuckets.get")

	_, err = svc.BackendBuckets.Delete(more2Project, "sdk-bb-1").Do()
	require.NoError(t, err)
}

func TestCompute2_Licenses_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Licenses.Insert(more2Project, &compute.License{Name: "sdk-lic-1"}).Do()
	require.NoError(t, err)
	got, err := svc.Licenses.Get(more2Project, "sdk-lic-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-lic-1", got.Name)
	list, err := svc.Licenses.List(more2Project).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.Licenses.Delete(more2Project, "sdk-lic-1").Do()
	require.NoError(t, err)
}

func TestCompute2_SslPolicies_CRUD_AvailableFeatures(t *testing.T) {
	svc := computeService(t)
	_, err := svc.SslPolicies.Insert(more2Project, &compute.SslPolicy{Name: "sdk-ssl-1", MinTlsVersion: "TLS_1_2", Profile: "MODERN"}).Do()
	require.NoError(t, err)
	got, err := svc.SslPolicies.Get(more2Project, "sdk-ssl-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-ssl-1", got.Name)
	assert.Equal(t, "TLS_1_2", got.MinTlsVersion)

	feat, err := svc.SslPolicies.ListAvailableFeatures(more2Project).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, feat.Features)

	list, err := svc.SslPolicies.List(more2Project).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)

	agg, err := svc.SslPolicies.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "global")

	_, err = svc.SslPolicies.Delete(more2Project, "sdk-ssl-1").Do()
	require.NoError(t, err)
}

func TestCompute2_TargetSslProxies_CRUD_Setters(t *testing.T) {
	svc := computeService(t)
	_, err := svc.TargetSslProxies.Insert(more2Project, &compute.TargetSslProxy{Name: "sdk-tsp-1"}).Do()
	require.NoError(t, err)
	got, err := svc.TargetSslProxies.Get(more2Project, "sdk-tsp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-tsp-1", got.Name)

	_, err = svc.TargetSslProxies.SetBackendService(more2Project, "sdk-tsp-1", &compute.TargetSslProxiesSetBackendServiceRequest{Service: "https://www.googleapis.com/compute/v1/projects/test-project/regions/us-central1/backendServices/bs"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetSslProxies.SetSslCertificates(more2Project, "sdk-tsp-1", &compute.TargetSslProxiesSetSslCertificatesRequest{SslCertificates: []string{"projects/test-project/global/sslCertificates/cert"}}).Do()
	require.NoError(t, err)

	list, err := svc.TargetSslProxies.List(more2Project).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.TargetSslProxies.Delete(more2Project, "sdk-tsp-1").Do()
	require.NoError(t, err)
}

func TestCompute2_ExternalVpnGateways_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.ExternalVpnGateways.Insert(more2Project, &compute.ExternalVpnGateway{Name: "sdk-evpn-1", RedundancyType: "FOUR_IPS_REDUNDANCY"}).Do()
	require.NoError(t, err)
	got, err := svc.ExternalVpnGateways.Get(more2Project, "sdk-evpn-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-evpn-1", got.Name)
	_, err = svc.ExternalVpnGateways.SetLabels(more2Project, "sdk-evpn-1", &compute.GlobalSetLabelsRequest{Labels: map[string]string{"env": "test"}}).Do()
	require.NoError(t, err)
	list, err := svc.ExternalVpnGateways.List(more2Project).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.ExternalVpnGateways.Delete(more2Project, "sdk-evpn-1").Do()
	require.NoError(t, err)
}

// --- Regional resources ------------------------------------------------

func TestCompute2_ResourcePolicies_CRUD_IAM_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.ResourcePolicies.Insert(more2Project, more2Region, &compute.ResourcePolicy{Name: "sdk-rp-1"}).Do()
	require.NoError(t, err)
	got, err := svc.ResourcePolicies.Get(more2Project, more2Region, "sdk-rp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rp-1", got.Name)

	_, err = svc.ResourcePolicies.Patch(more2Project, more2Region, "sdk-rp-1", &compute.ResourcePolicy{Description: "patched"}).Do()
	require.NoError(t, err)

	_, err = svc.ResourcePolicies.SetIamPolicy(more2Project, more2Region, "sdk-rp-1", &compute.RegionSetPolicyRequest{
		Policy: &compute.Policy{Bindings: []*compute.Binding{{Role: "roles/compute.viewer", Members: []string{"user:example@example.com"}}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.ResourcePolicies.GetIamPolicy(more2Project, more2Region, "sdk-rp-1").Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)

	agg, err := svc.ResourcePolicies.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "regions/"+more2Region)

	_, err = svc.ResourcePolicies.Delete(more2Project, more2Region, "sdk-rp-1").Do()
	require.NoError(t, err)
}

func TestCompute2_NodeTemplates_CRUD_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.NodeTemplates.Insert(more2Project, more2Region, &compute.NodeTemplate{Name: "sdk-nt-1"}).Do()
	require.NoError(t, err)
	got, err := svc.NodeTemplates.Get(more2Project, more2Region, "sdk-nt-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-nt-1", got.Name)
	list, err := svc.NodeTemplates.List(more2Project, more2Region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	agg, err := svc.NodeTemplates.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "regions/"+more2Region)
	_, err = svc.NodeTemplates.Delete(more2Project, more2Region, "sdk-nt-1").Do()
	require.NoError(t, err)
}

func TestCompute2_NotificationEndpoints_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.RegionNotificationEndpoints.Insert(more2Project, more2Region, &compute.NotificationEndpoint{Name: "sdk-ne-1"}).Do()
	require.NoError(t, err)
	got, err := svc.RegionNotificationEndpoints.Get(more2Project, more2Region, "sdk-ne-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-ne-1", got.Name)
	list, err := svc.RegionNotificationEndpoints.List(more2Project, more2Region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.RegionNotificationEndpoints.Delete(more2Project, more2Region, "sdk-ne-1").Do()
	require.NoError(t, err)
}

func TestCompute2_VpnGateways_CRUD_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.VpnGateways.Insert(more2Project, more2Region, &compute.VpnGateway{Name: "sdk-vpngw-1"}).Do()
	require.NoError(t, err)
	got, err := svc.VpnGateways.Get(more2Project, more2Region, "sdk-vpngw-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-vpngw-1", got.Name)
	_, err = svc.VpnGateways.SetLabels(more2Project, more2Region, "sdk-vpngw-1", &compute.RegionSetLabelsRequest{Labels: map[string]string{"env": "test"}}).Do()
	require.NoError(t, err)
	agg, err := svc.VpnGateways.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "regions/"+more2Region)
	_, err = svc.VpnGateways.Delete(more2Project, more2Region, "sdk-vpngw-1").Do()
	require.NoError(t, err)
}

func TestCompute2_VpnTunnels_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.VpnTunnels.Insert(more2Project, more2Region, &compute.VpnTunnel{Name: "sdk-vpnt-1"}).Do()
	require.NoError(t, err)
	got, err := svc.VpnTunnels.Get(more2Project, more2Region, "sdk-vpnt-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-vpnt-1", got.Name)
	list, err := svc.VpnTunnels.List(more2Project, more2Region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.VpnTunnels.Delete(more2Project, more2Region, "sdk-vpnt-1").Do()
	require.NoError(t, err)
}

func TestCompute2_TargetVpnGateways_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.TargetVpnGateways.Insert(more2Project, more2Region, &compute.TargetVpnGateway{Name: "sdk-tvgw-1"}).Do()
	require.NoError(t, err)
	got, err := svc.TargetVpnGateways.Get(more2Project, more2Region, "sdk-tvgw-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-tvgw-1", got.Name)
	list, err := svc.TargetVpnGateways.List(more2Project, more2Region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.TargetVpnGateways.Delete(more2Project, more2Region, "sdk-tvgw-1").Do()
	require.NoError(t, err)
}

func TestCompute2_ServiceAttachments_CRUD_IAM_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.ServiceAttachments.Insert(more2Project, more2Region, &compute.ServiceAttachment{Name: "sdk-sa-1"}).Do()
	require.NoError(t, err)
	got, err := svc.ServiceAttachments.Get(more2Project, more2Region, "sdk-sa-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-sa-1", got.Name)
	_, err = svc.ServiceAttachments.Patch(more2Project, more2Region, "sdk-sa-1", &compute.ServiceAttachment{Description: "patched"}).Do()
	require.NoError(t, err)
	_, err = svc.ServiceAttachments.SetIamPolicy(more2Project, more2Region, "sdk-sa-1", &compute.RegionSetPolicyRequest{
		Policy: &compute.Policy{Bindings: []*compute.Binding{{Role: "roles/compute.viewer", Members: []string{"user:example@example.com"}}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.ServiceAttachments.GetIamPolicy(more2Project, more2Region, "sdk-sa-1").Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
	agg, err := svc.ServiceAttachments.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "regions/"+more2Region)
	_, err = svc.ServiceAttachments.Delete(more2Project, more2Region, "sdk-sa-1").Do()
	require.NoError(t, err)
}

func TestCompute2_PacketMirrorings_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.PacketMirrorings.Insert(more2Project, more2Region, &compute.PacketMirroring{Name: "sdk-pm-1"}).Do()
	require.NoError(t, err)
	got, err := svc.PacketMirrorings.Get(more2Project, more2Region, "sdk-pm-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-pm-1", got.Name)
	_, err = svc.PacketMirrorings.Patch(more2Project, more2Region, "sdk-pm-1", &compute.PacketMirroring{Description: "patched"}).Do()
	require.NoError(t, err)
	_, err = svc.PacketMirrorings.Delete(more2Project, more2Region, "sdk-pm-1").Do()
	require.NoError(t, err)
}

// A packet mirroring policy is only meaningful if the members that decide what
// is mirrored and where it goes survive the round trip: the resolver reads
// mirroredResources, collectorIlb and filter to start the sessions, so a
// dropped member is a policy that mirrors the wrong traffic or none.
func TestCompute2_PacketMirrorings_PolicyShapeRoundTrips(t *testing.T) {
	svc := computeService(t)
	const name = "sdk-pm-policy"
	base := "https://www.googleapis.com/compute/v1/projects/" + more2Project
	policy := &compute.PacketMirroring{
		Name:    name,
		Enable:  "TRUE",
		Network: &compute.PacketMirroringNetworkInfo{Url: base + "/global/networks/sdk-pm-net"},
		CollectorIlb: &compute.PacketMirroringForwardingRuleInfo{
			Url: base + "/regions/" + more2Region + "/forwardingRules/sdk-pm-ilb",
		},
		MirroredResources: &compute.PacketMirroringMirroredResourceInfo{
			Instances: []*compute.PacketMirroringMirroredResourceInfoInstanceInfo{
				{Url: base + "/zones/" + more2Region + "-a/instances/sdk-pm-vm"},
			},
			Subnetworks: []*compute.PacketMirroringMirroredResourceInfoSubnetInfo{
				{Url: base + "/regions/" + more2Region + "/subnetworks/sdk-pm-subnet"},
			},
			Tags: []string{"mirrored"},
		},
		Filter: &compute.PacketMirroringFilter{
			Direction:   "INGRESS",
			IPProtocols: []string{"tcp"},
			CidrRanges:  []string{"10.0.0.0/8"},
		},
		Priority: 1200,
	}
	_, err := svc.PacketMirrorings.Insert(more2Project, more2Region, policy).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.PacketMirrorings.Delete(more2Project, more2Region, name).Do()
	})

	got, err := svc.PacketMirrorings.Get(more2Project, more2Region, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "TRUE", got.Enable)
	assert.Equal(t, int64(1200), got.Priority)
	require.NotNil(t, got.CollectorIlb)
	assert.Equal(t, policy.CollectorIlb.Url, got.CollectorIlb.Url)
	require.NotNil(t, got.Network)
	assert.Equal(t, policy.Network.Url, got.Network.Url)
	require.NotNil(t, got.MirroredResources)
	require.Len(t, got.MirroredResources.Instances, 1)
	assert.Equal(t, policy.MirroredResources.Instances[0].Url, got.MirroredResources.Instances[0].Url)
	require.Len(t, got.MirroredResources.Subnetworks, 1)
	assert.Equal(t, []string{"mirrored"}, got.MirroredResources.Tags)
	require.NotNil(t, got.Filter)
	assert.Equal(t, "INGRESS", got.Filter.Direction)
	assert.Equal(t, []string{"tcp"}, got.Filter.IPProtocols)
	assert.Equal(t, []string{"10.0.0.0/8"}, got.Filter.CidrRanges)
	assert.Equal(t, "compute#packetMirroring", got.Kind)
	assert.Contains(t, got.SelfLink, "/regions/"+more2Region+"/packetMirrorings/"+name)

	// Disabling is how an operator stops a policy mirroring without deleting
	// it, so the member the resolver reads has to change on a patch.
	_, err = svc.PacketMirrorings.Patch(more2Project, more2Region, name,
		&compute.PacketMirroring{Enable: "FALSE"}).Do()
	require.NoError(t, err)
	got, err = svc.PacketMirrorings.Get(more2Project, more2Region, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "FALSE", got.Enable)

	agg, err := svc.PacketMirrorings.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "regions/"+more2Region)

	perms, err := svc.PacketMirrorings.TestIamPermissions(more2Project, more2Region, name,
		&compute.TestPermissionsRequest{Permissions: []string{"compute.packetMirrorings.get"}}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"compute.packetMirrorings.get"}, perms.Permissions)
}

func TestCompute2_NetworkAttachments_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.NetworkAttachments.Insert(more2Project, more2Region, &compute.NetworkAttachment{Name: "sdk-na-1"}).Do()
	require.NoError(t, err)
	got, err := svc.NetworkAttachments.Get(more2Project, more2Region, "sdk-na-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-na-1", got.Name)
	_, err = svc.NetworkAttachments.Patch(more2Project, more2Region, "sdk-na-1", &compute.NetworkAttachment{Description: "patched"}).Do()
	require.NoError(t, err)
	_, err = svc.NetworkAttachments.Delete(more2Project, more2Region, "sdk-na-1").Do()
	require.NoError(t, err)
}

func TestCompute2_RegionSecurityPolicies_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.RegionSecurityPolicies.Insert(more2Project, more2Region, &compute.SecurityPolicy{Name: "sdk-rsp-1"}).Do()
	require.NoError(t, err)
	got, err := svc.RegionSecurityPolicies.Get(more2Project, more2Region, "sdk-rsp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rsp-1", got.Name)
	_, err = svc.RegionSecurityPolicies.Patch(more2Project, more2Region, "sdk-rsp-1", &compute.SecurityPolicy{Description: "patched"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionSecurityPolicies.Delete(more2Project, more2Region, "sdk-rsp-1").Do()
	require.NoError(t, err)
}

func TestCompute2_RegionSslCertificates_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.RegionSslCertificates.Insert(more2Project, more2Region, &compute.SslCertificate{Name: "sdk-rsc-1"}).Do()
	require.NoError(t, err)
	got, err := svc.RegionSslCertificates.Get(more2Project, more2Region, "sdk-rsc-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rsc-1", got.Name)
	list, err := svc.RegionSslCertificates.List(more2Project, more2Region).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.RegionSslCertificates.Delete(more2Project, more2Region, "sdk-rsc-1").Do()
	require.NoError(t, err)
}

func TestCompute2_RegionTargetHttpsProxies_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.RegionTargetHttpsProxies.Insert(more2Project, more2Region, &compute.TargetHttpsProxy{Name: "sdk-rthp-1"}).Do()
	require.NoError(t, err)
	got, err := svc.RegionTargetHttpsProxies.Get(more2Project, more2Region, "sdk-rthp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rthp-1", got.Name)
	_, err = svc.RegionTargetHttpsProxies.SetUrlMap(more2Project, more2Region, "sdk-rthp-1", &compute.UrlMapReference{UrlMap: "url-map-ref"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionTargetHttpsProxies.Delete(more2Project, more2Region, "sdk-rthp-1").Do()
	require.NoError(t, err)
}

func TestCompute2_RegionSslPolicies_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.RegionSslPolicies.Insert(more2Project, more2Region, &compute.SslPolicy{Name: "sdk-rssl-1", MinTlsVersion: "TLS_1_2"}).Do()
	require.NoError(t, err)
	got, err := svc.RegionSslPolicies.Get(more2Project, more2Region, "sdk-rssl-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rssl-1", got.Name)
	_, err = svc.RegionSslPolicies.Patch(more2Project, more2Region, "sdk-rssl-1", &compute.SslPolicy{Description: "patched"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionSslPolicies.Delete(more2Project, more2Region, "sdk-rssl-1").Do()
	require.NoError(t, err)
}

// --- Zonal resources ---------------------------------------------------

func TestCompute2_Autoscalers_CRUD_PatchUpdate(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Autoscalers.Insert(more2Project, more2Zone, &compute.Autoscaler{Name: "sdk-as-1"}).Do()
	require.NoError(t, err)
	got, err := svc.Autoscalers.Get(more2Project, more2Zone, "sdk-as-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-as-1", got.Name)

	// autoscalers.patch / update target the autoscaler by query param.
	_, err = svc.Autoscalers.Patch(more2Project, more2Zone, &compute.Autoscaler{Name: "sdk-as-1", Description: "patched"}).Autoscaler("sdk-as-1").Do()
	require.NoError(t, err)
	_, err = svc.Autoscalers.Update(more2Project, more2Zone, &compute.Autoscaler{Name: "sdk-as-1", Description: "updated"}).Autoscaler("sdk-as-1").Do()
	require.NoError(t, err)
	got2, err := svc.Autoscalers.Get(more2Project, more2Zone, "sdk-as-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "updated", got2.Description)

	list, err := svc.Autoscalers.List(more2Project, more2Zone).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.Autoscalers.Delete(more2Project, more2Zone, "sdk-as-1").Do()
	require.NoError(t, err)
}

func TestCompute2_NodeGroups_CRUD_IAM_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.NodeGroups.Insert(more2Project, more2Zone, 0, &compute.NodeGroup{Name: "sdk-ng-1"}).Do()
	require.NoError(t, err)
	got, err := svc.NodeGroups.Get(more2Project, more2Zone, "sdk-ng-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-ng-1", got.Name)
	_, err = svc.NodeGroups.Patch(more2Project, more2Zone, "sdk-ng-1", &compute.NodeGroup{Description: "patched"}).Do()
	require.NoError(t, err)
	_, err = svc.NodeGroups.SetIamPolicy(more2Project, more2Zone, "sdk-ng-1", &compute.ZoneSetPolicyRequest{
		Policy: &compute.Policy{Bindings: []*compute.Binding{{Role: "roles/compute.viewer", Members: []string{"user:example@example.com"}}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.NodeGroups.GetIamPolicy(more2Project, more2Zone, "sdk-ng-1").Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
	agg, err := svc.NodeGroups.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "zones/"+more2Zone)
	_, err = svc.NodeGroups.Delete(more2Project, more2Zone, "sdk-ng-1").Do()
	require.NoError(t, err)
}

func TestCompute2_Reservations_CRUD_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.Reservations.Insert(more2Project, more2Zone, &compute.Reservation{Name: "sdk-rsv-1"}).Do()
	require.NoError(t, err)
	got, err := svc.Reservations.Get(more2Project, more2Zone, "sdk-rsv-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-rsv-1", got.Name)
	agg, err := svc.Reservations.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "zones/"+more2Zone)
	_, err = svc.Reservations.Delete(more2Project, more2Zone, "sdk-rsv-1").Do()
	require.NoError(t, err)
}

func TestCompute2_StoragePools_CRUD_IAM_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.StoragePools.Insert(more2Project, more2Zone, &compute.StoragePool{Name: "sdk-sp-1"}).Do()
	require.NoError(t, err)
	got, err := svc.StoragePools.Get(more2Project, more2Zone, "sdk-sp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-sp-1", got.Name)
	_, err = svc.StoragePools.SetIamPolicy(more2Project, more2Zone, "sdk-sp-1", &compute.ZoneSetPolicyRequest{
		Policy: &compute.Policy{Bindings: []*compute.Binding{{Role: "roles/compute.viewer", Members: []string{"user:example@example.com"}}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.StoragePools.GetIamPolicy(more2Project, more2Zone, "sdk-sp-1").Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
	agg, err := svc.StoragePools.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "zones/"+more2Zone)
	_, err = svc.StoragePools.Delete(more2Project, more2Zone, "sdk-sp-1").Do()
	require.NoError(t, err)
}

func TestCompute2_TargetInstances_CRUD(t *testing.T) {
	svc := computeService(t)
	_, err := svc.TargetInstances.Insert(more2Project, more2Zone, &compute.TargetInstance{Name: "sdk-ti-1"}).Do()
	require.NoError(t, err)
	got, err := svc.TargetInstances.Get(more2Project, more2Zone, "sdk-ti-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-ti-1", got.Name)
	list, err := svc.TargetInstances.List(more2Project, more2Zone).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.TargetInstances.Delete(more2Project, more2Zone, "sdk-ti-1").Do()
	require.NoError(t, err)
}

func TestCompute2_FutureReservations_CRUD_Aggregated(t *testing.T) {
	svc := computeService(t)
	_, err := svc.FutureReservations.Insert(more2Project, more2Zone, &compute.FutureReservation{Name: "sdk-fr-1"}).Do()
	require.NoError(t, err)
	got, err := svc.FutureReservations.Get(more2Project, more2Zone, "sdk-fr-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-fr-1", got.Name)
	agg, err := svc.FutureReservations.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "zones/"+more2Zone)
	_, err = svc.FutureReservations.Delete(more2Project, more2Zone, "sdk-fr-1").Do()
	require.NoError(t, err)
}

func TestCompute2_InstantSnapshots_CRUD_SetLabels(t *testing.T) {
	svc := computeService(t)
	_, err := svc.InstantSnapshots.Insert(more2Project, more2Zone, &compute.InstantSnapshot{Name: "sdk-isnap-1"}).Do()
	require.NoError(t, err)
	got, err := svc.InstantSnapshots.Get(more2Project, more2Zone, "sdk-isnap-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-isnap-1", got.Name)
	_, err = svc.InstantSnapshots.SetLabels(more2Project, more2Zone, "sdk-isnap-1", &compute.ZoneSetLabelsRequest{Labels: map[string]string{"env": "test"}}).Do()
	require.NoError(t, err)
	list, err := svc.InstantSnapshots.List(more2Project, more2Zone).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.Items), 1)
	_, err = svc.InstantSnapshots.Delete(more2Project, more2Zone, "sdk-isnap-1").Do()
	require.NoError(t, err)
}

// --- Read-only catalogs ------------------------------------------------

func TestCompute2_NodeTypes_Catalog(t *testing.T) {
	svc := computeService(t)
	list, err := svc.NodeTypes.List(more2Project, more2Zone).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.Items)
	name := list.Items[0].Name
	got, err := svc.NodeTypes.Get(more2Project, more2Zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	agg, err := svc.NodeTypes.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "zones/"+more2Zone)
}

func TestCompute2_StoragePoolTypes_Catalog(t *testing.T) {
	svc := computeService(t)
	list, err := svc.StoragePoolTypes.List(more2Project, more2Zone).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.Items)
	name := list.Items[0].Name
	got, err := svc.StoragePoolTypes.Get(more2Project, more2Zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	agg, err := svc.StoragePoolTypes.AggregatedList(more2Project).Do()
	require.NoError(t, err)
	assert.Contains(t, agg.Items, "zones/"+more2Zone)
}

func TestCompute2_NetworkProfiles_Catalog(t *testing.T) {
	svc := computeService(t)
	list, err := svc.NetworkProfiles.List(more2Project).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.Items)
	name := list.Items[0].Name
	got, err := svc.NetworkProfiles.Get(more2Project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
}

func TestCompute2_RegionDiskTypes_Catalog(t *testing.T) {
	svc := computeService(t)
	list, err := svc.RegionDiskTypes.List(more2Project, more2Region).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.Items)
	name := list.Items[0].Name
	got, err := svc.RegionDiskTypes.Get(more2Project, more2Region, name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
}
