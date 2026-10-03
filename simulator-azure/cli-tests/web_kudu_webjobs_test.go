package azure_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for Kudu's WebJobs API on a web app's SCM site. A job lands
// with the app's content through az webapp deploy, and az rest reaches the
// SCM site with a Microsoft Entra token for App Service:
//
//	GET /api/webjobs
//	GET /api/triggeredwebjobs
//	GET+PUT+DELETE /api/triggeredwebjobs/{job}
//	POST /api/triggeredwebjobs/{job}/run
//	GET /api/triggeredwebjobs/{job}/history
//	GET /api/triggeredwebjobs/{job}/history/{id}
//	GET+PUT /api/triggeredwebjobs/{job}/settings
//	GET /api/continuouswebjobs
//	GET /api/continuouswebjobs/{job}
//	POST /api/continuouswebjobs/{job}/start
//	POST /api/continuouswebjobs/{job}/stop

func TestWebAppKudu_WebJobsAPI(t *testing.T) {
	rg, app := "kudu-webjobs-cli-rg", "kudu-webjobs-cli-app"
	az, scmHost := kuduCLIWebApp(t, rg, "kudu-webjobs-cli-plan", app)
	scm := "https://" + scmHost
	kudu := func(method, path string, extra ...string) string {
		args := append([]string{"rest", "--method", method, "--url", scm + path,
			"--resource", "https://appservice.azure.com"}, extra...)
		return runCLI(t, az.command(args...))
	}

	pkg := filepath.Join(t.TempDir(), "jobs.zip")
	require.NoError(t, os.WriteFile(pkg, stage3Zip(t, map[string]string{
		"App_Data/jobs/triggered/report/run.sh":  "#!/bin/sh\necho report ran\n",
		"App_Data/jobs/continuous/worker/run.sh": "#!/bin/sh\nwhile true; do sleep 1; done\n",
	}), 0o644))
	runCLI(t, az.command("webapp", "deploy", "-g", rg, "-n", app, "--src-path", pkg, "--type", "zip", "-o", "json"))

	var all []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	parseJSON(t, kudu("get", "/api/webjobs"), &all)
	kinds := map[string]string{}
	for _, j := range all {
		kinds[j.Name] = j.Type
	}
	assert.Equal(t, map[string]string{"report": "triggered", "worker": "continuous"}, kinds)

	var triggered []struct {
		Name       string `json:"name"`
		RunCommand string `json:"run_command"`
		URL        string `json:"url"`
	}
	parseJSON(t, kudu("get", "/api/triggeredwebjobs"), &triggered)
	require.Len(t, triggered, 1)
	assert.Equal(t, "run.sh", triggered[0].RunCommand)
	assert.Equal(t, scm+"/api/triggeredwebjobs/report", triggered[0].URL)

	// A run; az rest prints nothing for its 202, so the history shows it.
	kudu("post", "/api/triggeredwebjobs/report/run")
	type run struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Trigger string `json:"trigger"`
	}
	var finished run
	stage3AwaitCLI(t, func() (string, bool) {
		var history struct {
			Runs []run `json:"runs"`
		}
		parseJSON(t, kudu("get", "/api/triggeredwebjobs/report/history"), &history)
		if len(history.Runs) == 0 {
			return "no runs", false
		}
		finished = history.Runs[0]
		return finished.Status, finished.Status != "Running"
	}, "the report run")
	assert.Equal(t, "Success", finished.Status)
	assert.True(t, strings.HasPrefix(finished.Trigger, "External - "), "trigger %q", finished.Trigger)
	var one run
	parseJSON(t, kudu("get", "/api/triggeredwebjobs/report/history/"+finished.ID), &one)
	assert.Equal(t, finished.ID, one.ID)

	kudu("put", "/api/triggeredwebjobs/report/settings", "--body", `{"is_singleton":true}`)
	assert.JSONEq(t, `{"is_singleton":true}`, kudu("get", "/api/triggeredwebjobs/report/settings"))

	// A job uploaded as its run file alone.
	var uploaded struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	parseJSON(t, kudu("put", "/api/triggeredwebjobs/cleanup", "--body", "#!/bin/sh\necho cleanup\n",
		"--headers", `{"Content-Type":"text/plain","Content-Disposition":"attachment; filename=run.sh"}`), &uploaded)
	assert.Equal(t, "cleanup", uploaded.Name)
	assert.Equal(t, "triggered", uploaded.Type)
	kudu("delete", "/api/triggeredwebjobs/cleanup")
	parseJSON(t, kudu("get", "/api/triggeredwebjobs"), &triggered)
	require.Len(t, triggered, 1, "the deleted job is gone")

	awaitStatus := func(want string) {
		stage3AwaitCLI(t, func() (string, bool) {
			var job struct {
				Status string `json:"status"`
			}
			parseJSON(t, kudu("get", "/api/continuouswebjobs/worker"), &job)
			return job.Status, job.Status == want
		}, "the worker's status "+want)
	}
	awaitStatus("Running")
	kudu("post", "/api/continuouswebjobs/worker/stop")
	awaitStatus("Stopped")
	kudu("post", "/api/continuouswebjobs/worker/start")
	awaitStatus("Running")
	var continuous []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	parseJSON(t, kudu("get", "/api/continuouswebjobs"), &continuous)
	require.Len(t, continuous, 1)
	assert.Equal(t, "worker", continuous[0].Name)
}
