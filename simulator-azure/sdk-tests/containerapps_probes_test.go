package azure_sdk_test

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthApp is an app whose container answers response on / and reports its
// health on /healthz until a POST to /unhealthy, with probes probing it.
func healthApp(rg, response string, probes ...*armappcontainers.ContainerAppProbe) armappcontainers.ContainerApp {
	app := servingContainerApp(rg, response, nil)
	main := app.Properties.Template.Containers[0]
	main.Args = []*string{to.Ptr("health-http"), to.Ptr("80"), to.Ptr(response)}
	main.Probes = probes
	return app
}

func healthProbe(kind armappcontainers.Type) *armappcontainers.ContainerAppProbe {
	return &armappcontainers.ContainerAppProbe{
		Type:             to.Ptr(kind),
		HTTPGet:          &armappcontainers.ContainerAppProbeHTTPGet{Port: to.Ptr[int32](80), Path: to.Ptr("/healthz")},
		PeriodSeconds:    to.Ptr[int32](1),
		FailureThreshold: to.Ptr[int32](1),
	}
}

func containerAppIngressStatus(t *testing.T, method, host, path string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// awaitReplicaContainer reads the revision's one replica as the portal does,
// once a second, until its main container satisfies done; Container Apps
// offers nothing to wait on for a probe's verdict.
func awaitReplicaContainer(t *testing.T, rg, app, revision string, done func(*armappcontainers.ReplicaContainer) bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		replicas := listContainerAppReplicas(t, rg, app, revision)
		require.Len(t, replicas, 1)
		require.NotEmpty(t, replicas[0].Properties.Containers)
		if done(replicas[0].Properties.Containers[0]) {
			return
		}
		require.True(t, time.Now().Before(deadline), "the replica never changed: %+v", replicas[0].Properties.Containers[0])
		time.Sleep(time.Second)
	}
}

// A container whose readiness probe fails stops being ready, and ingress
// sends its replica no requests.
func TestSDK_ContainerAppsProbes_ReadinessTakesAReplicaOutOfIngress(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-ready-rg"), uniqueName("acaready")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created := createContainerApp(t, client, rg, name, healthApp(rg, "ready", healthProbe(armappcontainers.TypeReadiness)))
	fqdn, revision := ptrVal(created.Properties.Configuration.Ingress.Fqdn), ptrVal(created.Properties.LatestRevisionName)
	assert.Equal(t, "ready", containerAppIngressGet(t, fqdn))
	awaitReplicaContainer(t, rg, name, revision, func(c *armappcontainers.ReplicaContainer) bool { return ptrVal(c.Ready) })

	require.Equal(t, http.StatusOK, containerAppIngressStatus(t, http.MethodPost, fqdn, "/unhealthy"))
	awaitReplicaContainer(t, rg, name, revision, func(c *armappcontainers.ReplicaContainer) bool { return !ptrVal(c.Ready) })
	assert.Equal(t, http.StatusBadGateway, containerAppIngressStatus(t, http.MethodGet, fqdn, "/"),
		"ingress has no ready replica to send the request to")
}

// A container whose liveness probe fails restarts, and the restarted
// container serves again.
func TestSDK_ContainerAppsProbes_LivenessRestartsTheContainer(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-live-rg"), uniqueName("acalive")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created := createContainerApp(t, client, rg, name, healthApp(rg, "alive", healthProbe(armappcontainers.TypeLiveness)))
	fqdn, revision := ptrVal(created.Properties.Configuration.Ingress.Fqdn), ptrVal(created.Properties.LatestRevisionName)

	require.Equal(t, http.StatusOK, containerAppIngressStatus(t, http.MethodPost, fqdn, "/unhealthy"))
	awaitReplicaContainer(t, rg, name, revision, func(c *armappcontainers.ReplicaContainer) bool {
		return ptrVal(c.RestartCount) >= 1 && ptrVal(c.Ready)
	})
	assert.Equal(t, "alive", containerAppIngressGet(t, fqdn), "the restarted container is healthy again")
}

// A replica runs its init containers to completion before its containers;
// an init container that fails fails the revision.
func TestSDK_ContainerAppsProbes_InitContainersRunFirst(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-init-rg"), uniqueName("acainit")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	app := servingContainerApp(rg, "served", nil)
	app.Properties.Template.InitContainers = []*armappcontainers.InitContainer{{
		Name:  to.Ptr("setup"),
		Image: to.Ptr(commandImageName),
		Args:  []*string{to.Ptr("print"), to.Ptr("initialized")},
	}}
	created := createContainerApp(t, client, rg, name, app)
	revision := ptrVal(created.Properties.LatestRevisionName)
	assert.Equal(t, "served", containerAppIngressGet(t, ptrVal(created.Properties.Configuration.Ingress.Fqdn)))

	replicas := listContainerAppReplicas(t, rg, name, revision)
	require.Len(t, replicas, 1)
	require.Len(t, replicas[0].Properties.InitContainers, 1)
	setup := replicas[0].Properties.InitContainers[0]
	assert.Equal(t, "setup", ptrVal(setup.Name))
	assert.Equal(t, armappcontainers.ContainerAppContainerRunningStateTerminated, ptrVal(setup.RunningState))
	assert.Equal(t, "Completed", ptrVal(setup.RunningStateDetails))
	lines := readContainerAppConsole(t, client, rg, name, revision, "setup", func(line string) bool { return line == "initialized" })
	assert.Contains(t, lines, "initialized")

	failing := servingContainerApp(rg, "never", nil)
	failing.Properties.Template.InitContainers = []*armappcontainers.InitContainer{{
		Name:    to.Ptr("setup"),
		Image:   to.Ptr("mirror.gcr.io/library/alpine:latest"),
		Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr("exit 4")},
	}}
	failingName := uniqueName("acainitfail")
	poller, err := client.BeginCreateOrUpdate(ctx, rg, failingName, failing, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, failingName, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	_, err = poller.PollUntilDone(ctx, nil)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "an init container that fails fails the revision: %v", err)
	assert.Equal(t, "ContainerAppOperationError", respErr.ErrorCode)
}

// A container names each probe type once and gives each probe one handler.
func TestSDK_ContainerAppsProbes_InvalidProbesAreRefused(t *testing.T) {
	t.Parallel()
	rg := uniqueName("sdk-aca-badprobe-rg")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	for name, probes := range map[string][]*armappcontainers.ContainerAppProbe{
		"twice": {healthProbe(armappcontainers.TypeReadiness), healthProbe(armappcontainers.TypeReadiness)},
		"both handlers": {{
			Type:      to.Ptr(armappcontainers.TypeLiveness),
			HTTPGet:   &armappcontainers.ContainerAppProbeHTTPGet{Port: to.Ptr[int32](80)},
			TCPSocket: &armappcontainers.ContainerAppProbeTCPSocket{Port: to.Ptr[int32](80)},
		}},
		"liveness success threshold": {{
			Type:             to.Ptr(armappcontainers.TypeLiveness),
			TCPSocket:        &armappcontainers.ContainerAppProbeTCPSocket{Port: to.Ptr[int32](80)},
			SuccessThreshold: to.Ptr[int32](2),
		}},
	} {
		_, err := client.BeginCreateOrUpdate(ctx, rg, uniqueName("acabadprobe"), healthApp(rg, "x", probes...), nil)
		var respErr *azcore.ResponseError
		require.True(t, errors.As(err, &respErr), "%s: %v", name, err)
		assert.Equal(t, http.StatusBadRequest, respErr.StatusCode, name)
	}
}
