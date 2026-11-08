package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestLogicAppsCLI_MoreOperations exercises the extended Microsoft.Logic slice
// through `az rest`: integration accounts with a map artifact, an integration
// service environment, the provider operations list, and the workflow version
// and trigger collections.
func TestLogicAppsCLI_MoreOperations(t *testing.T) {
	t.Parallel()
	const apiVersion = "2019-05-01"

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

	mapURL := armURL("Microsoft.Logic", "integrationAccounts/cli-ia/maps/cli-map", apiVersion)
	runCLI(t, azRest("PUT", mapURL, `{"properties":{"mapType":"Xslt","contentType":"application/xml","content":"<xsl/>"}}`))
	out = runCLI(t, azRest("GET", mapURL, ""))
	if !strings.Contains(out, `"mapType": "Xslt"`) {
		t.Fatalf("map did not read back: %s", out)
	}
	runCLI(t, azRest("DELETE", mapURL, ""))
	runCLI(t, azRest("DELETE", iaURL, ""))

	iseURL := armURL("Microsoft.Logic", "integrationServiceEnvironments/cli-ise", apiVersion)
	runCLI(t, azRest("PUT", iseURL, `{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"state":"Enabled"}}`))
	out = runCLI(t, azRest("GET", iseURL, ""))
	if !strings.Contains(out, `"name": "cli-ise"`) {
		t.Fatalf("integration service environment did not read back: %s", out)
	}
	runCLI(t, azRest("DELETE", iseURL, ""))

	opsURL := fmt.Sprintf("%s/providers/Microsoft.Logic/operations?api-version=%s", baseURL, apiVersion)
	out = runCLI(t, azRest("GET", opsURL, ""))
	if !strings.Contains(out, "Microsoft.Logic/workflows/read") {
		t.Fatalf("operations list missing entries: %s", out)
	}

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

	// A For each loop records a scope repetition per item and a repetition of
	// each action inside it.
	loopURL := armURL("Microsoft.Logic", "workflows/cli-loop-wf", apiVersion)
	runCLI(t, azRest("PUT", loopURL, `{"location":"eastus","properties":{"definition":{"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#","contentVersion":"1.0.0.0","triggers":{"manual":{"type":"Request","kind":"Http"}},"actions":{"each":{"type":"Foreach","foreach":["x","y"],"actions":{"echo":{"type":"Compose","inputs":"@items('each')"}}}},"outputs":{}}}}`))
	runCLI(t, azRest("POST", strings.Replace(loopURL, "?api-version=", "/triggers/manual/run?api-version=", 1), ""))
	runsURL := strings.Replace(loopURL, "?api-version=", "/runs?api-version=", 1)
	var runs struct {
		Value []struct {
			Name       string `json:"name"`
			Properties struct {
				Status string `json:"status"`
			} `json:"properties"`
		} `json:"value"`
	}
	// Logic Apps offers no waiter for a run, so poll its status.
	deadline := time.Now().Add(60 * time.Second)
	for {
		parseJSON(t, runCLI(t, azRest("GET", runsURL, "")), &runs)
		if len(runs.Value) == 1 && runs.Value[0].Properties.Status != "Running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run did not finish: %+v", runs.Value)
		}
		time.Sleep(500 * time.Millisecond)
	}
	runName := runs.Value[0].Name
	actionURL := func(action, rest string) string {
		return strings.Replace(loopURL, "?api-version=", "/runs/"+runName+"/actions/"+action+rest+"?api-version=", 1)
	}
	var reps struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", actionURL("echo", "/repetitions"), "")), &reps)
	if len(reps.Value) != 2 || reps.Value[0].Name != "000000" || reps.Value[1].Name != "000001" {
		t.Fatalf("echo repetitions = %+v, want 000000 and 000001", reps.Value)
	}
	parseJSON(t, runCLI(t, azRest("GET", actionURL("each", "/scopeRepetitions"), "")), &reps)
	if len(reps.Value) != 2 {
		t.Fatalf("each scope repetitions = %+v, want two", reps.Value)
	}
	runCLI(t, azRest("DELETE", loopURL, ""))
}
