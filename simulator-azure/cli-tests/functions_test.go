package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const functionsAPIVersion = "2022-09-01"

func funcURL(path string) string {
	return armURL("Microsoft.Web", path, functionsAPIVersion)
}

func TestFunctionApp_CreateAndShow(t *testing.T) {
	t.Parallel()
	planURL := aspURL("serverfarms/func-test-plan")
	runCLI(t, azRest("PUT", planURL, `{"location":"eastus","sku":{"name":"Y1","tier":"Dynamic"}}`))

	url := funcURL("sites/cli-test-funcapp")
	body := `{
		"location": "eastus",
		"kind": "functionapp",
		"properties": {
			"serverFarmId": "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/cli-test-rg/providers/Microsoft.Web/serverfarms/func-test-plan",
			"siteConfig": {
				"appSettings": [
					{"name": "FUNCTIONS_EXTENSION_VERSION", "value": "~4"},
					{"name": "FUNCTIONS_WORKER_RUNTIME", "value": "node"}
				]
			}
		}
	}`

	out := runCLI(t, azRest("PUT", url, body))

	var site struct {
		Name       string `json:"name"`
		Location   string `json:"location"`
		Kind       string `json:"kind"`
		Properties struct {
			State             string `json:"state"`
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &site)
	assert.Equal(t, "cli-test-funcapp", site.Name)
	assert.Equal(t, "eastus", site.Location)

	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &site)
	assert.Equal(t, "cli-test-funcapp", site.Name)

	var listed struct {
		Value []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", funcURL("sites"), "")), &listed)
	var names []string
	for _, row := range listed.Value {
		names = append(names, row.Name)
		if row.Name == "cli-test-funcapp" {
			assert.Equal(t, "functionapp", row.Kind)
		}
	}
	assert.Contains(t, names, "cli-test-funcapp", "WebApps_ListByResourceGroup must list the function app")

	runCLI(t, azRest("DELETE", url, ""))
	runCLI(t, azRest("DELETE", planURL, ""))
}

// TestFunctionApp_CLI_InvokeReachesTheContainer invokes a container function
// app through its hostname: the container answers, and its access-log line
// reaches AppTraces in the workspace of the Application Insights component its
// connection string names.
func TestFunctionApp_CLI_InvokeReachesTheContainer(t *testing.T) {
	t.Parallel()
	wsID, customerID := cliLogWorkspace(t, "cli-invoke-ws")
	var component struct {
		Properties struct {
			ConnectionString string `json:"ConnectionString"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", insightsURL("components/cli-invoke-insights"), fmt.Sprintf(
		`{"location":"eastus","kind":"web","properties":{"Application_Type":"web","WorkspaceResourceId":%q}}`, wsID))), &component)
	require.NotEmpty(t, component.Properties.ConnectionString)

	url := funcURL("sites/cli-invoke-funcapp")
	body := fmt.Sprintf(`{
		"location": "eastus",
		"kind": "functionapp,linux,container",
		"properties": {
			"serverFarmId": "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/cli-test-rg/providers/Microsoft.Web/serverfarms/invoke-plan",
			"siteConfig": {
				"linuxFxVersion": "DOCKER|%s",
				"appCommandLine": "serve 80 cli-invoked",
				"appSettings": [{"name": "APPLICATIONINSIGHTS_CONNECTION_STRING", "value": %q}]
			}
		}
	}`, commandImageName, component.Properties.ConnectionString)
	runCLI(t, azRest("PUT", url, body))
	defer runCLI(t, azRest("DELETE", url, ""))

	// Azure routes by the per-site hostname <site>.azurewebsites.net; the
	// simulator serves every site on one port, so dial it and pin the Host header.
	invokeURL := baseURL + "/api/function"
	out := runCLI(t, azRest("POST", invokeURL, "{}", "--headers", "Host=cli-invoke-funcapp.azurewebsites.net"))
	assert.Equal(t, "cli-invoked", strings.TrimSpace(out))

	// The engine's log stream delivers the line after the response, and App
	// Service offers no event for its arrival, so read the table until it does.
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(cliQueryWorkspace(t, customerID, `AppTraces | where AppRoleName == "cli-invoke-funcapp"`), "POST /api/function") {
		require.True(t, time.Now().Before(deadline), "the container's access-log line should reach AppTraces")
		time.Sleep(200 * time.Millisecond)
	}
}

func TestFunctionApp_Delete(t *testing.T) {
	t.Parallel()
	url := funcURL("sites/delete-test-funcapp")
	runCLI(t, azRest("PUT", url, `{"location":"eastus","properties":{}}`))
	runCLI(t, azRest("DELETE", url, ""))

	failure := runCLIExpectFailure(t, azRest("GET", url, ""))
	assert.Contains(t, failure, "ResourceNotFound",
		"a deleted function app must answer GET with ResourceNotFound, got: %s", failure)
}
