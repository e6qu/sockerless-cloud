package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/listq"
	"github.com/e6qu/sockerless-cloud/sim/signals"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// AWS CodeBuild speaks AWS-JSON 1.1. A build runs the buildspec commands in the
// project's build-environment image and records the container's outcome.

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
	// cbLogsLocation reads the CloudWatch Logs half.
	LogsConfig              map[string]any   `json:"logsConfig,omitempty"`
	SecondarySources        []map[string]any `json:"secondarySources,omitempty"`
	SecondarySourceVersions []map[string]any `json:"secondarySourceVersions,omitempty"`
	SecondaryArtifacts      []map[string]any `json:"secondaryArtifacts,omitempty"`
	Cache                   map[string]any   `json:"cache,omitempty"`
	EncryptionKey           string           `json:"encryptionKey,omitempty"`
	BuildBatchConfig        map[string]any   `json:"buildBatchConfig,omitempty"`
	AutoRetryLimit          *int             `json:"autoRetryLimit,omitempty"`
}

type CBBuild struct {
	ID                      string             `json:"id"`
	Arn                     string             `json:"arn"`
	ProjectName             string             `json:"projectName"`
	SourceVersion           string             `json:"sourceVersion,omitempty"`
	BuildStatus             string             `json:"buildStatus"`
	StartTime               float64            `json:"startTime"`
	EndTime                 float64            `json:"endTime"`
	Phases                  []CBPhase          `json:"phases"`
	Source                  map[string]any     `json:"source,omitempty"`
	SecondarySources        []map[string]any   `json:"secondarySources,omitempty"`
	SecondarySourceVersions []map[string]any   `json:"secondarySourceVersions,omitempty"`
	Artifacts               map[string]any     `json:"artifacts,omitempty"`
	SecondaryArtifacts      []map[string]any   `json:"secondaryArtifacts,omitempty"`
	Cache                   map[string]any     `json:"cache,omitempty"`
	Logs                    map[string]any     `json:"logs"`
	Environment             map[string]any     `json:"environment,omitempty"`
	ServiceRole             string             `json:"serviceRole,omitempty"`
	TimeoutInMinutes        int                `json:"timeoutInMinutes,omitempty"`
	QueuedTimeoutInMinutes  int                `json:"queuedTimeoutInMinutes,omitempty"`
	EncryptionKey           string             `json:"encryptionKey,omitempty"`
	DebugSession            map[string]any     `json:"debugSession,omitempty"`
	BuildBatchArn           string             `json:"buildBatchArn,omitempty"`
	AutoRetryConfig         *CBAutoRetryConfig `json:"autoRetryConfig,omitempty"`
	ReportArns              []string           `json:"reportArns,omitempty"`
	// Seq orders ListBuilds by start order within one wall-clock second.
	Seq int64 `json:"-"`
	// The build produces one Report per entry on completion.
	Reports []cbReportSpec `json:"-"`
	// Workspace is bound into the build container at CODEBUILD_SRC_DIR. Report
	// ingestion reads it after the container exits, so it is durable: a restart
	// that adopts a running build still knows where its reports land.
	Workspace string `json:"-"`
	// BuildPlan and RuntimeEnvironment let a restart resume a build that has no
	// container yet; once one exists, recovery adopts it instead.
	BuildPlan          *cbBuildPlan      `json:"-"`
	RuntimeEnvironment map[string]string `json:"-"`
	// Project carries the build's overrides; recovery and RetryBuild run from it.
	Project CBProject `json:"-"`
}

type CBAutoRetryConfig struct {
	AutoRetryLimit    int    `json:"autoRetryLimit"`
	AutoRetryNumber   int    `json:"autoRetryNumber"`
	NextAutoRetry     string `json:"nextAutoRetry,omitempty"`
	PreviousAutoRetry string `json:"previousAutoRetry,omitempty"`
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

// Logs stream by default to `/aws/codebuild/<project>` under the build's id,
// or where logsConfig says, unless logsConfig disables them. The sandbox path
// passes no build id and reports only the status.
func cbLogsLocation(p CBProject, buildID string) map[string]any {
	group, stream, enabled := cbCloudWatchLogsTarget(p, buildID)
	cw := map[string]any{"status": "DISABLED"}
	if enabled {
		cw["status"] = "ENABLED"
		if buildID != "" {
			cw["groupName"] = group
			cw["streamName"] = stream
		}
	}
	logs := map[string]any{"cloudWatchLogs": cw}
	if enabled && buildID != "" {
		logs["groupName"] = group
		logs["streamName"] = stream
		logs["deepLink"] = "https://console.aws.amazon.com/cloudwatch/home?region=" + awsRegion() +
			"#logEvent:group=" + group + ";stream=" + stream
		logs["cloudWatchLogsArn"] = "arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":log-group:" + group + ":log-stream:" + stream
	}
	return logs
}

// An absent logsConfig.cloudWatchLogs means enabled at the defaults.
func cbCloudWatchLogsTarget(p CBProject, buildID string) (group, stream string, enabled bool) {
	enabled = true
	group = "/aws/codebuild/" + p.Name
	streamPrefix := ""
	if cw, ok := p.LogsConfig["cloudWatchLogs"].(map[string]any); ok {
		if strings.EqualFold(cbString(cw["status"]), "DISABLED") {
			enabled = false
		}
		if g := cbString(cw["groupName"]); g != "" {
			group = g
		}
		streamPrefix = cbString(cw["streamName"])
	}
	stream = buildID
	if _, id, found := strings.Cut(buildID, ":"); found {
		stream = id
	}
	if streamPrefix != "" {
		stream = streamPrefix + "/" + stream
	}
	return group, stream, enabled
}

// Discard output when the project disabled the logs.
func cbBuildLogSink(p CBProject, buildID string) sim.LogSink {
	group, stream, enabled := cbCloudWatchLogsTarget(p, buildID)
	if !enabled {
		return sim.NoopSink{}
	}
	// AWS CodeBuild creates the group and stream before the build starts, so the
	// stream exists even when the environment prints nothing.
	cwEnsureLogStream(group, stream)
	return &cwLogSink{logGroup: group, logStream: stream}
}

type CBTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// status is read-only, ACTIVE for a live group; type is TEST or
// CODE_COVERAGE.
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

// A build whose buildspec references the report group produces the report
// from its terminal state.
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

// The API never echoes the token; for SECRETS_MANAGER it shows the resource.
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
	cbBuildChanges      = signals.New[string]()
	cbReportGrps        sim.Store[CBReportGroup]
	cbReports           sim.Store[CBReport]
	cbReportResults     sim.Store[CBReportResults]
	cbSourceCreds       sim.Store[CBSourceCredential]
	cbSourceCredSecrets sim.Store[cbSourceCredentialSecret]
	cbMu                sync.Mutex
	cbBuildCancelMu     sync.Mutex
	cbBuildCancels      = map[string]func(){}
)

func registerCodeBuild(r *AWSRouter, srv *sim.Server) {
	cbProjects = sim.MakeStore[CBProject](srv.DB(), "codebuild_projects")
	cbBuilds = sim.Announcing(sim.MakeStore[CBBuild](srv.DB(), "codebuild_builds"), cbBuildChanges)
	cbReportGrps = sim.MakeStore[CBReportGroup](srv.DB(), "codebuild_report_groups")
	cbReports = sim.MakeStore[CBReport](srv.DB(), "codebuild_reports")
	cbReportResults = sim.MakeStore[CBReportResults](srv.DB(), "codebuild_report_results")
	cbSourceCreds = sim.MakeStore[CBSourceCredential](srv.DB(), "codebuild_source_credentials")
	cbSourceCredSecrets = sim.MakeStore[cbSourceCredentialSecret](srv.DB(), "codebuild_source_credential_secrets")
	cbIdempotencyTokens = sim.MakeStore[cbIdempotencyRecord](srv.DB(), "codebuild_idempotency_tokens")
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

// Orders builds created within the same wall-clock second.
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
	existing, err := cbEnvironmentBackend.Find(context.Background(), map[string]string{
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
	handle, err := cbEnvironmentBackend.Adopt(context.Background(), existing[0].ID, sim.ContainerConfig{Timeout: remaining}, sim.NoopSink{})
	if err != nil {
		return false, fmt.Errorf("adopt %s %s container: %w", resourceName, id, err)
	}
	cbRegisterBuildCancel(id, handle.Cancel)
	bg.Go(func() {
		result := handle.Wait()
		cbUnregisterBuildCancel(id)
		reason := ""
		if result.Error != nil {
			reason = result.Error.Error()
		} else if result.ExitCode != 0 {
			reason = fmt.Sprintf("Build command exited with status %d", result.ExitCode)
		}
		complete(id, result.ExitCode, reason)
	})
	return true, nil
}

func cbRecoverBuilds() error {
	for _, build := range cbBuilds.List() {
		if build.BuildStatus != "IN_PROGRESS" {
			continue
		}
		project := build.Project
		if project.Name == "" {
			return fmt.Errorf("build %s has no persisted build settings", build.ID)
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
	awsWriteError(w, awsErrorEnvelope{Protocol: awsJSON11, Model: "codebuild"}, http.StatusBadRequest, code, msg, "")
}

// cbCanAssumeRole refuses a role whose trust policy does not let AWS CodeBuild
// assume it for sourceArn.
func cbCanAssumeRole(w http.ResponseWriter, role, sourceArn string) bool {
	if _, err := iamServiceTrustedRole(role, "codebuild.amazonaws.com", sourceArn); err != nil {
		cbWriteError(w, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
		return false
	}
	return true
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
		LogsConfig   map[string]any `json:"logsConfig"`
		cbProjectSettings
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if !cbCanAssumeRole(w, req.ServiceRole, cbARN("project/"+req.Name)) {
		return
	}
	if !cbCanAssumeBatchRole(w, req.BuildBatchConfig, cbARN("project/"+req.Name)) {
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
		LogsConfig:   req.LogsConfig,
	}
	req.apply(&p)
	cbProjects.Put(req.Name, p)
	cbWriteJSON(w, http.StatusOK, map[string]any{"project": p})
}

func handleCBBatchGetProjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := cbProjects.List()
	names := make([]string, 0, len(all))
	for _, p := range all {
		names = append(names, p.Name)
	}
	// ListProjects defaults to ASCENDING.
	descending := strings.EqualFold(req.SortOrder, "DESCENDING")
	page, nextTok, pageOK := awsKeyPage(w, cbBadToken, names, func(s string) string { return cbOrderKey(s, descending) }, req.NextToken, 0, 100)
	if !pageOK {
		return
	}
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
		cbProjectSettings
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if !cbCanAssumeBatchRole(w, req.BuildBatchConfig, p.Arn) {
		return
	}
	req.apply(&p)
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
		if !cbCanAssumeRole(w, req.ServiceRole, cbARN("project/"+req.Name)) {
			return
		}
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
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbMu.Lock()
	defer cbMu.Unlock()

	cbProjects.Delete(req.Name)
	cbWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCBStartBuild(w http.ResponseWriter, r *http.Request) {
	var req cbStartBuildInput
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	p, ok := cbProjects.Get(req.ProjectName)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Project not found: "+req.ProjectName)
		return
	}
	if req.ServiceRoleOverride != "" && !cbCanAssumeRole(w, req.ServiceRoleOverride, p.Arn) {
		return
	}
	effective, err := cbApplyBuildOverrides(p, req)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	plan, err := cbBuildPlanForProject(effective, req.SourceVersion)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	reportSpecs, err := cbBuildReportSpecs(effective)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	batchArn := cbCallerBuildBatchArn(r, p.Name)
	token := req.IdempotencyToken
	keyed := req
	keyed.IdempotencyToken = ""
	digest := cbRequestDigest(keyed)

	cbMu.Lock()
	defer cbMu.Unlock()

	id, repeated, err := cbIdempotentID("StartBuild", token, digest)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if build, found := cbBuilds.Get(id); repeated && found {
		cbWriteJSON(w, http.StatusOK, map[string]any{"build": build})
		return
	}
	build := cbLaunchBuild(effective, plan, reportSpecs, req.SourceVersion, cbDebugSession(req.DebugSessionEnabled),
		batchArn, cbNewAutoRetryConfig(effective, 0, ""))
	cbRecordIdempotencyToken("StartBuild", token, digest, build.ID)
	cbWriteJSON(w, http.StatusOK, map[string]any{"build": build})
}

// nil when the build retries nothing automatically.
func cbNewAutoRetryConfig(project CBProject, number int, previous string) *CBAutoRetryConfig {
	if project.AutoRetryLimit == nil {
		return nil
	}
	return &CBAutoRetryConfig{AutoRetryLimit: *project.AutoRetryLimit, AutoRetryNumber: number, PreviousAutoRetry: previous}
}

// AWS CodeBuild starts each build of a batch as the batch's service role.
func cbCallerBuildBatchArn(r *http.Request, projectName string) string {
	caller, ok := sim.CallerOf(r.Context())
	if !ok || caller.Service != "codebuild.amazonaws.com" {
		return ""
	}
	batchID, ok := strings.CutPrefix(caller.Source, cbARN("build-batch/"))
	if !ok {
		return ""
	}
	batch, ok := cbBuildBatches.Get(batchID)
	if !ok || batch.ProjectName != projectName {
		return ""
	}
	return batch.Arn
}

// Caller holds cbMu.
func cbLaunchBuild(effective CBProject, plan cbBuildPlan, reports []cbReportSpec, sourceVersion string,
	debugSession map[string]any, batchArn string, autoRetry *CBAutoRetryConfig,
) CBBuild {
	buildID := effective.Name + ":" + uuid.New().String()
	now := cbEpochNow()
	runtimeEnvironment := cbEnvironment(effective.Environment, nil)
	build := CBBuild{
		ID:                      buildID,
		Arn:                     cbARN("build/" + buildID),
		ProjectName:             effective.Name,
		SourceVersion:           sourceVersion,
		BuildStatus:             "IN_PROGRESS",
		Seq:                     cbNextBuildSeq(),
		StartTime:               now,
		EndTime:                 now,
		Source:                  effective.Source,
		SecondarySources:        effective.SecondarySources,
		SecondarySourceVersions: effective.SecondarySourceVersions,
		Artifacts:               cbBuildArtifacts(effective.Artifacts),
		SecondaryArtifacts:      cbBuildArtifactsList(effective.SecondaryArtifacts),
		Cache:                   effective.Cache,
		Environment:             effective.Environment,
		ServiceRole:             effective.ServiceRole,
		TimeoutInMinutes:        int(cbBuildTimeout(effective) / time.Minute),
		QueuedTimeoutInMinutes:  effective.QueueTimeout,
		EncryptionKey:           effective.EncryptionKey,
		DebugSession:            debugSession,
		BuildBatchArn:           batchArn,
		AutoRetryConfig:         autoRetry,
		Reports:                 reports,
		BuildPlan:               &plan,
		RuntimeEnvironment:      runtimeEnvironment,
		Project:                 effective,
		Phases: []CBPhase{
			{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: now, EndTime: now, DurationInSeconds: 0},
			{PhaseType: "BUILD", PhaseStatus: "IN_PROGRESS", StartTime: now},
		},
		Logs: cbLogsLocation(effective, buildID),
	}
	cbBuilds.Put(buildID, build)
	go cbRunBuild(buildID, effective, plan, runtimeEnvironment)
	return build
}

func handleCBStopBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	// A settled build keeps its terminal status.
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
		cbNotifyBuildBatch(build)
	}
	return build
}

func handleCBRetryBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID               string `json:"id"`
		IdempotencyToken string `json:"idempotencyToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	prior, ok := cbBuilds.Get(req.ID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Build not found: "+req.ID)
		return
	}
	if prior.Project.Name == "" {
		cbWriteError(w, "InvalidInputException", "build "+req.ID+" has no persisted build settings")
		return
	}
	plan, err := cbBuildPlanForProject(prior.Project, prior.SourceVersion)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	batchArn := cbCallerBuildBatchArn(r, prior.ProjectName)
	digest := cbRequestDigest(req.ID)

	cbMu.Lock()
	defer cbMu.Unlock()

	id, repeated, err := cbIdempotentID("RetryBuild", req.IdempotencyToken, digest)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if build, found := cbBuilds.Get(id); repeated && found {
		cbWriteJSON(w, http.StatusOK, map[string]any{"build": build})
		return
	}
	build := cbLaunchBuild(prior.Project, plan, prior.Reports, prior.SourceVersion, prior.DebugSession,
		batchArn, cbNewAutoRetryConfig(prior.Project, 0, ""))
	cbRecordIdempotencyToken("RetryBuild", req.IdempotencyToken, digest, build.ID)
	cbWriteJSON(w, http.StatusOK, map[string]any{"build": build})
}

type cbBuildspec struct {
	Phases map[string]struct {
		Commands []string `yaml:"commands"`
	} `yaml:"phases"`
	// A build produces one Report per entry.
	Reports map[string]cbReportSpec `yaml:"reports"`
}

type cbBuildPlan struct {
	Commands      []string
	SourceVersion string
	// Empty reads buildspec.yml.
	BuildspecPath string
}

// In report-group-key order.
func cbBuildReportSpecs(project CBProject) ([]cbReportSpec, error) {
	buildspec := cbString(project.Source["buildspec"])
	inline, err := cbInlineBuildspec(buildspec)
	if err != nil || !inline {
		return nil, err
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

// The buildspec may be a declaration or the path of a file.
func cbInlineBuildspec(buildspec string) (bool, error) {
	if strings.TrimSpace(buildspec) == "" {
		return false, nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(buildspec), &document); err != nil {
		return false, fmt.Errorf("invalid buildspec: %w", err)
	}
	return len(document.Content) == 1 && document.Content[0].Kind == yaml.MappingNode, nil
}

func cbBuildspecCommands(buildspec string) ([]string, error) {
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

// Use an inline buildspec's commands, or the buildspec file in the source.
func cbBuildPlanForProject(project CBProject, sourceVersion string) (cbBuildPlan, error) {
	buildspec := cbString(project.Source["buildspec"])
	plan := cbBuildPlan{SourceVersion: sourceVersion}
	if strings.HasPrefix(buildspec, "arn:") {
		return cbBuildPlan{}, fmt.Errorf("buildspec %s names an Amazon S3 object, which the AWS service slice does not read", buildspec)
	}
	inline, err := cbInlineBuildspec(buildspec)
	if err != nil {
		return cbBuildPlan{}, err
	}
	noSource := strings.EqualFold(cbString(project.Source["type"]), "NO_SOURCE")
	switch {
	case inline:
		plan.Commands, err = cbBuildspecCommands(buildspec)
		if err != nil {
			return cbBuildPlan{}, err
		}
	case noSource && buildspec == "":
		return cbBuildPlan{}, fmt.Errorf("source.buildspec is required for NO_SOURCE builds")
	case noSource:
		return cbBuildPlan{}, fmt.Errorf("buildspec %s names a file, and a NO_SOURCE build has no source to read it from", buildspec)
	default:
		if buildspec != "" && !filepath.IsLocal(filepath.FromSlash(buildspec)) {
			return cbBuildPlan{}, fmt.Errorf("buildspec %s is not a path within the source directory", buildspec)
		}
		plan.BuildspecPath = buildspec
	}
	return plan, nil
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
	// cbCompleteBuild reads the reports from the workspace and removes it, so an
	// adopted build finds it too.
	cbRecordBuildWorkspace(buildID, workDir)
	defer func() {
		if _, ok := cbBuilds.Get(buildID); !ok {
			_ = os.RemoveAll(workDir)
		}
	}()

	if err := cbCheckoutSource(project, plan.SourceVersion, workDir); err != nil {
		return -1, err.Error()
	}
	if len(plan.Commands) == 0 {
		path := firstNonEmpty(plan.BuildspecPath, "buildspec.yml")
		buildspec, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(path)))
		if err != nil {
			return -1, fmt.Sprintf("read source %s: %v", path, err)
		}
		plan.Commands, err = cbBuildspecCommands(string(buildspec))
		if err != nil {
			return -1, err.Error()
		}
	}
	image := cbString(project.Environment["image"])
	if image == "" {
		return -1, "build environment image is required"
	}
	image = cbEnvironmentImage(image)
	architecture := "linux/amd64"
	if strings.Contains(strings.ToUpper(cbString(project.Environment["type"])), "ARM") {
		architecture = "linux/arm64"
	}
	// The simulator does not grant privilegedMode; the build runs unprivileged.
	sandbox := SandboxFargate
	network, err := awsPlatformNetwork(context.Background())
	if err != nil {
		return -1, fmt.Sprintf("build environment network: %v", err)
	}
	holder := roleHolder("codebuild", buildID)
	defer revokeRoleCredentials(holder)
	services, err := cbEnvironmentServiceEnv(network, holder, project.ServiceRole, buildID)
	if err != nil {
		return -1, fmt.Sprintf("build environment service endpoint: %v", err)
	}
	// The project's and build's own variables win.
	env = workloadhost.MergeEnv(services.Env, env)
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
	handle, err := services.place(context.Background(), cbEnvironmentBackend, sim.ContainerConfig{
		Image:        image,
		Architecture: architecture,
		Command:      []string{"/bin/sh"},
		Args:         []string{"-c", script.String()},
		WorkingDir:   "/codebuild/output/src",
		Binds:        []string{filepath.Clean(workDir) + ":/codebuild/output/src:z"},
		Env:          env,
		ExtraHosts:   workloadhost.ExtraHosts(),
		Network:      network.Name,
		DNS:          network.DNS(),
		Timeout:      cbBuildTimeout(project),
		Labels:       map[string]string{"sockerless-codebuild-build": buildID},
		Sandbox:      sandbox,
	}, cbBuildLogSink(project, buildID))
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

func cbInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
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
		// gitCloneDepth 0 clones the whole history.
		if depth, ok := cbInt(project.Source["gitCloneDepth"]); ok {
			options.Depth = depth
		}
		if insecure, _ := project.Source["insecureSsl"].(bool); insecure {
			options.InsecureSkipTLS = true
		}
		if submodules, ok := project.Source["gitSubmodulesConfig"].(map[string]any); ok {
			if fetch, _ := submodules["fetchSubmodules"].(bool); fetch {
				options.RecurseSubmodules = git.DefaultSubmoduleRecursionDepth
			}
		}
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

func cbRecordBuildWorkspace(buildID, workspace string) {
	cbMu.Lock()
	defer cbMu.Unlock()
	cbBuilds.Update(buildID, func(build *CBBuild) { build.Workspace = workspace })
}

func cbRegisterBuildCancel(buildID string, cancel func()) {
	cbBuildCancelMu.Lock()
	cbBuildCancels[buildID] = cancel
	if build, exists := cbBuilds.Get(buildID); exists && build.BuildStatus == "IN_PROGRESS" {
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
		// StopBuild settled the build while the container ran, so there are no
		// reports, but this function still removes the workspace.
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
		// Failure detail rides the phase's contexts; COMMAND_EXECUTION_ERROR is the
		// service's code for a failed buildspec command.
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
	if retry := build.AutoRetryConfig; status == "FAILED" && retry != nil && retry.AutoRetryNumber < retry.AutoRetryLimit {
		build.AutoRetryConfig.NextAutoRetry = cbAutoRetryBuild(build).Arn
	}
	cbBuilds.Put(buildID, build)
	cbNotifyBuildBatch(build)
}

// Caller holds cbMu.
func cbAutoRetryBuild(failed CBBuild) CBBuild {
	plan := cbBuildPlan{SourceVersion: failed.SourceVersion}
	if failed.BuildPlan != nil {
		plan = *failed.BuildPlan
	}
	retry := *failed.AutoRetryConfig
	return cbLaunchBuild(failed.Project, plan, failed.Reports, failed.SourceVersion, failed.DebugSession, failed.BuildBatchArn,
		&CBAutoRetryConfig{AutoRetryLimit: retry.AutoRetryLimit, AutoRetryNumber: retry.AutoRetryNumber + 1, PreviousAutoRetry: failed.Arn})
}

// Caller holds cbMu.
func cbReleaseBuildWorkspace(build *CBBuild) {
	if build.Workspace == "" {
		return
	}
	_ = os.RemoveAll(build.Workspace)
	build.Workspace = ""
}

// Caller holds cbMu.
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
			// "INCOMPLETE: … The build was not completed because of an error that is
			// not related to the tests."
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

// The Build shape carries per-phase failure detail.
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	page, nextTok, pageOK := cbBuildIDsPage(w, builds, req.SortOrder, req.NextToken)
	if !pageOK {
		return
	}
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
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := cbBuilds.List()
	page, nextTok, pageOK := cbBuildIDsPage(w, all, req.SortOrder, req.NextToken)
	if !pageOK {
		return
	}
	resp := map[string]any{"ids": page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// ListBuilds and ListBuildsForProject default to DESCENDING.
func cbBuildIDsPage(w http.ResponseWriter, builds []CBBuild, sortOrder, token string) ([]string, string, bool) {
	descending := !strings.EqualFold(sortOrder, "ASCENDING")
	page, next, ok := awsKeyPage(w, cbBadToken, builds, func(b CBBuild) string {
		return cbOrderKey(listq.IntKey(b.Seq), descending)
	}, token, 0, 100)
	if !ok {
		return nil, "", false
	}
	ids := make([]string, 0, len(page))
	for _, b := range page {
		ids = append(ids, b.ID)
	}
	return ids, next, true
}

func cbOrderKey(k string, descending bool) string {
	if descending {
		return awsDescKey(k)
	}
	return k
}

func handleCBCreateReportGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string         `json:"name"`
		Type         string         `json:"type"`
		ExportConfig map[string]any `json:"exportConfig"`
		Tags         []CBTag        `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	cbListSortedARNs(w, r, "reportGroups", func() []string {
		all := cbReportGrps.List()
		arns := make([]string, 0, len(all))
		for _, rg := range all {
			arns = append(arns, rg.Arn)
		}
		return arns
	})
}

func handleCBBatchGetReportGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportGroupArns []string `json:"reportGroupArns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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

func handleCBListReports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ReportGroupArn == "" {
		cbWriteError(w, "InvalidInputException", "reportGroupArn is required")
		return
	}
	cbWriteReportArnsPage(w, cbReports.List(), req.ReportGroupArn, req.SortOrder, req.MaxResults, req.NextToken)
}

// ListReports defaults to DESCENDING.
func cbWriteReportArnsPage(w http.ResponseWriter, all []CBReport, groupArn, sortOrder string, maxResults int, nextToken string) {
	var reports []CBReport
	for _, rep := range all {
		if groupArn == "" || rep.ReportGroupArn == groupArn {
			reports = append(reports, rep)
		}
	}
	descending := !strings.EqualFold(sortOrder, "ASCENDING")
	page, nextTok, pageOK := awsKeyPage(w, cbBadToken, reports, func(rep CBReport) string {
		return cbOrderKey(awsFloatKey(rep.Created)+"\x00"+rep.Arn, descending)
	}, nextToken, maxResults, 100)
	if !pageOK {
		return
	}
	arns := make([]string, 0, len(page))
	for _, rep := range page {
		arns = append(arns, rep.Arn)
	}
	resp := map[string]any{"reports": arns}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

func handleCBBatchGetReports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportArns []string `json:"reportArns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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

func handleCBImportSourceCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token           string `json:"token"`
		Username        string `json:"username"`
		ServerType      string `json:"serverType"`
		AuthType        string `json:"authType"`
		ShouldOverwrite *bool  `json:"shouldOverwrite"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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

	// AWS CodeBuild keys a source credential by server and auth type per account
	// and Region.
	arn := cbARN("token/" + strings.ToLower(req.ServerType))
	overwrite := req.ShouldOverwrite == nil || *req.ShouldOverwrite
	if _, ok := cbSourceCreds.Get(arn); ok && !overwrite {
		cbWriteError(w, "ResourceAlreadyExistsException", "Source credentials already exist for server type: "+req.ServerType)
		return
	}
	// For SECRETS_MANAGER the token is a secret ARN, echoed as resource.
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
	if err := kmsEnsureKeyMaterial(cbAWSOwnedKMSKeyID); err != nil {
		cbWriteError(w, "InternalFailure", "AWS owned KMS key material could not be generated")
		return
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
	if err := sim.ReadJSON(r, &req); err != nil {
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

func cbNameFromARN(arn string) string {
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

func cbListSortedARNs(w http.ResponseWriter, r *http.Request, field string, arns func() []string) {
	var req struct {
		SortOrder  string `json:"sortOrder"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	descending := strings.EqualFold(req.SortOrder, "DESCENDING")
	page, nextTok, pageOK := awsKeyPage(w, cbBadToken, arns(), func(s string) string { return cbOrderKey(s, descending) }, req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{field: page}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}
