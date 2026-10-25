package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// A batch build starts the builds its buildspec's batch section names
// (specs/cloud-api/aws/codebuild-batch-build-buildspec.md.gz) through
// StartBuild, StopBuild and RetryBuild, as the role its project's
// buildBatchConfig names (specs/cloud-api/aws/codebuild-batch-build.md.gz),
// and follows those builds to its own outcome.

// CBBuildBatch mirrors the CodeBuild BuildBatch shape.
type CBBuildBatch struct {
	ID                      string           `json:"id"`
	Arn                     string           `json:"arn"`
	ProjectName             string           `json:"projectName"`
	BuildBatchStatus        string           `json:"buildBatchStatus"`
	CurrentPhase            string           `json:"currentPhase,omitempty"`
	StartTime               float64          `json:"startTime"`
	EndTime                 float64          `json:"endTime"`
	SourceVersion           string           `json:"sourceVersion,omitempty"`
	Source                  map[string]any   `json:"source,omitempty"`
	SecondarySources        []map[string]any `json:"secondarySources,omitempty"`
	SecondarySourceVersions []map[string]any `json:"secondarySourceVersions,omitempty"`
	Artifacts               map[string]any   `json:"artifacts,omitempty"`
	SecondaryArtifacts      []map[string]any `json:"secondaryArtifacts,omitempty"`
	Cache                   map[string]any   `json:"cache,omitempty"`
	Environment             map[string]any   `json:"environment,omitempty"`
	ServiceRole             string           `json:"serviceRole,omitempty"`
	LogConfig               map[string]any   `json:"logConfig,omitempty"`
	BuildTimeoutInMinutes   int              `json:"buildTimeoutInMinutes,omitempty"`
	QueuedTimeoutInMinutes  int              `json:"queuedTimeoutInMinutes,omitempty"`
	Complete                bool             `json:"complete"`
	Initiator               string           `json:"initiator,omitempty"`
	EncryptionKey           string           `json:"encryptionKey,omitempty"`
	BuildBatchNumber        int64            `json:"buildBatchNumber,omitempty"`
	BuildBatchConfig        map[string]any   `json:"buildBatchConfig,omitempty"`
	BuildGroups             []CBBuildGroup   `json:"buildGroups,omitempty"`
	DebugSessionEnabled     *bool            `json:"debugSessionEnabled,omitempty"`
	Phases                  []CBPhase        `json:"phases,omitempty"`
	// Seq orders ListBuildBatches by start order; sim-internal, not wire.
	Seq int64 `json:"-"`
	// Request is the StartBuildBatch request; each build of the batch starts
	// with its overrides.
	Request cbStartBuildBatchInput `json:"-"`
	// Project is the project as the batch runs it, the request's overrides
	// applied.
	Project CBProject `json:"-"`
	// Tasks holds the batch section's build tasks, BuildGroups' order.
	Tasks    []cbBatchTask `json:"-"`
	FastFail bool          `json:"-"`
}

type CBBuildGroup struct {
	Identifier            string           `json:"identifier,omitempty"`
	DependsOn             []string         `json:"dependsOn,omitempty"`
	IgnoreFailure         bool             `json:"ignoreFailure"`
	CurrentBuildSummary   *CBBuildSummary  `json:"currentBuildSummary,omitempty"`
	PriorBuildSummaryList []CBBuildSummary `json:"priorBuildSummaryList,omitempty"`
}

type CBBuildSummary struct {
	Arn         string  `json:"arn"`
	RequestedOn float64 `json:"requestedOn"`
	BuildStatus string  `json:"buildStatus"`
}

type cbBatchTaskEnv struct {
	ComputeType    string            `yaml:"compute-type" json:"computeType,omitempty"`
	Image          string            `yaml:"image" json:"image,omitempty"`
	PrivilegedMode *bool             `yaml:"privileged-mode" json:"privilegedMode,omitempty"`
	Type           string            `yaml:"type" json:"type,omitempty"`
	Variables      map[string]string `yaml:"variables" json:"variables,omitempty"`
}

type cbBatchTask struct {
	Identifier    string         `yaml:"identifier" json:"identifier,omitempty"`
	Buildspec     string         `yaml:"buildspec" json:"buildspec,omitempty"`
	DebugSession  *bool          `yaml:"debug-session" json:"debugSession,omitempty"`
	DependOn      []string       `yaml:"depend-on" json:"dependOn,omitempty"`
	Env           cbBatchTaskEnv `yaml:"env" json:"env"`
	IgnoreFailure bool           `yaml:"ignore-failure" json:"ignoreFailure,omitempty"`
}

type cbBatchspec struct {
	Batch *struct {
		FastFail    bool          `yaml:"fast-fail"`
		BuildList   []cbBatchTask `yaml:"build-list"`
		BuildGraph  []cbBatchTask `yaml:"build-graph"`
		BuildMatrix *struct {
			Static struct {
				IgnoreFailure bool           `yaml:"ignore-failure"`
				Env           cbBatchTaskEnv `yaml:"env"`
			} `yaml:"static"`
			Dynamic struct {
				Buildspec []string `yaml:"buildspec"`
				Env       struct {
					ComputeType []string            `yaml:"compute-type"`
					Image       []string            `yaml:"image"`
					Variables   map[string][]string `yaml:"variables"`
				} `yaml:"env"`
			} `yaml:"dynamic"`
		} `yaml:"build-matrix"`
	} `yaml:"batch"`
}

// cbBatchMu serializes changes to build batches. A holder may call the
// CodeBuild API, whose handlers take cbMu, so cbMu holders never take it.
var cbBatchMu sync.Mutex

type cbProjectSettings struct {
	SecondarySources        []map[string]any `json:"secondarySources"`
	SecondarySourceVersions []map[string]any `json:"secondarySourceVersions"`
	SecondaryArtifacts      []map[string]any `json:"secondaryArtifacts"`
	Cache                   map[string]any   `json:"cache"`
	EncryptionKey           string           `json:"encryptionKey"`
	BuildBatchConfig        map[string]any   `json:"buildBatchConfig"`
	AutoRetryLimit          *int             `json:"autoRetryLimit"`
}

func (s cbProjectSettings) apply(p *CBProject) {
	if s.SecondarySources != nil {
		p.SecondarySources = s.SecondarySources
	}
	if s.SecondarySourceVersions != nil {
		p.SecondarySourceVersions = s.SecondarySourceVersions
	}
	if s.SecondaryArtifacts != nil {
		p.SecondaryArtifacts = s.SecondaryArtifacts
	}
	if s.Cache != nil {
		p.Cache = s.Cache
	}
	if s.EncryptionKey != "" {
		p.EncryptionKey = s.EncryptionKey
	}
	if s.BuildBatchConfig != nil {
		p.BuildBatchConfig = s.BuildBatchConfig
	}
	if s.AutoRetryLimit != nil {
		p.AutoRetryLimit = s.AutoRetryLimit
	}
}

// cbCanAssumeBatchRole writes CodeBuild's refusal of a buildBatchConfig whose
// service role it cannot assume.
func cbCanAssumeBatchRole(w http.ResponseWriter, config map[string]any, sourceArn string) bool {
	role := cbString(config["serviceRole"])
	return role == "" || cbCanAssumeRole(w, role, sourceArn)
}

// cbParseBatchspec reads a buildspec's batch section into the build tasks it
// defines.
func cbParseBatchspec(buildspec string) ([]cbBatchTask, bool, error) {
	var spec cbBatchspec
	if err := yaml.Unmarshal([]byte(buildspec), &spec); err != nil {
		return nil, false, fmt.Errorf("invalid buildspec: %w", err)
	}
	batch := spec.Batch
	if batch == nil {
		return nil, false, fmt.Errorf("the buildspec has no batch section")
	}
	defined := 0
	for _, present := range []bool{batch.BuildList != nil, batch.BuildGraph != nil, batch.BuildMatrix != nil} {
		if present {
			defined++
		}
	}
	if defined != 1 {
		return nil, false, fmt.Errorf("the batch section must define exactly one of build-list, build-graph and build-matrix")
	}
	switch {
	case batch.BuildList != nil:
		tasks := slices.Clone(batch.BuildList)
		for i := range tasks {
			tasks[i].DependOn = nil
		}
		return tasks, batch.FastFail, cbValidateBatchTasks(tasks)
	case batch.BuildGraph != nil:
		tasks := slices.Clone(batch.BuildGraph)
		return tasks, batch.FastFail, cbValidateBatchTasks(tasks)
	}
	return cbExpandBuildMatrix(batch.BuildMatrix.Static.IgnoreFailure, batch.BuildMatrix.Static.Env,
		batch.BuildMatrix.Dynamic.Buildspec, batch.BuildMatrix.Dynamic.Env.Image, batch.BuildMatrix.Dynamic.Env.ComputeType,
		batch.BuildMatrix.Dynamic.Env.Variables), batch.FastFail, nil
}

func cbValidateBatchTasks(tasks []cbBatchTask) error {
	if len(tasks) == 0 {
		return fmt.Errorf("the batch section defines no builds")
	}
	byIdentifier := map[string]cbBatchTask{}
	for _, task := range tasks {
		if task.Identifier == "" {
			return fmt.Errorf("every batch build task requires an identifier")
		}
		if _, duplicate := byIdentifier[task.Identifier]; duplicate {
			return fmt.Errorf("batch build identifier %s is defined more than once", task.Identifier)
		}
		byIdentifier[task.Identifier] = task
	}
	for _, task := range tasks {
		for _, dependency := range task.DependOn {
			if _, ok := byIdentifier[dependency]; !ok {
				return fmt.Errorf("batch build %s depends on %s, which the batch does not define", task.Identifier, dependency)
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(identifier string) error {
		switch state[identifier] {
		case 1:
			return fmt.Errorf("batch build %s depends on itself", identifier)
		case 2:
			return nil
		}
		state[identifier] = 1
		for _, dependency := range byIdentifier[identifier].DependOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[identifier] = 2
		return nil
	}
	for _, task := range tasks {
		if err := visit(task.Identifier); err != nil {
			return err
		}
	}
	return nil
}

// cbExpandBuildMatrix creates a build for each combination of the dynamic
// values, the static ones applying to every build.
func cbExpandBuildMatrix(ignoreFailure bool, static cbBatchTaskEnv, buildspecs, images, computeTypes []string, variables map[string][]string) []cbBatchTask {
	orEmpty := func(values []string) []string {
		if len(values) == 0 {
			return []string{""}
		}
		return values
	}
	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	sort.Strings(names)
	combinations := []map[string]string{{}}
	for _, name := range names {
		var next []map[string]string
		for _, combination := range combinations {
			for _, value := range orEmpty(variables[name]) {
				extended := make(map[string]string, len(combination)+1)
				for k, v := range combination {
					extended[k] = v
				}
				extended[name] = value
				next = append(next, extended)
			}
		}
		combinations = next
	}
	var tasks []cbBatchTask
	for _, buildspec := range orEmpty(buildspecs) {
		for _, image := range orEmpty(images) {
			for _, computeType := range orEmpty(computeTypes) {
				for _, combination := range combinations {
					env := cbBatchTaskEnv{Type: static.Type, PrivilegedMode: static.PrivilegedMode, Image: image, ComputeType: computeType}
					if len(combination) > 0 {
						env.Variables = combination
					}
					tasks = append(tasks, cbBatchTask{Buildspec: buildspec, Env: env, IgnoreFailure: ignoreFailure})
				}
			}
		}
	}
	return tasks
}

// cbCheckBatchRestrictions holds a batch's tasks to its buildBatchConfig's
// restrictions.
func cbCheckBatchRestrictions(batch CBBuildBatch, tasks []cbBatchTask) error {
	restrictions, _ := batch.BuildBatchConfig["restrictions"].(map[string]any)
	if maximum, ok := cbInt(restrictions["maximumBuildsAllowed"]); ok && len(tasks) > maximum {
		return fmt.Errorf("the batch defines %d builds, more than restrictions.maximumBuildsAllowed (%d) allows", len(tasks), maximum)
	}
	allowed, _ := restrictions["computeTypesAllowed"].([]any)
	if len(allowed) == 0 {
		return nil
	}
	for _, task := range tasks {
		computeType := firstNonEmpty(task.Env.ComputeType, cbString(batch.Project.Environment["computeType"]))
		if !slices.ContainsFunc(allowed, func(v any) bool { return cbString(v) == computeType }) {
			return fmt.Errorf("compute type %s is not in restrictions.computeTypesAllowed", computeType)
		}
	}
	return nil
}

func cbBatchIDFromArn(arn string) (string, bool) {
	return strings.CutPrefix(arn, cbARN("build-batch/"))
}

func cbBuildIDFromArn(arn string) (string, bool) {
	return strings.CutPrefix(arn, cbARN("build/"))
}

// cbNotifyBuildBatch tells the batch a build belongs to that the build
// settled.
func cbNotifyBuildBatch(build CBBuild) {
	if batchID, ok := cbBatchIDFromArn(build.BuildBatchArn); ok {
		go cbAdvanceBuildBatch(batchID)
	}
}

func cbBatchActor(batch CBBuildBatch) awsActor {
	return awsActor{Service: "codebuild.amazonaws.com", Source: batch.Arn, Role: cbString(batch.BuildBatchConfig["serviceRole"])}
}

// cbCallCodeBuild calls a CodeBuild operation as the batch's service role and
// decodes its answer into out.
func cbCallCodeBuild(batch CBBuildBatch, operation string, input, out any) error {
	status, body, err := awsCallJSON(context.Background(), cbBatchActor(batch), "codebuild",
		"CodeBuild_20161006."+operation, "application/x-amz-json-1.1", input)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		var refusal struct {
			Message string `json:"message"`
			Type    string `json:"__type"`
		}
		_ = json.Unmarshal(body, &refusal)
		return fmt.Errorf("%s answered %d: %s %s", operation, status, refusal.Type, refusal.Message)
	}
	return json.Unmarshal(body, out)
}

// cbBatchMemberInput is the StartBuild request for one task of the batch:
// the batch's overrides, then the task's own.
func cbBatchMemberInput(batch CBBuildBatch, task cbBatchTask) cbStartBuildInput {
	input := cbStartBuildInput{cbBuildOverrides: batch.Request.cbBuildOverrides}
	input.IdempotencyToken = ""
	input.ProjectName = batch.ProjectName
	input.TimeoutInMinutesOverride = batch.Request.BuildTimeoutInMinutesOverride
	if task.Buildspec != "" {
		input.BuildspecOverride = task.Buildspec
	}
	if task.Env.Image != "" {
		input.ImageOverride = task.Env.Image
	}
	if task.Env.ComputeType != "" {
		input.ComputeTypeOverride = task.Env.ComputeType
	}
	if task.Env.Type != "" {
		input.EnvironmentTypeOverride = task.Env.Type
	}
	if task.Env.PrivilegedMode != nil {
		input.PrivilegedModeOverride = task.Env.PrivilegedMode
	}
	if task.DebugSession != nil {
		input.DebugSessionEnabled = task.DebugSession
	}
	if len(task.Env.Variables) > 0 {
		names := make([]string, 0, len(task.Env.Variables))
		for name := range task.Env.Variables {
			names = append(names, name)
		}
		sort.Strings(names)
		overrides := make([]map[string]any, 0, len(names))
		for _, name := range names {
			overrides = append(overrides, map[string]any{"name": name, "value": task.Env.Variables[name], "type": "PLAINTEXT"})
		}
		merged := cbMergeEnvironmentVariables(cbAnyList(input.EnvironmentVariablesOverride), overrides)
		input.EnvironmentVariablesOverride = make([]map[string]any, 0, len(merged))
		for _, item := range merged {
			input.EnvironmentVariablesOverride = append(input.EnvironmentVariablesOverride, item.(map[string]any))
		}
	}
	return input
}

func cbAnyList(items []map[string]any) []any {
	list := make([]any, 0, len(items))
	for _, item := range items {
		list = append(list, item)
	}
	return list
}

// cbEndBatchPhase ends the batch's running phase with status and opens next,
// or ends the batch on status when next is empty.
func cbEndBatchPhase(batch *CBBuildBatch, status, next string, contexts ...CBPhaseContext) {
	now := cbEpochNow()
	if last := len(batch.Phases) - 1; last >= 0 && batch.Phases[last].PhaseStatus == "IN_PROGRESS" {
		batch.Phases[last].PhaseStatus = status
		batch.Phases[last].EndTime = now
		batch.Phases[last].DurationInSeconds = now - batch.Phases[last].StartTime
		batch.Phases[last].Contexts = append(batch.Phases[last].Contexts, contexts...)
	}
	if next != "" {
		batch.Phases = append(batch.Phases, CBPhase{PhaseType: next, PhaseStatus: "IN_PROGRESS", StartTime: now})
		batch.CurrentPhase = next
		return
	}
	// BuildBatchPhaseType has no COMPLETED: a batch ends on a phase named by
	// its outcome.
	batch.Phases = append(batch.Phases, CBPhase{PhaseType: status, PhaseStatus: status, StartTime: now, EndTime: now})
	batch.CurrentPhase = status
	batch.BuildBatchStatus = status
	batch.EndTime = now
	batch.Complete = true
}

// cbRefreshBuildGroups brings each group's current build summary up to date
// with its build, following the automatic retries CodeBuild made of it.
func cbRefreshBuildGroups(batch *CBBuildBatch) {
	for i := range batch.BuildGroups {
		group := &batch.BuildGroups[i]
		for group.CurrentBuildSummary != nil {
			id, _ := cbBuildIDFromArn(group.CurrentBuildSummary.Arn)
			build, ok := cbBuilds.Get(id)
			if !ok {
				break
			}
			group.CurrentBuildSummary.BuildStatus = build.BuildStatus
			if build.AutoRetryConfig == nil || build.AutoRetryConfig.NextAutoRetry == "" {
				break
			}
			nextID, _ := cbBuildIDFromArn(build.AutoRetryConfig.NextAutoRetry)
			next, ok := cbBuilds.Get(nextID)
			if !ok {
				break
			}
			group.PriorBuildSummaryList = append(group.PriorBuildSummaryList, *group.CurrentBuildSummary)
			group.CurrentBuildSummary = &CBBuildSummary{Arn: next.Arn, RequestedOn: next.StartTime, BuildStatus: next.BuildStatus}
		}
	}
}

func cbGroupSettled(group CBBuildGroup) bool {
	return group.CurrentBuildSummary != nil && group.CurrentBuildSummary.BuildStatus != "IN_PROGRESS"
}

// cbGroupFailed reports a settled group whose failure fails the batch.
func cbGroupFailed(group CBBuildGroup) bool {
	return cbGroupSettled(group) && group.CurrentBuildSummary.BuildStatus != "SUCCEEDED" && !group.IgnoreFailure
}

// cbAdvanceBuildBatch moves a running batch on: it stops its builds when one
// fails a fast-fail batch, starts each build whose dependencies completed, and
// settles the batch once none runs.
func cbAdvanceBuildBatch(batchID string) {
	cbBatchMu.Lock()
	defer cbBatchMu.Unlock()
	batch, ok := cbBuildBatches.Get(batchID)
	if !ok {
		return
	}
	cbRefreshBuildGroups(&batch)
	if batch.BuildBatchStatus != "IN_PROGRESS" || batch.CurrentPhase != "IN_PROGRESS" {
		cbBuildBatches.Put(batchID, batch)
		return
	}
	failed := slices.ContainsFunc(batch.BuildGroups, cbGroupFailed)
	var contexts []CBPhaseContext
	if !failed || !batch.FastFail {
		byIdentifier := map[string]CBBuildGroup{}
		for _, group := range batch.BuildGroups {
			if group.Identifier != "" {
				byIdentifier[group.Identifier] = group
			}
		}
		for i := range batch.BuildGroups {
			group := &batch.BuildGroups[i]
			if group.CurrentBuildSummary != nil || !cbDependenciesCompleted(*group, byIdentifier) {
				continue
			}
			summary, err := cbStartBatchBuild(batch, *group, batch.Tasks[i])
			if err != nil {
				// A build the batch cannot start fails the batch, as a
				// failed build of a fast-fail batch does.
				contexts = append(contexts, CBPhaseContext{StatusCode: "CLIENT_ERROR", Message: err.Error()})
				failed = true
				break
			}
			group.CurrentBuildSummary = &summary
		}
	}
	if failed && (batch.FastFail || len(contexts) > 0) {
		contexts = append(contexts, cbStopRunningBatchBuilds(&batch)...)
	}
	running := slices.ContainsFunc(batch.BuildGroups, func(group CBBuildGroup) bool {
		return group.CurrentBuildSummary != nil && group.CurrentBuildSummary.BuildStatus == "IN_PROGRESS"
	})
	switch {
	case running:
		if len(contexts) > 0 {
			last := len(batch.Phases) - 1
			batch.Phases[last].Contexts = append(batch.Phases[last].Contexts, contexts...)
		}
	case failed || slices.ContainsFunc(batch.BuildGroups, func(group CBBuildGroup) bool { return group.CurrentBuildSummary == nil }):
		// A group whose dependency failed never starts, and fails the batch.
		cbEndBatchPhase(&batch, "FAILED", "", contexts...)
	default:
		cbEndBatchPhase(&batch, "SUCCEEDED", "", contexts...)
	}
	cbBuildBatches.Put(batchID, batch)
}

// cbStopRunningBatchBuilds stops, as the batch's service role, each of its
// builds still running, and returns why any could not be stopped.
func cbStopRunningBatchBuilds(batch *CBBuildBatch) []CBPhaseContext {
	var contexts []CBPhaseContext
	for _, group := range batch.BuildGroups {
		if group.CurrentBuildSummary == nil || group.CurrentBuildSummary.BuildStatus != "IN_PROGRESS" {
			continue
		}
		id, _ := cbBuildIDFromArn(group.CurrentBuildSummary.Arn)
		var stopped struct {
			Build CBBuild `json:"build"`
		}
		if err := cbCallCodeBuild(*batch, "StopBuild", map[string]any{"id": id}, &stopped); err != nil {
			contexts = append(contexts, CBPhaseContext{StatusCode: "CLIENT_ERROR", Message: err.Error()})
		}
	}
	cbRefreshBuildGroups(batch)
	return contexts
}

// cbDependenciesCompleted reports whether every build a group depends on
// completed in a way that lets it run: succeeded, or failed with its failure
// ignored.
func cbDependenciesCompleted(group CBBuildGroup, byIdentifier map[string]CBBuildGroup) bool {
	for _, dependency := range group.DependsOn {
		other := byIdentifier[dependency]
		if !cbGroupSettled(other) || (other.CurrentBuildSummary.BuildStatus != "SUCCEEDED" && !other.IgnoreFailure) {
			return false
		}
	}
	return true
}

// cbStartBatchBuild starts a group's build: StartBuild for its first, and
// RetryBuild of its last build when the batch is retried.
func cbStartBatchBuild(batch CBBuildBatch, group CBBuildGroup, task cbBatchTask) (CBBuildSummary, error) {
	var started struct {
		Build CBBuild `json:"build"`
	}
	var err error
	if prior := len(group.PriorBuildSummaryList); prior > 0 {
		id, _ := cbBuildIDFromArn(group.PriorBuildSummaryList[prior-1].Arn)
		err = cbCallCodeBuild(batch, "RetryBuild", map[string]any{"id": id}, &started)
	} else {
		err = cbCallCodeBuild(batch, "StartBuild", cbBatchMemberInput(batch, task), &started)
	}
	if err != nil {
		return CBBuildSummary{}, fmt.Errorf("start batch build %s: %w", firstNonEmpty(task.Identifier, "of the build matrix"), err)
	}
	return CBBuildSummary{Arn: started.Build.Arn, RequestedOn: started.Build.StartTime, BuildStatus: started.Build.BuildStatus}, nil
}

// cbDownloadBatchspec reads the batch's buildspec, inline or from its source,
// and lays out the batch's build groups from its batch section.
func cbDownloadBatchspec(batchID string) {
	batch, ok := cbBuildBatches.Get(batchID)
	if !ok {
		return
	}
	tasks, fastFail, err := cbReadBatchspec(batch)
	if err == nil {
		err = cbCheckBatchRestrictions(batch, tasks)
	}
	cbBatchMu.Lock()
	batch, ok = cbBuildBatches.Get(batchID)
	if !ok || batch.BuildBatchStatus != "IN_PROGRESS" {
		cbBatchMu.Unlock()
		return
	}
	if err != nil {
		cbEndBatchPhase(&batch, "FAILED", "", CBPhaseContext{StatusCode: "CLIENT_ERROR", Message: err.Error()})
		cbBuildBatches.Put(batchID, batch)
		cbBatchMu.Unlock()
		return
	}
	batch.Tasks = tasks
	batch.FastFail = fastFail
	batch.BuildGroups = make([]CBBuildGroup, 0, len(tasks))
	for _, task := range tasks {
		batch.BuildGroups = append(batch.BuildGroups, CBBuildGroup{
			Identifier: task.Identifier, DependsOn: task.DependOn, IgnoreFailure: task.IgnoreFailure,
		})
	}
	cbEndBatchPhase(&batch, "SUCCEEDED", "IN_PROGRESS")
	cbBuildBatches.Put(batchID, batch)
	cbBatchMu.Unlock()
	cbAdvanceBuildBatch(batchID)
}

func cbReadBatchspec(batch CBBuildBatch) ([]cbBatchTask, bool, error) {
	project := batch.Project
	buildspec := cbString(project.Source["buildspec"])
	inline, err := cbInlineBuildspec(buildspec)
	if err != nil {
		return nil, false, err
	}
	if !inline {
		if strings.EqualFold(cbString(project.Source["type"]), "NO_SOURCE") {
			return nil, false, fmt.Errorf("a NO_SOURCE batch build requires an inline buildspec")
		}
		path := firstNonEmpty(buildspec, "buildspec.yml")
		if !filepath.IsLocal(filepath.FromSlash(path)) {
			return nil, false, fmt.Errorf("buildspec %s is not a path within the source directory", path)
		}
		workDir, err := os.MkdirTemp("", "sockerless-codebuild-batch-*")
		if err != nil {
			return nil, false, err
		}
		defer os.RemoveAll(workDir)
		if err := cbCheckoutSource(project, batch.SourceVersion, workDir); err != nil {
			return nil, false, err
		}
		content, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(path)))
		if err != nil {
			return nil, false, fmt.Errorf("read source %s: %v", path, err)
		}
		buildspec = string(content)
	}
	return cbParseBatchspec(buildspec)
}

// cbApplyBatchOverrides is cbApplyOverrides plus the members only
// StartBuildBatch carries.
func cbApplyBatchOverrides(project CBProject, input cbStartBuildBatchInput) (CBProject, error) {
	if err := cbValidateRange("buildTimeoutInMinutesOverride", input.BuildTimeoutInMinutesOverride, 5, 2160); err != nil {
		return CBProject{}, err
	}
	effective, err := cbApplyOverrides(project, input.cbBuildOverrides)
	if err != nil {
		return CBProject{}, err
	}
	if input.ReportBuildBatchStatusOverride != nil {
		if *input.ReportBuildBatchStatusOverride && !cbSourceTypeIn(effective, cbReportBatchStatusSourceTypes) {
			return CBProject{}, fmt.Errorf("reportBuildBatchStatusOverride is valid only for %s sources", strings.Join(cbReportBatchStatusSourceTypes, ", "))
		}
		effective.Source["reportBuildStatus"] = *input.ReportBuildBatchStatusOverride
	}
	if input.BuildTimeoutInMinutesOverride != nil {
		effective.BuildTimeout = *input.BuildTimeoutInMinutesOverride
	}
	if input.BuildBatchConfigOverride != nil {
		effective.BuildBatchConfig = input.BuildBatchConfigOverride
	}
	return effective, nil
}

func handleCBStartBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req cbStartBuildBatchInput
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
	effective, err := cbApplyBatchOverrides(p, req)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if cbString(effective.BuildBatchConfig["serviceRole"]) == "" {
		cbWriteError(w, "InvalidInputException", "project "+p.Name+" has no buildBatchConfig.serviceRole for CodeBuild to start the batch's builds as")
		return
	}
	batchID := p.Name + ":" + uuid.New().String()
	batchArn := cbARN("build-batch/" + batchID)
	if req.BuildBatchConfigOverride != nil && !cbCanAssumeBatchRole(w, effective.BuildBatchConfig, batchArn) {
		return
	}
	token := req.IdempotencyToken
	keyed := req
	keyed.IdempotencyToken = ""
	digest := cbRequestDigest(keyed)

	cbBatchMu.Lock()
	defer cbBatchMu.Unlock()

	id, repeated, err := cbIdempotentID("StartBuildBatch", token, digest)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if batch, found := cbBuildBatches.Get(id); repeated && found {
		cbWriteJSON(w, http.StatusOK, map[string]any{"buildBatch": batch})
		return
	}
	now := cbEpochNow()
	batch := CBBuildBatch{
		ID:                      batchID,
		Arn:                     batchArn,
		ProjectName:             p.Name,
		BuildBatchStatus:        "IN_PROGRESS",
		CurrentPhase:            "DOWNLOAD_BATCHSPEC",
		StartTime:               now,
		EndTime:                 now,
		SourceVersion:           req.SourceVersion,
		Source:                  effective.Source,
		SecondarySources:        effective.SecondarySources,
		SecondarySourceVersions: effective.SecondarySourceVersions,
		Artifacts:               cbBuildArtifacts(effective.Artifacts),
		SecondaryArtifacts:      cbBuildArtifactsList(effective.SecondaryArtifacts),
		Cache:                   effective.Cache,
		Environment:             effective.Environment,
		ServiceRole:             effective.ServiceRole,
		LogConfig:               effective.LogsConfig,
		BuildTimeoutInMinutes:   int(cbBuildTimeout(effective).Minutes()),
		QueuedTimeoutInMinutes:  effective.QueueTimeout,
		EncryptionKey:           effective.EncryptionKey,
		BuildBatchNumber:        1,
		BuildBatchConfig:        effective.BuildBatchConfig,
		DebugSessionEnabled:     req.DebugSessionEnabled,
		Seq:                     cbNextSeq(&cbBatchSeq),
		Request:                 req,
		Project:                 effective,
		Phases: []CBPhase{
			{PhaseType: "SUBMITTED", PhaseStatus: "SUCCEEDED", StartTime: now, EndTime: now},
			{PhaseType: "DOWNLOAD_BATCHSPEC", PhaseStatus: "IN_PROGRESS", StartTime: now},
		},
	}
	cbBuildBatches.Put(batchID, batch)
	cbRecordIdempotencyToken("StartBuildBatch", token, digest, batchID)
	go cbDownloadBatchspec(batchID)
	cbWriteJSON(w, http.StatusOK, map[string]any{"buildBatch": batch})
}

func handleCBStopBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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

// cbStopBuildBatchByID stops a running batch and, as its service role, each of
// its builds still running.
func cbStopBuildBatchByID(batchID string) CBBuildBatch {
	cbBatchMu.Lock()
	defer cbBatchMu.Unlock()
	batch, ok := cbResolveBuildBatch(batchID)
	if !ok || batch.BuildBatchStatus != "IN_PROGRESS" {
		return batch
	}
	cbRefreshBuildGroups(&batch)
	contexts := cbStopRunningBatchBuilds(&batch)
	cbEndBatchPhase(&batch, "STOPPED", "", contexts...)
	cbBuildBatches.Put(batch.ID, batch)
	return batch
}

func handleCBRetryBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID               string `json:"id"`
		IdempotencyToken string `json:"idempotencyToken"`
		RetryType        string `json:"retryType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	retryType := firstNonEmpty(req.RetryType, "RETRY_ALL_BUILDS")
	if retryType != "RETRY_ALL_BUILDS" && retryType != "RETRY_FAILED_BUILDS" {
		cbWriteError(w, "InvalidInputException", "retryType must be RETRY_ALL_BUILDS or RETRY_FAILED_BUILDS")
		return
	}
	digest := cbRequestDigest([]string{req.ID, retryType})

	cbBatchMu.Lock()
	defer cbBatchMu.Unlock()

	batch, ok := cbResolveBuildBatch(req.ID)
	if !ok {
		cbWriteError(w, "ResourceNotFoundException", "Build batch not found: "+req.ID)
		return
	}
	id, repeated, err := cbIdempotentID("RetryBuildBatch", req.IdempotencyToken, digest)
	if err != nil {
		cbWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if repeated && id == batch.ID {
		cbWriteJSON(w, http.StatusOK, map[string]any{"buildBatch": batch})
		return
	}
	// "Only batch builds that have failed can be retried."
	if batch.BuildBatchStatus == "IN_PROGRESS" || batch.BuildBatchStatus == "SUCCEEDED" {
		cbWriteError(w, "InvalidInputException", "build batch "+batch.ID+" is "+batch.BuildBatchStatus+"; only a batch build that failed can be retried")
		return
	}
	if len(batch.Tasks) == 0 {
		cbWriteError(w, "InvalidInputException", "build batch "+batch.ID+" started no builds to retry")
		return
	}
	cbRefreshBuildGroups(&batch)
	for i := range batch.BuildGroups {
		group := &batch.BuildGroups[i]
		if group.CurrentBuildSummary == nil {
			continue
		}
		if retryType == "RETRY_FAILED_BUILDS" && group.CurrentBuildSummary.BuildStatus == "SUCCEEDED" {
			continue
		}
		group.PriorBuildSummaryList = append(group.PriorBuildSummaryList, *group.CurrentBuildSummary)
		group.CurrentBuildSummary = nil
	}
	now := cbEpochNow()
	batch.BuildBatchStatus = "IN_PROGRESS"
	batch.Complete = false
	batch.EndTime = now
	batch.CurrentPhase = "IN_PROGRESS"
	batch.Phases = append(batch.Phases, CBPhase{PhaseType: "IN_PROGRESS", PhaseStatus: "IN_PROGRESS", StartTime: now})
	cbBuildBatches.Put(batch.ID, batch)
	cbRecordIdempotencyToken("RetryBuildBatch", req.IdempotencyToken, digest, batch.ID)
	go cbAdvanceBuildBatch(batch.ID)
	cbWriteJSON(w, http.StatusOK, map[string]any{"buildBatch": batch})
}

func handleCBDeleteBuildBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		cbWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	cbBatchMu.Lock()
	defer cbBatchMu.Unlock()

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
	deleted := []string{}
	cbMu.Lock()
	for _, group := range batch.BuildGroups {
		summaries := slices.Clone(group.PriorBuildSummaryList)
		if group.CurrentBuildSummary != nil {
			summaries = append(summaries, *group.CurrentBuildSummary)
		}
		for _, summary := range summaries {
			id, _ := cbBuildIDFromArn(summary.Arn)
			if _, ok := cbBuilds.Get(id); ok {
				cbBuilds.Delete(id)
				deleted = append(deleted, id)
			}
		}
	}
	cbMu.Unlock()
	cbBuildBatches.Delete(batch.ID)
	cbWriteJSON(w, http.StatusOK, map[string]any{
		"statusCode":       "DELETED",
		"buildsDeleted":    deleted,
		"buildsNotDeleted": []map[string]any{},
	})
}

func handleCBBatchGetBuildBatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	descending := !strings.EqualFold(sortOrder, "ASCENDING")
	page, nextTok, pageOK := awsKeyPage(w, cbBadToken, batches, func(b CBBuildBatch) string {
		return cbOrderKey(listq.IntKey(b.Seq), descending)
	}, nextToken, maxResults, 100)
	if !pageOK {
		return
	}
	ids := make([]string, 0, len(page))
	for _, b := range page {
		ids = append(ids, b.ID)
	}
	resp := map[string]any{"ids": ids}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	cbWriteJSON(w, http.StatusOK, resp)
}

// cbResolveBuildBatch finds a batch by id or ARN.
func cbResolveBuildBatch(idOrARN string) (CBBuildBatch, bool) {
	if id, ok := cbBatchIDFromArn(idOrARN); ok {
		idOrARN = id
	}
	return cbBuildBatches.Get(idOrARN)
}

// cbRecoverBuildBatches resumes the batches a restart interrupted: one still
// reading its buildspec reads it again, and one running its builds moves on
// from where its builds are.
func cbRecoverBuildBatches() error {
	for _, batch := range cbBuildBatches.List() {
		if batch.BuildBatchStatus != "IN_PROGRESS" {
			continue
		}
		if batch.Project.Name == "" {
			return fmt.Errorf("build batch %s has no persisted batch settings", batch.ID)
		}
		if batch.CurrentPhase == "DOWNLOAD_BATCHSPEC" {
			go cbDownloadBatchspec(batch.ID)
			continue
		}
		go cbAdvanceBuildBatch(batch.ID)
	}
	return nil
}
