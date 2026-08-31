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
