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

// TestServiceUsage_OperationDelete exercises serviceusage.operations.delete:
// the client drops its interest in a long-running operation. Per AIP /
// google.longrunning the call returns google.protobuf.Empty ({}) and does not
// cancel the operation, but the record it dropped is gone.
//
// The operation is one the service really minted — enabling a service names its
// long-running operation in the top-level operations/{id} collection, the same
// collection operations.get and operations.delete address — so the delete is
// asserted against a record the service holds rather than against an invented
// identifier.
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

// TestVPCAccess_ConnectorPatch exercises the connector update path: create a
// connector, PATCH its scaling fields, then read the mutation back.
func TestVPCAccess_ConnectorPatch(t *testing.T) {
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
	// Scaling fields the patch did not carry keep the value the create set —
	// an update that reset them to their zero value would be a data loss the
	// caller never asked for.
	assert.Equal(t, int64(2), conn.MinInstances,
		"minInstances survives an update that does not carry it")
	// Immutable fields are preserved across the update.
	assert.Equal(t, "default", conn.Network)
	assert.Equal(t, "10.8.0.0/28", conn.IpCidrRange)

	_, _ = svc.Projects.Locations.Connectors.Delete(connName).Do()
}
