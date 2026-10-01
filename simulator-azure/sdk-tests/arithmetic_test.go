package azure_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerApps_JobArithmetic(t *testing.T) {
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
		result = queryWorkspace(t, "default", kql)
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
	rg, jobName := "arith-aca-rg", "arith-aca-fail-job"
	acaCreateJobWithImageAndCommand(t, rg, jobName, evalImageName, []string{"3 +"})
	execName := acaStartExecution(t, rg, jobName)

	// Poll for terminal status — container start latency on slow CI
	// can exceed any fixed sleep.
	var lastStatus string
	require.Eventually(t, func() bool {
		exec := acaGetExecution(t, rg, jobName, execName)
		execProps := exec["properties"].(map[string]any)
		lastStatus, _ = execProps["status"].(string)
		return lastStatus == "Failed" || lastStatus == "Succeeded"
	}, 30*time.Second, 250*time.Millisecond, "execution should reach terminal status; last=%s", lastStatus)
	assert.Equal(t, "Failed", lastStatus)
}

func TestContainerApps_JobArithmeticLogs(t *testing.T) {
	rg, jobName := "arith-aca-rg", "arith-aca-log-job"
	acaCreateJobWithImageAndCommand(t, rg, jobName, evalImageName, []string{"10 / 3"})
	_ = acaStartExecution(t, rg, jobName)

	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "arith-aca-log-job"`

	var allLogs string
	require.Eventually(t, func() bool {
		result := queryWorkspace(t, "default", kql)
		if len(result.Tables) != 1 {
			return false
		}
		table := result.Tables[0]
		logIdx := -1
		for i, col := range table.Columns {
			if col.Name == "Log_s" {
				logIdx = i
				break
			}
		}
		if logIdx < 0 {
			return false
		}
		var logs []string
		for _, row := range table.Rows {
			logs = append(logs, row[logIdx].(string))
		}
		allLogs = strings.Join(logs, "\n")
		return strings.Contains(allLogs, "3.333") && strings.Contains(allLogs, "Parsing expression:")
	}, 30*time.Second, 250*time.Millisecond, "expected '3.333' + 'Parsing expression:' in Log Analytics; saw=%q", allLogs)
}
