package gcp_sdk_test

// Project-lifecycle round-trips over both Cloud Resource Manager API
// versions, driven by the two official Go clients real consumers use:
// cloud.google.com/go/resourcemanager/apiv3 (the GAPIC ProjectsClient with
// its long-running-operation poller) and google.golang.org/api/
// cloudresourcemanager/v1 (the wire gcloud's `projects` commands and
// terraform-provider-google's google_project resource speak), plus the
// Cloud Billing project billing-info read google_project's terraform Read
// performs.

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
	crmv1 "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const crmTestOrg = "organizations/123456789012"

var crmProjectNumberName = regexp.MustCompile(`^projects/[1-9][0-9]{11}$`)

func projectsV3Client(t *testing.T) *resourcemanager.ProjectsClient {
	t.Helper()
	c, err := resourcemanager.NewProjectsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestResourceManagerProjects_APIv3Lifecycle(t *testing.T) {
	c := projectsV3Client(t)
	const projectID = "apiv3-proj-lifecycle"

	op, err := c.CreateProject(ctx, &resourcemanagerpb.CreateProjectRequest{
		Project: &resourcemanagerpb.Project{
			ProjectId:   projectID,
			DisplayName: "APIv3 Lifecycle",
			Parent:      crmTestOrg,
			Labels:      map[string]string{"env": "sdk-test"},
		},
	})
	require.NoError(t, err)
	created, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, projectID, created.GetProjectId())
	assert.Equal(t, resourcemanagerpb.Project_ACTIVE, created.GetState())
	assert.Regexp(t, crmProjectNumberName, created.GetName(),
		"v3 resource name must be the projects/{projectNumber} form")

	// Resuming the LRO by name polls GET /v3/operations/{operation}.
	resumed := c.CreateProjectOperation(op.Name())
	polled, err := resumed.Poll(ctx)
	require.NoError(t, err)
	assert.True(t, resumed.Done())
	assert.Equal(t, projectID, polled.GetProjectId())

	// Get by project ID and by project number resolve to the same project.
	got, err := c.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: "projects/" + projectID})
	require.NoError(t, err)
	assert.Equal(t, created.GetName(), got.GetName())
	byNumber, err := c.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: created.GetName()})
	require.NoError(t, err)
	assert.Equal(t, projectID, byNumber.GetProjectId())

	// Duplicate project ID is a real 409 ALREADY_EXISTS.
	dupOp, err := c.CreateProject(ctx, &resourcemanagerpb.CreateProjectRequest{
		Project: &resourcemanagerpb.Project{ProjectId: projectID},
	})
	if err == nil {
		_, err = dupOp.Wait(ctx)
	}
	require.Error(t, err)
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 409, gerr.Code)

	// ListProjects under the org surfaces the project.
	var listed []string
	it := c.ListProjects(ctx, &resourcemanagerpb.ListProjectsRequest{Parent: crmTestOrg})
	for {
		p, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		listed = append(listed, p.GetProjectId())
	}
	assert.Contains(t, listed, projectID)

	// SearchProjects finds it by ID query.
	sit := c.SearchProjects(ctx, &resourcemanagerpb.SearchProjectsRequest{Query: "id:apiv3-proj-*"})
	found := false
	for {
		p, err := sit.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		if p.GetProjectId() == projectID {
			found = true
		}
	}
	assert.True(t, found, "SearchProjects should match id:apiv3-proj-*")

	// The created project's {project} path segment works against an
	// existing per-service route: create a service account in it.
	iamSvc, err := iam.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	sa, err := iamSvc.Projects.ServiceAccounts.Create("projects/"+projectID, &iam.CreateServiceAccountRequest{
		AccountId:      "apiv3-proj-sa",
		ServiceAccount: &iam.ServiceAccount{DisplayName: "APIv3 project SA"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, projectID, sa.ProjectId)
	assert.True(t, strings.HasPrefix(sa.Email, "apiv3-proj-sa@"+projectID+"."), "SA email carries the new project ID: %s", sa.Email)

	// Delete soft-deletes into DELETE_REQUESTED with a deleteTime.
	dop, err := c.DeleteProject(ctx, &resourcemanagerpb.DeleteProjectRequest{Name: "projects/" + projectID})
	require.NoError(t, err)
	deleted, err := dop.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, resourcemanagerpb.Project_DELETE_REQUESTED, deleted.GetState())
	got, err = c.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: "projects/" + projectID})
	require.NoError(t, err)
	assert.Equal(t, resourcemanagerpb.Project_DELETE_REQUESTED, got.GetState())
	assert.NotNil(t, got.GetDeleteTime())

	// A pending-deletion project is omitted from ListProjects by default
	// and returned with showDeleted.
	it = c.ListProjects(ctx, &resourcemanagerpb.ListProjectsRequest{Parent: crmTestOrg})
	for {
		p, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		assert.NotEqual(t, projectID, p.GetProjectId(), "DELETE_REQUESTED project must not list by default")
	}
	foundDeleted := false
	it = c.ListProjects(ctx, &resourcemanagerpb.ListProjectsRequest{Parent: crmTestOrg, ShowDeleted: true})
	for {
		p, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		if p.GetProjectId() == projectID {
			foundDeleted = true
		}
	}
	assert.True(t, foundDeleted, "showDeleted must surface the DELETE_REQUESTED project")

	// Deleting again requires ACTIVE: a real 400 FAILED_PRECONDITION.
	dop2, err := c.DeleteProject(ctx, &resourcemanagerpb.DeleteProjectRequest{Name: "projects/" + projectID})
	if err == nil {
		_, err = dop2.Wait(ctx)
	}
	require.Error(t, err)
	gerr = nil
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 400, gerr.Code)

	// Undelete restores ACTIVE.
	uop, err := c.UndeleteProject(ctx, &resourcemanagerpb.UndeleteProjectRequest{Name: "projects/" + projectID})
	require.NoError(t, err)
	restored, err := uop.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, resourcemanagerpb.Project_ACTIVE, restored.GetState())
}

func TestResourceManagerProjects_APIv3UnknownProjectDenied(t *testing.T) {
	c := projectsV3Client(t)
	_, err := c.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: "projects/never-created-proj"})
	require.Error(t, err)
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 403, gerr.Code, "project existence is never disclosed: a missing project reads as PERMISSION_DENIED")
}

func crmV1Service(t *testing.T) *crmv1.Service {
	t.Helper()
	svc, err := crmv1.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestResourceManagerProjects_V1Lifecycle(t *testing.T) {
	svc := crmV1Service(t)
	const projectID = "v1-proj-lifecycle"

	op, err := svc.Projects.Create(&crmv1.Project{
		ProjectId: projectID,
		Name:      "V1 Lifecycle",
		Parent:    &crmv1.ResourceId{Type: "organization", Id: "123456789012"},
		Labels:    map[string]string{"env": "sdk-test"},
	}).Do()
	require.NoError(t, err)

	// gcloud's create waiter and terraform's ResourceManagerOperationWaiter
	// poll the operation by name.
	polled, err := svc.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.True(t, polled.Done)

	got, err := svc.Projects.Get(projectID).Do()
	require.NoError(t, err)
	assert.Equal(t, projectID, got.ProjectId)
	assert.Equal(t, "ACTIVE", got.LifecycleState)
	assert.Equal(t, "V1 Lifecycle", got.Name)
	assert.Greater(t, got.ProjectNumber, int64(0), "v1 read must carry a real project number")
	require.NotNil(t, got.Parent)
	assert.Equal(t, "organization", got.Parent.Type)

	// gcloud projects list always sends the lifecycleState filter.
	list, err := svc.Projects.List().Filter("lifecycleState:ACTIVE AND id:v1-proj-*").Do()
	require.NoError(t, err)
	require.Len(t, list.Projects, 1)
	assert.Equal(t, projectID, list.Projects[0].ProjectId)

	// gcloud projects update does a read-modify-write PUT.
	got.Name = "V1 Renamed"
	updated, err := svc.Projects.Update(projectID, got).Do()
	require.NoError(t, err)
	assert.Equal(t, "V1 Renamed", updated.Name)

	// v1 project IAM colon-verbs address the same per-project policy.
	setPol, err := svc.Projects.SetIamPolicy(projectID, &crmv1.SetIamPolicyRequest{
		Policy: &crmv1.Policy{
			Bindings: []*crmv1.Binding{{Role: "roles/viewer", Members: []string{"user:v1@example.com"}}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, setPol.Bindings, 1)
	gotPol, err := svc.Projects.GetIamPolicy(projectID, &crmv1.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, gotPol.Bindings, 1)
	assert.Equal(t, "roles/viewer", gotPol.Bindings[0].Role)

	// The billing read google_project's terraform Read performs: a project
	// with no billing account reads billingEnabled=false.
	billingSvc, err := cloudbilling.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	bi, err := billingSvc.Projects.GetBillingInfo("projects/" + projectID).Do()
	require.NoError(t, err)
	assert.Equal(t, projectID, bi.ProjectId)
	assert.False(t, bi.BillingEnabled)
	assert.Empty(t, bi.BillingAccountName)

	// Duplicate create is a real 409.
	_, err = svc.Projects.Create(&crmv1.Project{ProjectId: projectID}).Do()
	require.Error(t, err)
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 409, gerr.Code)

	// Soft delete: DELETE_REQUESTED, excluded by gcloud's ACTIVE filter,
	// still readable, then undeletable.
	_, err = svc.Projects.Delete(projectID).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Get(projectID).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETE_REQUESTED", got.LifecycleState)
	list, err = svc.Projects.List().Filter("lifecycleState:ACTIVE AND id:v1-proj-*").Do()
	require.NoError(t, err)
	assert.Empty(t, list.Projects, "gcloud's ACTIVE filter must exclude the pending-deletion project")
	_, err = svc.Projects.Undelete(projectID, &crmv1.UndeleteProjectRequest{}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Get(projectID).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", got.LifecycleState)

	// A never-created project reads as a real 403.
	_, err = svc.Projects.Get("v1-never-created-proj").Do()
	require.Error(t, err)
	gerr = nil
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 403, gerr.Code)
}
