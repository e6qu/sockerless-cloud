package azure_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createContainerApp(t *testing.T, client *armappcontainers.ContainerAppsClient, rg, name string, app armappcontainers.ContainerApp) armappcontainers.ContainerApp {
	t.Helper()
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, app, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	return created.ContainerApp
}

func listContainerAppReplicas(t *testing.T, rg, app, revision string) []*armappcontainers.Replica {
	t.Helper()
	client, err := armappcontainers.NewContainerAppsRevisionReplicasClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	listed, err := client.ListReplicas(ctx, rg, app, revision, nil)
	require.NoError(t, err)
	return listed.Value
}

func replicaNames(replicas []*armappcontainers.Replica) []string {
	names := make([]string, 0, len(replicas))
	for _, r := range replicas {
		names = append(names, ptrVal(r.Name))
	}
	return names
}

// A write that changes no revision-scope field leaves the revision's replicas
// running as they were.
func TestSDK_ContainerAppsReplicas_AnUnchangedTemplateKeepsTheReplicas(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-keep-rg"), uniqueName("acakeep")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created := createContainerApp(t, client, rg, name, servingContainerApp(rg, "kept", nil))
	revision := ptrVal(created.Properties.LatestRevisionName)
	before := replicaNames(listContainerAppReplicas(t, rg, name, revision))
	require.Len(t, before, 1)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, servingContainerApp(rg, "kept", nil), nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	updateContainerApp(t, client, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{
			Configuration: &armappcontainers.Configuration{MaxInactiveRevisions: to.Ptr[int32](5)},
		},
	})
	assert.Equal(t, before, replicaNames(listContainerAppReplicas(t, rg, name, revision)),
		"neither a repeated PUT nor an application-scope change restarts the replica")
	assert.Equal(t, "kept", containerAppIngressGet(t, ptrVal(created.Properties.Configuration.Ingress.Fqdn)))
}

// A replica whose container exits restarts in place: the revision keeps the
// replica, under its name, and counts the container's restarts.
func TestSDK_ContainerAppsReplicas_AnExitedContainerRestartsInPlace(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-restart-rg"), uniqueName("acarestart")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created := createContainerApp(t, client, rg, name, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
				"/providers/Microsoft.App/managedEnvironments/sim-env"),
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{
					Name:    to.Ptr("main"),
					Image:   to.Ptr("mirror.gcr.io/library/alpine:latest"),
					Command: []*string{to.Ptr("/bin/sh"), to.Ptr("-c"), to.Ptr("sleep 1; exit 3")},
				}},
				Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
			},
		},
	})
	revision := ptrVal(created.Properties.LatestRevisionName)
	first := listContainerAppReplicas(t, rg, name, revision)
	require.Len(t, first, 1)

	// Container Apps offers nothing to wait on for a restart; read the
	// replica as the portal does, once a second, through the first restart's
	// ten-second back-off.
	deadline := time.Now().Add(90 * time.Second)
	for {
		replicas := listContainerAppReplicas(t, rg, name, revision)
		require.Len(t, replicas, 1, "the revision keeps its one replica across the restart")
		assert.Equal(t, ptrVal(first[0].Name), ptrVal(replicas[0].Name))
		containers := replicas[0].Properties.Containers
		require.Len(t, containers, 1)
		if ptrVal(containers[0].RestartCount) >= 1 {
			break
		}
		require.True(t, time.Now().Before(deadline), "the container never restarted: %+v", containers[0])
		time.Sleep(time.Second)
	}
}

// Ingress spreads requests across the revision's replicas; each replica
// answers as the pod it is, whose hostname is the replica's name.
func TestSDK_ContainerAppsReplicas_IngressSpreadsAcrossReplicas(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-spread-rg"), uniqueName("acaspread")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	app := servingContainerApp(rg, "", nil)
	app.Properties.Template.Containers[0].Args = []*string{to.Ptr("hostname-http"), to.Ptr("80")}
	app.Properties.Template.Scale = &armappcontainers.Scale{MinReplicas: to.Ptr[int32](2), MaxReplicas: to.Ptr[int32](2)}
	created := createContainerApp(t, client, rg, name, app)
	replicas := replicaNames(listContainerAppReplicas(t, rg, name, ptrVal(created.Properties.LatestRevisionName)))
	require.Len(t, replicas, 2)

	answered := map[string]bool{}
	for range 4 {
		answered[containerAppIngressGet(t, ptrVal(created.Properties.Configuration.Ingress.Fqdn))] = true
	}
	assert.ElementsMatch(t, replicas, keys(answered), "four requests reach both replicas, each named as its pod")
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A persistent simulator that restarts takes over the replicas a revision
// ran: the replica keeps its name and keeps answering through ingress.
func TestSDK_ContainerAppsReplicas_ReplicasSurviveASimulatorRestart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	port := freeAzureSimPort(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := startPersistentAzureSimulatorOn(t, "docker", stateDir, port)
	t.Cleanup(func() { _ = shutdownAzureSimulator(cmd) })

	rg, name := uniqueName("sdk-aca-adopt-rg"), uniqueName("acaadopt")
	bearer, err := fetchAccessTokenFrom(endpoint, "test-client-id", "test-client-secret", "https://management.azure.com/.default")
	require.NoError(t, err)
	resp := restartRawReq(t, "PUT", endpoint, "/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		"", bearer, []byte(`{"location":"eastus"}`), map[string]string{"Content-Type": "application/json"})
	resp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)

	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &restartCredential{endpoint: endpoint}, restartClientOpts(endpoint))
	require.NoError(t, err)
	replicasClient, err := armappcontainers.NewContainerAppsRevisionReplicasClient(subscriptionID, &restartCredential{endpoint: endpoint}, restartClientOpts(endpoint))
	require.NoError(t, err)
	app := servingContainerApp(rg, "", nil)
	app.Properties.Template.Containers[0].Args = []*string{to.Ptr("hostname-http"), to.Ptr("80")}
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, app, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	revision := ptrVal(created.Properties.LatestRevisionName)
	fqdn := ptrVal(created.Properties.Configuration.Ingress.Fqdn)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	listed := func() []string {
		got, err := replicasClient.ListReplicas(ctx, rg, name, revision, nil)
		require.NoError(t, err)
		return replicaNames(got.Value)
	}
	ingress := func() string {
		resp := restartRawReq(t, "GET", endpoint, "/", fqdn, "", nil, nil)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "ingress: %s", body)
		return string(body)
	}
	before := listed()
	require.Len(t, before, 1)
	assert.Equal(t, before[0], ingress())

	require.NoError(t, shutdownAzureSimulator(cmd))
	cmd = startPersistentAzureSimulatorOn(t, "docker", stateDir, port)

	assert.Equal(t, before, listed(), "the restarted simulator holds the replica it ran")
	assert.Equal(t, before[0], ingress(), "the replica it took over answers through ingress")
}
