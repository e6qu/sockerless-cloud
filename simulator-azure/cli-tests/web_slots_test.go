package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for App Service deployment slots running their own instances,
// and for the swap az webapp deployment slot swap sends:
//
//	az webapp deployment slot create --configuration-source
//	az webapp config set --slot --startup-file
//	az webapp config appsettings set --slot --settings / --slot-settings
//	az webapp deployment slot swap
//	  POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/slots/{slot}/slotsswap
//
// Each slot answers on its own hostname with its own container; the swap
// moves what each hostname serves and leaves the sticky settings in place.

func TestWebAppSlots_NativeAzSlotSwap(t *testing.T) {
	az := kuduCLILogin(t)
	rg, plan, app, slot := "slots-cli-rg", "slots-cli-plan", "slots-cli-app", "staging"
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
				"appCommandLine": "http 80 production-content",
				"appSettings": [{"name": "SWAPPED", "value": "from-production"}]
			}
		}
	}`, subscriptionID, rg, plan, commandImageName), "-o", "json"))
	t.Cleanup(func() { runCLI(t, az.command("webapp", "delete", "-g", rg, "-n", app)) })

	served := func(host string) string {
		return strings.TrimSpace(runCLI(t, az.command("rest", "--method", "GET", "--url", az.baseURL+"/",
			"--headers", "Host="+host+".azurewebsites.net", "--skip-authorization-header")))
	}

	runCLI(t, az.command("webapp", "deployment", "slot", "create", "-g", rg, "-n", app, "-s", slot,
		"--configuration-source", app, "-o", "json"))
	runCLI(t, az.command("webapp", "config", "set", "-g", rg, "-n", app, "-s", slot,
		"--startup-file", "http 80 staging-content", "-o", "json"))
	runCLI(t, az.command("webapp", "config", "appsettings", "set", "-g", rg, "-n", app, "-s", slot,
		"--settings", "SWAPPED=from-staging", "-o", "json"))
	runCLI(t, az.command("webapp", "config", "appsettings", "set", "-g", rg, "-n", app, "-s", slot,
		"--slot-settings", "STICKY=staging", "-o", "json"))
	runCLI(t, az.command("webapp", "config", "appsettings", "set", "-g", rg, "-n", app,
		"--slot-settings", "STICKY=production", "-o", "json"))

	assert.Equal(t, "production-content", served(app))
	assert.Equal(t, "staging-content", served(app+"-"+slot), "the slot's hostname reaches the slot's own container")

	runCLI(t, az.command("webapp", "deployment", "slot", "swap", "-g", rg, "-n", app, "-s", slot))

	assert.Equal(t, "staging-content", served(app), "production's hostname serves what the slot ran")
	assert.Equal(t, "production-content", served(app+"-"+slot))

	settings := func(args ...string) map[string]string {
		var list []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		parseJSON(t, runCLI(t, az.command(append([]string{"webapp", "config", "appsettings", "list",
			"-g", rg, "-n", app, "-o", "json"}, args...)...)), &list)
		out := map[string]string{}
		for _, kv := range list {
			out[kv.Name] = kv.Value
		}
		return out
	}
	prod := settings()
	assert.Equal(t, "from-staging", prod["SWAPPED"], "a swappable setting moves with the content")
	assert.Equal(t, "production", prod["STICKY"], "a slot setting stays with its slot")
	staging := settings("-s", slot)
	assert.Equal(t, "from-production", staging["SWAPPED"])
	assert.Equal(t, "staging", staging["STICKY"])

	var shown struct {
		SlotSwapStatus struct {
			SourceSlotName      string `json:"sourceSlotName"`
			DestinationSlotName string `json:"destinationSlotName"`
		} `json:"slotSwapStatus"`
	}
	parseJSON(t, runCLI(t, az.command("webapp", "show", "-g", rg, "-n", app, "-o", "json")), &shown)
	assert.Equal(t, slot, shown.SlotSwapStatus.SourceSlotName)
	assert.Equal(t, "production", shown.SlotSwapStatus.DestinationSlotName)

	runCLI(t, az.command("webapp", "deployment", "slot", "delete", "-g", rg, "-n", app, "-s", slot))
	var slots []any
	parseJSON(t, runCLI(t, az.command("webapp", "deployment", "slot", "list", "-g", rg, "-n", app, "-o", "json")), &slots)
	require.Empty(t, slots)
	assert.Equal(t, "staging-content", served(app), "production keeps serving after the slot is gone")
}
