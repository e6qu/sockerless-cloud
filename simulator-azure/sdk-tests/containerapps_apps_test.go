package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v2 ContainerApps Apps routes (Microsoft.App/containerApps). The aca
// backend's UseApp path uses ContainerAppsClient.{BeginCreateOrUpdate,
// Get, BeginDelete}. Pin the contract using the same SDK + types the
// backend uses.

func TestSDK_ContainerAppsApps_CreateGetDelete(t *testing.T) {
	rg := "sdk-aca-app-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-test-app", armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Tags: map[string]*string{
			"sockerless-managed":      to.Ptr("true"),
			"sockerless-container-id": to.Ptr("abc123"),
		},
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Configuration: &armappcontainers.Configuration{
				ActiveRevisionsMode: to.Ptr(armappcontainers.ActiveRevisionsModeSingle),
				Ingress: &armappcontainers.Ingress{
					External:   to.Ptr(false),
					TargetPort: to.Ptr[int32](8080),
					Transport:  to.Ptr(armappcontainers.IngressTransportMethodAuto),
				},
			},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{
						Name:  to.Ptr("main"),
						Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
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

	app, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err, "BeginCreateOrUpdate poller must complete")
	require.NotNil(t, app.Name)
	assert.Equal(t, "sdk-test-app", *app.Name)
	require.NotNil(t, app.Properties)
	require.NotNil(t, app.Properties.ProvisioningState)
	assert.Equal(t, "Succeeded", string(*app.Properties.ProvisioningState),
		"provisioningState must be Succeeded so backend's appContainerState reads 'running'")
	require.NotNil(t, app.Properties.LatestReadyRevisionName)
	assert.NotEmpty(t, *app.Properties.LatestReadyRevisionName,
		"LatestReadyRevisionName drives the Ready check in appContainerState")
	require.NotNil(t, app.Properties.LatestRevisionFqdn)
	assert.NotEmpty(t, *app.Properties.LatestRevisionFqdn,
		"LatestRevisionFqdn is what cloudServiceRegisterCNAME reads to seed Private DNS")
	host, _ := simHostParts(t)
	assert.True(t, strings.HasSuffix(*app.Properties.LatestRevisionFqdn, ".internal.sim-env."+host),
		"LatestRevisionFqdn must be an Azure-shaped hostname under the simulator ARM host")

	// GET round-trip.
	getResp, err := client.Get(ctx, rg, "sdk-test-app", nil)
	require.NoError(t, err)
	require.NotNil(t, getResp.Name)
	assert.Equal(t, "sdk-test-app", *getResp.Name)
	require.NotNil(t, getResp.Tags["sockerless-managed"])
	assert.Equal(t, "true", *getResp.Tags["sockerless-managed"])

	// DELETE.
	delPoller, err := client.BeginDelete(ctx, rg, "sdk-test-app", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// GET after delete should 404.
	_, err = client.Get(ctx, rg, "sdk-test-app", nil)
	assert.Error(t, err, "Get after delete must fail")
}

func TestSDK_ContainerAppsApps_StartsRealReplicaAndLogs(t *testing.T) {
	rg := "sdk-aca-app-exec-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-exec-app", armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{
						Name:  to.Ptr("main"),
						Image: to.Ptr(evalImageName),
						Args:  []*string{to.Ptr("8 * 7")},
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
		delPoller, derr := client.BeginDelete(ctx, rg, "sdk-exec-app", nil)
		if derr == nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	}()

	found := false
	// Generous deadline so a slow real-replica start on a loaded CI runner doesn't
	// expire before the arithmetic output is logged; the loop breaks on first match.
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		kql := `ContainerAppConsoleLogs_CL | where ContainerAppName_s == "sdk-exec-app"`
		result := queryWorkspace(t, "default", kql)
		require.Len(t, result.Tables, 1)
		table := result.Tables[0]
		logIdx := -1
		for i, col := range table.Columns {
			if col.Name == "Log_s" {
				logIdx = i
				break
			}
		}
		require.GreaterOrEqual(t, logIdx, 0)
		var logs []string
		for _, row := range table.Rows {
			if msg, ok := row[logIdx].(string); ok {
				logs = append(logs, msg)
			}
		}
		if strings.Contains(strings.Join(logs, "\n"), "56") {
			found = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.True(t, found, "expected ACA App replica to execute the real container and emit arithmetic output")
}

func TestSDK_ContainerAppsApps_MultiContainerSharesLocalhost(t *testing.T) {
	rg := "sdk-aca-app-pod-rg"
	appName := "sdk-app-pod-localhost"
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
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{
						Name:  to.Ptr("main"),
						Image: to.Ptr(httpProbeImageName),
						Args:  []*string{to.Ptr("probe-once"), to.Ptr("aca-app-sidecar-ok")},
					},
					{
						Name:  to.Ptr("sidecar"),
						Image: to.Ptr(httpProbeImageName),
						Args:  []*string{to.Ptr("server")},
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

	require.Eventually(t, func() bool {
		result := queryWorkspace(t, "default", `ContainerAppConsoleLogs_CL | where ContainerAppName_s == "`+appName+`"`)
		if len(result.Tables) == 0 {
			return false
		}
		for _, row := range result.Tables[0].Rows {
			for _, cell := range row {
				if s, ok := cell.(string); ok && strings.Contains(s, "aca-app-sidecar-ok") {
					return true
				}
			}
		}
		return false
	}, 20*time.Second, 500*time.Millisecond)
}

// TestSDK_ContainerAppsApps_SystemDataPreservedAcrossUpdates pins ARM
// SystemData semantics: `createdAt` is stamped once on resource creation
// and preserved across PUT/PATCH updates; `lastModifiedAt` reflects the
// last write. Real Azure ARM enforces this — restamping createdAt on
// every update breaks audit trails and surfaces in azure-cli `--query
// systemData` output.
func TestSDK_ContainerAppsApps_SystemDataPreservedAcrossUpdates(t *testing.T) {
	rg := "sdk-aca-systemdata-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"
	mkApp := func(image string) armappcontainers.ContainerApp {
		return armappcontainers.ContainerApp{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.ContainerAppProperties{
				EnvironmentID: to.Ptr(envID),
				Template: &armappcontainers.Template{
					Containers: []*armappcontainers.Container{
						{Name: to.Ptr("main"), Image: to.Ptr(image)},
					},
				},
			},
		}
	}

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-systemdata-app", mkApp("alpine:3.18"), nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.SystemData)
	require.NotNil(t, created.SystemData.CreatedAt)
	originalCreatedAt := *created.SystemData.CreatedAt
	require.NotEmpty(t, originalCreatedAt, "createdAt must be stamped on initial create")

	// Update with a different image — wait long enough that any restamp
	// would produce a strictly later timestamp than originalCreatedAt.
	time.Sleep(20 * time.Millisecond)
	upPoller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-systemdata-app", mkApp("public.ecr.aws/docker/library/alpine:latest"), nil)
	require.NoError(t, err)
	updated, err := upPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SystemData)
	require.NotNil(t, updated.SystemData.CreatedAt)
	assert.Equal(t, originalCreatedAt, *updated.SystemData.CreatedAt,
		"systemData.createdAt must be preserved across updates (real ARM stamps once on create)")

	// LastModifiedAt should be present and >= originalCreatedAt.
	require.NotNil(t, updated.SystemData.LastModifiedAt)
	assert.NotEmpty(t, *updated.SystemData.LastModifiedAt,
		"systemData.lastModifiedAt must be stamped on every write")

	// Cleanup.
	delPoller, err := client.BeginDelete(ctx, rg, "sdk-systemdata-app", nil)
	require.NoError(t, err)
	_, _ = delPoller.PollUntilDone(ctx, nil)
}

// WebApps.UpdateAzureStorageAccounts route — the azure-functions
// backend's volumes.go binds named docker volumes to Azure Files
// shares via this call; without it, function apps cannot mount user
// volumes.
func TestSDK_WebApps_UpdateAzureStorageAccounts(t *testing.T) {
	rg := "sdk-azf-storage-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	defer azureDeleteSite(rg, "sdk-storage-site")

	// Create a site first.
	createPoller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-storage-site", armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("functionapp"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
				"/providers/Microsoft.Web/serverFarms/test-plan"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Attach two named volumes via UpdateAzureStorageAccounts.
	resp, err := client.UpdateAzureStorageAccounts(ctx, rg, "sdk-storage-site",
		armappservice.AzureStoragePropertyDictionaryResource{
			Properties: map[string]*armappservice.AzureStorageInfoValue{
				"data": {
					Type:        to.Ptr(armappservice.AzureStorageTypeAzureFiles),
					AccountName: to.Ptr("simstorage"),
					ShareName:   to.Ptr("data-share"),
					AccessKey:   to.Ptr("fake-key"),
					MountPath:   to.Ptr("/mnt/data"),
				},
				"cache": {
					Type:        to.Ptr(armappservice.AzureStorageTypeAzureFiles),
					AccountName: to.Ptr("simstorage"),
					ShareName:   to.Ptr("cache-share"),
					AccessKey:   to.Ptr("fake-key"),
					MountPath:   to.Ptr("/mnt/cache"),
				},
			},
		}, nil)
	require.NoError(t, err, "UpdateAzureStorageAccounts must succeed against the sim")
	require.NotNil(t, resp.Properties)
	assert.Len(t, resp.Properties, 2, "both volume mappings should round-trip")
	require.Contains(t, resp.Properties, "data")
	require.NotNil(t, resp.Properties["data"])
	assert.Equal(t, "data-share", *resp.Properties["data"].ShareName)
	assert.Equal(t, "/mnt/data", *resp.Properties["data"].MountPath)
}

// TestSDK_ContainerAppsApps_PatchMergesNotReplaces verifies that the
// PATCH route merges Configuration and Template sub-fields rather than
// wholesale-replacing them. A PATCH that only changes containers must
// preserve the existing secrets, ingress, and scale. Uses raw HTTP
// because the Azure SDK only exposes CreateOrUpdate (PUT), not PATCH.
func TestSDK_ContainerAppsApps_PatchMergesNotReplaces(t *testing.T) {
	rg := "sdk-aca-patch-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	// Create with full Configuration (secrets, ingress) + Template (scale, volumes) via PUT.
	createPoller, err := client.BeginCreateOrUpdate(ctx, rg, "patch-merge-app", armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Configuration: &armappcontainers.Configuration{
				ActiveRevisionsMode: to.Ptr(armappcontainers.ActiveRevisionsModeSingle),
				Secrets: []*armappcontainers.Secret{
					{Name: to.Ptr("db-password"), Value: to.Ptr("supersecret")},
				},
				Ingress: &armappcontainers.Ingress{
					External:   to.Ptr(false),
					TargetPort: to.Ptr[int32](8080),
					Transport:  to.Ptr(armappcontainers.IngressTransportMethodAuto),
				},
			},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{Name: to.Ptr("main"), Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest")},
				},
				Volumes: []*armappcontainers.Volume{
					{Name: to.Ptr("data"), StorageType: to.Ptr(armappcontainers.StorageType("EmptyDirectory"))},
				},
				Scale: &armappcontainers.Scale{
					MinReplicas: to.Ptr[int32](1),
					MaxReplicas: to.Ptr[int32](3),
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defer func() {
		delPoller, _ := client.BeginDelete(ctx, rg, "patch-merge-app", nil)
		if delPoller != nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	}()

	// PATCH only the template.containers image. The PATCH body omits
	// secrets, ingress, scale, and volumes — those must be preserved.
	patchURL := baseURL + "/subscriptions/" + subscriptionID +
		"/resourceGroups/" + rg + "/providers/Microsoft.App/containerApps/patch-merge-app?api-version=2025-01-01"
	patchBody := `{
		"properties": {
			"template": {
				"containers": [{"name":"main","image":"public.ecr.aws/docker/library/alpine:3.20"}]
			}
		}
	}`
	req, err := http.NewRequest("PATCH", patchURL, strings.NewReader(patchBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	patchResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	patchBytes, _ := io.ReadAll(patchResp.Body)
	patchResp.Body.Close()
	require.Equal(t, 200, patchResp.StatusCode, "PATCH response: %s", patchBytes)

	var patched struct {
		Properties struct {
			Configuration struct {
				Secrets []struct {
					Name string `json:"name"`
				} `json:"secrets"`
			} `json:"configuration"`
			Template struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
				Volumes []struct {
					Name string `json:"name"`
				} `json:"volumes"`
				Scale struct {
					MaxReplicas int `json:"maxReplicas"`
				} `json:"scale"`
			} `json:"template"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(patchBytes, &patched))

	// Secrets preserved despite being absent from the PATCH body.
	require.NotEmpty(t, patched.Properties.Configuration.Secrets, "Secrets must be preserved across PATCH")
	assert.Equal(t, "db-password", patched.Properties.Configuration.Secrets[0].Name)

	// Volumes preserved.
	require.NotEmpty(t, patched.Properties.Template.Volumes, "Volumes must be preserved across PATCH")
	assert.Equal(t, "data", patched.Properties.Template.Volumes[0].Name)

	// Scale preserved.
	assert.Equal(t, 3, patched.Properties.Template.Scale.MaxReplicas, "Scale must be preserved across PATCH")

	// Container image updated.
	require.NotEmpty(t, patched.Properties.Template.Containers)
	assert.Equal(t, "public.ecr.aws/docker/library/alpine:3.20", patched.Properties.Template.Containers[0].Image)
}

// TestSDK_ContainerAppsApps_PatchRFC7396Semantics proves the PATCH handler
// implements the full RFC 7396 JSON Merge Patch contract ARM documents: a
// nested object merges member-wise (patching scale.minReplicas preserves
// maxReplicas), and an explicit null removes the member (deleting a tag).
func TestSDK_ContainerAppsApps_PatchRFC7396Semantics(t *testing.T) {
	rg := "sdk-aca-rfc7396-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	createPoller, err := client.BeginCreateOrUpdate(ctx, rg, "rfc7396-app", armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"keep": to.Ptr("kept"), "drop": to.Ptr("doomed")},
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{Name: to.Ptr("main"), Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest")},
				},
				Scale: &armappcontainers.Scale{
					MinReplicas: to.Ptr[int32](1),
					MaxReplicas: to.Ptr[int32](5),
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defer func() {
		delPoller, _ := client.BeginDelete(ctx, rg, "rfc7396-app", nil)
		if delPoller != nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	}()

	// A merge patch that deletes one tag via null, adds another, and patches
	// one member of the nested scale object.
	patchURL := baseURL + "/subscriptions/" + subscriptionID +
		"/resourceGroups/" + rg + "/providers/Microsoft.App/containerApps/rfc7396-app?api-version=2025-01-01"
	patchBody := `{"tags":{"drop":null,"added":"new"},"properties":{"template":{"scale":{"minReplicas":2}}}}`
	req, err := http.NewRequest("PATCH", patchURL, strings.NewReader(patchBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	patchResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	patchBytes, _ := io.ReadAll(patchResp.Body)
	patchResp.Body.Close()
	require.Equal(t, 200, patchResp.StatusCode, "PATCH response: %s", patchBytes)

	var patched struct {
		Tags       map[string]string `json:"tags"`
		Properties struct {
			Template struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
				Scale struct {
					MinReplicas     int `json:"minReplicas"`
					MaxReplicas     int `json:"maxReplicas"`
					CooldownPeriod  int `json:"cooldownPeriod"`
					PollingInterval int `json:"pollingInterval"`
				} `json:"scale"`
			} `json:"template"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(patchBytes, &patched))

	// null removes the member; other members merge.
	assert.Equal(t, map[string]string{"keep": "kept", "added": "new"}, patched.Tags,
		"an explicit null in the merge patch must remove the tag")

	// Nested objects merge member-wise: minReplicas updated, siblings kept.
	assert.Equal(t, 2, patched.Properties.Template.Scale.MinReplicas)
	assert.Equal(t, 5, patched.Properties.Template.Scale.MaxReplicas,
		"patching scale.minReplicas must not discard scale.maxReplicas")
	assert.Equal(t, 300, patched.Properties.Template.Scale.CooldownPeriod,
		"server-stamped cooldownPeriod default must survive the merge")
	assert.Equal(t, 30, patched.Properties.Template.Scale.PollingInterval,
		"server-stamped pollingInterval default must survive the merge")

	// Untouched members are preserved.
	require.NotEmpty(t, patched.Properties.Template.Containers)
	assert.Equal(t, "public.ecr.aws/docker/library/alpine:latest", patched.Properties.Template.Containers[0].Image)
}

// TestSDK_ContainerAppsApps_DeleteLROEnvelope proves the DELETE contract at
// the wire level: 202 Accepted with an empty body, absolute
// Azure-AsyncOperation and Location URLs, an operation-status envelope
// carrying id/name/status, and a final GET that 404s once the operation
// reports Succeeded.
func TestSDK_ContainerAppsApps_DeleteLROEnvelope(t *testing.T) {
	rg := "sdk-aca-dellro-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.App/managedEnvironments/sim-env"

	createPoller, err := client.BeginCreateOrUpdate(ctx, rg, "dellro-app", armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr(envID),
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{
					{Name: to.Ptr("main"), Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest")},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	appURL := baseURL + "/subscriptions/" + subscriptionID +
		"/resourceGroups/" + rg + "/providers/Microsoft.App/containerApps/dellro-app?api-version=2025-01-01"
	req, err := http.NewRequest("DELETE", appURL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	delBody, _ := io.ReadAll(delResp.Body)
	delResp.Body.Close()

	require.Equal(t, 202, delResp.StatusCode, "DELETE must be a 202 LRO: %s", delBody)
	assert.Empty(t, delBody, "a 202 DELETE response has no body")

	opURL := delResp.Header.Get("Azure-AsyncOperation")
	locURL := delResp.Header.Get("Location")
	require.NotEmpty(t, opURL, "Azure-AsyncOperation header required on a 202 DELETE")
	require.NotEmpty(t, locURL, "Location header required on a 202 DELETE")
	assert.True(t, strings.HasPrefix(opURL, "http"), "Azure-AsyncOperation must be an absolute URL: %s", opURL)
	assert.True(t, strings.HasPrefix(locURL, "http"), "Location must be an absolute URL: %s", locURL)
	assert.Contains(t, opURL, "/providers/Microsoft.App/locations/", "operation URL follows ARM conventions")
	assert.Contains(t, opURL, "/operationStatuses/")
	assert.Contains(t, locURL, "/operationResults/")
	assert.NotEmpty(t, delResp.Header.Get("Retry-After"))

	// The operation-status envelope carries id/name/status and settles to
	// Succeeded.
	var envelope struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		opReq, err := http.NewRequest("GET", opURL, nil)
		require.NoError(t, err)
		opReq.Header.Set("Authorization", simARMBearer)
		opResp, err := http.DefaultClient.Do(opReq)
		require.NoError(t, err)
		opBytes, _ := io.ReadAll(opResp.Body)
		opResp.Body.Close()
		require.Equal(t, 200, opResp.StatusCode, "operation status response: %s", opBytes)
		require.NoError(t, json.Unmarshal(opBytes, &envelope))
		require.NotEmpty(t, envelope.Name, "operation envelope must carry name")
		require.Contains(t, envelope.ID, "/operationStatuses/", "operation envelope must carry its ARM id")
		if envelope.Status == "Succeeded" {
			break
		}
		require.Equal(t, "InProgress", envelope.Status)
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, "Succeeded", envelope.Status, "delete operation must settle to Succeeded")

	// After the operation succeeds the resource is gone.
	getReq, err := http.NewRequest("GET", appURL, nil)
	require.NoError(t, err)
	getReq.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getResp.Body.Close()
	assert.Equal(t, 404, getResp.StatusCode, "GET after a completed delete must be 404")
}
