package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func newACRTasksTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{
		Provider: "azure", ListenAddr: ":0", LogLevel: "error",
		Persist: true, DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	return srv
}

func acrTasksURL(reg Registry, suffix string) string {
	return fmt.Sprintf("http://%s%s%s?api-version=2019-06-01-preview", acrAuthTestARMHost, reg.ID, suffix)
}

// acrTasksARM sends an Azure Resource Manager request with the bearer a client
// acquires from the token endpoint.
func acrTasksARM(t *testing.T, srv *sim.Server, method, target, body string) (*http.Response, []byte) {
	t.Helper()
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return acrServe(t, srv, req)
}

func acrTasksPost(t *testing.T, srv *sim.Server, target, body string) (*http.Response, []byte) {
	t.Helper()
	return acrTasksARM(t, srv, http.MethodPost, target, body)
}

func acrTasksRegistry(t *testing.T, srv *sim.Server, name string) Registry {
	t.Helper()
	resp, data := acrTasksARM(t, srv, http.MethodPut,
		fmt.Sprintf("http://%s/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/%s?api-version=2023-07-01", acrAuthTestARMHost, name),
		`{"location":"eastus","sku":{"name":"Standard"},"properties":{}}`)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create registry %s: status %d: %s", name, resp.StatusCode, data)
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("decode registry: %v", err)
	}
	return reg
}

func acrTasksGetRun(t *testing.T, srv *sim.Server, reg Registry, runID string) acrRun {
	t.Helper()
	resp, body := acrTasksARM(t, srv, http.MethodGet, acrTasksURL(reg, "/runs/"+runID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get run %s: status %d: %s", runID, resp.StatusCode, body)
	}
	var run acrRun
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	return run
}

// acrTasksAwaitRun blocks until the run's worker has recorded its terminal
// status.
func acrTasksAwaitRun(runID string) {
	acrActiveRunsMu.Lock()
	active := acrActiveRuns[runID]
	acrActiveRunsMu.Unlock()
	if active != nil {
		<-active.done
	}
}

// The schedule call answers with the Run Queued and runs it in the
// background; the run then reads its terminal status, its error, and a log
// whose blob carries the `Complete` metadata the az CLI stops streaming on.
func TestACRScheduleRunQueuesAndRunsInTheBackground(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "tasksqueuereg")

	resp, body := acrTasksPost(t, srv, acrTasksURL(reg, "/scheduleRun"), `{
		"type": "DockerBuildRequest",
		"dockerFilePath": "Dockerfile",
		"imageNames": ["tasksqueuereg.azurecr.io/app:v1"],
		"sourceLocation": "https://nosuchacct.blob.core.windows.net/ctx/missing.tar.gz",
		"platform": {"os": "Linux"}
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scheduleRun: status %d: %s", resp.StatusCode, body)
	}
	var queued acrRun
	if err := json.Unmarshal(body, &queued); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if queued.Properties.Status != "Queued" {
		t.Fatalf("scheduleRun answered status %q, want Queued", queued.Properties.Status)
	}
	if queued.Properties.FinishTime != "" || queued.Properties.StartTime != "" {
		t.Fatalf("a queued run has neither started nor finished: %+v", queued.Properties)
	}

	acrTasksAwaitRun(queued.Properties.RunID)
	run := acrTasksGetRun(t, srv, reg, queued.Properties.RunID)
	if run.Properties.Status != "Failed" {
		t.Fatalf("run without its build context ended %q, want Failed", run.Properties.Status)
	}
	if !strings.Contains(run.Properties.RunErrorMessage, "not found") {
		t.Fatalf("runErrorMessage %q must say the build context was not found", run.Properties.RunErrorMessage)
	}
	if run.Properties.StartTime == "" || run.Properties.FinishTime == "" {
		t.Fatalf("a finished run reports when it started and finished: %+v", run.Properties)
	}

	resp, body = acrTasksPost(t, srv, acrTasksURL(reg, "/runs/"+run.Properties.RunID+"/listLogSasUrl"), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listLogSasUrl: status %d: %s", resp.StatusCode, body)
	}
	var sas struct {
		LogLink string `json:"logLink"`
	}
	if err := json.Unmarshal(body, &sas); err != nil {
		t.Fatalf("decode log link: %v", err)
	}
	link, err := url.Parse(sas.LogLink)
	if err != nil {
		t.Fatalf("parse log link %q: %v", sas.LogLink, err)
	}
	resp, _ = acrServe(t, srv, httptest.NewRequest(http.MethodHead, link.RequestURI(), nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log blob properties: status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("x-ms-meta-Complete"); got != "Failed" {
		t.Fatalf("log blob Complete metadata = %q, want Failed", got)
	}
}

func TestACRScheduleRunRefusesATimeoutOutOfRange(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "taskstimeoutreg")
	for _, timeout := range []int{299, 28801} {
		resp, body := acrTasksPost(t, srv, acrTasksURL(reg, "/scheduleRun"), fmt.Sprintf(`{
			"type": "DockerBuildRequest",
			"imageNames": ["taskstimeoutreg.azurecr.io/app:v1"],
			"sourceLocation": "https://acct.blob.core.windows.net/ctx/c.tar.gz",
			"timeout": %d
		}`, timeout))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("timeout %d: status %d, want 400: %s", timeout, resp.StatusCode, body)
		}
	}
	if runs := acrRuns.List(); len(runs) != 0 {
		t.Fatalf("a refused request schedules no run, found %d", len(runs))
	}
}

// acrTasksQueuedRun records a Queued run and the active entry a schedule call
// would register for it, so the worker can be driven directly.
func acrTasksQueuedRun(t *testing.T, reg Registry, runID string) (*acrActiveRun, context.Context) {
	t.Helper()
	acrRuns.Put(runID, acrRun{
		ID:   reg.ID + "/runs/" + runID,
		Name: runID,
		Type: "Microsoft.ContainerRegistry/registries/runs",
		Properties: acrRunProperties{
			RunID: runID, Status: "Queued", ProvisioningState: "Succeeded",
			RunType: "QuickBuild", CreateTime: acrNow(),
		},
	})
	stop, cancel := context.WithCancelCause(context.Background())
	active := &acrActiveRun{cancel: cancel, done: make(chan struct{})}
	acrActiveRunsMu.Lock()
	acrActiveRuns[runID] = active
	acrActiveRunsMu.Unlock()
	return active, stop
}

// acrTasksMissingContext is a docker build run whose source does not exist.
func acrTasksMissingContext(timeout time.Duration) acrRunSpec {
	return acrRunSpec{
		runType: "QuickBuild",
		timeout: timeout,
		source:  "https://nosuchacct.blob.core.windows.net/ctx/missing.tar.gz",
		docker:  &acrDockerBuildSpec{ImageNames: []string{"tasksreg.azurecr.io/app:v1"}, IsPushEnabled: true},
	}
}

// A run whose timeout passes ends Timeout, not Failed, whatever its build
// reported on the way out.
func TestACRRunPastItsTimeoutEndsTimeout(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "taskdeadlinereg")
	active, stop := acrTasksQueuedRun(t, reg, "cbdeadline")
	acrExecuteRun(context.Background(), stop, active, "cbdeadline", acrTasksMissingContext(1), reg)

	run, _ := acrRuns.Get("cbdeadline")
	if run.Properties.Status != "Timeout" {
		t.Fatalf("run past its timeout ended %q, want Timeout", run.Properties.Status)
	}
	if run.Properties.StartTime == "" {
		t.Fatal("a run that timed out had started")
	}
}

// A run canceled while Queued never starts; cancel of a run that has ended
// leaves its status alone; cancel of a run nobody scheduled is not found.
func TestACRCancelRun(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "taskcancelreg")

	active, stop := acrTasksQueuedRun(t, reg, "cbqueued")
	active.cancel(errACRRunCanceled)
	acrExecuteRun(context.Background(), stop, active, "cbqueued", acrTasksMissingContext(acrRunDefaultTimeoutSeconds*time.Second), reg)
	run := acrTasksGetRun(t, srv, reg, "cbqueued")
	if run.Properties.Status != "Canceled" {
		t.Fatalf("run canceled while queued ended %q, want Canceled", run.Properties.Status)
	}
	if run.Properties.StartTime != "" {
		t.Fatalf("a run canceled while queued never started, startTime %q", run.Properties.StartTime)
	}

	resp, body := acrTasksPost(t, srv, acrTasksURL(reg, "/runs/cbqueued/cancel"), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel of an ended run: status %d: %s", resp.StatusCode, body)
	}
	if got := acrTasksGetRun(t, srv, reg, "cbqueued").Properties.FinishTime; got != run.Properties.FinishTime {
		t.Fatalf("cancel of an ended run changed its finishTime from %q to %q", run.Properties.FinishTime, got)
	}

	resp, _ = acrTasksPost(t, srv, acrTasksURL(reg, "/runs/cbnobody/cancel"), "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel of an unknown run: status %d, want 404", resp.StatusCode)
	}
}

// A run a previous process left Queued or Running cannot resume, so a new
// process ends it Error.
func TestACRInterruptedRunsEndError(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "taskrestartreg")
	for id, status := range map[string]string{"cbrunning": "Running", "cbwaiting": "Queued", "cbdone": "Succeeded"} {
		acrRuns.Put(id, acrRun{ID: reg.ID + "/runs/" + id, Name: id,
			Properties: acrRunProperties{RunID: id, Status: status}})
	}
	acrFailInterruptedRuns()
	for id, want := range map[string]string{"cbrunning": "Error", "cbwaiting": "Error", "cbdone": "Succeeded"} {
		run, _ := acrRuns.Get(id)
		if run.Properties.Status != want {
			t.Errorf("run %s: status %q, want %q", id, run.Properties.Status, want)
		}
	}
}

// The log link answers the Blob service's ranged read with 206 and the range
// it served.
func TestACRRunLogServesRangedReads(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "tasklogreg")
	acrRuns.Put("cblog", acrRun{ID: reg.ID + "/runs/cblog", Name: "cblog",
		Properties: acrRunProperties{RunID: "cblog", Status: "Succeeded"}})
	acrRunLogs.Put("cblog", "step one\r\nstep two\r\n")

	req := httptest.NewRequest(http.MethodGet, "/acr/v1/logs/cblog", nil)
	req.Header.Set("x-ms-version", "2025-01-05")
	req.Header.Set("x-ms-range", "bytes=0-7")
	resp, body := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("ranged log read: status %d, want 206", resp.StatusCode)
	}
	if string(body) != "step one" {
		t.Fatalf("ranged log read = %q, want %q", body, "step one")
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 0-7/20" {
		t.Fatalf("Content-Range = %q, want bytes 0-7/20", got)
	}
	if got := resp.Header.Get("x-ms-meta-Complete"); got != "Succeeded" {
		t.Fatalf("Complete metadata = %q, want Succeeded", got)
	}

	resp, _ = acrServe(t, srv, httptest.NewRequest(http.MethodHead, "/acr/v1/logs/cbnolog", nil))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("log of a run that wrote none: status %d, want 404", resp.StatusCode)
	}
}
