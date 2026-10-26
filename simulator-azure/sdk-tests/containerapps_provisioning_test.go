package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A write answers before its revision provisions; a revision whose replicas
// fail to start fails the operation and leaves the app Failed, still serving
// from the revision that was ready before.
func TestSDK_ContainerAppsApps_ProvisioningSettlesAfterTheWrite(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-provisioning-rg"
	ensureRG(t, rg)
	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	revisionsClient, err := armappcontainers.NewContainerAppsRevisionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	const name = "sdk-provisioning-app"
	app := func(command ...string) armappcontainers.ContainerApp {
		return armappcontainers.ContainerApp{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.ContainerAppProperties{
				EnvironmentID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
					"/providers/Microsoft.App/managedEnvironments/sim-env"),
				Configuration: &armappcontainers.Configuration{
					ActiveRevisionsMode: to.Ptr(armappcontainers.ActiveRevisionsModeSingle),
					Ingress:             &armappcontainers.Ingress{External: to.Ptr(false), TargetPort: to.Ptr[int32](8080)},
				},
				Template: &armappcontainers.Template{
					Containers: []*armappcontainers.Container{{
						Name:    to.Ptr("main"),
						Image:   to.Ptr("mirror.gcr.io/library/alpine:latest"),
						Command: to.SliceOfPtrs(command...),
					}},
					Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
				},
			},
		}
	}
	t.Cleanup(func() {
		if poller, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = poller.PollUntilDone(ctx, nil)
		}
	})

	var initial *http.Response
	poller, err := client.BeginCreateOrUpdate(runtime.WithCaptureResponse(ctx, &initial), rg, name,
		app("nc", "-lk", "-p", "8080", "-e", "true"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, initial.StatusCode)
	body, err := runtime.Payload(initial)
	require.NoError(t, err)
	var accepted struct {
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(body, &accepted))
	assert.Equal(t, "InProgress", accepted.Properties.ProvisioningState, "the create answers before its revision provisions")
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armappcontainers.ContainerAppProvisioningStateSucceeded, ptrVal(created.Properties.ProvisioningState))
	ready := ptrVal(created.Properties.LatestReadyRevisionName)
	require.NotEmpty(t, ready)
	assert.Equal(t, ready, ptrVal(created.Properties.LatestRevisionName))

	// The startup probe on the ingress target port fails when the container exits
	// before it listens.
	poller, err = client.BeginCreateOrUpdate(ctx, rg, name, app("sh", "-c", "exit 3"), nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr, "the write's operation fails with the revision")
	assert.Equal(t, "ContainerAppOperationError", respErr.ErrorCode)

	failedApp, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err, "the app outlives its failed revision")
	assert.Equal(t, armappcontainers.ContainerAppProvisioningStateFailed, ptrVal(failedApp.Properties.ProvisioningState))
	assert.Equal(t, ready, ptrVal(failedApp.Properties.LatestReadyRevisionName))
	failedName := ptrVal(failedApp.Properties.LatestRevisionName)
	require.NotEqual(t, ready, failedName)

	failed, err := revisionsClient.GetRevision(ctx, rg, name, failedName, nil)
	require.NoError(t, err)
	assert.Equal(t, armappcontainers.RevisionProvisioningStateFailed, ptrVal(failed.Properties.ProvisioningState))
	previous, err := revisionsClient.GetRevision(ctx, rg, name, ready, nil)
	require.NoError(t, err)
	assert.True(t, ptrVal(previous.Properties.Active), "the ready revision keeps serving")
	assert.Equal(t, armappcontainers.RevisionProvisioningStateProvisioned, ptrVal(previous.Properties.ProvisioningState))
	assert.EqualValues(t, 100, ptrVal(previous.Properties.TrafficWeight))
}
