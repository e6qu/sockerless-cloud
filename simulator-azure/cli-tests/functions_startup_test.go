package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFunctionApp_CLI_StartupFileRunsTheContainer drives a Linux custom
// container function app with the native az commands: `az functionapp config
// set --startup-file` writes siteConfig.appCommandLine, `az functionapp config
// appsettings set` writes WEBSITES_PORT, and a request to the site's hostname
// reaches the container the image's entrypoint runs with that command, on that
// port.
func TestFunctionApp_CLI_StartupFileRunsTheContainer(t *testing.T) {
	// The full workload runtime: the site runs a real container.
	env := startAzTLSSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-startup",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-startup"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	rg, app := "startup-cli-rg", "startup-cli-func"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	arm := func(resourcePath string) string {
		return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/%s?api-version=%s",
			env.baseURL, subscriptionID, rg, resourcePath, stage4CLIAPIVersion)
	}
	planID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/startup-cli-plan", subscriptionID, rg)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", arm("Microsoft.Web/serverfarms/startup-cli-plan"),
		"--body", `{"location":"eastus","kind":"linux","sku":{"name":"B1","tier":"Basic"},"properties":{"reserved":true}}`, "-o", "json"))
	runCLI(t, env.command("rest", "--method", "PUT", "--url", arm("Microsoft.Web/sites/"+app), "--body", fmt.Sprintf(
		`{"location":"eastus","kind":"functionapp,linux,container","properties":{"serverFarmId":%q,"reserved":true,"siteConfig":{"linuxFxVersion":"DOCKER|%s"}}}`,
		planID, commandImageName), "-o", "json"))

	runCLI(t, env.command("functionapp", "config", "set", "-g", rg, "-n", app,
		"--startup-file", "serve 8080 from-the-cli-startup-file", "-o", "json"))
	runCLI(t, env.command("functionapp", "config", "appsettings", "set", "-g", rg, "-n", app,
		"--settings", "WEBSITES_PORT=8080", "-o", "json"))

	var shown struct {
		AppCommandLine string `json:"appCommandLine"`
		LinuxFxVersion string `json:"linuxFxVersion"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "config", "show", "-g", rg, "-n", app, "-o", "json")), &shown)
	assert.Equal(t, "serve 8080 from-the-cli-startup-file", shown.AppCommandLine)
	assert.Equal(t, "DOCKER|"+commandImageName, shown.LinuxFxVersion)

	out := runCLI(t, env.command("rest", "--method", "POST", "--url", env.baseURL+"/api/function",
		"--body", "{}", "--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header"))
	require.Equal(t, "from-the-cli-startup-file", strings.TrimSpace(out))

	runCLI(t, env.command("rest", "--method", "DELETE", "--url", arm("Microsoft.Web/sites/"+app)))
}
