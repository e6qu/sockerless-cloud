package gcp_sdk_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	serviceusage "google.golang.org/api/serviceusage/v1"
	vpcaccess "google.golang.org/api/vpcaccess/v1"
)

func serviceUsageService(t *testing.T) *serviceusage.Service {
	t.Helper()
	svc, err := serviceusage.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func vpcAccessService(t *testing.T) *vpcaccess.Service {
	t.Helper()
	svc, err := vpcaccess.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// TestServiceUsage_OperationDelete: operations.delete returns
// google.protobuf.Empty and does not cancel the operation, but the record is
// gone. Enabling a service mints the operation in the top-level
// operations/{id} collection that get and delete address.
func TestServiceUsage_OperationDelete(t *testing.T) {
	svc := serviceUsageService(t)

	enabled, err := svc.Services.Enable(
		"projects/su-op-delete/services/run.googleapis.com",
		&serviceusage.EnableServiceRequest{}).Do()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(enabled.Name, "operations/"),
		"an enable names its operation in the top-level collection, got %q", enabled.Name)

	got, err := svc.Operations.Get(enabled.Name).Do()
	require.NoError(t, err, "the operation the enable returned is gettable by name")
	assert.Equal(t, enabled.Name, got.Name)

	_, err = svc.Operations.Delete(enabled.Name).Do()
	require.NoError(t, err)

	_, err = svc.Operations.Get(enabled.Name).Do()
	require.Error(t, err, "the deleted operation is no longer a record the service holds")
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Code)
}

// TestServiceUsage_GetReportsDisabledUntilEnabled reads a service through
// services.get before, after and once more after it is enabled and disabled:
// Service Usage reports a service the project never enabled as DISABLED.
func TestServiceUsage_GetReportsDisabledUntilEnabled(t *testing.T) {
	t.Parallel()
	svc := serviceUsageService(t)
	const name = "projects/su-get-state/services/pubsub.googleapis.com"

	before, err := svc.Services.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, before.Name)
	assert.Equal(t, "DISABLED", before.State, "a never-enabled service is DISABLED")

	_, err = svc.Services.Enable(name, &serviceusage.EnableServiceRequest{}).Do()
	require.NoError(t, err)
	enabled, err := svc.Services.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ENABLED", enabled.State)

	_, err = svc.Services.Disable(name, &serviceusage.DisableServiceRequest{}).Do()
	require.NoError(t, err)
	disabled, err := svc.Services.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DISABLED", disabled.State)
}

func TestVPCAccess_ConnectorPatch(t *testing.T) {
	t.Parallel()
	svc := vpcAccessService(t)
	parent := "projects/test-project/locations/us-central1"

	createOp, err := svc.Projects.Locations.Connectors.Create(parent, &vpcaccess.Connector{
		Network:      "default",
		IpCidrRange:  "10.8.0.0/28",
		MachineType:  "e2-micro",
		MinInstances: 2,
		MaxInstances: 3,
	}).ConnectorId("patch-conn").Do()
	require.NoError(t, err)
	assert.True(t, createOp.Done)

	connName := parent + "/connectors/patch-conn"

	patchOp, err := svc.Projects.Locations.Connectors.Patch(connName, &vpcaccess.Connector{
		MachineType:  "e2-standard-4",
		MaxInstances: 5,
	}).UpdateMask("machineType,maxInstances").Do()
	require.NoError(t, err)
	assert.True(t, patchOp.Done)

	conn, err := svc.Projects.Locations.Connectors.Get(connName).Do()
	require.NoError(t, err)
	assert.Equal(t, "e2-standard-4", conn.MachineType)
	assert.Equal(t, int64(5), conn.MaxInstances)
	// Scaling fields the patch did not carry keep their created values.
	assert.Equal(t, int64(2), conn.MinInstances,
		"minInstances survives an update that does not carry it")
	// Immutable fields are preserved across the update.
	assert.Equal(t, "default", conn.Network)
	assert.Equal(t, "10.8.0.0/28", conn.IpCidrRange)

	_, _ = svc.Projects.Locations.Connectors.Delete(connName).Do()
}
