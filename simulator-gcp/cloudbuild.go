package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// Cloud Build v1 slice: a client submits a build, the simulator fetches the
// source, runs each step and reports the build's progress and result the way
// Cloud Build does.
//
// Real API: https://cloud.google.com/build/docs/api/reference/rest

// Build represents a Cloud Build build resource.
type Build struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	ProjectID string `json:"projectId"`
	// ServiceAccount is the identity the build runs as, as
	// projects/{project}/serviceAccounts/{email}; empty means the project's
	// Cloud Build service account.
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// A trigger carries a build template, which has not run and has no status;
	// omitting it is what the absence means. A build that has run always has
	// one, so nothing that ran loses it — and "" is a value the enum does not
	// declare.
	Status           string            `json:"status,omitempty"`
	StatusDetail     string            `json:"statusDetail,omitempty"`
	Source           *BuildSource      `json:"source,omitempty"`
	Steps            []*BuildStep      `json:"steps,omitempty"`
	Images           []string          `json:"images,omitempty"`
	CreateTime       string            `json:"createTime,omitempty"`
	StartTime        string            `json:"startTime,omitempty"`
	FinishTime       string            `json:"finishTime,omitempty"`
	LogsBucket       string            `json:"logsBucket,omitempty"`
	AvailableSecrets *AvailableSecrets `json:"availableSecrets,omitempty"`
	Substitutions    map[string]string `json:"substitutions,omitempty"`
	Options          map[string]any    `json:"options,omitempty"`
	Approval         *BuildApproval    `json:"approval,omitempty"`
	BuildTriggerID   string            `json:"buildTriggerId,omitempty"`
	Timeout          string            `json:"timeout,omitempty"`
}

// cloudBuildDefaultTimeout is how long a build runs when it names no timeout:
// "Default time is 60 minutes."
const cloudBuildDefaultTimeout = 60 * time.Minute

// BuildApproval mirrors the Cloud Build v1 BuildApproval schema. A build only
// carries one when its trigger requires approval.
type BuildApproval struct {
	State  string               `json:"state,omitempty"`
	Config map[string]any       `json:"config,omitempty"`
	Result *BuildApprovalResult `json:"result,omitempty"`
}

// BuildApprovalResult mirrors the Cloud Build v1 ApprovalResult schema.
type BuildApprovalResult struct {
	Decision        string `json:"decision,omitempty"`
	Comment         string `json:"comment,omitempty"`
	URL             string `json:"url,omitempty"`
	ApproverAccount string `json:"approverAccount,omitempty"`
	ApprovalTime    string `json:"approvalTime,omitempty"`
}

type BuildSource struct {
	StorageSource *StorageSource `json:"storageSource,omitempty"`
}

type StorageSource struct {
	Bucket string `json:"bucket"`
	Object string `json:"object"`
}

type BuildStep struct {
	Name       string   `json:"name"`
	Args       []string `json:"args,omitempty"`
	Env        []string `json:"env,omitempty"`
	SecretEnv  []string `json:"secretEnv,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	Entrypoint string   `json:"entrypoint,omitempty"`
	ID         string   `json:"id,omitempty"`
	// Status and Timing are what the build reports about the step as it runs.
	// Cloud Build fills both in, and they are the only way a client can tell
	// which step a running build is on — the build's own status says WORKING
	// from before the source is fetched until the last step finishes.
	Status string       `json:"status,omitempty"`
	Timing *BuildTiming `json:"timing,omitempty"`
}

// BuildTiming is the interval a step occupied.
type BuildTiming struct {
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
}

// AvailableSecrets binds Secret Manager references to environment
// variable names usable by steps via `secretEnv`.
type AvailableSecrets struct {
	SecretManager []*SecretManagerSecret `json:"secretManager,omitempty"`
}

type SecretManagerSecret struct {
	VersionName string `json:"versionName"`
	Env         string `json:"env"`
}

// Operation is the LRO wrapper Cloud Build returns from CreateBuild.
type CloudBuildOperation struct {
	Name     string         `json:"name"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Done     bool           `json:"done"`
	Response map[string]any `json:"response,omitempty"`
	Error    *BuildError    `json:"error,omitempty"`
}

type BuildError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type BuildTrigger struct {
	ID                    string            `json:"id,omitempty"`
	Name                  string            `json:"name,omitempty"`
	ResourceName          string            `json:"resourceName,omitempty"`
	Description           string            `json:"description,omitempty"`
	Filename              string            `json:"filename,omitempty"`
	Disabled              bool              `json:"disabled,omitempty"`
	IgnoredFiles          []string          `json:"ignoredFiles,omitempty"`
	IncludedFiles         []string          `json:"includedFiles,omitempty"`
	Substitutions         map[string]string `json:"substitutions,omitempty"`
	Tags                  []string          `json:"tags,omitempty"`
	CreateTime            string            `json:"createTime,omitempty"`
	ApprovalConfig        map[string]any    `json:"approvalConfig,omitempty"`
	TriggerTemplate       map[string]any    `json:"triggerTemplate,omitempty"`
	GitFileSource         map[string]any    `json:"gitFileSource,omitempty"`
	SourceToBuild         map[string]any    `json:"sourceToBuild,omitempty"`
	RepositoryEventConfig map[string]any    `json:"repositoryEventConfig,omitempty"`
	Github                map[string]any    `json:"github,omitempty"`
	WebhookConfig         map[string]any    `json:"webhookConfig,omitempty"`
	Build                 *Build            `json:"build,omitempty"`
}

// WorkerPool mirrors the Cloud Build v1 WorkerPool resource. Output-only
// fields (name, uid, state, *Time, etag) are populated by the simulator.
type WorkerPool struct {
	Name                string            `json:"name,omitempty"`
	DisplayName         string            `json:"displayName,omitempty"`
	UID                 string            `json:"uid,omitempty"`
	Annotations         map[string]string `json:"annotations,omitempty"`
	CreateTime          string            `json:"createTime,omitempty"`
	UpdateTime          string            `json:"updateTime,omitempty"`
	DeleteTime          string            `json:"deleteTime,omitempty"`
	State               string            `json:"state,omitempty"`
	PrivatePoolV1Config map[string]any    `json:"privatePoolV1Config,omitempty"`
	Etag                string            `json:"etag,omitempty"`
}

// GitHubEnterpriseConfig mirrors the Cloud Build v1 source-host config.
type GitHubEnterpriseConfig struct {
	Name          string         `json:"name,omitempty"`
	HostURL       string         `json:"hostUrl,omitempty"`
	AppID         string         `json:"appId,omitempty"`
	CreateTime    string         `json:"createTime,omitempty"`
	WebhookKey    string         `json:"webhookKey,omitempty"`
	PeeredNetwork string         `json:"peeredNetwork,omitempty"`
	Secrets       map[string]any `json:"secrets,omitempty"`
	DisplayName   string         `json:"displayName,omitempty"`
	SslCa         string         `json:"sslCa,omitempty"`
}

// BitbucketServerConfig mirrors the Cloud Build v1 Bitbucket Server config.
type BitbucketServerConfig struct {
	Name                  string           `json:"name,omitempty"`
	HostURI               string           `json:"hostUri,omitempty"`
	Secrets               map[string]any   `json:"secrets,omitempty"`
	CreateTime            string           `json:"createTime,omitempty"`
	Username              string           `json:"username,omitempty"`
	WebhookKey            string           `json:"webhookKey,omitempty"`
	APIKey                string           `json:"apiKey,omitempty"`
	ConnectedRepositories []map[string]any `json:"connectedRepositories,omitempty"`
	PeeredNetwork         string           `json:"peeredNetwork,omitempty"`
	SslCa                 string           `json:"sslCa,omitempty"`
	PeeredNetworkIPRange  string           `json:"peeredNetworkIpRange,omitempty"`
}

var cbBuilds sim.Store[Build]

// cbRunning holds the cancel function of the context a build's steps execute
// under, keyed by build ID, for as long as those steps are running and until
// the build's record has settled from them. Cancelling a build is not a status
// flag: the entry is what lets CancelBuild and the build operation's cancel
// reach the `docker` process a step is running and terminate it, which is what
// "cancels a build in progress" means. Its presence also holds the build's
// operation open, so the operation completes on the settled record.
var cbRunning sync.Map

var cbTriggers sim.Store[BuildTrigger]
var cbWorkerPools sim.Store[WorkerPool]
var cbGHEConfigs sim.Store[GitHubEnterpriseConfig]
var cbBitbucketConfigs sim.Store[BitbucketServerConfig]

func registerCloudBuild(srv *sim.Server) {
	cbBuilds = sim.MakeStore[Build](srv.DB(), "cloudbuild_builds")
	cbTriggers = sim.MakeStore[BuildTrigger](srv.DB(), "cloudbuild_triggers")
	cbWorkerPools = sim.MakeStore[WorkerPool](srv.DB(), "cloudbuild_worker_pools")
	cbGHEConfigs = sim.MakeStore[GitHubEnterpriseConfig](srv.DB(), "cloudbuild_ghe_configs")
	cbBitbucketConfigs = sim.MakeStore[BitbucketServerConfig](srv.DB(), "cloudbuild_bitbucket_configs")

	// CreateBuild: POST /v1/projects/{project}/builds
	registerCloudBuildRegional(srv)

	srv.HandleFunc("POST /v1/projects/{project}/builds", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		if _, ok := crmProjectNumber(project); !ok {
			crmProjectPermissionDenied(w)
			return
		}
		var build Build
		if err := sim.ReadJSON(r, &build); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid build body: %v", err)
			return
		}
		build.ID = sim.NewUUID()
		build.ProjectID = project
		build.Status = "QUEUED"
		build.CreateTime = time.Now().UTC().Format(time.RFC3339)
		build.Name = fmt.Sprintf("projects/%s/locations/global/builds/%s", project, build.ID)

		cbBuilds.Put(build.ID, build)
		cbWriteBuildOperation(w, project, startCloudBuild(build))
	})

	srv.HandleFunc("POST /v1/projects/{project}/triggers", handleCreateBuildTrigger)
	srv.HandleFunc("GET /v1/projects/{project}/triggers", handleListBuildTriggers)
	srv.HandleFunc("GET /v1/projects/{project}/triggers/{trigger}", handleGetBuildTrigger)
	srv.HandleFunc("PATCH /v1/projects/{project}/triggers/{trigger}", handleUpdateBuildTrigger)
	srv.HandleFunc("DELETE /v1/projects/{project}/triggers/{trigger}", handleDeleteBuildTrigger)

	// ListBuilds: GET /v1/projects/{project}/builds
	srv.HandleFunc("GET /v1/projects/{project}/builds", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		builds := cbBuilds.Filter(func(b Build) bool { return b.ProjectID == project })
		sort.Slice(builds, func(i, j int) bool { return builds[i].CreateTime > builds[j].CreateTime })
		page, next, ok := paginateList(w, r, builds)
		if !ok {
			return
		}
		resp := map[string]any{"builds": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GetBuild: GET /v1/projects/{project}/builds/{id}
	srv.HandleFunc("GET /v1/projects/{project}/builds/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		build, ok := cbBuilds.Get(id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "build %s not found", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, build)
	})

	// CancelBuild. The document declares it twice — on the legacy global
	// resource path projects/{project}/builds/{id}:cancel and on the regional
	// path projects/{project}/locations/{location}/builds/{id}:cancel — and
	// both name the same build. Go ServeMux doesn't allow `{id}:cancel`, so
	// each mount takes a single wildcard and parses the colon suffix.
	cancelBuild := func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "idAction")
		id, action, found := strings.Cut(idAction, ":")
		if found && cbBuildActionHandled(w, r, sim.PathParam(r, "project"), id, action) {
			return
		}
		if !found || action != "cancel" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown build action %q", idAction)
			return
		}
		build, ok := cancelCloudBuild(id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "build %s not found", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, build)
	}
	srv.HandleFunc("POST /v1/projects/{project}/builds/{idAction}", cancelBuild)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/builds/{idAction}", cancelBuild)

	// GetOperation for cloudbuild LROs:
	// GET /v1/{name=operations/**}  — Go SDK uses this path.
	srv.HandleFunc("GET /v1/operations/build/{project}/{id}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		id := sim.PathParam(r, "id")
		build, ok := cbBuilds.Get(id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation for build %s not found", id)
			return
		}
		cbWriteBuildOperation(w, project, build)
	})

	// Global GetOperation: GET /v1/operations/{operation}. The cloudbuild
	// LRO names the simulator mints are `operations/build/{project}/{id}`;
	// the {+name} template captures the whole tail as a single param.
	srv.HandleFunc("GET /v1/operations/{operation...}", handleCloudBuildGetOperation)

	// Regional builds (read-only mirror of the global build endpoints).
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/builds", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		builds := cbBuilds.Filter(func(b Build) bool { return b.ProjectID == project })
		sort.Slice(builds, func(i, j int) bool { return builds[i].CreateTime > builds[j].CreateTime })
		page, next, ok := paginateList(w, r, builds)
		if !ok {
			return
		}
		resp := map[string]any{"builds": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/builds/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		build, ok := cbBuilds.Get(id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "build %s not found", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, build)
	})

	// getDefaultServiceAccount: GET /v1/projects/{p}/locations/{loc}/defaultServiceAccount.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/defaultServiceAccount", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		number, ok := crmProjectNumber(project)
		if !ok {
			crmProjectPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name":                fmt.Sprintf("projects/%s/locations/%s/defaultServiceAccount", project, location),
			"serviceAccountEmail": fmt.Sprintf("projects/%s/serviceAccounts/%s@cloudbuild.gserviceaccount.com", project, number),
		})
	})

	// Worker pools (regional).
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/workerPools", handleCreateWorkerPool)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/workerPools", handleListWorkerPools)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/workerPools/{pool}", handleGetWorkerPool)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/workerPools/{pool}", handlePatchWorkerPool)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/workerPools/{pool}", handleDeleteWorkerPool)

	// GitHub Enterprise configs — global and regional.
	srv.HandleFunc("POST /v1/projects/{project}/githubEnterpriseConfigs", handleCreateGHEConfig)
	srv.HandleFunc("GET /v1/projects/{project}/githubEnterpriseConfigs", handleListGHEConfigs)
	srv.HandleFunc("GET /v1/projects/{project}/githubEnterpriseConfigs/{config}", handleGetGHEConfig)
	srv.HandleFunc("PATCH /v1/projects/{project}/githubEnterpriseConfigs/{config}", handlePatchGHEConfig)
	srv.HandleFunc("DELETE /v1/projects/{project}/githubEnterpriseConfigs/{config}", handleDeleteGHEConfig)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs", handleCreateGHEConfig)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs", handleListGHEConfigs)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}", handleGetGHEConfig)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}", handlePatchGHEConfig)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}", handleDeleteGHEConfig)

	// Bitbucket Server configs (regional) + repos list.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs", handleCreateBitbucketConfig)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs", handleListBitbucketConfigs)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}", handleGetBitbucketConfig)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}", handlePatchBitbucketConfig)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}", handleDeleteBitbucketConfig)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}/repos", handleListBitbucketRepos)
}

// cbConfigLRO records a settled worker-pool or source-host configuration
// operation in the regional operations collection, under an id of its own, so
// every mutation is a distinct operation operations.get can read. Its metadata
// is the {Create,Update,Delete}<Kind>OperationMetadata the method declares,
// whose member naming the resource is member.
func cbConfigLRO(project, location, message, member, target string, resource any, typeURL string) Operation {
	started := nowTimestamp()
	return newLRO(project, location, resource, typeURL, gcpFixedOperationMetadata(map[string]any{
		"@type":        "type.googleapis.com/google.devtools.cloudbuild.v1." + message,
		member:         target,
		"createTime":   started,
		"completeTime": nowTimestamp(),
	}))
}

func handleCloudBuildGetOperation(w http.ResponseWriter, r *http.Request) {
	name := "operations/" + sim.PathParam(r, "operation")
	// cloudbuild build LROs are named operations/build/{project}/{id}.
	parts := strings.Split(name, "/")
	if len(parts) == 4 && parts[1] == "build" {
		id := parts[3]
		build, ok := cbBuilds.Get(id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation for build %s not found", id)
			return
		}
		cbWriteBuildOperation(w, parts[2], build)
		return
	}
	// Other (config / worker-pool) LROs resolve synchronously.
	sim.WriteJSON(w, http.StatusOK, CloudBuildOperation{Name: name, Done: true})
}

func cbConfigKey(project, location, kind, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/%s/%s", project, location, kind, id)
}

func handleCreateWorkerPool(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("workerPoolId")
	if id == "" {
		id = sim.NewUUID()
	}
	var pool WorkerPool
	if err := sim.ReadJSON(r, &pool); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workerPool body: %v", err)
		return
	}
	pool.Name = fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, location, id)
	pool.UID = sim.NewUUID()
	pool.State = "RUNNING"
	pool.CreateTime = nowTimestamp()
	pool.UpdateTime = pool.CreateTime
	pool.Etag = sim.NewUUID()
	cbWorkerPools.Put(pool.Name, pool)
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(project, location, "CreateWorkerPoolOperationMetadata", "workerPool", pool.Name,
		pool, "type.googleapis.com/google.devtools.cloudbuild.v1.WorkerPool"))
}

func handleGetWorkerPool(w http.ResponseWriter, r *http.Request) {
	key := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "pool"))
	pool, ok := cbWorkerPools.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "workerPool %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, pool)
}

func handleListWorkerPools(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/locations/%s/workerPools/", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	pools := cbWorkerPools.Filter(func(p WorkerPool) bool { return strings.HasPrefix(p.Name, prefix) })
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	page, next, ok := paginateListParam(w, r, pools, "pageSize")
	if !ok {
		return
	}
	resp := map[string]any{"workerPools": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handlePatchWorkerPool(w http.ResponseWriter, r *http.Request) {
	key := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "pool"))
	prior, ok := cbWorkerPools.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "workerPool %s not found", key)
		return
	}
	var update WorkerPool
	if err := sim.ReadJSON(r, &update); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workerPool body: %v", err)
		return
	}
	mask := r.URL.Query().Get("updateMask")
	has := func(p string) bool { return updateMaskHas(mask, p) }
	if mask == "" || has("displayName") {
		prior.DisplayName = update.DisplayName
	}
	if mask == "" || has("annotations") {
		prior.Annotations = update.Annotations
	}
	if mask == "" || has("privatePoolV1Config") {
		prior.PrivatePoolV1Config = update.PrivatePoolV1Config
	}
	prior.UpdateTime = nowTimestamp()
	prior.Etag = sim.NewUUID()
	cbWorkerPools.Put(key, prior)
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"),
		"UpdateWorkerPoolOperationMetadata", "workerPool", key, prior, "type.googleapis.com/google.devtools.cloudbuild.v1.WorkerPool"))
}

func handleDeleteWorkerPool(w http.ResponseWriter, r *http.Request) {
	key := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "pool"))
	if _, ok := cbWorkerPools.Get(key); !ok && r.URL.Query().Get("allowMissing") != "true" {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "workerPool %s not found", key)
		return
	}
	cbWorkerPools.Delete(key)
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"),
		"DeleteWorkerPoolOperationMetadata", "workerPool", key, nil, gcpEmptyType))
}

func handleCreateGHEConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := buildTriggerLocation(r)
	id := r.URL.Query().Get("gheConfigId")
	if id == "" {
		id = sim.NewUUID()
	}
	var cfg GitHubEnterpriseConfig
	if err := sim.ReadJSON(r, &cfg); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid githubEnterpriseConfig body: %v", err)
		return
	}
	cfg.Name = cbConfigKey(project, location, "githubEnterpriseConfigs", id)
	cfg.CreateTime = nowTimestamp()
	cbGHEConfigs.Put(cfg.Name, cfg)
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(project, location, "CreateGitHubEnterpriseConfigOperationMetadata",
		"githubEnterpriseConfig", cfg.Name, cfg, "type.googleapis.com/google.devtools.cloudbuild.v1.GitHubEnterpriseConfig"))
}

func handleGetGHEConfig(w http.ResponseWriter, r *http.Request) {
	key := cbConfigKey(sim.PathParam(r, "project"), buildTriggerLocation(r), "githubEnterpriseConfigs", sim.PathParam(r, "config"))
	cfg, ok := cbGHEConfigs.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "githubEnterpriseConfig %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleListGHEConfigs(w http.ResponseWriter, r *http.Request) {
	prefix := cbConfigKey(sim.PathParam(r, "project"), buildTriggerLocation(r), "githubEnterpriseConfigs", "")
	configs := cbGHEConfigs.Filter(func(c GitHubEnterpriseConfig) bool { return strings.HasPrefix(c.Name, prefix) })
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"configs": configs})
}

func handlePatchGHEConfig(w http.ResponseWriter, r *http.Request) {
	key := cbConfigKey(sim.PathParam(r, "project"), buildTriggerLocation(r), "githubEnterpriseConfigs", sim.PathParam(r, "config"))
	prior, ok := cbGHEConfigs.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "githubEnterpriseConfig %s not found", key)
		return
	}
	var update GitHubEnterpriseConfig
	if err := sim.ReadJSON(r, &update); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid githubEnterpriseConfig body: %v", err)
		return
	}
	mask := r.URL.Query().Get("updateMask")
	if mask == "" || updateMaskHas(mask, "hostUrl") {
		prior.HostURL = update.HostURL
	}
	if mask == "" || updateMaskHas(mask, "appId") {
		prior.AppID = update.AppID
	}
	if mask == "" || updateMaskHas(mask, "displayName") {
		prior.DisplayName = update.DisplayName
	}
	if mask == "" || updateMaskHas(mask, "peeredNetwork") {
		prior.PeeredNetwork = update.PeeredNetwork
	}
	if mask == "" || updateMaskHas(mask, "secrets") {
		prior.Secrets = update.Secrets
	}
	if mask == "" || updateMaskHas(mask, "sslCa") {
		prior.SslCa = update.SslCa
	}
	cbGHEConfigs.Put(key, prior)
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(sim.PathParam(r, "project"), buildTriggerLocation(r),
		"UpdateGitHubEnterpriseConfigOperationMetadata", "githubEnterpriseConfig", key,
		prior, "type.googleapis.com/google.devtools.cloudbuild.v1.GitHubEnterpriseConfig"))
}

func handleDeleteGHEConfig(w http.ResponseWriter, r *http.Request) {
	key := cbConfigKey(sim.PathParam(r, "project"), buildTriggerLocation(r), "githubEnterpriseConfigs", sim.PathParam(r, "config"))
	if !cbGHEConfigs.Delete(key) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "githubEnterpriseConfig %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(sim.PathParam(r, "project"), buildTriggerLocation(r),
		"DeleteGitHubEnterpriseConfigOperationMetadata", "githubEnterpriseConfig", key, nil, gcpEmptyType))
}

func handleCreateBitbucketConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("bitbucketServerConfigId")
	if id == "" {
		id = sim.NewUUID()
	}
	var cfg BitbucketServerConfig
	if err := sim.ReadJSON(r, &cfg); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid bitbucketServerConfig body: %v", err)
		return
	}
	cfg.Name = cbConfigKey(project, location, "bitbucketServerConfigs", id)
	cfg.CreateTime = nowTimestamp()
	cbBitbucketConfigs.Put(cfg.Name, cfg)
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(project, location, "CreateBitbucketServerConfigOperationMetadata",
		"bitbucketServerConfig", cfg.Name, cfg, "type.googleapis.com/google.devtools.cloudbuild.v1.BitbucketServerConfig"))
}

func handleGetBitbucketConfig(w http.ResponseWriter, r *http.Request) {
	key := cbConfigKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), "bitbucketServerConfigs", sim.PathParam(r, "config"))
	cfg, ok := cbBitbucketConfigs.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bitbucketServerConfig %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleListBitbucketConfigs(w http.ResponseWriter, r *http.Request) {
	prefix := cbConfigKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), "bitbucketServerConfigs", "")
	configs := cbBitbucketConfigs.Filter(func(c BitbucketServerConfig) bool { return strings.HasPrefix(c.Name, prefix) })
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	page, next, ok := paginateListParam(w, r, configs, "pageSize")
	if !ok {
		return
	}
	resp := map[string]any{"bitbucketServerConfigs": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handlePatchBitbucketConfig(w http.ResponseWriter, r *http.Request) {
	key := cbConfigKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), "bitbucketServerConfigs", sim.PathParam(r, "config"))
	prior, ok := cbBitbucketConfigs.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bitbucketServerConfig %s not found", key)
		return
	}
	var update BitbucketServerConfig
	if err := sim.ReadJSON(r, &update); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid bitbucketServerConfig body: %v", err)
		return
	}
	mask := r.URL.Query().Get("updateMask")
	if mask == "" || updateMaskHas(mask, "hostUri") {
		prior.HostURI = update.HostURI
	}
	if mask == "" || updateMaskHas(mask, "username") {
		prior.Username = update.Username
	}
	if mask == "" || updateMaskHas(mask, "apiKey") {
		prior.APIKey = update.APIKey
	}
	if mask == "" || updateMaskHas(mask, "secrets") {
		prior.Secrets = update.Secrets
	}
	if mask == "" || updateMaskHas(mask, "peeredNetwork") {
		prior.PeeredNetwork = update.PeeredNetwork
	}
	if mask == "" || updateMaskHas(mask, "sslCa") {
		prior.SslCa = update.SslCa
	}
	cbBitbucketConfigs.Put(key, prior)
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"),
		"UpdateBitbucketServerConfigOperationMetadata", "bitbucketServerConfig", key,
		prior, "type.googleapis.com/google.devtools.cloudbuild.v1.BitbucketServerConfig"))
}

func handleDeleteBitbucketConfig(w http.ResponseWriter, r *http.Request) {
	key := cbConfigKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), "bitbucketServerConfigs", sim.PathParam(r, "config"))
	if !cbBitbucketConfigs.Delete(key) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bitbucketServerConfig %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cbConfigLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"),
		"DeleteBitbucketServerConfigOperationMetadata", "bitbucketServerConfig", key, nil, gcpEmptyType))
}

func handleListBitbucketRepos(w http.ResponseWriter, r *http.Request) {
	key := cbConfigKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), "bitbucketServerConfigs", sim.PathParam(r, "config"))
	if _, ok := cbBitbucketConfigs.Get(key); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bitbucketServerConfig %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"bitbucketServerRepositories": []any{}})
}

// updateMaskHas reports whether a comma-separated FieldMask names a field
// (or one of its sub-paths).
func updateMaskHas(mask, field string) bool {
	for _, f := range strings.Split(mask, ",") {
		f = strings.TrimSpace(f)
		if f == field || strings.HasPrefix(f, field+".") {
			return true
		}
	}
	return false
}

func buildTriggerLocation(r *http.Request) string {
	if loc := sim.PathParam(r, "location"); loc != "" {
		return loc
	}
	return "global"
}

func buildTriggerKey(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/triggers/%s", project, location, id)
}

func normalizeBuildTrigger(project, location string, trigger BuildTrigger) BuildTrigger {
	if trigger.ID == "" {
		trigger.ID = sim.NewUUID()
	}
	if trigger.Name == "" {
		trigger.Name = trigger.ID
	}
	// BuildTrigger has no location member; the trigger's location is
	// derived from the request URL per call (buildTriggerLocation) and
	// is encoded in resourceName.
	trigger.ResourceName = buildTriggerKey(project, location, trigger.ID)
	if trigger.CreateTime == "" {
		trigger.CreateTime = nowTimestamp()
	}
	return trigger
}

func handleCreateBuildTrigger(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := buildTriggerLocation(r)
	var trigger BuildTrigger
	if err := sim.ReadJSON(r, &trigger); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid trigger body: %v", err)
		return
	}
	trigger = normalizeBuildTrigger(project, location, trigger)
	cbTriggers.Put(trigger.ResourceName, trigger)
	sim.WriteJSON(w, http.StatusOK, trigger)
}

func handleListBuildTriggers(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := buildTriggerLocation(r)
	prefix := fmt.Sprintf("projects/%s/locations/%s/triggers/", project, location)
	triggers := cbTriggers.Filter(func(t BuildTrigger) bool {
		return strings.HasPrefix(t.ResourceName, prefix)
	})
	sort.Slice(triggers, func(i, j int) bool { return triggers[i].Name < triggers[j].Name })
	page, next, ok := paginateList(w, r, triggers)
	if !ok {
		return
	}
	resp := map[string]any{"triggers": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleGetBuildTrigger(w http.ResponseWriter, r *http.Request) {
	key := buildTriggerKey(sim.PathParam(r, "project"), buildTriggerLocation(r), sim.PathParam(r, "trigger"))
	trigger, ok := cbTriggers.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "trigger %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, trigger)
}

func handleUpdateBuildTrigger(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := buildTriggerLocation(r)
	id := sim.PathParam(r, "trigger")
	key := buildTriggerKey(project, location, id)
	var trigger BuildTrigger
	if err := sim.ReadJSON(r, &trigger); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid trigger body: %v", err)
		return
	}
	// Honor updateMask (a documented triggers.patch query param): real Cloud Build
	// merges only the masked top-level fields into the existing trigger;
	// terraform-provider-google always sends one, so without this a masked PATCH
	// dropped every unlisted field and the next plan showed drift.
	if mask := r.URL.Query().Get("updateMask"); mask != "" {
		if prior, ok := cbTriggers.Get(key); ok {
			fields := strings.Split(mask, ",")
			has := func(p string) bool {
				for _, f := range fields {
					f = strings.TrimSpace(f)
					if f == p || strings.HasPrefix(f, p+".") {
						return true
					}
				}
				return false
			}
			merged := prior
			if has("name") {
				merged.Name = trigger.Name
			}
			if has("description") {
				merged.Description = trigger.Description
			}
			if has("filename") {
				merged.Filename = trigger.Filename
			}
			if has("disabled") {
				merged.Disabled = trigger.Disabled
			}
			if has("ignoredFiles") {
				merged.IgnoredFiles = trigger.IgnoredFiles
			}
			if has("includedFiles") {
				merged.IncludedFiles = trigger.IncludedFiles
			}
			if has("substitutions") {
				merged.Substitutions = trigger.Substitutions
			}
			if has("tags") {
				merged.Tags = trigger.Tags
			}
			if has("approvalConfig") {
				merged.ApprovalConfig = trigger.ApprovalConfig
			}
			if has("triggerTemplate") {
				merged.TriggerTemplate = trigger.TriggerTemplate
			}
			if has("gitFileSource") {
				merged.GitFileSource = trigger.GitFileSource
			}
			if has("sourceToBuild") {
				merged.SourceToBuild = trigger.SourceToBuild
			}
			if has("repositoryEventConfig") {
				merged.RepositoryEventConfig = trigger.RepositoryEventConfig
			}
			if has("github") {
				merged.Github = trigger.Github
			}
			if has("build") {
				merged.Build = trigger.Build
			}
			trigger = merged
		}
	}
	trigger.ID = id
	if prior, ok := cbTriggers.Get(key); ok {
		trigger.CreateTime = prior.CreateTime
	}
	trigger = normalizeBuildTrigger(project, location, trigger)
	cbTriggers.Put(key, trigger)
	sim.WriteJSON(w, http.StatusOK, trigger)
}

func handleDeleteBuildTrigger(w http.ResponseWriter, r *http.Request) {
	cbTriggers.Delete(buildTriggerKey(sim.PathParam(r, "project"), buildTriggerLocation(r), sim.PathParam(r, "trigger")))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// executeCancellableBuild runs a build's steps under a context registered in
// cbRunning against the build ID, so CancelBuild — arriving on another
// connection while the steps run — terminates the running `docker` process
// instead of only relabelling the record. The build's own verdict is
// subordinate to a cancellation: a step killed mid-flight reports a failure,
// and reporting that failure would erase the cancel the client asked for, so
// the record the cancel wrote wins.
func executeCancellableBuild(ctx context.Context, b Build) Build {
	timeout := cloudBuildDefaultTimeout
	if b.Timeout != "" {
		d, err := time.ParseDuration(b.Timeout)
		if err != nil || d <= 0 {
			cbBuilds.Update(b.ID, func(stored *Build) {
				stored.Status = "FAILURE"
				stored.StatusDetail = fmt.Sprintf("invalid build timeout %q", b.Timeout)
				stored.FinishTime = time.Now().UTC().Format(time.RFC3339)
			})
			stored, _ := cbBuilds.Get(b.ID)
			return stored
		}
		timeout = d
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Register inside the record's own update, so a cancel either lands first
	// and the steps never start, or lands after and finds the entry to stop
	// them.
	cancelledFirst := false
	cbBuilds.Update(b.ID, func(stored *Build) {
		if stored.Status == "CANCELLED" {
			cancelledFirst = true
			return
		}
		cbRunning.Store(b.ID, cancel)
	})
	if cancelledFirst {
		stored, _ := cbBuilds.Get(b.ID)
		return stored
	}
	defer cbRunning.Delete(b.ID)
	// The timeout ticks from startTime, which executeBuild stamps as it
	// begins.
	stepsCtx, stopTimer := context.WithTimeout(ctx, timeout)
	defer stopTimer()

	result := executeBuild(stepsCtx, b)
	if errors.Is(stepsCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		result.Status = "TIMEOUT"
		result.StatusDetail = fmt.Sprintf("build exceeded its timeout of %s", timeout)
		result.FinishTime = time.Now().UTC().Format(time.RFC3339)
	}

	// Settle the record in one update so a cancel landing between reading the
	// stored build and writing the result cannot be lost: a build the cancel
	// already settled keeps the cancel's verdict, and the step's own failure —
	// which is what a terminated step reports — never overwrites it.
	cbBuilds.Update(b.ID, func(stored *Build) {
		// The step statuses and timings were recorded on the stored build as
		// its steps ran. result is the copy taken before any of them did, so
		// the run's own step state is carried over rather than written out —
		// a cancelled build reported a step that had never started otherwise.
		steps := stored.Steps
		cancelled := stored.Status == "CANCELLED"
		if cancelled {
			result.Status = stored.Status
			result.StatusDetail = stored.StatusDetail
			result.FinishTime = stored.FinishTime
		}
		*stored = result
		stored.Steps = steps
		if cancelled {
			// A step the cancel terminated reports CANCELLED — not the
			// failure its killed process returned, and not the WORKING it was
			// interrupted in. The failure is an artifact of the kill for the
			// same reason the build's own is: a step that had really failed
			// would have settled the build before a cancel could land, and
			// CancelBuild only moves a build that has not settled.
			stamp := time.Now().UTC().Format(time.RFC3339)
			for _, step := range stored.Steps {
				if step == nil {
					continue
				}
				switch step.Status {
				case "", "PENDING", "QUEUED", "WORKING", "FAILURE":
					step.Status = "CANCELLED"
					if step.Timing == nil {
						step.Timing = &BuildTiming{}
					}
					if step.Timing.EndTime == "" {
						step.Timing.EndTime = stamp
					}
				}
			}
		}
	})
	return result
}

// cancelCloudBuild implements CancelBuild: it moves a build that has not
// settled to CANCELLED and terminates the steps it is running. Cancelling a
// build that has already finished leaves its recorded verdict alone, which is
// what "cancels a build in progress" leaves for a build that is not.
func cancelCloudBuild(id string) (Build, bool) {
	cbBuilds.Update(id, func(b *Build) {
		if b.Status == "QUEUED" || b.Status == "WORKING" {
			b.Status = "CANCELLED"
			b.FinishTime = time.Now().UTC().Format(time.RFC3339)
		}
	})
	build, ok := cbBuilds.Get(id)
	if !ok {
		return Build{}, false
	}
	if build.Status == "CANCELLED" {
		if v, loaded := cbRunning.Load(id); loaded {
			if cancel, isFunc := v.(context.CancelFunc); isFunc {
				cancel()
			}
		}
	}
	return build, true
}

// handleCloudBuildCancelOperation answers cloudbuild.operations.cancel for a
// build operation (operations/build/{project}/{id}). Cloud Build's build
// operations are a projection of the build record, so cancelling the operation
// is cancelling the build: the steps stop and the record settles CANCELLED.
// google.longrunning CancelOperation answers google.protobuf.Empty.
func handleCloudBuildCancelOperation(w http.ResponseWriter, id string) {
	if _, ok := cancelCloudBuild(id); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation for build %s not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cloudBuildOperationError is the error a settled build's operation carries.
// A cancelled build reports google.rpc.Code.CANCELLED, which is the code
// AIP-151 says a successfully cancelled operation ends up with; a build that
// ran out of time reports DEADLINE_EXCEEDED, and any other unsuccessful build
// reports INTERNAL with the build's own status detail.
func cloudBuildOperationError(b Build) *BuildError {
	switch b.Status {
	case "CANCELLED":
		return &BuildError{Code: 1, Message: "The operation was cancelled."}
	case "TIMEOUT":
		return &BuildError{Code: 4, Message: b.StatusDetail}
	}
	return &BuildError{Code: 13, Message: b.StatusDetail}
}

// executeBuild runs the build steps against the source context and
// returns the final build record with status + finishTime populated.
// Matches the real Cloud Build behavior: downloads source from GCS,
// extracts it, executes each step (currently only gcr.io/cloud-builders/docker),
// expands secretEnv via AvailableSecrets → Secret Manager.
func executeBuild(ctx context.Context, b Build) Build {
	b.StartTime = time.Now().UTC().Format(time.RFC3339)
	b.Status = "WORKING"
	// Real Cloud Build moves a build QUEUED → WORKING before its first step
	// runs, and that transition is what tells a client the build is live and
	// cancellable. Record it, but never over a cancel that already landed.
	cbBuilds.Update(b.ID, func(stored *Build) {
		if stored.Status == "QUEUED" {
			stored.Status = "WORKING"
			stored.StartTime = b.StartTime
		}
	})

	fail := func(msg string) Build {
		b.Status = "FAILURE"
		b.StatusDetail = msg
		b.FinishTime = time.Now().UTC().Format(time.RFC3339)
		return b
	}

	if b.Source == nil || b.Source.StorageSource == nil {
		return fail("source.storageSource is required")
	}

	// Fetch the tarball from sim GCS (gcs.go on-disk + sim.Store metadata).
	data, err := GCSObjectBytes(b.Source.StorageSource.Bucket, b.Source.StorageSource.Object)
	if err != nil {
		return fail(fmt.Sprintf("fetch source object %s in bucket %s: %v",
			b.Source.StorageSource.Object, b.Source.StorageSource.Bucket, err))
	}

	// Extract to a temp dir.
	workDir, err := os.MkdirTemp("", "sim-cloudbuild-*")
	if err != nil {
		return fail(fmt.Sprintf("tempdir: %v", err))
	}
	defer os.RemoveAll(workDir)

	if err := archive.ExtractTar(bytes.NewReader(data), workDir, cloudBuildWorkerDiskBytes); err != nil {
		return fail(fmt.Sprintf("extract source: %v", err))
	}
	dockerConfigDir, err := cloudBuildDockerConfig(b, workDir)
	if err != nil {
		return fail(fmt.Sprintf("docker configuration: %v", err))
	}
	defer os.RemoveAll(dockerConfigDir)
	dockerEnv := sim.DockerConfigEnv(dockerConfigDir)

	// Resolve Secret Manager references for secretEnv expansion.
	secretValues := map[string]string{}
	if b.AvailableSecrets != nil {
		for _, sm := range b.AvailableSecrets.SecretManager {
			payload, err := resolveSecretManagerReference(sm.VersionName)
			if err != nil {
				return fail(fmt.Sprintf("resolve secret %s: %v", sm.VersionName, err))
			}
			secretValues[sm.Env] = string(payload)
		}
	}

	// Execute each build step. Only gcr.io/cloud-builders/docker is
	// implemented.
	//
	// A step's status and timing are recorded on the stored build as it runs,
	// because the build's own status is WORKING from before the source is
	// fetched until the last step finishes — the step is where a client sees
	// which part of the build is actually executing.
	markStep := func(i int, status string, started, finished bool) {
		stamp := time.Now().UTC().Format(time.RFC3339)
		cbBuilds.Update(b.ID, func(stored *Build) {
			if i >= len(stored.Steps) || stored.Steps[i] == nil {
				return
			}
			stored.Steps[i].Status = status
			if stored.Steps[i].Timing == nil {
				stored.Steps[i].Timing = &BuildTiming{}
			}
			if started {
				stored.Steps[i].Timing.StartTime = stamp
			}
			if finished {
				stored.Steps[i].Timing.EndTime = stamp
			}
		})
	}
	for i, step := range b.Steps {
		if step == nil {
			continue
		}
		if !strings.HasPrefix(step.Name, "gcr.io/cloud-builders/docker") {
			markStep(i, "FAILURE", false, true)
			return fail(fmt.Sprintf("step %d: builder %q not supported by this simulator (only gcr.io/cloud-builders/docker)",
				i, step.Name))
		}
		markStep(i, "WORKING", true, false)
		if err := runDockerStep(ctx, workDir, step, secretValues, dockerEnv); err != nil {
			markStep(i, "FAILURE", false, true)
			return fail(fmt.Sprintf("step %d (%s %v): %v", i, step.Name, step.Args, err))
		}
		markStep(i, "SUCCESS", false, true)
	}

	b.Status = "SUCCESS"
	b.FinishTime = time.Now().UTC().Format(time.RFC3339)
	return b
}

// runDockerStep executes one `gcr.io/cloud-builders/docker` step.
// Args are the docker sub-command args (e.g. ["build","-t","img","."]).
// secretValues map env-var-name → resolved secret payload; these are
// added to the subprocess env when the step's secretEnv references them.
//
// `docker push` semantics: real Cloud Build pushes the built image to the
// target registry (Artifact Registry / Container Registry / etc.) and the
// cloud's compute later pulls it from there over the standard /v2/ API. The
// sim is faithful to that: the build step's `docker build -t <URL>` tagged the
// image with the target ref, and a `push` step does a real `docker push <URL>`
// then drops the local copy, so the workload pulls from the registry — not a
// local-daemon shortcut. The ref's host routes to the registry's /v2/ (the
// configured AR endpoint / the harness's published sim registry).
func runDockerStep(ctx context.Context, workDir string, step *BuildStep, secretValues map[string]string, dockerEnv []string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker CLI not available: %w", err)
	}
	if len(step.Args) >= 2 && step.Args[0] == "push" {
		target := step.Args[1]
		push := workload.DockerCommand(ctx, append(os.Environ(), dockerEnv...), "push", target)
		if out, err := push.CombinedOutput(); err != nil {
			return fmt.Errorf("docker push %s failed: %w: %s", target, err, strings.TrimSpace(string(out)))
		}
		// Drop the local copy so the run pulls from the registry, not the
		// build host's daemon. Best-effort — a failure here doesn't fail the
		// build (the push already succeeded).
		if out, err := workload.DockerCommand(ctx, nil, "rmi", "-f", target).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "cloudbuild: could not remove local build output %s after push: %v: %s\n",
				target, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	env := append(os.Environ(), dockerEnv...)
	args := step.Args
	if len(args) >= 1 && args[0] == "build" {
		args = append(workload.DockerBuildInvocation(ctx, env), args[1:]...)
	}
	dir, err := cloudBuildStepDir(workDir, step.Dir)
	if err != nil {
		return err
	}
	cmd := workload.DockerCommand(ctx, nil, args...)
	cmd.Dir = dir
	for _, e := range step.Env {
		env = append(env, e)
	}
	for _, secEnvName := range step.SecretEnv {
		if v, ok := secretValues[secEnvName]; ok {
			env = append(env, secEnvName+"="+v)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cloudBuildWorkspace is where Cloud Build mounts the build's source in every
// step's container; the simulator runs steps on the host in workDir instead.
const cloudBuildWorkspace = "/workspace"

// cloudBuildWorkerDiskBytes is the disk of a default-pool Cloud Build worker,
// which bounds how far a source archive can expand.
const cloudBuildWorkerDiskBytes = 100 << 30

// cloudBuildStepDir resolves a step's dir against the host directory that
// stands in for /workspace. Cloud Build resolves a relative dir against
// /workspace; an absolute dir names a path in the step's container, which on
// the simulator's host exists only inside /workspace.
func cloudBuildStepDir(workDir, dir string) (string, error) {
	rel := dir
	if path.IsAbs(dir) {
		var ok bool
		if rel, ok = strings.CutPrefix(path.Clean(dir), cloudBuildWorkspace); !ok || (rel != "" && rel[0] != '/') {
			return "", fmt.Errorf("step dir %q is outside %s, which is all a step on this host can reach", dir, cloudBuildWorkspace)
		}
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "" {
		return workDir, nil
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("step dir %q leaves %s", dir, cloudBuildWorkspace)
	}
	return filepath.Join(workDir, filepath.FromSlash(rel)), nil
}
