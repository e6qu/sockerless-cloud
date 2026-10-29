package gcp_sdk_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/googleapis/gax-go/v2/apierror"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

type projectCreation struct {
	once sync.Once
	err  error
}

var testProjects sync.Map

// ensureProject creates the project once per run under the test organization:
// a project that does not exist grants nobody anything, so every call into it
// is refused.
func ensureProject(t *testing.T, id string) {
	t.Helper()
	ensureProjectAt(t, baseURL, id)
}

// createTestFolder creates a folder under the test organization and returns
// its name, folders/{id}.
func createTestFolder(t *testing.T, displayName string) string {
	t.Helper()
	c, err := resourcemanager.NewFoldersRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	defer c.Close()
	op, err := c.CreateFolder(ctx, &resourcemanagerpb.CreateFolderRequest{
		Folder: &resourcemanagerpb.Folder{DisplayName: displayName, Parent: crmTestOrg},
	})
	require.NoError(t, err)
	folder, err := op.Wait(ctx)
	require.NoError(t, err)
	return folder.GetName()
}

// createProjectUnder creates a project under a folder.
func createProjectUnder(t *testing.T, id, folder string) {
	t.Helper()
	c, err := resourcemanager.NewProjectsRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	defer c.Close()
	op, err := c.CreateProject(ctx, &resourcemanagerpb.CreateProjectRequest{
		Project: &resourcemanagerpb.Project{ProjectId: id, DisplayName: id, Parent: folder},
	})
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)
}

// ensureProjectAt is ensureProject for a dedicated simulator process.
func ensureProjectAt(t *testing.T, base, id string) {
	t.Helper()
	entry, _ := testProjects.LoadOrStore(base+" "+id, &projectCreation{})
	creation := entry.(*projectCreation)
	creation.once.Do(func() {
		c, err := resourcemanager.NewProjectsRESTClient(ctx, option.WithEndpoint(base), option.WithTokenSource(simTokenSourceFor(base)))
		if err != nil {
			creation.err = err
			return
		}
		defer c.Close()
		op, err := c.CreateProject(ctx, &resourcemanagerpb.CreateProjectRequest{
			Project: &resourcemanagerpb.Project{ProjectId: id, DisplayName: id, Parent: crmTestOrg},
		})
		if err != nil {
			var apiErr *apierror.APIError
			if !errors.As(err, &apiErr) || apiErr.HTTPCode() != http.StatusConflict {
				creation.err = err
			}
			return
		}
		_, creation.err = op.Wait(ctx)
	})
	require.NoError(t, creation.err, "create project %s", id)
}
