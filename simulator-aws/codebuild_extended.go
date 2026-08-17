package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// AWS CodeBuild — extended control-plane surface (build batches, compute fleets,
// sandboxes, webhooks, report test cases / code coverage / trends, resource
// policies, command executions, curated images, project visibility, cache
// invalidation, and shared-resource listings). All AWS JSON 1.1
// (X-Amz-Target: CodeBuild_20161006.<Op>), matching the real wire shapes the
// AWS SDK Go v2 and the `aws` CLI parse.

// --- Build batches -----------------------------------------------------------

// CBBuildBatch mirrors the CodeBuild BuildBatch shape. A batch settles SUCCEEDED
// the same way a single build does; the sim tracks its terminal state on a real
// store rather than fabricating it.
type CBBuildBatch struct {
	ID                    string         `json:"id"`
	Arn                   string         `json:"arn"`
	ProjectName           string         `json:"projectName"`
	BuildBatchStatus      string         `json:"buildBatchStatus"`
	CurrentPhase          string         `json:"currentPhase,omitempty"`
	StartTime             float64        `json:"startTime"`
	EndTime               float64        `json:"endTime"`
	Source                map[string]any `json:"source,omitempty"`
	Environment           map[string]any `json:"environment,omitempty"`
	ServiceRole           string         `json:"serviceRole,omitempty"`
	BuildTimeoutInMinutes int            `json:"buildTimeoutInMinutes,omitempty"`
	Complete              bool           `json:"complete"`
	Initiator             string         `json:"initiator,omitempty"`
	BuildBatchNumber      int64          `json:"buildBatchNumber,omitempty"`
	Phases                []CBPhase      `json:"phases,omitempty"`
	// Seq orders ListBuildBatches by start order; sim-internal, not wire.
	Seq int64 `json:"-"`
	// BuildPlan and RuntimeEnvironment preserve execution inputs across the
	// narrow pre-container restart window.
	BuildPlan          *cbBuildPlan      `json:"-"`
	RuntimeEnvironment map[string]string `json:"-"`
}

// CBFleet mirrors the CodeBuild Fleet shape (a compute fleet resource keyed by
// ARN). A fleet settles ACTIVE on create.
type CBFleet struct {
	Arn                  string         `json:"arn"`
	Name                 string         `json:"name"`
	ID                   string         `json:"id"`
	Created              float64        `json:"created"`
	LastModified         float64        `json:"lastModified"`
	Status               map[string]any `json:"status"`
	BaseCapacity         int            `json:"baseCapacity,omitempty"`
	EnvironmentType      string         `json:"environmentType,omitempty"`
	ComputeType          string         `json:"computeType,omitempty"`
	ScalingConfiguration map[string]any `json:"scalingConfiguration,omitempty"`
	OverflowBehavior     string         `json:"overflowBehavior,omitempty"`
	ImageID              string         `json:"imageId,omitempty"`
	FleetServiceRole     string         `json:"fleetServiceRole,omitempty"`
	Tags                 []CBTag        `json:"tags,omitempty"`
}

// CBSandbox mirrors the CodeBuild Sandbox shape. A sandbox settles RUNNING on
// start (the real terminal session state for an interactive sandbox).
type CBSandbox struct {
	ID               string            `json:"id"`
	Arn              string            `json:"arn"`
	ProjectName      string            `json:"projectName"`
	RequestTime      float64           `json:"requestTime"`
	StartTime        float64           `json:"startTime"`
	EndTime          float64           `json:"endTime,omitempty"`
	Status           string            `json:"status"`
	Environment      map[string]any    `json:"environment,omitempty"`
	ServiceRole      string            `json:"serviceRole,omitempty"`
	CurrentSession   *CBSandboxSession `json:"currentSession,omitempty"`
	TimeoutInMinutes int               `json:"timeoutInMinutes,omitempty"`
	Seq              int64             `json:"-"`
}

// CBSandboxSession mirrors the SandboxSession shape carried inside a sandbox.
type CBSandboxSession struct {
	ID           string         `json:"id"`
	Status       string         `json:"status"`
	StartTime    float64        `json:"startTime"`
	CurrentPhase string         `json:"currentPhase,omitempty"`
	Logs         map[string]any `json:"logs,omitempty"`
}

// CBCommandExecution mirrors the CommandExecution shape: a command run inside a
// sandbox, settling SUCCEEDED from the local process exit status.
type CBCommandExecution struct {
	ID                    string  `json:"id"`
	SandboxID             string  `json:"sandboxId"`
	SandboxArn            string  `json:"sandboxArn,omitempty"`
	SubmitTime            float64 `json:"submitTime"`
	StartTime             float64 `json:"startTime"`
	EndTime               float64 `json:"endTime,omitempty"`
	Status                string  `json:"status"`
	Command               string  `json:"command"`
	Type                  string  `json:"type,omitempty"`
	ExitCode              string  `json:"exitCode,omitempty"`
	StandardOutputContent string  `json:"standardOutputContent,omitempty"`
	StandardErrContent    string  `json:"standardErrContent,omitempty"`
	Seq                   int64   `json:"-"`
}

// CBWebhook mirrors the Webhook shape attached to a project. The real API never
// echoes the secret back beyond import; the sim stores it and returns the
// url/payloadUrl/secret per the shape.
type CBWebhook struct {
	URL          string             `json:"url,omitempty"`
	PayloadURL   string             `json:"payloadUrl,omitempty"`
	Secret       string             `json:"secret,omitempty"`
	BranchFilter string             `json:"branchFilter,omitempty"`
	FilterGroups [][]map[string]any `json:"filterGroups,omitempty"`
	BuildType    string             `json:"buildType,omitempty"`
	Status       string             `json:"status,omitempty"`
	// ProjectName keys the webhook to its project; sim-internal, not wire.
	ProjectName string `json:"-"`
}

var (
	cbBuildBatches sim.Store[CBBuildBatch]
	cbFleets       sim.Store[CBFleet]
	cbSandboxes    sim.Store[CBSandbox]
	cbCommandExecs sim.Store[CBCommandExecution]
	cbWebhooks     sim.Store[CBWebhook]
	cbResourcePols sim.Store[IAMResourcePolicy]
	cbBatchSeq     int64
	cbSandboxSeq   int64
	cbCmdSeq       int64
	cbSeqMu        sync.Mutex
)

func registerCodeBuildExtended(r *sim.AWSRouter, srv *sim.Server) {
	cbBuildBatches = sim.MakeStore[CBBuildBatch](srv.DB(), "codebuild_build_batches")
	cbFleets = sim.MakeStore[CBFleet](srv.DB(), "codebuild_fleets")
	cbSandboxes = sim.MakeStore[CBSandbox](srv.DB(), "codebuild_sandboxes")
	cbCommandExecs = sim.MakeStore[CBCommandExecution](srv.DB(), "codebuild_command_executions")
	cbWebhooks = sim.MakeStore[CBWebhook](srv.DB(), "codebuild_webhooks")
	cbResourcePols = sim.MakeStore[IAMResourcePolicy](srv.DB(), "codebuild_resource_policies")
	cbRebuildSequences()
	if err := cbRecoverBuildBatches(); err != nil {
		panic(fmt.Sprintf("restore AWS CodeBuild build batches: %v", err))
	}

	// Builds
	r.Register("CodeBuild_20161006.BatchDeleteBuilds", handleCBBatchDeleteBuilds)

	// Build batches
	r.Register("CodeBuild_20161006.StartBuildBatch", handleCBStartBuildBatch)
	r.Register("CodeBuild_20161006.StopBuildBatch", handleCBStopBuildBatch)
	r.Register("CodeBuild_20161006.RetryBuildBatch", handleCBRetryBuildBatch)
	r.Register("CodeBuild_20161006.DeleteBuildBatch", handleCBDeleteBuildBatch)
	r.Register("CodeBuild_20161006.BatchGetBuildBatches", handleCBBatchGetBuildBatches)
	r.Register("CodeBuild_20161006.ListBuildBatches", handleCBListBuildBatches)
	r.Register("CodeBuild_20161006.ListBuildBatchesForProject", handleCBListBuildBatchesForProject)

	// Fleets
	r.Register("CodeBuild_20161006.CreateFleet", handleCBCreateFleet)
	r.Register("CodeBuild_20161006.UpdateFleet", handleCBUpdateFleet)
	r.Register("CodeBuild_20161006.DeleteFleet", handleCBDeleteFleet)
	r.Register("CodeBuild_20161006.BatchGetFleets", handleCBBatchGetFleets)
	r.Register("CodeBuild_20161006.ListFleets", handleCBListFleets)

	// Sandboxes
	r.Register("CodeBuild_20161006.StartSandbox", handleCBStartSandbox)
	r.Register("CodeBuild_20161006.StopSandbox", handleCBStopSandbox)
	r.Register("CodeBuild_20161006.StartSandboxConnection", handleCBStartSandboxConnection)
	r.Register("CodeBuild_20161006.BatchGetSandboxes", handleCBBatchGetSandboxes)
	r.Register("CodeBuild_20161006.ListSandboxes", handleCBListSandboxes)
	r.Register("CodeBuild_20161006.ListSandboxesForProject", handleCBListSandboxesForProject)

	// Command executions
	r.Register("CodeBuild_20161006.StartCommandExecution", handleCBStartCommandExecution)
	r.Register("CodeBuild_20161006.BatchGetCommandExecutions", handleCBBatchGetCommandExecutions)
	r.Register("CodeBuild_20161006.ListCommandExecutionsForSandbox", handleCBListCommandExecutionsForSandbox)

	// Webhooks
	r.Register("CodeBuild_20161006.CreateWebhook", handleCBCreateWebhook)
	r.Register("CodeBuild_20161006.UpdateWebhook", handleCBUpdateWebhook)
	r.Register("CodeBuild_20161006.DeleteWebhook", handleCBDeleteWebhook)

	// Reports
	r.Register("CodeBuild_20161006.DeleteReport", handleCBDeleteReport)
	r.Register("CodeBuild_20161006.DescribeTestCases", handleCBDescribeTestCases)
	r.Register("CodeBuild_20161006.DescribeCodeCoverages", handleCBDescribeCodeCoverages)
	r.Register("CodeBuild_20161006.GetReportGroupTrend", handleCBGetReportGroupTrend)

	// Resource policy
	r.Register("CodeBuild_20161006.PutResourcePolicy", handleCBPutResourcePolicy)
	r.Register("CodeBuild_20161006.GetResourcePolicy", handleCBGetResourcePolicy)
	r.Register("CodeBuild_20161006.DeleteResourcePolicy", handleCBDeleteResourcePolicy)

	// Project visibility / cache / curated images / shared listings
	r.Register("CodeBuild_20161006.UpdateProjectVisibility", handleCBUpdateProjectVisibility)
	r.Register("CodeBuild_20161006.InvalidateProjectCache", handleCBInvalidateProjectCache)
	r.Register("CodeBuild_20161006.ListCuratedEnvironmentImages", handleCBListCuratedEnvironmentImages)
	r.Register("CodeBuild_20161006.ListSharedProjects", handleCBListSharedProjects)
	r.Register("CodeBuild_20161006.ListSharedReportGroups", handleCBListSharedReportGroups)
}

func cbRebuildSequences() {
	cbSeqMu.Lock()
	defer cbSeqMu.Unlock()
	cbBatchSeq = 0
	cbSandboxSeq = 0
	cbCmdSeq = 0
	for _, batch := range cbBuildBatches.List() {
		if batch.Seq > cbBatchSeq {
			cbBatchSeq = batch.Seq
		}
	}
	for _, sandbox := range cbSandboxes.List() {
		if sandbox.Seq > cbSandboxSeq {
			cbSandboxSeq = sandbox.Seq
		}
	}
	for _, execution := range cbCommandExecs.List() {
		if execution.Seq > cbCmdSeq {
			cbCmdSeq = execution.Seq
		}
	}
}

func cbRecoverBuildBatches() error {
	for _, batch := range cbBuildBatches.List() {
		if batch.BuildBatchStatus != "IN_PROGRESS" {
			continue
		}
		project, ok := cbProjects.Get(batch.ProjectName)
		if !ok {
			return fmt.Errorf("build batch %s references missing project %s", batch.ID, batch.ProjectName)
		}
		adopted, err := cbAdoptBuildWorkload(
			batch.ID,
			"build batch",
			batch.StartTime,
			cbBuildTimeout(project),
			cbCompleteBuildBatch,
		)
		if err != nil {
			return err
		}
		if adopted {
			continue
		}
		if batch.BuildPlan == nil {
			return fmt.Errorf("build batch %s has neither a workload container nor a persisted build plan", batch.ID)
		}
		go cbRunBuildBatch(batch.ID, project, *batch.BuildPlan, batch.RuntimeEnvironment)
	}
	return nil
}

func cbNextSeq(p *int64) int64 {
	cbSeqMu.Lock()
	defer cbSeqMu.Unlock()
	*p++
	return *p
}

// --- BatchDeleteBuilds -------------------------------------------------------

func handleCBBatchDeleteBuilds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	deleted := []string{}
	type notDeleted struct {
		ID         string `json:"id"`
		StatusCode string `json:"statusCode"`
	}
	notDel := []notDeleted{}
	for _, id := range req.IDs {
		if _, ok := cbBuilds.Get(id); ok {
			cbBuilds.Delete(id)
			deleted = append(deleted, id)
		} else {
			notDel = append(notDel, notDeleted{ID: id, StatusCode: "BUILD_NOT_FOUND"})
		}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"buildsDeleted":    deleted,
		"buildsNotDeleted": notDel,
	})
}

// --- Build batches -----------------------------------------------------------

func handleCBStartBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName       string `json:"projectName"`
		BuildspecOverride string `json:"buildspecOverride"`
		SourceVersion     string `json:"sourceVersion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	p, ok := cbProjects.Get(req.ProjectName)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+req.ProjectName)
		return
	}
	plan, err := cbBuildPlanForProject(p, req.BuildspecOverride, req.SourceVersion)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	batchID := req.ProjectName + ":" + uuid.New().String()
	now := cbEpochNow()
	runtimeEnvironment := cbEnvironment(p.Environment, nil)
	batch := CBBuildBatch{
		ID:                 batchID,
		Arn:                cbARN("build-batch/" + batchID),
		ProjectName:        req.ProjectName,
		BuildBatchStatus:   "IN_PROGRESS",
		CurrentPhase:       "SUBMITTED",
		StartTime:          now,
		EndTime:            now,
		Source:             p.Source,
		Environment:        p.Environment,
		ServiceRole:        p.ServiceRole,
		Complete:           false,
		BuildBatchNumber:   1,
		Seq:                cbNextSeq(&cbBatchSeq),
		BuildPlan:          &plan,
		RuntimeEnvironment: runtimeEnvironment,
		Phases: []CBPhase{
			{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: now, EndTime: now, DurationInSeconds: 0},
			{PhaseType: "COMBINE_ARTIFACTS", PhaseStatus: "IN_PROGRESS", StartTime: now},
		},
	}
	cbBuildBatches.Put(batchID, batch)
	go cbRunBuildBatch(batchID, p, plan, runtimeEnvironment)
	cbWriteJSON(w, http.StatusOK, map[string]any{"buildBatch": batch})
}

func cbRunBuildBatch(batchID string, project CBProject, plan cbBuildPlan, environment map[string]string) {
	exitCode, reason := cbRunCommands(batchID, project, plan, environment)
	cbCompleteBuildBatch(batchID, exitCode, reason)
}

func cbCompleteBuildBatch(batchID string, exitCode int, reason string) {
	cbMu.Lock()
	defer cbMu.Unlock()
	batch, ok := cbBuildBatches.Get(batchID)
	if !ok || batch.BuildBatchStatus != "IN_PROGRESS" {
		return
	}
	now := cbEpochNow()
	status := "SUCCEEDED"
	if exitCode != 0 {
		status = "FAILED"
	}
	batch.BuildBatchStatus = status
	batch.CurrentPhase = "COMPLETED"
	batch.EndTime = now
	batch.Complete = true
	batch.Phases = []CBPhase{
		{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: batch.StartTime, EndTime: batch.StartTime, DurationInSeconds: 0},
		{PhaseType: "COMBINE_ARTIFACTS", PhaseStatus: status, StartTime: batch.StartTime, EndTime: now, DurationInSeconds: now - batch.StartTime},
		{PhaseType: "COMPLETED", PhaseStatus: status, StartTime: now, EndTime: now, DurationInSeconds: 0},
	}
	if reason != "" {
		batch.Phases[1].Contexts = []CBPhaseContext{{StatusCode: "COMMAND_EXECUTION_ERROR", Message: reason}}
	}
	cbBuildBatches.Put(batchID, batch)
}

func handleCBStopBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	batch, ok := cbResolveBuildBatch(req.ID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Build batch not found: "+req.ID)
		return
	}
	batch = cbStopBuildBatchByID(batch.ID)
	cbWriteJSON(w, http.StatusOK, map[string]any{"buildBatch": batch})
}

func cbStopBuildBatchByID(batchID string) CBBuildBatch {
	cbMu.Lock()
	defer cbMu.Unlock()
	batch, ok := cbResolveBuildBatch(batchID)
	if !ok {
		return CBBuildBatch{}
	}
	if batch.BuildBatchStatus == "IN_PROGRESS" {
		cbCancelBuild(batchID)
		now := cbEpochNow()
		batch.BuildBatchStatus = "STOPPED"
		batch.CurrentPhase = "STOPPED"
		batch.EndTime = now
		batch.Complete = true
		cbBuildBatches.Put(batch.ID, batch)
	}
	return batch
}

func handleCBRetryBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	prior, ok := cbResolveBuildBatch(req.ID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Build batch not found: "+req.ID)
		return
	}
	project, ok := cbProjects.Get(prior.ProjectName)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+prior.ProjectName)
		return
	}
	plan, err := cbBuildPlanForProject(project, "", "")
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	// RetryBuildBatch starts a fresh batch of the same project (a new id),
	// mirroring real CodeBuild which produces a new batch resource.
	batchID := prior.ProjectName + ":" + uuid.New().String()
	now := cbEpochNow()
	runtimeEnvironment := cbEnvironment(project.Environment, nil)
	batch := CBBuildBatch{
		ID:                 batchID,
		Arn:                cbARN("build-batch/" + batchID),
		ProjectName:        prior.ProjectName,
		BuildBatchStatus:   "IN_PROGRESS",
		CurrentPhase:       "SUBMITTED",
		StartTime:          now,
		EndTime:            now,
		Source:             prior.Source,
		Environment:        prior.Environment,
		ServiceRole:        prior.ServiceRole,
		BuildBatchNumber:   prior.BuildBatchNumber + 1,
		Seq:                cbNextSeq(&cbBatchSeq),
		BuildPlan:          &plan,
		RuntimeEnvironment: runtimeEnvironment,
		Phases: []CBPhase{
			{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: now, EndTime: now, DurationInSeconds: 0},
			{PhaseType: "COMBINE_ARTIFACTS", PhaseStatus: "IN_PROGRESS", StartTime: now},
		},
	}
	cbBuildBatches.Put(batchID, batch)
	go cbRunBuildBatch(batchID, project, plan, runtimeEnvironment)
	cbWriteJSON(w, http.StatusOK, map[string]any{"buildBatch": batch})
}

func handleCBDeleteBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	batch, ok := cbResolveBuildBatch(req.ID)
	if !ok {
		// DeleteBuildBatch on a missing batch returns the unmatched id in
		// buildsNotDeleted, not an error (matches real CodeBuild).
		cbWriteJSON(w, http.StatusOK, map[string]any{
			"statusCode": "BUILD_BATCH_NOT_FOUND",
			"buildsNotDeleted": []map[string]any{
				{"id": req.ID, "statusCode": "BUILD_BATCH_NOT_FOUND"},
			},
		})
		return
	}
	cbBuildBatches.Delete(batch.ID)
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"statusCode":       "DELETED",
		"buildsDeleted":    []string{},
		"buildsNotDeleted": []map[string]any{},
	})
}

func handleCBBatchGetBuildBatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	found := []CBBuildBatch{}
	notFound := []string{}
	for _, id := range req.IDs {
		if b, ok := cbResolveBuildBatch(id); ok {
			found = append(found, b)
		} else {
			notFound = append(notFound, id)
		}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"buildBatches":         found,
		"buildBatchesNotFound": notFound,
	})
}

func handleCBListBuildBatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cbWriteBuildBatchIDsPage(w, cbBuildBatches.List(), "", req.SortOrder, req.MaxResults, req.NextToken)
}

func handleCBListBuildBatchesForProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName string `json:"projectName"`
		SortOrder   string `json:"sortOrder"`
		MaxResults  int    `json:"maxResults"`
		NextToken   string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cbWriteBuildBatchIDsPage(w, cbBuildBatches.List(), req.ProjectName, req.SortOrder, req.MaxResults, req.NextToken)
}

func cbWriteBuildBatchIDsPage(w http.ResponseWriter, all []CBBuildBatch, project, sortOrder string, maxResults int, nextToken string) {
	var batches []CBBuildBatch
	for _, b := range all {
		if project == "" || b.ProjectName == project {
			batches = append(batches, b)
		}
	}
	ascending := strings.EqualFold(sortOrder, "ASCENDING")
	sort.Slice(batches, func(i, j int) bool {
		if ascending {
			return batches[i].Seq < batches[j].Seq
		}
		return batches[i].Seq > batches[j].Seq
	})
	ids := make([]string, 0, len(batches))
	for _, b := range batches {
		ids = append(ids, b.ID)
	}
	page, nextTok := awsPage(ids, nextToken, maxResults, 100)
	resp := map[string]any{"ids": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// cbResolveBuildBatch finds a batch by id or ARN.
func cbResolveBuildBatch(idOrARN string) (CBBuildBatch, bool) {
	if b, ok := cbBuildBatches.Get(idOrARN); ok {
		return b, true
	}
	for _, b := range cbBuildBatches.List() {
		if b.Arn == idOrARN {
			return b, true
		}
	}
	return CBBuildBatch{}, false
}

// --- Fleets ------------------------------------------------------------------

func handleCBCreateFleet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                 string         `json:"name"`
		BaseCapacity         int            `json:"baseCapacity"`
		EnvironmentType      string         `json:"environmentType"`
		ComputeType          string         `json:"computeType"`
		ScalingConfiguration map[string]any `json:"scalingConfiguration"`
		OverflowBehavior     string         `json:"overflowBehavior"`
		ImageID              string         `json:"imageId"`
		FleetServiceRole     string         `json:"fleetServiceRole"`
		Tags                 []CBTag        `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		cbWriteError(w, "InvalidInputException", "name is required")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	for _, f := range cbFleets.List() {
		if f.Name == req.Name {
			cbWriteError(w, "ResourceAlreadyExistsException", "Fleet already exists: "+req.Name)
			return
		}
	}
	id := req.Name + ":" + uuid.New().String()
	arn := cbARN("fleet/" + id)
	now := cbEpochNow()
	fleet := CBFleet{
		Arn:                  arn,
		Name:                 req.Name,
		ID:                   id,
		Created:              now,
		LastModified:         now,
		Status:               map[string]any{"statusCode": "ACTIVE", "message": "Fleet is active"},
		BaseCapacity:         req.BaseCapacity,
		EnvironmentType:      req.EnvironmentType,
		ComputeType:          req.ComputeType,
		ScalingConfiguration: req.ScalingConfiguration,
		OverflowBehavior:     req.OverflowBehavior,
		ImageID:              req.ImageID,
		FleetServiceRole:     req.FleetServiceRole,
		Tags:                 req.Tags,
	}
	cbFleets.Put(arn, fleet)
	cbWriteJSON(w, http.StatusOK, map[string]any{"fleet": fleet})
}

func handleCBUpdateFleet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn                  string         `json:"arn"`
		BaseCapacity         *int           `json:"baseCapacity"`
		EnvironmentType      string         `json:"environmentType"`
		ComputeType          string         `json:"computeType"`
		ScalingConfiguration map[string]any `json:"scalingConfiguration"`
		OverflowBehavior     string         `json:"overflowBehavior"`
		ImageID              string         `json:"imageId"`
		FleetServiceRole     string         `json:"fleetServiceRole"`
		Tags                 []CBTag        `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	fleet, ok := cbResolveFleet(req.Arn)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Fleet not found: "+req.Arn)
		return
	}
	if req.BaseCapacity != nil {
		fleet.BaseCapacity = *req.BaseCapacity
	}
	if req.EnvironmentType != "" {
		fleet.EnvironmentType = req.EnvironmentType
	}
	if req.ComputeType != "" {
		fleet.ComputeType = req.ComputeType
	}
	if req.ScalingConfiguration != nil {
		fleet.ScalingConfiguration = req.ScalingConfiguration
	}
	if req.OverflowBehavior != "" {
		fleet.OverflowBehavior = req.OverflowBehavior
	}
	if req.ImageID != "" {
		fleet.ImageID = req.ImageID
	}
	if req.FleetServiceRole != "" {
		fleet.FleetServiceRole = req.FleetServiceRole
	}
	if req.Tags != nil {
		fleet.Tags = req.Tags
	}
	fleet.LastModified = cbEpochNow()
	cbFleets.Put(fleet.Arn, fleet)
	cbWriteJSON(w, http.StatusOK, map[string]any{"fleet": fleet})
}

func handleCBDeleteFleet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"arn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	if fleet, ok := cbResolveFleet(req.Arn); ok {
		cbFleets.Delete(fleet.Arn)
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCBBatchGetFleets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	found := []CBFleet{}
	notFound := []string{}
	for _, nameOrARN := range req.Names {
		if f, ok := cbResolveFleet(nameOrARN); ok {
			found = append(found, f)
		} else {
			notFound = append(notFound, nameOrARN)
		}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"fleets":         found,
		"fleetsNotFound": notFound,
	})
}

func handleCBListFleets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := cbFleets.List()
	arns := make([]string, 0, len(all))
	for _, f := range all {
		arns = append(arns, f.Arn)
	}
	sort.Strings(arns)
	if strings.EqualFold(req.SortOrder, "DESCENDING") {
		reverseStrings(arns)
	}
	page, nextTok := awsPage(arns, req.NextToken, req.MaxResults, 100)
	resp := map[string]any{"fleets": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// cbResolveFleet finds a fleet by ARN or name.
func cbResolveFleet(nameOrARN string) (CBFleet, bool) {
	if f, ok := cbFleets.Get(nameOrARN); ok {
		return f, true
	}
	for _, f := range cbFleets.List() {
		if f.Arn == nameOrARN || f.Name == nameOrARN {
			return f, true
		}
	}
	return CBFleet{}, false
}

// --- Sandboxes ---------------------------------------------------------------

func handleCBStartSandbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName string `json:"projectName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	var env map[string]any
	var serviceRole string
	if req.ProjectName != "" {
		p, ok := cbProjects.Get(req.ProjectName)
		if !ok {
			cbWriteError(w, "ResourceNotFoundException", "Project not found: "+req.ProjectName)
			return
		}
		env = p.Environment
		serviceRole = p.ServiceRole
	}
	id := uuid.New().String()
	now := cbEpochNow()
	sb := CBSandbox{
		ID:          id,
		Arn:         cbARN("sandbox/" + id),
		ProjectName: req.ProjectName,
		RequestTime: now,
		StartTime:   now,
		Status:      "RUNNING",
		Environment: env,
		ServiceRole: serviceRole,
		Seq:         cbNextSeq(&cbSandboxSeq),
		CurrentSession: &CBSandboxSession{
			ID:           uuid.New().String(),
			Status:       "RUNNING",
			StartTime:    now,
			CurrentPhase: "PROVISIONING",
			Logs:         cbLogsLocation(),
		},
	}
	cbSandboxes.Put(id, sb)
	cbWriteJSON(w, http.StatusOK, map[string]any{"sandbox": sb})
}

func handleCBStopSandbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	sb, ok := cbResolveSandbox(req.ID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Sandbox not found: "+req.ID)
		return
	}
	if sb.Status == "RUNNING" {
		now := cbEpochNow()
		sb.Status = "STOPPED"
		sb.EndTime = now
		if sb.CurrentSession != nil {
			sb.CurrentSession.Status = "STOPPED"
		}
		cbSandboxes.Put(sb.ID, sb)
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{"sandbox": sb})
}

func handleCBStartSandboxConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SandboxID string `json:"sandboxId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	sb, ok := cbResolveSandbox(req.SandboxID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Sandbox not found: "+req.SandboxID)
		return
	}
	// StartSandboxConnection returns the SSM session connection details a client
	// uses to attach to the sandbox, shaped exactly like the real SSMSession.
	sessionID := sb.ID + "-" + uuid.New().String()
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"ssmSession": map[string]any{
			"sessionId":  sessionID,
			"streamUrl":  fmt.Sprintf("wss://ssmmessages.us-east-1.amazonaws.com/v1/data-channel/%s?role=publish_subscribe", sessionID),
			"tokenValue": "AAEAAW" + uuid.New().String(),
		},
	})
}

func handleCBBatchGetSandboxes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	found := []CBSandbox{}
	notFound := []string{}
	for _, id := range req.IDs {
		if sb, ok := cbResolveSandbox(id); ok {
			found = append(found, sb)
		} else {
			notFound = append(notFound, id)
		}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"sandboxes":         found,
		"sandboxesNotFound": notFound,
	})
}

func handleCBListSandboxes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cbWriteSandboxIDsPage(w, cbSandboxes.List(), "", req.SortOrder, req.MaxResults, req.NextToken)
}

func handleCBListSandboxesForProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName string `json:"projectName"`
		SortOrder   string `json:"sortOrder"`
		MaxResults  int    `json:"maxResults"`
		NextToken   string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cbWriteSandboxIDsPage(w, cbSandboxes.List(), req.ProjectName, req.SortOrder, req.MaxResults, req.NextToken)
}

func cbWriteSandboxIDsPage(w http.ResponseWriter, all []CBSandbox, project, sortOrder string, maxResults int, nextToken string) {
	var boxes []CBSandbox
	for _, sb := range all {
		if project == "" || sb.ProjectName == project {
			boxes = append(boxes, sb)
		}
	}
	ascending := strings.EqualFold(sortOrder, "ASCENDING")
	sort.Slice(boxes, func(i, j int) bool {
		if ascending {
			return boxes[i].Seq < boxes[j].Seq
		}
		return boxes[i].Seq > boxes[j].Seq
	})
	ids := make([]string, 0, len(boxes))
	for _, sb := range boxes {
		ids = append(ids, sb.ID)
	}
	page, nextTok := awsPage(ids, nextToken, maxResults, 100)
	resp := map[string]any{"ids": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

func cbResolveSandbox(idOrARN string) (CBSandbox, bool) {
	if sb, ok := cbSandboxes.Get(idOrARN); ok {
		return sb, true
	}
	for _, sb := range cbSandboxes.List() {
		if sb.Arn == idOrARN {
			return sb, true
		}
	}
	return CBSandbox{}, false
}

// --- Command executions ------------------------------------------------------

func handleCBStartCommandExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SandboxID string `json:"sandboxId"`
		Command   string `json:"command"`
		Type      string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Command == "" {
		cbWriteError(w, "InvalidInputException", "command is required")
		return
	}
	sb, ok := cbResolveSandbox(req.SandboxID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Sandbox not found: "+req.SandboxID)
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	id := uuid.New().String()
	now := cbEpochNow()
	cmdType := req.Type
	if cmdType == "" {
		cmdType = "SHELL"
	}
	ce := CBCommandExecution{
		ID:         id,
		SandboxID:  sb.ID,
		SandboxArn: sb.Arn,
		SubmitTime: now,
		StartTime:  now,
		Status:     "IN_PROGRESS",
		Command:    req.Command,
		Type:       cmdType,
		Seq:        cbNextSeq(&cbCmdSeq),
	}
	cbCommandExecs.Put(id, ce)
	go cbRunCommandExecution(id, sb, req.Command)
	cbWriteJSON(w, http.StatusOK, map[string]any{"commandExecution": ce})
}

// cbRunCommandExecution runs the command in the sandbox's build environment
// container — the same image a build of the sandbox's project runs in — and
// records the real terminal status, exit code and captured output.
func cbRunCommandExecution(id string, sandbox CBSandbox, command string) {
	image := cbString(sandbox.Environment["image"])
	if image == "" {
		cbCompleteCommandExecution(id, -1, "",
			"sandbox "+sandbox.ID+" has no build environment image to run the command in")
		return
	}
	workDir, err := os.MkdirTemp("", "sockerless-cb-cmd-*")
	if err != nil {
		cbCompleteCommandExecution(id, -1, "", err.Error())
		return
	}
	defer os.RemoveAll(workDir)

	platform, err := localImagePlatform(context.Background(), image)
	if err != nil {
		cbCompleteCommandExecution(id, -1, "", err.Error())
		return
	}

	var mu sync.Mutex
	var stdout, stderr strings.Builder
	sink := sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		defer mu.Unlock()
		if line.Stream == "stderr" {
			stderr.WriteString(line.Text + "\n")
		} else {
			stdout.WriteString(line.Text + "\n")
		}
	})
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Command:      []string{"/bin/sh"},
		Args:         []string{"-c", command},
		WorkingDir:   "/codebuild/output/src",
		Binds:        []string{filepath.Clean(workDir) + ":/codebuild/output/src:z"},
		Env: map[string]string{
			"CODEBUILD_SRC_DIR":  "/codebuild/output/src",
			"AWS_DEFAULT_REGION": awsRegion(),
			"AWS_REGION":         awsRegion(),
		},
		ExtraHosts: hostMetadataExtraHosts(),
		Labels:     map[string]string{"sockerless-codebuild-command": id},
		Sandbox:    sim.SandboxFargate,
	}, sink)
	if err != nil {
		cbCompleteCommandExecution(id, -1, "", fmt.Sprintf("start build environment %s: %v", image, err))
		return
	}
	result := handle.Wait()
	mu.Lock()
	defer mu.Unlock()
	if result.Error != nil {
		cbCompleteCommandExecution(id, -1, stdout.String(), result.Error.Error())
		return
	}
	cbCompleteCommandExecution(id, result.ExitCode, stdout.String(), stderr.String())
}

func cbCompleteCommandExecution(id string, exitCode int, stdout, stderr string) {
	cbMu.Lock()
	defer cbMu.Unlock()
	ce, ok := cbCommandExecs.Get(id)
	if !ok {
		return
	}
	now := cbEpochNow()
	ce.EndTime = now
	ce.ExitCode = fmt.Sprintf("%d", exitCode)
	if exitCode == 0 {
		ce.Status = "SUCCEEDED"
	} else {
		ce.Status = "FAILED"
	}
	ce.StandardOutputContent = stdout
	ce.StandardErrContent = stderr
	cbCommandExecs.Put(id, ce)
}

func handleCBBatchGetCommandExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SandboxID           string   `json:"sandboxId"`
		CommandExecutionIDs []string `json:"commandExecutionIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	found := []CBCommandExecution{}
	notFound := []string{}
	for _, id := range req.CommandExecutionIDs {
		if ce, ok := cbCommandExecs.Get(id); ok {
			found = append(found, ce)
		} else {
			notFound = append(notFound, id)
		}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"commandExecutions":         found,
		"commandExecutionsNotFound": notFound,
	})
}

func handleCBListCommandExecutionsForSandbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SandboxID  string `json:"sandboxId"`
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var execs []CBCommandExecution
	for _, ce := range cbCommandExecs.List() {
		if ce.SandboxID == req.SandboxID {
			execs = append(execs, ce)
		}
	}
	ascending := strings.EqualFold(req.SortOrder, "ASCENDING")
	sort.Slice(execs, func(i, j int) bool {
		if ascending {
			return execs[i].Seq < execs[j].Seq
		}
		return execs[i].Seq > execs[j].Seq
	})
	page, nextTok := awsPage(execs, req.NextToken, req.MaxResults, 100)
	if page == nil {
		page = []CBCommandExecution{}
	}
	resp := map[string]any{"commandExecutions": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// --- Webhooks ----------------------------------------------------------------

func handleCBCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName  string             `json:"projectName"`
		BranchFilter string             `json:"branchFilter"`
		BuildType    string             `json:"buildType"`
		FilterGroups [][]map[string]any `json:"filterGroups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if _, ok := cbProjects.Get(req.ProjectName); !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+req.ProjectName)
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	if _, ok := cbWebhooks.Get(req.ProjectName); ok {
		cbWriteError(w, "ResourceAlreadyExistsException", "Webhook already exists for project: "+req.ProjectName)
		return
	}
	hook := CBWebhook{
		URL:          fmt.Sprintf("https://codebuild.us-east-1.amazonaws.com/webhooks?t=%s", uuid.New().String()),
		PayloadURL:   fmt.Sprintf("https://codebuild.us-east-1.amazonaws.com/webhooks/%s", req.ProjectName),
		Secret:       uuid.New().String(),
		BranchFilter: req.BranchFilter,
		BuildType:    req.BuildType,
		FilterGroups: req.FilterGroups,
		Status:       "NORMAL",
		ProjectName:  req.ProjectName,
	}
	cbWebhooks.Put(req.ProjectName, hook)
	cbWriteJSON(w, http.StatusOK, map[string]any{"webhook": hook})
}

func handleCBUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName  string             `json:"projectName"`
		BranchFilter string             `json:"branchFilter"`
		BuildType    string             `json:"buildType"`
		FilterGroups [][]map[string]any `json:"filterGroups"`
		RotateSecret bool               `json:"rotateSecret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	hook, ok := cbWebhooks.Get(req.ProjectName)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Webhook not found for project: "+req.ProjectName)
		return
	}
	hook.BranchFilter = req.BranchFilter
	if req.BuildType != "" {
		hook.BuildType = req.BuildType
	}
	if req.FilterGroups != nil {
		hook.FilterGroups = req.FilterGroups
	}
	if req.RotateSecret {
		hook.Secret = uuid.New().String()
	}
	cbWebhooks.Put(req.ProjectName, hook)
	cbWriteJSON(w, http.StatusOK, map[string]any{"webhook": hook})
}

func handleCBDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName string `json:"projectName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	if _, ok := cbWebhooks.Get(req.ProjectName); !ok {
		cbWriteError(w, "ResourceNotFoundException", "Webhook not found for project: "+req.ProjectName)
		return
	}
	cbWebhooks.Delete(req.ProjectName)
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

// --- Reports: DeleteReport, test cases, code coverage, trend -----------------

func handleCBDeleteReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"arn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	// "When a test report is deleted, its test cases are also deleted."
	cbReports.Delete(req.Arn)
	cbReportResults.Delete(req.Arn)
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

// handleCBDescribeTestCases answers with the test cases the report's raw data
// files held. A report whose build produced no matching result file — or one
// produced by a CODE_COVERAGE report group — has no test cases, and the answer
// is the empty list the service returns for it.
func handleCBDescribeTestCases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportArn  string `json:"reportArn"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
		Filter     *struct {
			Status  string `json:"status"`
			Keyword string `json:"keyword"`
		} `json:"filter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if _, ok := cbReports.Get(req.ReportArn); !ok {
		cbWriteError(w, "ResourceNotFoundException", "Report not found: "+req.ReportArn)
		return
	}
	results, _ := cbReportResults.Get(req.ReportArn)
	cases := make([]CBTestCase, 0, len(results.TestCases))
	for _, testCase := range results.TestCases {
		if req.Filter != nil && req.Filter.Status != "" &&
			!strings.EqualFold(req.Filter.Status, testCase.Status) {
			continue
		}
		if req.Filter != nil && req.Filter.Keyword != "" &&
			!strings.Contains(testCase.Name, req.Filter.Keyword) &&
			!strings.Contains(testCase.Prefix, req.Filter.Keyword) {
			continue
		}
		cases = append(cases, testCase)
	}
	page, nextTok := awsPage(cases, req.NextToken, req.MaxResults, 100)
	resp := map[string]any{"testCases": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// handleCBDescribeCodeCoverages answers with the per-file coverage the report's
// raw data files held. A TEST report, or one whose build produced no matching
// coverage file, has none.
func handleCBDescribeCodeCoverages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportArn       string   `json:"reportArn"`
		MaxResults      int      `json:"maxResults"`
		NextToken       string   `json:"nextToken"`
		SortBy          string   `json:"sortBy"`
		SortOrder       string   `json:"sortOrder"`
		MinLineCoverage *float64 `json:"minLineCoveragePercentage"`
		MaxLineCoverage *float64 `json:"maxLineCoveragePercentage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if _, ok := cbReports.Get(req.ReportArn); !ok {
		cbWriteError(w, "ResourceNotFoundException", "Report not found: "+req.ReportArn)
		return
	}
	results, _ := cbReportResults.Get(req.ReportArn)
	coverages := make([]CBCodeCoverage, 0, len(results.CodeCoverages))
	for _, coverage := range results.CodeCoverages {
		if req.MinLineCoverage != nil && coverage.LineCoveragePercentage < *req.MinLineCoverage {
			continue
		}
		if req.MaxLineCoverage != nil && coverage.LineCoveragePercentage > *req.MaxLineCoverage {
			continue
		}
		coverages = append(coverages, coverage)
	}
	// sortBy is FILE_PATH or LINE_COVERAGE_PERCENTAGE; the default order is by
	// file path, which is the order ingestion recorded.
	if strings.EqualFold(req.SortBy, "LINE_COVERAGE_PERCENTAGE") {
		sort.SliceStable(coverages, func(i, j int) bool {
			return coverages[i].LineCoveragePercentage < coverages[j].LineCoveragePercentage
		})
	} else {
		sort.SliceStable(coverages, func(i, j int) bool {
			return coverages[i].FilePath < coverages[j].FilePath
		})
	}
	if strings.EqualFold(req.SortOrder, "DESCENDING") {
		for i, j := 0, len(coverages)-1; i < j; i, j = i+1, j-1 {
			coverages[i], coverages[j] = coverages[j], coverages[i]
		}
	}
	page, nextTok := awsPage(coverages, req.NextToken, req.MaxResults, 100)
	resp := map[string]any{"codeCoverages": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

func handleCBGetReportGroupTrend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportGroupArn string `json:"reportGroupArn"`
		NumOfReports   int    `json:"numOfReports"`
		TrendField     string `json:"trendField"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if _, ok := cbReportGrps.Get(req.ReportGroupArn); !ok {
		cbWriteError(w, "ResourceNotFoundException", "Report group not found: "+req.ReportGroupArn)
		return
	}

	// GetReportGroupTrend aggregates the trend field across the group's reports.
	var reports []CBReport
	for _, rep := range cbReports.List() {
		if rep.ReportGroupArn == req.ReportGroupArn {
			reports = append(reports, rep)
		}
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Created > reports[j].Created })
	limit := req.NumOfReports
	if limit <= 0 || limit > len(reports) {
		limit = len(reports)
	}
	reports = reports[:limit]

	rawData := make([]map[string]any, 0, len(reports))
	var sum, minVal, maxVal float64
	first := true
	for _, rep := range reports {
		val, ok := cbReportTrendValue(rep, req.TrendField)
		if !ok {
			cbWriteError(w, "InvalidInputException", "Invalid trendField: "+req.TrendField)
			return
		}
		rawData = append(rawData, map[string]any{
			"reportArn": rep.Arn,
			"data":      fmt.Sprintf("%g", val),
		})
		sum += val
		if first {
			minVal, maxVal = val, val
			first = false
		} else {
			if val < minVal {
				minVal = val
			}
			if val > maxVal {
				maxVal = val
			}
		}
	}
	avg := 0.0
	if len(reports) > 0 {
		avg = sum / float64(len(reports))
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"rawData": rawData,
		"stats": map[string]any{
			"average": fmt.Sprintf("%g", avg),
			"max":     fmt.Sprintf("%g", maxVal),
			"min":     fmt.Sprintf("%g", minVal),
		},
	})
}

// cbReportTrendValue is one report's value for a ReportGroupTrendFieldType,
// measured from the raw result files that report ingested. Reports false for a
// field the service does not define.
func cbReportTrendValue(report CBReport, trendField string) (float64, bool) {
	results, _ := cbReportResults.Get(report.Arn)
	switch strings.ToUpper(trendField) {
	case "", "PASS_RATE":
		passed := 0
		for _, testCase := range results.TestCases {
			if testCase.Status == "SUCCEEDED" {
				passed++
			}
		}
		if len(results.TestCases) == 0 {
			return 0, true
		}
		return float64(passed) * 100 / float64(len(results.TestCases)), true
	case "DURATION":
		var nanoseconds int64
		for _, testCase := range results.TestCases {
			nanoseconds += testCase.DurationInNanoSeconds
		}
		return float64(nanoseconds), true
	case "TOTAL":
		return float64(len(results.TestCases)), true
	case "LINE_COVERAGE", "LINES_COVERED", "LINES_MISSED",
		"BRANCH_COVERAGE", "BRANCHES_COVERED", "BRANCHES_MISSED":
		var linesCovered, linesMissed, branchesCovered, branchesMissed int
		for _, coverage := range results.CodeCoverages {
			linesCovered += coverage.LinesCovered
			linesMissed += coverage.LinesMissed
			branchesCovered += coverage.BranchesCovered
			branchesMissed += coverage.BranchesMissed
		}
		switch strings.ToUpper(trendField) {
		case "LINES_COVERED":
			return float64(linesCovered), true
		case "LINES_MISSED":
			return float64(linesMissed), true
		case "BRANCHES_COVERED":
			return float64(branchesCovered), true
		case "BRANCHES_MISSED":
			return float64(branchesMissed), true
		case "LINE_COVERAGE":
			if linesCovered+linesMissed == 0 {
				return 0, true
			}
			return float64(linesCovered) * 100 / float64(linesCovered+linesMissed), true
		default:
			if branchesCovered+branchesMissed == 0 {
				return 0, true
			}
			return float64(branchesCovered) * 100 / float64(branchesCovered+branchesMissed), true
		}
	}
	return 0, false
}

// --- Resource policy ---------------------------------------------------------

func handleCBPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Policy      string `json:"policy"`
		ResourceArn string `json:"resourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ResourceArn == "" || req.Policy == "" {
		cbWriteError(w, "InvalidInputException", "resourceArn and policy are required")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	cbResourcePols.Put(req.ResourceArn, IAMResourcePolicy{ARN: req.ResourceArn, Policy: req.Policy})
	// Mirror into the central IAM resource-policy store so the enforcement gate
	// sees it, exactly as SQS/SNS/DynamoDB do.
	iamPutResourcePolicy(req.ResourceArn, req.Policy)
	cbWriteJSON(w, http.StatusOK, map[string]any{"resourceArn": req.ResourceArn})
}

func handleCBGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	pol, ok := cbResourcePols.Get(req.ResourceArn)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Resource policy not found: "+req.ResourceArn)
		return
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{"policy": pol.Policy})
}

func handleCBDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	cbResourcePols.Delete(req.ResourceArn)
	iamDeleteResourcePolicy(req.ResourceArn)
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

// --- Project visibility / cache ----------------------------------------------

func handleCBUpdateProjectVisibility(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectArn         string `json:"projectArn"`
		ProjectVisibility  string `json:"projectVisibility"`
		ResourceAccessRole string `json:"resourceAccessRole"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	name := cbNameFromARN(req.ProjectArn)
	p, ok := cbProjects.Get(name)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+req.ProjectArn)
		return
	}
	resp := map[string]any{
		"projectArn":        p.Arn,
		"projectVisibility": req.ProjectVisibility,
	}
	if strings.EqualFold(req.ProjectVisibility, "PUBLIC_READ") {
		resp["publicProjectAlias"] = fmt.Sprintf("https://%s.codebuild.aws/%s", "project", p.Name)
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

func handleCBInvalidateProjectCache(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName string `json:"projectName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if _, ok := cbProjects.Get(req.ProjectName); !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+req.ProjectName)
		return
	}
	// InvalidateProjectCache clears the project's build cache; the sim holds no
	// cache artifacts, so success with an empty body is the faithful response.
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

// --- Curated environment images ----------------------------------------------

// handleCBListCuratedEnvironmentImages returns AWS's real curated image
// platforms (Amazon Linux / Ubuntu standard images), in the EnvironmentPlatform
// shape. This is a known static-but-real set published by AWS.
func handleCBListCuratedEnvironmentImages(w http.ResponseWriter, r *http.Request) {
	platforms := []map[string]any{
		{
			"platform": "AMAZON_LINUX",
			"languages": []map[string]any{
				{
					"language": "BASE",
					"images": []map[string]any{
						{
							"name":        "aws/codebuild/amazonlinux2-x86_64-standard:5.0",
							"description": "AL2 5.0",
							"versions":    []string{"aws/codebuild/amazonlinux2-x86_64-standard:5.0"},
						},
						{
							"name":        "aws/codebuild/amazonlinux2-aarch64-standard:3.0",
							"description": "AL2 aarch64 3.0",
							"versions":    []string{"aws/codebuild/amazonlinux2-aarch64-standard:3.0"},
						},
					},
				},
			},
		},
		{
			"platform": "UBUNTU",
			"languages": []map[string]any{
				{
					"language": "BASE",
					"images": []map[string]any{
						{
							"name":        "aws/codebuild/standard:7.0",
							"description": "Ubuntu standard 7.0",
							"versions":    []string{"aws/codebuild/standard:7.0"},
						},
						{
							"name":        "aws/codebuild/standard:6.0",
							"description": "Ubuntu standard 6.0",
							"versions":    []string{"aws/codebuild/standard:6.0"},
						},
					},
				},
			},
		},
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{"platforms": platforms})
}

// --- Shared resource listings ------------------------------------------------

// handleCBListSharedProjects returns the projects shared with the account via a
// resource policy. The sim has no cross-account sharing, so it returns the set
// of project ARNs that carry a resource policy — a real, derivable shared set.
func handleCBListSharedProjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var arns []string
	for _, pol := range cbResourcePols.List() {
		if strings.Contains(pol.ARN, ":project/") {
			arns = append(arns, pol.ARN)
		}
	}
	sort.Strings(arns)
	if strings.EqualFold(req.SortOrder, "DESCENDING") {
		reverseStrings(arns)
	}
	page, nextTok := awsPage(arns, req.NextToken, req.MaxResults, 100)
	resp := map[string]any{"projects": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// handleCBListSharedReportGroups returns the report groups shared via a resource
// policy (the report-group ARNs carrying a policy).
func handleCBListSharedReportGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var arns []string
	for _, pol := range cbResourcePols.List() {
		if strings.Contains(pol.ARN, ":report-group/") {
			arns = append(arns, pol.ARN)
		}
	}
	sort.Strings(arns)
	if strings.EqualFold(req.SortOrder, "DESCENDING") {
		reverseStrings(arns)
	}
	page, nextTok := awsPage(arns, req.NextToken, req.MaxResults, 100)
	resp := map[string]any{"reportGroups": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}
