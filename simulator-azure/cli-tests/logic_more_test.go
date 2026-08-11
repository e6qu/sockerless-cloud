package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestLogicAppsCLI_MoreOperations exercises the extended Microsoft.Logic ARM
// slice through `az rest`: integration accounts (+ a map artifact), an
// integration service environment, the provider operations list, and the
// workflow version/trigger read collections.
func TestLogicAppsCLI_MoreOperations(t *testing.T) {
	const apiVersion = "2019-05-01"

	// ---- integration account CRUD + list ----
	iaURL := armURL("Microsoft.Logic", "integrationAccounts/cli-ia", apiVersion)
	runCLI(t, azRest("PUT", iaURL, `{"location":"eastus","sku":{"name":"Standard"},"properties":{"state":"Enabled"}}`))
	out := runCLI(t, azRest("GET", iaURL, ""))
	if !strings.Contains(out, `"name": "cli-ia"`) {
		t.Fatalf("integration account did not read back: %s", out)
	}
	listURL := armURL("Microsoft.Logic", "integrationAccounts", apiVersion)
	out = runCLI(t, azRest("GET", listURL, ""))
	if !strings.Contains(out, "cli-ia") {
		t.Fatalf("integration account list missing account: %s", out)
	}

	// ---- integration account map artifact ----
	mapURL := armURL("Microsoft.Logic", "integrationAccounts/cli-ia/maps/cli-map", apiVersion)
	runCLI(t, azRest("PUT", mapURL, `{"properties":{"mapType":"Xslt","contentType":"application/xml","content":"<xsl/>"}}`))
	out = runCLI(t, azRest("GET", mapURL, ""))
	if !strings.Contains(out, `"mapType": "Xslt"`) {
		t.Fatalf("map did not read back: %s", out)
	}
	runCLI(t, azRest("DELETE", mapURL, ""))
	runCLI(t, azRest("DELETE", iaURL, ""))

	// ---- integration service environment ----
	iseURL := armURL("Microsoft.Logic", "integrationServiceEnvironments/cli-ise", apiVersion)
	runCLI(t, azRest("PUT", iseURL, `{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"state":"Enabled"}}`))
	out = runCLI(t, azRest("GET", iseURL, ""))
	if !strings.Contains(out, `"name": "cli-ise"`) {
		t.Fatalf("integration service environment did not read back: %s", out)
	}
	runCLI(t, azRest("DELETE", iseURL, ""))

	// ---- provider operations ----
	opsURL := fmt.Sprintf("%s/providers/Microsoft.Logic/operations?api-version=%s", baseURL, apiVersion)
	out = runCLI(t, azRest("GET", opsURL, ""))
	if !strings.Contains(out, "Microsoft.Logic/workflows/read") {
		t.Fatalf("operations list missing entries: %s", out)
	}

	// ---- workflow version + trigger read collections ----
	wfURL := armURL("Microsoft.Logic", "workflows/cli-more-wf", apiVersion)
	runCLI(t, azRest("PUT", wfURL, `{"location":"eastus","properties":{"definition":{"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#","contentVersion":"1.0.0.0","triggers":{"manual":{"type":"Request","kind":"Http"}},"actions":{},"outputs":{}}}}`))
	versionsURL := strings.Replace(wfURL, "?api-version=", "/versions?api-version=", 1)
	out = runCLI(t, azRest("GET", versionsURL, ""))
	if !strings.Contains(out, "Microsoft.Logic/workflows/versions") {
		t.Fatalf("workflow versions list empty: %s", out)
	}
	triggersURL := strings.Replace(wfURL, "?api-version=", "/triggers?api-version=", 1)
	out = runCLI(t, azRest("GET", triggersURL, ""))
	if !strings.Contains(out, `"name": "manual"`) {
		t.Fatalf("workflow triggers list missing manual trigger: %s", out)
	}
	runCLI(t, azRest("DELETE", wfURL, ""))
}
