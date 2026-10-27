package azure_sdk_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every armappcontainers.Configuration member (dapr, identitySettings,
// maxInactiveRevisions, runtime, service) round-trips through create, get and
// patch as the SDK spells it.

// fullContainerAppConfiguration sets every member to a non-default value, so a
// dropped field shows as a mismatch, not a zero-value coincidence.
func fullContainerAppConfiguration() *armappcontainers.Configuration {
	return &armappcontainers.Configuration{
		ActiveRevisionsMode:  to.Ptr(armappcontainers.ActiveRevisionsModeMultiple),
		MaxInactiveRevisions: to.Ptr[int32](7),
		Dapr: &armappcontainers.Dapr{
			Enabled:            to.Ptr(true),
			AppID:              to.Ptr("cfg-dapr-app"),
			AppPort:            to.Ptr[int32](9000),
			AppProtocol:        to.Ptr(armappcontainers.AppProtocolGrpc),
			EnableAPILogging:   to.Ptr(true),
			HTTPMaxRequestSize: to.Ptr[int32](16),
			HTTPReadBufferSize: to.Ptr[int32](8),
			LogLevel:           to.Ptr(armappcontainers.LogLevelDebug),
		},
		Runtime: &armappcontainers.Runtime{
			Java: &armappcontainers.RuntimeJava{EnableMetrics: to.Ptr(true)},
		},
		Service: &armappcontainers.Service{Type: to.Ptr("redis")},
		IdentitySettings: []*armappcontainers.IdentitySettings{
			{
				Identity: to.Ptr("/subscriptions/" + subscriptionID +
					"/resourceGroups/cfg-rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/cfg-identity"),
				Lifecycle: to.Ptr(armappcontainers.IdentitySettingsLifeCycleInit),
			},
		},
	}
}

func assertFullContainerAppConfiguration(t *testing.T, cfg *armappcontainers.Configuration) {
	t.Helper()
	require.NotNil(t, cfg)

	require.NotNil(t, cfg.MaxInactiveRevisions)
	assert.Equal(t, int32(7), *cfg.MaxInactiveRevisions)

	require.NotNil(t, cfg.Dapr)
	require.NotNil(t, cfg.Dapr.Enabled)
	assert.True(t, *cfg.Dapr.Enabled)
	require.NotNil(t, cfg.Dapr.AppID)
	assert.Equal(t, "cfg-dapr-app", *cfg.Dapr.AppID)
	require.NotNil(t, cfg.Dapr.AppPort)
	assert.Equal(t, int32(9000), *cfg.Dapr.AppPort)
	require.NotNil(t, cfg.Dapr.AppProtocol)
	assert.Equal(t, armappcontainers.AppProtocolGrpc, *cfg.Dapr.AppProtocol)
	require.NotNil(t, cfg.Dapr.EnableAPILogging)
	assert.True(t, *cfg.Dapr.EnableAPILogging)
	require.NotNil(t, cfg.Dapr.HTTPMaxRequestSize)
	assert.Equal(t, int32(16), *cfg.Dapr.HTTPMaxRequestSize)
	require.NotNil(t, cfg.Dapr.HTTPReadBufferSize)
	assert.Equal(t, int32(8), *cfg.Dapr.HTTPReadBufferSize)
	require.NotNil(t, cfg.Dapr.LogLevel)
	assert.Equal(t, armappcontainers.LogLevelDebug, *cfg.Dapr.LogLevel)

	require.NotNil(t, cfg.Runtime)
	require.NotNil(t, cfg.Runtime.Java)
	require.NotNil(t, cfg.Runtime.Java.EnableMetrics)
	assert.True(t, *cfg.Runtime.Java.EnableMetrics)

	require.NotNil(t, cfg.Service)
	require.NotNil(t, cfg.Service.Type)
	assert.Equal(t, "redis", *cfg.Service.Type)

	require.Len(t, cfg.IdentitySettings, 1)
	require.NotNil(t, cfg.IdentitySettings[0].Identity)
	assert.Contains(t, *cfg.IdentitySettings[0].Identity, "/userAssignedIdentities/cfg-identity")
	require.NotNil(t, cfg.IdentitySettings[0].Lifecycle)
	assert.Equal(t, armappcontainers.IdentitySettingsLifeCycleInit, *cfg.IdentitySettings[0].Lifecycle)
}

// TestSDK_ContainerAppsApps_ConfigurationFullFidelityRoundTrip: the create
// response and a later GET return every member unchanged.
func TestSDK_ContainerAppsApps_ConfigurationFullFidelityRoundTrip(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-cfg-rg"
	appName := "sdk-cfg-app"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	poller, err := client.BeginCreateOrUpdate(ctx, rg, appName, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Configuration: fullContainerAppConfiguration(),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defer func() {
		delPoller, derr := client.BeginDelete(ctx, rg, appName, nil)
		if derr == nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	}()
	require.NotNil(t, created.Properties)
	assertFullContainerAppConfiguration(t, created.Properties.Configuration)

	got, err := client.Get(ctx, rg, appName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assertFullContainerAppConfiguration(t, got.Properties.Configuration)
}

// TestSDK_ContainerAppsApps_PatchDaprMemberPreservesSiblings merge-patches one
// dapr member; every sibling survives. Raw HTTP, because the SDK exposes only
// PUT.
func TestSDK_ContainerAppsApps_PatchDaprMemberPreservesSiblings(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-cfg-patch-rg"
	appName := "sdk-cfg-patch-app"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	poller, err := client.BeginCreateOrUpdate(ctx, rg, appName, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Configuration: fullContainerAppConfiguration(),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defer func() {
		delPoller, derr := client.BeginDelete(ctx, rg, appName, nil)
		if derr == nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	}()

	patchURL := baseURL + "/subscriptions/" + subscriptionID +
		"/resourceGroups/" + rg + "/providers/Microsoft.App/containerApps/" + appName + "?api-version=2025-01-01"
	patchBody := `{"properties":{"configuration":{"dapr":{"logLevel":"warn"}}}}`
	req, err := http.NewRequest("PATCH", patchURL, strings.NewReader(patchBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	patchResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	patchBytes, _ := io.ReadAll(patchResp.Body)
	patchResp.Body.Close()
	require.Equal(t, 200, patchResp.StatusCode, "PATCH response: %s", patchBytes)

	got, err := client.Get(ctx, rg, appName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	cfg := got.Properties.Configuration
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.Dapr)
	require.NotNil(t, cfg.Dapr.LogLevel)
	assert.Equal(t, armappcontainers.LogLevelWarn, *cfg.Dapr.LogLevel,
		"the patched dapr member must take the new value")

	require.NotNil(t, cfg.Dapr.Enabled)
	assert.True(t, *cfg.Dapr.Enabled, "dapr.enabled must survive a logLevel-only merge patch")
	require.NotNil(t, cfg.Dapr.AppID)
	assert.Equal(t, "cfg-dapr-app", *cfg.Dapr.AppID)
	require.NotNil(t, cfg.Dapr.AppPort)
	assert.Equal(t, int32(9000), *cfg.Dapr.AppPort)
	require.NotNil(t, cfg.Dapr.AppProtocol)
	assert.Equal(t, armappcontainers.AppProtocolGrpc, *cfg.Dapr.AppProtocol)
	require.NotNil(t, cfg.Dapr.EnableAPILogging)
	assert.True(t, *cfg.Dapr.EnableAPILogging)
	require.NotNil(t, cfg.Dapr.HTTPMaxRequestSize)
	assert.Equal(t, int32(16), *cfg.Dapr.HTTPMaxRequestSize)
	require.NotNil(t, cfg.Dapr.HTTPReadBufferSize)
	assert.Equal(t, int32(8), *cfg.Dapr.HTTPReadBufferSize)

	require.NotNil(t, cfg.MaxInactiveRevisions)
	assert.Equal(t, int32(7), *cfg.MaxInactiveRevisions)
	require.NotNil(t, cfg.Runtime)
	require.NotNil(t, cfg.Runtime.Java)
	require.NotNil(t, cfg.Runtime.Java.EnableMetrics)
	assert.True(t, *cfg.Runtime.Java.EnableMetrics)
	require.NotNil(t, cfg.Service)
	require.NotNil(t, cfg.Service.Type)
	assert.Equal(t, "redis", *cfg.Service.Type)
	require.Len(t, cfg.IdentitySettings, 1)
}

// TestSDK_ContainerAppsApps_DaprSidecarServesRealDaprAPI: a client container
// in the replica fetches daprd's http://localhost:3500/v1.0/metadata, and the
// response, read from its console stream, reports the configured appId and
// appPort.
func TestSDK_ContainerAppsApps_DaprSidecarServesRealDaprAPI(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-dapr-exec-rg"
	appName := "sdk-dapr-exec-app"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	// The main container serves the appPort, 8080; the client container shares
	// the replica's network namespace and polls daprd until it answers.
	metadataScript := "until wget -q -O - http://127.0.0.1:3500/v1.0/metadata 2>/dev/null; do sleep 0.5; done; echo"

	poller, err := client.BeginCreateOrUpdate(ctx, rg, appName, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Configuration: &armappcontainers.Configuration{
				Dapr: &armappcontainers.Dapr{
					Enabled:            to.Ptr(true),
					AppID:              to.Ptr("sdk-dapr-app"),
					AppPort:            to.Ptr[int32](8080),
					AppProtocol:        to.Ptr(armappcontainers.AppProtocolHTTP),
					EnableAPILogging:   to.Ptr(true),
					HTTPMaxRequestSize: to.Ptr[int32](8),
					HTTPReadBufferSize: to.Ptr[int32](8),
					LogLevel:           to.Ptr(armappcontainers.LogLevelInfo),
				},
			},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{
						Name:  to.Ptr("main"),
						Image: to.Ptr(httpProbeImageName),
						Args:  []*string{to.Ptr("echo-request")},
					},
					{
						Name:    to.Ptr("client"),
						Image:   to.Ptr("mirror.gcr.io/library/alpine:latest"),
						Command: []*string{to.Ptr("/bin/sh")},
						Args:    []*string{to.Ptr("-c"), to.Ptr(metadataScript)},
					},
				},
				Scale: &armappcontainers.Scale{
					MinReplicas: to.Ptr[int32](1),
					MaxReplicas: to.Ptr[int32](1),
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defer func() {
		delPoller, derr := client.BeginDelete(ctx, rg, appName, nil)
		if derr == nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	}()

	// daprd's metadata carries the appId in `id`, and the appPort and protocol in
	// `appConnectionProperties`.
	app, err := client.Get(ctx, rg, appName, nil)
	require.NoError(t, err)
	metadata := func(line string) bool {
		return strings.Contains(line, `"id":"sdk-dapr-app"`) &&
			strings.Contains(line, `"port":8080`) &&
			strings.Contains(line, `"protocol":"http"`)
	}
	lines := readContainerAppConsole(t, client, rg, appName, ptrVal(app.Properties.LatestRevisionName), "client", metadata)
	require.True(t, metadata(lines[len(lines)-1]),
		"expected the replica's client container to read the real daprd metadata endpoint on localhost:3500")
}
