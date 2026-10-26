package azure_cli_test

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

// TestContainerApps_CrossJobDNS_CLI checks that a job resolves another job of
// the same managed environment by name, driven through `az rest`.
func TestContainerApps_CrossJobDNS_CLI(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-job DNS test (no fallback): %v", err)
	}

	rg := "cli-xjob-rg"
	env := "cli-xjob-env"

	armPUT(t, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s?api-version=2023-07-01",
		subscriptionID, rg), `{"location":"eastus"}`)

	envID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s",
		subscriptionID, rg, env)
	armPUT(t, envID+"?api-version=2024-03-01",
		`{"location":"eastus","properties":{"zoneRedundant":false}}`)
	defer armDELETE(t, envID+"?api-version=2024-03-01")

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
		armPUT(t, fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s?api-version=2024-03-01",
			subscriptionID, rg, jobName), string(raw))
	}
	createJob("alpha")
	createJob("beta")

	startJob := func(jobName string) {
		armPOST(t, fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s/start?api-version=2024-03-01",
			subscriptionID, rg, jobName), "")
	}
	startJob("alpha")
	startJob("beta")

	alphaContainer := waitForJobContainer(t, "alpha")
	_ = waitForJobContainer(t, "beta")

	var getent []byte
	require.Eventually(t, func() bool {
		var err error
		getent, err = exec.Command("docker", "exec", alphaContainer, "getent", "hosts", "beta").CombinedOutput()
		return err == nil && len(getent) > 0
	}, 30*time.Second, 500*time.Millisecond, "alpha should resolve 'beta' via ACA env DNS: %s", getent)
	assert.Contains(t, string(getent), "beta")
}

func waitForJobContainer(t *testing.T, jobName string) string {
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
			if strings.Contains(parts[1], "/jobs/"+jobName+"/executions/") {
				name = parts[0]
				return true
			}
		}
		return false
	}, 30*time.Second, 300*time.Millisecond, "container for job %s should be running", jobName)
	return name
}

func armPUT(t *testing.T, path, body string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", baseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+armBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 400, "PUT %s: %d %s", path, resp.StatusCode, string(data))
}

func armPOST(t *testing.T, path, body string) {
	t.Helper()
	req, _ := http.NewRequest("POST", baseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+armBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 400, "POST %s: %d %s", path, resp.StatusCode, string(data))
}

func armDELETE(t *testing.T, path string) {
	t.Helper()
	req, _ := http.NewRequest("DELETE", baseURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+armBearer)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}
