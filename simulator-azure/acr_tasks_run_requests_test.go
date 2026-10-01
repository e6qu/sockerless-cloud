package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func acrTestTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// The az CLI archives the source directory itself as an entry named "/".
	if err := tw.WriteHeader(&tar.Header{Name: "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func acrTestRunVariables() acrRunVariables {
	return acrRunVariables{
		ID: "cb12", SharedVolume: "acb_home_vol_x", Registry: "myreg.azurecr.io", RegistryName: "myreg",
		Date: time.Date(2026, 10, 1, 4, 5, 6, 0, time.UTC), OS: "linux", Architecture: "amd64", TaskName: "quickrun",
	}
}

// The source a client uploads to the URL listBuildSourceUploadUrl hands out is
// the source a run request names by the relative path: the URL takes the Blob
// service's Put Blob under its signature, and refuses a tampered one.
func TestACRBuildSourceUploadIsTheRunSource(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "tasksourcereg")

	resp, body := acrTasksPost(t, srv, acrTasksURL(reg, "/listBuildSourceUploadUrl"), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listBuildSourceUploadUrl: status %d: %s", resp.StatusCode, body)
	}
	var upload struct {
		UploadURL    string `json:"uploadUrl"`
		RelativePath string `json:"relativePath"`
	}
	if err := json.Unmarshal(body, &upload); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(upload.UploadURL)
	if err != nil {
		t.Fatalf("parse upload URL %q: %v", upload.UploadURL, err)
	}
	if !strings.HasSuffix(u.Path, "/"+upload.RelativePath) {
		t.Fatalf("upload URL %q does not end in the relative path %q", upload.UploadURL, upload.RelativePath)
	}
	archive := acrTestTarGz(t, map[string]string{"acb.yaml": "steps:\n  - cmd: alpine echo hi\n"})

	tampered := *u
	q := tampered.Query()
	q.Set("se", time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339))
	tampered.RawQuery = q.Encode()
	req := httptest.NewRequest(http.MethodPut, tampered.RequestURI(), bytes.NewReader(archive))
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("x-ms-version", "2025-01-05")
	resp, _ = acrServe(t, srv, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an upload whose signature does not cover its expiry: status %d, want 403", resp.StatusCode)
	}

	req = httptest.NewRequest(http.MethodPut, u.RequestURI(), bytes.NewReader(archive))
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("x-ms-version", "2025-01-05")
	resp, body = acrServe(t, srv, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Put Blob to the upload URL: status %d: %s", resp.StatusCode, body)
	}

	source, err := acrOpenSource(reg, upload.RelativePath)
	if err != nil {
		t.Fatalf("open the uploaded source by its relative path: %v", err)
	}
	got, err := io.ReadAll(source)
	_ = source.Close()
	if err != nil || !bytes.Equal(got, archive) {
		t.Fatalf("the run source is not what was uploaded (err %v)", err)
	}
	if _, err := acrOpenSource(reg, "source/202601010000/never-uploaded.tar.gz"); err == nil {
		t.Fatal("a relative path nobody uploaded to resolves to no source")
	}

	dir := t.TempDir()
	if err := acrExtractSource(bytes.NewReader(archive), dir); err != nil {
		t.Fatalf("unpack the az CLI's archive shape: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "acb.yaml")); err != nil {
		t.Fatalf("the task file was not unpacked: %v", err)
	}
}

func TestACRScheduleRunRefusesMalformedRequests(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "taskbadreqreg")
	cases := map[string]struct {
		body   string
		status int
	}{
		"unknown type":             {`{"type":"GitBuildRequest"}`, http.StatusBadRequest},
		"no type":                  {`{"encodedTaskContent":"c3RlcHM6IFtd"}`, http.StatusBadRequest},
		"encoded without content":  {`{"type":"EncodedTaskRunRequest","platform":{"os":"Linux"}}`, http.StatusBadRequest},
		"encoded not base64":       {`{"type":"EncodedTaskRunRequest","encodedTaskContent":"%%%","platform":{"os":"Linux"}}`, http.StatusBadRequest},
		"file without path":        {`{"type":"FileTaskRunRequest","sourceLocation":"source/x.tar.gz","platform":{"os":"Linux"}}`, http.StatusBadRequest},
		"task without id":          {`{"type":"TaskRunRequest"}`, http.StatusBadRequest},
		"task not there":           {fmt.Sprintf(`{"type":"TaskRunRequest","taskId":%q}`, reg.ID+"/tasks/nosuchtask"), http.StatusNotFound},
		"task of another registry": {`{"type":"TaskRunRequest","taskId":"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/other/tasks/t"}`, http.StatusBadRequest},
		"agent pool not there":     {`{"type":"EncodedTaskRunRequest","encodedTaskContent":"c3RlcHM6IFtd","agentPoolName":"nopool","platform":{"os":"Linux"}}`, http.StatusNotFound},
	}
	for name, tc := range cases {
		resp, body := acrTasksPost(t, srv, acrTasksURL(reg, "/scheduleRun"), tc.body)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d: %s", name, resp.StatusCode, tc.status, body)
		}
	}
	if runs := acrRuns.List(); len(runs) != 0 {
		t.Fatalf("a refused request schedules no run, found %d", len(runs))
	}
}

// A TaskRunRequest runs the Task's step with the request's overrides: its
// file, its arguments merged by name, and its values merged by name.
func TestACRTaskRunRequestAppliesOverrides(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "taskoverridereg")
	resp, body := acrTasksARM(t, srv, http.MethodPut, acrTasksURL(reg, "/tasks/build"), `{
		"location": "eastus",
		"properties": {
			"status": "Enabled",
			"platform": {"os": "Linux"},
			"timeout": 900,
			"step": {"type": "Docker", "dockerFilePath": "Dockerfile", "contextPath": "source/a.tar.gz",
				"imageNames": ["app:{{.Run.ID}}"], "arguments": [{"name": "A", "value": "1"}, {"name": "B", "value": "2"}]}
		}
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task: status %d: %s", resp.StatusCode, body)
	}
	spec, rerr := acrTaskRunSpec(acrTaskRunRequest{
		TaskID: reg.ID + "/tasks/build",
		OverrideTaskStepProperties: &acrOverrideTaskStepProperties{
			File: "other.Dockerfile", ContextPath: "source/b.tar.gz",
			Arguments: []acrArgument{{Name: "B", Value: "20"}, {Name: "C", Value: "3"}},
		},
	}, reg)
	if rerr != nil {
		t.Fatalf("resolve the task run: %v", rerr)
	}
	if spec.runType != "QuickBuild" || spec.taskName != "build" || spec.timeout != 900*time.Second || spec.source != "source/b.tar.gz" {
		t.Fatalf("task run spec %+v", spec)
	}
	want := []acrArgument{{Name: "A", Value: "1"}, {Name: "B", Value: "20"}, {Name: "C", Value: "3"}}
	if spec.docker == nil || spec.docker.DockerFilePath != "other.Dockerfile" || !reflect.DeepEqual(spec.docker.Arguments, want) {
		t.Fatalf("docker step after overrides: %+v", spec.docker)
	}

	names, err := acrRunImageNames([]string{"app:{{.Run.ID}}", "localhost:5000/x:1", "example.com/y"}, acrTestRunVariables())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"myreg.azurecr.io/app:cb12", "localhost:5000/x:1", "example.com/y"}) {
		t.Fatalf("image names %v", names)
	}

	acrTasksARM(t, srv, http.MethodPatch, acrTasksURL(reg, "/tasks/build"), `{"properties":{"status":"Disabled"}}`)
	resp, body = acrTasksPost(t, srv, acrTasksURL(reg, "/scheduleRun"),
		fmt.Sprintf(`{"type":"TaskRunRequest","taskId":%q}`, reg.ID+"/tasks/build"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("run of a disabled task: status %d: %s", resp.StatusCode, body)
	}
}

// A run scheduled on an agent pool waits Queued while every agent of the pool
// is busy, and listQueueStatus counts it until it leaves the queue.
func TestACRAgentPoolQueueStatusCountsQueuedRuns(t *testing.T) {
	srv := newACRTasksTestServer(t)
	reg := acrTasksRegistry(t, srv, "taskpoolreg")
	resp, body := acrTasksARM(t, srv, http.MethodPut, acrTasksURL(reg, "/agentPools/pool1"),
		`{"location":"eastus","properties":{"count":1,"tier":"S1","os":"Linux"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create agent pool: status %d: %s", resp.StatusCode, body)
	}
	queueStatus := func() int {
		t.Helper()
		resp, body := acrTasksPost(t, srv, acrTasksURL(reg, "/agentPools/pool1/listQueueStatus"), "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("listQueueStatus: status %d: %s", resp.StatusCode, body)
		}
		var status struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(body, &status); err != nil {
			t.Fatal(err)
		}
		return status.Count
	}
	if got := queueStatus(); got != 0 {
		t.Fatalf("an idle pool queues %d runs", got)
	}

	pool := reg.ID + "/agentPools/pool1"
	if err := acrAcquireAgent(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			acrReleaseAgent(pool)
		}
	})

	content := base64.StdEncoding.EncodeToString([]byte("steps:\n  - cmd: alpine true\n"))
	var runIDs []string
	for range 2 {
		resp, body = acrTasksPost(t, srv, acrTasksURL(reg, "/scheduleRun"), fmt.Sprintf(
			`{"type":"EncodedTaskRunRequest","encodedTaskContent":%q,"agentPoolName":"pool1","platform":{"os":"Linux"}}`, content))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("scheduleRun: status %d: %s", resp.StatusCode, body)
		}
		var run acrRun
		if err := json.Unmarshal(body, &run); err != nil {
			t.Fatal(err)
		}
		if run.Properties.AgentPoolName != "pool1" || run.Properties.RunType != "QuickRun" {
			t.Fatalf("queued run %+v", run.Properties)
		}
		runIDs = append(runIDs, run.Properties.RunID)
	}
	if got := queueStatus(); got != 2 {
		t.Fatalf("listQueueStatus = %d with two runs waiting for the pool's one agent, want 2", got)
	}

	for _, id := range runIDs {
		resp, body = acrTasksPost(t, srv, acrTasksURL(reg, "/runs/"+id+"/cancel"), "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("cancel: status %d: %s", resp.StatusCode, body)
		}
		if got := acrTasksGetRun(t, srv, reg, id).Properties; got.Status != "Canceled" || got.StartTime != "" {
			t.Fatalf("a run canceled while waiting for an agent: %+v", got)
		}
	}
	if got := queueStatus(); got != 0 {
		t.Fatalf("listQueueStatus = %d once the waiting runs were canceled, want 0", got)
	}
	acrReleaseAgent(pool)
	released = true

	resp, _ = acrTasksPost(t, srv, acrTasksURL(reg, "/agentPools/nopool/listQueueStatus"), "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("listQueueStatus of a pool that does not exist: status %d, want 404", resp.StatusCode)
	}
}

func TestACRTaskFileRendersValuesAndAliases(t *testing.T) {
	content := []byte(`version: v1.1.0
alias:
  values:
    repo: team/app
stepTimeout: 120
env: ["A=task", "B=task"]
steps:
  - build: -t $Registry/$repo:{{.Values.tag}} -f Dockerfile .
    cache: disabled
  - push: ["$Registry/$repo:{{.Values.tag}}"]
  - id: check
    cmd: bash echo $ID {{.Run.RegistryName}} {{.Values.secret}} $HOME $$ID {{.Values.missing}}
    env: ["B=step"]
    timeout: 30
    when: ["-"]
  - cmd: alpine true
    when: [check, acb_step_0]
`)
	values := []byte("tag: v1\nsecret: from-file\n")
	task, err := acrLoadTaskFile(content, values, []acrSetValue{{Name: "secret", Value: "s3cr3t", IsSecret: true}}, acrTestRunVariables())
	if err != nil {
		t.Fatalf("load the task file: %v", err)
	}
	if got := task.Steps[0].Build; got != "-t myreg.azurecr.io/team/app:v1 -f Dockerfile ." {
		t.Fatalf("build step %q", got)
	}
	if got := []string(task.Steps[1].Push); !reflect.DeepEqual(got, []string{"myreg.azurecr.io/team/app:v1"}) {
		t.Fatalf("push step %v", got)
	}
	check := task.Steps[2]
	if want := "mcr.microsoft.com/acr/bash:9fb281c echo cb12 myreg s3cr3t $HOME $ID"; check.Cmd != want {
		t.Fatalf("cmd step %q, want %q", check.Cmd, want)
	}
	if !reflect.DeepEqual(check.Env, []string{"B=step", "A=task"}) || check.Timeout != 30 || len(check.deps) != 0 {
		t.Fatalf("check step %+v", check)
	}
	if task.Steps[0].Timeout != 120 || task.Steps[0].ID != "acb_step_0" || len(task.Steps[0].deps) != 0 {
		t.Fatalf("first step %+v", task.Steps[0])
	}
	if got := task.Steps[1].deps; !reflect.DeepEqual(got, []string{"acb_step_0"}) {
		t.Fatalf("a step without when follows the previous one, deps %v", got)
	}
	if got := task.Steps[3].deps; !reflect.DeepEqual(got, []string{"check", "acb_step_0"}) {
		t.Fatalf("deps %v", got)
	}

	// Before v1.1.0 there are no aliases: `$Registry` stays as written.
	old, err := acrLoadTaskFile([]byte("steps:\n  - cmd: bash echo $Registry\n"), nil, nil, acrTestRunVariables())
	if err != nil {
		t.Fatal(err)
	}
	if old.Steps[0].Cmd != "bash echo $Registry" || old.Version != acrTaskDefaultVersion {
		t.Fatalf("a v1.0.0 task file expanded aliases: %+v", old.Steps[0])
	}
}

func TestACRTaskFileRefusesInvalidTasks(t *testing.T) {
	for name, content := range map[string]string{
		"no steps":         "version: v1.1.0\n",
		"two kinds":        "steps:\n  - cmd: alpine true\n    build: .\n",
		"duplicate id":     "steps:\n  - id: a\n    cmd: alpine true\n  - id: a\n    cmd: alpine true\n",
		"unknown when":     "steps:\n  - cmd: alpine true\n    when: [nope]\n",
		"cycle":            "steps:\n  - id: a\n    cmd: alpine true\n    when: [b]\n  - id: b\n    cmd: alpine true\n    when: [a]\n",
		"unknown property": "steps:\n  - cmd: alpine true\n    colour: red\n",
		"bad cache":        "steps:\n  - build: .\n    cache: sometimes\n",
		"key vault secret": "secrets:\n  - id: s\n    keyvault: https://v.vault.azure.net/secrets/s\nsteps:\n  - cmd: alpine true\n",
		"step network":     "steps:\n  - cmd: alpine true\n    network: mine\n",
		"push detached":    "steps:\n  - push: [x]\n    detach: true\n",
		"space in id":      "steps:\n  - id: a b\n    cmd: alpine true\n",
		"negative retries": "steps:\n  - cmd: alpine true\n    retries: -1\n",
	} {
		if _, err := acrLoadTaskFile([]byte(content), nil, nil, acrTestRunVariables()); err == nil {
			t.Errorf("%s: the task file loaded", name)
		}
	}
}

func TestACRSplitArgs(t *testing.T) {
	got, err := acrSplitArgs(`alpine sh -c "echo \"hi there\" && ls" 'a b' c\ d`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpine", "sh", "-c", `echo "hi there" && ls`, "a b", "c d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("split %q, want %q", got, want)
	}
	if _, err := acrSplitArgs(`echo "open`); err == nil {
		t.Fatal("an unterminated quote is refused")
	}
	if tags := acrBuildTags([]string{"-t", "a:1", "--tag=b", "-tc", "-f", "x", "."}); !reflect.DeepEqual(tags, []string{"a:1", "b", "c"}) {
		t.Fatalf("tags %v", tags)
	}
}
