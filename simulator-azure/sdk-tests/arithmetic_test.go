package azure_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerApps_JobArithmetic(t *testing.T) {
	t.Parallel()
	rg, jobName := "arith-aca-rg", "arith-aca-job"
	acaCreateJobWithImageAndCommand(t, rg, jobName, evalImageName, []string{"(10 + 5) * 2"})
	execName := acaStartExecution(t, rg, jobName)

	exec := acaWaitExecution(t, rg, jobName, execName)
	execProps := exec["properties"].(map[string]any)
	assert.Equal(t, "Succeeded", execProps["status"])

	// Poll until the arithmetic result is ingested into Log Analytics (async
	// in the sim) — a fixed sleep races a loaded runner.
	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "arith-aca-job"`
	var result queryResponse
	require.Eventually(t, func() bool {
		result = queryWorkspace(t, acaLogsCustomerID(t), kql)
		if len(result.Tables) != 1 {
			return false
		}
		for _, row := range result.Tables[0].Rows {
			for _, cell := range row {
				if s, ok := cell.(string); ok && s == "30" {
					return true
				}
			}
		}
		return false
	}, 60*time.Second, 200*time.Millisecond)

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
		logs = append(logs, row[logIdx].(string))
	}
	assert.Contains(t, logs, "30", "expected output '30' in Log Analytics")
}

func TestContainerApps_JobArithmeticInvalid(t *testing.T) {
	t.Parallel()
	rg, jobName := "arith-aca-rg-8e21", "arith-aca-fail-job"
	acaCreateJobWithImageAndCommand(t, rg, jobName, evalImageName, []string{"3 +"})
	execName := acaStartExecution(t, rg, jobName)

	// Poll for terminal status — container start latency on slow CI
	// can exceed any fixed sleep.
	var lastStatus string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		exec := acaGetExecution(t, rg, jobName, execName)
		execProps := exec["properties"].(map[string]any)
		lastStatus, _ = execProps["status"].(string)
		assert.Contains(c, []string{"Failed", "Succeeded"}, lastStatus, "execution %s should reach a terminal status", execName)
	}, 30*time.Second, 250*time.Millisecond)
	assert.Equal(t, "Failed", lastStatus)
}

func TestContainerApps_JobArithmeticLogs(t *testing.T) {
	t.Parallel()
	rg, jobName := "arith-aca-rg-0e0a", "arith-aca-log-job"
	acaCreateJobWithImageAndCommand(t, rg, jobName, evalImageName, []string{"10 / 3"})
	execName := acaStartExecution(t, rg, jobName)

	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "arith-aca-log-job"`

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		exec := acaGetExecution(t, rg, jobName, execName)
		status, _ := exec["properties"].(map[string]any)["status"].(string)
		result := queryWorkspace(t, acaLogsCustomerID(t), kql)
		if !assert.Len(c, result.Tables, 1, "execution %s is %s", execName, status) {
			return
		}
		table := result.Tables[0]
		logIdx := -1
		for i, col := range table.Columns {
			if col.Name == "Log_s" {
				logIdx = i
				break
			}
		}
		if !assert.GreaterOrEqual(c, logIdx, 0, "Log_s column; execution %s is %s", execName, status) {
			return
		}
		var logs []string
		for _, row := range table.Rows {
			logs = append(logs, row[logIdx].(string))
		}
		allLogs := strings.Join(logs, "\n")
		assert.Contains(c, allLogs, "3.333", "execution %s is %s", execName, status)
		assert.Contains(c, allLogs, "Parsing expression:", "execution %s is %s", execName, status)
	}, 30*time.Second, 250*time.Millisecond)
}
