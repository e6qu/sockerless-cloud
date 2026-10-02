package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerAppsCLI_LogsShowFollowsTheReplica drives `az containerapp logs
// show --follow`, logged in to a TLS simulator running the workload runtime.
// The CLI asks the app's getAuthToken for a token, reads its
// eventStreamEndpoint, resolves the latest revision's first replica and its
// container, and follows that container's console until the container exits.
func TestContainerAppsCLI_LogsShowFollowsTheReplica(t *testing.T) {
	env := startAzTLSSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-aca-logs",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-aca-logs"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })

	rg, app := "aca-logs-cli-rg", "aca-logs-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	rest := func(method, url, body string, extra ...string) *exec.Cmd {
		args := []string{"rest", "--method", method, "--url", url, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return env.command(append(args, extra...)...)
	}
	appURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s?api-version=%s",
		env.baseURL, subscriptionID, rg, app, acaAPIVersion)
	azLongRunning(t, rest, "PUT", appURL, fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"template": {
				"containers": [{"name": "main", "image": %q, "args": ["6 * 7"]}],
				"scale": {"minReplicas": 1, "maxReplicas": 1}
			}
		}
	}`, evalImageName))
	t.Cleanup(func() { azLongRunning(t, rest, "DELETE", appURL, "") })

	console := runCLI(t, env.command("containerapp", "logs", "show", "-g", rg, "-n", app,
		"--follow", "--tail", "300", "--format", "json"))
	var logged []string
	for _, line := range strings.Split(strings.TrimSpace(console), "\n") {
		var entry struct {
			TimeStamp string `json:"TimeStamp"`
			Log       string `json:"Log"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), "a console log line is JSON: %s", line)
		logged = append(logged, entry.Log)
	}
	assert.Contains(t, logged, "42", "the replica's container evaluates (6 * 7); its console carried %q", logged)
	require.NotEmpty(t, logged)
	assert.Equal(t, "Connecting to the container 'main'...", logged[0])

	system := runCLI(t, env.command("containerapp", "logs", "show", "-g", rg, "-n", app, "--type", "system"))
	assert.Contains(t, system, `"Reason": "ContainerStarted"`)

	var revisions []struct {
		Name       string `json:"name"`
		Properties struct {
			Active   bool  `json:"active"`
			Replicas int32 `json:"replicas"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command("containerapp", "revision", "list", "-g", rg, "-n", app, "-o", "json")), &revisions)
	require.Len(t, revisions, 1)
	assert.True(t, revisions[0].Properties.Active)

	listReplicas := func() []string {
		var replicas []struct {
			Name string `json:"name"`
		}
		parseJSON(t, runCLI(t, env.command("containerapp", "replica", "list", "-g", rg, "-n", app,
			"--revision", revisions[0].Name, "-o", "json")), &replicas)
		names := make([]string, 0, len(replicas))
		for _, replica := range replicas {
			names = append(names, replica.Name)
		}
		return names
	}
	before := listReplicas()
	require.Len(t, before, 1)
	runCLI(t, env.command("containerapp", "revision", "restart", "-g", rg, "-n", app, "--revision", revisions[0].Name))
	after := listReplicas()
	require.Len(t, after, 1)
	assert.NotEqual(t, before[0], after[0], "a restarted revision runs a new replica")
}
