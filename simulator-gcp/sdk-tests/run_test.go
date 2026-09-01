package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cloud Run Jobs v2 uses REST API.
// The cloud.google.com/go/run package uses gRPC by default,
// so we use direct HTTP calls against the REST API.

func TestCloudRun_CreateJob(t *testing.T) {
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"containers": []map[string]any{
					{"image": "alpine:latest"},
				},
			},
		},
	}
	body, _ := json.Marshal(job)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId=test-job",
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
	assert.Equal(t, "projects/test-project/locations/us-central1/jobs/test-job", response["name"])
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
					{"image": "alpine:latest"},
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
	createCloudRunJob(t, "get-job")

	getReq, err := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/get-job", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result), "body: %s", data)
	assert.Equal(t, "projects/test-project/locations/us-central1/jobs/get-job", result["name"])
}

func TestCloudRun_ListJobs(t *testing.T) {
	// A list method is only worth anything if a job that exists reaches the
	// caller, so the list must carry the job this test just created.
	const jobID = "list-job"
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
	const jobID = "del-job"
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
	// Create a job with a unique name for this test
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"timeout": "1s",
				"containers": []map[string]any{
					{"image": "alpine:latest"},
				},
			},
		},
	}
	body, _ := json.Marshal(job)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId=log-inject-job",
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()

	// Run the job
	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/log-inject-job:run",
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
	entries := waitForJobLogEntries(t, "log-inject-job", func(entries []jobLogEntry) bool {
		return len(entries) >= 2
	})

	for _, entry := range entries {
		assert.Equal(t, "cloud_run_job", entry.resourceType)
		assert.Equal(t, "log-inject-job", entry.jobName)
	}
	messages := jobLogMessages(entries)
	assert.Equal(t, "Container started", messages[0])
	assert.Equal(t, "Execution completed successfully", messages[1])
}

// createAndRunJob creates a job and runs it, returning the execution name from the LRO response.
func createAndRunJob(t *testing.T, jobID string) string {
	t.Helper()
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"timeout": "1s",
				"containers": []map[string]any{
					{"image": "alpine:latest"},
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

	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	defer runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)

	var lro map[string]any
	data, _ := io.ReadAll(runResp.Body)
	require.NoError(t, json.Unmarshal(data, &lro))
	response := lro["response"].(map[string]any)
	return response["name"].(string)
}

// waitExecutionDone polls getExecution until the execution completes
// (completionTime set) and returns it. The sim runs the job asynchronously
// (container start + completion), so a fixed sleep races a loaded CI runner.
// The poll runs on the calling goroutine so a failing fetch fails the test with
// its own error rather than being retried until the deadline expires.
// awaitExecutionRunning returns the first snapshot in which the execution has
// a task running. An execution that settles without ever reporting one is the
// failure — the running state is what the caller is here to observe, and a
// point sample taken after it has passed reports its absence as if it never
// happened.
func awaitExecutionRunning(t *testing.T, execName string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		exec := getExecution(t, execName)
		if running, _ := exec["runningCount"].(float64); running > 0 {
			return exec
		}
		if ct, _ := exec["completionTime"].(string); ct != "" {
			t.Fatalf("execution %q settled without ever reporting a running task: %v", execName, exec)
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %q never reported a running task within 60s: %v", execName, exec)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitExecutionDone(t *testing.T, execName string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		exec := getExecution(t, execName)
		if ct, _ := exec["completionTime"].(string); ct != "" {
			return exec
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %q did not complete within 60s: %v", execName, exec)
		}
		time.Sleep(200 * time.Millisecond)
	}
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

func TestCloudRun_ExecutionRunningState(t *testing.T) {
	const jobID = "status-running-job"
	const marker = "status-running-marker"

	// The container announces itself on stdout and then holds, so the running
	// snapshot below is taken while a workload container is genuinely up
	// rather than while the execution record merely claims one.
	//
	// The snapshot is found by watching for it, not by sampling once after the
	// marker arrives. Waiting for the marker and then reading is a race the
	// test loses under load: the marker's trip through Cloud Logging can take
	// longer than the container's hold, and then the read finds an execution
	// that has already settled. Widening the hold only moves the number.
	// Watching cannot lose it — the running state either occurs, and the poll
	// sees it, or it never occurs, which is the defect this test is for.
	execName := createAndRunJobWithImageAndCommand(t, jobID, commandImageName,
		[]string{"log", marker, "30"}, "120s")
	waitForJobLogMessage(t, jobID, marker)

	exec := awaitExecutionRunning(t, execName)
	assert.Equal(t, float64(1), exec["runningCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.Empty(t, exec["completionTime"])

	// The running task was a real container: once it exits, the execution
	// settles from its exit status as one succeeded task.
	done := waitExecutionDone(t, execName)
	assert.Equal(t, float64(0), done["runningCount"])
	assert.Equal(t, float64(1), done["succeededCount"])
	assert.Equal(t, float64(0), done["failedCount"])
	assert.NotEmpty(t, done["completionTime"])
}

func TestCloudRun_ExecutionSucceededState(t *testing.T) {
	execName := createAndRunJob(t, "status-succeeded-job")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])
}

func TestCloudRun_ExecutionCancelledState(t *testing.T) {
	const jobID = "status-cancel-job"
	const marker = "status-cancel-marker"

	// The container announces itself on stdout and then holds until it is
	// cancelled. Cancelling a workload that had already exited would settle
	// the execution from its exit status instead, leaving the cancelled count
	// at zero and the assertions below unprovable.
	execName := createAndRunJobWithImageAndCommand(t, jobID, commandImageName,
		[]string{"log", marker, "60"}, "60s")
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
}

func createAndRunJobWithCommand(t *testing.T, jobID string, cmd []string, timeout string) string {
	return createAndRunJobWithImageAndCommand(t, jobID, "alpine:latest", cmd, timeout)
}

func createAndRunJobWithImageAndCommand(t *testing.T, jobID string, image string, cmd []string, timeout string) string {
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
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()

	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	defer runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)

	var lro map[string]any
	data, _ := io.ReadAll(runResp.Body)
	require.NoError(t, json.Unmarshal(data, &lro))
	response := lro["response"].(map[string]any)
	return response["name"].(string)
}

func TestCloudRun_ExecutionRunsCommand(t *testing.T) {
	execName := createAndRunJobWithCommand(t, "exec-cmd-job", []string{"echo", "hello"}, "5s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])
}

func TestCloudRun_ExecutionFailedState(t *testing.T) {
	execName := createAndRunJobWithCommand(t, "exec-fail-job", []string{"sh", "-c", "exit 1"}, "5s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(1), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])
}

func TestCloudRun_ExecutionLogsRealOutput(t *testing.T) {
	_ = createAndRunJobWithCommand(t, "exec-log-job", []string{"echo", "real output from process"}, "5s")

	// The process has to run and its stdout has to be ingested into Cloud
	// Logging — both async in the sim — so wait for the line the process
	// printed rather than sleeping. A failed log read fails the test here
	// instead of looking like a line that has not arrived yet.
	waitForJobLogMessage(t, "exec-log-job", "real output from process")
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
