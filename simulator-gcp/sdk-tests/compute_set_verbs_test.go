package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The "set<Thing>" verbs each write one member onto a resource and answer with
// an Operation, so every assertion here reads the resource back — an Operation
// says nothing about what was stored.

func TestCompute_TargetHttpsProxySetVerbs(t *testing.T) {
	svc := computeService(t)
	const project, name = "set-verbs", "fronted"

	_, err := svc.TargetHttpsProxies.Insert(project, &compute.TargetHttpsProxy{
		Name: name, UrlMap: "global/urlMaps/original",
	}).Do()
	require.NoError(t, err)

	_, err = svc.TargetHttpsProxies.SetSslPolicy(project, name,
		&compute.SslPolicyReference{SslPolicy: "global/sslPolicies/modern"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetHttpsProxies.SetQuicOverride(project, name,
		&compute.TargetHttpsProxiesSetQuicOverrideRequest{QuicOverride: "ENABLE"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetHttpsProxies.SetCertificateMap(project, name,
		&compute.TargetHttpsProxiesSetCertificateMapRequest{CertificateMap: "//certificatemanager.googleapis.com/maps/m"}).Do()
	require.NoError(t, err)

	got, err := svc.TargetHttpsProxies.Get(project, name).Do()
	require.NoError(t, err)
	assert.Contains(t, got.SslPolicy, "modern")
	assert.Equal(t, "ENABLE", got.QuicOverride)
	assert.Contains(t, got.CertificateMap, "maps/m")
	assert.Contains(t, got.UrlMap, "original", "a set-verb leaves the members it did not name alone")

	// A body without the member the verb sets is refused rather than storing
	// an empty one.
	_, err = svc.TargetHttpsProxies.SetSslPolicy(project, name, &compute.SslPolicyReference{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sslPolicy")

	// And a proxy that is not there cannot be set.
	_, err = svc.TargetHttpsProxies.SetQuicOverride(project, "absent",
		&compute.TargetHttpsProxiesSetQuicOverrideRequest{QuicOverride: "NONE"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCompute_TargetTcpProxySetVerbs(t *testing.T) {
	svc := computeService(t)
	const project, name = "set-verbs-tcp", "tcp-front"

	_, err := svc.TargetTcpProxies.Insert(project, &compute.TargetTcpProxy{Name: name}).Do()
	require.NoError(t, err)

	_, err = svc.TargetTcpProxies.SetBackendService(project, name,
		&compute.TargetTcpProxiesSetBackendServiceRequest{Service: "global/backendServices/pool"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetTcpProxies.SetProxyHeader(project, name,
		&compute.TargetTcpProxiesSetProxyHeaderRequest{ProxyHeader: "PROXY_V1"}).Do()
	require.NoError(t, err)

	got, err := svc.TargetTcpProxies.Get(project, name).Do()
	require.NoError(t, err)
	assert.Contains(t, got.Service, "pool")
	assert.Equal(t, "PROXY_V1", got.ProxyHeader)
}

// A backend bucket's edge security policy arrives as a SecurityPolicyReference
// — the body says securityPolicy, and the bucket stores edgeSecurityPolicy.
func TestCompute_BackendBucketEdgeSecurityPolicyLandsOnItsOwnMember(t *testing.T) {
	svc := computeService(t)
	const project, name = "set-verbs-bucket", "assets"

	_, err := svc.BackendBuckets.Insert(project, &compute.BackendBucket{
		Name: name, BucketName: "assets-bucket",
	}).Do()
	require.NoError(t, err)

	_, err = svc.BackendBuckets.SetEdgeSecurityPolicy(project, name,
		&compute.SecurityPolicyReference{SecurityPolicy: "global/securityPolicies/edge"}).Do()
	require.NoError(t, err)

	got, err := svc.BackendBuckets.Get(project, name).Do()
	require.NoError(t, err)
	assert.Contains(t, got.EdgeSecurityPolicy, "edge")
}

func TestCompute_RegionalSetVerbs(t *testing.T) {
	svc := computeService(t)
	const project, region = "set-verbs-region", "us-central1"

	_, err := svc.RegionBackendServices.Insert(project, region,
		&compute.BackendService{Name: "regional-pool"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionBackendServices.SetSecurityPolicy(project, region, "regional-pool",
		&compute.SecurityPolicyReference{SecurityPolicy: "regions/" + region + "/securityPolicies/armour"}).Do()
	require.NoError(t, err)
	backend, err := svc.RegionBackendServices.Get(project, region, "regional-pool").Do()
	require.NoError(t, err)
	assert.Contains(t, backend.SecurityPolicy, "armour")

	_, err = svc.RegionTargetHttpProxies.Insert(project, region,
		&compute.TargetHttpProxy{Name: "regional-front", UrlMap: "regions/" + region + "/urlMaps/first"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionTargetHttpProxies.SetUrlMap(project, region, "regional-front",
		&compute.UrlMapReference{UrlMap: "regions/" + region + "/urlMaps/second"}).Do()
	require.NoError(t, err)
	proxy, err := svc.RegionTargetHttpProxies.Get(project, region, "regional-front").Do()
	require.NoError(t, err)
	assert.Contains(t, proxy.UrlMap, "second")
}

func TestCompute_TargetInstanceSecurityPolicy(t *testing.T) {
	svc := computeService(t)
	const project, zone, name = "set-verbs-target", "us-central1-a", "forwarded"

	_, err := svc.TargetInstances.Insert(project, zone, &compute.TargetInstance{Name: name}).Do()
	require.NoError(t, err)
	_, err = svc.TargetInstances.SetSecurityPolicy(project, zone, name,
		&compute.SecurityPolicyReference{SecurityPolicy: "global/securityPolicies/armour"}).Do()
	require.NoError(t, err)

	got, err := svc.TargetInstances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Contains(t, got.SecurityPolicy, "armour")
}

func TestCompute_SubnetworkPrivateGoogleAccess(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, region, network = "set-verbs-subnet", "us-central1", "custom"

	op, err := svc.Networks.Insert(project, &compute.Network{Name: network}).Do()
	require.NoError(t, err)
	require.NotNil(t, op)
	_, err = svc.Subnetworks.Insert(project, region, &compute.Subnetwork{
		Name: "workers", IpCidrRange: "10.31.0.0/24",
		Network: "projects/" + project + "/global/networks/" + network,
	}).Do()
	require.NoError(t, err)

	got, err := svc.Subnetworks.Get(project, region, "workers").Do()
	require.NoError(t, err)
	assert.False(t, got.PrivateIpGoogleAccess, "a subnetwork starts without Private Google Access")

	_, err = svc.Subnetworks.SetPrivateIpGoogleAccess(project, region, "workers",
		&compute.SubnetworksSetPrivateIpGoogleAccessRequest{PrivateIpGoogleAccess: true}).Do()
	require.NoError(t, err)
	got, err = svc.Subnetworks.Get(project, region, "workers").Do()
	require.NoError(t, err)
	assert.True(t, got.PrivateIpGoogleAccess)

	_, err = svc.Subnetworks.SetPrivateIpGoogleAccess(project, region, "absent",
		&compute.SubnetworksSetPrivateIpGoogleAccessRequest{PrivateIpGoogleAccess: true}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// An instance records a Cloud Armor policy on the access configs of the
// interfaces the request names, which is the member the Instance schema
// carries it on.
func TestCompute_InstanceSecurityPolicyLandsOnItsAccessConfigs(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "set-verbs-instance", "us-central1-a", "armoured"
	createVerbInstance(t, svc, project, zone, name)

	// No external address yet, so there is nothing to apply the policy to and
	// the verb says so rather than passing silently.
	_, err := svc.Instances.SetSecurityPolicy(project, zone, name,
		&compute.InstancesSetSecurityPolicyRequest{
			SecurityPolicy: "global/securityPolicies/armour",
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "external address")

	_, err = svc.Instances.AddAccessConfig(project, zone, name, "nic0",
		&compute.AccessConfig{Name: "external-nat", Type: "ONE_TO_ONE_NAT"}).Do()
	require.NoError(t, err)

	_, err = svc.Instances.SetSecurityPolicy(project, zone, name,
		&compute.InstancesSetSecurityPolicyRequest{
			NetworkInterfaces: []string{"nic0"},
			SecurityPolicy:    "global/securityPolicies/armour",
		}).Do()
	require.NoError(t, err)

	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, got.NetworkInterfaces, 1)
	require.Len(t, got.NetworkInterfaces[0].AccessConfigs, 1)
	assert.Contains(t, got.NetworkInterfaces[0].AccessConfigs[0].SecurityPolicy, "armour")
}
