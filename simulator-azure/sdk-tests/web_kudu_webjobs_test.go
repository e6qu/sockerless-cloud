package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for Kudu's WebJobs API on a web app's SCM site, which reads and
// writes the same jobs and run history as the Microsoft.Web webjob resources:
//
//	GET /api/webjobs
//	GET /api/triggeredwebjobs
//	GET+PUT+DELETE /api/triggeredwebjobs/{job}
//	POST /api/triggeredwebjobs/{job}/run
//	GET /api/triggeredwebjobs/{job}/history
//	GET /api/triggeredwebjobs/{job}/history/{id}
//	GET+PUT /api/triggeredwebjobs/{job}/settings
//	GET /api/continuouswebjobs
//	GET+PUT+DELETE /api/continuouswebjobs/{job}
//	POST /api/continuouswebjobs/{job}/start
//	POST /api/continuouswebjobs/{job}/stop

type kuduWebJob struct {
	Name           string         `json:"name"`
	Type           string         `json:"type"`
	RunCommand     string         `json:"run_command"`
	URL            string         `json:"url"`
	HistoryURL     string         `json:"history_url"`
	Status         string         `json:"status"`
	Error          *string        `json:"error"`
	Settings       map[string]any `json:"settings"`
	LatestRun      *kuduWebJobRun `json:"latest_run"`
	UsingSDK       bool           `json:"using_sdk"`
	DetailedStatus string         `json:"detailed_status"`
}

type kuduWebJobRun struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Duration  string `json:"duration"`
	URL       string `json:"url"`
	JobName   string `json:"job_name"`
	Trigger   string `json:"trigger"`
}

// kuduAwait polls a Kudu job URL until done reports the decoded body
// finished; Kudu exposes no long-poll for a job's state, so the test reads
// the job's own status field.
func kuduAwait[T any](t *testing.T, rawURL string, auth func(*http.Request), done func(T) bool) T {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		resp, body := kuduRequest(t, http.MethodGet, rawURL, nil, "", auth)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
		var v T
		require.NoError(t, json.Unmarshal(body, &v), "%s", body)
		if done(v) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not settle: %s", rawURL, body)
		}
		time.Sleep(time.Second)
	}
}

func TestSDK_WebApps_KuduWebJobsAPI(t *testing.T) {
	rg, name := "sdk-kudu-webjobs-rg", "sdk-kudu-webjobs-app"
	client := createStackWebApp(t, rg, name, "NODE|20-lts", nil)

	site, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	var scm string
	for _, s := range site.Properties.HostNameSSLStates {
		if s.HostType != nil && *s.HostType == armappservice.HostTypeRepository {
			scm = "http://" + *s.Name
		}
	}
	require.NotEmpty(t, scm)
	credsPoller, err := client.BeginListPublishingCredentials(ctx, rg, name, nil)
	require.NoError(t, err)
	creds, err := credsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	auth := func(r *http.Request) {
		r.SetBasicAuth(*creds.Properties.PublishingUserName, *creds.Properties.PublishingPassword)
	}

	resp, _ := kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs", nil, "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the webjobs API takes the SCM site's credentials")
	resp, body := kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.JSONEq(t, `[]`, string(body))

	// A triggered job uploaded as its run file alone.
	upload := func(kind, job string, data []byte, contentType, disposition string) (*http.Response, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, scm+"/api/"+kind+"/"+job, strings.NewReader(string(data)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", contentType)
		if disposition != "" {
			req.Header.Set("Content-Disposition", disposition)
		}
		auth(req)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var buf strings.Builder
		_, err = io.Copy(&buf, resp.Body)
		require.NoError(t, err)
		return resp, []byte(buf.String())
	}
	resp, body = upload("triggeredwebjobs", "report",
		[]byte("#!/bin/sh\necho report ran with $WEBJOBS_COMMAND_ARGUMENTS\n[ \"$1\" = one ] || exit 7\n"),
		"application/octet-stream", `attachment; filename="run.sh"`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var report kuduWebJob
	require.NoError(t, json.Unmarshal(body, &report))
	assert.Equal(t, "report", report.Name)
	assert.Equal(t, "triggered", report.Type)
	assert.Equal(t, "run.sh", report.RunCommand)
	assert.Equal(t, scm+"/api/triggeredwebjobs/report", report.URL)
	assert.Equal(t, scm+"/api/triggeredwebjobs/report/history", report.HistoryURL)
	assert.Nil(t, report.LatestRun, "a job that never ran has no latest run")

	resp, body = upload("triggeredwebjobs", "nameless", []byte("echo"), "application/octet-stream", "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "an upload that is no zip must name its file: %s", body)
	resp, body = upload("triggeredwebjobs", "norun", makeJobsZip(t, map[string]string{"helper.sh": "echo"}), "application/zip", "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a job needs a run file: %s", body)

	// A continuous job uploaded as a zip starts on its own.
	resp, body = upload("continuouswebjobs", "worker",
		makeJobsZip(t, map[string]string{"run.sh": "#!/bin/sh\nwhile true; do sleep 1; done\n"}), "application/zip", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	worker := kuduAwait(t, scm+"/api/continuouswebjobs/worker", auth, func(j kuduWebJob) bool { return j.Status == "Running" })
	assert.Equal(t, "continuous", worker.Type)
	assert.Equal(t, scm+"/api/continuouswebjobs/worker", worker.URL)

	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/webjobs", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var all []kuduWebJob
	require.NoError(t, json.Unmarshal(body, &all))
	names := map[string]string{}
	for _, j := range all {
		names[j.Name] = j.Type
	}
	assert.Equal(t, map[string]string{"report": "triggered", "worker": "continuous"}, names)

	// Azure Resource Manager reads the jobs Kudu placed.
	armJob, err := client.GetTriggeredWebJob(ctx, rg, name, "report", nil)
	require.NoError(t, err)
	assert.Equal(t, scm+"/api/triggeredwebjobs/report", *armJob.Properties.URL)
	armWorker, err := client.GetContinuousWebJob(ctx, rg, name, "worker", nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ContinuousWebJobStatusRunning, *armWorker.Properties.Status)

	// A run answers 202 with its history entry as Location; its arguments
	// reach the run file's command line.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scm+"/api/triggeredwebjobs/report/run?arguments=one", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "kudu-webjobs-test")
	auth(req)
	runResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	runResp.Body.Close()
	require.Equal(t, http.StatusAccepted, runResp.StatusCode)
	location := runResp.Header.Get("Location")
	require.True(t, strings.HasPrefix(location, scm+"/api/triggeredwebjobs/report/history/"), "Location %q", location)
	run := kuduAwait(t, location, auth, func(r kuduWebJobRun) bool { return r.Status != "Running" })
	assert.Equal(t, "Success", run.Status, "the run file exits 0 only when it receives the argument")
	assert.Equal(t, "report", run.JobName)
	assert.Equal(t, "External - kudu-webjobs-test", run.Trigger)
	assert.Equal(t, location, run.URL)
	assert.NotEmpty(t, run.EndTime)
	assert.Regexp(t, `^\d\d:\d\d:\d\d$`, run.Duration)

	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs/report/history", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var history struct {
		Runs []kuduWebJobRun `json:"runs"`
	}
	require.NoError(t, json.Unmarshal(body, &history))
	require.Len(t, history.Runs, 1)
	assert.Equal(t, run.ID, history.Runs[0].ID)
	armRun, err := client.GetTriggeredWebJobHistory(ctx, rg, name, "report", run.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.TriggeredWebJobStatusSuccess, *armRun.Properties.Runs[0].Status)
	report = kuduAwait(t, scm+"/api/triggeredwebjobs/report", auth, func(j kuduWebJob) bool { return j.LatestRun != nil })
	assert.Equal(t, run.ID, report.LatestRun.ID)

	// A job runs once at a time.
	resp, body = upload("triggeredwebjobs", "slow", []byte("#!/bin/sh\nsleep 300\n"), "text/plain", `attachment; filename=run.sh`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/triggeredwebjobs/slow/run", nil, "", auth)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, "%s", body)
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/triggeredwebjobs/slow/run", nil, "", auth)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "%s", body)
	assert.Contains(t, string(body), "already running")
	resp, _ = kuduRequest(t, http.MethodDelete, scm+"/api/triggeredwebjobs/slow", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// settings.job.
	resp, body = kuduRequest(t, http.MethodPut, scm+"/api/triggeredwebjobs/report/settings",
		[]byte(`{"is_singleton":true}`), "application/json", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs/report/settings", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	assert.JSONEq(t, `{"is_singleton":true}`, string(body))
	resp, body = kuduRequest(t, http.MethodGet, scm+"/api/triggeredwebjobs/report", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	require.NoError(t, json.Unmarshal(body, &report))
	assert.Equal(t, map[string]any{"is_singleton": true}, report.Settings)

	// Stop and start the continuous job.
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/continuouswebjobs/worker/stop", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	kuduAwait(t, scm+"/api/continuouswebjobs/worker", auth, func(j kuduWebJob) bool { return j.Status == "Stopped" })
	resp, body = kuduRequest(t, http.MethodPost, scm+"/api/continuouswebjobs/worker/start", nil, "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	kuduAwait(t, scm+"/api/continuouswebjobs/worker", auth, func(j kuduWebJob) bool { return j.Status == "Running" })

	// Deleting a job removes it from both APIs.
	for _, path := range []string{"/api/triggeredwebjobs/report", "/api/continuouswebjobs/worker"} {
		resp, body = kuduRequest(t, http.MethodDelete, scm+path, nil, "", auth)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
		resp, _ = kuduRequest(t, http.MethodGet, scm+path, nil, "", auth)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s is gone", path)
	}
	_, err = client.GetTriggeredWebJob(ctx, rg, name, "report", nil)
	require.Error(t, err)
	_, err = client.GetContinuousWebJob(ctx, rg, name, "worker", nil)
	require.Error(t, err)
}
