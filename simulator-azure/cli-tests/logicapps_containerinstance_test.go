package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLogicAppsCLI_WorkflowLifecycle(t *testing.T) {
	url := armURL("Microsoft.Logic", "workflows/cli-logic", "2019-05-01")
	body := `{"location":"eastus","properties":{"definition":{"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#","contentVersion":"1.0.0.0","triggers":{"manual":{"type":"Request","kind":"Http"}},"actions":{},"outputs":{}},"parameters":{}},"tags":{"env":"cli"}}`
	runCLI(t, azRest("PUT", url, body))

	out := runCLI(t, azRest("GET", url, ""))
	if !strings.Contains(out, `"state": "Enabled"`) {
		t.Fatalf("workflow did not read back enabled state: %s", out)
	}

	runCLI(t, azRest("POST", strings.Replace(url, "?api-version=", "/disable?api-version=", 1), ""))
	out = runCLI(t, azRest("GET", url, ""))
	if !strings.Contains(out, `"state": "Disabled"`) {
		t.Fatalf("workflow did not read back disabled state: %s", out)
	}

	runCLI(t, azRest("POST", strings.Replace(url, "?api-version=", "/enable?api-version=", 1), ""))
	runURL := strings.Replace(url, "?api-version=", "/triggers/manual/run?api-version=", 1)
	runCLI(t, azRest("POST", runURL, ""))
	runsURL := strings.Replace(url, "?api-version=", "/runs?api-version=", 1)
	out = runCLI(t, azRest("GET", runsURL, ""))
	if !strings.Contains(out, `"status": "Succeeded"`) {
		t.Fatalf("workflow run history did not include succeeded run: %s", out)
	}
}

func TestContainerInstancesCLI_GroupLogsLifecycle(t *testing.T) {
	url := armURL("Microsoft.ContainerInstance", "containerGroups/cli-aci", "2023-05-01")
	body := fmt.Sprintf(`{"location":"eastus","properties":{"osType":"Linux","restartPolicy":"Never","containers":[{"name":"main","properties":{"image":%q,"command":["/usr/local/bin/container-command","log","aci-cli"],"resources":{"requests":{"cpu":1,"memoryInGB":1}}}}]}}`, commandImageName)
	runCLI(t, azRest("PUT", url, body))

	logURL := strings.Replace(url, "?api-version=", "/containers/main/logs?api-version=", 1)
	// Generous deadline so a slow real-container start on a loaded CI runner
	// doesn't expire before logs are emitted; the loop breaks on first match.
	deadline := time.Now().Add(30 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		logs = runCLI(t, azRest("GET", logURL, ""))
		if strings.Contains(logs, "aci-cli") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !strings.Contains(logs, "aci-cli") {
		t.Fatalf("ACI logs missing real container output: %s", logs)
	}

	runCLI(t, azRest("DELETE", url, ""))
}
