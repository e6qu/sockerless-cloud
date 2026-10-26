package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/functions/apiv2/functionspb"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestIntegration_CloudRunJobLifecycle creates, runs, observes, cancels and
// deletes a job.
func TestIntegration_CloudRunJobLifecycle(t *testing.T) {
	t.Parallel()
	jobID := uniquename.New("integ-crj")
	const marker = "integ-crj-running"

	// The container announces itself and holds, so every step runs against a
	// running workload: one that had exited would settle the execution from its
	// exit status and leave nothing to cancel.
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

	runReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID+":run",
		strings.NewReader("{}"))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	require.NoError(t, err)
	defer runResp.Body.Close()
	require.Equal(t, http.StatusOK, runResp.StatusCode)
	run := readJobRun(t, runResp.Body)
	execName := run.Execution

	// The container's own stdout line reached Cloud Logging, so the workload
	// started.
	entries := waitForJobLogEntries(t, jobID, func(entries []jobLogEntry) bool {
		return containsString(jobLogMessages(entries), marker)
	})
	assert.Equal(t, "cloud_run_job", entries[0].resourceType)
	assert.Equal(t, jobID, entries[0].jobName)
	assert.Equal(t, marker, entries[0].message)

	exec := getExecution(t, execName)
	assert.Equal(t, float64(1), exec["runningCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.Empty(t, exec["completionTime"])

	parts := strings.SplitN(execName, "/executions/", 2)
	cancelURL := baseURL + "/v2/" + parts[0] + "/executions/" + parts[1] + ":cancel"
	cancelReq, _ := http.NewRequestWithContext(ctx, "POST", cancelURL, strings.NewReader("{}"))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	require.NoError(t, err)
	cancelResp.Body.Close()
	require.Equal(t, http.StatusOK, cancelResp.StatusCode)

	// The running task was cancelled, not settled from an exit status.
	exec = getExecution(t, execName)
	assert.Equal(t, float64(0), exec["runningCount"])
	assert.Equal(t, float64(1), exec["cancelledCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])
	assert.NotEmpty(t, exec["completionTime"])

	// The RunJob operation ends with the cancellation.
	_, err = waitJobRun(t, run)
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err), "RunJob operation error: %v", err)

	delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	assert.Equal(t, http.StatusOK, delResp.StatusCode)

	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v2/projects/test-project/locations/us-central1/jobs/"+jobID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}

func TestIntegration_CloudFunctionsLifecycle(t *testing.T) {
	t.Parallel()
	const fnID = "integ-gcf"
	client := newFunctionsClient(t)
	fn := deployFunction(t, client, fnID, &runpb.Container{Image: httpProbeImageName, Args: []string{"log-request"}})

	code, _, body := invokeService(t, invokerIDToken(t, fn.ServiceConfig.Uri), fn.ServiceConfig.Uri, http.MethodPost, "/integ", "{}")
	require.Equal(t, http.StatusOK, code, "body=%q", body)
	assert.Equal(t, "POST /integ", body)

	waitForFunctionLogMessage(t, fnID, "POST /integ")

	op, err := client.DeleteFunction(ctx, &functionspb.DeleteFunctionRequest{Name: fn.Name})
	require.NoError(t, err)
	require.NoError(t, op.Wait(ctx))
	_, err = client.GetFunction(ctx, &functionspb.GetFunctionRequest{Name: fn.Name})
	assert.Equal(t, codes.NotFound, status.Code(err), "a deleted function must be gone: %v", err)
}
