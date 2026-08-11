package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
// cancel the operation. The round-trip confirms the DELETE /v1/operations/{id}
// route is served and the empty response decodes cleanly through the SDK.
func TestServiceUsage_OperationDelete(t *testing.T) {
	svc := serviceUsageService(t)

	// Operation resources in serviceusage are named operations/{id}; the SDK
	// expands the {+name} template against this global collection.
	_, err := svc.Operations.Delete("operations/test-op-id").Do()
	require.NoError(t, err)
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
	// Immutable network field is preserved across the update.
	assert.Equal(t, "default", conn.Network)

	_, _ = svc.Projects.Locations.Connectors.Delete(connName).Do()
}
