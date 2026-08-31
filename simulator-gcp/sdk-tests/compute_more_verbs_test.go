package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// A router's named sets, a CDN backend's signed-URL keys, and the operations an
// organization-scoped call produces.

func TestCompute_RouterNamedSets(t *testing.T) {
	svc := computeService(t)
	const project, region, router = "named-sets", "us-central1", "border"

	_, err := svc.Routers.Insert(project, region, &compute.Router{
		Name: router, Network: "global/networks/default",
	}).Do()
	require.NoError(t, err)

	// A named set is created by writing it: a route policy names the sets it
	// matches against, so they have to be declarable.
	_, err = svc.Routers.UpdateNamedSet(project, region, router, &compute.NamedSet{
		Name: "trusted", Type: "PREFIX",
		Elements: []*compute.Expr{{Expression: "10.0.0.0/8"}},
	}).Do()
	require.NoError(t, err)

	got, err := svc.Routers.GetNamedSet(project, region, router).NamedSet("trusted").Do()
	require.NoError(t, err)
	require.NotNil(t, got.Resource)
	assert.Equal(t, "trusted", got.Resource.Name)
	require.Len(t, got.Resource.Elements, 1)

	listed, err := svc.Routers.ListNamedSets(project, region, router).Do()
	require.NoError(t, err)
	require.Len(t, listed.Result, 1)

	// A patch merges; the type it did not mention survives.
	_, err = svc.Routers.PatchNamedSet(project, region, router, &compute.NamedSet{
		Name:     "trusted",
		Elements: []*compute.Expr{{Expression: "10.0.0.0/8"}, {Expression: "192.168.0.0/16"}},
	}).Do()
	require.NoError(t, err)
	got, err = svc.Routers.GetNamedSet(project, region, router).NamedSet("trusted").Do()
	require.NoError(t, err)
	require.Len(t, got.Resource.Elements, 2)
	assert.Equal(t, "PREFIX", got.Resource.Type, "a patch keeps what it did not mention")

	_, err = svc.Routers.DeleteNamedSet(project, region, router).NamedSet("trusted").Do()
	require.NoError(t, err)
	_, err = svc.Routers.GetNamedSet(project, region, router).NamedSet("trusted").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Deleting one that is not there says so rather than passing.
	_, err = svc.Routers.DeleteNamedSet(project, region, router).NamedSet("trusted").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// A signed-URL key's value is write-only: Compute Engine reports the names a
// backend holds and never the key material, which is the whole point of it
// being a signing key.
func TestCompute_BackendBucketSignedUrlKeys(t *testing.T) {
	svc := computeService(t)
	const project, name = "signed-keys", "assets"

	_, err := svc.BackendBuckets.Insert(project, &compute.BackendBucket{
		Name: name, BucketName: "assets-bucket", EnableCdn: true,
	}).Do()
	require.NoError(t, err)

	_, err = svc.BackendBuckets.AddSignedUrlKey(project, name, &compute.SignedUrlKey{
		KeyName: "primary", KeyValue: "aaaaaaaaaaaaaaaaaaaaaa==",
	}).Do()
	require.NoError(t, err)

	got, err := svc.BackendBuckets.Get(project, name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.CdnPolicy)
	require.Len(t, got.CdnPolicy.SignedUrlKeyNames, 1)
	assert.Equal(t, "primary", got.CdnPolicy.SignedUrlKeyNames[0])

	// The same name twice is refused rather than shadowing the first.
	_, err = svc.BackendBuckets.AddSignedUrlKey(project, name, &compute.SignedUrlKey{
		KeyName: "primary", KeyValue: "bbbbbbbbbbbbbbbbbbbbbb==",
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has a signed-URL key")

	// A key with no value is not a key.
	_, err = svc.BackendBuckets.AddSignedUrlKey(project, name, &compute.SignedUrlKey{
		KeyName: "empty",
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyValue")

	_, err = svc.BackendBuckets.DeleteSignedUrlKey(project, name, "primary").Do()
	require.NoError(t, err)
	got, err = svc.BackendBuckets.Get(project, name).Do()
	require.NoError(t, err)
	assert.Empty(t, got.CdnPolicy.SignedUrlKeyNames)

	_, err = svc.BackendBuckets.DeleteSignedUrlKey(project, name, "primary").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no signed-URL key")
}

// The operations an organization-scoped call produces are addressed without a
// project, because the resource they acted on has none. They come out of the
// same store every other Compute operation is recorded in.
func TestCompute_OrganizationOperations(t *testing.T) {
	svc := computeService(t)

	listed, err := svc.GlobalOrganizationOperations.List().Do()
	require.NoError(t, err)
	require.NotNil(t, listed)

	// One that was never minted is not found, and deleting it says the same.
	_, err = svc.GlobalOrganizationOperations.Get("operation-absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	err = svc.GlobalOrganizationOperations.Delete("operation-absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// The verbs the global load-balancing collections carry beyond their lifecycle.
func TestCompute_GlobalBackendServiceSecurityAndSigning(t *testing.T) {
	svc := computeService(t)
	const project, name = "lb-verbs", "origin"

	_, err := svc.BackendServices.Insert(project, &compute.BackendService{
		Name: name, Protocol: "HTTP", EnableCDN: true,
	}).Do()
	require.NoError(t, err)

	// A service sits behind a policy at the origin and, for cached content,
	// one at the edge. Both arrive as a SecurityPolicyReference, so the same
	// body member has to land on two different resource members.
	_, err = svc.BackendServices.SetSecurityPolicy(project, name,
		&compute.SecurityPolicyReference{SecurityPolicy: "global/securityPolicies/origin-armour"}).Do()
	require.NoError(t, err)
	_, err = svc.BackendServices.SetEdgeSecurityPolicy(project, name,
		&compute.SecurityPolicyReference{SecurityPolicy: "global/securityPolicies/edge-armour"}).Do()
	require.NoError(t, err)

	got, err := svc.BackendServices.Get(project, name).Do()
	require.NoError(t, err)
	assert.Contains(t, got.SecurityPolicy, "origin-armour")
	assert.Contains(t, got.EdgeSecurityPolicy, "edge-armour")
	assert.Equal(t, "HTTP", got.Protocol, "a set-verb leaves the rest of the service alone")

	// The effective-policies read answers for a service that exists. Compute
	// Engine declares no response for it, so there is nothing to assert but
	// that it succeeds — and that it refuses a service that is not there.
	require.NoError(t, svc.BackendServices.GetEffectiveSecurityPolicies(project, name).Do())
	require.Error(t, svc.BackendServices.GetEffectiveSecurityPolicies(project, "absent").Do())

	_, err = svc.BackendServices.AddSignedUrlKey(project, name, &compute.SignedUrlKey{
		KeyName: "primary", KeyValue: "cccccccccccccccccccccc==",
	}).Do()
	require.NoError(t, err)
	got, err = svc.BackendServices.Get(project, name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.CdnPolicy)
	require.Len(t, got.CdnPolicy.SignedUrlKeyNames, 1)

	_, err = svc.BackendServices.DeleteSignedUrlKey(project, name, "primary").Do()
	require.NoError(t, err)
	got, err = svc.BackendServices.Get(project, name).Do()
	require.NoError(t, err)
	assert.Empty(t, got.CdnPolicy.SignedUrlKeyNames)
}

func TestCompute_ForwardingRuleTargetLabelsAndProxyUrlMap(t *testing.T) {
	svc := computeService(t)
	const project = "lb-more"

	_, err := svc.GlobalForwardingRules.Insert(project, &compute.ForwardingRule{
		Name: "front", IPProtocol: "TCP", PortRange: "443",
	}).Do()
	require.NoError(t, err)

	_, err = svc.GlobalForwardingRules.SetTarget(project, "front",
		&compute.TargetReference{Target: "global/targetHttpsProxies/secure"}).Do()
	require.NoError(t, err)
	_, err = svc.GlobalForwardingRules.SetLabels(project, "front",
		&compute.GlobalSetLabelsRequest{Labels: map[string]string{"team": "edge"}}).Do()
	require.NoError(t, err)
	_, err = svc.GlobalForwardingRules.Patch(project, "front",
		&compute.ForwardingRule{Description: "the front door"}).Do()
	require.NoError(t, err)

	rule, err := svc.GlobalForwardingRules.Get(project, "front").Do()
	require.NoError(t, err)
	assert.Contains(t, rule.Target, "secure")
	assert.Equal(t, "edge", rule.Labels["team"])
	assert.Equal(t, "the front door", rule.Description)
	assert.Equal(t, "443", rule.PortRange, "a patch keeps what it did not mention")

	// A proxy's URL map is set through the spelling Compute Engine declares
	// for it, which carries no scope segment.
	_, err = svc.TargetHttpProxies.Insert(project, &compute.TargetHttpProxy{
		Name: "plain", UrlMap: "global/urlMaps/first",
	}).Do()
	require.NoError(t, err)
	_, err = svc.TargetHttpProxies.SetUrlMap(project, "plain",
		&compute.UrlMapReference{UrlMap: "global/urlMaps/second"}).Do()
	require.NoError(t, err)
	proxy, err := svc.TargetHttpProxies.Get(project, "plain").Do()
	require.NoError(t, err)
	assert.Contains(t, proxy.UrlMap, "second")

	// A HTTPS proxy takes the same two verbs at the unscoped spelling the
	// document declares for them.
	_, err = svc.TargetHttpsProxies.Insert(project, &compute.TargetHttpsProxy{
		Name: "secure", UrlMap: "global/urlMaps/first",
	}).Do()
	require.NoError(t, err)
	_, err = svc.TargetHttpsProxies.SetUrlMap(project, "secure",
		&compute.UrlMapReference{UrlMap: "global/urlMaps/second"}).Do()
	require.NoError(t, err)
	_, err = svc.TargetHttpsProxies.SetSslCertificates(project, "secure",
		&compute.TargetHttpsProxiesSetSslCertificatesRequest{
			SslCertificates: []string{"global/sslCertificates/star"},
		}).Do()
	require.NoError(t, err)
	secure, err := svc.TargetHttpsProxies.Get(project, "secure").Do()
	require.NoError(t, err)
	assert.Contains(t, secure.UrlMap, "second")
	require.Len(t, secure.SslCertificates, 1)

	_, err = svc.TargetHttpProxies.Patch(project, "plain",
		&compute.TargetHttpProxy{Description: "plain http"}).Do()
	require.NoError(t, err)
	proxy, err = svc.TargetHttpProxies.Get(project, "plain").Do()
	require.NoError(t, err)
	assert.Equal(t, "plain http", proxy.Description)
}
