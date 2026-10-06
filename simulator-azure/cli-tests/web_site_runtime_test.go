package azure_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for what an App Service app runs — its connection strings in
// the environment, its persistent /home share, the stopped state — and for
// swap with preview, as the Azure CLI drives them:
//
//	az webapp config connection-string set (--settings, --slot-settings)
//	az webapp restart
//	az webapp stop / az webapp start
//	az webapp deployment slot swap --action preview
//	  POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/applySlotConfig
//	az webapp deployment slot swap --action reset
//	  POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/resetSlotConfig
//	az webapp deployment slot swap --action swap

func TestWebAppRuntime_ConnectionStringsHomeStopAndSwapPreview(t *testing.T) {
	az := kuduCLILogin(t)
	rg, plan, app, slot := "runtime-cli-rg", "runtime-cli-plan", "runtime-cli-app", "staging"
	runCLI(t, az.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, az.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "S1", "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=2025-03-01",
		az.baseURL, subscriptionID, rg, app)
	runCLI(t, az.command("rest", "--method", "PUT", "--url", siteURL, "--body", fmt.Sprintf(`{
		"location": "eastus",
		"kind": "app,linux,container",
		"properties": {
			"serverFarmId": "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
			"reserved": true,
			"siteConfig": {
				"linuxFxVersion": "DOCKER|%s",
				"appCommandLine": "files-http 80 /home/site/wwwroot",
				"appSettings": [{"name": "WEBSITES_ENABLE_APP_SERVICE_STORAGE", "value": "true"}]
			}
		}
	}`, subscriptionID, rg, plan, commandImageName), "-o", "json"))
	t.Cleanup(func() { runCLI(t, az.command("webapp", "delete", "-g", rg, "-n", app)) })

	site := func(host, method, path string, extra ...string) *exec.Cmd {
		args := append([]string{"rest", "--method", method, "--url", az.baseURL + path,
			"--headers", "Host=" + host + ".azurewebsites.net", "--skip-authorization-header"}, extra...)
		return az.command(args...)
	}
	get := func(host, path string) string {
		return strings.TrimSpace(runCLI(t, site(host, "GET", path)))
	}

	runCLI(t, az.command("webapp", "config", "connection-string", "set", "-g", rg, "-n", app,
		"-t", "SQLAzure", "--settings", "Orders=Server=tcp:orders.database.windows.net", "-o", "json"))
	runCLI(t, az.command("webapp", "config", "connection-string", "set", "-g", rg, "-n", app,
		"-t", "PostgreSQL", "--settings", "Events=host=events.postgres", "-o", "json"))
	assert.Equal(t, "Server=tcp:orders.database.windows.net", get(app, "/env/SQLAZURECONNSTR_Orders"))
	assert.Equal(t, "host=events.postgres", get(app, "/env/POSTGRESQLCONNSTR_Events"))

	// A file the app writes to /home survives a restart, and Kudu reads it.
	runCLI(t, site(app, "PUT", "/files/notes/kept.txt", "--body", "written by the app"))
	runCLI(t, az.command("webapp", "restart", "-g", rg, "-n", app))
	assert.Equal(t, "written by the app", get(app, "/files/notes/kept.txt"))
	scm := "https://" + kuduCLIScmHost(t, az, rg, app)
	assert.Equal(t, "written by the app", strings.TrimSpace(runCLI(t, az.command("rest", "--method", "get",
		"--url", scm+"/api/vfs/site/wwwroot/notes/kept.txt", "--resource", "https://appservice.azure.com"))))

	// A stopped app answers the stopped-site page and runs nothing.
	runCLI(t, az.command("webapp", "stop", "-g", rg, "-n", app))
	var shown struct {
		State string `json:"state"`
	}
	parseJSON(t, runCLI(t, az.command("webapp", "show", "-g", rg, "-n", app, "-o", "json")), &shown)
	assert.Equal(t, "Stopped", shown.State)
	_, stderr, err := runCLIStreamsResult(site(app, "GET", "/"))
	require.Error(t, err, "a stopped app refuses the request")
	assert.Contains(t, stderr, "This web app is stopped")
	runCLI(t, az.command("webapp", "start", "-g", rg, "-n", app))
	assert.Equal(t, "files-http", get(app, "/"))

	// Swap with preview: the slot runs on production's slot settings until
	// the reset, and the swap completes the preview.
	runCLI(t, az.command("webapp", "deployment", "slot", "create", "-g", rg, "-n", app, "-s", slot,
		"--configuration-source", app, "-o", "json"))
	runCLI(t, az.command("webapp", "config", "appsettings", "set", "-g", rg, "-n", app, "-s", slot,
		"--slot-settings", "STICKY=staging", "-o", "json"))
	runCLI(t, az.command("webapp", "config", "appsettings", "set", "-g", rg, "-n", app,
		"--slot-settings", "STICKY=production", "-o", "json"))
	runCLI(t, az.command("webapp", "config", "connection-string", "set", "-g", rg, "-n", app, "-s", slot,
		"-t", "Custom", "--slot-settings", "Db=staging-db", "-o", "json"))
	runCLI(t, az.command("webapp", "config", "connection-string", "set", "-g", rg, "-n", app,
		"-t", "Custom", "--slot-settings", "Db=production-db", "-o", "json"))
	slotHost := app + "-" + slot
	runCLI(t, site(slotHost, "PUT", "/files/slot-marker.txt", "--body", "staging content"))
	assert.Equal(t, "staging", get(slotHost, "/env/STICKY"))

	runCLI(t, az.command("webapp", "deployment", "slot", "swap", "-g", rg, "-n", app, "-s", slot, "--action", "preview"))
	assert.Equal(t, "production", get(slotHost, "/env/STICKY"))
	assert.Equal(t, "production-db", get(slotHost, "/env/CUSTOMCONNSTR_Db"))
	runCLI(t, az.command("webapp", "deployment", "slot", "swap", "-g", rg, "-n", app, "-s", slot, "--action", "reset"))
	assert.Equal(t, "staging", get(slotHost, "/env/STICKY"))
	assert.Equal(t, "staging-db", get(slotHost, "/env/CUSTOMCONNSTR_Db"))

	runCLI(t, az.command("webapp", "deployment", "slot", "swap", "-g", rg, "-n", app, "-s", slot, "--action", "preview"))
	runCLI(t, az.command("webapp", "deployment", "slot", "swap", "-g", rg, "-n", app, "-s", slot, "--action", "swap"))
	assert.Equal(t, "staging content", get(app, "/files/slot-marker.txt"), "production serves the slot's content")
	assert.Equal(t, "production", get(app, "/env/STICKY"))
	assert.Equal(t, "production-db", get(app, "/env/CUSTOMCONNSTR_Db"))
	assert.Equal(t, "staging", get(slotHost, "/env/STICKY"))
	assert.Equal(t, "staging-db", get(slotHost, "/env/CUSTOMCONNSTR_Db"))
}
