package azure_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const acaAPIVersion = "2023-05-01"

func acaURL(path string) string {
	return armURL("Microsoft.App", path, acaAPIVersion)
}

func TestContainerApps_CLI_StartAndCheckLogs(t *testing.T) {
	// Create a Container Apps Job with echo command
	jobURL := acaURL("jobs/cli-aca-job")
	jobBody := `{
		"location": "eastus",
		"properties": {
			"environmentId": "",
			"configuration": {
				"replicaTimeout": 30,
				"triggerType": "Manual",
				"manualTriggerConfig": { "parallelism": 1, "replicaCompletionCount": 1 }
			},
			"template": {
				"containers": [{
					"name": "app",
					"image": "public.ecr.aws/docker/library/alpine:latest",
					"command": ["echo", "hello-from-aca"]
				}]
			}
		}
	}`
	runCLI(t, azRest("PUT", jobURL, jobBody))

	// Start execution
	rawStartURL := armURL("Microsoft.App", "jobs/cli-aca-job/start", acaAPIVersion)
	out := runCLI(t, azRest("POST", rawStartURL, ""))

	var startResult struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	parseJSON(t, out, &startResult)
	require.NotEmpty(t, startResult.Name)

	// Poll until the execution completes (async in the sim) — a fixed sleep
	// races a loaded runner.
	execURL := armURL("Microsoft.App", "jobs/cli-aca-job/executions/"+startResult.Name, acaAPIVersion)
	var execResult struct {
		Properties struct {
			Status string `json:"status"`
		} `json:"properties"`
	}
	require.Eventually(t, func() bool {
		out = runCLI(t, azRest("GET", execURL, ""))
		parseJSON(t, out, &execResult)
		s := execResult.Properties.Status
		return s == "Succeeded" || s == "Failed"
	}, 60*time.Second, 300*time.Millisecond)
	assert.Equal(t, "Succeeded", execResult.Properties.Status)

	// Poll Log Analytics until the real process output is ingested.
	queryURL := baseURL + "/v1/workspaces/default/query"
	kqlBody := `{"query": "ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"cli-aca-job\""}`
	require.Eventually(t, func() bool {
		out = runCLI(t, azRest("POST", queryURL, kqlBody))
		return strings.Contains(out, "hello-from-aca")
	}, 60*time.Second, 300*time.Millisecond)
	assert.Contains(t, out, "hello-from-aca", "expected real process output in Log Analytics")

	// Cleanup
	runCLI(t, azRest("DELETE", jobURL, ""))
}

func TestContainerApps_CLI_StartFailure(t *testing.T) {
	jobURL := acaURL("jobs/cli-aca-fail-job")
	jobBody := `{
		"location": "eastus",
		"properties": {
			"environmentId": "",
			"configuration": {
				"replicaTimeout": 30,
				"triggerType": "Manual",
				"manualTriggerConfig": { "parallelism": 1, "replicaCompletionCount": 1 }
			},
			"template": {
				"containers": [{
					"name": "app",
					"image": "public.ecr.aws/docker/library/alpine:latest",
					"command": ["sh", "-c", "exit 1"]
				}]
			}
		}
	}`
	runCLI(t, azRest("PUT", jobURL, jobBody))

	// Start execution
	rawStartURL := armURL("Microsoft.App", "jobs/cli-aca-fail-job/start", acaAPIVersion)
	out := runCLI(t, azRest("POST", rawStartURL, ""))

	var startResult struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &startResult)
	require.NotEmpty(t, startResult.Name)

	// Poll until the execution reaches a terminal status (async in the sim).
	execURL := armURL("Microsoft.App", "jobs/cli-aca-fail-job/executions/"+startResult.Name, acaAPIVersion)
	var execResult struct {
		Properties struct {
			Status string `json:"status"`
		} `json:"properties"`
	}
	require.Eventually(t, func() bool {
		out = runCLI(t, azRest("GET", execURL, ""))
		parseJSON(t, out, &execResult)
		s := execResult.Properties.Status
		return s == "Succeeded" || s == "Failed"
	}, 60*time.Second, 300*time.Millisecond)
	assert.True(t, strings.Contains(execResult.Properties.Status, "Failed"),
		"expected status to be Failed, got: %s", execResult.Properties.Status)

	// Cleanup
	runCLI(t, azRest("DELETE", jobURL, ""))
}
