package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"sync"
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

// cliLogWorkspace creates a Log Analytics workspace through az rest and
// returns its ARM id and customer id.
func cliLogWorkspace(t *testing.T, name string) (id, customerID string) {
	t.Helper()
	var ws struct {
		ID         string `json:"id"`
		Properties struct {
			CustomerID string `json:"customerId"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", monitorURL("workspaces/"+name), `{"location":"eastus"}`)), &ws)
	return ws.ID, ws.Properties.CustomerID
}

// cliLogs is the Container Apps environment the suite's jobs and apps run
// in, linked to a workspace, so their logs land where the tests query them.
var cliLogs struct {
	sync.Mutex
	envID, customerID string
}

// cliLogsEnvironment creates, once per run, the environment the suite's
// Container Apps run in and the workspace its appLogsConfiguration names.
func cliLogsEnvironment(t *testing.T) (envID, customerID string) {
	t.Helper()
	cliLogs.Lock()
	defer cliLogs.Unlock()
	if cliLogs.envID != "" {
		return cliLogs.envID, cliLogs.customerID
	}
	_, customer := cliLogWorkspace(t, "cli-aca-logs-ws")
	var keys struct {
		PrimarySharedKey string `json:"primarySharedKey"`
	}
	parseJSON(t, runCLI(t, azRest("POST", monitorURL("workspaces/cli-aca-logs-ws/sharedKeys"), "")), &keys)
	var env struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", acaURL("managedEnvironments/cli-aca-logs-env"), fmt.Sprintf(
		`{"location":"eastus","properties":{"appLogsConfiguration":{"destination":"log-analytics","logAnalyticsConfiguration":{"customerId":%q,"sharedKey":%q}}}}`,
		customer, keys.PrimarySharedKey))), &env)
	cliLogs.envID, cliLogs.customerID = env.ID, customer
	return cliLogs.envID, cliLogs.customerID
}

// cliQueryWorkspace runs a KQL query against a workspace through az rest.
func cliQueryWorkspace(t *testing.T, customerID, kql string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"query": kql})
	require.NoError(t, err)
	return runCLI(t, azRest("POST", baseURL+"/v1/workspaces/"+customerID+"/query", string(body)))
}

// TestLogAnalytics_QueryBooleanOperatorsCLI drives the Log Analytics query
// endpoint through `az rest` with where clauses joined by and/or and a string
// literal holding a pipe, and reads back the BadArgumentError a query that does
// not parse is refused with. The rows arrive through the Logs Ingestion API,
// by way of a data collection rule writing a custom table.
func TestLogAnalytics_QueryBooleanOperatorsCLI(t *testing.T) {
	const role = "cli-and-or-role"
	wsID, customerID := cliLogWorkspace(t, "cli-and-or-ws")
	runCLI(t, azRest("PUT", monitorURL("workspaces/cli-and-or-ws/tables/SimTraces_CL"),
		`{"properties":{"schema":{"name":"SimTraces_CL","columns":[{"name":"TimeGenerated","type":"dateTime"},{"name":"AppRoleName","type":"string"},{"name":"Message","type":"string"}]}}}`))
	var rule struct {
		Properties struct {
			ImmutableID string `json:"immutableId"`
			Endpoints   struct {
				LogsIngestion string `json:"logsIngestion"`
			} `json:"endpoints"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", armURL("Microsoft.Insights", "dataCollectionRules/cli-and-or-dcr", "2023-03-11"), fmt.Sprintf(`{
		"location": "eastus",
		"kind": "Direct",
		"properties": {
			"streamDeclarations": {"Custom-SimTraces_CL": {"columns": [
				{"name": "TimeGenerated", "type": "datetime"},
				{"name": "AppRoleName", "type": "string"},
				{"name": "Message", "type": "string"}
			]}},
			"destinations": {"logAnalytics": [{"name": "ws", "workspaceResourceId": %q}]},
			"dataFlows": [{"streams": ["Custom-SimTraces_CL"], "destinations": ["ws"]}]
		}
	}`, wsID))), &rule)
	require.NotEmpty(t, rule.Properties.ImmutableID)
	require.NotEmpty(t, rule.Properties.Endpoints.LogsIngestion)

	ts := time.Now().UTC().Format(time.RFC3339)
	rows, err := json.Marshal([]map[string]string{
		{"TimeGenerated": ts, "Message": "alpha | beta", "AppRoleName": role},
		{"TimeGenerated": ts, "Message": "gamma", "AppRoleName": role},
		{"TimeGenerated": ts, "Message": "alpha | beta", "AppRoleName": "cli-other-role"},
	})
	require.NoError(t, err)
	runCLI(t, azRest("POST", rule.Properties.Endpoints.LogsIngestion+"/dataCollectionRules/"+
		rule.Properties.ImmutableID+"/streams/Custom-SimTraces_CL?api-version=2023-01-01", string(rows)))

	var result struct {
		Tables []struct {
			Columns []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"columns"`
			Rows [][]any `json:"rows"`
		} `json:"tables"`
	}

	out := cliQueryWorkspace(t, customerID,
		`SimTraces_CL | where AppRoleName == "`+role+`" and Message == "alpha | beta" | project Message`)
	parseJSON(t, out, &result)
	require.Len(t, result.Tables, 1)
	require.Len(t, result.Tables[0].Rows, 1, "and keeps only the row both comparisons match: %s", out)
	assert.Equal(t, "alpha | beta", result.Tables[0].Rows[0][0])

	out = cliQueryWorkspace(t, customerID,
		`SimTraces_CL | where AppRoleName == "`+role+`" and (Message == "gamma" or Message has "beta") | count`)
	parseJSON(t, out, &result)
	require.Len(t, result.Tables[0].Rows, 1)
	assert.Equal(t, "Count", result.Tables[0].Columns[0].Name)
	assert.EqualValues(t, 2, result.Tables[0].Rows[0][0], "or matches both of the role's rows: %s", out)

	body, err := json.Marshal(map[string]string{"query": `SimTraces_CL | where AppRoleName == "x" and`})
	require.NoError(t, err)
	failure := runCLIExpectFailure(t, azRest("POST", baseURL+"/v1/workspaces/"+customerID+"/query", string(body)))
	assert.Contains(t, failure, "BadArgumentError")
	assert.Contains(t, failure, "SyntaxError")
}
