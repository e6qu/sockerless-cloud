package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// cbBuildActionHandled serves the build colon-verbs beyond cancel. It reports
// whether it took the action so the cancel fan-in keeps its own answer for
// everything else.
func cbBuildActionHandled(w http.ResponseWriter, r *http.Request, project, id, action string) bool {
	switch action {
	case "retry":
		cbHandleRetryBuild(w, r, project, id)
	case "approve":
		cbHandleApproveBuild(w, r, project, id)
	default:
		return false
	}
	return true
}

// cbHandleRetryBuild starts a new build from the original's specification, the
// way the service does: retrying does not re-run the original record, it
// creates a fresh one that carries the same steps.
func cbHandleRetryBuild(w http.ResponseWriter, r *http.Request, project, id string) {
	original, ok := cbBuilds.Get(id)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "build %s not found", id)
		return
	}
	retried := original
	retried.ID = sim.NewUUID()
	retried.Status = "QUEUED"
	retried.CreateTime = time.Now().UTC().Format(time.RFC3339)
	retried.Name = fmt.Sprintf("projects/%s/locations/global/builds/%s", project, retried.ID)
	cbBuilds.Put(retried.ID, retried)
	cbWriteBuildOperation(w, project, startCloudBuild(retried))
}

// cbHandleApproveBuild records the decision on a build waiting for one. A build
// that was never pending approval has no decision to record.
func cbHandleApproveBuild(w http.ResponseWriter, r *http.Request, project, id string) {
	build, ok := cbBuilds.Get(id)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "build %s not found", id)
		return
	}
	var req struct {
		ApprovalResult struct {
			Decision        string `json:"decision"`
			Comment         string `json:"comment"`
			URL             string `json:"url"`
			ApproverAccount string `json:"approverAccount"`
		} `json:"approvalResult"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	decision := req.ApprovalResult.Decision
	if decision != "APPROVED" && decision != "REJECTED" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"approvalResult.decision must be APPROVED or REJECTED, got %q", decision)
		return
	}
	if build.Approval == nil || build.Approval.State != "PENDING" {
		GCPErrorf(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
			"build %s is not pending approval", id)
		return
	}
	build.Approval.State = decision
	build.Approval.Result = &BuildApprovalResult{
		Decision:        decision,
		Comment:         req.ApprovalResult.Comment,
		URL:             req.ApprovalResult.URL,
		ApproverAccount: req.ApprovalResult.ApproverAccount,
		ApprovalTime:    time.Now().UTC().Format(time.RFC3339),
	}
	cbBuilds.Put(id, build)
	if decision == "REJECTED" {
		build.Status = "CANCELLED"
		cbBuilds.Put(id, build)
		cbWriteBuildOperation(w, project, build)
		return
	}
	cbWriteBuildOperation(w, project, startCloudBuild(build))
}

// cbTriggerActionHandled serves the Cloud Build trigger colon-verbs. Eventarc
// owns the same regional triggers path under /v1, so its fan-in calls this
// first and keeps its IAM verbs for everything this declines.
func cbTriggerActionHandled(w http.ResponseWriter, r *http.Request, project, location, id, action string) bool {
	switch action {
	case "run":
		cbHandleRunTrigger(w, r, project, location, id)
	case "webhook":
		cbHandleTriggerWebhook(w, r, project, location, id)
	default:
		return false
	}
	return true
}

func cbHandleRunTrigger(w http.ResponseWriter, r *http.Request, project, location, id string) {
	trigger, ok := cbTriggers.Get(buildTriggerKey(project, location, id))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "trigger %s not found", id)
		return
	}
	build := trigger.Build
	if build == nil {
		GCPErrorf(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
			"trigger %s declares no inline build to run", id)
		return
	}
	started := *build
	started.ID = sim.NewUUID()
	started.ProjectID = project
	started.Status = "QUEUED"
	started.CreateTime = time.Now().UTC().Format(time.RFC3339)
	started.Name = fmt.Sprintf("projects/%s/locations/global/builds/%s", project, started.ID)
	started.BuildTriggerID = trigger.ID
	cbBuilds.Put(started.ID, started)
	cbWriteBuildOperation(w, project, startCloudBuild(started))
}

// cbHandleTriggerWebhook answers the webhook a trigger exposes. The caller
// names the trigger in the path and presents the secret the trigger declares,
// and the trigger's build starts. The response carries no members, so what a
// caller sees of the build is the build itself — which is exactly why
// answering without starting one reports nothing wrong.
func cbHandleTriggerWebhook(w http.ResponseWriter, r *http.Request, project, location, id string) {
	trigger, ok := cbTriggers.Get(buildTriggerKey(project, location, id))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "trigger %s not found", id)
		return
	}
	if !cbTriggerWebhookSecretMatches(w, r, trigger) {
		return
	}
	if trigger.Disabled {
		GCPErrorf(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
			"trigger %s is disabled", id)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"could not read the delivery: %v", err)
		return
	}
	delivery, _ := cbReadWebhookDelivery(body)
	cbStartTriggeredBuild(r, trigger, delivery)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cbTriggerWebhookSecretMatches authenticates a trigger webhook. The trigger's
// WebhookConfig names a Secret Manager version, and the caller presents its
// payload as the secret query parameter — so the check reads the secret the
// operator actually stored rather than trusting the request.
func cbTriggerWebhookSecretMatches(w http.ResponseWriter, r *http.Request, trigger BuildTrigger) bool {
	reference := ""
	if trigger.WebhookConfig != nil {
		reference, _ = trigger.WebhookConfig["secret"].(string)
	}
	if reference == "" {
		GCPErrorf(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
			"trigger %s declares no webhookConfig, so it has no webhook to call", trigger.ID)
		return false
	}
	payload, err := resolveSecretManagerReference(reference)
	if err != nil {
		GCPErrorf(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
			"the trigger's webhook secret %s could not be read: %v", reference, err)
		return false
	}
	if presented := r.URL.Query().Get("secret"); presented != string(payload) {
		GCPErrorf(w, http.StatusUnauthorized, "UNAUTHENTICATED",
			"the secret does not match the one the trigger declares")
		return false
	}
	return true
}

func cbWriteBuildOperation(w http.ResponseWriter, project string, build Build) {
	sim.WriteJSON(w, http.StatusOK, cbBuildOperation(project, build))
}

// cbBuildTerminal reports whether a build status is one the build does not
// leave: the Build.Status values the API documents after WORKING.
func cbBuildTerminal(status string) bool {
	switch status {
	case "SUCCESS", "FAILURE", "INTERNAL_ERROR", "TIMEOUT", "CANCELLED", "EXPIRED":
		return true
	}
	return false
}

// cbBuildOperation renders the operation that tracks a build. Cloud Build's
// build operations are a view of the build record: the metadata is a
// BuildOperationMetadata carrying the build as it stands, and the operation is
// done once the build reaches a terminal status and its steps have stopped,
// with the Build as its response when it succeeded and an error otherwise.
func cbBuildOperation(project string, build Build) CloudBuildOperation {
	_, stepsRunning := cbRunning.Load(build.ID)
	op := CloudBuildOperation{
		Name: fmt.Sprintf("operations/build/%s/%s", project, build.ID),
		Done: cbBuildTerminal(build.Status) && !stepsRunning,
		Metadata: map[string]any{
			"@type": "type.googleapis.com/google.devtools.cloudbuild.v1.BuildOperationMetadata",
			"build": build,
		},
	}
	if !op.Done {
		return op
	}
	if build.Status == "SUCCESS" {
		op.Response = gcpOperationAny(build, "type.googleapis.com/google.devtools.cloudbuild.v1.Build")
	} else {
		op.Error = cloudBuildOperationError(build)
	}
	return op
}

// startCloudBuild runs a recorded QUEUED build in the background and returns
// it as it stands, which is what CreateBuild and the methods that start a
// build answer with: the operation comes back at once and completes when the
// build ends.
func startCloudBuild(build Build) Build {
	bg.Go(func() { executeCancellableBuild(context.Background(), build) })
	return build
}

func registerCloudBuildRegional(srv *sim.Server) {
	// Regional build create. The global collection was mounted; its regional
	// twin writes the same record under the location the caller named.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/builds", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		var build Build
		if err := sim.ReadJSON(r, &build); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid build body: %v", err)
			return
		}
		build.ID = sim.NewUUID()
		build.ProjectID = project
		build.Status = "QUEUED"
		build.CreateTime = time.Now().UTC().Format(time.RFC3339)
		build.Name = fmt.Sprintf("projects/%s/locations/%s/builds/%s", project, location, build.ID)
		cbBuilds.Put(build.ID, build)
		cbWriteBuildOperation(w, project, startCloudBuild(build))
	})

	// Global trigger colon-verbs. The regional ones arrive through Eventarc's
	// fan-in, which shares the path; nothing else claims the global one.
	srv.HandleFunc("POST /v1/projects/{project}/triggers/{triggerAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		id, action, found := strings.Cut(sim.PathParam(r, "triggerAction"), ":")
		if !found || !cbTriggerActionHandled(w, r, project, "global", id, action) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"unknown trigger action %q", sim.PathParam(r, "triggerAction"))
		}
	})

	// The three webhook receivers a source host posts a delivery to. They
	// differ only in the path the host was given.
	srv.HandleFunc("POST /v1/webhook", cbHandleSharedWebhook)
	srv.HandleFunc("POST /v1/githubDotComWebhook:receive", cbHandleSharedWebhook)
	srv.HandleFunc("POST /v1/locations/{location}/regionalWebhook", cbHandleSharedWebhook)

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{configAction}",
		func(w http.ResponseWriter, r *http.Request) {
			configAction := sim.PathParam(r, "configAction")
			id, action, found := strings.Cut(configAction, ":")
			if !found || action != "removeBitbucketServerConnectedRepository" {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"unknown Bitbucket Server config action %q", configAction)
				return
			}
			name := cbConfigKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"), "bitbucketServerConfigs", id)
			config, ok := cbBitbucketConfigs.Get(name)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Bitbucket Server config %q not found", name)
				return
			}
			// connectedRepository is the repository id itself, not a wrapper
			// around one.
			var req struct {
				ConnectedRepository struct {
					ProjectKey string `json:"projectKey"`
					RepoSlug   string `json:"repoSlug"`
				} `json:"connectedRepository"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			target := req.ConnectedRepository.ProjectKey + "/" + req.ConnectedRepository.RepoSlug
			kept := make([]map[string]any, 0, len(config.ConnectedRepositories))
			removed := false
			for _, repo := range config.ConnectedRepositories {
				if cbConnectedRepoID(repo) == target {
					removed = true
					continue
				}
				kept = append(kept, repo)
			}
			if !removed {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"repository %q is not connected to %q", target, name)
				return
			}
			config.ConnectedRepositories = kept
			cbBitbucketConfigs.Put(name, config)
			sim.WriteJSON(w, http.StatusOK, map[string]any{})
		})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}/connectedRepositories:batchCreate",
		func(w http.ResponseWriter, r *http.Request) {
			created := nowTimestamp()
			name := cbConfigKey(sim.PathParam(r, "project"), sim.PathParam(r, "location"),
				"bitbucketServerConfigs", sim.PathParam(r, "config"))
			config, ok := cbBitbucketConfigs.Get(name)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Bitbucket Server config %q not found", name)
				return
			}
			var req struct {
				Requests []struct {
					Parent                             string `json:"parent"`
					BitbucketServerConnectedRepository struct {
						Parent string `json:"parent"`
						Repo   struct {
							ProjectKey string `json:"projectKey"`
							RepoSlug   string `json:"repoSlug"`
						} `json:"repo"`
					} `json:"bitbucketServerConnectedRepository"`
				} `json:"requests"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if len(req.Requests) == 0 {
				GCPError(w, http.StatusBadRequest, "requests must not be empty", "INVALID_ARGUMENT")
				return
			}
			connected := make([]map[string]any, 0, len(req.Requests))
			for _, one := range req.Requests {
				repo := one.BitbucketServerConnectedRepository.Repo
				// The config carries repository ids; the response carries the
				// connected-repository resources. Two shapes, one write.
				id := map[string]any{"projectKey": repo.ProjectKey, "repoSlug": repo.RepoSlug}
				if !cbRepoConnected(config.ConnectedRepositories, repo.ProjectKey+"/"+repo.RepoSlug) {
					config.ConnectedRepositories = append(config.ConnectedRepositories, id)
				}
				// status is a google.rpc.Status; a connection that succeeded
				// carries code OK, which renders as the empty message.
				connected = append(connected, map[string]any{
					"parent": name,
					"repo":   id,
					"status": map[string]any{},
				})
			}
			cbBitbucketConfigs.Put(name, config)
			sim.WriteJSON(w, http.StatusOK, newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"),
				map[string]any{"bitbucketServerConnectedRepositories": connected},
				"type.googleapis.com/google.devtools.cloudbuild.v1.BatchCreateBitbucketServerConnectedRepositoriesResponse",
				gcpFixedOperationMetadata(map[string]any{
					"@type":        "type.googleapis.com/google.devtools.cloudbuild.v1.BatchCreateBitbucketServerConnectedRepositoriesResponseMetadata",
					"config":       name,
					"createTime":   created,
					"completeTime": nowTimestamp(),
				})))
		})
}

// cbConnectedRepoID reads "<projectKey>/<repoSlug>" out of a stored id.
func cbConnectedRepoID(entry map[string]any) string {
	projectKey, _ := entry["projectKey"].(string)
	repoSlug, _ := entry["repoSlug"].(string)
	return projectKey + "/" + repoSlug
}

func cbRepoConnected(entries []map[string]any, want string) bool {
	for _, entry := range entries {
		if cbConnectedRepoID(entry) == want {
			return true
		}
	}
	return false
}
