package azure_sdk_test

import (
	"io"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/require"
)

// TestContainerApps_ListSecretsEmptyArrayNotNull: listSecrets on an app or job
// with a configuration but no secrets returns the required
// SecretsCollection.value as [], not null. The Go SDK reads both as an empty
// slice, so the check is on the wire, where strict clients break.
func TestContainerApps_ListSecretsEmptyArrayNotNull(t *testing.T) {
	t.Parallel()
	rg := "sdk-secrets-empty-rg"
	ensureRG(t, rg)
	cred := &fakeCredential{}

	appClient, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	// A configuration (ingress) but no secrets.
	appPoller, err := appClient.BeginCreateOrUpdate(ctx, rg, "secretless-app", armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			Configuration: &armappcontainers.Configuration{
				Ingress: &armappcontainers.Ingress{External: to.Ptr(true), TargetPort: to.Ptr(int32(80))},
			},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{Name: to.Ptr("main"), Image: to.Ptr("mirror.gcr.io/library/nginx:latest")}},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = appPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	appPath := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.App/containerApps/secretless-app/listSecrets"
	resp := armReq(t, "POST", appPath, "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Contains(t, string(body), `"value":[]`, "app listSecrets must return [] not null; got %s", string(body))
	require.NotContains(t, string(body), "null", "app listSecrets must not emit null: %s", string(body))

	jobClient, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	jobPoller, err := jobClient.BeginCreateOrUpdate(ctx, rg, "secretless-job", armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.JobProperties{
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout: to.Ptr(int32(60)),
			},
			Template: &armappcontainers.JobTemplate{
				Containers: []*armappcontainers.Container{{Name: to.Ptr("main"), Image: to.Ptr("mirror.gcr.io/library/nginx:latest")}},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = jobPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	jobPath := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.App/jobs/secretless-job/listSecrets"
	jresp := armReq(t, "POST", jobPath, "")
	jbody, _ := io.ReadAll(jresp.Body)
	jresp.Body.Close()
	require.Contains(t, string(jbody), `"value":[]`, "job listSecrets must return [] not null; got %s", string(jbody))
}
