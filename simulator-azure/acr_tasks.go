package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
	"github.com/rs/zerolog"
)

// ACR Tasks quick-build slice. A client builds an image through the real ACR
// Tasks API — RegistriesClient.BeginScheduleRun with a DockerBuildRequest, then
// polling RunsClient.Get. This implements
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
}

var acrRuns sim.Store[acrRun]
var acrRunLogs sim.Store[string]
var acrTasksLogger zerolog.Logger

func registerACRTasks(srv *sim.Server) {
	acrRuns = sim.MakeStore[acrRun](srv.DB(), "acr_runs")
	acrRunLogs = sim.MakeStore[string](srv.DB(), "acr_run_logs")
	acrTasksLogger = srv.Logger()

	// The run-log endpoint listLogSasUrl advertises. Real ACR serves the
	// build log as a plain text blob at a SAS-authorized URL; the sim's
	// link carries the run id as the capability.
	srv.HandleFunc("GET /acr/v1/logs/{runId}", func(w http.ResponseWriter, r *http.Request) {
		log, ok := acrRunLogs.Get(sim.PathParam(r, "runId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(log))
	})
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Task %q not found.", sim.PathParam(r, "taskName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// POST .../taskRuns/{name}/listDetails — returns the TaskRun with results.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/taskRuns/{taskRunName}/listDetails", func(w http.ResponseWriter, r *http.Request) {
		res, ok := taskRuns.Get(acrChildResourceID(r, "taskRuns", "taskRunName"))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "TaskRun %q not found.", sim.PathParam(r, "taskRunName"))
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", runID)
			return
		}
		var req struct{}
		_ = sim.ReadJSON(r, &req)
		sim.WriteJSON(w, http.StatusOK, run)
	})

	// POST .../runs/{runId}/cancel — cancels a run. No response body.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/runs/{runId}/cancel", func(w http.ResponseWriter, r *http.Request) {
		runID := sim.PathParam(r, "runId")
		acrRuns.Update(runID, func(run *acrRun) {
			run.Properties.Status = "Canceled"
		})
		w.WriteHeader(http.StatusOK)
	})

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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The run %q was not found.", runID)
			return
		}
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"logLink": fmt.Sprintf("%s://%s/acr/v1/logs/%s", scheme, r.Host, runID),
		})
	})

	// POST .../listBuildSourceUploadUrl — where a client uploads a build
	// context tar before scheduling a run.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/listBuildSourceUploadUrl", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := acrRegistryID(r); !ok {
			acrRegistryNotFound(w, r)
			return
		}
		rel := "source/" + generateUUID() + ".tar.gz"
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

func handleACRScheduleRun(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	registry := sim.PathParam(r, "registryName")

	var req acrDockerBuildRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", "failed to parse run request: "+err.Error(), http.StatusBadRequest)
		return
	}

	runID := "cb" + generateUUID()[:8]
	now := time.Now().UTC().Format(time.RFC3339)
	run := acrRun{
		ID:   fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s/runs/%s", sub, rg, registry, runID),
		Name: runID,
		Type: "Microsoft.ContainerRegistry/registries/runs",
		Properties: acrRunProperties{
			RunID:      runID,
			RunType:    "QuickBuild",
			CreateTime: now,
			StartTime:  now,
			Platform:   req.Platform,
		},
	}

	// Execute the docker build synchronously. Real ACR Tasks is async
	// (QUEUED → RUNNING → SUCCEEDED) with status surfaced via the Run
	// resource; the sim compresses that into the one call, exactly as the
	// GCP Cloud Build slice does, so the SDK poller resolves on the first
	// body read.
	buildLog, buildErr := executeACRBuild(r.Context(), req)
	acrRunLogs.Put(runID, buildLog)
	run.Properties.FinishTime = time.Now().UTC().Format(time.RFC3339)
	run.Properties.LastUpdatedTime = run.Properties.FinishTime
	if buildErr != nil {
		run.Properties.Status = "Failed"
		run.Properties.ProvisioningState = "Failed"
		acrRuns.Put(runID, run)
		// Surface the build failure in the sim log for harness diagnosis.
		// The response is still a 200 with the Run body (status=Failed) —
		// real ACR returns the run resource and reports failure through
		// its status, which the SDK body poller reads to fail the caller;
		// an ARM error envelope here would break the poller's body read.
		acrTasksLogger.Error().Str("runId", runID).Strs("images", req.ImageNames).
			Err(buildErr).Msg("ACR Task build failed")
		sim.WriteJSON(w, http.StatusOK, run)
		return
	}

	run.Properties.Status = "Succeeded"
	run.Properties.ProvisioningState = "Succeeded"
	for _, img := range req.ImageNames {
		run.Properties.OutputImages = append(run.Properties.OutputImages, parseACRImage(img))
	}
	acrRuns.Put(runID, run)
	sim.WriteJSON(w, http.StatusOK, run)
}

func handleACRGetRun(w http.ResponseWriter, r *http.Request) {
	runID := sim.PathParam(r, "runId")
	run, ok := acrRuns.Get(runID)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", runID)
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
// (Dockerfile + COPY'd files) streamed to `docker build -` on stdin.
// dockerBuildxAvailable reports whether the host's docker CLI has the buildx
// plugin, which decides how the ACR Tasks build must invoke docker (see
// executeACRBuild). Probed per build — cheap relative to the build itself.
func dockerBuildxAvailable(ctx context.Context) bool {
	return exec.CommandContext(ctx, "docker", "buildx", "version").Run() == nil
}

func executeACRBuild(ctx context.Context, req acrDockerBuildRequest) (string, error) {
	var runLog strings.Builder
	logf := func(format string, args ...any) {
		fmt.Fprintf(&runLog, format+"\n", args...)
	}
	if len(req.ImageNames) == 0 {
		return runLog.String(), fmt.Errorf("imageNames is required")
	}
	if req.SourceLocation == "" {
		return runLog.String(), fmt.Errorf("sourceLocation is required (only source-based DockerBuildRequest is supported)")
	}

	account, container, blob, err := parseACRBlobURL(req.SourceLocation)
	if err != nil {
		return runLog.String(), fmt.Errorf("parse sourceLocation: %w", err)
	}
	obj, ok := blobObjects.Get(blobObjectKey(account, container, blob))
	if !ok {
		return runLog.String(), fmt.Errorf("source context blob %s/%s not found in storage account %s", container, blob, account)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		return runLog.String(), fmt.Errorf("docker CLI not available for ACR Tasks build: %w", err)
	}

	dockerfile := req.DockerFilePath
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	// Choose the build invocation that lands the image in the daemon store on
	// whatever docker CLI the host has, so the subsequent `docker push` finds
	// it. When the buildx plugin is present, `docker buildx build --load`
	// exports the result into the daemon store for every driver (the default
	// docker-container buildx driver otherwise leaves it in the build cache only
	// → push "image not known"). When buildx is absent (the legacy builder,
	// e.g. the `docker.io` package), plain `docker build` writes to the store
	// natively and rejects the buildx-only `--load` flag, so it must be omitted.
	var args []string
	if dockerBuildxAvailable(ctx) {
		args = []string{"buildx", "build", "--load", "-f", dockerfile}
	} else {
		args = []string{"build", "-f", dockerfile}
	}
	acrTasksLogger.Info().Str("invocation", "docker "+args[0]).Bool("load", args[0] == "buildx").
		Strs("images", req.ImageNames).Msg("ACR Tasks: building overlay")
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

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(obj.Data)
	out, err := cmd.CombinedOutput()
	runLog.Write(out)
	if err != nil {
		return runLog.String(), fmt.Errorf("docker build %v failed: %w: %s", req.ImageNames, err, strings.TrimSpace(string(out)))
	}

	// Push each tag to its registry and drop the local copy — the faithful
	// build→push that leaves the image in the registry, not on the build
	// host. The image ref's host (e.g. the configured ACR endpoint) routes
	// to the registry's /v2/; pushing is a plain registry-client operation.
	if !req.IsPushEnabledOrDefault() {
		return runLog.String(), nil
	}
	for _, img := range req.ImageNames {
		logf("The push refers to repository [%s]", img)
		push := exec.CommandContext(ctx, "docker", "push", img)
		out, err := push.CombinedOutput()
		runLog.Write(out)
		if err != nil {
			return runLog.String(), fmt.Errorf("docker push %s failed: %w: %s", img, err, strings.TrimSpace(string(out)))
		}
		if out, err := exec.CommandContext(ctx, "docker", "rmi", "-f", img).CombinedOutput(); err != nil {
			acrTasksLogger.Warn().Str("image", img).Str("out", strings.TrimSpace(string(out))).
				Msg("could not remove local ACR Task build output after push")
		}
	}
	return runLog.String(), nil
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
