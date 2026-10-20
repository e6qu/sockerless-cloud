package azure_cli_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v2 ContainerApps "Apps" routes (Microsoft.App/containerApps).
// Smoke-test the CRUD via az rest so a regression in the route
// registration is caught at the wire level.
func TestContainerAppsApps_CLI_CreateGetDelete(t *testing.T) {
	t.Parallel()
	appURL := acaURL("containerApps/cli-test-app")
	body := `{
		"location": "eastus",
		"tags": { "sockerless-managed": "true" },
		"properties": {
			"environmentId": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/sim-env",
			"configuration": {
				"activeRevisionsMode": "Single",
				"ingress": { "external": false, "targetPort": 8080, "transport": "auto" }
			},
			"template": {
				"containers": [{ "name": "main", "image": "mirror.gcr.io/library/alpine:latest", "command": ["nc", "-lk", "-p", "8080", "-e", "true"] }],
				"scale": { "minReplicas": 1, "maxReplicas": 1 }
			}
		}
	}`
	azRestLongRunning(t, "PUT", appURL, body)
	out := runCLI(t, azRest("GET", appURL, ""))
	var created struct {
		Name       string `json:"name"`
		Properties struct {
			ProvisioningState       string `json:"provisioningState"`
			LatestReadyRevisionName string `json:"latestReadyRevisionName"`
			LatestRevisionFqdn      string `json:"latestRevisionFqdn"`
		} `json:"properties"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, "cli-test-app", created.Name)
	assert.Equal(t, "Succeeded", created.Properties.ProvisioningState,
		"backend reads provisioningState=Succeeded for state=running")
	assert.NotEmpty(t, created.Properties.LatestReadyRevisionName)
	assert.NotEmpty(t, created.Properties.LatestRevisionFqdn,
		"LatestRevisionFqdn drives Private DNS CNAME for peer discovery")

	out = runCLI(t, azRest("GET", appURL, ""))
	var got struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, "cli-test-app", got.Name)

	azRestLongRunning(t, "DELETE", appURL, "")

	// `az rest` exits non-zero on 404 and runCLI requires success, so read
	// the deleted app over raw HTTP.
	req, err := http.NewRequest("GET", baseURL+"/subscriptions/"+subscriptionID+
		"/resourceGroups/"+resourceGroup+
		"/providers/Microsoft.App/containerApps/cli-test-app?api-version="+acaAPIVersion, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+armBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "GET after the delete operation completes must be 404")
}

// TestContainerAppsApps_CLI_PatchMergeSemantics drives the RFC 7396 PATCH
// contract over az rest: a null tag member deletes the tag, and a nested
// scale patch merges member-wise instead of replacing the object.
func TestContainerAppsApps_CLI_PatchMergeSemantics(t *testing.T) {
	t.Parallel()
	appURL := acaURL("containerApps/cli-patch-app")
	body := `{
		"location": "eastus",
		"tags": { "keep": "kept", "drop": "doomed" },
		"properties": {
			"environmentId": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/sim-env",
			"template": {
				"containers": [{ "name": "main", "image": "mirror.gcr.io/library/alpine:latest" }],
				"scale": { "minReplicas": 1, "maxReplicas": 5 }
			}
		}
	}`
	azRestLongRunning(t, "PUT", appURL, body)
	t.Cleanup(func() { _, _ = azRest("DELETE", appURL, "").CombinedOutput() })

	// The scale change makes a revision, so the patch is accepted and
	// provisions it.
	azRestLongRunning(t, "PATCH", appURL,
		`{"tags":{"drop":null,"added":"new"},"properties":{"template":{"scale":{"minReplicas":2}}}}`)
	out := runCLI(t, azRest("GET", appURL, ""))
	var patched struct {
		Tags       map[string]string `json:"tags"`
		Properties struct {
			Template struct {
				Scale struct {
					MinReplicas int `json:"minReplicas"`
					MaxReplicas int `json:"maxReplicas"`
				} `json:"scale"`
			} `json:"template"`
		} `json:"properties"`
	}
	parseJSON(t, out, &patched)
	assert.Equal(t, map[string]string{"keep": "kept", "added": "new"}, patched.Tags,
		"a null tag member in the merge patch must remove the tag")
	assert.Equal(t, 2, patched.Properties.Template.Scale.MinReplicas)
	assert.Equal(t, 5, patched.Properties.Template.Scale.MaxReplicas,
		"patching scale.minReplicas must not discard scale.maxReplicas")
}

func TestContainerAppsApps_CLI_StartsRealReplicaAndLogs(t *testing.T) {
	t.Parallel()
	appName := "cli-exec-app"
	appURL := acaURL("containerApps/" + appName)
	envID, customerID := cliLogsEnvironment(t)
	body := fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"environmentId": %q,
			"template": {
				"containers": [{
					"name": "main",
					"image": %q,
					"args": ["9 * 6"]
				}],
				"scale": { "minReplicas": 1, "maxReplicas": 1 }
			}
		}
	}`, envID, evalImageName)
	azRestLongRunning(t, "PUT", appURL, body)
	defer runCLI(t, azRest("DELETE", appURL, ""))

	// Generous deadline so a slow real-replica start on a loaded CI runner
	// doesn't expire before the arithmetic output is logged.
	//
	// The assertion is on the decoded console lines rather than on a substring
	// of the query response: "54" appears in the ISO-8601 TimeGenerated stamp
	// of any row landing at 54 seconds past the minute, so a substring search
	// passes for a replica that printed something else entirely — or that
	// printed nothing and left only a stamp behind.
	lines := waitForContainerAppLogLine(t, "ContainerAppName_s", appName, "54", 60*time.Second)
	assert.Contains(t, lines, "54",
		"the replica evaluates 9 * 6 and prints the result; its console lines were %q", lines)

	// The platform's own events about the replica are system logs, apart
	// from what the container printed.
	system := cliQueryWorkspace(t, customerID,
		`ContainerAppSystemLogs_CL | where ContainerAppName_s == "`+appName+`" and Reason_s == "ContainerStarted" | project Log_s`)
	assert.Contains(t, system, "Started container 'main'")
	assert.NotContains(t, lines, "Container app replica started", "the console holds only what the containers printed")
}

// TestContainerAppsApps_CLI_ConfigurationRoundTrip drives every
// configuration member — dapr, identitySettings, maxInactiveRevisions,
// runtime, and service — through az rest PUT/GET/PATCH, pinning the raw
// ARM wire spelling of each member.
func TestContainerAppsApps_CLI_ConfigurationRoundTrip(t *testing.T) {
	t.Parallel()
	appURL := acaURL("containerApps/cli-cfg-app")
	body := `{
		"location": "eastus",
		"properties": {
			"environmentId": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/sim-env",
			"configuration": {
				"activeRevisionsMode": "Multiple",
				"maxInactiveRevisions": 12,
				"dapr": {
					"enabled": false,
					"appId": "cli-dapr-app",
					"appPort": 9000,
					"appProtocol": "grpc",
					"enableApiLogging": true,
					"httpMaxRequestSize": 16,
					"httpReadBufferSize": 8,
					"logLevel": "debug"
				},
				"runtime": {
					"java": { "enableMetrics": true }
				},
				"service": { "type": "redis" },
				"identitySettings": [
					{ "identity": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/cli-identity", "lifecycle": "Init" }
				]
			}
		}
	}`
	azRestLongRunning(t, "PUT", appURL, body)
	t.Cleanup(func() { _, _ = azRest("DELETE", appURL, "").CombinedOutput() })

	type configShape struct {
		ActiveRevisionsMode  string `json:"activeRevisionsMode"`
		MaxInactiveRevisions int    `json:"maxInactiveRevisions"`
		Dapr                 struct {
			Enabled            bool   `json:"enabled"`
			AppID              string `json:"appId"`
			AppPort            int    `json:"appPort"`
			AppProtocol        string `json:"appProtocol"`
			EnableAPILogging   bool   `json:"enableApiLogging"`
			HTTPMaxRequestSize int    `json:"httpMaxRequestSize"`
			HTTPReadBufferSize int    `json:"httpReadBufferSize"`
			LogLevel           string `json:"logLevel"`
		} `json:"dapr"`
		Runtime struct {
			Java struct {
				EnableMetrics bool `json:"enableMetrics"`
			} `json:"java"`
		} `json:"runtime"`
		Service struct {
			Type string `json:"type"`
		} `json:"service"`
		IdentitySettings []struct {
			Identity  string `json:"identity"`
			Lifecycle string `json:"lifecycle"`
		} `json:"identitySettings"`
	}
	var got struct {
		Properties struct {
			Configuration configShape `json:"configuration"`
		} `json:"properties"`
	}
	out := runCLI(t, azRest("GET", appURL, ""))
	parseJSON(t, out, &got)
	cfg := got.Properties.Configuration
	assert.Equal(t, "Multiple", cfg.ActiveRevisionsMode)
	assert.Equal(t, 12, cfg.MaxInactiveRevisions)
	assert.False(t, cfg.Dapr.Enabled)
	assert.Equal(t, "cli-dapr-app", cfg.Dapr.AppID)
	assert.Equal(t, 9000, cfg.Dapr.AppPort)
	assert.Equal(t, "grpc", cfg.Dapr.AppProtocol)
	assert.True(t, cfg.Dapr.EnableAPILogging)
	assert.Equal(t, 16, cfg.Dapr.HTTPMaxRequestSize)
	assert.Equal(t, 8, cfg.Dapr.HTTPReadBufferSize)
	assert.Equal(t, "debug", cfg.Dapr.LogLevel)
	assert.True(t, cfg.Runtime.Java.EnableMetrics)
	assert.Equal(t, "redis", cfg.Service.Type)
	require.Len(t, cfg.IdentitySettings, 1)
	assert.Contains(t, cfg.IdentitySettings[0].Identity, "/userAssignedIdentities/cli-identity")
	assert.Equal(t, "Init", cfg.IdentitySettings[0].Lifecycle)

	// An RFC 7396 merge patch of one dapr member must preserve every
	// sibling dapr member and every sibling configuration member.
	out = runCLI(t, azRest("PATCH", appURL,
		`{"properties":{"configuration":{"dapr":{"logLevel":"warn"}}}}`))
	var patched struct {
		Properties struct {
			Configuration configShape `json:"configuration"`
		} `json:"properties"`
	}
	parseJSON(t, out, &patched)
	pcfg := patched.Properties.Configuration
	assert.Equal(t, "warn", pcfg.Dapr.LogLevel, "the patched dapr member must take the new value")
	assert.Equal(t, "cli-dapr-app", pcfg.Dapr.AppID, "sibling dapr members must survive the merge")
	assert.Equal(t, 9000, pcfg.Dapr.AppPort)
	assert.Equal(t, "grpc", pcfg.Dapr.AppProtocol)
	assert.True(t, pcfg.Dapr.EnableAPILogging)
	assert.Equal(t, 12, pcfg.MaxInactiveRevisions, "sibling configuration members must survive the merge")
	assert.True(t, pcfg.Runtime.Java.EnableMetrics)
	assert.Equal(t, "redis", pcfg.Service.Type)
	require.Len(t, pcfg.IdentitySettings, 1)
}

// WebApps.UpdateAzureStorageAccounts is what the azure-functions
// backend uses to bind named volumes to Azure Files.
func TestWebApps_CLI_UpdateAzureStorageAccounts(t *testing.T) {
	t.Parallel()
	const azfAPIVersion = "2024-04-01"
	siteURL := armURL("Microsoft.Web", "sites/cli-storage-site", azfAPIVersion)
	siteBody := `{"location":"eastus","kind":"functionapp","properties":{"serverFarmId":"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/serverFarms/test"}}`
	runCLI(t, azRest("PUT", siteURL, siteBody))

	storageURL := armURL("Microsoft.Web", "sites/cli-storage-site/config/azurestorageaccounts", azfAPIVersion)
	storageBody := `{
		"properties": {
			"data": {
				"type": "AzureFiles",
				"accountName": "simstorage",
				"shareName": "data-share",
				"accessKey": "fake-key",
				"mountPath": "/mnt/data"
			}
		}
	}`
	out := runCLI(t, azRest("PUT", storageURL, storageBody))
	var resp struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			AccountName string `json:"accountName"`
			ShareName   string `json:"shareName"`
			MountPath   string `json:"mountPath"`
		} `json:"properties"`
	}
	parseJSON(t, out, &resp)
	require.Contains(t, resp.Properties, "data")
	assert.Equal(t, "AzureFiles", resp.Properties["data"].Type)
	assert.Equal(t, "data-share", resp.Properties["data"].ShareName)
	assert.Equal(t, "/mnt/data", resp.Properties["data"].MountPath)
}

// A create whose revision fails to provision answers InProgress, fails its
// operation, and leaves the app Failed with the revision it failed on.
func TestContainerAppsApps_CLI_FailedProvisioningKeepsTheApp(t *testing.T) {
	t.Parallel()
	appURL := acaURL("containerApps/cli-failed-app")
	body := `{
		"location": "eastus",
		"properties": {
			"environmentId": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/sim-env",
			"configuration": { "ingress": { "external": false, "targetPort": 8080 } },
			"template": {
				"containers": [{ "name": "main", "image": "mirror.gcr.io/library/alpine:latest", "command": ["sh", "-c", "exit 3"] }],
				"scale": { "minReplicas": 1, "maxReplicas": 1 }
			}
		}
	}`
	stdout, stderr := runCLIStreams(t, azRest("PUT", appURL, body, "--debug"))
	t.Cleanup(func() { _, _ = azRest("DELETE", appURL, "").CombinedOutput() })
	var accepted struct {
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, stdout, &accepted)
	assert.Equal(t, "InProgress", accepted.Properties.ProvisioningState)

	opURL := azDebugResponseHeader(stderr, "Azure-AsyncOperation")
	require.NotEmpty(t, opURL, "the create names its operation")
	var op struct {
		Status string `json:"status"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	for {
		out, debug := runCLIStreams(t, azRest("GET", opURL, "", "--debug"))
		parseJSON(t, out, &op)
		if op.Status != "InProgress" {
			break
		}
		azAwaitRetryAfter(t, debug)
	}
	assert.Equal(t, "Failed", op.Status)
	assert.Equal(t, "ContainerAppOperationError", op.Error.Code)

	var app struct {
		Properties struct {
			ProvisioningState  string `json:"provisioningState"`
			LatestRevisionName string `json:"latestRevisionName"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", appURL, "")), &app)
	assert.Equal(t, "Failed", app.Properties.ProvisioningState)
	var revision struct {
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", acaURL("containerApps/cli-failed-app/revisions/"+app.Properties.LatestRevisionName), "")), &revision)
	assert.Equal(t, "Failed", revision.Properties.ProvisioningState)
}
