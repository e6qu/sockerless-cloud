package gcp_sdk_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	run "google.golang.org/api/run/v1"
)

// Cloud Run v1 (Knative-style) child-resource SDK tests. Creating a
// Service auto-materializes a Configuration, a Route, and the first
// Revision (mirroring Cloud Run's server-side Knative reconciliation);
// these tests drive the canonical google.golang.org/api/run/v1 client
// against both the /apis/serving.knative.dev/... namespaces surface and
// the /v1/projects/{p}/locations/{loc}/... regional mirror so the
// simulator's path shapes stay pinned to the Discovery document's.

func newRunV1(t *testing.T) *run.APIService {
	t.Helper()
	svc, err := run.NewService(context.Background(),
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestCloudRunV1_KnativeChildResources_RoundTrip(t *testing.T) {
	svc := newRunV1(t)
	const namespace = "test-project-knative-children"

	created, err := svc.Namespaces.Services.Create("namespaces/"+namespace, &run.Service{
		Metadata: &run.ObjectMeta{Name: "svc-children"},
		Spec: &run.ServiceSpec{Template: &run.RevisionTemplate{
			Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "gcr.io/x/children"}}},
		}},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, created.Status)
	revName := created.Status.LatestCreatedRevisionName
	require.NotEmpty(t, revName)

	t.Cleanup(func() {
		_, _ = svc.Namespaces.Services.Delete("namespaces/" + namespace + "/services/svc-children").Do()
	})

	// Configuration: get + list.
	cfg, err := svc.Namespaces.Configurations.Get("namespaces/" + namespace + "/configurations/svc-children").Do()
	require.NoError(t, err)
	assert.Equal(t, "svc-children", cfg.Metadata.Name)
	require.NotNil(t, cfg.Status)
	assert.Equal(t, revName, cfg.Status.LatestCreatedRevisionName)

	cfgList, err := svc.Namespaces.Configurations.List("namespaces/" + namespace).Do()
	require.NoError(t, err)
	require.Len(t, cfgList.Items, 1)
	assert.Equal(t, "svc-children", cfgList.Items[0].Metadata.Name)

	// Revision: get + list.
	rev, err := svc.Namespaces.Revisions.Get("namespaces/" + namespace + "/revisions/" + revName).Do()
	require.NoError(t, err)
	assert.Equal(t, revName, rev.Metadata.Name)
	require.NotNil(t, rev.Status)
	assert.Equal(t, "svc-children", rev.Status.ServiceName)
	require.Len(t, rev.Spec.Containers, 1)
	assert.Equal(t, "gcr.io/x/children", rev.Spec.Containers[0].Image)

	revList, err := svc.Namespaces.Revisions.List("namespaces/" + namespace).Do()
	require.NoError(t, err)
	require.Len(t, revList.Items, 1)

	// Route: get + list.
	rt, err := svc.Namespaces.Routes.Get("namespaces/" + namespace + "/routes/svc-children").Do()
	require.NoError(t, err)
	assert.Equal(t, "svc-children", rt.Metadata.Name)
	require.NotNil(t, rt.Status)
	assert.NotEmpty(t, rt.Status.Url)

	rtList, err := svc.Namespaces.Routes.List("namespaces/" + namespace).Do()
	require.NoError(t, err)
	require.Len(t, rtList.Items, 1)

	// A second deploy adds a second revision under the same configuration.
	updated, err := svc.Namespaces.Services.ReplaceService("namespaces/"+namespace+"/services/svc-children", &run.Service{
		Metadata: &run.ObjectMeta{Name: "svc-children"},
		Spec: &run.ServiceSpec{Template: &run.RevisionTemplate{
			Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "gcr.io/x/children-v2"}}},
		}},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Metadata.Generation)

	revList2, err := svc.Namespaces.Revisions.List("namespaces/" + namespace).Do()
	require.NoError(t, err)
	require.Len(t, revList2.Items, 2, "second deploy materializes a second revision")

	// Delete the older revision.
	_, err = svc.Namespaces.Revisions.Delete("namespaces/" + namespace + "/revisions/" + revName).Do()
	require.NoError(t, err)
	_, err = svc.Namespaces.Revisions.Get("namespaces/" + namespace + "/revisions/" + revName).Do()
	require.Error(t, err, "deleted revision must be gone")
}

func TestCloudRunV1_DomainMappings_RoundTrip(t *testing.T) {
	svc := newRunV1(t)
	const namespace = "test-project-domainmappings"

	created, err := svc.Namespaces.Domainmappings.Create("namespaces/"+namespace, &run.DomainMapping{
		Metadata: &run.ObjectMeta{Name: "www.example.com"},
		Spec:     &run.DomainMappingSpec{RouteName: "svc-dm"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "www.example.com", created.Metadata.Name)
	require.NotNil(t, created.Status)
	require.NotEmpty(t, created.Status.ResourceRecords)
	assert.Equal(t, "svc-dm", created.Status.MappedRouteName)

	t.Cleanup(func() {
		_, _ = svc.Namespaces.Domainmappings.Delete("namespaces/" + namespace + "/domainmappings/www.example.com").Do()
	})

	got, err := svc.Namespaces.Domainmappings.Get("namespaces/" + namespace + "/domainmappings/www.example.com").Do()
	require.NoError(t, err)
	assert.Equal(t, "www.example.com", got.Metadata.Name)

	list, err := svc.Namespaces.Domainmappings.List("namespaces/" + namespace).Do()
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	_, err = svc.Namespaces.Domainmappings.Delete("namespaces/" + namespace + "/domainmappings/www.example.com").Do()
	require.NoError(t, err)
	_, err = svc.Namespaces.Domainmappings.Get("namespaces/" + namespace + "/domainmappings/www.example.com").Do()
	require.Error(t, err, "deleted domain mapping must be gone")
}

func TestCloudRunV1_AuthorizedDomains_List(t *testing.T) {
	svc := newRunV1(t)
	// Namespaces surface.
	resp, err := svc.Namespaces.Authorizeddomains.List("namespaces/test-project-ad").Do()
	require.NoError(t, err)
	assert.NotNil(t, resp.Domains)
	// projects surface (Projects.Authorizeddomains.List).
	presp, err := svc.Projects.Authorizeddomains.List("projects/test-project-ad").Do()
	require.NoError(t, err)
	assert.NotNil(t, presp.Domains)
}

// TestCloudRunV1_RegionalMirror_RoundTrip drives the
// /v1/projects/{p}/locations/{loc}/... regional Service surface and its
// auto-created children, asserting the regional path shape round-trips.
func TestCloudRunV1_RegionalMirror_RoundTrip(t *testing.T) {
	svc := newRunV1(t)
	const project = "test-project-regional"
	const location = "us-central1"
	parent := "projects/" + project + "/locations/" + location

	created, err := svc.Projects.Locations.Services.Create(parent, &run.Service{
		Metadata: &run.ObjectMeta{Name: "svc-regional"},
		Spec: &run.ServiceSpec{Template: &run.RevisionTemplate{
			Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "gcr.io/x/regional"}}},
		}},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, created.Status)
	revName := created.Status.LatestCreatedRevisionName
	require.NotEmpty(t, revName)

	t.Cleanup(func() {
		_, _ = svc.Projects.Locations.Services.Delete(parent + "/services/svc-regional").Do()
	})

	got, err := svc.Projects.Locations.Services.Get(parent + "/services/svc-regional").Do()
	require.NoError(t, err)
	assert.Equal(t, "svc-regional", got.Metadata.Name)

	list, err := svc.Projects.Locations.Services.List(parent).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Items)

	cfg, err := svc.Projects.Locations.Configurations.Get(parent + "/configurations/svc-regional").Do()
	require.NoError(t, err)
	assert.Equal(t, "svc-regional", cfg.Metadata.Name)

	rev, err := svc.Projects.Locations.Revisions.Get(parent + "/revisions/" + revName).Do()
	require.NoError(t, err)
	assert.Equal(t, revName, rev.Metadata.Name)

	rt, err := svc.Projects.Locations.Routes.Get(parent + "/routes/svc-regional").Do()
	require.NoError(t, err)
	assert.Equal(t, "svc-regional", rt.Metadata.Name)

	dm, err := svc.Projects.Locations.Domainmappings.Create(parent, &run.DomainMapping{
		Metadata: &run.ObjectMeta{Name: "regional.example.com"},
		Spec:     &run.DomainMappingSpec{RouteName: "svc-regional"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "regional.example.com", dm.Metadata.Name)
	_, err = svc.Projects.Locations.Domainmappings.Get(parent + "/domainmappings/regional.example.com").Do()
	require.NoError(t, err)
	dmList, err := svc.Projects.Locations.Domainmappings.List(parent).Do()
	require.NoError(t, err)
	require.NotEmpty(t, dmList.Items)
	_, err = svc.Projects.Locations.Domainmappings.Delete(parent + "/domainmappings/regional.example.com").Do()
	require.NoError(t, err)
}
