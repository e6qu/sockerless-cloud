package gcp_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// TestIAMEnforcement_ReferencePagePermissionsDecideManagedGroupVerbs checks a
// managed instance group's custom verbs against the permissions their REST
// reference pages state: listManagedInstances needs
// compute.instanceGroupManagers.get, which roles/compute.viewer holds, and
// resize needs compute.instanceGroupManagers.update, which it does not.
func TestIAMEnforcement_ReferencePagePermissionsDecideManagedGroupVerbs(t *testing.T) {
	t.Parallel()
	const project, zone, group = "iam-mig-project", "us-central1-a", "viewed"
	createManagedGroup(t, computeService(t), project, zone, group)
	email, ts := enforcementPrincipal(t, "enf-mig-viewer")
	grantOnProject(t, project, "roles/compute.viewer", "serviceAccount:"+email)

	viewer, err := compute.NewService(ctx, option.WithEndpoint(baseURL+"/compute/v1/"), option.WithTokenSource(ts))
	require.NoError(t, err)
	_, err = viewer.InstanceGroupManagers.ListManagedInstances(project, zone, group).Do()
	require.NoError(t, err, "roles/compute.viewer holds compute.instanceGroupManagers.get")

	_, err = viewer.InstanceGroupManagers.Resize(project, zone, group, 1).Do()
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr), "resize needs compute.instanceGroupManagers.update: %v", err)
	require.Equal(t, http.StatusForbidden, apiErr.Code)
}
