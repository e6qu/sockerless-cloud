package azure_sdk_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Full-fidelity coverage of armappcontainers.Configuration: dapr,
// identitySettings, maxInactiveRevisions, runtime, and service must
// round-trip through create/get/patch exactly as the SDK spells them.

// fullContainerAppConfiguration builds a Configuration with every member
// set to a non-default value, so a dropped field surfaces as a mismatch
// rather than a zero-value coincidence.
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

// assertFullContainerAppConfiguration checks every member
// fullContainerAppConfiguration set, on a Configuration read back from
// the sim.
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

// TestSDK_ContainerAppsApps_ConfigurationFullFidelityRoundTrip creates
// an app whose configuration sets every armappcontainers.Configuration
// member to a non-default value and asserts both the create response and
// a subsequent GET return them all unchanged.
func TestSDK_ContainerAppsApps_ConfigurationFullFidelityRoundTrip(t *testing.T) {
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

// TestSDK_ContainerAppsApps_PatchDaprMemberPreservesSiblings PATCHes a
// single dapr member (RFC 7396 merge) and asserts every sibling dapr
// member and every sibling configuration member survives unchanged. Uses
// raw HTTP because the SDK only exposes CreateOrUpdate (PUT), not PATCH.
func TestSDK_ContainerAppsApps_PatchDaprMemberPreservesSiblings(t *testing.T) {
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

	// Sibling dapr members merge member-wise, not wholesale-replace.
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

	// Sibling configuration members are untouched by a dapr-only patch.
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

// TestSDK_ContainerAppsApps_DaprSidecarServesRealDaprAPI deploys an app
// with dapr enabled and proves the sim runs the real daprd sidecar in
// the replica's network namespace: a client container in the replica
// fetches http://localhost:3500/v1.0/metadata and the response — carried
// through the app's console logs, the same observation path
// TestSDK_ContainerAppsApps_StartsRealReplicaAndLogs uses — reports the
// configured appId and appPort, proving the configuration reached daprd.
func TestSDK_ContainerAppsApps_DaprSidecarServesRealDaprAPI(t *testing.T) {
	rg := "sdk-aca-dapr-exec-rg"
	appName := "sdk-dapr-exec-app"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	// The main container serves HTTP on :8080 (the configured appPort);
	// the client container shares the replica's network namespace and
	// polls the daprd metadata endpoint until the sidecar answers.
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
						Image:   to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
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

	// The daprd metadata response proves the real sidecar is serving the
	// Dapr HTTP API on the replica's localhost with the configured
	// identity: `id` carries the appId, `appConnectionProperties` the
	// appPort and protocol daprd was told to reach the app on. Generous
	// deadline: the first run pulls the pinned daprd image.
	require.Eventually(t, func() bool {
		result := queryWorkspace(t, "default", `ContainerAppConsoleLogs_CL | where ContainerAppName_s == "`+appName+`"`)
		if len(result.Tables) == 0 {
			return false
		}
		var logs strings.Builder
		for _, row := range result.Tables[0].Rows {
			for _, cell := range row {
				if s, ok := cell.(string); ok {
					logs.WriteString(s)
					logs.WriteString("\n")
				}
			}
		}
		joined := logs.String()
		return strings.Contains(joined, `"id":"sdk-dapr-app"`) &&
			strings.Contains(joined, `"port":8080`) &&
			strings.Contains(joined, `"protocol":"http"`)
	}, 90*time.Second, 500*time.Millisecond,
		"expected the replica's client container to read the real daprd metadata endpoint on localhost:3500")
}
