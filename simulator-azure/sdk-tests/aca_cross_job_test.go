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

// TestContainerApps_CrossJobDNS exercises cross-job DNS on Azure:
// ACA environments back themselves with a real Docker user-defined
// network; jobs in the same environment are connected to that network
// at execution-start time with the job short name as DNS alias. Two
// jobs in the same env resolve each other via Docker's embedded DNS.
func TestContainerApps_CrossJobDNS(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-job DNS test (no fallback): %v", err)
	}

	rg := "xjob-aca-rg"
	env := "xjob-aca-env"

	// 1. Resource group.
	rgBody := `{"location":"eastus"}`
	doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=2023-07-01",
		baseURL, subscriptionID, rg), rgBody)

	// 2. Managed environment — simulator auto-backs it with a Docker network.
	envBody := `{"location":"eastus","properties":{"zoneRedundant":false}}`
	envEcho := doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s?api-version=2024-03-01",
		baseURL, subscriptionID, rg, env), envBody)
	// The Docker network that backs the env is sim-internal wiring; it
	// must never leak onto the ARM wire (ManagedEnvironment has no such
	// member).
	require.NotContains(t, envEcho, "dockerNetworkName")
	defer doDELETE(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s?api-version=2024-03-01",
		baseURL, subscriptionID, rg, env))

	envID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s",
		subscriptionID, rg, env)

	// 3. Two jobs in that env running sleep 30 so we can exec into them.
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
						"image":   "public.ecr.aws/docker/library/alpine:latest",
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

	// 4. Start both executions.
	startJob := func(jobName string) string {
		resp := doPOST(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s/start?api-version=2024-03-01",
			baseURL, subscriptionID, rg, jobName), "")
		// Start returns 202 with Location header pointing at the LRO
		// poller. For our test we derive the exec name from the
		// returned execution body if present, else poll the job's
		// executions endpoint. Simpler: list executions and pick the
		// latest one matching the job.
		_ = resp
		return jobName
	}
	startJob("alpha")
	startJob("beta")

	// 5. Wait for containers to exist. Azure exec container name
	// convention: sockerless-sim-azure-execution-<executionName>.
	// Easier path: filter docker ps by label sockerless-sim-type=
	// aca-job-execution and the sockerless-sim-job label (we added
	// none, but the jobs are distinguishable by their entry in the
	// `containerName` prefix we just logged). For the test, we grep
	// the simulator's container output.
	alphaContainer := waitForJobContainer(t, "alpha", envID)
	_ = waitForJobContainer(t, "beta", envID) // ensure beta container is up before the exec below

	// 6. Cross-job DNS: alpha resolves beta via Docker's embedded DNS
	// on the env's network (alias = job short name).
	var getent []byte
	require.Eventually(t, func() bool {
		var err error
		getent, err = exec.Command("docker", "exec", alphaContainer, "getent", "hosts", "beta").CombinedOutput()
		return err == nil && len(getent) > 0
	}, 30*time.Second, 500*time.Millisecond, "alpha should resolve 'beta' via ACA env DNS: %s", getent)
	assert.Contains(t, string(getent), "beta", "getent output should mention beta: %s", getent)
}

// waitForJobContainer polls docker ps until the simulator has started
// a container for the job. Returns the Docker container name.
func waitForJobContainer(t *testing.T, jobName, envID string) string {
	t.Helper()
	var name string
	require.Eventually(t, func() bool {
		// docker ps --filter label=sockerless-sim-type=aca-job-execution
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
