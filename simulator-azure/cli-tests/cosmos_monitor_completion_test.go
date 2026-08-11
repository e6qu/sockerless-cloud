package azure_cli_test

import (
	"strings"
	"testing"
)

// TestAzureCosmosDB_MetricsUsagesAndPECCLIFlows drives the Cosmos DB metric,
// usage, metric-definition, percentile and private-endpoint-connection surfaces
// through `az rest` against the simulator.
func TestAzureCosmosDB_MetricsUsagesAndPECCLIFlows(t *testing.T) {
	account := "climetricscosmos"
	const ver = "2024-08-15"
	acctURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account, ver)
	runCLI(t, azRest("PUT", acctURL, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard","locations":[{"locationName":"East US","failoverPriority":0}]}}`))

	metricsURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/metrics", ver)
	if out := runCLI(t, azRest("GET", metricsURL, "")); !strings.Contains(out, "metricValues") {
		t.Fatalf("account metrics missing metricValues: %s", out)
	}
	usagesURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/usages", ver)
	if out := runCLI(t, azRest("GET", usagesURL, "")); !strings.Contains(out, "currentValue") {
		t.Fatalf("account usages missing currentValue: %s", out)
	}
	defsURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/metricDefinitions", ver)
	if out := runCLI(t, azRest("GET", defsURL, "")); !strings.Contains(out, "metricAvailabilities") {
		t.Fatalf("account metricDefinitions missing metricAvailabilities: %s", out)
	}
	percURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/percentile/metrics", ver)
	if out := runCLI(t, azRest("GET", percURL, "")); !strings.Contains(out, "P99") {
		t.Fatalf("percentile metrics missing P99: %s", out)
	}

	// Private endpoint connection: approve, read, list.
	pecURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/privateEndpointConnections/cli-pe-1", ver)
	putOut := runCLI(t, azRest("PUT", pecURL, `{"properties":{"privateLinkServiceConnectionState":{"status":"Approved","description":"ok"}}}`))
	if !strings.Contains(putOut, `"Succeeded"`) {
		t.Fatalf("PEC create missing Succeeded provisioning state: %s", putOut)
	}
	if out := runCLI(t, azRest("GET", pecURL, "")); !strings.Contains(out, "Approved") {
		t.Fatalf("PEC get missing connection status: %s", out)
	}
	pecListURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/privateEndpointConnections", ver)
	if out := runCLI(t, azRest("GET", pecListURL, "")); !strings.Contains(out, "cli-pe-1") {
		t.Fatalf("PEC list missing the connection: %s", out)
	}
	runCLI(t, azRest("DELETE", pecURL, ""))
}

// TestLogAnalytics_QueryAndMetadataCLIFlows drives the Log Analytics query (GET
// + batch) and metadata data-plane endpoints through `az rest`.
func TestLogAnalytics_QueryAndMetadataCLIFlows(t *testing.T) {
	// GET /v1/workspaces/{workspaceId}/query (KQL via the query parameter).
	queryURL := baseURL + "/v1/workspaces/default/query?query=AppTraces%20%7C%20take%201"
	if out := runCLI(t, azRest("GET", queryURL, "")); !strings.Contains(out, "PrimaryResult") {
		t.Fatalf("Log Analytics GET query missing PrimaryResult table: %s", out)
	}

	// GET /v1/workspaces/{workspaceId}/metadata (workspace schema).
	metaURL := baseURL + "/v1/workspaces/default/metadata"
	if out := runCLI(t, azRest("GET", metaURL, "")); !strings.Contains(out, "tables") {
		t.Fatalf("Log Analytics metadata missing tables: %s", out)
	}

	// POST /v1/$batch (a batch of one query).
	batchURL := baseURL + "/v1/$batch"
	batchBody := `{"requests":[{"id":"r1","workspace":"default","body":{"query":"AppTraces | take 1"}}]}`
	if out := runCLI(t, azRest("POST", batchURL, batchBody)); !strings.Contains(out, "r1") {
		t.Fatalf("Log Analytics batch missing request id: %s", out)
	}
}
