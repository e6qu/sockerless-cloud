package azure_sdk_test

import (
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

// A job's name resolves inside its environment: once beta's replica has
// started, alpha's container looks beta up and prints the answer, which the
// environment's console logs carry.
func TestContainerApps_CrossJobDNS(t *testing.T) {
	t.Parallel()
	const rg = "aca-logs-rg"
	envID, customerID := acaLogsEnvironment(t)
	jobs, err := armappcontainers.NewJobsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)

	run := func(name, script string) string {
		t.Helper()
		create, err := jobs.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.Job{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.JobProperties{
				EnvironmentID: to.Ptr(envID),
				Configuration: &armappcontainers.JobConfiguration{
					TriggerType:    to.Ptr(armappcontainers.TriggerTypeManual),
					ReplicaTimeout: to.Ptr[int32](120),
					ManualTriggerConfig: &armappcontainers.JobConfigurationManualTriggerConfig{
						Parallelism: to.Ptr[int32](1), ReplicaCompletionCount: to.Ptr[int32](1),
					},
				},
				Template: &armappcontainers.JobTemplate{Containers: []*armappcontainers.Container{{
					Name:    to.Ptr("worker"),
					Image:   to.Ptr("mirror.gcr.io/library/alpine:latest"),
					Command: []*string{to.Ptr("sh"), to.Ptr("-c")},
					Args:    []*string{to.Ptr(script)},
				}}},
			},
		}, nil)
		require.NoError(t, err)
		_, err = create.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		t.Cleanup(func() {
			if poller, err := jobs.BeginDelete(ctx, rg, name, nil); err == nil {
				_, _ = poller.PollUntilDone(ctx, nil)
			}
		})
		start, err := jobs.BeginStart(ctx, rg, name, nil)
		require.NoError(t, err)
		started, err := start.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, started.Name)
		return *started.Name
	}
	query := func(kql string) bool {
		result := queryWorkspace(t, customerID, kql)
		return len(result.Tables) == 1 && len(result.Tables[0].Rows) > 0
	}

	betaExecution := run("xjob-beta", "sleep 60")
	require.Eventually(t, func() bool {
		return query(`ContainerAppSystemLogs_CL | where JobName_s == "xjob-beta" and ExecutionName_s == "` +
			betaExecution + `" and Log_s startswith "Started container"`)
	}, 60*time.Second, 200*time.Millisecond, "beta's replica never started")

	alphaExecution := run("xjob-alpha", "getent hosts xjob-beta")
	exec := acaWaitExecution(t, rg, "xjob-alpha", alphaExecution)
	assert.Equal(t, "Succeeded", exec["properties"].(map[string]any)["status"], "alpha resolved xjob-beta")
	require.Eventually(t, func() bool {
		return query(`ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "xjob-alpha" and Log_s contains "xjob-beta"`)
	}, 60*time.Second, 200*time.Millisecond, "alpha's lookup of xjob-beta is in its console log")
}

func doPUT(t *testing.T, url, body string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "PUT", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 400, "PUT %s failed: %d %s", url, resp.StatusCode, string(data))
	return string(data)
}

func doDELETE(t *testing.T, url string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	req.Header.Set("Authorization", simARMBearer)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}
