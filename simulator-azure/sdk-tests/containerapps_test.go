package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Container Apps Jobs use the Microsoft.App provider.
// The armappcontainers SDK requires specific API versions that may differ.
// Using direct HTTP calls for more reliable testing against the simulator.

func TestContainerApps_CreateJob(t *testing.T) {
	// Ensure resource group exists
	rgBody := `{"location":"eastus"}`
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/aca-rg?api-version=2023-07-01",
		strings.NewReader(rgBody))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	job := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"configuration": map[string]any{
				"triggerType":    "Manual",
				"replicaTimeout": 1,
			},
			"template": map[string]any{
				"containers": []map[string]any{
					{
						"name":  "worker",
						"image": "public.ecr.aws/docker/library/alpine:latest",
					},
				},
			},
		},
	}
	body, _ := json.Marshal(job)

	req, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/aca-rg/providers/Microsoft.App/jobs/test-job?api-version=2024-03-01",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Contains(t, []int{200, 201}, resp.StatusCode)

	var result map[string]any
	data, _ := io.ReadAll(resp.Body)
	json.Unmarshal(data, &result)
	assert.Equal(t, "test-job", result["name"])
}

func TestContainerApps_StartJobInjectsLogs(t *testing.T) {
	// Ensure resource group exists
	rgBody := `{"location":"eastus"}`
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/aca-log-rg?api-version=2023-07-01",
		strings.NewReader(rgBody))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	// Create job
	job := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"configuration": map[string]any{
				"triggerType":    "Manual",
				"replicaTimeout": 1,
			},
			"template": map[string]any{
				"containers": []map[string]any{
					{"name": "worker", "image": "public.ecr.aws/docker/library/alpine:latest"},
				},
			},
		},
	}
	body, _ := json.Marshal(job)
	createReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/aca-log-rg/providers/Microsoft.App/jobs/log-test-job?api-version=2024-03-01",
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", simARMBearer)
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()

	// Start execution
	startReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/aca-log-rg/providers/Microsoft.App/jobs/log-test-job/start?api-version=2024-03-01",
		strings.NewReader("{}"))
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("Authorization", simARMBearer)
	startResp, err := http.DefaultClient.Do(startReq)
	require.NoError(t, err)
	startResp.Body.Close()
	require.Equal(t, http.StatusAccepted, startResp.StatusCode)

	// Poll until both the start + completion log entries are ingested (async
	// in the sim) — a fixed sleep races a loaded runner.
	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "log-test-job"`
	var result queryResponse
	require.Eventually(t, func() bool {
		result = queryWorkspace(t, "default", kql)
		return len(result.Tables) == 1 && len(result.Tables[0].Rows) >= 2
	}, 60*time.Second, 200*time.Millisecond)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]
	require.GreaterOrEqual(t, len(table.Rows), 2, "should have at least start and completion entries")

	// Find Log_s column index
	logIdx := -1
	for i, col := range table.Columns {
		if col.Name == "Log_s" {
			logIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, logIdx, 0)

	assert.Equal(t, "Container started", table.Rows[0][logIdx])
	assert.Equal(t, "Execution completed successfully", table.Rows[1][logIdx])
}

func TestContainerApps_MultiContainerJobSharesLocalhost(t *testing.T) {
	rg := "aca-pod-rg"
	rgBody := `{"location":"eastus"}`
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(rgBody))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	jobName := "aca-pod-localhost"
	job := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"configuration": map[string]any{
				"triggerType":    "Manual",
				"replicaTimeout": 30,
			},
			"template": map[string]any{
				"containers": []map[string]any{
					{
						"name":  "main",
						"image": httpProbeImageName,
						"args":  []string{"probe-once", "aca-sidecar-ok"},
					},
					{
						"name":  "sidecar",
						"image": httpProbeImageName,
						"args":  []string{"server"},
					},
				},
			},
		},
	}
	body, _ := json.Marshal(job)
	createReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", simARMBearer)
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()

	startReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"/start?api-version=2024-03-01",
		strings.NewReader("{}"))
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("Authorization", simARMBearer)
	startResp, err := http.DefaultClient.Do(startReq)
	require.NoError(t, err)
	startResp.Body.Close()
	require.Less(t, startResp.StatusCode, 400)

	require.Eventually(t, func() bool {
		result := queryWorkspace(t, "default", `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "`+jobName+`"`)
		if len(result.Tables) == 0 {
			return false
		}
		for _, row := range result.Tables[0].Rows {
			for _, cell := range row {
				if s, ok := cell.(string); ok && strings.Contains(s, "aca-sidecar-ok") {
					return true
				}
			}
		}
		return false
	}, 20*time.Second, 500*time.Millisecond)
}

// acaCreateJob creates a Container Apps Job and returns its resource ID.
func acaCreateJob(t *testing.T, rg, jobName string) {
	t.Helper()

	// Ensure resource group exists
	rgBody := `{"location":"eastus"}`
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(rgBody))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	job := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"configuration": map[string]any{
				"triggerType":    "Manual",
				"replicaTimeout": 1,
			},
			"template": map[string]any{
				"containers": []map[string]any{
					{"name": "worker", "image": "public.ecr.aws/docker/library/alpine:latest"},
				},
			},
		},
	}
	body, _ := json.Marshal(job)
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, []int{200, 201}, resp.StatusCode)
}

// acaStartExecution starts a job execution and returns the execution name from the response.
func acaStartExecution(t *testing.T, rg, jobName string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"/start?api-version=2024-03-01",
		strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result map[string]any
	data, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(data, &result))
	return result["name"].(string)
}

// acaGetExecution returns the execution status.
// acaWaitExecution polls acaGetExecution until the execution reaches a
// terminal state (endTime set). The sim runs the job asynchronously, so a
// fixed sleep races a loaded CI runner.
func acaWaitExecution(t *testing.T, rg, jobName, execName string) map[string]any {
	t.Helper()
	var exec map[string]any
	require.Eventually(t, func() bool {
		exec = acaGetExecution(t, rg, jobName, execName)
		props, _ := exec["properties"].(map[string]any)
		if props == nil {
			return false
		}
		et, _ := props["endTime"].(string)
		return et != ""
	}, 60*time.Second, 200*time.Millisecond)
	return exec
}

func acaGetExecution(t *testing.T, rg, jobName, execName string) map[string]any {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"/executions/"+execName+"?api-version=2024-03-01",
		nil)
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	data, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func TestContainerApps_ExecutionRunningState(t *testing.T) {
	acaCreateJob(t, "status-rg", "running-job")
	execName := acaStartExecution(t, "status-rg", "running-job")

	// Immediately check — should be Running
	exec := acaGetExecution(t, "status-rg", "running-job", execName)
	props := exec["properties"].(map[string]any)
	assert.Equal(t, "Running", props["status"])
	assert.NotEmpty(t, props["startTime"])
}

func TestContainerApps_ExecutionSucceededState(t *testing.T) {
	acaCreateJob(t, "status-rg", "succeed-job")
	execName := acaStartExecution(t, "status-rg", "succeed-job")

	exec := acaWaitExecution(t, "status-rg", "succeed-job", execName)
	props := exec["properties"].(map[string]any)
	assert.Equal(t, "Succeeded", props["status"])
	assert.NotEmpty(t, props["endTime"])
}

func TestContainerApps_ExecutionStoppedState(t *testing.T) {
	acaCreateJob(t, "status-rg", "stop-job")
	execName := acaStartExecution(t, "status-rg", "stop-job")

	// Stop the execution
	stopReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/status-rg/providers/Microsoft.App/jobs/stop-job/executions/"+execName+"/stop?api-version=2024-03-01",
		strings.NewReader("{}"))
	stopReq.Header.Set("Content-Type", "application/json")
	stopReq.Header.Set("Authorization", simARMBearer)
	stopResp, err := http.DefaultClient.Do(stopReq)
	require.NoError(t, err)
	stopResp.Body.Close()
	require.Equal(t, http.StatusOK, stopResp.StatusCode)

	exec := acaGetExecution(t, "status-rg", "stop-job", execName)
	props := exec["properties"].(map[string]any)
	assert.Equal(t, "Stopped", props["status"])
	assert.NotEmpty(t, props["endTime"])
}

// acaCreateJobWithCommand creates a Container Apps Job with a command.
func acaCreateJobWithCommand(t *testing.T, rg, jobName string, cmd []string) {
	acaCreateJobWithImageAndCommand(t, rg, jobName, "public.ecr.aws/docker/library/alpine:latest", cmd)
}

// acaCreateJobWithImageAndCommand creates a Container Apps Job with a specific image and command.
func acaCreateJobWithImageAndCommand(t *testing.T, rg, jobName, image string, cmd []string) {
	t.Helper()

	// Ensure resource group exists
	rgBody := `{"location":"eastus"}`
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(rgBody))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	container := map[string]any{
		"name":  "worker",
		"image": image,
		"args":  cmd,
	}
	job := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"configuration": map[string]any{
				"triggerType":    "Manual",
				"replicaTimeout": 5,
			},
			"template": map[string]any{
				"containers": []map[string]any{container},
			},
		},
	}
	body, _ := json.Marshal(job)
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, []int{200, 201}, resp.StatusCode)
}

func TestContainerApps_ExecutionRunsCommand(t *testing.T) {
	acaCreateJobWithCommand(t, "exec-rg", "exec-cmd-job", []string{"echo", "hello"})
	execName := acaStartExecution(t, "exec-rg", "exec-cmd-job")

	exec := acaWaitExecution(t, "exec-rg", "exec-cmd-job", execName)
	props := exec["properties"].(map[string]any)
	assert.Equal(t, "Succeeded", props["status"])
	assert.NotEmpty(t, props["endTime"])
}

func TestContainerApps_ExecutionFailedStatus(t *testing.T) {
	acaCreateJobWithCommand(t, "exec-rg", "exec-fail-job", []string{"sh", "-c", "exit 1"})
	execName := acaStartExecution(t, "exec-rg", "exec-fail-job")

	exec := acaWaitExecution(t, "exec-rg", "exec-fail-job", execName)
	props := exec["properties"].(map[string]any)
	assert.Equal(t, "Failed", props["status"])
	assert.NotEmpty(t, props["endTime"])
}

func TestContainerApps_ExecutionLogsRealOutput(t *testing.T) {
	acaCreateJobWithCommand(t, "exec-rg", "exec-log-job", []string{"echo", "real aca output"})
	_ = acaStartExecution(t, "exec-rg", "exec-log-job")

	// Poll until the process has run and its stdout is ingested (async in the
	// sim) — a fixed sleep races a loaded runner.
	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "exec-log-job"`
	var result queryResponse
	require.Eventually(t, func() bool {
		result = queryWorkspace(t, "default", kql)
		if len(result.Tables) != 1 {
			return false
		}
		for _, row := range result.Tables[0].Rows {
			for _, cell := range row {
				if s, ok := cell.(string); ok && s == "real aca output" {
					return true
				}
			}
		}
		return false
	}, 60*time.Second, 200*time.Millisecond)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]

	// Find Log_s column index
	logIdx := -1
	for i, col := range table.Columns {
		if col.Name == "Log_s" {
			logIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, logIdx, 0)

	// Should have at least "Container started" and "real aca output"
	var logs []string
	for _, row := range table.Rows {
		logs = append(logs, row[logIdx].(string))
	}
	assert.Contains(t, logs, "real aca output", "process stdout should appear in Log Analytics")
}

func TestContainerApps_GetJob(t *testing.T) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/aca-rg/providers/Microsoft.App/jobs/test-job?api-version=2024-03-01",
		nil)
	req.Header.Set("Authorization", simARMBearer)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	data, _ := io.ReadAll(resp.Body)
	json.Unmarshal(data, &result)
	assert.Equal(t, "test-job", result["name"])
}

// --- SDK-level tests using armappcontainers ---

// ensureRG creates a resource group via raw HTTP (needed before SDK calls).
func ensureRG(t *testing.T, rg string) {
	t.Helper()
	rgBody := `{"location":"eastus"}`
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(rgBody))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()
}

func TestSDK_ContainerApps_CreateAndGetJob(t *testing.T) {
	rg := "sdk-aca-create-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-test-job", armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.JobProperties{
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout: to.Ptr[int32](5),
			},
			Template: &armappcontainers.JobTemplate{
				Containers: []*armappcontainers.Container{
					{
						Name:  to.Ptr("worker"),
						Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	job, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "sdk-test-job", *job.Name)
	assert.Equal(t, "Succeeded", string(*job.Properties.ProvisioningState))
	assert.Equal(t, "eastus", *job.Location)

	// GET the same job
	getResp, err := client.Get(ctx, rg, "sdk-test-job", nil)
	require.NoError(t, err)
	assert.Equal(t, "sdk-test-job", *getResp.Name)
}

func TestSDK_ContainerApps_StartAndListExecutions(t *testing.T) {
	rg := "sdk-aca-exec-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	jobsClient, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Create job with short timeout
	poller, err := jobsClient.BeginCreateOrUpdate(ctx, rg, "sdk-exec-job", armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.JobProperties{
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout: to.Ptr[int32](2),
			},
			Template: &armappcontainers.JobTemplate{
				Containers: []*armappcontainers.Container{
					{
						Name:    to.Ptr("worker"),
						Image:   to.Ptr("public.ecr.aws/docker/library/alpine:latest"),
						Command: []*string{to.Ptr("echo"), to.Ptr("sdk-hello")},
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Start execution
	startPoller, err := jobsClient.BeginStart(ctx, rg, "sdk-exec-job", nil)
	require.NoError(t, err)

	execResp, err := startPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, execResp.Name)
	execName := *execResp.Name
	assert.NotEmpty(t, execName)

	// Poll the executions list until the execution appears in a terminal
	// (Succeeded) state — completion is async in the sim, so a fixed sleep
	// races a loaded runner.
	execClient, err := armappcontainers.NewJobsExecutionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	found := false
	require.Eventually(t, func() bool {
		pager := execClient.NewListPager(rg, "sdk-exec-job", nil)
		for pager.More() {
			page, perr := pager.NextPage(ctx)
			if perr != nil {
				return false
			}
			for _, e := range page.Value {
				if e.Name != nil && *e.Name == execName && e.Properties != nil &&
					e.Properties.Status != nil && string(*e.Properties.Status) == "Succeeded" {
					found = true
					return true
				}
			}
		}
		return false
	}, 60*time.Second, 200*time.Millisecond)
	assert.True(t, found, "expected to find execution %s as Succeeded in list", execName)
}

func TestSDK_ContainerApps_ListByResourceGroup(t *testing.T) {
	rg := "sdk-aca-list-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Create two jobs
	for _, name := range []string{"sdk-list-job-a", "sdk-list-job-b"} {
		p, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.Job{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.JobProperties{
				Configuration: &armappcontainers.JobConfiguration{
					TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
					ReplicaTimeout: to.Ptr[int32](1),
				},
				Template: &armappcontainers.JobTemplate{
					Containers: []*armappcontainers.Container{
						{Name: to.Ptr("w"), Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest")},
					},
				},
			},
		}, nil)
		require.NoError(t, err)
		_, err = p.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}

	// List jobs
	pager := client.NewListByResourceGroupPager(rg, nil)
	var jobs []*armappcontainers.Job
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		jobs = append(jobs, page.Value...)
	}

	names := make(map[string]bool)
	for _, j := range jobs {
		names[*j.Name] = true
	}
	assert.True(t, names["sdk-list-job-a"], "job A should be in list")
	assert.True(t, names["sdk-list-job-b"], "job B should be in list")
}

func TestSDK_ContainerApps_DeleteJob(t *testing.T) {
	rg := "sdk-aca-del-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Create job
	p, err := client.BeginCreateOrUpdate(ctx, rg, "sdk-del-job", armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.JobProperties{
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout: to.Ptr[int32](1),
			},
			Template: &armappcontainers.JobTemplate{
				Containers: []*armappcontainers.Container{
					{Name: to.Ptr("w"), Image: to.Ptr("public.ecr.aws/docker/library/alpine:latest")},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = p.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Delete job
	delPoller, err := client.BeginDelete(ctx, rg, "sdk-del-job", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// GET should 404
	_, err = client.Get(ctx, rg, "sdk-del-job", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

// --- Error path tests ---

func TestSDK_ContainerApps_GetNonExistentJob(t *testing.T) {
	rg := "sdk-aca-err-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	_, err = client.Get(ctx, rg, "does-not-exist", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

func TestSDK_ContainerApps_StartNonExistentJob(t *testing.T) {
	rg := "sdk-aca-err-rg"
	ensureRG(t, rg)

	cred := &fakeCredential{}
	client, err := armappcontainers.NewJobsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	_, err = client.BeginStart(ctx, rg, "does-not-exist", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

func TestContainerApps_StopNonExistentExecution(t *testing.T) {
	rg := "sdk-aca-err-rg"
	ensureRG(t, rg)

	// Create a job first so the stop fails on the execution, not the job
	acaCreateJob(t, rg, "stop-err-job")

	req, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/stop-err-job/executions/nonexistent/stop?api-version=2024-03-01",
		strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	var errResp map[string]any
	data, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(data, &errResp))
	errObj := errResp["error"].(map[string]any)
	assert.Equal(t, "ResourceNotFound", errObj["code"])
}
