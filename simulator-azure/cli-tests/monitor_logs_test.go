package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for a Log Analytics workspace's tables, data collection
// endpoints and rules, and the Logs Ingestion API that uploads through a
// rule. The tables go through the native az monitor log-analytics workspace
// table commands; the endpoints and rules, whose commands ship in a CLI
// extension, through az rest:
//
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.OperationalInsights/workspaces/{workspaceName}/tables
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.OperationalInsights/workspaces/{workspaceName}/tables/{tableName}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.OperationalInsights/workspaces/{workspaceName}/tables/{tableName}/migrate
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionEndpoints/{dataCollectionEndpointName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionEndpoints
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/dataCollectionEndpoints
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionRules/{dataCollectionRuleName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/dataCollectionRules
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/dataCollectionRules
//	POST /dataCollectionRules/{ruleId}/streams/{stream}

func TestLogAnalytics_CLI_TablesRulesAndIngestion(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-logs")
	rg := "cli-logs-rg"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))

	var ws struct {
		ID         string `json:"id"`
		CustomerID string `json:"customerId"`
	}
	parseJSON(t, runCLI(t, env.command("monitor", "log-analytics", "workspace", "create",
		"-g", rg, "-n", "cli-logs-ws", "-o", "json")), &ws)
	other := env.command("monitor", "log-analytics", "workspace", "create", "-g", rg, "-n", "cli-logs-other", "-o", "json")
	var otherWS struct {
		CustomerID string `json:"customerId"`
	}
	parseJSON(t, runCLI(t, other), &otherWS)

	table := func(args ...string) []string {
		return append([]string{"monitor", "log-analytics", "workspace", "table"},
			append(args, "-g", rg, "--workspace-name", "cli-logs-ws", "-o", "json")...)
	}
	var created struct {
		Name   string `json:"name"`
		Schema struct {
			TableType    string `json:"tableType"`
			TableSubType string `json:"tableSubType"`
			Columns      []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"columns"`
		} `json:"schema"`
		RetentionInDays int `json:"retentionInDays"`
	}
	parseJSON(t, runCLI(t, env.command(table("create", "-n", "Builds_CL",
		"--columns", "TimeGenerated=datetime", "Pipeline=string", "Attempt=int", "Detail=dynamic")...)), &created)
	assert.Equal(t, "Builds_CL", created.Name)
	assert.Equal(t, "CustomLog", created.Schema.TableType)
	assert.Equal(t, "DataCollectionRuleBased", created.Schema.TableSubType)
	assert.Len(t, created.Schema.Columns, 4)
	parseJSON(t, runCLI(t, env.command(table("update", "-n", "Builds_CL", "--retention-time", "45")...)), &created)
	assert.Equal(t, 45, created.RetentionInDays)
	parseJSON(t, runCLI(t, env.command(table("show", "-n", "Builds_CL")...)), &created)
	assert.Equal(t, 45, created.RetentionInDays)
	var tables []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command(table("list")...)), &tables)
	names := map[string]bool{}
	for _, tbl := range tables {
		names[tbl.Name] = true
	}
	assert.True(t, names["Builds_CL"] && names["AppTraces"], "the list holds the custom and the Azure tables: %v", names)

	// An environment linking the workspace creates the classic custom log
	// tables Container Apps writes, and migrate moves one onto data
	// collection rules.
	runCLI(t, env.command("rest", "--method", "put", "--url",
		fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/cli-logs-env?api-version=2025-01-01",
			env.baseURL, subscriptionID, rg),
		"--body", fmt.Sprintf(`{"location":"eastus","properties":{"appLogsConfiguration":{"destination":"log-analytics","logAnalyticsConfiguration":{"customerId":%q}}}}`, ws.CustomerID)))
	parseJSON(t, runCLI(t, env.command(table("show", "-n", "ContainerAppConsoleLogs_CL")...)), &created)
	assert.Equal(t, "Classic", created.Schema.TableSubType)
	runCLI(t, env.command(table("migrate", "--table-name", "ContainerAppConsoleLogs_CL")...))
	parseJSON(t, runCLI(t, env.command(table("show", "-n", "ContainerAppConsoleLogs_CL")...)), &created)
	assert.Equal(t, "DataCollectionRuleBased", created.Schema.TableSubType)

	rest := func(method, path, body string) string {
		args := []string{"rest", "--method", method, "--url", env.baseURL + path, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return runCLI(t, env.command(args...))
	}
	insights := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights", subscriptionID, rg)
	var dce struct {
		ID         string `json:"id"`
		Properties struct {
			LogsIngestion struct {
				Endpoint string `json:"endpoint"`
			} `json:"logsIngestion"`
		} `json:"properties"`
	}
	parseJSON(t, rest("put", insights+"/dataCollectionEndpoints/cli-dce?api-version=2023-03-11", `{"location":"eastus"}`), &dce)
	require.NotEmpty(t, dce.Properties.LogsIngestion.Endpoint)

	var rule struct {
		Properties struct {
			ImmutableID  string `json:"immutableId"`
			Destinations struct {
				LogAnalytics []struct {
					WorkspaceID string `json:"workspaceId"`
				} `json:"logAnalytics"`
			} `json:"destinations"`
		} `json:"properties"`
	}
	ruleBody := fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"dataCollectionEndpointId": %q,
			"streamDeclarations": {"Custom-BuildsRaw": {"columns": [
				{"name": "Time", "type": "string"},
				{"name": "Pipeline", "type": "string"},
				{"name": "Attempt", "type": "int"},
				{"name": "Detail", "type": "string"}
			]}},
			"destinations": {"logAnalytics": [{"name": "ws", "workspaceResourceId": %q}]},
			"dataFlows": [{
				"streams": ["Custom-BuildsRaw"],
				"destinations": ["ws"],
				"transformKql": "source | where Attempt > 0 | extend TimeGenerated = todatetime(Time), Detail = parse_json(Detail) | project-away Time",
				"outputStream": "Custom-Builds_CL"
			}]
		}
	}`, dce.ID, ws.ID)
	parseJSON(t, rest("put", insights+"/dataCollectionRules/cli-dcr?api-version=2023-03-11", ruleBody), &rule)
	require.NotEmpty(t, rule.Properties.ImmutableID)
	require.Len(t, rule.Properties.Destinations.LogAnalytics, 1)
	assert.Equal(t, ws.CustomerID, rule.Properties.Destinations.LogAnalytics[0].WorkspaceID)

	now := time.Now().UTC().Format(time.RFC3339)
	upload, err := json.Marshal([]map[string]any{
		{"Time": now, "Pipeline": "cli-build", "Attempt": 1, "Detail": `{"stage":"compile"}`},
		{"Time": now, "Pipeline": "cli-build", "Attempt": 0, "Detail": `{}`},
	})
	require.NoError(t, err)
	runCLI(t, env.command("rest", "--method", "post", "--url",
		dce.Properties.LogsIngestion.Endpoint+"/dataCollectionRules/"+rule.Properties.ImmutableID+"/streams/Custom-BuildsRaw?api-version=2023-01-01",
		"--resource", "https://monitor.azure.com", "--body", string(upload)))

	query := func(customerID, kql string) string {
		body, err := json.Marshal(map[string]string{"query": kql})
		require.NoError(t, err)
		return runCLI(t, env.command("rest", "--method", "post", "--url", env.baseURL+"/v1/workspaces/"+customerID+"/query",
			"--resource", "https://api.loganalytics.io", "--body", string(body), "-o", "json"))
	}
	var result struct {
		Tables []struct {
			Rows [][]any `json:"rows"`
		} `json:"tables"`
	}
	parseJSON(t, query(ws.CustomerID, `Builds_CL | where Pipeline == "cli-build" | project Attempt, Stage = tostring(Detail.stage)`), &result)
	require.Len(t, result.Tables[0].Rows, 1, "the transform drops the row without an attempt")
	assert.Equal(t, []any{float64(1), "compile"}, result.Tables[0].Rows[0])
	failure := runCLIExpectFailure(t, env.command("rest", "--method", "post", "--url", env.baseURL+"/v1/workspaces/"+otherWS.CustomerID+"/query",
		"--resource", "https://api.loganalytics.io", "--body", `{"query":"Builds_CL | take 1"}`))
	assert.Contains(t, failure, "BadArgumentError", "the other workspace has no such table")

	assert.Contains(t, rest("get", insights+"/dataCollectionRules?api-version=2023-03-11", ""), "cli-dcr")
	assert.Contains(t, rest("get", "/subscriptions/"+subscriptionID+"/providers/Microsoft.Insights/dataCollectionRules?api-version=2023-03-11", ""), "cli-dcr")
	assert.Contains(t, rest("patch", insights+"/dataCollectionRules/cli-dcr?api-version=2023-03-11", `{"tags":{"team":"logs"}}`), `"team": "logs"`)
	assert.Contains(t, rest("get", insights+"/dataCollectionRules/cli-dcr?api-version=2023-03-11", ""), rule.Properties.ImmutableID)
	assert.Contains(t, rest("get", insights+"/dataCollectionEndpoints?api-version=2023-03-11", ""), "cli-dce")
	assert.Contains(t, rest("get", "/subscriptions/"+subscriptionID+"/providers/Microsoft.Insights/dataCollectionEndpoints?api-version=2023-03-11", ""), "cli-dce")
	assert.Contains(t, rest("patch", insights+"/dataCollectionEndpoints/cli-dce?api-version=2023-03-11", `{"tags":{"team":"logs"}}`), `"team": "logs"`)
	assert.Contains(t, rest("get", insights+"/dataCollectionEndpoints/cli-dce?api-version=2023-03-11", ""), "cli-dce")
	rest("delete", insights+"/dataCollectionRules/cli-dcr?api-version=2023-03-11", "")
	rest("delete", insights+"/dataCollectionEndpoints/cli-dce?api-version=2023-03-11", "")
	failure = runCLIExpectFailure(t, env.command("rest", "--method", "get", "--url", env.baseURL+insights+"/dataCollectionRules/cli-dcr?api-version=2023-03-11"))
	assert.Contains(t, failure, "ResourceNotFound")

	runCLI(t, env.command(append(table("delete", "-n", "Builds_CL"), "--yes")...))
	failure = runCLIExpectFailure(t, env.command(table("show", "-n", "Builds_CL")...))
	assert.True(t, strings.Contains(failure, "ResourceNotFound") || strings.Contains(failure, "not found"), "%s", failure)
}
