package azure_sdk_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HTTP ingress that names no target port reaches the one port the container
// listens on, and the container reads its revision from the variable
// Container Apps adds.
func TestSDK_ContainerAppsIngress_DetectsTheTargetPort(t *testing.T) {
	t.Parallel()
	rg, name := uniquename.New("sdk-aca-port-rg"), uniquename.New("acaport")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
				"/providers/Microsoft.App/managedEnvironments/sim-env"),
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{
				External: to.Ptr(true),
			}},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{
					Name:  to.Ptr("main"),
					Image: to.Ptr(commandImageName),
					Args:  []*string{to.Ptr("env-http"), to.Ptr("9471"), to.Ptr("CONTAINER_APP_REVISION")},
				}},
				Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	req.Host = ptrVal(created.Properties.Configuration.Ingress.Fqdn)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	revision := ptrVal(created.Properties.LatestRevisionName)
	require.NotEmpty(t, revision)
	assert.Equal(t, revision, strings.TrimSpace(string(body)))
}
