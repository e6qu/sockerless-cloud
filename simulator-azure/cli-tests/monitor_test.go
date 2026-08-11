package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const monitorAPIVersion = "2022-10-01"
const insightsAPIVersion = "2020-02-02"

func monitorURL(path string) string {
	return armURL("Microsoft.OperationalInsights", path, monitorAPIVersion)
}

func TestMonitorWorkspace_CreateAndShow(t *testing.T) {
	url := monitorURL("workspaces/cli-test-workspace")

	out := runCLI(t, azRest("PUT", url,
		`{"location":"eastus","properties":{"retentionInDays":30}}`))

	var ws struct {
		Name       string `json:"name"`
		Location   string `json:"location"`
		Properties struct {
			ProvisioningState               string `json:"provisioningState"`
			WorkspaceID                     string `json:"customerId"`
			PublicNetworkAccessForIngestion string `json:"publicNetworkAccessForIngestion"`
			PublicNetworkAccessForQuery     string `json:"publicNetworkAccessForQuery"`
		} `json:"properties"`
	}
	parseJSON(t, out, &ws)
	assert.Equal(t, "cli-test-workspace", ws.Name)
	assert.Equal(t, "eastus", ws.Location)
	assert.Equal(t, "Succeeded", ws.Properties.ProvisioningState)
	assert.NotEmpty(t, ws.Properties.WorkspaceID)
	assert.Equal(t, "Enabled", ws.Properties.PublicNetworkAccessForIngestion)
	assert.Equal(t, "Enabled", ws.Properties.PublicNetworkAccessForQuery)

	// GET
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &ws)
	assert.Equal(t, "cli-test-workspace", ws.Name)
	assert.Equal(t, "Enabled", ws.Properties.PublicNetworkAccessForIngestion)
	assert.Equal(t, "Enabled", ws.Properties.PublicNetworkAccessForQuery)

	// Cleanup
	runCLI(t, azRest("DELETE", url, ""))
}

func TestMonitorWorkspace_Delete(t *testing.T) {
	url := monitorURL("workspaces/delete-test-ws")
	runCLI(t, azRest("PUT", url, `{"location":"eastus","properties":{}}`))
	runCLI(t, azRest("DELETE", url, ""))

	cmd := azRest("GET", url, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "Expected GET to fail after deletion")
}

// App Insights (Microsoft.Insights/components) CLI coverage via az rest.
// The `az monitor app-insights component` high-level commands route to the
// same ARM endpoints; exercising via az rest covers the same code paths.

func insightsURL(path string) string {
	return armURL("Microsoft.Insights", path, insightsAPIVersion)
}

func TestAppInsights_CreateAndShow(t *testing.T) {
	url := insightsURL("components/cli-test-appinsights")

	out := runCLI(t, azRest("PUT", url,
		`{"location":"eastus","kind":"web","properties":{"Application_Type":"web"}}`))

	var comp struct {
		Name       string `json:"name"`
		Location   string `json:"location"`
		Kind       string `json:"kind"`
		Properties struct {
			InstrumentationKey string `json:"InstrumentationKey"`
			ConnectionString   string `json:"connectionString"`
			ProvisioningState  string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &comp)
	assert.Equal(t, "cli-test-appinsights", comp.Name)
	assert.Equal(t, "eastus", comp.Location)
	assert.Equal(t, "web", comp.Kind)
	assert.NotEmpty(t, comp.Properties.InstrumentationKey)
	assert.Contains(t, comp.Properties.ConnectionString, "InstrumentationKey=")
	assert.Equal(t, "Succeeded", comp.Properties.ProvisioningState)

	// GET
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &comp)
	assert.Equal(t, "cli-test-appinsights", comp.Name)
	assert.NotEmpty(t, comp.Properties.InstrumentationKey)

	// Cleanup
	runCLI(t, azRest("DELETE", url, ""))
}

func TestAppInsights_InstrumentationKeyInConnectionString(t *testing.T) {
	url := insightsURL("components/ikey-check-appinsights")

	out := runCLI(t, azRest("PUT", url,
		`{"location":"eastus","kind":"web","properties":{"Application_Type":"web"}}`))

	var comp struct {
		Properties struct {
			InstrumentationKey string `json:"InstrumentationKey"`
			ConnectionString   string `json:"connectionString"`
		} `json:"properties"`
	}
	parseJSON(t, out, &comp)
	require.NotEmpty(t, comp.Properties.InstrumentationKey)
	require.NotEmpty(t, comp.Properties.ConnectionString)
	// ConnectionString must embed the instrumentation key
	assert.Contains(t, comp.Properties.ConnectionString, comp.Properties.InstrumentationKey)

	runCLI(t, azRest("DELETE", url, ""))
}
