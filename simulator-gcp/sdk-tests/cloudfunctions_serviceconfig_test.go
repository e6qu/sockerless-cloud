package gcp_sdk_test

import (
	"testing"

	"cloud.google.com/go/functions/apiv2/functionspb"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A function's serviceConfig instance bounds and Serverless VPC Access
// connector reach the template of the Cloud Run service that serves it, and
// read back on the function.
func TestCloudFunctions_ServiceConfigScalingAndConnectorReachTheService(t *testing.T) {
	t.Parallel()
	client := newFunctionsClient(t)
	fn := deployFunction(t, client, "sdk-fn-serviceconfig", &runpb.Container{Image: httpProbeImageName, Args: []string{"log-request"}})
	const connector = "projects/test-project/locations/us-central1/connectors/fn-connector"

	op, err := client.UpdateFunction(ctx, &functionspb.UpdateFunctionRequest{
		Function: &functionspb.Function{
			Name: fn.Name,
			ServiceConfig: &functionspb.ServiceConfig{
				MaxInstanceCount:           3,
				VpcConnector:               connector,
				VpcConnectorEgressSettings: functionspb.ServiceConfig_ALL_TRAFFIC,
			},
		},
	})
	require.NoError(t, err)
	updated, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, connector, updated.ServiceConfig.VpcConnector)
	assert.Equal(t, functionspb.ServiceConfig_ALL_TRAFFIC, updated.ServiceConfig.VpcConnectorEgressSettings)
	assert.Equal(t, int32(3), updated.ServiceConfig.MaxInstanceCount)

	svc, err := newServicesClient(t).GetService(ctx, &runpb.GetServiceRequest{Name: fn.ServiceConfig.Service})
	require.NoError(t, err)
	require.NotNil(t, svc.Template.Scaling)
	assert.Equal(t, int32(3), svc.Template.Scaling.MaxInstanceCount)
	require.NotNil(t, svc.Template.VpcAccess)
	assert.Equal(t, connector, svc.Template.VpcAccess.Connector)
	assert.Equal(t, runpb.VpcAccess_ALL_TRAFFIC, svc.Template.VpcAccess.Egress)
}
