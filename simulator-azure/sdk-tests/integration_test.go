package azure_sdk_test

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

func TestIntegration_ACAJobLifecycle(t *testing.T) {
	t.Parallel()
	rg := "int-aca-rg"
	jobName := "int-aca-job"

	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(`{"location":"eastus"}`))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	acaCreateJob(t, rg, jobName)

	execName := acaStartExecution(t, rg, jobName)

	exec := acaGetExecution(t, rg, jobName, execName)
	execProps := exec["properties"].(map[string]any)
	assert.Equal(t, "Running", execProps["status"])

	// The platform's start event lands in the environment workspace's
	// ContainerAppSystemLogs_CL.
	kql := `ContainerAppSystemLogs_CL | where JobName_s == "` + jobName + `" and Log_s == "Container started"`
	result := queryWorkspace(t, acaLogsCustomerID(t), kql)
	require.Len(t, result.Tables, 1)
	require.GreaterOrEqual(t, len(result.Tables[0].Rows), 1, "should have start log entry")

	stopReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"/executions/"+execName+"/stop?api-version=2024-03-01",
		strings.NewReader("{}"))
	stopReq.Header.Set("Content-Type", "application/json")
	stopReq.Header.Set("Authorization", simARMBearer)
	stopResp, err := http.DefaultClient.Do(stopReq)
	require.NoError(t, err)
	stopResp.Body.Close()
	require.Equal(t, http.StatusOK, stopResp.StatusCode)

	exec = acaGetExecution(t, rg, jobName, execName)
	execProps = exec["properties"].(map[string]any)
	assert.Equal(t, "Stopped", execProps["status"])
	assert.NotEmpty(t, execProps["endTime"])

	// ARM DELETE is a 202 long-running operation; the job reads
	// provisioningState=Deleting until it completes.
	delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		nil)
	delReq.Header.Set("Authorization", simARMBearer)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, delResp.StatusCode)
	require.NotEmpty(t, delResp.Header.Get("Azure-AsyncOperation"))
	awaitARMOperation[struct{}](t, delResp)
	delResp.Body.Close()

	// The job is 404 once the delete completes.
	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		nil)
	getReq.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}

func TestIntegration_AzureFunctionsLifecycle(t *testing.T) {
	t.Parallel()
	rg := "int-func-rg"
	siteName := "int-func-app"

	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(`{"location":"eastus"}`))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	site := map[string]any{
		"location": "eastus",
		"kind":     "functionapp,linux,container",
		"properties": map[string]any{
			"serverFarmId": "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/plan",
			"siteConfig": map[string]any{
				"linuxFxVersion": "DOCKER|" + commandImageName,
				"appCommandLine": "serve 80 integration-invoked",
			},
		},
	}
	siteBody, _ := json.Marshal(site)
	siteReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/"+siteName+"?api-version=2023-12-01",
		strings.NewReader(string(siteBody)))
	siteReq.Header.Set("Content-Type", "application/json")
	siteReq.Header.Set("Authorization", simARMBearer)
	siteResp, err := http.DefaultClient.Do(siteReq)
	require.NoError(t, err)
	defer siteResp.Body.Close()
	require.Equal(t, http.StatusOK, siteResp.StatusCode)

	var siteResult map[string]any
	data, _ := io.ReadAll(siteResp.Body)
	require.NoError(t, json.Unmarshal(data, &siteResult))
	props := siteResult["properties"].(map[string]any)
	defaultHostName := props["defaultHostName"].(string)
	require.NotEmpty(t, defaultHostName)

	// Azure routes invoke by the site's DefaultHostName, so dial the simulator
	// with that Host.
	invokeReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/api/function", strings.NewReader("{}"))
	invokeReq.Header.Set("Content-Type", "application/json")
	invokeReq.Host = defaultHostName
	invokeResp, err := http.DefaultClient.Do(invokeReq)
	require.NoError(t, err)
	invokeBody, err := io.ReadAll(invokeResp.Body)
	invokeResp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, invokeResp.StatusCode)
	require.Equal(t, "integration-invoked", string(invokeBody))

	// The container's access-log line reaches the site's container log after the
	// response, and App Service offers no event for its arrival.
	require.Eventually(t, func() bool {
		return strings.Contains(siteContainerLog(t, rg, siteName), "POST /api/function")
	}, 30*time.Second, 200*time.Millisecond, "the container's access-log line should reach the site's log")

	delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/"+siteName+"?api-version=2023-12-01",
		nil)
	delReq.Header.Set("Authorization", simARMBearer)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode)

	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/"+siteName+"?api-version=2023-12-01",
		nil)
	getReq.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}
