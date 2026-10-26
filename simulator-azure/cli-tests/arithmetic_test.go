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
	t.Parallel()
	jobName := "cli-arith-aca-job"
	jobURL := acaURL("jobs/" + jobName)
	envID, _ := cliLogsEnvironment(t)
	jobBody := fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"environmentId": %q,
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
	}`, envID, evalImageName)
	runCLI(t, azRest("PUT", jobURL, jobBody))

	rawStartURL := armURL("Microsoft.App", "jobs/"+jobName+"/start", acaAPIVersion)
	out := runCLI(t, azRest("POST", rawStartURL, ""))

	var startResult struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &startResult)
	require.NotEmpty(t, startResult.Name)

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

	// Assert on the log lines themselves: the response document carries
	// timestamps and identifiers, so a substring search for "14" over it proves
	// nothing.
	lines := waitForContainerAppLogLine(t, "ContainerGroupName_s", jobName, "14", 60*time.Second)
	assert.Contains(t, lines, "14",
		"the workload evaluates (3 + 4) * 2 and prints the result; its console lines were %q", lines)

	runCLI(t, azRest("DELETE", jobURL, ""))
}

// waitForContainerAppLogLine polls Log Analytics until the workload's own
// output lands. A replica logs that it started before the workload prints, so
// waiting for any line ends on a lifecycle line.
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

// containerAppLogLines decodes the console lines of one workload from the query
// response's PrimaryResult table, filtered by ContainerGroupName_s for a job or
// ContainerAppName_s for an app.
func containerAppLogLines(t *testing.T, filterColumn, name string) []string {
	t.Helper()
	_, customerID := cliLogsEnvironment(t)
	out := cliQueryWorkspace(t, customerID, `ContainerAppConsoleLogs_CL | where `+filterColumn+` == "`+name+`"`)

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
	t.Parallel()
	jobName := "cli-arith-aca-fail"
	jobURL := acaURL("jobs/" + jobName)
	envID, _ := cliLogsEnvironment(t)
	jobBody := fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"environmentId": %q,
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
	}`, envID, evalImageName)
	runCLI(t, azRest("PUT", jobURL, jobBody))

	rawStartURL := armURL("Microsoft.App", "jobs/"+jobName+"/start", acaAPIVersion)
	out := runCLI(t, azRest("POST", rawStartURL, ""))

	var startResult struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &startResult)
	require.NotEmpty(t, startResult.Name)

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

	runCLI(t, azRest("DELETE", jobURL, ""))
}
