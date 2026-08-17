package azure_cli_test

import (
	"fmt"
	"slices"
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

	// Poll Log Analytics until the output is ingested, then assert on the log
	// lines themselves. A substring search for "14" over the whole response
	// document is not an arithmetic assertion: the payload carries ISO-8601
	// TimeGenerated stamps and identifiers, so any row landing at 14 minutes
	// or 14 seconds past the hour satisfies it and the workload could have
	// printed anything at all.
	lines := waitForContainerAppLogLine(t, "ContainerGroupName_s", jobName, "14", 60*time.Second)
	assert.Contains(t, lines, "14",
		"the workload evaluates (3 + 4) * 2 and prints the result; its console lines were %q", lines)

	// Cleanup
	runCLI(t, azRest("DELETE", jobURL, ""))
}

// waitForContainerAppLogLine polls Log Analytics until the workload's own
// output lands, and returns whatever lines it saw when it stopped waiting.
// Waiting for the first line of any kind is a race the platform always wins:
// a replica logs that it started before the workload inside it prints
// anything, so the wait ends on a lifecycle line and the assertion that
// follows reads an incomplete console.
func waitForContainerAppLogLine(t *testing.T, column, name, want string, budget time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		lines := containerAppLogLines(t, column, name)
		if slices.Contains(lines, want) || !time.Now().Before(deadline) {
			return lines
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// containerAppLogLines reads the console lines Log Analytics holds for one
// workload, selected by the column the workload kind is recorded under —
// ContainerGroupName_s for a job, ContainerAppName_s for an app — and decoded
// out of the query response's PrimaryResult table rather than matched as a
// substring of the whole document. The column index is resolved from the
// response's own column list, so a schema change surfaces as a failure here
// rather than as a silently-empty result.
func containerAppLogLines(t *testing.T, filterColumn, name string) []string {
	t.Helper()
	queryURL := baseURL + "/v1/workspaces/default/query"
	kqlBody := `{"query": "ContainerAppConsoleLogs_CL | where ` + filterColumn + ` == \"` + name + `\""}`
	out := runCLI(t, azRest("POST", queryURL, kqlBody))

	var response struct {
		Tables []struct {
			Name    string `json:"name"`
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
			Rows [][]any `json:"rows"`
		} `json:"tables"`
	}
	parseJSON(t, out, &response)
	require.NotEmpty(t, response.Tables, "the query response carries no table: %s", out)

	table := response.Tables[0]
	require.Equal(t, "PrimaryResult", table.Name, "the first table of a query response is the primary result: %s", out)
	logColumn := -1
	for i, column := range table.Columns {
		if column.Name == "Log_s" {
			logColumn = i
		}
	}
	require.NotEqual(t, -1, logColumn,
		"ContainerAppConsoleLogs_CL must project the Log_s column; got columns %+v", table.Columns)

	lines := make([]string, 0, len(table.Rows))
	for _, row := range table.Rows {
		require.Greater(t, len(row), logColumn, "a row is shorter than the column list declares: %v", row)
		line, ok := row[logColumn].(string)
		require.True(t, ok, "Log_s is declared a string but row %v holds %T", row, row[logColumn])
		lines = append(lines, line)
	}
	return lines
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
