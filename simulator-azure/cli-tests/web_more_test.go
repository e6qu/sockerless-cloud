package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

const webMoreAPIVersion = "2023-12-01"

func webMoreURL(resourcePath string) string {
	return armURL("Microsoft.Web", resourcePath, webMoreAPIVersion)
}

// TestWebMore_CLI exercises the broader Microsoft.Web slice: plan list, a web
// app's lifecycle, config, deployments and host-name bindings, a deployment
// slot, a static web app, and the geo-region catalog.
func TestWebMore_CLI(t *testing.T) {
	t.Parallel()
	runCLI(t, azRest("PUT", webMoreURL("serverfarms/cli-wm-plan"),
		`{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}`))
	runCLI(t, azRest("PUT", webMoreURL("sites/cli-wm-site"),
		`{"location":"eastus","kind":"functionapp","properties":{"serverFarmId":"/subscriptions/`+subscriptionID+`/resourceGroups/`+resourceGroup+`/providers/Microsoft.Web/serverfarms/cli-wm-plan"}}`))

	subPlans := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/serverfarms?api-version=%s",
		baseURL, subscriptionID, webMoreAPIVersion)
	var plansResp struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", subPlans, "")), &plansResp)
	found := false
	for _, p := range plansResp.Value {
		if p.Name == "cli-wm-plan" {
			found = true
		}
	}
	assert.True(t, found, "plan must appear in list-by-subscription")

	runCLI(t, azRest("POST", webMoreURL("sites/cli-wm-site/stop"), ""))
	runCLI(t, azRest("POST", webMoreURL("sites/cli-wm-site/start"), ""))

	runCLI(t, azRest("PUT", webMoreURL("sites/cli-wm-site/config/metadata"),
		`{"properties":{"OWNER":"team-a"}}`))
	var md struct {
		Properties map[string]string `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("POST", webMoreURL("sites/cli-wm-site/config/metadata/list"), "")), &md)
	assert.Equal(t, "team-a", md.Properties["OWNER"])

	runCLI(t, azRest("PUT", webMoreURL("sites/cli-wm-site/deployments/d1"),
		`{"properties":{"status":4,"author":"ci","message":"ok"}}`))
	var dep struct {
		Properties struct {
			Author string `json:"author"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("sites/cli-wm-site/deployments/d1"), "")), &dep)
	assert.Equal(t, "ci", dep.Properties.Author)

	runCLI(t, azRest("PUT", webMoreURL("sites/cli-wm-site/hostNameBindings/app.example.com"),
		`{"properties":{"siteName":"cli-wm-site","hostNameType":"Verified"}}`))
	runCLI(t, azRest("GET", webMoreURL("sites/cli-wm-site/hostNameBindings/app.example.com"), ""))

	runCLI(t, azRest("PUT", webMoreURL("sites/cli-wm-site/slots/staging"),
		`{"location":"eastus","kind":"functionapp"}`))
	var slot struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("sites/cli-wm-site/slots/staging"), "")), &slot)
	assert.Equal(t, "cli-wm-site/staging", slot.Name)

	runCLI(t, azRest("PUT", webMoreURL("staticSites/cli-wm-static"),
		`{"location":"eastus2","sku":{"name":"Free","tier":"Free"},"properties":{"repositoryUrl":"https://github.com/example/x","branch":"main"}}`))
	var static struct {
		Properties struct {
			DefaultHostname string `json:"defaultHostname"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", webMoreURL("staticSites/cli-wm-static"), "")), &static)
	assert.Equal(t, "cli-wm-static.azurestaticapps.net", static.Properties.DefaultHostname)

	geo := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Web/geoRegions?api-version=%s",
		baseURL, subscriptionID, webMoreAPIVersion)
	runCLI(t, azRest("GET", geo, ""))

	runCLI(t, azRest("DELETE", webMoreURL("sites/cli-wm-site/slots/staging"), ""))
	runCLI(t, azRest("DELETE", webMoreURL("staticSites/cli-wm-static"), ""))
	runCLI(t, azRest("DELETE", webMoreURL("sites/cli-wm-site"), ""))
	runCLI(t, azRest("DELETE", webMoreURL("serverfarms/cli-wm-plan"), ""))
}
