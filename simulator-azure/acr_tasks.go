package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/rs/zerolog"
)

// ACR Tasks quick-build slice. A client builds an image through the real ACR
// Tasks API — RegistriesClient.BeginScheduleRun with a DockerBuildRequest,
// which answers with the Run Queued, then RunsClient.Get until the Run is
// terminal. This implements
// that slice at cloud-API fidelity: the build context is fetched from the
// sim's blob storage (where the backend's azblob upload landed), the
// docker build runs on the host engine — the sim's build infrastructure,
// exactly as the GCP Cloud Build slice (simulator-gcp/cloudbuild.go) does
// — and the resulting image lands in the local daemon tagged with the ACR
// image name, ready for sim.StartContainerSync to run. There is no
// consumer-aware special-casing: any ACR Tasks client (SDK / az acr
// build / terraform) issuing a docker-build run reaches the same path.
//
// Real API: https://learn.microsoft.com/en-us/rest/api/containerregistry/registries/schedule-run

type acrRun struct {
	ID         string           `json:"id,omitempty"`
	Name       string           `json:"name,omitempty"`
	Type       string           `json:"type,omitempty"`
	Properties acrRunProperties `json:"properties"`
}

type acrRunProperties struct {
	RunID             string               `json:"runId"`
	Status            string               `json:"status"`
	ProvisioningState string               `json:"provisioningState"`
	RunType           string               `json:"runType,omitempty"`
	CreateTime        string               `json:"createTime,omitempty"`
	StartTime         string               `json:"startTime,omitempty"`
	FinishTime        string               `json:"finishTime,omitempty"`
	LastUpdatedTime   string               `json:"lastUpdatedTime,omitempty"`
	OutputImages      []acrImageDescriptor `json:"outputImages,omitempty"`
	Platform          *acrPlatform         `json:"platform,omitempty"`
	RunErrorMessage   string               `json:"runErrorMessage,omitempty"`
	IsArchiveEnabled  bool                 `json:"isArchiveEnabled"`
}

type acrImageDescriptor struct {
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
}

type acrPlatform struct {
	OS           string `json:"os,omitempty"`
	Architecture string `json:"architecture,omitempty"`
}

type acrArgument struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

// acrDockerBuildRequest is the subset of the ACR Tasks DockerBuildRequest
// the sim honors. `type` discriminates the run-request union; sockerless
// only issues DockerBuildRequest.
type acrDockerBuildRequest struct {
	Type           string        `json:"type"`
	DockerFilePath string        `json:"dockerFilePath"`
	ImageNames     []string      `json:"imageNames"`
	SourceLocation string        `json:"sourceLocation"`
	IsPushEnabled  *bool         `json:"isPushEnabled"`
	NoCache        bool          `json:"noCache"`
	Arguments      []acrArgument `json:"arguments"`
	Target         string        `json:"target"`
	Platform       *acrPlatform  `json:"platform"`
	Timeout        *int32        `json:"timeout"`
}

// The DockerBuildRequest `timeout` bounds, in seconds, from the registry tasks
// specification.
const (
	acrRunDefaultTimeoutSeconds = 3600
	acrRunMinTimeoutSeconds     = 300
	acrRunMaxTimeoutSeconds     = 28800
)

var (
	errACRRunCanceled = errors.New("the run was canceled")
	errACRRunTimedOut = errors.New("the run exceeded its timeout")
)

// acrActiveRun is a run this process is executing: what stops it, what closes
// once its terminal status is recorded, and the log it is writing.
type acrActiveRun struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
	log    *acrRunLog
}

// acrRunLog is the log of a running run, which the log link serves while the
// run is still writing it.
type acrRunLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *acrRunLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *acrRunLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

var (
	acrRuns        sim.Store[acrRun]
	acrRunLogs     sim.Store[string]
	acrTasksLogger zerolog.Logger
	acrTasksServer *sim.Server

	acrActiveRunsMu sync.Mutex
	acrActiveRuns   = map[string]*acrActiveRun{}
)

func acrRunFinished(status string) bool {
	switch status {
	case "Succeeded", "Failed", "Canceled", "Error", "Timeout":
		return true
	}
	return false
}

// acrRunLogCompletion is the `Complete` metadata a run's log blob gains when
// the run ends. The az CLI streams the log until the metadata appears and
// reads the run's outcome from its value.
func acrRunLogCompletion(status string) string {
	switch status {
	case "Timeout":
		return "TimedOut"
	case "Error":
		return "InternalError"
	}
	return status
}

func registerACRTasks(srv *sim.Server) {
	acrRuns = sim.MakeStore[acrRun](srv.DB(), "acr_runs")
	acrRunLogs = sim.MakeStore[string](srv.DB(), "acr_run_logs")
	acrTasksLogger = srv.Logger()
	acrTasksServer = srv
	acrActiveRunsMu.Lock()
	acrActiveRuns = map[string]*acrActiveRun{}
	acrActiveRunsMu.Unlock()
	acrFailInterruptedRuns()

	// The run-log blob listLogSasUrl advertises. ACR serves a run's log as a
	// blob at a SAS URL that grows while the run writes it and gains
	// `Complete` metadata when the run ends; `az acr build` and `az acr task
	// logs` stream it with the Blob service's get-properties and ranged reads.
	// The GET pattern serves HEAD as well.
	srv.HandleFunc("GET /acr/v1/logs/{runId}", handleACRRunLog)
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerRegistry"
	// scheduleRun / runs / the task-children action verbs are not in the
	// path-normalization allowlist, so they are registered with the exact
	// casing the SDK emits.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/scheduleRun", handleACRScheduleRun)
	srv.HandleFunc("GET "+armBase+"/registries/{registryName}/runs/{runId}", handleACRGetRun)

	// Registry-tasks children (Microsoft.ContainerRegistry/registries/<x>):
	// tasks and agentPools are tracked resources (Resource base — location +
	// tags); taskRuns is a proxy resource that additionally carries location +
	// identity. They share the generic CRUD shape with the registry children.
	const t = "Microsoft.ContainerRegistry/registries/"
	tasks := sim.MakeStore[acrSubResource](srv.DB(), "acr_tasks")
	taskRuns := sim.MakeStore[acrSubResource](srv.DB(), "acr_task_runs")
	agentPools := sim.MakeStore[acrSubResource](srv.DB(), "acr_agent_pools")
	registerACRChild(srv, acrChildKind{
		seg: "tasks", nameParam: "taskName", typeName: t + "tasks",
		allowLocation: true, allowTags: true, allowIdentity: true, patch: true, store: tasks,
	})
	registerACRChild(srv, acrChildKind{
		seg: "taskRuns", nameParam: "taskRunName", typeName: t + "taskRuns",
		allowLocation: true, allowIdentity: true, patch: true, store: taskRuns,
	})
	registerACRChild(srv, acrChildKind{
		seg: "agentPools", nameParam: "agentPoolName", typeName: t + "agentPools",
		allowLocation: true, allowTags: true, patch: true, store: agentPools,
	})

	// POST .../tasks/{name}/listDetails — returns the Task including its
	// credentials. The sim stores no secret credentials, so this returns the
	// stored task resource as-is.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/tasks/{taskName}/listDetails", func(w http.ResponseWriter, r *http.Request) {
		res, ok := tasks.Get(acrChildResourceID(r, "tasks", "taskName"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Task %q not found.", sim.PathParam(r, "taskName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// POST .../taskRuns/{name}/listDetails — returns the TaskRun with results.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/taskRuns/{taskRunName}/listDetails", func(w http.ResponseWriter, r *http.Request) {
		res, ok := taskRuns.Get(acrChildResourceID(r, "taskRuns", "taskRunName"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "TaskRun %q not found.", sim.PathParam(r, "taskRunName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// POST .../agentPools/{name}/listQueueStatus — number of queued runs.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/agentPools/{agentPoolName}/listQueueStatus", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"count": 0})
	})

	// GET .../runs — list runs scheduled against the registry.
	srv.HandleFunc("GET "+armBase+"/registries/{registryName}/runs", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		prefix := regID + "/runs/"
		matched := acrRuns.Filter(func(run acrRun) bool { return strings.HasPrefix(run.ID, prefix) })
		if matched == nil {
			matched = []acrRun{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": matched})
	})

	// PATCH .../runs/{runId} — update mutable run properties (isArchiveEnabled).
	srv.HandleFunc("PATCH "+armBase+"/registries/{registryName}/runs/{runId}", func(w http.ResponseWriter, r *http.Request) {
		runID := sim.PathParam(r, "runId")
		run, ok := acrRuns.Get(runID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", runID)
			return
		}
		var req struct {
			IsArchiveEnabled *bool `json:"isArchiveEnabled"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "failed to parse run update: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.IsArchiveEnabled != nil {
			acrRuns.Update(runID, func(stored *acrRun) {
				stored.Properties.IsArchiveEnabled = *req.IsArchiveEnabled
				stored.Properties.LastUpdatedTime = acrNow()
			})
			run, _ = acrRuns.Get(runID)
		}
		sim.WriteJSON(w, http.StatusOK, run)
	})

	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/runs/{runId}/cancel", handleACRCancelRun)

	// POST .../runs/{runId}/listLogSasUrl — link to the run's build logs.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/runs/{runId}/listLogSasUrl", func(w http.ResponseWriter, r *http.Request) {
		runID := sim.PathParam(r, "runId")
		// A link to the logs of a run that never happened leads nowhere: the
		// endpoint it points at answers 404, so handing the link out reports a
		// log that is not there. Only the run is checked, because scheduling
		// one does not require the registry resource to exist either — the
		// build runs against the registry's login server, and holding the log
		// link to a stricter rule than the run itself would refuse a link to a
		// log that is there.
		if _, ok := acrRuns.Get(runID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The run %q was not found.", runID)
			return
		}
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"logLink": fmt.Sprintf("%s://%s%s%s", scheme, r.Host, acrRunLogPathPrefix, runID),
		})
	})

	// POST .../listBuildSourceUploadUrl — where a client uploads a build
	// context tar before scheduling a run.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/listBuildSourceUploadUrl", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := acrRegistryID(r); !ok {
			acrRegistryNotFound(w, r)
			return
		}
		rel := "source/" + sim.NewUUID() + ".tar.gz"
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"uploadUrl":    fmt.Sprintf("%s://%s/acr/v1/build-source/%s", scheme, r.Host, rel),
			"relativePath": rel,
		})
	})
}

// acrChildResourceID builds the ARM resource ID for a registry child given its
// path segment and name route param.
func acrChildResourceID(r *http.Request, seg, nameParam string) string {
	regID, _ := acrRegistryID(r)
	return fmt.Sprintf("%s/%s/%s", regID, seg, sim.PathParam(r, nameParam))
}

func acrNow() string { return time.Now().UTC().Format(time.RFC3339) }

// handleACRScheduleRun records the Run Queued and answers with it at once, as
// the service does: the build runs in the background, and a client follows it
// through Runs_Get or the run's log. The Registries_ScheduleRun the Azure CLI's
// SDK calls accepts only a 200, and the Go SDK's poller completes on a 200
// that names no operation to poll.
func handleACRScheduleRun(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	registry := sim.PathParam(r, "registryName")

	var req acrDockerBuildRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "failed to parse run request: "+err.Error(), http.StatusBadRequest)
		return
	}

	regID, _ := acrRegistryID(r)
	reg, ok := acrRegistries.Get(regID)
	if !ok {
		AzureError(w, "ResourceNotFound", fmt.Sprintf("The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.", registry, rg), http.StatusNotFound)
		return
	}

	timeoutSeconds := int32(acrRunDefaultTimeoutSeconds)
	if req.Timeout != nil {
		if *req.Timeout < acrRunMinTimeoutSeconds || *req.Timeout > acrRunMaxTimeoutSeconds {
			AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest,
				"The run timeout %d is out of range: it must be between %d and %d seconds.",
				*req.Timeout, acrRunMinTimeoutSeconds, acrRunMaxTimeoutSeconds)
			return
		}
		timeoutSeconds = *req.Timeout
	}

	runID := "cb" + sim.NewUUID()[:8]
	now := acrNow()
	run := acrRun{
		ID:   fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s/runs/%s", sub, rg, registry, runID),
		Name: runID,
		Type: "Microsoft.ContainerRegistry/registries/runs",
		Properties: acrRunProperties{
			RunID:             runID,
			Status:            "Queued",
			ProvisioningState: "Succeeded",
			RunType:           "QuickBuild",
			CreateTime:        now,
			LastUpdatedTime:   now,
			Platform:          req.Platform,
		},
	}
	acrRuns.Put(runID, run)
	acrStartRun(runID, req, reg, time.Duration(timeoutSeconds)*time.Second)
	sim.WriteJSON(w, http.StatusOK, run)
}

// acrStartRun registers the run as active before its worker starts, so a
// cancel that arrives the moment the schedule call returns finds it.
func acrStartRun(runID string, req acrDockerBuildRequest, reg Registry, timeout time.Duration) {
	stop, cancel := context.WithCancelCause(context.Background())
	active := &acrActiveRun{cancel: cancel, done: make(chan struct{})}
	acrActiveRunsMu.Lock()
	acrActiveRuns[runID] = active
	acrActiveRunsMu.Unlock()
	acrTasksServer.StartBackground("acr-tasks-run", func(serverCtx context.Context) {
		acrExecuteRun(serverCtx, stop, active, runID, req, reg, timeout)
	})
}

// acrExecuteRun moves a run from Queued to Running, builds, and records the
// terminal status: Succeeded, Failed, Canceled when Runs_Cancel stopped it,
// Timeout when it outlived its timeout, and Error when the simulator shut
// down under it.
func acrExecuteRun(serverCtx, stop context.Context, active *acrActiveRun, runID string, req acrDockerBuildRequest, reg Registry, timeout time.Duration) {
	defer func() {
		active.cancel(nil)
		acrActiveRunsMu.Lock()
		if acrActiveRuns[runID] == active {
			delete(acrActiveRuns, runID)
		}
		acrActiveRunsMu.Unlock()
		close(active.done)
	}()

	runCtx, cancelRun := context.WithCancelCause(serverCtx)
	defer cancelRun(nil)
	unlink := context.AfterFunc(stop, func() { cancelRun(context.Cause(stop)) })
	defer unlink()

	if context.Cause(stop) != nil || serverCtx.Err() != nil {
		acrFinishRun(runID, acrRunOutcome(serverCtx, context.Cause(stop), nil))
		return
	}
	log := &acrRunLog{}
	acrActiveRunsMu.Lock()
	active.log = log
	acrActiveRunsMu.Unlock()
	acrRuns.Update(runID, func(run *acrRun) {
		now := acrNow()
		run.Properties.Status = "Running"
		run.Properties.StartTime = now
		run.Properties.LastUpdatedTime = now
	})

	buildCtx, cancelBuild := context.WithTimeoutCause(runCtx, timeout, errACRRunTimedOut)
	defer cancelBuild()
	buildErr := executeACRBuild(buildCtx, req, reg, runID, log)
	acrRunLogs.Put(runID, log.String())

	outcome := acrRunOutcome(serverCtx, context.Cause(buildCtx), buildErr)
	if outcome.status == "Succeeded" && req.IsPushEnabledOrDefault() {
		for _, img := range req.ImageNames {
			outcome.outputImages = append(outcome.outputImages, parseACRImage(img))
		}
	}
	if outcome.status == "Failed" {
		output := log.String()
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		acrTasksLogger.Error().Str("runId", runID).Strs("images", req.ImageNames).
			Str("output", output).Err(buildErr).Msg("ACR Task build failed")
	}
	acrFinishRun(runID, outcome)
}

type acrOutcome struct {
	status       string
	message      string
	outputImages []acrImageDescriptor
}

// acrRunOutcome names the terminal status of a run whose work ended with err,
// cause being why its context ended, if it did.
func acrRunOutcome(serverCtx context.Context, cause, err error) acrOutcome {
	switch {
	case errors.Is(cause, errACRRunCanceled):
		return acrOutcome{status: "Canceled"}
	case errors.Is(cause, errACRRunTimedOut):
		return acrOutcome{status: "Timeout", message: errACRRunTimedOut.Error()}
	case serverCtx.Err() != nil:
		return acrOutcome{status: "Error", message: "The run was interrupted by a service restart before it completed."}
	case err != nil:
		return acrOutcome{status: "Failed", message: err.Error()}
	}
	return acrOutcome{status: "Succeeded"}
}

func acrFinishRun(runID string, outcome acrOutcome) {
	acrRuns.Update(runID, func(run *acrRun) {
		now := acrNow()
		run.Properties.Status = outcome.status
		run.Properties.RunErrorMessage = outcome.message
		run.Properties.OutputImages = outcome.outputImages
		run.Properties.FinishTime = now
		run.Properties.LastUpdatedTime = now
	})
}

// acrFailInterruptedRuns ends every run a previous process left Queued or
// Running: its execution died with that process and cannot resume.
func acrFailInterruptedRuns() {
	for _, run := range acrRuns.Filter(func(run acrRun) bool { return !acrRunFinished(run.Properties.Status) }) {
		acrRuns.Update(run.Properties.RunID, func(run *acrRun) {
			if acrRunFinished(run.Properties.Status) {
				return
			}
			now := acrNow()
			run.Properties.Status = "Error"
			run.Properties.RunErrorMessage = "The run was interrupted by a service restart before it completed."
			run.Properties.FinishTime = now
			run.Properties.LastUpdatedTime = now
		})
	}
}

// handleACRCancelRun stops a Queued or Running run and answers once the run
// has stopped and reads Canceled. A run that already ended keeps its status.
func handleACRCancelRun(w http.ResponseWriter, r *http.Request) {
	runID := sim.PathParam(r, "runId")
	if _, ok := acrRuns.Get(runID); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", runID)
		return
	}
	acrActiveRunsMu.Lock()
	active := acrActiveRuns[runID]
	acrActiveRunsMu.Unlock()
	if active != nil {
		active.cancel(errACRRunCanceled)
		select {
		case <-active.done:
		case <-r.Context().Done():
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

const acrRunLogPathPrefix = "/acr/v1/logs/"

func acrIsRunLogPath(path string) bool {
	return strings.HasPrefix(path, acrRunLogPathPrefix) && !strings.Contains(path[len(acrRunLogPathPrefix):], "/")
}

// handleACRRunLog serves a run's log the way the Blob service serves the log
// blob: absent until the run starts writing it, growing while it runs, with
// ranged reads, and carrying `Complete` metadata once the run has ended.
func handleACRRunLog(w http.ResponseWriter, r *http.Request) {
	runID := sim.PathParam(r, "runId")
	acrActiveRunsMu.Lock()
	var live *acrRunLog
	if active := acrActiveRuns[runID]; active != nil {
		live = active.log
	}
	acrActiveRunsMu.Unlock()

	var content string
	complete := ""
	if live != nil {
		content = live.String()
	} else {
		stored, ok := acrRunLogs.Get(runID)
		if !ok {
			writeStorageError(w, "BlobNotFound", "The specified blob does not exist.", http.StatusNotFound)
			return
		}
		content = stored
		if run, ok := acrRuns.Get(runID); ok && acrRunFinished(run.Properties.Status) {
			complete = acrRunLogCompletion(run.Properties.Status)
		}
	}

	size := int64(len(content))
	start, end, partial, ok := azureStorageReadRange(w, r, size)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Accept-Ranges", "bytes")
	if complete != "" {
		w.Header().Set("x-ms-meta-Complete", complete)
	}
	azureStorageServeRead(w, r, strings.NewReader(content), size, start, end, partial)
}

func handleACRGetRun(w http.ResponseWriter, r *http.Request) {
	runID := sim.PathParam(r, "runId")
	run, ok := acrRuns.Get(runID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", runID)
		return
	}
	sim.WriteJSON(w, http.StatusOK, run)
}

// executeACRBuild fetches the build context the backend uploaded to sim
// blob storage, runs `docker build` against the host engine, then — exactly
// as real ACR Tasks with IsPushEnabled does — `docker push`es the result to
// the registry and removes the local copy. The registry, not the build
// host, is the source of truth; the workload later pulls the image from the
// registry over the standard /v2/ API. The build context is a gzipped tar
// (Dockerfile + COPY'd files) streamed to `docker build -` on stdin, and the
// docker output goes to runLog as it is written.
func executeACRBuild(ctx context.Context, req acrDockerBuildRequest, reg Registry, runID string, runLog io.Writer) error {
	logf := func(format string, args ...any) {
		_, _ = fmt.Fprintf(runLog, format+"\n", args...)
	}
	if len(req.ImageNames) == 0 {
		return fmt.Errorf("imageNames is required")
	}
	if req.SourceLocation == "" {
		return fmt.Errorf("sourceLocation is required (only source-based DockerBuildRequest is supported)")
	}

	// The run's docker steps push to — and pull base images from — the
	// registry as the run itself: a Docker configuration whose credential
	// helper answers this registry's login server with an identity token
	// of the run, which the client exchanges through the registry's
	// refresh-token grant, the way an ACR Tasks run holds its registry's
	// push and pull scopes.
	dockerConfigDir, err := acrRunDockerConfig(reg, runID)
	if err != nil {
		return fmt.Errorf("docker configuration: %w", err)
	}
	defer os.RemoveAll(dockerConfigDir)
	dockerEnv := append(os.Environ(), sim.DockerConfigEnv(dockerConfigDir)...)

	account, container, blob, err := parseACRBlobURL(req.SourceLocation)
	if err != nil {
		return fmt.Errorf("parse sourceLocation: %w", err)
	}
	obj, ok := blobObjects.Get(blobObjectKey(account, container, blob))
	if !ok {
		return fmt.Errorf("source context blob %s/%s not found in storage account %s", container, blob, account)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker CLI not available for ACR Tasks build: %w", err)
	}

	dockerfile := req.DockerFilePath
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	args := append(workload.DockerBuildInvocation(ctx, dockerEnv), "-f", dockerfile)
	for _, img := range req.ImageNames {
		args = append(args, "-t", img)
	}
	if req.Platform != nil && req.Platform.OS != "" {
		platform := req.Platform.OS
		if req.Platform.Architecture != "" {
			platform += "/" + req.Platform.Architecture
		}
		args = append(args, "--platform", platform)
	}
	if req.NoCache {
		args = append(args, "--no-cache")
	}
	for _, a := range req.Arguments {
		args = append(args, "--build-arg", a.Name+"="+a.Value)
	}
	if req.Target != "" {
		args = append(args, "--target", req.Target)
	}
	args = append(args, "-") // build context from stdin (gzipped tar)
	acrTasksLogger.Info().Str("runId", runID).Strs("invocation", args).Msg("ACR Tasks: building")

	_, buildContext, err := blobOpen(obj)
	if err != nil {
		return fmt.Errorf("read the build context: %w", err)
	}
	defer func() { _ = buildContext.Close() }()
	cmd := workload.DockerCommand(ctx, dockerEnv, args...)
	cmd.Stdin = buildContext
	cmd.Stdout = runLog
	cmd.Stderr = runLog
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build %v failed: %w", req.ImageNames, err)
	}

	// Push each tag to its registry and drop the local copy — the faithful
	// build→push that leaves the image in the registry, not on the build
	// host. The image ref's host (e.g. the configured ACR endpoint) routes
	// to the registry's /v2/; pushing is a plain registry-client operation.
	if !req.IsPushEnabledOrDefault() {
		return nil
	}
	for _, img := range req.ImageNames {
		logf("The push refers to repository [%s]", img)
		push := workload.DockerCommand(ctx, dockerEnv, "push", img)
		push.Stdout = runLog
		push.Stderr = runLog
		if err := push.Run(); err != nil {
			return fmt.Errorf("docker push %s failed: %w", img, err)
		}
		if out, err := workload.DockerCommand(ctx, dockerEnv, "rmi", "-f", img).CombinedOutput(); err != nil {
			acrTasksLogger.Warn().Str("image", img).Str("out", strings.TrimSpace(string(out))).
				Msg("could not remove local ACR Task build output after push")
		}
	}
	return nil
}

// IsPushEnabledOrDefault reports whether the build output should be pushed.
// Real ACR Tasks defaults IsPushEnabled to true for a DockerBuildRequest;
// the sim honors an explicit false (build-only) but pushes otherwise.
func (r acrDockerBuildRequest) IsPushEnabledOrDefault() bool {
	return r.IsPushEnabled == nil || *r.IsPushEnabled
}

// parseACRBlobURL splits an Azure blob URL
// (https://<account>.blob.core.windows.net/<container>/<blob>) into its
// account / container / blob-name parts. The account is the host's first
// subdomain label; the first path segment is the container and the rest is
// the blob name (which may itself contain slashes).
func parseACRBlobURL(raw string) (account, container, blob string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", err
	}
	host := u.Host
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	account, _, _ = strings.Cut(host, ".")
	if account == "" {
		return "", "", "", fmt.Errorf("no storage account in host %q", u.Host)
	}
	p := strings.TrimPrefix(u.Path, "/")
	container, blob, ok := strings.Cut(p, "/")
	if !ok || container == "" || blob == "" {
		return "", "", "", fmt.Errorf("expected /<container>/<blob> path, got %q", u.Path)
	}
	return account, container, blob, nil
}

// parseACRImage splits <registry>/<repository>:<tag> into its parts for
// the Run's outputImages list.
func parseACRImage(img string) acrImageDescriptor {
	d := acrImageDescriptor{}
	rest := img
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		d.Registry = rest[:i]
		rest = rest[i+1:]
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		d.Repository = rest[:i]
		d.Tag = rest[i+1:]
	} else {
		d.Repository = rest
	}
	return d
}

// acrRunDockerConfig writes the Docker configuration an ACR Tasks run's
// docker steps use: the registry's login server, with or without its port,
// answered with an identity token of the run.
func acrRunDockerConfig(reg Registry, runID string) (string, error) {
	refreshToken, err := acrMintRefreshToken(reg, "acr-task-run:"+runID)
	if err != nil {
		return "", err
	}
	return sim.WriteDockerConfig(sim.DockerConfigSpec{
		HostPatterns: []string{acrBareHost(acrLoginServer(reg)) + ":*"},
		Hosts:        []string{acrLoginServer(reg), acrBareHost(acrLoginServer(reg))},
		Credential: sim.DockerCredential{
			Username: sim.DockerIdentityTokenUsername,
			Secret:   refreshToken,
		},
	})
}
