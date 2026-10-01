package azure_cli_test

import (
	"encoding/json"
	"testing"
	"time"

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

	failure := runCLIExpectFailure(t, azRest("GET", url, ""))
	assert.Contains(t, failure, "ResourceNotFound",
		"a deleted Azure Monitor workspace must answer GET with ResourceNotFound, got: %s", failure)
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

// TestLogAnalytics_QueryBooleanOperatorsCLI drives the Log Analytics query
// endpoint through `az rest` with where clauses joined by and/or and a string
// literal holding a pipe, and reads back the BadArgumentError a query that does
// not parse is refused with.
func TestLogAnalytics_QueryBooleanOperatorsCLI(t *testing.T) {
	const role = "cli-and-or-role"
	ts := time.Now().UTC().Format(time.RFC3339)
	rows, err := json.Marshal([]map[string]string{
		{"TimeGenerated": ts, "Message": "alpha | beta", "AppRoleName": role},
		{"TimeGenerated": ts, "Message": "gamma", "AppRoleName": role},
		{"TimeGenerated": ts, "Message": "alpha | beta", "AppRoleName": "cli-other-role"},
	})
	require.NoError(t, err)
	runCLI(t, azRest("POST", baseURL+"/dataCollectionRules/dcr-1/streams/Custom-Logs", string(rows)))

	queryURL := baseURL + "/v1/workspaces/default/query"
	query := func(kql string) string {
		body, err := json.Marshal(map[string]string{"query": kql})
		require.NoError(t, err)
		return string(body)
	}
	var result struct {
		Tables []struct {
			Columns []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"columns"`
			Rows [][]any `json:"rows"`
		} `json:"tables"`
	}

	out := runCLI(t, azRest("POST", queryURL,
		query(`AppTraces | where AppRoleName == "`+role+`" and Message == "alpha | beta" | project Message`)))
	parseJSON(t, out, &result)
	require.Len(t, result.Tables, 1)
	require.Len(t, result.Tables[0].Rows, 1, "and keeps only the row both comparisons match: %s", out)
	assert.Equal(t, "alpha | beta", result.Tables[0].Rows[0][0])

	out = runCLI(t, azRest("POST", queryURL,
		query(`AppTraces | where AppRoleName == "`+role+`" and (Message == "gamma" or Message has "beta") | count`)))
	parseJSON(t, out, &result)
	require.Len(t, result.Tables[0].Rows, 1)
	assert.Equal(t, "Count", result.Tables[0].Columns[0].Name)
	assert.EqualValues(t, 2, result.Tables[0].Rows[0][0], "or matches both of the role's rows: %s", out)

	failure := runCLIExpectFailure(t, azRest("POST", queryURL,
		query(`AppTraces | where AppRoleName == "x" and`)))
	assert.Contains(t, failure, "BadArgumentError")
	assert.Contains(t, failure, "SyntaxError")
}
