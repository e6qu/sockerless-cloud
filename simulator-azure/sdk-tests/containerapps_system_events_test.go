package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/require"
)

// A revision's provisioning writes the system log messages Container Apps
// documents, as each step happens: its creation, its provisioning, the traffic
// it takes, and, in single-revision mode, the deactivation of the one before.
func TestSDK_ContainerAppsRevisions_SystemLogEvents(t *testing.T) {
	t.Parallel()
	const rg, name = "aca-logs-rg", "sdk-revision-events"
	envID, customerID := acaLogsEnvironment(t)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)

	put := func(arg string) string {
		t.Helper()
		poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.ContainerApp{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.ContainerAppProperties{
				EnvironmentID: to.Ptr(envID),
				Template: &armappcontainers.Template{
					Containers: []*armappcontainers.Container{{
						Name: to.Ptr("main"), Image: to.Ptr(evalImageName), Args: []*string{to.Ptr(arg)},
					}},
					Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
				},
			},
		}, nil)
		require.NoError(t, err)
		app, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return ptrVal(app.Properties.LatestRevisionName)
	}
	t.Cleanup(func() {
		if poller, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = poller.PollUntilDone(ctx, nil)
		}
	})
	logged := func(message string) {
		t.Helper()
		kql := `ContainerAppSystemLogs_CL | where ContainerAppName_s == "` + name + `" and Log_s == "` + message + `"`
		require.Eventually(t, func() bool {
			result := queryWorkspace(t, customerID, kql)
			return len(result.Tables) == 1 && len(result.Tables[0].Rows) == 1
		}, 60*time.Second, 200*time.Millisecond, "the system log carries %q once", message)
	}

	first := put("1 + 1")
	logged("Creating a new revision: " + first)
	logged("Successfully provisioned revision " + first)
	logged("Setting a traffic weight of 100% for revision " + first)

	second := put("2 + 2")
	require.NotEqual(t, first, second)
	logged("Creating a new revision: " + second)
	logged("Successfully provisioned revision " + second)
	logged("Deactivating old revisions since 'ActiveRevisionsMode=Single'")
}
