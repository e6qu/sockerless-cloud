package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/logging/logadmin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_CloudRunJobLifecycle exercises the full Cloud Run backend
// flow against the simulator: create → run → verify running → logs → cancel → verify cancelled → delete.
func TestIntegration_CloudRunJobLifecycle(t *testing.T) {
	const jobID = "integ-crj"
	const marker = "integ-crj-running"

	// 1. Create job. Its container announces itself on stdout and then holds,
	// so every step below runs against a workload that is really up: a
	// container that had already exited would settle the execution from its
	// exit status and leave the cancel with nothing to cancel.
	job := map[string]any{
		"template": map[string]any{
			"template": map[string]any{
				"timeout": "60s",
				"containers": []map[string]any{
					{"image": commandImageName, "args": []string{"log", marker, "60"}},
				},
			},
		},
	}
	body, err := json.Marshal(job)
	require.NoError(t, err)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs?jobId="+jobID,
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	// 2. Run job
	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	defer runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)

	var lro map[string]any
	data, err := io.ReadAll(runResp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &lro))
	response, ok := lro["response"].(map[string]any)
	require.True(t, ok, "the run operation must carry the execution: %s", data)
	execName, ok := response["name"].(string)
	require.True(t, ok, "the execution must be named: %s", data)

	// 3. The workload really started: the line the container printed on its
	// own stdout reached Cloud Logging, and the execution's own start entry
	// precedes it.
	entries := waitForJobLogEntries(t, jobID, func(entries []jobLogEntry) bool {
		return containsString(jobLogMessages(entries), marker)
	})
	assert.Equal(t, "cloud_run_job", entries[0].resourceType)
	assert.Equal(t, jobID, entries[0].jobName)
	assert.Equal(t, "Container started", entries[0].message)

	// 4. Verify running state while that container is still holding.
	exec := getExecution(t, execName)
	assert.Equal(t, float64(1), exec["runningCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.Empty(t, exec["completionTime"])

	// 5. Cancel execution
	parts := strings.SplitN(execName, "/executions/", 2)
	cancelURL := baseURL + "/v2/" + parts[0] + "/executions/" + parts[1] + ":cancel"
	cancelReq, _ := http.NewRequestWithContext(ctx, "POST", cancelURL, strings.NewReader("{}"))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	require.NoError(t, err)
	cancelResp.Body.Close()
	require.Equal(t, http.StatusOK, cancelResp.StatusCode)

	// 6. Verify cancelled state — the running task was cancelled, not settled
	// from an exit status.
	exec = getExecution(t, execName)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(1), exec["cancelledCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])

	// 7. Delete job
	delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	assert.Equal(t, http.StatusOK, delResp.StatusCode)

	// Verify job is gone
	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}

// TestIntegration_CloudFunctionsLifecycle exercises the full Cloud Functions
// flow: create → verify URI → invoke → logs → delete.
func TestIntegration_CloudFunctionsLifecycle(t *testing.T) {
	const fnID = "integ-gcf"

	// 1. Create function
	fn := map[string]any{
		"buildConfig": map[string]any{
			"runtime":    "go121",
			"entryPoint": "Handler",
		},
	}
	body, _ := json.Marshal(fn)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/functions?functionId="+fnID,
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	// 2. Extract and verify ServiceConfig.Uri
	var lro map[string]any
	data, _ := io.ReadAll(createResp.Body)
	require.NoError(t, json.Unmarshal(data, &lro))
	response := lro["response"].(map[string]any)
	svcConfig := response["serviceConfig"].(map[string]any)
	uri := svcConfig["uri"].(string)
	assert.Contains(t, uri, "/v2-functions-invoke/"+fnID)

	// 3. Invoke function via the returned URI
	invokeResp, err := http.DefaultClient.Post(uri, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	invokeResp.Body.Close()
	assert.Equal(t, http.StatusOK, invokeResp.StatusCode)

	// 4. Verify log entries
	logClient := logadminClient(t)
	filter := `resource.type="cloud_run_revision" AND resource.labels.service_name="` + fnID + `"`
	it := logClient.Entries(ctx, logadmin.Filter(filter))

	entry, err := it.Next()
	require.NoError(t, err)
	assert.Equal(t, "cloud_run_revision", entry.Resource.Type)
	assert.Equal(t, fnID, entry.Resource.Labels["service_name"])
	assert.Equal(t, "Function invoked", entry.Payload)

	// 5. Delete function
	delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/v2/projects/test-project/locations/us-central1/functions/"+fnID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	assert.Equal(t, http.StatusOK, delResp.StatusCode)

	// Verify function is gone
	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v2/projects/test-project/locations/us-central1/functions/"+fnID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}
