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
	rg := "int-aca-rg"
	jobName := "int-aca-job"

	// 1. Create resource group
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(`{"location":"eastus"}`))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	// 2. Create job
	acaCreateJob(t, rg, jobName)

	// 3. Start execution
	execName := acaStartExecution(t, rg, jobName)

	// 4. Verify Running state immediately
	exec := acaGetExecution(t, rg, jobName, execName)
	execProps := exec["properties"].(map[string]any)
	assert.Equal(t, "Running", execProps["status"])

	// 5. Query ContainerAppConsoleLogs_CL for start entry
	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "` + jobName + `"`
	result := queryWorkspace(t, "default", kql)
	require.Len(t, result.Tables, 1)
	require.GreaterOrEqual(t, len(result.Tables[0].Rows), 1, "should have start log entry")

	// 6. Stop execution
	stopReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"/executions/"+execName+"/stop?api-version=2024-03-01",
		strings.NewReader("{}"))
	stopReq.Header.Set("Content-Type", "application/json")
	stopReq.Header.Set("Authorization", simARMBearer)
	stopResp, err := http.DefaultClient.Do(stopReq)
	require.NoError(t, err)
	stopResp.Body.Close()
	require.Equal(t, http.StatusOK, stopResp.StatusCode)

	// 7. Verify Stopped state
	exec = acaGetExecution(t, rg, jobName, execName)
	execProps = exec["properties"].(map[string]any)
	assert.Equal(t, "Stopped", execProps["status"])
	assert.NotEmpty(t, execProps["endTime"])

	// 8. Delete job — ARM DELETE is a 202 long-running operation with the
	// LRO headers; the job stays observable in provisioningState=Deleting
	// until the operation completes.
	delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		nil)
	delReq.Header.Set("Authorization", simARMBearer)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	require.Equal(t, http.StatusAccepted, delResp.StatusCode)
	require.NotEmpty(t, delResp.Header.Get("Azure-AsyncOperation"))

	// 9. Verify 404 once the delete operation completes — poll like a real
	// ARM client.
	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		nil)
	getReq.Header.Set("Authorization", simARMBearer)
	deadline := time.Now().Add(10 * time.Second)
	status := 0
	for time.Now().Before(deadline) {
		getResp, err := http.DefaultClient.Do(getReq)
		require.NoError(t, err)
		getResp.Body.Close()
		status = getResp.StatusCode
		if status == http.StatusNotFound {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	assert.Equal(t, http.StatusNotFound, status)
}

func TestIntegration_AzureFunctionsLifecycle(t *testing.T) {
	rg := "int-func-rg"
	siteName := "int-func-app"

	// 1. Create resource group
	rgReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(`{"location":"eastus"}`))
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", simARMBearer)
	rgResp, err := http.DefaultClient.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()

	// 2. Create function app
	site := map[string]any{
		"location": "eastus",
		"kind":     "functionapp",
		"properties": map[string]any{
			"serverFarmId": "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/plan",
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

	// 3. Verify DefaultHostName
	var siteResult map[string]any
	data, _ := io.ReadAll(siteResp.Body)
	require.NoError(t, json.Unmarshal(data, &siteResult))
	props := siteResult["properties"].(map[string]any)
	defaultHostName := props["defaultHostName"].(string)
	require.NotEmpty(t, defaultHostName)

	// 4. Invoke via DefaultHostName (matches real Azure routing). Sim hosts
	// every site on one port so connect to baseURL but pin Host header.
	invokeReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/api/function", strings.NewReader("{}"))
	invokeReq.Header.Set("Content-Type", "application/json")
	invokeReq.Host = defaultHostName
	invokeResp, err := http.DefaultClient.Do(invokeReq)
	require.NoError(t, err)
	invokeResp.Body.Close()
	require.Equal(t, http.StatusOK, invokeResp.StatusCode)

	// 5. Query AppTraces for the function we just invoked. Poll until the trace
	// is ingested rather than racing on a fixed sleep that a loaded runner could
	// exceed.
	kql := `AppTraces | where AppRoleName == "` + siteName + `"`
	require.Eventually(t, func() bool {
		result := queryWorkspace(t, "default", kql)
		return len(result.Tables) == 1 && len(result.Tables[0].Rows) >= 1
	}, 30*time.Second, 200*time.Millisecond, "should have at least one Function invoked AppTraces entry")

	// 6. Delete function app
	delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/"+siteName+"?api-version=2023-12-01",
		nil)
	delReq.Header.Set("Authorization", simARMBearer)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode)

	// 7. Verify 404 after delete
	getReq, _ := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/"+siteName+"?api-version=2023-12-01",
		nil)
	getReq.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}
