package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// AWS CodeBuild — AWS JSON 1.1 protocol (X-Amz-Target: CodeBuild_20161006.<Op>).
// Builds execute project buildspec commands inside the project's configured
// build-environment image and record terminal state from the real container.

type CBProject struct {
	Name         string         `json:"name"`
	Arn          string         `json:"arn"`
	Description  string         `json:"description,omitempty"`
	Source       map[string]any `json:"source"`
	Artifacts    map[string]any `json:"artifacts"`
	Environment  map[string]any `json:"environment"`
	ServiceRole  string         `json:"serviceRole"`
	Created      float64        `json:"created"`
	LastModified float64        `json:"lastModified"`
	BuildTimeout int            `json:"timeoutInMinutes"`
	QueueTimeout int            `json:"queuedTimeoutInMinutes"`
	Tags         []CBTag        `json:"tags,omitempty"`
}

type CBBuild struct {
	ID            string         `json:"id"`
	Arn           string         `json:"arn"`
	ProjectName   string         `json:"projectName"`
	SourceVersion string         `json:"sourceVersion,omitempty"`
	BuildStatus   string         `json:"buildStatus"`
	StartTime     float64        `json:"startTime"`
	EndTime       float64        `json:"endTime"`
	Phases        []CBPhase      `json:"phases"`
	Logs          map[string]any `json:"logs"`
	Environment   map[string]any `json:"environment,omitempty"`
	ReportArns    []string       `json:"reportArns,omitempty"`
	// Seq is a sim-internal monotonic creation order used to sort ListBuilds
	// faithfully by start order; it's not part of the CodeBuild wire shape.
	Seq int64 `json:"-"`
	// Reports holds the buildspec's reports section; the build produces a
	// Report per entry on completion, from the raw result files the entry
	// names. Sim-internal, not part of the wire shape.
	Reports []cbReportSpec `json:"-"`
	// Workspace is the build environment's source directory on the host, bound
	// into the build container at CODEBUILD_SRC_DIR. Report ingestion reads the
	// raw result files out of it once the container has exited, so it is
	// durable: a control-plane restart that adopts a running build container
	// still knows where that build's reports will land.
	Workspace string `json:"-"`
	// BuildPlan and RuntimeEnvironment are the durable execution inputs needed
	// to resume the narrow pre-container window after a control-plane restart.
	// Once the real build container exists, recovery adopts that container
	// instead of executing the commands again.
	BuildPlan          *cbBuildPlan      `json:"-"`
	RuntimeEnvironment map[string]string `json:"-"`
}

type CBPhase struct {
	PhaseType         string           `json:"phaseType"`
	PhaseStatus       string           `json:"phaseStatus"`
	StartTime         float64          `json:"startTime"`
	EndTime           float64          `json:"endTime"`
	DurationInSeconds float64          `json:"durationInSeconds"`
	Contexts          []CBPhaseContext `json:"contexts,omitempty"`
}

type CBPhaseContext struct {
	StatusCode string `json:"statusCode"`
	Message    string `json:"message"`
}

// cbLogsLocation is the LogsLocation the sim reports: builds run as local
// processes without a CloudWatch log sink, so log enablement is the real
// member cloudWatchLogs.status (not an invented boolean).
func cbLogsLocation() map[string]any {
	return map[string]any{
		"cloudWatchLogs": map[string]any{"status": "DISABLED"},
	}
}

type CBTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CBReportGroup mirrors the CodeBuild ReportGroup shape. status is read-only
// and ACTIVE for a live group; type is TEST or CODE_COVERAGE.
type CBReportGroup struct {
	Arn          string         `json:"arn"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	ExportConfig map[string]any `json:"exportConfig,omitempty"`
	Created      float64        `json:"created"`
	LastModified float64        `json:"lastModified"`
	Status       string         `json:"status"`
	Tags         []CBTag        `json:"tags,omitempty"`
}

// CBReport mirrors the CodeBuild Report shape. A report is produced by a build
// whose buildspec references the report group; the sim creates it from the
// build's terminal state, never a synthetic placeholder.
type CBReport struct {
	Arn            string         `json:"arn"`
	Type           string         `json:"type"`
	Name           string         `json:"name"`
	ReportGroupArn string         `json:"reportGroupArn"`
	ExecutionId    string         `json:"executionId,omitempty"`
	Status         string         `json:"status"`
	Created        float64        `json:"created"`
	Expired        float64        `json:"expired,omitempty"`
	ExportConfig   map[string]any `json:"exportConfig,omitempty"`
	Truncated      bool           `json:"truncated"`
}

// CBSourceCredential mirrors the SourceCredentialsInfo shape. The token itself
// is never echoed back by the real API; only the ARN, authType, serverType,
// and (for SECRETS_MANAGER) the resource are readable.
type CBSourceCredential struct {
	Arn        string `json:"arn"`
	ServerType string `json:"serverType"`
	AuthType   string `json:"authType"`
	Resource   string `json:"resource,omitempty"`
}

type cbSourceCredentialSecret struct {
	Arn        string `json:"arn"`
	Username   string `json:"username,omitempty"`
	Ciphertext []byte `json:"ciphertext"`
}

const cbAWSOwnedKMSKeyID = "aws-owned-codebuild"

var (
	cbProjects          sim.Store[CBProject]
	cbBuilds            sim.Store[CBBuild]
	cbReportGrps        sim.Store[CBReportGroup]
	cbReports           sim.Store[CBReport]
	cbReportResults     sim.Store[CBReportResults]
	cbSourceCreds       sim.Store[CBSourceCredential]
	cbSourceCredSecrets sim.Store[cbSourceCredentialSecret]
	cbMu                sync.Mutex
	cbBuildCancelMu     sync.Mutex
	cbBuildCancels      = map[string]func(){}
)

func registerCodeBuild(r *sim.AWSRouter, srv *sim.Server) {
	cbProjects = sim.MakeStore[CBProject](srv.DB(), "codebuild_projects")
	cbBuilds = sim.MakeStore[CBBuild](srv.DB(), "codebuild_builds")
	cbReportGrps = sim.MakeStore[CBReportGroup](srv.DB(), "codebuild_report_groups")
	cbReports = sim.MakeStore[CBReport](srv.DB(), "codebuild_reports")
	cbReportResults = sim.MakeStore[CBReportResults](srv.DB(), "codebuild_report_results")
	cbSourceCreds = sim.MakeStore[CBSourceCredential](srv.DB(), "codebuild_source_credentials")
	cbSourceCredSecrets = sim.MakeStore[cbSourceCredentialSecret](srv.DB(), "codebuild_source_credential_secrets")
	cbRebuildBuildSequence()
	cbBuildCancelMu.Lock()
	cbBuildCancels = make(map[string]func())
	cbBuildCancelMu.Unlock()

	r.Register("CodeBuild_20161006.CreateProject", handleCBCreateProject)
	r.Register("CodeBuild_20161006.BatchGetProjects", handleCBBatchGetProjects)
	r.Register("CodeBuild_20161006.ListProjects", handleCBListProjects)
	r.Register("CodeBuild_20161006.UpdateProject", handleCBUpdateProject)
	r.Register("CodeBuild_20161006.DeleteProject", handleCBDeleteProject)
	r.Register("CodeBuild_20161006.StartBuild", handleCBStartBuild)
	r.Register("CodeBuild_20161006.StopBuild", handleCBStopBuild)
	r.Register("CodeBuild_20161006.RetryBuild", handleCBRetryBuild)
	r.Register("CodeBuild_20161006.BatchGetBuilds", handleCBBatchGetBuilds)
	r.Register("CodeBuild_20161006.ListBuildsForProject", handleCBListBuildsForProject)
	r.Register("CodeBuild_20161006.ListBuilds", handleCBListBuilds)

	r.Register("CodeBuild_20161006.CreateReportGroup", handleCBCreateReportGroup)
	r.Register("CodeBuild_20161006.UpdateReportGroup", handleCBUpdateReportGroup)
	r.Register("CodeBuild_20161006.DeleteReportGroup", handleCBDeleteReportGroup)
	r.Register("CodeBuild_20161006.ListReportGroups", handleCBListReportGroups)
	r.Register("CodeBuild_20161006.BatchGetReportGroups", handleCBBatchGetReportGroups)
	r.Register("CodeBuild_20161006.ListReports", handleCBListReports)
	r.Register("CodeBuild_20161006.ListReportsForReportGroup", handleCBListReportsForReportGroup)
	r.Register("CodeBuild_20161006.BatchGetReports", handleCBBatchGetReports)

	r.Register("CodeBuild_20161006.ImportSourceCredentials", handleCBImportSourceCredentials)
	r.Register("CodeBuild_20161006.ListSourceCredentials", handleCBListSourceCredentials)
	r.Register("CodeBuild_20161006.DeleteSourceCredentials", handleCBDeleteSourceCredentials)
}

func cbARN(resource string) string {
	return fmt.Sprintf("arn:aws:codebuild:%s:%s:%s", awsRegion(), awsAccountID(), resource)
}

func cbEpochNow() float64 {
	return float64(time.Now().UTC().Unix())
}

// cbBuildSeq is a process-wide monotonic counter giving each build a strictly
// increasing creation order, so ListBuilds sorts faithfully by start order even
// when builds are created within the same wall-clock second.
var cbBuildSeq atomic.Int64

func cbNextBuildSeq() int64 { return cbBuildSeq.Add(1) }

func cbBuildTimeout(project CBProject) time.Duration {
	minutes := project.BuildTimeout
	if minutes <= 0 {
		minutes = 60
	}
	return time.Duration(minutes) * time.Minute
}

func cbRebuildBuildSequence() {
	var latest int64
	for _, build := range cbBuilds.List() {
		if build.Seq > latest {
			latest = build.Seq
		}
	}
	cbBuildSeq.Store(latest)
}

func cbAdoptBuildWorkload(
	id string,
	resourceName string,
	startTime float64,
	timeout time.Duration,
	complete func(string, int, string),
) (bool, error) {
	existing, err := sim.FindExistingContainers(map[string]string{
		"sockerless-codebuild-build": id,
	})
	if err != nil {
		return false, fmt.Errorf("find %s %s container: %w", resourceName, id, err)
	}
	if len(existing) > 1 {
		return false, fmt.Errorf("%s %s has %d workload containers", resourceName, id, len(existing))
	}
	if len(existing) == 0 {
		return false, nil
	}
	remaining := time.Until(time.Unix(int64(startTime), 0).Add(timeout))
	if remaining <= 0 {
		remaining = time.Nanosecond
	}
	handle, err := sim.AdoptContainer(existing[0].ID, sim.ContainerConfig{Timeout: remaining}, sim.NoopSink{})
	if err != nil {
		return false, fmt.Errorf("adopt %s %s container: %w", resourceName, id, err)
	}
	cbRegisterBuildCancel(id, handle.Cancel)
	go func() {
		result := handle.Wait()
		cbUnregisterBuildCancel(id)
		reason := ""
		if result.Error != nil {
			reason = result.Error.Error()
		} else if result.ExitCode != 0 {
			reason = fmt.Sprintf("Build command exited with status %d", result.ExitCode)
		}
		complete(id, result.ExitCode, reason)
	}()
	return true, nil
}

func cbRecoverBuilds() error {
	for _, build := range cbBuilds.List() {
		if build.BuildStatus != "IN_PROGRESS" {
			continue
		}
		project, ok := cbProjects.Get(build.ProjectName)
		if !ok {
			return fmt.Errorf("build %s references missing project %s", build.ID, build.ProjectName)
		}
		adopted, err := cbAdoptBuildWorkload(
			build.ID,
			"build",
			build.StartTime,
			cbBuildTimeout(project),
			cbCompleteBuild,
		)
		if err != nil {
			return err
		}
		if adopted {
			continue
		}
		if build.BuildPlan == nil {
			return fmt.Errorf("build %s has neither a workload container nor a persisted build plan", build.ID)
		}
		go cbRunBuild(build.ID, project, *build.BuildPlan, build.RuntimeEnvironment)
	}
	return nil
}

func cbWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cbWriteError(w http.ResponseWriter, code string, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"__type":  code,
		"message": msg,
	})
}

func handleCBCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string         `json:"name"`
		Description  string         `json:"description"`
		Source       map[string]any `json:"source"`
		Artifacts    map[string]any `json:"artifacts"`
		Environment  map[string]any `json:"environment"`
		ServiceRole  string         `json:"serviceRole"`
		BuildTimeout int            `json:"timeoutInMinutes"`
		QueueTimeout int            `json:"queuedTimeoutInMinutes"`
		Tags         []CBTag        `json:"tags"`
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

	if _, ok := cbProjects.Get(req.Name); ok {
		cbWriteError(w, "ResourceAlreadyExistsException", "Project already exists: "+req.Name)
		return
	}

	now := cbEpochNow()
	if req.Source == nil {
		req.Source = map[string]any{"type": "NO_SOURCE"}
	}
	if req.Artifacts == nil {
		req.Artifacts = map[string]any{"type": "NO_ARTIFACTS"}
	}
	if req.Environment == nil {
		req.Environment = map[string]any{"type": "LINUX_CONTAINER", "image": "aws/codebuild/standard:7.0", "computeType": "BUILD_GENERAL1_SMALL"}
	}
	if req.BuildTimeout == 0 {
		req.BuildTimeout = 60
	}
	if req.QueueTimeout == 0 {
		req.QueueTimeout = 480
	}
	p := CBProject{
		Name:         req.Name,
		Arn:          cbARN("project/" + req.Name),
		Description:  req.Description,
		Source:       req.Source,
		Artifacts:    req.Artifacts,
		Environment:  req.Environment,
		ServiceRole:  req.ServiceRole,
		Created:      now,
		LastModified: now,
		BuildTimeout: req.BuildTimeout,
		QueueTimeout: req.QueueTimeout,
		Tags:         req.Tags,
	}
	cbProjects.Put(req.Name, p)
	cbWriteJSON(w, http.StatusOK, map[string]any{"project": p})
}

func handleCBBatchGetProjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var found []CBProject
	var notFound []string
	for _, nameOrARN := range req.Names {
		p, ok := cbProjects.Get(nameOrARN)
		if !ok {
			p, ok = cbProjects.Get(cbNameFromARN(nameOrARN))
		}
		if ok {
			found = append(found, p)
		} else {
			notFound = append(notFound, nameOrARN)
		}
	}
	if found == nil {
		found = []CBProject{}
	}
	if notFound == nil {
		notFound = []string{}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"projects":         found,
		"projectsNotFound": notFound,
	})
}

func handleCBListProjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortBy    string `json:"sortBy"`
		SortOrder string `json:"sortOrder"`
		NextToken string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := cbProjects.List()
	names := make([]string, 0, len(all))
	for _, p := range all {
		names = append(names, p.Name)
	}
	// ListProjects sorts by name; default order is ASCENDING.
	sort.Strings(names)
	if strings.EqualFold(req.SortOrder, "DESCENDING") {
		reverseStrings(names)
	}
	page, nextTok := awsPage(names, req.NextToken, 0, 100)
	resp := map[string]any{"projects": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

func handleCBUpdateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Source      map[string]any `json:"source"`
		Artifacts   map[string]any `json:"artifacts"`
		Environment map[string]any `json:"environment"`
		ServiceRole string         `json:"serviceRole"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	p, ok := cbProjects.Get(req.Name)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+req.Name)
		return
	}
	if req.Description != "" {
		p.Description = req.Description
	}
	if req.Source != nil {
		p.Source = req.Source
	}
	if req.Artifacts != nil {
		p.Artifacts = req.Artifacts
	}
	if req.Environment != nil {
		p.Environment = req.Environment
	}
	if req.ServiceRole != "" {
		p.ServiceRole = req.ServiceRole
	}
	p.LastModified = cbEpochNow()
	cbProjects.Put(req.Name, p)
	cbWriteJSON(w, http.StatusOK, map[string]any{"project": p})
}

func handleCBDeleteProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	cbProjects.Delete(req.Name)
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCBStartBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName                  string           `json:"projectName"`
		BuildspecOverride            string           `json:"buildspecOverride"`
		SourceVersion                string           `json:"sourceVersion"`
		EnvironmentVariablesOverride []map[string]any `json:"environmentVariablesOverride"`
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
	reportSpecs, err := cbBuildReportSpecs(p, req.BuildspecOverride)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	buildID := req.ProjectName + ":" + uuid.New().String()
	now := cbEpochNow()
	runtimeEnvironment := cbEnvironment(p.Environment, req.EnvironmentVariablesOverride)
	build := CBBuild{
		ID:                 buildID,
		Arn:                cbARN("build/" + buildID),
		ProjectName:        req.ProjectName,
		SourceVersion:      req.SourceVersion,
		BuildStatus:        "IN_PROGRESS",
		Seq:                cbNextBuildSeq(),
		StartTime:          now,
		EndTime:            now,
		Environment:        p.Environment,
		Reports:            reportSpecs,
		BuildPlan:          &plan,
		RuntimeEnvironment: runtimeEnvironment,
		Phases: []CBPhase{
			{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: now, EndTime: now, DurationInSeconds: 0},
			{PhaseType: "BUILD", PhaseStatus: "IN_PROGRESS", StartTime: now},
		},
		Logs: cbLogsLocation(),
	}
	cbBuilds.Put(buildID, build)
	go cbRunBuild(buildID, p, plan, runtimeEnvironment)
	cbWriteJSON(w, http.StatusOK, map[string]any{"build": build})
}

func handleCBStopBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	build, ok := cbBuilds.Get(req.ID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Build not found: "+req.ID)
		return
	}
	build = cbStopBuildByID(req.ID)
	cbWriteJSON(w, http.StatusOK, map[string]any{"build": build})
}

func cbStopBuildByID(buildID string) CBBuild {
	cbMu.Lock()
	defer cbMu.Unlock()
	build, ok := cbBuilds.Get(buildID)
	if !ok {
		return CBBuild{}
	}
	// StopBuild transitions a running build to STOPPED. A build that already
	// settled keeps its terminal status (real CodeBuild is idempotent here).
	if build.BuildStatus == "IN_PROGRESS" {
		cbCancelBuild(buildID)
		now := cbEpochNow()
		build.BuildStatus = "STOPPED"
		build.EndTime = now
		build.Phases = []CBPhase{
			{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: build.StartTime, EndTime: build.StartTime, DurationInSeconds: 0},
			{PhaseType: "BUILD", PhaseStatus: "STOPPED", StartTime: build.StartTime, EndTime: now, DurationInSeconds: now - build.StartTime},
			{PhaseType: "COMPLETED", PhaseStatus: "STOPPED", StartTime: now, EndTime: now, DurationInSeconds: 0},
		}
		cbBuilds.Put(buildID, build)
	}
	return build
}

func handleCBRetryBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	prior, ok := cbBuilds.Get(req.ID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Build not found: "+req.ID)
		return
	}
	p, ok := cbProjects.Get(prior.ProjectName)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+prior.ProjectName)
		return
	}
	plan, err := cbBuildPlanForProject(p, "", prior.SourceVersion)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	reportSpecs, err := cbBuildReportSpecs(p, "")
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	// RetryBuild starts a fresh build of the same project (a new build id),
	// mirroring real CodeBuild which produces a new build resource.
	buildID := prior.ProjectName + ":" + uuid.New().String()
	now := cbEpochNow()
	runtimeEnvironment := cbEnvironment(p.Environment, nil)
	build := CBBuild{
		ID:                 buildID,
		Arn:                cbARN("build/" + buildID),
		ProjectName:        prior.ProjectName,
		SourceVersion:      prior.SourceVersion,
		BuildStatus:        "IN_PROGRESS",
		Seq:                cbNextBuildSeq(),
		StartTime:          now,
		EndTime:            now,
		Environment:        p.Environment,
		Reports:            reportSpecs,
		BuildPlan:          &plan,
		RuntimeEnvironment: runtimeEnvironment,
		Phases: []CBPhase{
			{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: now, EndTime: now, DurationInSeconds: 0},
			{PhaseType: "BUILD", PhaseStatus: "IN_PROGRESS", StartTime: now},
		},
		Logs: cbLogsLocation(),
	}
	cbBuilds.Put(buildID, build)
	go cbRunBuild(buildID, p, plan, runtimeEnvironment)
	cbWriteJSON(w, http.StatusOK, map[string]any{"build": build})
}

type cbBuildspec struct {
	Phases map[string]struct {
		Commands []string `yaml:"commands"`
	} `yaml:"phases"`
	// Reports maps a report-group name or ARN to its report config; a build
	// with a reports section produces a Report per entry, exactly like real
	// CodeBuild.
	Reports map[string]cbReportSpec `yaml:"reports"`
}

type cbBuildPlan struct {
	Commands      []string
	SourceVersion string
}

// cbBuildReportSpecs returns the reports a project's buildspec declares, in
// report-group-key order (empty when the buildspec has no reports section).
func cbBuildReportSpecs(p CBProject, override string) ([]cbReportSpec, error) {
	buildspec := override
	if buildspec == "" {
		buildspec = cbString(p.Source["buildspec"])
	}
	if buildspec == "" {
		return nil, nil
	}
	var spec cbBuildspec
	if err := yaml.Unmarshal([]byte(buildspec), &spec); err != nil {
		return nil, fmt.Errorf("invalid buildspec: %w", err)
	}
	keys := make([]string, 0, len(spec.Reports))
	for key := range spec.Reports {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	specs := make([]cbReportSpec, 0, len(keys))
	for _, key := range keys {
		entry := spec.Reports[key]
		entry.Key = key
		specs = append(specs, entry)
	}
	if err := cbValidateReportSpecs(specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func cbBuildCommands(p CBProject, override string) ([]string, error) {
	buildspec := override
	if buildspec == "" {
		buildspec = cbString(p.Source["buildspec"])
	}
	if buildspec == "" {
		return nil, fmt.Errorf("source.buildspec is required for NO_SOURCE builds")
	}
	var spec cbBuildspec
	if err := yaml.Unmarshal([]byte(buildspec), &spec); err != nil {
		return nil, fmt.Errorf("invalid buildspec: %w", err)
	}
	var commands []string
	for _, phase := range []string{"install", "pre_build", "build", "post_build"} {
		commands = append(commands, spec.Phases[phase].Commands...)
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("buildspec must contain at least one command")
	}
	return commands, nil
}

func cbBuildPlanForProject(project CBProject, override, sourceVersion string) (cbBuildPlan, error) {
	if override == "" && cbString(project.Source["buildspec"]) == "" &&
		!strings.EqualFold(cbString(project.Source["type"]), "NO_SOURCE") {
		return cbBuildPlan{SourceVersion: sourceVersion}, nil
	}
	commands, err := cbBuildCommands(project, override)
	if err != nil {
		return cbBuildPlan{}, err
	}
	return cbBuildPlan{Commands: commands, SourceVersion: sourceVersion}, nil
}

func cbRunBuild(buildID string, project CBProject, plan cbBuildPlan, env map[string]string) {
	exitCode, reason := cbRunCommands(buildID, project, plan, env)
	cbCompleteBuild(buildID, exitCode, reason)
}

func cbRunCommands(buildID string, project CBProject, plan cbBuildPlan, env map[string]string) (int, string) {
	workDir, err := os.MkdirTemp("", "sockerless-codebuild-*")
	if err != nil {
		return -1, err.Error()
	}
	// The workspace outlives this function: cbCompleteBuild reads the build's
	// raw report files out of it and removes it afterwards, so an adopted build
	// finds it too.
	cbRecordBuildWorkspace(buildID, workDir)

	if err := cbCheckoutSource(project, plan.SourceVersion, workDir); err != nil {
		return -1, err.Error()
	}
	if len(plan.Commands) == 0 {
		buildspec, err := os.ReadFile(filepath.Join(workDir, "buildspec.yml"))
		if err != nil {
			return -1, fmt.Sprintf("read source buildspec.yml: %v", err)
		}
		projectWithBuildspec := project
		projectWithBuildspec.Source = make(map[string]any, len(project.Source)+1)
		for key, value := range project.Source {
			projectWithBuildspec.Source[key] = value
		}
		projectWithBuildspec.Source["buildspec"] = string(buildspec)
		plan.Commands, err = cbBuildCommands(projectWithBuildspec, "")
		if err != nil {
			return -1, err.Error()
		}
	}
	image := cbString(project.Environment["image"])
	if image == "" {
		return -1, "build environment image is required"
	}
	architecture := "linux/amd64"
	if strings.Contains(strings.ToUpper(cbString(project.Environment["type"])), "ARM") {
		architecture = "linux/arm64"
	}
	env["CODEBUILD_BUILD_ID"] = buildID
	env["CODEBUILD_PROJECT_NAME"] = project.Name
	env["CODEBUILD_SRC_DIR"] = "/codebuild/output/src"
	env["AWS_DEFAULT_REGION"] = firstNonEmpty(env["AWS_DEFAULT_REGION"], awsRegion())
	env["AWS_REGION"] = firstNonEmpty(env["AWS_REGION"], awsRegion())
	env = rewriteHostDockerInternalEnv(env)

	var script strings.Builder
	script.WriteString("set -e\n")
	for _, command := range plan.Commands {
		script.WriteString(command)
		script.WriteByte('\n')
	}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        image,
		Architecture: architecture,
		Command:      []string{"/bin/sh"},
		Args:         []string{"-c", script.String()},
		WorkingDir:   "/codebuild/output/src",
		Binds:        []string{filepath.Clean(workDir) + ":/codebuild/output/src:z"},
		Env:          env,
		ExtraHosts:   hostMetadataExtraHosts(),
		Timeout:      cbBuildTimeout(project),
		Labels:       map[string]string{"sockerless-codebuild-build": buildID},
		Sandbox:      sim.SandboxFargate,
	}, sim.NoopSink{})
	if err != nil {
		return -1, fmt.Sprintf("start build environment %s: %v", image, err)
	}
	cbRegisterBuildCancel(buildID, handle.Cancel)
	result := handle.Wait()
	cbUnregisterBuildCancel(buildID)
	if result.Error != nil {
		return -1, result.Error.Error()
	}
	if result.ExitCode != 0 {
		return result.ExitCode, fmt.Sprintf("Build command exited with status %d", result.ExitCode)
	}
	return 0, ""
}

func cbCheckoutSource(project CBProject, sourceVersion, workDir string) error {
	sourceType := strings.ToUpper(cbString(project.Source["type"]))
	switch sourceType {
	case "", "NO_SOURCE":
		return nil
	case "GITHUB", "GITHUB_ENTERPRISE", "GITLAB", "GITLAB_SELF_MANAGED", "BITBUCKET":
		location := cbString(project.Source["location"])
		if location == "" {
			return fmt.Errorf("source.location is required for %s", sourceType)
		}
		options := &git.CloneOptions{URL: location, Depth: 1, SingleBranch: true}
		if sourceVersion != "" && sourceVersion != "HEAD" {
			options.Depth = 0
			options.SingleBranch = false
		}
		if secret, ok := cbSourceCredentialForProject(project); ok {
			options.Auth = &githttp.BasicAuth{Username: secret.Username, Password: secret.Password}
		}
		repository, err := git.PlainClone(workDir, false, options)
		if err != nil {
			return fmt.Errorf("clone %s source %s: %w", sourceType, location, err)
		}
		if sourceVersion != "" && sourceVersion != "HEAD" {
			revision, err := repository.ResolveRevision(plumbing.Revision(sourceVersion))
			if err != nil {
				return fmt.Errorf("resolve source version %s: %w", sourceVersion, err)
			}
			tree, err := repository.Worktree()
			if err != nil {
				return fmt.Errorf("open source worktree: %w", err)
			}
			if err := tree.Checkout(&git.CheckoutOptions{Hash: *revision}); err != nil {
				return fmt.Errorf("checkout source version %s: %w", sourceVersion, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("source type %s is not executable by the AWS service slice", sourceType)
	}
}

type cbDecryptedSourceCredential struct {
	Username string
	Password string
}

func cbSourceCredentialForProject(project CBProject) (cbDecryptedSourceCredential, bool) {
	sourceType := strings.ToUpper(cbString(project.Source["type"]))
	if auth, ok := project.Source["auth"].(map[string]any); ok &&
		strings.EqualFold(cbString(auth["type"]), "SECRETS_MANAGER") {
		return cbSecretsManagerSourceCredential(cbString(auth["resource"]), sourceType)
	}
	serverType := sourceType
	switch sourceType {
	case "GITLAB_SELF_MANAGED":
		serverType = "GITLAB_SELF_MANAGED"
	case "GITHUB_ENTERPRISE":
		serverType = "GITHUB_ENTERPRISE"
	}
	for _, credential := range cbSourceCreds.List() {
		if !strings.EqualFold(credential.ServerType, serverType) {
			continue
		}
		if strings.EqualFold(credential.AuthType, "SECRETS_MANAGER") {
			return cbSecretsManagerSourceCredential(credential.Resource, serverType)
		}
		secret, ok := cbSourceCredSecrets.Get(credential.Arn)
		if !ok {
			return cbDecryptedSourceCredential{}, false
		}
		_, plaintext, decrypted := kmsDecryptBytes(secret.Ciphertext)
		if !decrypted {
			return cbDecryptedSourceCredential{}, false
		}
		username := secret.Username
		if username == "" {
			username = "oauth2"
		}
		return cbDecryptedSourceCredential{Username: username, Password: string(plaintext)}, true
	}
	return cbDecryptedSourceCredential{}, false
}

func cbSecretsManagerSourceCredential(resource, sourceType string) (cbDecryptedSourceCredential, bool) {
	secret, ok := resolveSMSecret(resource)
	if !ok {
		return cbDecryptedSourceCredential{}, false
	}
	version, ok := secret.versionByIDOrStage("", "")
	if !ok || version.SecretString == "" {
		return cbDecryptedSourceCredential{}, false
	}
	var value struct {
		ServerType string `json:"ServerType"`
		AuthType   string `json:"AuthType"`
		Token      string `json:"Token"`
		Username   string `json:"Username"`
	}
	if json.Unmarshal([]byte(version.SecretString), &value) != nil ||
		!strings.EqualFold(value.ServerType, sourceType) ||
		value.Token == "" {
		return cbDecryptedSourceCredential{}, false
	}
	username := value.Username
	if username == "" {
		username = "oauth2"
	}
	return cbDecryptedSourceCredential{Username: username, Password: value.Token}, true
}

// cbRecordBuildWorkspace stores where a build's source directory lives so
// report ingestion can read the build's raw result files out of it after the
// build container exits.
func cbRecordBuildWorkspace(buildID, workspace string) {
	cbMu.Lock()
	defer cbMu.Unlock()
	cbBuilds.Update(buildID, func(build *CBBuild) { build.Workspace = workspace })
}

func cbRegisterBuildCancel(buildID string, cancel func()) {
	cbBuildCancelMu.Lock()
	cbBuildCancels[buildID] = cancel
	build, exists := cbBuilds.Get(buildID)
	batch, batchExists := cbBuildBatches.Get(buildID)
	if (exists && build.BuildStatus == "IN_PROGRESS") ||
		(batchExists && batch.BuildBatchStatus == "IN_PROGRESS") {
		cbBuildCancelMu.Unlock()
		return
	}
	delete(cbBuildCancels, buildID)
	cbBuildCancelMu.Unlock()
	cancel()
}

func cbUnregisterBuildCancel(buildID string) {
	cbBuildCancelMu.Lock()
	defer cbBuildCancelMu.Unlock()
	delete(cbBuildCancels, buildID)
}

func cbCancelBuild(buildID string) {
	cbBuildCancelMu.Lock()
	cancel := cbBuildCancels[buildID]
	cbBuildCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func cbCompleteBuild(buildID string, exitCode int, reason string) {
	cbMu.Lock()
	defer cbMu.Unlock()

	build, ok := cbBuilds.Get(buildID)
	if !ok {
		return
	}
	if build.BuildStatus != "IN_PROGRESS" {
		// The build already settled — StopBuild is the way that happens while
		// the container is still running — so there are no reports to read, but
		// the source directory is still on disk and is this function's to remove.
		cbReleaseBuildWorkspace(&build)
		cbBuilds.Put(buildID, build)
		return
	}
	now := cbEpochNow()
	status := "SUCCEEDED"
	if exitCode != 0 {
		status = "FAILED"
	}
	build.BuildStatus = status
	build.EndTime = now
	buildPhase := CBPhase{PhaseType: "BUILD", PhaseStatus: status, StartTime: build.StartTime, EndTime: now, DurationInSeconds: now - build.StartTime}
	if reason != "" {
		// Failure detail rides the phase's contexts (the real Build
		// shape's per-phase PhaseContext), not the LogsLocation.
		// COMMAND_EXECUTION_ERROR is real CodeBuild's status code for a
		// failed buildspec command.
		buildPhase.Contexts = []CBPhaseContext{{
			StatusCode: "COMMAND_EXECUTION_ERROR",
			Message:    reason,
		}}
	}
	build.Phases = []CBPhase{
		{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: build.StartTime, EndTime: build.StartTime, DurationInSeconds: 0},
		buildPhase,
		{PhaseType: "COMPLETED", PhaseStatus: status, StartTime: now, EndTime: now, DurationInSeconds: 0},
	}

	build = cbProduceBuildReports(build, now)
	cbReleaseBuildWorkspace(&build)
	cbBuilds.Put(buildID, build)
}

// cbReleaseBuildWorkspace removes the build environment's source directory once
// nothing will read it again. Caller holds cbMu.
func cbReleaseBuildWorkspace(build *CBBuild) {
	if build.Workspace == "" {
		return
	}
	_ = os.RemoveAll(build.Workspace)
	build.Workspace = ""
}

// cbProduceBuildReports turns each entry of the build's buildspec reports
// section into a Report, reading the raw result files the entry names out of
// the build environment's source directory. Caller holds cbMu.
func cbProduceBuildReports(build CBBuild, now float64) CBBuild {
	for _, spec := range build.Reports {
		group, err := cbReportGroupForSpec(spec, build.ProjectName)
		if err != nil {
			build.Phases = cbAppendReportFailure(build.Phases, err.Error(), now)
			continue
		}
		reportName := build.ID
		if _, suffix, found := strings.Cut(build.ID, ":"); found {
			reportName = suffix
		}
		reportArn := cbARN("report/" + group.Name + ":" + reportName)
		report := CBReport{
			Arn:            reportArn,
			Type:           group.Type,
			Name:           reportName,
			ReportGroupArn: group.Arn,
			ExecutionId:    build.Arn,
			Created:        now,
			ExportConfig:   group.ExportConfig,
		}
		switch {
		case build.BuildStatus != "SUCCEEDED":
			// "INCOMPLETE: … The build was not completed because of an error
			// that is not related to the tests."
			report.Status = "INCOMPLETE"
		case build.Workspace == "":
			build.Phases = cbAppendReportFailure(build.Phases,
				"build environment source directory is unknown, so reports."+spec.Key+" cannot be read", now)
			report.Status = "INCOMPLETE"
		default:
			results, status, err := cbIngestReport(build.Workspace, spec, reportArn)
			if err != nil {
				build.Phases = cbAppendReportFailure(build.Phases, err.Error(), now)
				report.Status = "INCOMPLETE"
				break
			}
			results.TestCases, report.Truncated = cbTruncateTestCases(results.TestCases)
			report.Status = status
			cbReportResults.Put(reportArn, results)
		}
		cbReports.Put(reportArn, report)
		build.ReportArns = append(build.ReportArns, reportArn)
	}
	return build
}

// cbAppendReportFailure records why a report could not be produced on the
// build's phases, where the real Build shape carries per-phase failure detail.
func cbAppendReportFailure(phases []CBPhase, message string, now float64) []CBPhase {
	for i := range phases {
		if phases[i].PhaseType != "COMPLETED" {
			continue
		}
		phases[i].PhaseStatus = "FAILED"
		phases[i].Contexts = append(phases[i].Contexts, CBPhaseContext{
			StatusCode: "CLIENT_ERROR",
			Message:    message,
		})
		return phases
	}
	return append(phases, CBPhase{
		PhaseType: "COMPLETED", PhaseStatus: "FAILED", StartTime: now, EndTime: now,
		Contexts: []CBPhaseContext{{StatusCode: "CLIENT_ERROR", Message: message}},
	})
}

func handleCBBatchGetBuilds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var found []CBBuild
	var notFound []string
	for _, id := range req.IDs {
		if b, ok := cbBuilds.Get(id); ok {
			found = append(found, b)
		} else {
			notFound = append(notFound, id)
		}
	}
	if found == nil {
		found = []CBBuild{}
	}
	if notFound == nil {
		notFound = []string{}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"builds":         found,
		"buildsNotFound": notFound,
	})
}

func handleCBListBuildsForProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectName string `json:"projectName"`
		SortOrder   string `json:"sortOrder"`
		NextToken   string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := cbBuilds.List()
	var builds []CBBuild
	for _, b := range all {
		if b.ProjectName == req.ProjectName {
			builds = append(builds, b)
		}
	}
	ids := cbSortBuildIDs(builds, req.SortOrder)
	page, nextTok := awsPage(ids, req.NextToken, 0, 100)
	resp := map[string]any{"ids": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

func handleCBListBuilds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder string `json:"sortOrder"`
		NextToken string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := cbBuilds.List()
	ids := cbSortBuildIDs(all, req.SortOrder)
	page, nextTok := awsPage(ids, req.NextToken, 0, 100)
	resp := map[string]any{"ids": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// cbSortBuildIDs orders builds by start time and returns their IDs. AWS
// ListBuilds / ListBuildsForProject default to DESCENDING (most-recent first);
// sortOrder="ASCENDING" reverses it. Ties break on ID for a stable page cursor.
func cbSortBuildIDs(builds []CBBuild, sortOrder string) []string {
	ascending := strings.EqualFold(sortOrder, "ASCENDING")
	sort.Slice(builds, func(i, j int) bool {
		if ascending {
			return builds[i].Seq < builds[j].Seq
		}
		return builds[i].Seq > builds[j].Seq
	})
	ids := make([]string, 0, len(builds))
	for _, b := range builds {
		ids = append(ids, b.ID)
	}
	return ids
}

// --- Report groups ---

func handleCBCreateReportGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string         `json:"name"`
		Type         string         `json:"type"`
		ExportConfig map[string]any `json:"exportConfig"`
		Tags         []CBTag        `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		cbWriteError(w, "InvalidInputException", "name is required")
		return
	}
	if req.Type == "" {
		cbWriteError(w, "InvalidInputException", "type is required")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	arn := cbARN("report-group/" + req.Name)
	if _, ok := cbReportGrps.Get(arn); ok {
		cbWriteError(w, "ResourceAlreadyExistsException", "Report group already exists: "+req.Name)
		return
	}
	now := cbEpochNow()
	rg := CBReportGroup{
		Arn:          arn,
		Name:         req.Name,
		Type:         req.Type,
		ExportConfig: req.ExportConfig,
		Created:      now,
		LastModified: now,
		Status:       "ACTIVE",
		Tags:         req.Tags,
	}
	cbReportGrps.Put(arn, rg)
	cbWriteJSON(w, http.StatusOK, map[string]any{"reportGroup": rg})
}

func handleCBUpdateReportGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn          string         `json:"arn"`
		ExportConfig map[string]any `json:"exportConfig"`
		Tags         []CBTag        `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	rg, ok := cbReportGrps.Get(req.Arn)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Report group not found: "+req.Arn)
		return
	}
	if req.ExportConfig != nil {
		rg.ExportConfig = req.ExportConfig
	}
	if req.Tags != nil {
		rg.Tags = req.Tags
	}
	rg.LastModified = cbEpochNow()
	cbReportGrps.Put(req.Arn, rg)
	cbWriteJSON(w, http.StatusOK, map[string]any{"reportGroup": rg})
}

func handleCBDeleteReportGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn           string `json:"arn"`
		DeleteReports bool   `json:"deleteReports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	if req.DeleteReports {
		for _, rep := range cbReports.List() {
			if rep.ReportGroupArn == req.Arn {
				cbReports.Delete(rep.Arn)
				cbReportResults.Delete(rep.Arn)
			}
		}
	}
	cbReportGrps.Delete(req.Arn)
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCBListReportGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := cbReportGrps.List()
	arns := make([]string, 0, len(all))
	for _, rg := range all {
		arns = append(arns, rg.Arn)
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

func handleCBBatchGetReportGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportGroupArns []string `json:"reportGroupArns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var found []CBReportGroup
	var notFound []string
	for _, arn := range req.ReportGroupArns {
		if rg, ok := cbReportGrps.Get(arn); ok {
			found = append(found, rg)
		} else {
			notFound = append(notFound, arn)
		}
	}
	if found == nil {
		found = []CBReportGroup{}
	}
	if notFound == nil {
		notFound = []string{}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"reportGroups":         found,
		"reportGroupsNotFound": notFound,
	})
}

// --- Reports ---

func handleCBListReports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cbWriteReportArnsPage(w, cbReports.List(), "", req.SortOrder, req.MaxResults, req.NextToken)
}

func handleCBListReportsForReportGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportGroupArn string `json:"reportGroupArn"`
		SortOrder      string `json:"sortOrder"`
		MaxResults     int    `json:"maxResults"`
		NextToken      string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ReportGroupArn == "" {
		cbWriteError(w, "InvalidInputException", "reportGroupArn is required")
		return
	}
	cbWriteReportArnsPage(w, cbReports.List(), req.ReportGroupArn, req.SortOrder, req.MaxResults, req.NextToken)
}

// cbWriteReportArnsPage filters reports (optionally by group), sorts by creation
// order, and writes a paged {reports:[arns],nextToken}. ListReports defaults to
// DESCENDING (most-recent first), matching real CodeBuild.
func cbWriteReportArnsPage(w http.ResponseWriter, all []CBReport, groupArn, sortOrder string, maxResults int, nextToken string) {
	var reports []CBReport
	for _, rep := range all {
		if groupArn == "" || rep.ReportGroupArn == groupArn {
			reports = append(reports, rep)
		}
	}
	ascending := strings.EqualFold(sortOrder, "ASCENDING")
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Created == reports[j].Created {
			if ascending {
				return reports[i].Arn < reports[j].Arn
			}
			return reports[i].Arn > reports[j].Arn
		}
		if ascending {
			return reports[i].Created < reports[j].Created
		}
		return reports[i].Created > reports[j].Created
	})
	arns := make([]string, 0, len(reports))
	for _, rep := range reports {
		arns = append(arns, rep.Arn)
	}
	page, nextTok := awsPage(arns, nextToken, maxResults, 100)
	resp := map[string]any{"reports": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

func handleCBBatchGetReports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportArns []string `json:"reportArns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var found []CBReport
	var notFound []string
	for _, arn := range req.ReportArns {
		if rep, ok := cbReports.Get(arn); ok {
			found = append(found, rep)
		} else {
			notFound = append(notFound, arn)
		}
	}
	if found == nil {
		found = []CBReport{}
	}
	if notFound == nil {
		notFound = []string{}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"reports":         found,
		"reportsNotFound": notFound,
	})
}

// --- Source credentials ---

func handleCBImportSourceCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token           string `json:"token"`
		Username        string `json:"username"`
		ServerType      string `json:"serverType"`
		AuthType        string `json:"authType"`
		ShouldOverwrite *bool  `json:"shouldOverwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ServerType == "" || req.AuthType == "" {
		cbWriteError(w, "InvalidInputException", "serverType and authType are required")
		return
	}
	if req.Token == "" {
		cbWriteError(w, "InvalidInputException", "token is required")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	// Real CodeBuild keys a source credential by (serverType, authType) per
	// region/account, so the ARN is deterministic from the server type.
	arn := cbARN("token/" + strings.ToLower(req.ServerType))
	overwrite := req.ShouldOverwrite == nil || *req.ShouldOverwrite
	if _, ok := cbSourceCreds.Get(arn); ok && !overwrite {
		cbWriteError(w, "ResourceAlreadyExistsException", "Source credentials already exist for server type: "+req.ServerType)
		return
	}
	// For SECRETS_MANAGER auth, the token is a secret ARN echoed back as resource.
	resource := ""
	if strings.EqualFold(req.AuthType, "SECRETS_MANAGER") {
		resource = req.Token
	}
	cred := CBSourceCredential{
		Arn:        arn,
		ServerType: req.ServerType,
		AuthType:   req.AuthType,
		Resource:   resource,
	}
	if _, ok := kmsGetKeyMaterial(cbAWSOwnedKMSKeyID); !ok {
		if _, err := kmsGenerateKeyMaterial(cbAWSOwnedKMSKeyID); err != nil {
			cbWriteError(w, "InternalFailure", "AWS owned KMS key material could not be generated")
			return
		}
	}
	ciphertext, encrypted := kmsEncryptBytes(cbAWSOwnedKMSKeyID, []byte(req.Token))
	if !encrypted {
		cbWriteError(w, "InternalFailure", "source credential could not be encrypted")
		return
	}
	cbSourceCreds.Put(arn, cred)
	cbSourceCredSecrets.Put(arn, cbSourceCredentialSecret{
		Arn: arn, Username: req.Username, Ciphertext: ciphertext,
	})
	cbWriteJSON(w, http.StatusOK, map[string]any{"arn": arn})
}

func handleCBListSourceCredentials(w http.ResponseWriter, r *http.Request) {
	all := cbSourceCreds.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Arn < all[j].Arn })
	if all == nil {
		all = []CBSourceCredential{}
	}
	cbWriteJSON(w, http.StatusOK, map[string]any{"sourceCredentialsInfos": all})
}

func handleCBDeleteSourceCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"arn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	if _, ok := cbSourceCreds.Get(req.Arn); !ok {
		cbWriteError(w, "ResourceNotFoundException", "Source credentials not found: "+req.Arn)
		return
	}
	cbSourceCreds.Delete(req.Arn)
	cbSourceCredSecrets.Delete(req.Arn)
	cbWriteJSON(w, http.StatusOK, map[string]any{"arn": req.Arn})
}

func reverseStrings(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func cbNameFromARN(arn string) string {
	// arn:aws:codebuild:us-east-1:123456789012:project/name
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return arn
}

func cbString(v any) string {
	s, _ := v.(string)
	return s
}

func cbEnvironment(projectEnv map[string]any, overrides []map[string]any) map[string]string {
	env := map[string]string{
		"PATH": os.Getenv("PATH"),
		"HOME": os.Getenv("HOME"),
	}
	for k, v := range cbEnvironmentValues(projectEnv["environmentVariables"]) {
		env[k] = v
	}
	for _, item := range overrides {
		name := cbString(item["name"])
		if name == "" {
			name = cbString(item["Name"])
		}
		if name == "" {
			continue
		}
		value := cbString(item["value"])
		if value == "" {
			value = cbString(item["Value"])
		}
		env[name] = value
	}
	return env
}

func cbEnvironmentValues(v any) map[string]string {
	env := map[string]string{}
	values, ok := v.([]any)
	if !ok {
		return env
	}
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name := cbString(item["name"])
		if name == "" {
			name = cbString(item["Name"])
		}
		if name == "" {
			continue
		}
		value := cbString(item["value"])
		if value == "" {
			value = cbString(item["Value"])
		}
		env[name] = value
	}
	return env
}
