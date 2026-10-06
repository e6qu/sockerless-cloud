package azure_sdk_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the rest of a web app's SCM site — Kudu's VFS, command,
// settings and log stream APIs — and for the logs and schedules of its
// webjobs:
//
//	GET+PUT+DELETE /api/vfs/{path}
//	GET /vfs/{path}
//	POST /api/command
//	GET+POST /api/settings
//	GET+DELETE /api/settings/{key}
//	GET /logstream
//	GET /api/triggeredwebjobs/{job} (scheduler_logs_url)
//	GET /api/triggeredwebjobs/{job}/history (output_url, error_url)
//	GET /api/continuouswebjobs/{job} (log_url)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/triggeredwebjobs/{webJobName}/history

const kuduAlpineImage = "public.ecr.aws/docker/library/alpine:latest"

// kuduSCM returns a site's SCM base URL and a Microsoft Entra bearer for App
// Service, the way az rest reaches the SCM site.
func kuduSCM(t *testing.T, client *armappservice.WebAppsClient, rg, name string) (string, func(*http.Request)) {
	t.Helper()
	site, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	var scm string
	for _, s := range site.Properties.HostNameSSLStates {
		if s.HostType != nil && *s.HostType == armappservice.HostTypeRepository {
			scm = "http://" + *s.Name
		}
	}
	require.NotEmpty(t, scm)
	token, _, err := fetchSimAccessToken("https://appservice.azure.com/.default")
	require.NoError(t, err)
	return scm, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

type kuduVFSEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
	Mime  string `json:"mime"`
	Href  string `json:"href"`
	Path  string `json:"path"`
}

type kuduCommandResult struct {
	Output   string `json:"Output"`
	Error    string `json:"Error"`
	ExitCode int    `json:"ExitCode"`
}

func TestSDK_WebApps_KuduVFSCommandAndSettings(t *testing.T) {
	pullImageWithRetry(t, kuduAlpineImage)
	rg, name := "sdk-kudu-vfs-rg", "sdk-kudu-vfs-app"
	client := createStackWebApp(t, rg, name, "DOCKER|"+kuduAlpineImage, map[string]string{"GREETING": "from-app-settings"})
	scm, auth := kuduSCM(t, client, rg, name)
	withHeader := func(k, v string) func(*http.Request) {
		return func(r *http.Request) { auth(r); r.Header.Set(k, v) }
	}

	resp, body := kuduRequest(t, http.MethodGet, scm+"/api/vfs/", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var root []kuduVFSEntry
	require.NoError(t, json.Unmarshal(body, &root), "%s", body)
	names := map[string]kuduVFSEntry{}
	for _, e := range root {
		names[e.Name] = e
	}
	require.Contains(t, names, "site")
	assert.Equal(t, "inode/directory", names["site"].Mime)
	assert.Equal(t, scm+"/api/vfs/site/", names["site"].Href)
	assert.Equal(t, "/home/site", names["site"].Path)

	resp, body = kuduRequest(t, http.MethodPut, scm+"/api/vfs/site/wwwroot/hello.txt", []byte("v1"), "", auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "%s", body)
	etag := resp.Header.Get("ETag")
	require.NotEmpty(t, etag)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/vfs/site/wwwroot/hello.txt", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "v1", string(body))
	assert.Equal(t, etag, resp.Header.Get("ETag"))
	assert.Equal(t, "text/plain", strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/api/vfs/site/wwwroot/hello.txt", nil, "", withHeader("If-None-Match", etag))
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)

	resp, _ = kuduRequest(t, http.MethodPut, scm+"/api/vfs/site/wwwroot/hello.txt", []byte("v2"), "", auth)
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode, "overwriting a file takes If-Match")
	resp, _ = kuduRequest(t, http.MethodPut, scm+"/api/vfs/site/wwwroot/hello.txt", []byte("v2"), "", withHeader("If-Match", `"0"`))
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode, "a stale ETag is refused")
	resp, body = kuduRequest(t, http.MethodPut, scm+"/api/vfs/site/wwwroot/hello.txt", []byte("v2"), "", withHeader("If-Match", etag))
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "%s", body)

	resp, body = kuduRequest(t, http.MethodGet, scm+"/vfs/site/wwwroot/", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var wwwroot []kuduVFSEntry
	require.NoError(t, json.Unmarshal(body, &wwwroot))
	require.Len(t, wwwroot, 1)
	assert.Equal(t, "hello.txt", wwwroot[0].Name)
	assert.Equal(t, int64(2), wwwroot[0].Size)
	assert.Equal(t, scm+"/vfs/site/wwwroot/hello.txt", wwwroot[0].Href)
	_, err := time.Parse(time.RFC3339Nano, wwwroot[0].Mtime)
	assert.NoError(t, err, "mtime %q", wwwroot[0].Mtime)

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scm+"/api/vfs/site/wwwroot", nil)
	require.NoError(t, err)
	auth(req)
	redirect, err := noRedirect.Do(req)
	require.NoError(t, err)
	redirect.Body.Close()
	assert.Equal(t, http.StatusTemporaryRedirect, redirect.StatusCode, "a directory without its trailing slash redirects")
	assert.Equal(t, scm+"/api/vfs/site/wwwroot/", redirect.Header.Get("Location"))

	resp, _ = kuduRequest(t, http.MethodPut, scm+"/api/vfs/data/scratch/", nil, "", auth)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	resp, _ = kuduRequest(t, http.MethodPut, scm+"/api/vfs/data/scratch/", nil, "", auth)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "the directory exists")

	// The command runs in the site's image with /home mounted and the app
	// settings in its environment; what it writes to wwwroot is the site's
	// content, so a webjob it writes there is discovered.
	command := func(cmd, dir string) (int, kuduCommandResult, string) {
		payload, err := json.Marshal(map[string]string{"command": cmd, "dir": dir})
		require.NoError(t, err)
		resp, body := kuduRequest(t, http.MethodPost, scm+"/api/command", payload, "application/json", auth)
		var out kuduCommandResult
		if resp.StatusCode == http.StatusOK {
			require.NoError(t, json.Unmarshal(body, &out), "%s", body)
		}
		return resp.StatusCode, out, string(body)
	}
	status, out, raw := command(`cat hello.txt && echo && echo "$GREETING" && echo to-stderr >&2 && mkdir -p App_Data/jobs/triggered/fromcmd && printf 'echo hi\n' > App_Data/jobs/triggered/fromcmd/run.sh`, `site\wwwroot`)
	require.Equal(t, http.StatusOK, status, raw)
	assert.Equal(t, "v2\nfrom-app-settings\n", out.Output)
	assert.Equal(t, "to-stderr\n", out.Error)
	assert.Equal(t, 0, out.ExitCode)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs/fromcmd", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the job the command wrote is discovered: %s", body)
	status, out, raw = command("pwd; exit 3", "data/scratch")
	require.Equal(t, http.StatusOK, status, raw)
	assert.Equal(t, "/home/data/scratch\n", out.Output)
	assert.Equal(t, 3, out.ExitCode)

	// Settings: Kudu's defaults under the settings written here, under the
	// app settings; SCM_COMMAND_IDLE_TIMEOUT bounds a silent command.
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/settings", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var settings map[string]string
	require.NoError(t, json.Unmarshal(body, &settings))
	assert.Equal(t, "60", settings["SCM_COMMAND_IDLE_TIMEOUT"])
	assert.Equal(t, "from-app-settings", settings["GREETING"])
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/settings", []byte(`{"SCM_COMMAND_IDLE_TIMEOUT":"2","custom_key":"v"}`), "application/json", auth)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "%s", body)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/settings/custom_key", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.JSONEq(t, `"v"`, string(body))
	status, _, raw = command("sleep 30", "")
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Contains(t, raw, "SCM_COMMAND_IDLE_TIMEOUT")
	resp, _ = kuduRequest(t, http.MethodDelete, scm+"/api/settings/SCM_COMMAND_IDLE_TIMEOUT", nil, "", auth)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/settings/SCM_COMMAND_IDLE_TIMEOUT", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `"60"`, string(body))
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/api/settings/no_such_setting", nil, "", auth)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, _ = kuduRequest(t, http.MethodDelete, scm+"/api/vfs/site/wwwroot/hello.txt", nil, "", auth)
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode, "deleting a file takes If-Match")
	resp, _ = kuduRequest(t, http.MethodDelete, scm+"/api/vfs/site/wwwroot/hello.txt", nil, "", withHeader("If-Match", "*"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/api/vfs/site/wwwroot/hello.txt", nil, "", auth)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = kuduRequest(t, http.MethodDelete, scm+"/api/vfs/site/wwwroot/App_Data/", nil, "", auth)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "a directory with files takes recursive=true")
	resp, _ = kuduRequest(t, http.MethodDelete, scm+"/api/vfs/site/wwwroot/App_Data/?recursive=true", nil, "", auth)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs/fromcmd", nil, "", auth)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "the job left with its files")
}

func TestSDK_WebApps_KuduLogStream(t *testing.T) {
	rg, name := "sdk-kudu-logstream-rg", "sdk-kudu-logstream-app"
	client := createStackWebApp(t, rg, name, "DOCKER|"+servingImageName, nil)
	scm, auth := kuduSCM(t, client, rg, name)

	streamCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, scm+"/logstream", nil)
	require.NoError(t, err)
	auth(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	lines := bufio.NewScanner(resp.Body)
	require.True(t, lines.Scan(), "the stream opens with its welcome line")
	assert.Contains(t, lines.Text(), "Welcome, you are now connected to log-streaming service.")

	// The serving workload logs one line per request it answers.
	status, body := azureSiteRequest(t, name, http.MethodGet, "/streamed-request", "")
	require.Equal(t, http.StatusOK, status, "%s", body)
	for lines.Scan() {
		if strings.HasSuffix(lines.Text(), " GET /streamed-request") {
			return
		}
	}
	t.Fatalf("the stream ended before the request's log line: %v", lines.Err())
}

func TestSDK_WebApps_KuduWebJobLogsAndSchedule(t *testing.T) {
	pullImageWithRetry(t, kuduAlpineImage)
	rg, name := "sdk-kudu-schedule-rg", "sdk-kudu-schedule-app"
	client := createStackWebApp(t, rg, name, "DOCKER|"+kuduAlpineImage, nil)
	scm, auth := kuduSCM(t, client, rg, name)

	upload := func(kind, job, script string) {
		req := func(r *http.Request) {
			auth(r)
			r.Header.Set("Content-Disposition", "attachment; filename=run.sh")
		}
		resp, body := kuduRequest(t, http.MethodPut, scm+"/api/"+kind+"webjobs/"+job, []byte(script), "text/plain", req)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	}
	upload("triggered", "ticker", "#!/bin/sh\necho scheduled-out\necho scheduled-err >&2\n")
	const schedule = "*/2 * * * * *"
	resp, body := kuduRequest(t, http.MethodPut, scm+"/api/triggeredwebjobs/ticker/settings",
		[]byte(`{"schedule":"`+schedule+`"}`), "application/json", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)

	type run struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Trigger   string `json:"trigger"`
		OutputURL string `json:"output_url"`
		ErrorURL  string `json:"error_url"`
	}
	type history struct {
		Runs []run `json:"runs"`
	}
	done := kuduAwait(t, scm+"/api/triggeredwebjobs/ticker/history", auth, func(h history) bool {
		for _, r := range h.Runs {
			if r.Trigger == "Schedule - "+schedule && r.Status != "Running" {
				return true
			}
		}
		return false
	})
	var scheduled run
	for _, r := range done.Runs {
		if r.Trigger == "Schedule - "+schedule && r.Status != "Running" {
			scheduled = r
			break
		}
	}
	assert.Equal(t, "Success", scheduled.Status)
	require.Equal(t, scm+"/vfs/data/jobs/triggered/ticker/"+scheduled.ID+"/output_log.txt", scheduled.OutputURL)
	resp, body = kuduRequest(t, http.MethodGet, scheduled.OutputURL, nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Contains(t, string(body), "INFO] scheduled-out")
	assert.Contains(t, string(body), "SYS INFO] Status changed to Success")
	require.NotEmpty(t, scheduled.ErrorURL)
	resp, body = kuduRequest(t, http.MethodGet, scheduled.ErrorURL, nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Contains(t, string(body), "ERR ] scheduled-err")

	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs/ticker", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var job struct {
		SchedulerLogsURL string `json:"scheduler_logs_url"`
	}
	require.NoError(t, json.Unmarshal(body, &job))
	require.Equal(t, scm+"/vfs/data/jobs/triggered/ticker/job_scheduler.log", job.SchedulerLogsURL)
	resp, body = kuduRequest(t, http.MethodGet, job.SchedulerLogsURL, nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.Contains(t, string(body), "Job schedule: "+schedule)

	// Azure Resource Manager names the same logs.
	armPager := client.NewListTriggeredWebJobHistoryPager(rg, name, "ticker", nil)
	var armOutputs []string
	for armPager.More() {
		page, err := armPager.NextPage(ctx)
		require.NoError(t, err)
		for _, h := range page.Value {
			for _, r := range h.Properties.Runs {
				if r.OutputURL != nil {
					armOutputs = append(armOutputs, *r.OutputURL)
				}
			}
		}
	}
	assert.Contains(t, armOutputs, scheduled.OutputURL)

	// A schedule that does not parse is the job's error, and runs nothing.
	resp, body = kuduRequest(t, http.MethodPut, scm+"/api/triggeredwebjobs/ticker/settings",
		[]byte(`{"schedule":"every minute"}`), "application/json", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	broken := kuduAwait(t, scm+"/api/triggeredwebjobs/ticker", auth, func(j kuduWebJob) bool { return j.Error != nil })
	assert.Contains(t, *broken.Error, "six")

	upload("continuous", "looper", "#!/bin/sh\necho looping\nwhile true; do sleep 1; done\n")
	worker := kuduAwait(t, scm+"/api/continuouswebjobs/looper", auth, func(j struct {
		Status string `json:"status"`
		LogURL string `json:"log_url"`
	}) bool {
		return j.Status == "Running" && j.LogURL != ""
	})
	require.Equal(t, scm+"/vfs/data/jobs/continuous/looper/job_log.txt", worker.LogURL)
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, body = kuduRequest(t, http.MethodGet, worker.LogURL, nil, "", auth)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
		if strings.Contains(string(body), "INFO] looping") {
			break
		}
		require.True(t, time.Now().Before(deadline), "the continuous job's output reaches its log: %s", body)
		time.Sleep(time.Second)
	}
	assert.Contains(t, string(body), "SYS INFO] Status changed to Running")
	resp, _ = kuduRequest(t, http.MethodPost, scm+"/api/continuouswebjobs/looper/stop", nil, "", auth)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
