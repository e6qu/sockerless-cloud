package gcp_cli_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudRun_CLI_ArithmeticEval(t *testing.T) {
	jobID := "cli-arith-crj"
	createBody := fmt.Sprintf(`{
		"template": {
			"taskCount": 1,
			"template": {
				"containers": [{
					"name": "app",
					"image": %q,
					"args": ["(3 + 4) * 2"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`, evalImageName)
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId="+jobID, createBody)

	// Run the job
	out := httpDoJSON(t, "POST", jobURL(jobID+":run"), "")

	var lro struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.Name)

	// Poll until the execution reaches a terminal state (succeeded+failed > 0)
	// rather than racing on a fixed sleep that a loaded CI runner could exceed.
	exec := waitForExecution(t, lro.Response.Name)
	assert.Equal(t, 1, exec.SucceededCount, "expected job to succeed")
	assert.Equal(t, 0, exec.FailedCount)

	// Query Cloud Logging for job output
	out = runCLI(t, gcloudCLI("logging", "read",
		`resource.type="cloud_run_job" AND resource.labels.job_name="`+jobID+`"`,
		"--format", "json",
	))
	assert.Contains(t, out, "14", "expected '14' in Cloud Logging")

	// Cleanup
	httpDoJSON(t, "DELETE", jobURL(jobID), "")
}

type cloudRunExecutionCounts struct {
	SucceededCount int `json:"succeededCount"`
	FailedCount    int `json:"failedCount"`
}

// waitForExecution polls the Cloud Run execution resource until it reaches a
// terminal state (at least one task succeeded or failed). This replaces a fixed
// sleep, which races on a loaded CI runner where the job may not finish in time.
func waitForExecution(t *testing.T, execName string) cloudRunExecutionCounts {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var exec cloudRunExecutionCounts
	for time.Now().Before(deadline) {
		out := httpDoJSON(t, "GET", baseURL+"/v2/"+execName, "")
		exec = cloudRunExecutionCounts{}
		parseJSON(t, out, &exec)
		if exec.SucceededCount+exec.FailedCount > 0 {
			return exec
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("execution %s did not reach a terminal state within deadline", execName)
	return exec
}

func TestCloudRun_CLI_ArithmeticInvalid(t *testing.T) {
	jobID := "cli-arith-crj-fail"
	createBody := fmt.Sprintf(`{
		"template": {
			"taskCount": 1,
			"template": {
				"containers": [{
					"name": "app",
					"image": %q,
					"args": ["3 +"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`, evalImageName)
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId="+jobID, createBody)

	// Run the job
	out := httpDoJSON(t, "POST", jobURL(jobID+":run"), "")

	var lro struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.Name)

	// Poll until the execution reaches a terminal state (succeeded+failed > 0)
	// rather than racing on a fixed sleep that a loaded CI runner could exceed.
	exec := waitForExecution(t, lro.Response.Name)
	assert.Equal(t, 0, exec.SucceededCount)
	assert.Equal(t, 1, exec.FailedCount, "expected job to fail")

	// Cleanup
	httpDoJSON(t, "DELETE", jobURL(jobID), "")
}
