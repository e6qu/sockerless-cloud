package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMonitorDiagnosticSettings_CLI drives Azure Monitor diagnostic settings
// on a Container Apps environment with the native az commands: the categories
// the environment offers, a setting created, shown, listed and deleted.
//
//	GET    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettingsCategories
//	PUT    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings/{name}
//	GET    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings/{name}
//	GET    /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings
//	DELETE /{resourceUri}/providers/Microsoft.Insights/diagnosticSettings/{name}
func TestMonitorDiagnosticSettings_CLI(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-diagnostic-settings")
	const rg = "cli-diag-rg"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))

	var ws struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("monitor", "log-analytics", "workspace", "create", "-g", rg, "-n", "cli-diag-ws", "-o", "json")), &ws)
	var cae struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("rest", "--method", "PUT", "--url", fmt.Sprintf(
		"%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/cli-diag-env?api-version=%s", env.baseURL, subscriptionID, rg, acaAPIVersion),
		"--body", `{"location":"eastus","properties":{"appLogsConfiguration":{"destination":"azure-monitor"}}}`, "-o", "json")), &cae)

	// az prints the categories collection whole, and flattens each
	// resource's properties into it.
	var categories struct {
		Value []struct {
			Name         string `json:"name"`
			CategoryType string `json:"categoryType"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("monitor", "diagnostic-settings", "categories", "list", "--resource", cae.ID, "-o", "json")), &categories)
	var names []string
	for _, c := range categories.Value {
		names = append(names, c.Name)
		assert.Equal(t, "Logs", c.CategoryType)
	}
	assert.Contains(t, names, "ContainerAppConsoleLogs")
	assert.Contains(t, names, "ContainerAppSystemLogs")

	var created struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		WorkspaceID string `json:"workspaceId"`
		Logs        []struct {
			Category string `json:"category"`
			Enabled  bool   `json:"enabled"`
		} `json:"logs"`
	}
	parseJSON(t, runCLI(t, env.command("monitor", "diagnostic-settings", "create", "-n", "cli-diag", "--resource", cae.ID,
		"--workspace", ws.ID, "--logs", `[{"category":"ContainerAppConsoleLogs","enabled":true},{"category":"ContainerAppSystemLogs","enabled":true}]`, "-o", "json")), &created)
	assert.Equal(t, cae.ID+"/providers/Microsoft.Insights/diagnosticSettings/cli-diag", created.ID)
	assert.Equal(t, ws.ID, created.WorkspaceID)
	require.Len(t, created.Logs, 2)

	_, stderr, err := runCLIStreamsResult(env.command("monitor", "diagnostic-settings", "create", "-n", "cli-diag-bad", "--resource", cae.ID,
		"--workspace", ws.ID, "--logs", `[{"category":"AppServiceConsoleLogs","enabled":true}]`, "-o", "json"))
	require.Error(t, err, "a category of another resource type is refused")
	assert.Contains(t, stderr, "AppServiceConsoleLogs")

	var shown struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("monitor", "diagnostic-settings", "show", "-n", "cli-diag", "--resource", cae.ID, "-o", "json")), &shown)
	assert.Equal(t, "cli-diag", shown.Name)
	var listed []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("monitor", "diagnostic-settings", "list", "--resource", cae.ID, "-o", "json")), &listed)
	require.Len(t, listed, 1)
	assert.Equal(t, "cli-diag", listed[0].Name)

	runCLI(t, env.command("monitor", "diagnostic-settings", "delete", "-n", "cli-diag", "--resource", cae.ID))
	parseJSON(t, runCLI(t, env.command("monitor", "diagnostic-settings", "list", "--resource", cae.ID, "-o", "json")), &listed)
	assert.Empty(t, listed)
}
