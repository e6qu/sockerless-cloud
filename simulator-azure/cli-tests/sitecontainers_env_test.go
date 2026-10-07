package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// TestSiteContainers_CLI_InheritAppSettingsAndConnectionStrings creates a main
// and a sidecar with `az webapp sitecontainers create --sitecontainers-spec-file`,
// which leaves inheritAppSettingsAndConnectionStrings unset, so both inherit
// every app setting and connection string; a sidecar PUT with the flag false
// gets neither. The main relays requests to each sidecar over the shared
// loopback.
func TestSiteContainers_CLI_InheritAppSettingsAndConnectionStrings(t *testing.T) {
	az := kuduCLILogin(t)
	rg, plan, app := "sc-inherit-cli-rg", "sc-inherit-cli-plan", "sc-inherit-cli-app"
	runCLI(t, az.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, az.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "S1", "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s",
		az.baseURL, subscriptionID, rg, app)
	runCLI(t, az.command("rest", "--method", "PUT", "--url", siteURL+"?api-version=2025-03-01", "--body", fmt.Sprintf(`{
		"location": "eastus",
		"kind": "app,linux,sitecontainers",
		"properties": {
			"serverFarmId": "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
			"reserved": true,
			"siteConfig": {
				"linuxFxVersion": "SITECONTAINERS",
				"appSettings": [{"name": "GREETING_SETTING", "value": "hello from an app setting"}],
				"connectionStrings": [{"name": "Orders", "connectionString": "Server=orders", "type": "Custom"}]
			}
		}
	}`, subscriptionID, rg, plan), "-o", "json"))
	t.Cleanup(func() { runCLI(t, az.command("webapp", "delete", "-g", rg, "-n", app)) })

	spec := filepath.Join(t.TempDir(), "sitecontainers.json")
	require.NoError(t, os.WriteFile(spec, fmt.Appendf(nil, `[
		{"name": "main", "properties": {"image": %q, "targetPort": "8080", "isMain": true, "startUpCommand": "relay-local"}},
		{"name": "inherits", "properties": {"image": %q, "targetPort": "9090", "isMain": false, "startUpCommand": "files-http 9090 /tmp"}}
	]`, httpProbeImageName, commandImageName), 0o600))
	runCLI(t, az.command("webapp", "sitecontainers", "create", "-g", rg, "-n", app,
		"--sitecontainers-spec-file", spec, "-o", "json"))
	runCLI(t, az.command("rest", "--method", "PUT", "--url", siteURL+"/sitecontainers/isolated?api-version=2025-03-01",
		"--body", fmt.Sprintf(`{"properties": {"image": %q, "targetPort": "9091", "isMain": false,
			"startUpCommand": "files-http 9091 /tmp", "inheritAppSettingsAndConnectionStrings": false}}`, commandImageName),
		"-o", "json"))
	var shown struct {
		Inherit *bool `json:"inheritAppSettingsAndConnectionStrings"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, az.command("webapp", "sitecontainers", "show", "-g", rg, "-n", app,
		"--container-name", "inherits", "-o", "json"))), &shown))
	require.NotNil(t, shown.Inherit)
	assert.True(t, *shown.Inherit, "inheritAppSettingsAndConnectionStrings defaults to true")

	get := func(path string) *exec.Cmd {
		return az.command("rest", "--method", "GET", "--url", az.baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header")
	}
	assert.Equal(t, "hello from an app setting", strings.TrimSpace(runCLI(t, get("/9090/env/GREETING_SETTING"))))
	assert.Equal(t, "Server=orders", strings.TrimSpace(runCLI(t, get("/9090/env/CUSTOMCONNSTR_Orders"))))
	assert.Contains(t, runCLIExpectFailure(t, get("/9091/env/GREETING_SETTING")), "Not Found",
		"a sidecar that does not inherit gets no app setting")
}
