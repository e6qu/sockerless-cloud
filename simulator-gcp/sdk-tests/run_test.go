package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Cloud Run Jobs v2 uses REST API.
// The cloud.google.com/go/run package uses gRPC by default,
// so we use direct HTTP calls against the REST API.

func TestCloudRun_CreateJob(t *testing.T) {
	jobID := uniqueName("test-job")
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"containers": []map[string]any{
					{"image": "public.ecr.aws/docker/library/alpine:latest"},
				},
			},
		},
	}
	body, _ := json.Marshal(job)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result), "body: %s", data)

	// The response is a long-running operation that already completed, and it
	// carries the created job as its embedded response.
	done, ok := result["done"].(bool)
	require.True(t, ok, "the operation must report a boolean done: %s", data)
	assert.True(t, done, "the create operation is complete")

	response, ok := result["response"].(map[string]any)
	require.True(t, ok, "a completed operation must carry the created job: %s", data)
	assert.Equal(t, "type.googleapis.com/google.cloud.run.v2.Job", response["@type"])
	assert.Equal(t, "projects/test-project/locations/us-central1/jobs/"+jobID, response["name"])
}

// createCloudRunJob creates a single-container Cloud Run job and requires the
// create to succeed. The tests that exercise the other job methods need a job
// to address; this is the job they address.
func createCloudRunJob(t *testing.T, jobID string) {
	t.Helper()
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"containers": []map[string]any{
					{"image": "public.ecr.aws/docker/library/alpine:latest"},
				},
			},
		},
	}
	body, err := json.Marshal(job)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestCloudRun_GetJob(t *testing.T) {
	jobID := uniqueName("get-job")
	createCloudRunJob(t, jobID)

	getReq, err := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result), "body: %s", data)
	assert.Equal(t, "projects/test-project/locations/us-central1/jobs/"+jobID, result["name"])
}

func TestCloudRun_ListJobs(t *testing.T) {
	// A list method is only worth anything if a job that exists reaches the
	// caller, so the list must carry the job this test just created.
	jobID := uniqueName("list-job")
	createCloudRunJob(t, jobID)

	req, err := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var list struct {
		Jobs []struct {
			Name string `json:"name"`
		} `json:"jobs"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))

	var names []string
	for _, job := range list.Jobs {
		names = append(names, job.Name)
	}
	assert.Contains(t, names, "projects/test-project/locations/us-central1/jobs/"+jobID)
}

func TestCloudRun_DeleteJob(t *testing.T) {
	jobID := uniqueName("del-job")
	createCloudRunJob(t, jobID)

	delReq, err := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The delete removed the job: a get on it now finds nothing.
	getReq, err := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID, nil)
	require.NoError(t, err)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode, "the deleted job must be gone")
}

func TestCloudRun_RunJobInjectsLogEntries(t *testing.T) {
	jobID := uniqueName("log-inject-job")
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"timeout": "1s",
				"containers": []map[string]any{
					{"image": "public.ecr.aws/docker/library/alpine:latest"},
				},
			},
		},
	}
	body, _ := json.Marshal(job)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode, "create job %s", jobID)

	// Run the job
	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)

	// Poll the log query (same filter the backend uses) until the execution
	// has run and BOTH the start + completion entries are ingested — both are
	// async in the sim, so a fixed sleep races a loaded runner. The entries are
	// asserted after the wait, not inside it: an assertion inside a poll that
	// keeps polling reports nothing until the deadline.
	entries := waitForJobLogEntries(t, jobID, func(entries []jobLogEntry) bool {
		return len(entries) >= 2
	})

	for _, entry := range entries {
		assert.Equal(t, "cloud_run_job", entry.resourceType)
		assert.Equal(t, jobID, entry.jobName)
	}
	messages := jobLogMessages(entries)
	assert.Equal(t, "Container started", messages[0])
	assert.Equal(t, "Execution completed successfully", messages[1])
}

// createAndRunJob creates a job and runs it, returning what the run started.
func createAndRunJob(t *testing.T, jobID string) jobRun {
	t.Helper()
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"timeout": "1s",
				"containers": []map[string]any{
					{"image": "public.ecr.aws/docker/library/alpine:latest"},
				},
			},
		},
	}
	body, _ := json.Marshal(job)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode, "create job %s", jobID)

	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	defer runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)
	return readJobRun(t, runResp.Body)
}

// jobRun names what a RunJob call started: the operation, which completes when
// the execution finishes, and the execution, which the operation's Execution
// metadata names from the start.
type jobRun struct {
	Operation string
	Execution string
}

func readJobRun(t *testing.T, body io.Reader) jobRun {
	t.Helper()
	var lro struct {
		Name     string `json:"name"`
		Metadata struct {
			Type string `json:"@type"`
			Name string `json:"name"`
		} `json:"metadata"`
	}
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &lro), "RunJob response: %s", data)
	require.Equal(t, "type.googleapis.com/google.cloud.run.v2.Execution", lro.Metadata.Type,
		"RunJob reports the Execution as its operation metadata: %s", data)
	require.NotEmpty(t, lro.Name, "RunJob response: %s", data)
	require.NotEmpty(t, lro.Metadata.Name, "RunJob response: %s", data)
	return jobRun{Operation: lro.Name, Execution: lro.Metadata.Name}
}

// waitJobRun waits on the RunJob operation through the Cloud Run SDK, which
// completes it when the execution finishes, and returns the settled execution
// together with the operation's outcome: nil for an execution whose tasks all
// succeeded, the operation's error otherwise.
func waitJobRun(t *testing.T, run jobRun) (map[string]any, error) {
	t.Helper()
	_, err := newJobsClient(t).RunJobOperation(run.Operation).Wait(ctx)
	return getExecution(t, run.Execution), err
}

func getExecution(t *testing.T, execName string) map[string]any {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+"/v2/"+execName, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	data, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

// heldJob is a Cloud Run job execution whose container announced itself on
// stdout and holds until the test releases it.
type heldJob struct {
	jobRun
	release func()
}

// runHeldJob runs a job whose container prints marker and then holds until an
// object named release appears in a Cloud Storage bucket the job mounts as a
// volume. The test ends the hold by writing that object, so no fixed duration
// decides how long the workload runs.
func runHeldJob(t *testing.T, jobID, marker string) heldJob {
	t.Helper()
	client := storageClient(t)
	t.Cleanup(func() { client.Close() })
	bucket := client.Bucket(uniqueName("held-job-release"))
	require.NoError(t, bucket.Create(ctx, "test-project", nil))

	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"timeout":    "120s",
				"maxRetries": 0,
				"containers": []map[string]any{{
					"image":        commandImageName,
					"args":         []string{"log-until", marker, "/mnt/release/release"},
					"volumeMounts": []map[string]any{{"name": "release", "mountPath": "/mnt/release"}},
				}},
				"volumes": []map[string]any{{
					"name": "release",
					"gcs":  map[string]any{"bucket": bucket.BucketName(), "readOnly": true},
				}},
			},
		},
	}
	body, _ := json.Marshal(job)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode, "create job %s", jobID)

	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	defer runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)
	return heldJob{
		jobRun: readJobRun(t, runResp.Body),
		release: func() {
			putVolumeObject(t, bucket, "release", "text/plain", "")
		},
	}
}

func TestCloudRun_ExecutionRunningState(t *testing.T) {
	jobID := uniqueName("status-running-job")
	const marker = "status-running-marker"

	// The container announces itself on stdout and holds until released, so
	// the snapshot below is taken while a workload container is genuinely up
	// rather than while the execution record merely claims one.
	run := runHeldJob(t, jobID, marker)
	waitForJobLogMessage(t, jobID, marker)

	exec := getExecution(t, run.Execution)
	assert.Equal(t, float64(1), exec["runningCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.Empty(t, exec["completionTime"])

	// The running task was a real container: once it exits, the execution
	// settles from its exit status as one succeeded task.
	run.release()
	done, err := waitJobRun(t, run.jobRun)
	require.NoError(t, err)
	assert.Equal(t, float64(0), done["runningCount"])
	assert.Equal(t, float64(1), done["succeededCount"])
	assert.Equal(t, float64(0), done["failedCount"])
	assert.NotEmpty(t, done["completionTime"])
}

func TestCloudRun_ExecutionSucceededState(t *testing.T) {
	run := createAndRunJob(t, uniqueName("status-succeeded-job"))

	exec, err := waitJobRun(t, run)
	require.NoError(t, err)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])
}

func TestCloudRun_ExecutionCancelledState(t *testing.T) {
	jobID := uniqueName("status-cancel-job")
	const marker = "status-cancel-marker"

	// The container announces itself on stdout and then holds, never released,
	// until it is cancelled. Cancelling a workload that had already exited would settle
	// the execution from its exit status instead, leaving the cancelled count
	// at zero and the assertions below unprovable.
	run := runHeldJob(t, jobID, marker)
	execName := run.Execution
	waitForJobLogMessage(t, jobID, marker)

	running := getExecution(t, execName)
	require.Equal(t, float64(1), running["runningCount"], "the execution is running when the cancel arrives")
	require.Empty(t, running["completionTime"])

	parts := strings.SplitN(execName, "/executions/", 2)
	cancelURL := baseURL + "/v2/" + parts[0] + "/executions/" + parts[1] + ":cancel"
	cancelReq, _ := http.NewRequestWithContext(ctx, "POST", cancelURL, strings.NewReader("{}"))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	require.NoError(t, err)
	cancelResp.Body.Close()
	require.Equal(t, http.StatusOK, cancelResp.StatusCode)

	exec := getExecution(t, execName)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(1), exec["cancelledCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])

	// The RunJob operation ends with the cancellation once the workload stops.
	_, err = waitJobRun(t, run.jobRun)
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err), "RunJob operation error: %v", err)
}

func createAndRunJobWithCommand(t *testing.T, jobID string, cmd []string, timeout string) jobRun {
	return createAndRunJobWithImageAndCommand(t, jobID, "public.ecr.aws/docker/library/alpine:latest", cmd, timeout)
}

func createAndRunJobWithImageAndCommand(t *testing.T, jobID string, image string, cmd []string, timeout string) jobRun {
	t.Helper()
	return createAndRunJobInProject(t, "test-project", jobID, image, cmd, timeout)
}

func createAndRunJobInProject(t *testing.T, project, jobID string, image string, cmd []string, timeout string) jobRun {
	t.Helper()
	containers := []map[string]any{
		{
			"image": image,
			"args":  cmd,
		},
	}
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"timeout":    timeout,
				"containers": containers,
			},
		},
	}
	body, _ := json.Marshal(job)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/"+project+"/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode, "create job %s", jobID)

	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/"+project+"/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	defer runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)
	return readJobRun(t, runResp.Body)
}

func TestCloudRun_ExecutionRunsCommand(t *testing.T) {
	run := createAndRunJobWithCommand(t, uniqueName("exec-cmd-job"), []string{"echo", "hello"}, "5s")

	exec, err := waitJobRun(t, run)
	require.NoError(t, err)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])
}

func TestCloudRun_ExecutionFailedState(t *testing.T) {
	run := createAndRunJobWithCommand(t, uniqueName("exec-fail-job"), []string{"sh", "-c", "exit 1"}, "5s")

	exec, err := waitJobRun(t, run)
	require.Error(t, err, "a failed execution fails its RunJob operation")
	assert.Contains(t, err.Error(), "has failed to complete, 0/1 tasks were a success")
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(1), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])
}

func TestCloudRun_ExecutionLogsRealOutput(t *testing.T) {
	jobID := uniqueName("exec-log-job")
	_ = createAndRunJobWithCommand(t, jobID, []string{"echo", "real output from process"}, "5s")

	// The process has to run and its stdout has to be ingested into Cloud
	// Logging — both async in the sim — so wait for the line the process
	// printed rather than sleeping. A failed log read fails the test here
	// instead of looking like a line that has not arrived yet.
	waitForJobLogMessage(t, jobID, "real output from process")
}

// containsString reports whether want is an element of msgs.
func containsString(msgs []string, want string) bool {
	for _, m := range msgs {
		if m == want {
			return true
		}
	}
	return false
}
