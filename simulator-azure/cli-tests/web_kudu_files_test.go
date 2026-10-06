package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for Kudu's VFS, command and settings APIs on a web app's SCM
// site, and for a webjob's schedule and logs, reached with az rest and a
// Microsoft Entra token for App Service:
//
//	GET+PUT+DELETE /api/vfs/{path}
//	POST /api/command
//	GET+POST /api/settings
//	GET /api/settings/{key}
//	PUT /api/triggeredwebjobs/{job}/settings (schedule)
//	GET /api/triggeredwebjobs/{job}/history (output_url)
//	GET /vfs/{path}

func TestWebAppKudu_VFSCommandSettingsAndSchedule(t *testing.T) {
	stage3PullImage(t, stage3CLIAlpine)
	az := kuduCLILogin(t)
	rg, plan, app := "kudu-files-cli-rg", "kudu-files-cli-plan", "kudu-files-cli-app"
	runCLI(t, az.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, az.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "B1", "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=2025-03-01",
		az.baseURL, subscriptionID, rg, app)
	runCLI(t, az.command("rest", "--method", "PUT", "--url", siteURL, "--body", fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"serverFarmId": "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
			"reserved": true,
			"siteConfig": {"linuxFxVersion": "DOCKER|%s"}
		}
	}`, subscriptionID, rg, plan, stage3CLIAlpine), "-o", "json"))
	t.Cleanup(func() { runCLI(t, az.command("webapp", "delete", "-g", rg, "-n", app)) })
	scm := "https://" + kuduCLIScmHost(t, az, rg, app)
	kudu := func(method, path string, extra ...string) string {
		args := append([]string{"rest", "--method", method, "--url", scm + path,
			"--resource", "https://appservice.azure.com"}, extra...)
		return runCLI(t, az.command(args...))
	}

	kudu("put", "/api/vfs/site/wwwroot/hello.txt", "--body", "written through the vfs",
		"--headers", `{"Content-Type":"text/plain"}`)
	assert.Equal(t, "written through the vfs", strings.TrimSpace(kudu("get", "/api/vfs/site/wwwroot/hello.txt")))
	kudu("put", "/api/vfs/site/wwwroot/hello.txt", "--body", "rewritten",
		"--headers", `{"Content-Type":"text/plain","If-Match":"*"}`)
	var listing []struct {
		Name string `json:"name"`
		Size int    `json:"size"`
		Href string `json:"href"`
	}
	parseJSON(t, kudu("get", "/api/vfs/site/wwwroot/"), &listing)
	require.Len(t, listing, 1)
	assert.Equal(t, "hello.txt", listing[0].Name)
	assert.Equal(t, len("rewritten"), listing[0].Size)
	assert.Equal(t, scm+"/api/vfs/site/wwwroot/hello.txt", listing[0].Href)

	var result struct {
		Output   string `json:"Output"`
		Error    string `json:"Error"`
		ExitCode int    `json:"ExitCode"`
	}
	parseJSON(t, kudu("post", "/api/command", "--body", `{"command":"cat hello.txt && echo && pwd","dir":"site\\wwwroot"}`), &result)
	assert.Equal(t, "rewritten\n/home/site/wwwroot\n", result.Output)
	assert.Equal(t, 0, result.ExitCode)

	kudu("post", "/api/settings", "--body", `{"custom_key":"set through the settings api"}`)
	var settings map[string]string
	parseJSON(t, kudu("get", "/api/settings"), &settings)
	assert.Equal(t, "set through the settings api", settings["custom_key"])
	assert.Equal(t, "7200", settings["SCM_LOGSTREAM_TIMEOUT"])
	assert.Equal(t, `"set through the settings api"`, strings.TrimSpace(kudu("get", "/api/settings/custom_key")))

	kudu("put", "/api/triggeredwebjobs/ticker", "--body", "#!/bin/sh\necho scheduled-out\n",
		"--headers", `{"Content-Type":"text/plain","Content-Disposition":"attachment; filename=run.sh"}`)
	const schedule = "*/2 * * * * *"
	kudu("put", "/api/triggeredwebjobs/ticker/settings", "--body", `{"schedule":"`+schedule+`"}`)
	type run struct {
		Status    string `json:"status"`
		Trigger   string `json:"trigger"`
		OutputURL string `json:"output_url"`
	}
	var scheduled run
	stage3AwaitCLI(t, func() (string, bool) {
		var history struct {
			Runs []run `json:"runs"`
		}
		parseJSON(t, kudu("get", "/api/triggeredwebjobs/ticker/history"), &history)
		for _, r := range history.Runs {
			if r.Trigger == "Schedule - "+schedule && r.Status != "Running" {
				scheduled = r
				return r.Status, true
			}
		}
		return fmt.Sprintf("%d runs", len(history.Runs)), false
	}, "a scheduled run of the ticker job")
	assert.Equal(t, "Success", scheduled.Status)
	require.True(t, strings.HasPrefix(scheduled.OutputURL, scm+"/vfs/data/jobs/triggered/ticker/"), scheduled.OutputURL)
	assert.Contains(t, runCLI(t, az.command("rest", "--method", "get", "--url", scheduled.OutputURL,
		"--resource", "https://appservice.azure.com")), "INFO] scheduled-out")
	kudu("put", "/api/triggeredwebjobs/ticker/settings", "--body", `{}`)

	kudu("delete", "/api/vfs/site/wwwroot/hello.txt", "--headers", `{"If-Match":"*"}`)
	parseJSON(t, kudu("get", "/api/vfs/site/wwwroot/"), &listing)
	for _, e := range listing {
		assert.NotEqual(t, "hello.txt", e.Name, "the deleted file is gone")
	}
}
