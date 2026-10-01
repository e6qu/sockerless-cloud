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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/rs/zerolog"
)

// The Azure Container Registry Tasks run slice. Registries_ScheduleRun queues
// a run of any of the four run requests — a docker build, a task file in the
// uploaded source, an inline task file, or a run of a Task resource — and
// answers with it Queued; the run then executes in the background on the host
// container engine, the simulator's build infrastructure, pushing what it
// builds into the registry as the run itself. A client follows it through
// Runs_Get or the log blob listLogSasUrl links to.
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
	Task              string               `json:"task,omitempty"`
	AgentPoolName     string               `json:"agentPoolName,omitempty"`
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

// The run request `timeout` bounds, in seconds, from the registry tasks
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
	acrTasks       sim.Store[acrSubResource]
	acrAgentPools  sim.Store[acrSubResource]
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
	srv.HandleFunc("PUT "+acrBuildSourcePathPrefix+"{container}/{blob...}", handleACRBuildSource)
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
	acrTasks = sim.MakeStore[acrSubResource](srv.DB(), "acr_tasks")
	tasks := acrTasks
	taskRuns := sim.MakeStore[acrSubResource](srv.DB(), "acr_task_runs")
	acrAgentPools = sim.MakeStore[acrSubResource](srv.DB(), "acr_agent_pools")
	agentPools := acrAgentPools
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
		afterWrite: func() {
			acrPoolMu.Lock()
			defer acrPoolMu.Unlock()
			acrPoolNotify()
		},
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

	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/agentPools/{agentPoolName}/listQueueStatus", handleACRAgentPoolQueueStatus)

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

	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/listBuildSourceUploadUrl", handleACRListBuildSourceUploadURL)
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		AzureError(w, "InvalidRequestContent", "failed to read run request: "+err.Error(), http.StatusBadRequest)
		return
	}
	regID, _ := acrRegistryID(r)
	reg, ok := acrRegistries.Get(regID)
	if !ok {
		AzureError(w, "ResourceNotFound", fmt.Sprintf("The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.", registry, rg), http.StatusNotFound)
		return
	}
	spec, rerr := acrDecodeRunRequest(body, reg)
	if rerr != nil {
		AzureError(w, rerr.code, rerr.message, rerr.status)
		return
	}
	if spec.agentPool != "" {
		if _, ok := acrAgentPools.Get(regID + "/agentPools/" + spec.agentPool); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.ContainerRegistry/registries/agentPools/%s' under registry '%s' was not found.", spec.agentPool, registry)
			return
		}
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
			RunType:           spec.runType,
			Task:              spec.taskName,
			AgentPoolName:     spec.agentPool,
			CreateTime:        now,
			LastUpdatedTime:   now,
			Platform:          spec.platform,
			IsArchiveEnabled:  spec.isArchiveEnabled,
		},
	}
	acrRuns.Put(runID, run)
	acrStartRun(runID, spec, reg)
	sim.WriteJSON(w, http.StatusOK, run)
}

// acrStartRun registers the run as active before its worker starts, so a
// cancel that arrives the moment the schedule call returns finds it.
func acrStartRun(runID string, spec acrRunSpec, reg Registry) {
	stop, cancel := context.WithCancelCause(context.Background())
	active := &acrActiveRun{cancel: cancel, done: make(chan struct{})}
	acrActiveRunsMu.Lock()
	acrActiveRuns[runID] = active
	acrActiveRunsMu.Unlock()
	acrTasksServer.StartBackground("acr-tasks-run", func(serverCtx context.Context) {
		acrExecuteRun(serverCtx, stop, active, runID, spec, reg)
	})
}

// acrExecuteRun holds a run Queued until an agent of its pool is free, moves
// it to Running, executes it, and records the terminal status: Succeeded,
// Failed, Canceled when Runs_Cancel stopped it, Timeout when it outlived its
// timeout, and Error when the simulator shut down under it.
func acrExecuteRun(serverCtx, stop context.Context, active *acrActiveRun, runID string, spec acrRunSpec, reg Registry) {
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
	if spec.agentPool != "" {
		pool := reg.ID + "/agentPools/" + spec.agentPool
		if err := acrAcquireAgent(runCtx, pool); err != nil {
			acrFinishRun(runID, acrRunOutcome(serverCtx, context.Cause(runCtx), nil))
			return
		}
		defer acrReleaseAgent(pool)
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

	started := time.Now()
	buildCtx, cancelBuild := context.WithTimeoutCause(runCtx, spec.timeout, errACRRunTimedOut)
	defer cancelBuild()
	images, runErr := acrExecuteSpec(buildCtx, spec, reg, runID, log)

	outcome := acrRunOutcome(serverCtx, context.Cause(buildCtx), runErr)
	if outcome.status == "Succeeded" {
		for _, img := range images {
			outcome.outputImages = append(outcome.outputImages, parseACRImage(img))
		}
		_, _ = fmt.Fprintf(log, "\r\nRun ID: %s was successful after %s\r\n", runID, time.Since(started).Round(time.Second))
	} else {
		_, _ = fmt.Fprintf(log, "\r\nRun ID: %s failed after %s. Error: %s\r\n", runID, time.Since(started).Round(time.Second), outcome.message)
	}
	acrRunLogs.Put(runID, log.String())
	if outcome.status == "Failed" {
		output := log.String()
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		acrTasksLogger.Error().Str("runId", runID).Str("output", output).Err(runErr).Msg("ACR Tasks run failed")
	}
	acrFinishRun(runID, outcome)
}

// acrExecuteSpec executes a run with the registry credential the run holds,
// and returns the images it pushed.
func acrExecuteSpec(ctx context.Context, spec acrRunSpec, reg Registry, runID string, runLog io.Writer) ([]string, error) {
	// The run pushes to — and pulls images from — its registry as the run
	// itself: with an identity token of the run, which the engine exchanges
	// through the registry's refresh-token grant, the way an ACR Tasks run
	// holds its registry's push and pull scopes. Cmd and push steps present
	// it through the engine API; docker builds through a Docker
	// configuration whose credential helper answers the registry's login
	// server with it.
	refreshToken, err := acrMintRefreshToken(reg, "acr-task-run:"+runID)
	if err != nil {
		return nil, err
	}
	dockerConfigDir, err := acrRunDockerConfig(reg, refreshToken)
	if err != nil {
		return nil, fmt.Errorf("docker configuration: %w", err)
	}
	defer os.RemoveAll(dockerConfigDir)
	dockerEnv := append(os.Environ(), sim.DockerConfigEnv(dockerConfigDir)...)
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker CLI not available for ACR Tasks run: %w", err)
	}

	vars := acrRunVariables{
		ID:           runID,
		SharedVolume: "acb_home_vol_" + sim.NewUUID(),
		Registry:     acrLoginServer(reg),
		RegistryName: reg.Name,
		Date:         time.Now(),
		OS:           "linux",
		Architecture: "amd64",
		TaskName:     spec.taskName,
	}
	if vars.TaskName == "" {
		vars.TaskName = "quickrun"
	}
	if spec.platform != nil {
		if spec.platform.OS != "" {
			vars.OS = strings.ToLower(spec.platform.OS)
		}
		if spec.platform.Architecture != "" {
			vars.Architecture = strings.ToLower(spec.platform.Architecture)
		}
	}

	source, err := acrOpenSource(reg, spec.source)
	if err != nil {
		return nil, err
	}
	if source != nil {
		defer func() { _ = source.Close() }()
	}
	if spec.docker != nil {
		if source == nil {
			return nil, fmt.Errorf("sourceLocation is required: a docker build needs a source context")
		}
		return executeACRBuild(ctx, *spec.docker, source, vars, spec.platform, dockerEnv, runLog)
	}

	workDir, err := os.MkdirTemp("", "acr-run-workspace-*")
	if err != nil {
		return nil, err
	}
	if source != nil {
		if err := acrExtractSource(source, workDir); err != nil {
			_ = os.RemoveAll(workDir)
			return nil, fmt.Errorf("unpack the source: %w", err)
		}
	}
	content, values := spec.encodedTask, spec.encodedValues
	if spec.taskFile != "" {
		if content, err = acrReadSourceFile(workDir, spec.taskFile); err != nil {
			_ = os.RemoveAll(workDir)
			return nil, err
		}
	}
	if spec.valuesFile != "" {
		if values, err = acrReadSourceFile(workDir, spec.valuesFile); err != nil {
			_ = os.RemoveAll(workDir)
			return nil, err
		}
	}
	task, err := acrLoadTaskFile(content, values, spec.values, vars)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return nil, err
	}
	if err := sim.RequireContainerRuntime("an ACR Tasks run"); err != nil {
		_ = os.RemoveAll(workDir)
		return nil, err
	}
	x := &acrTaskRun{
		runID:        runID,
		log:          runLog,
		engine:       sim.DockerClient(),
		dockerEnv:    dockerEnv,
		platform:     acrDockerPlatform(spec.platform),
		network:      acrTaskDefaultNetwork + "_" + runID,
		volume:       vars.SharedVolume,
		registryHost: acrBareHost(acrLoginServer(reg)),
		registryAuth: sim.RegistryIdentityToken(refreshToken),
		workDir:      workDir,
	}
	err = acrRunTaskFile(ctx, task, x)
	return x.pushed, err
}

// acrReadSourceFile reads a file the request names relative to the source.
func acrReadSourceFile(workDir, name string) ([]byte, error) {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(filepath.FromSlash(strings.TrimPrefix(name, "./")))
	if err != nil {
		return nil, fmt.Errorf("read %s from the source: %w", name, err)
	}
	return data, nil
}

func acrDockerPlatform(p *acrPlatform) string {
	if p == nil || p.OS == "" {
		return ""
	}
	platform := strings.ToLower(p.OS)
	if p.Architecture != "" {
		platform += "/" + strings.ToLower(p.Architecture)
	}
	return platform
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

// executeACRBuild runs a docker build run: the source tar streams to `docker
// build -` on the host engine, and — as ACR Tasks does when isPushEnabled is
// not false — each image is pushed to its registry and the local copy
// removed, so the registry and not the build host holds the result. It
// returns the images it pushed.
func executeACRBuild(ctx context.Context, req acrDockerBuildSpec, source io.Reader, vars acrRunVariables, platform *acrPlatform, dockerEnv []string, runLog io.Writer) ([]string, error) {
	logf := func(format string, args ...any) {
		_, _ = fmt.Fprintf(runLog, format+"\n", args...)
	}
	images, err := acrRunImageNames(req.ImageNames, vars)
	if err != nil {
		return nil, err
	}
	dockerfile := req.DockerFilePath
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	args := append(workload.DockerBuildInvocation(ctx, dockerEnv), "-f", dockerfile)
	for _, img := range images {
		args = append(args, "-t", img)
	}
	if p := acrDockerPlatform(platform); p != "" {
		args = append(args, "--platform", p)
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
	args = append(args, "-")
	acrTasksLogger.Info().Str("runId", vars.ID).Strs("images", images).Msg("ACR Tasks: building")

	if err := acrDockerBuild(ctx, dockerEnv, args, "", source, runLog); err != nil {
		return nil, fmt.Errorf("docker build %v failed: %w", images, err)
	}
	if !req.IsPushEnabled || len(images) == 0 {
		for _, img := range images {
			_ = workload.DockerCommand(context.Background(), dockerEnv, "rmi", "-f", img).Run()
		}
		return nil, nil
	}
	for _, img := range images {
		logf("The push refers to repository [%s]", img)
		push := workload.DockerCommand(ctx, dockerEnv, "push", img)
		push.Stdout = runLog
		push.Stderr = runLog
		if err := push.Run(); err != nil {
			return nil, fmt.Errorf("docker push %s failed: %w", img, err)
		}
		if out, err := workload.DockerCommand(ctx, dockerEnv, "rmi", "-f", img).CombinedOutput(); err != nil {
			acrTasksLogger.Warn().Str("image", img).Str("out", strings.TrimSpace(string(out))).
				Msg("could not remove local ACR Task build output after push")
		}
	}
	return images, nil
}

// acrDockerBuild runs a docker build on the host engine with the run's Docker
// configuration in env: in dir, or reading its context from stdin when the
// arguments name `-` as the context.
func acrDockerBuild(ctx context.Context, env, args []string, dir string, stdin io.Reader, runLog io.Writer) error {
	cmd := workload.DockerCommand(ctx, env, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Stdout = runLog
	cmd.Stderr = runLog
	return cmd.Run()
}

// acrRunImageNames renders a docker build's image names with the run's
// variables, as `{{.Run.ID}}` in `az acr build --image app:{{.Run.ID}}`, and
// places a name with no registry in the run's own registry.
func acrRunImageNames(names []string, vars acrRunVariables) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, name := range names {
		rendered, err := acrRenderTemplate("imageNames", name, map[string]any{"Run": vars.template()})
		if err != nil {
			return nil, err
		}
		rendered = strings.TrimSpace(rendered)
		if first, _, found := strings.Cut(rendered, "/"); !found || (!strings.ContainsAny(first, ".:") && first != "localhost") {
			rendered = vars.Registry + "/" + rendered
		}
		out = append(out, rendered)
	}
	return out, nil
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
func acrRunDockerConfig(reg Registry, refreshToken string) (string, error) {
	return sim.WriteDockerConfig(sim.DockerConfigSpec{
		HostPatterns: []string{acrBareHost(acrLoginServer(reg)) + ":*"},
		Hosts:        []string{acrLoginServer(reg), acrBareHost(acrLoginServer(reg))},
		Credential: sim.DockerCredential{
			Username: sim.DockerIdentityTokenUsername,
			Secret:   refreshToken,
		},
	})
}
