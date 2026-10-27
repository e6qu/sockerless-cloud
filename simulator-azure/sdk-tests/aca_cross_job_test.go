package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerApps_CrossJobDNS: two jobs of one managed environment resolve
// each other by job name.
func TestContainerApps_CrossJobDNS(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-job DNS test (no fallback): %v", err)
	}

	rg := "xjob-aca-rg"
	env := "xjob-aca-env"

	rgBody := `{"location":"eastus"}`
	doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=2023-07-01",
		baseURL, subscriptionID, rg), rgBody)

	envBody := `{"location":"eastus","properties":{"zoneRedundant":false}}`
	envEcho := doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s?api-version=2024-03-01",
		baseURL, subscriptionID, rg, env), envBody)
	// ManagedEnvironment has no member naming its backing network.
	require.NotContains(t, envEcho, "dockerNetworkName")
	defer doDELETE(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s?api-version=2024-03-01",
		baseURL, subscriptionID, rg, env))

	envID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s",
		subscriptionID, rg, env)

	// The jobs sleep so the test can exec into them.
	createJob := func(jobName string) {
		body := map[string]any{
			"location": "eastus",
			"properties": map[string]any{
				"environmentId": envID,
				"configuration": map[string]any{
					"triggerType":    "Manual",
					"replicaTimeout": 60,
				},
				"template": map[string]any{
					"containers": []map[string]any{{
						"name":    "worker",
						"image":   "mirror.gcr.io/library/alpine:latest",
						"command": []string{"sh", "-c"},
						"args":    []string{"sleep 30"},
					}},
				},
			},
		}
		raw, _ := json.Marshal(body)
		doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s?api-version=2024-03-01",
			baseURL, subscriptionID, rg, jobName), string(raw))
	}
	createJob("alpha")
	createJob("beta")

	startJob := func(jobName string) string {
		resp := doPOST(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s/start?api-version=2024-03-01",
			baseURL, subscriptionID, rg, jobName), "")
		_ = resp
		return jobName
	}
	startJob("alpha")
	startJob("beta")

	alphaContainer := waitForJobContainer(t, "alpha", envID)
	_ = waitForJobContainer(t, "beta", envID)

	var getent []byte
	require.Eventually(t, func() bool {
		var err error
		getent, err = exec.Command("docker", "exec", alphaContainer, "getent", "hosts", "beta").CombinedOutput()
		return err == nil && len(getent) > 0
	}, 30*time.Second, 500*time.Millisecond, "alpha should resolve 'beta' via ACA env DNS: %s", getent)
	assert.Contains(t, string(getent), "beta", "getent output should mention beta: %s", getent)
}

// waitForJobContainer returns the name of the container the simulator started
// for the job.
func waitForJobContainer(t *testing.T, jobName, envID string) string {
	t.Helper()
	var name string
	require.Eventually(t, func() bool {
		out, err := exec.Command("docker", "ps",
			"--filter", "label=sockerless-sim-type=aca-job-execution",
			"--format", "{{.Names}}\t{{.Label \"sockerless-exec-id\"}}",
		).Output()
		if err != nil {
			return false
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) < 2 {
				continue
			}
			// exec-id format: <envIDpath>/jobs/<jobName>/executions/<execName>
			if strings.Contains(parts[1], "/jobs/"+jobName+"/executions/") {
				name = parts[0]
				return true
			}
		}
		return false
	}, 30*time.Second, 300*time.Millisecond, "container for job %s should be running", jobName)
	return name
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

func doPOST(t *testing.T, url, body string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 400, "POST %s failed: %d %s", url, resp.StatusCode, string(data))
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
