package gcp_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jobsBaseURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs", baseURL, project, location)
}

func jobURL(name string) string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs/%s", baseURL, project, location, name)
}

func TestCloudRun_CLI_RunJobAndCheckLogs(t *testing.T) {
	// Create a Cloud Run Job with echo command
	createBody := `{
		"template": {
			"taskCount": 1,
			"template": {
				"containers": [{
					"name": "app",
					"image": "alpine:latest",
					"command": ["echo", "hello-from-crj"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId=cli-run-job", createBody)

	// Run the job
	out := httpDoJSON(t, "POST", jobURL("cli-run-job:run"), "")

	// Parse LRO to get execution name
	var lro struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.Name)

	// Poll until the execution completes (run + completion are async in the
	// sim) — a fixed sleep races a loaded runner.
	var exec struct {
		SucceededCount int `json:"succeededCount"`
		FailedCount    int `json:"failedCount"`
		RunningCount   int `json:"runningCount"`
	}
	require.Eventually(t, func() bool {
		out = httpDoJSON(t, "GET", baseURL+"/v2/"+lro.Response.Name, "")
		parseJSON(t, out, &exec)
		return exec.RunningCount == 0 && exec.SucceededCount+exec.FailedCount > 0
	}, 60*time.Second, 250*time.Millisecond)
	assert.Equal(t, 1, exec.SucceededCount, "expected job to succeed")
	assert.Equal(t, 0, exec.FailedCount)
	assert.Equal(t, 0, exec.RunningCount)

	// Poll Cloud Logging until the job's real output is ingested.
	require.Eventually(t, func() bool {
		out = runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_job" AND resource.labels.job_name="cli-run-job"`,
			"--format", "json",
		))
		return strings.Contains(out, "hello-from-crj")
	}, 60*time.Second, 250*time.Millisecond)
	assert.Contains(t, out, "hello-from-crj", "expected real process output in Cloud Logging")

	// Cleanup
	httpDoJSON(t, "DELETE", jobURL("cli-run-job"), "")
}

func TestCloudRun_CLI_RunJobFailure(t *testing.T) {
	createBody := `{
		"template": {
			"taskCount": 1,
			"template": {
				"containers": [{
					"name": "app",
					"image": "alpine:latest",
					"command": ["sh", "-c", "exit 1"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId=cli-fail-job", createBody)

	// Run the job
	out := httpDoJSON(t, "POST", jobURL("cli-fail-job:run"), "")

	var lro struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.Name)

	// Poll until the execution completes (async in the sim).
	var exec struct {
		SucceededCount int `json:"succeededCount"`
		FailedCount    int `json:"failedCount"`
		RunningCount   int `json:"runningCount"`
		Conditions     []struct {
			State string `json:"state"`
		} `json:"conditions"`
	}
	require.Eventually(t, func() bool {
		out = httpDoJSON(t, "GET", baseURL+"/v2/"+lro.Response.Name, "")
		parseJSON(t, out, &exec)
		return exec.RunningCount == 0 && exec.SucceededCount+exec.FailedCount > 0
	}, 60*time.Second, 250*time.Millisecond)
	assert.Equal(t, 0, exec.SucceededCount)
	assert.Equal(t, 1, exec.FailedCount, "expected job to fail")
	assert.Equal(t, 0, exec.RunningCount)

	// Verify at least one condition indicates failure
	found := false
	for _, c := range exec.Conditions {
		if strings.Contains(c.State, "FAILED") {
			found = true
		}
	}
	assert.True(t, found, "expected CONDITION_FAILED in conditions")

	// Cleanup
	httpDoJSON(t, "DELETE", jobURL("cli-fail-job"), "")
}
