package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

// listUsable, through the generated Go client:
//
//	GET /compute/v1/projects/{project}/global/backendServices/listUsable
//	GET /compute/v1/projects/{project}/global/backendBuckets/listUsable
//	GET /compute/v1/projects/{project}/regions/{region}/backendServices/listUsable
//
// The literal segment has to beat the `{name}` get, which would otherwise
// answer `backend service "listUsable" not found`.

func TestCompute_BackendServicesListUsable(t *testing.T) {
	svc := computeService(t)
	const project = "usable-backends"

	_, err := svc.BackendServices.Insert(project, &compute.BackendService{
		Name: "web-backend", Protocol: "HTTP",
	}).Do()
	require.NoError(t, err)

	usable, err := svc.BackendServices.ListUsable(project).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#usableBackendServiceList", usable.Kind)
	require.Len(t, usable.Items, 1)
	assert.Equal(t, "web-backend", usable.Items[0].Name)

	// Another project's backend services are not usable from this one.
	_, err = svc.BackendServices.Insert("other-usable-backends", &compute.BackendService{
		Name: "elsewhere", Protocol: "HTTP",
	}).Do()
	require.NoError(t, err)
	usable, err = svc.BackendServices.ListUsable(project).Do()
	require.NoError(t, err)
	require.Len(t, usable.Items, 1)
	assert.Equal(t, "web-backend", usable.Items[0].Name)
}

func TestCompute_BackendBucketsListUsable(t *testing.T) {
	svc := computeService(t)
	const project = "usable-buckets"

	_, err := svc.BackendBuckets.Insert(project, &compute.BackendBucket{
		Name: "static-assets", BucketName: "assets",
	}).Do()
	require.NoError(t, err)

	usable, err := svc.BackendBuckets.ListUsable(project).Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#usableBackendBucketList", usable.Kind)
	require.Len(t, usable.Items, 1)
	assert.Equal(t, "static-assets", usable.Items[0].Name)
}

func TestCompute_RegionBackendServicesListUsable(t *testing.T) {
	svc := computeService(t)
	const project = "usable-region-backends"

	_, err := svc.RegionBackendServices.Insert(project, "us-central1", &compute.BackendService{
		Name: "regional-backend", Protocol: "HTTP",
	}).Do()
	require.NoError(t, err)

	usable, err := svc.RegionBackendServices.ListUsable(project, "us-central1").Do()
	require.NoError(t, err)
	assert.Equal(t, "compute#usableBackendServiceList", usable.Kind)
	require.Len(t, usable.Items, 1)
	assert.Equal(t, "regional-backend", usable.Items[0].Name)

	// The list is scoped to its region, not to the project.
	usable, err = svc.RegionBackendServices.ListUsable(project, "europe-west1").Do()
	require.NoError(t, err)
	assert.Empty(t, usable.Items)
}
