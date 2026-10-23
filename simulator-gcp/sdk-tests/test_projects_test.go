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

var testProjects sync.Map

// ensureProject creates project id through Cloud Resource Manager under the
// test organization, once for the run, so a service that resolves its
// resources' projects finds it.
func ensureProject(t *testing.T, id string) {
	t.Helper()
	once, _ := testProjects.LoadOrStore(id, &sync.Once{})
	var err error
	once.(*sync.Once).Do(func() {
		var c *resourcemanager.ProjectsClient
		c, err = resourcemanager.NewProjectsRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
		if err != nil {
			return
		}
		defer c.Close()
		op, createErr := c.CreateProject(ctx, &resourcemanagerpb.CreateProjectRequest{
			Project: &resourcemanagerpb.Project{ProjectId: id, DisplayName: id, Parent: crmTestOrg},
		})
		if createErr != nil {
			var apiErr *apierror.APIError
			if !errors.As(createErr, &apiErr) || apiErr.HTTPCode() != http.StatusConflict {
				err = createErr
			}
			return
		}
		_, err = op.Wait(ctx)
	})
	require.NoError(t, err, "create project %s", id)
}
