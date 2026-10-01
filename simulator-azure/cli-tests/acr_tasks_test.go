package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

const acrTasksAPIVersion = "2019-06-01-preview"

func acrTasksURL(registry, path string) string {
	return armURL("Microsoft.ContainerRegistry", "registries/"+registry+path, acrTasksAPIVersion)
}

type acrTasksRun struct {
	Name       string `json:"name"`
	Properties struct {
		RunID           string `json:"runId"`
		Status          string `json:"status"`
		StartTime       string `json:"startTime"`
		FinishTime      string `json:"finishTime"`
		RunErrorMessage string `json:"runErrorMessage"`
	} `json:"properties"`
}

// TestACRTasksCLI_ScheduleRunQueuesThenEnds schedules a quick build through
// Registries_ScheduleRun, which answers with the Run Queued; the run then ends
// in the background and reads its terminal status and the reason it failed,
// and its log link, the run and a cancel of the ended run stay consistent.
func TestACRTasksCLI_ScheduleRunQueuesThenEnds(t *testing.T) {
	const registry = "clitasksregistry"
	runCLI(t, azRest("PUT", acrURL("registries/"+registry),
		`{"location":"eastus","sku":{"name":"Standard"},"properties":{"adminUserEnabled":false}}`))
	t.Cleanup(func() { _, _, _ = runCLIStreamsResult(azRest("DELETE", acrURL("registries/"+registry), "")) })

	out := runCLI(t, azRest("POST", acrTasksURL(registry, "/scheduleRun"), fmt.Sprintf(`{
		"type": "DockerBuildRequest",
		"dockerFilePath": "Dockerfile",
		"imageNames": ["%s.azurecr.io/app:v1"],
		"isPushEnabled": false,
		"sourceLocation": "https://clitasksnoacct.blob.core.windows.net/ctx/missing.tar.gz",
		"platform": {"os": "Linux"}
	}`, registry)))
	var queued acrTasksRun
	parseJSON(t, out, &queued)
	if queued.Properties.Status != "Queued" {
		t.Fatalf("scheduleRun answered status %q, want Queued", queued.Properties.Status)
	}
	if queued.Properties.FinishTime != "" {
		t.Fatalf("a queued run has not finished, finishTime %q", queued.Properties.FinishTime)
	}

	runURL := acrTasksURL(registry, "/runs/"+queued.Properties.RunID)
	out = waitForCLIJSON(t, runURL, func(body string) bool {
		var run acrTasksRun
		parseJSON(t, body, &run)
		switch run.Properties.Status {
		case "Queued", "Started", "Running":
			return false
		}
		return true
	})
	var run acrTasksRun
	parseJSON(t, out, &run)
	if run.Properties.Status != "Failed" {
		t.Fatalf("a run without its build context ended %q, want Failed", run.Properties.Status)
	}
	if !strings.Contains(run.Properties.RunErrorMessage, "not found") {
		t.Fatalf("runErrorMessage %q must say the build context was not found", run.Properties.RunErrorMessage)
	}

	var sas struct {
		LogLink string `json:"logLink"`
	}
	parseJSON(t, runCLI(t, azRest("POST", acrTasksURL(registry, "/runs/"+run.Properties.RunID+"/listLogSasUrl"), "")), &sas)
	if !strings.Contains(sas.LogLink, run.Properties.RunID) {
		t.Fatalf("log link %q must name the run", sas.LogLink)
	}

	runCLI(t, azRest("POST", acrTasksURL(registry, "/runs/"+run.Properties.RunID+"/cancel"), ""))
	var after acrTasksRun
	parseJSON(t, runCLI(t, azRest("GET", runURL, "")), &after)
	if after.Properties.Status != "Failed" || after.Properties.FinishTime != run.Properties.FinishTime {
		t.Fatalf("cancel of an ended run changed it: %+v, was %+v", after.Properties, run.Properties)
	}

	_, stderr, err := runCLIStreamsResult(azRest("POST", acrTasksURL(registry, "/scheduleRun"), fmt.Sprintf(`{
		"type": "DockerBuildRequest",
		"imageNames": ["%s.azurecr.io/app:v1"],
		"sourceLocation": "https://clitasksnoacct.blob.core.windows.net/ctx/missing.tar.gz",
		"timeout": 60
	}`, registry)))
	if err == nil || !strings.Contains(stderr, "Bad Request") {
		t.Fatalf("a timeout below the 300-second minimum must be refused with 400; err %v, stderr %s", err, stderr)
	}
}
