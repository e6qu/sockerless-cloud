package azure_cli_test

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerAppsCLI_JobExecutionRunsItsReplicas drives `az containerapp
// job show`, `job start` and `job execution show` against a manual job with a
// parallelism, a replica completion count and a retry limit: `job show` reads
// the settings back, and the execution succeeds once its replicas complete.
func TestContainerAppsCLI_JobExecutionRunsItsReplicas(t *testing.T) {
	t.Parallel()
	env := startAzTLSSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-aca-jobs",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-aca-jobs"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })

	rg, job := "aca-jobs-cli-rg", "aca-jobs-cli-job"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	rest := func(method, url, body string, extra ...string) *exec.Cmd {
		args := []string{"rest", "--method", method, "--url", url, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return env.command(append(args, extra...)...)
	}
	jobURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s?api-version=%s",
		env.baseURL, subscriptionID, rg, job, acaAPIVersion)
	azLongRunning(t, rest, "PUT", jobURL, `{
		"location": "eastus",
		"properties": {
			"configuration": {
				"triggerType": "Manual",
				"replicaTimeout": 60,
				"replicaRetryLimit": 1,
				"manualTriggerConfig": {"parallelism": 2, "replicaCompletionCount": 2}
			},
			"template": {
				"containers": [{"name": "worker", "image": "mirror.gcr.io/library/alpine:latest",
					"command": ["sh", "-c", "echo replica $HOSTNAME"]}]
			}
		}
	}`)
	t.Cleanup(func() { azLongRunning(t, rest, "DELETE", jobURL, "") })

	var shown struct {
		Properties struct {
			Configuration struct {
				ReplicaRetryLimit   int `json:"replicaRetryLimit"`
				ManualTriggerConfig struct {
					Parallelism            int `json:"parallelism"`
					ReplicaCompletionCount int `json:"replicaCompletionCount"`
				} `json:"manualTriggerConfig"`
			} `json:"configuration"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command("containerapp", "job", "show", "-g", rg, "-n", job, "-o", "json")), &shown)
	assert.Equal(t, 1, shown.Properties.Configuration.ReplicaRetryLimit)
	assert.Equal(t, 2, shown.Properties.Configuration.ManualTriggerConfig.Parallelism)
	assert.Equal(t, 2, shown.Properties.Configuration.ManualTriggerConfig.ReplicaCompletionCount)

	var started struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("containerapp", "job", "start", "-g", rg, "-n", job, "-o", "json")), &started)
	require.NotEmpty(t, started.Name)

	var execution struct {
		Properties struct {
			Status  string `json:"status"`
			EndTime string `json:"endTime"`
		} `json:"properties"`
	}
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, env.command("containerapp", "job", "execution", "show", "-g", rg, "-n", job,
			"--job-execution-name", started.Name, "-o", "json")), &execution)
		return execution.Properties.EndTime != ""
	}, 2*time.Minute, time.Second, "the execution never ended")
	assert.Equal(t, "Succeeded", execution.Properties.Status)
}
