package gcp_cli_test

import (
	"encoding/json"
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

// jobRun names what a v2 RunJob call started: the operation, which Cloud Run
// completes when the execution finishes, and the execution its Execution
// metadata names from the start.
type jobRun struct {
	Operation string
	Execution string
}

// runJob runs a job through the v2 RunJob method. gcloud's own `run jobs
// execute` drives the v1 Knative run, which answers with the Execution and no
// operation, so the v2 method is put on the wire directly.
func runJob(t *testing.T, jobID string) jobRun {
	t.Helper()
	out := httpDoJSON(t, "POST", jobURL(jobID+":run"), "")
	var op struct {
		Name     string `json:"name"`
		Metadata struct {
			Type string `json:"@type"`
			Name string `json:"name"`
		} `json:"metadata"`
	}
	parseJSON(t, out, &op)
	require.Equal(t, "type.googleapis.com/google.cloud.run.v2.Execution", op.Metadata.Type,
		"RunJob reports the Execution as its operation metadata: %s", out)
	require.NotEmpty(t, op.Name, out)
	require.NotEmpty(t, op.Metadata.Name, out)
	return jobRun{Operation: op.Name, Execution: op.Metadata.Name}
}

// runOperationResult is a finished google.longrunning.Operation of the Cloud
// Run v2 API.
type runOperationResult struct {
	Done  bool `json:"done"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Response json.RawMessage `json:"response"`
}

// waitRunOperation blocks on run.projects.locations.operations.wait until the
// operation is done. The method may answer before then, so a not-done answer is
// waited on again.
func waitRunOperation(t *testing.T, name string) runOperationResult {
	t.Helper()
	var op runOperationResult
	for !op.Done {
		op = runOperationResult{}
		parseJSON(t, httpDoJSON(t, "POST", baseURL+"/v2/"+name+":wait", `{"timeout":"120s"}`), &op)
	}
	return op
}

func TestCloudRun_CLI_RunJobAndCheckLogs(t *testing.T) {
	// Create a Cloud Run Job with echo command
	createBody := `{
		"template": {
			"taskCount": 1,
			"template": {
				"containers": [{
					"name": "app",
					"image": "` + cliWorkloadImage + `",
					"command": ["echo", "hello-from-crj"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId=cli-run-job", createBody)

	// Run the job and wait on its operation, which completes when the
	// execution does.
	run := runJob(t, "cli-run-job")
	op := waitRunOperation(t, run.Operation)
	require.Nil(t, op.Error, "the execution succeeded, so its RunJob operation did")

	var exec struct {
		SucceededCount int `json:"succeededCount"`
		FailedCount    int `json:"failedCount"`
		RunningCount   int `json:"runningCount"`
	}
	out := httpDoJSON(t, "GET", baseURL+"/v2/"+run.Execution, "")
	parseJSON(t, out, &exec)
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
					"image": "` + cliWorkloadImage + `",
					"command": ["sh", "-c", "exit 1"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId=cli-fail-job", createBody)

	// Run the job and wait on its operation, which completes when the
	// execution does and carries the execution's failure.
	run := runJob(t, "cli-fail-job")
	op := waitRunOperation(t, run.Operation)
	require.NotNil(t, op.Error, "a failed execution fails its RunJob operation")
	assert.Contains(t, op.Error.Message, "has failed to complete, 0/1 tasks were a success")

	var exec struct {
		SucceededCount int `json:"succeededCount"`
		FailedCount    int `json:"failedCount"`
		RunningCount   int `json:"runningCount"`
		Conditions     []struct {
			State string `json:"state"`
		} `json:"conditions"`
	}
	out := httpDoJSON(t, "GET", baseURL+"/v2/"+run.Execution, "")
	parseJSON(t, out, &exec)
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
