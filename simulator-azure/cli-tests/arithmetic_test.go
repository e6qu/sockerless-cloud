package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerApps_CLI_ArithmeticEval(t *testing.T) {
	jobName := "cli-arith-aca-job"
	jobURL := acaURL("jobs/" + jobName)
	jobBody := fmt.Sprintf(`{
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
					"image": %q,
					"args": ["(3 + 4) * 2"]
				}]
			}
		}
	}`, evalImageName)
	runCLI(t, azRest("PUT", jobURL, jobBody))

	// Start execution
	rawStartURL := armURL("Microsoft.App", "jobs/"+jobName+"/start", acaAPIVersion)
	out := runCLI(t, azRest("POST", rawStartURL, ""))

	var startResult struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &startResult)
	require.NotEmpty(t, startResult.Name)

	// Poll until the execution completes (async in the sim) — a fixed sleep
	// races a loaded runner.
	execURL := armURL("Microsoft.App", "jobs/"+jobName+"/executions/"+startResult.Name, acaAPIVersion)
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

	// Poll Log Analytics until the output is ingested.
	queryURL := baseURL + "/v1/workspaces/default/query"
	kqlBody := `{"query": "ContainerAppConsoleLogs_CL | where ContainerGroupName_s == \"` + jobName + `\""}`
	require.Eventually(t, func() bool {
		out = runCLI(t, azRest("POST", queryURL, kqlBody))
		return strings.Contains(out, "14")
	}, 60*time.Second, 300*time.Millisecond)
	assert.Contains(t, out, "14", "expected '14' in Log Analytics")

	// Cleanup
	runCLI(t, azRest("DELETE", jobURL, ""))
}

func TestContainerApps_CLI_ArithmeticInvalid(t *testing.T) {
	jobName := "cli-arith-aca-fail"
	jobURL := acaURL("jobs/" + jobName)
	jobBody := fmt.Sprintf(`{
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
					"image": %q,
					"args": ["3 +"]
				}]
			}
		}
	}`, evalImageName)
	runCLI(t, azRest("PUT", jobURL, jobBody))

	// Start execution
	rawStartURL := armURL("Microsoft.App", "jobs/"+jobName+"/start", acaAPIVersion)
	out := runCLI(t, azRest("POST", rawStartURL, ""))

	var startResult struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &startResult)
	require.NotEmpty(t, startResult.Name)

	// Poll until the execution reaches a terminal status (async in the sim).
	execURL := armURL("Microsoft.App", "jobs/"+jobName+"/executions/"+startResult.Name, acaAPIVersion)
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
