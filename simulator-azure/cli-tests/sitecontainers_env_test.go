package azure_cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSiteContainers_CLI_EnvironmentVariablesNameAppSettings creates a
// sitecontainer with `az webapp sitecontainers create --sitecontainers-spec-file`
// whose environmentVariables name an app setting: the container gets the
// setting's value under the variable's name, and an empty string once
// `az webapp config appsettings delete` removes the setting.
func TestSiteContainers_CLI_EnvironmentVariablesNameAppSettings(t *testing.T) {
	az := kuduCLILogin(t)
	rg, plan, app := "sc-env-cli-rg", "sc-env-cli-plan", "sc-env-cli-app"
	runCLI(t, az.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, az.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "S1", "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=2025-03-01",
		az.baseURL, subscriptionID, rg, app)
	runCLI(t, az.command("rest", "--method", "PUT", "--url", siteURL, "--body", fmt.Sprintf(`{
		"location": "eastus",
		"kind": "app,linux,sitecontainers",
		"properties": {
			"serverFarmId": "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
			"reserved": true,
			"siteConfig": {
				"linuxFxVersion": "SITECONTAINERS",
				"appSettings": [{"name": "GREETING_SETTING", "value": "hello from an app setting"}]
			}
		}
	}`, subscriptionID, rg, plan), "-o", "json"))
	t.Cleanup(func() { runCLI(t, az.command("webapp", "delete", "-g", rg, "-n", app)) })

	spec := filepath.Join(t.TempDir(), "sitecontainers.json")
	require.NoError(t, os.WriteFile(spec, fmt.Appendf(nil, `[{
		"name": "main",
		"properties": {
			"image": %q,
			"targetPort": "80",
			"isMain": true,
			"startUpCommand": "files-http 80 /home/site/wwwroot",
			"environmentVariables": [{"name": "GREETING", "value": "GREETING_SETTING"}]
		}
	}]`, commandImageName), 0o600))
	runCLI(t, az.command("webapp", "sitecontainers", "create", "-g", rg, "-n", app,
		"--sitecontainers-spec-file", spec, "-o", "json"))
	shown := runCLI(t, az.command("webapp", "sitecontainers", "show", "-g", rg, "-n", app,
		"--container-name", "main", "-o", "json"))
	assert.Contains(t, shown, `"GREETING_SETTING"`, "the resource keeps the setting's name")

	get := func(path string) string {
		return strings.TrimSpace(runCLI(t, az.command("rest", "--method", "GET", "--url", az.baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header")))
	}
	assert.Equal(t, "hello from an app setting", get("/env/GREETING"))

	runCLI(t, az.command("webapp", "config", "appsettings", "delete", "-g", rg, "-n", app,
		"--setting-names", "GREETING_SETTING", "-o", "json"))
	assert.Equal(t, "", get("/env/GREETING"), "a setting that does not exist is an empty string")
}
