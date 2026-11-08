package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/google/uuid"
)

// AWS Systems Manager Automation runs an Automation runbook (an SSM document of
// schema version 0.3) as an execution of StepExecutions. The runner in
// ssm_automation_runner.go runs each step through the service API its action
// names (ssm-automation-actions.md.gz) and records each status as it settles
// (ssm-automation-statuses.md.gz). Every piece of runner state lives on the
// execution record, so StopAutomationExecution and SendAutomationSignal reach a
// running execution through the store, and a restarted simulator resumes it.

type SSMAutomationStep struct {
	StepName       string              `json:"StepName"`
	Action         string              `json:"Action"`
	StepStatus     string              `json:"StepStatus"`
	StepExecID     string              `json:"StepExecutionId"`
	ExecStartTime  float64             `json:"ExecutionStartTime,omitempty"`
	ExecEndTime    float64             `json:"ExecutionEndTime,omitempty"`
	ResponseCode   string              `json:"ResponseCode,omitempty"`
	Inputs         map[string]string   `json:"Inputs,omitempty"`
	Outputs        map[string][]string `json:"Outputs,omitempty"`
	Values         map[string]any      `json:"Values,omitempty"`
	FailureMessage string              `json:"FailureMessage,omitempty"`
	FailureStage   string              `json:"FailureStage,omitempty"`
	FailureType    string              `json:"FailureType,omitempty"`
	TimeoutSeconds int64               `json:"TimeoutSeconds,omitempty"`
	MaxAttempts    int                 `json:"MaxAttempts,omitempty"`
	OnFailure      string              `json:"OnFailure,omitempty"`
	OnCancel       string              `json:"OnCancel,omitempty"`
	NextStep       string              `json:"NextStep,omitempty"`
	IsEnd          bool                `json:"IsEnd"`
	IsCritical     bool                `json:"IsCritical"`
	ValidNextSteps []string            `json:"ValidNextSteps,omitempty"`
}

// ssmAutomationIdentity is who a step's service calls run as: the runbook's
// assume role, else the credential of the caller that started the execution,
// else the service principal that started it in-process.
type ssmAutomationIdentity struct {
	AccessKeyID  string `json:"AccessKeyId,omitempty"`
	SessionToken string `json:"SessionToken,omitempty"`
	Service      string `json:"Service,omitempty"`
	Source       string `json:"Source,omitempty"`
	Role         string `json:"Role,omitempty"`
}

type ssmAutomationSignal struct {
	Type      string              `json:"Type"`
	Payload   map[string][]string `json:"Payload,omitempty"`
	CallerArn string              `json:"CallerArn,omitempty"`
}

type SSMAutomationExecution struct {
	AutomationExecutionId string                `json:"AutomationExecutionId"`
	DocumentName          string                `json:"DocumentName"`
	DocumentVersion       string                `json:"DocumentVersion,omitempty"`
	Content               string                `json:"Content"`
	Format                string                `json:"Format"`
	Status                string                `json:"AutomationExecutionStatus"`
	Mode                  string                `json:"Mode"`
	Parameters            map[string][]string   `json:"Parameters,omitempty"`
	Values                map[string]any        `json:"Values,omitempty"`
	Outputs               map[string][]string   `json:"Outputs,omitempty"`
	StartTime             float64               `json:"ExecutionStartTime"`
	EndTime               float64               `json:"ExecutionEndTime,omitempty"`
	ExecutedBy            string                `json:"ExecutedBy"`
	Steps                 []SSMAutomationStep   `json:"StepExecutions"`
	FailureMessage        string                `json:"FailureMessage,omitempty"`
	CurrentStepName       string                `json:"CurrentStepName,omitempty"`
	CurrentAction         string                `json:"CurrentAction,omitempty"`
	Cursor                string                `json:"Cursor,omitempty"`
	Identity              ssmAutomationIdentity `json:"Identity"`
	StopRequested         string                `json:"StopRequested,omitempty"`
	StopStepID            string                `json:"StopStepId,omitempty"`
	Signals               []ssmAutomationSignal `json:"Signals,omitempty"`
	ParentId              string                `json:"ParentAutomationExecutionId,omitempty"`
	TargetParameterName   string                `json:"TargetParameterName,omitempty"`
	Targets               []SSMTarget           `json:"Targets,omitempty"`
	TargetMaps            []map[string][]string `json:"TargetMaps,omitempty"`
	ResolvedTargets       []string              `json:"ResolvedTargets,omitempty"`
	MaxConcurrency        string                `json:"MaxConcurrency,omitempty"`
	MaxErrors             string                `json:"MaxErrors,omitempty"`
	Target                string                `json:"Target,omitempty"`
	ChangeRequestName     string                `json:"ChangeRequestName,omitempty"`
	Subtype               string                `json:"AutomationSubtype,omitempty"`
	ScheduledTime         float64               `json:"ScheduledTime,omitempty"`
	Runbooks              []ssmChangeRunbook    `json:"Runbooks,omitempty"`
}

type ssmChangeRunbook struct {
	DocumentName    string              `json:"DocumentName"`
	DocumentVersion string              `json:"DocumentVersion,omitempty"`
	Parameters      map[string][]string `json:"Parameters,omitempty"`
}

func ssmAutomationARN(id string) string {
	return "arn:aws:ssm:" + awsRegion() + ":" + awsAccountID() + ":automation-execution/" + id
}

func ssmAutomationTerminal(status string) bool {
	switch status {
	case "Success", "Failed", "TimedOut", "Cancelled", "CompletedWithSuccess", "CompletedWithFailure", "Rejected", "ChangeCalendarOverrideRejected", "Exited":
		return true
	}
	return false
}

func ssmAutomationNotFound(w http.ResponseWriter, id string) {
	AWSErrorf(w, "AutomationExecutionNotFoundException", http.StatusBadRequest,
		"There is no automation execution information for the requested automation execution ID %s.", id)
}

// ssmAutomationCallerIdentity is the identity StartAutomationExecution was
// called with: a SigV4 credential, or an AWS service calling in-process as its
// own principal.
func ssmAutomationCallerIdentity(r *http.Request) ssmAutomationIdentity {
	if akid := iamAccessKeyIDFromRequest(r); akid != "" {
		return ssmAutomationIdentity{AccessKeyID: akid, SessionToken: sigv4PresentedSessionToken(r)}
	}
	if caller, ok := sim.CallerOf(r.Context()); ok && sim.InProcess(r) {
		return ssmAutomationIdentity{Service: caller.Service, Source: caller.Source}
	}
	return ssmAutomationIdentity{}
}

// ssmAutomationDefinition reads the runbook a start names. A document that is no
// Automation runbook is not an Automation definition.
func ssmAutomationDefinition(w http.ResponseWriter, name, version string, documentType string) (SSMDocumentVersion, bool) {
	doc, ok := ssmDocuments.Get(name)
	if !ok || doc.DocumentType != documentType {
		AWSErrorf(w, "AutomationDefinitionNotFoundException", http.StatusBadRequest,
			"An Automation runbook with the specified name couldn't be found.")
		return SSMDocumentVersion{}, false
	}
	v, ok := ssmDocVersion(doc, version)
	if !ok {
		AWSErrorf(w, "AutomationDefinitionVersionNotFoundException", http.StatusBadRequest,
			"An Automation runbook with the specified name and version couldn't be found.")
		return SSMDocumentVersion{}, false
	}
	return v, true
}

type ssmStartAutomationRequest struct {
	DocumentName        string                `json:"DocumentName"`
	DocumentVersion     string                `json:"DocumentVersion"`
	Parameters          map[string][]string   `json:"Parameters"`
	Mode                string                `json:"Mode"`
	TargetParameterName string                `json:"TargetParameterName"`
	Targets             []SSMTarget           `json:"Targets"`
	TargetMaps          []map[string][]string `json:"TargetMaps"`
	MaxConcurrency      string                `json:"MaxConcurrency"`
	MaxErrors           string                `json:"MaxErrors"`
	TargetLocations     []map[string]any      `json:"TargetLocations"`
	TargetLocationsURL  string                `json:"TargetLocationsURL"`
	Tags                []SSMTag              `json:"Tags"`
}

func handleSSMStartAutomationExecution(w http.ResponseWriter, r *http.Request) {
	var req ssmStartAutomationRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.TargetLocations) > 0 || req.TargetLocationsURL != "" {
		AWSErrorf(w, "InvalidAutomationExecutionParametersException", http.StatusBadRequest,
			"TargetLocations run an automation in other accounts and Regions, which this simulator does not serve.")
		return
	}
	version, ok := ssmAutomationDefinition(w, req.DocumentName, req.DocumentVersion, "Automation")
	if !ok {
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "Auto"
	}
	exec := SSMAutomationExecution{
		AutomationExecutionId: uuid.New().String(),
		DocumentName:          req.DocumentName,
		DocumentVersion:       version.DocumentVersion,
		Content:               version.Content,
		Format:                version.DocumentFormat,
		Mode:                  mode,
		ExecutedBy:            awsCallerArn(r),
		Identity:              ssmAutomationCallerIdentity(r),
		TargetParameterName:   req.TargetParameterName,
		Targets:               req.Targets,
		TargetMaps:            req.TargetMaps,
		MaxConcurrency:        req.MaxConcurrency,
		MaxErrors:             req.MaxErrors,
	}
	if failure := ssmPrepareAutomation(r.Context(), &exec, req.Parameters); failure != "" {
		AWSError(w, "InvalidAutomationExecutionParametersException", failure, http.StatusBadRequest)
		return
	}
	if len(req.Targets) > 0 || len(req.TargetMaps) > 0 {
		if failure := ssmResolveAutomationTargets(&exec); failure != "" {
			AWSError(w, "InvalidAutomationExecutionParametersException", failure, http.StatusBadRequest)
			return
		}
	}
	ssmAutomations.Put(exec.AutomationExecutionId, exec)
	ssmTagOnCreate("Automation", exec.AutomationExecutionId, req.Tags)
	ssmStartAutomationRunner(exec.AutomationExecutionId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AutomationExecutionId": exec.AutomationExecutionId})
}

// ssmResolveAutomationTargets turns Targets or TargetMaps into the values of the
// child automations a rate-controlled execution runs (ssm-running-automations-map-targets.md.gz).
func ssmResolveAutomationTargets(exec *SSMAutomationExecution) string {
	if len(exec.TargetMaps) > 0 {
		if len(exec.Targets) > 0 {
			return "Targets and TargetMaps can't be specified together."
		}
		return ""
	}
	if exec.TargetParameterName == "" {
		return "TargetParameterName is required when Targets are specified."
	}
	runbook, err := ssmParseRunbook(exec.Content, exec.Format)
	if err != nil {
		return err.Error()
	}
	if _, defined := runbook.Parameters[exec.TargetParameterName]; !defined {
		return "TargetParameterName " + exec.TargetParameterName + " isn't a parameter of the runbook."
	}
	for _, t := range exec.Targets {
		if t.Key != "ParameterValues" {
			return "Target key " + t.Key + " isn't supported; target an automation by ParameterValues."
		}
		exec.ResolvedTargets = append(exec.ResolvedTargets, t.Values...)
	}
	if len(exec.ResolvedTargets) == 0 {
		return "The Targets resolve to no resource."
	}
	return ""
}

func handleSSMStartChangeRequestExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DocumentName      string              `json:"DocumentName"`
		DocumentVersion   string              `json:"DocumentVersion"`
		Parameters        map[string][]string `json:"Parameters"`
		ChangeRequestName string              `json:"ChangeRequestName"`
		Runbooks          []struct {
			DocumentName    string              `json:"DocumentName"`
			DocumentVersion string              `json:"DocumentVersion"`
			Parameters      map[string][]string `json:"Parameters"`
		} `json:"Runbooks"`
		ScheduledTime *float64 `json:"ScheduledTime"`
		Tags          []SSMTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	version, ok := ssmAutomationDefinition(w, req.DocumentName, req.DocumentVersion, "Automation.ChangeTemplate")
	if !ok {
		return
	}
	exec := SSMAutomationExecution{
		AutomationExecutionId: uuid.New().String(),
		DocumentName:          req.DocumentName,
		DocumentVersion:       version.DocumentVersion,
		Content:               version.Content,
		Format:                version.DocumentFormat,
		Mode:                  "Auto",
		ExecutedBy:            awsCallerArn(r),
		Identity:              ssmAutomationCallerIdentity(r),
		ChangeRequestName:     req.ChangeRequestName,
		Subtype:               "ChangeRequest",
	}
	for _, rb := range req.Runbooks {
		runbook, ok := ssmAutomationDefinition(w, rb.DocumentName, rb.DocumentVersion, "Automation")
		if !ok {
			return
		}
		exec.Runbooks = append(exec.Runbooks, ssmChangeRunbook{DocumentName: rb.DocumentName, DocumentVersion: runbook.DocumentVersion, Parameters: rb.Parameters})
	}
	if req.ScheduledTime != nil {
		exec.ScheduledTime = *req.ScheduledTime
	}
	if failure := ssmPrepareAutomation(r.Context(), &exec, req.Parameters); failure != "" {
		AWSError(w, "InvalidAutomationExecutionParametersException", failure, http.StatusBadRequest)
		return
	}
	ssmAutomations.Put(exec.AutomationExecutionId, exec)
	ssmTagOnCreate("Automation", exec.AutomationExecutionId, req.Tags)
	ssmStartAutomationRunner(exec.AutomationExecutionId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AutomationExecutionId": exec.AutomationExecutionId})
}

// ssmPrepareAutomation resolves the runbook's parameters against the request
// and lays out every step as Pending, or returns why the parameters are not
// valid.
func ssmPrepareAutomation(ctx context.Context, exec *SSMAutomationExecution, provided map[string][]string) string {
	runbook, err := ssmParseRunbook(exec.Content, exec.Format)
	if err != nil {
		return err.Error()
	}
	exec.Status = "Pending"
	if exec.ScheduledTime > 0 {
		exec.Status = "Scheduled"
	}
	exec.StartTime = ssmNowSeconds()
	exec.Outputs = map[string][]string{}
	if len(exec.Targets) > 0 || len(exec.TargetMaps) > 0 {
		if exec.TargetParameterName != "" {
			provided = ssmWithParameter(provided, exec.TargetParameterName, nil)
		}
		exec.Parameters = provided
		return ""
	}
	values, failure := ssmResolveRunbookParameters(ctx, runbook, provided, exec.Identity)
	if failure != "" {
		return failure
	}
	exec.Values = values
	if exec.Identity.Role == "" && runbook.AssumeRole != "" {
		role, err := (ssmInterpolator{exec: *exec}).resolve(runbook.AssumeRole)
		if err != nil {
			return "The runbook's assumeRole can't be resolved: " + err.Error()
		}
		exec.Identity.Role = ssmValueString(role)
	}
	exec.Parameters = map[string][]string{}
	for name, value := range values {
		exec.Parameters[name] = ssmValueStrings(value)
	}
	exec.Steps = make([]SSMAutomationStep, 0, len(runbook.MainSteps))
	for _, step := range runbook.MainSteps {
		exec.Steps = append(exec.Steps, ssmNewStepExecution(step))
	}
	if len(runbook.MainSteps) > 0 {
		exec.Cursor = runbook.MainSteps[0].Name
	}
	return ""
}

func ssmWithParameter(params map[string][]string, name string, value []string) map[string][]string {
	out := make(map[string][]string, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	if value == nil {
		delete(out, name)
	} else {
		out[name] = value
	}
	return out
}

func ssmNewStepExecution(step ssmRunbookStep) SSMAutomationStep {
	s := SSMAutomationStep{
		StepName:       step.Name,
		Action:         step.Action,
		StepStatus:     "Pending",
		StepExecID:     uuid.New().String(),
		TimeoutSeconds: step.TimeoutSeconds,
		MaxAttempts:    step.MaxAttempts,
		OnFailure:      step.OnFailure,
		OnCancel:       step.OnCancel,
		NextStep:       step.NextStep,
		IsEnd:          step.IsEnd,
		IsCritical:     step.critical(),
	}
	if step.Action == "aws:branch" {
		s.ValidNextSteps = step.branchTargets()
	}
	return s
}

func ssmStartAutomationRunner(id string) {
	var workerCtx context.Context
	run, ok := bg.Handoff(func() { ssmRunAutomation(workerCtx, id) })
	if !ok {
		return
	}
	sfnAWSServer.StartBackground("AWS Systems Manager Automation execution", func(ctx context.Context) {
		workerCtx = ctx
		run()
	})
}

// recoverSSMAutomationExecutions resumes the executions an earlier process left
// running, from the step each was on.
func recoverSSMAutomationExecutions() {
	for _, exec := range ssmAutomations.List() {
		if !ssmAutomationTerminal(exec.Status) {
			ssmStartAutomationRunner(exec.AutomationExecutionId)
		}
	}
}

func handleSSMStopAutomationExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutomationExecutionId string `json:"AutomationExecutionId"`
		Type                  string `json:"Type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stopType := req.Type
	if stopType == "" {
		stopType = "Cancel"
	}
	exec, ok := ssmAutomations.Get(req.AutomationExecutionId)
	if !ok {
		ssmAutomationNotFound(w, req.AutomationExecutionId)
		return
	}
	if ssmAutomationTerminal(exec.Status) {
		AWSErrorf(w, "InvalidAutomationStatusUpdateException", http.StatusBadRequest,
			"Automation execution %s is in a terminal state (%s) and can't be stopped.", exec.AutomationExecutionId, exec.Status)
		return
	}
	ssmAutomations.Update(req.AutomationExecutionId, func(e *SSMAutomationExecution) {
		if ssmAutomationTerminal(e.Status) || e.StopRequested == "Cancel" {
			return
		}
		e.StopRequested = stopType
		if stopType == "Cancel" {
			e.Status = "Cancelling"
		}
	})
	for _, child := range ssmAutomations.Filter(func(c SSMAutomationExecution) bool {
		return c.ParentId == req.AutomationExecutionId && !ssmAutomationTerminal(c.Status)
	}) {
		ssmAutomations.Update(child.AutomationExecutionId, func(e *SSMAutomationExecution) {
			if !ssmAutomationTerminal(e.Status) {
				e.StopRequested, e.Status = stopType, "Cancelling"
			}
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMSendAutomationSignal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutomationExecutionId string              `json:"AutomationExecutionId"`
		SignalType            string              `json:"SignalType"`
		Payload               map[string][]string `json:"Payload"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	exec, ok := ssmAutomations.Get(req.AutomationExecutionId)
	if !ok {
		ssmAutomationNotFound(w, req.AutomationExecutionId)
		return
	}
	invalid := func() {
		AWSErrorf(w, "InvalidAutomationSignalException", http.StatusBadRequest,
			"The signal %s isn't valid for the current Automation execution.", req.SignalType)
	}
	stepNotFound := func() {
		AWSErrorf(w, "AutomationStepNotFoundException", http.StatusBadRequest,
			"The specified step name and execution ID don't exist. Verify the information and try again.")
	}
	payload := func(key string) string {
		if v := req.Payload[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if ssmAutomationTerminal(exec.Status) || exec.StopRequested != "" {
		invalid()
		return
	}
	current := ssmCurrentStep(exec)
	switch req.SignalType {
	case "Resume":
		if current == nil || current.Action != "aws:pause" || current.StepStatus != "Waiting" {
			invalid()
			return
		}
		if payload("StepName") != current.StepName {
			stepNotFound()
			return
		}
	case "StartStep":
		if exec.Mode != "Interactive" || exec.Status != "Waiting" {
			invalid()
			return
		}
		runbook, err := ssmParseRunbook(exec.Content, exec.Format)
		if err != nil || runbook.step(payload("StepName")) == nil {
			stepNotFound()
			return
		}
	case "StopStep":
		if exec.Mode != "Interactive" || current == nil || current.StepStatus != "InProgress" {
			invalid()
			return
		}
		if payload("StepExecutionId") != current.StepExecID {
			stepNotFound()
			return
		}
		ssmAutomations.Update(exec.AutomationExecutionId, func(e *SSMAutomationExecution) { e.StopStepID = current.StepExecID })
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	default:
		invalid()
		return
	}
	signal := ssmAutomationSignal{Type: req.SignalType, Payload: req.Payload, CallerArn: awsCallerArn(r)}
	ssmAutomations.Update(exec.AutomationExecutionId, func(e *SSMAutomationExecution) { e.Signals = append(e.Signals, signal) })
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ssmCurrentStep is the step the execution is on: the last one that started.
func ssmCurrentStep(exec SSMAutomationExecution) *SSMAutomationStep {
	for i := len(exec.Steps) - 1; i >= 0; i-- {
		switch exec.Steps[i].StepStatus {
		case "InProgress", "Waiting", "Cancelling":
			return &exec.Steps[i]
		}
	}
	return nil
}

func ssmAutomationStepsWire(steps []SSMAutomationStep) []map[string]any {
	out := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		row := map[string]any{
			"StepName":        s.StepName,
			"Action":          s.Action,
			"StepStatus":      s.StepStatus,
			"StepExecutionId": s.StepExecID,
			"IsEnd":           s.IsEnd,
			"IsCritical":      s.IsCritical,
		}
		if s.ExecStartTime > 0 {
			row["ExecutionStartTime"] = s.ExecStartTime
		}
		if s.ExecEndTime > 0 {
			row["ExecutionEndTime"] = s.ExecEndTime
		}
		if s.ResponseCode != "" {
			row["ResponseCode"] = s.ResponseCode
		}
		if len(s.Inputs) > 0 {
			row["Inputs"] = s.Inputs
		}
		if len(s.Outputs) > 0 {
			row["Outputs"] = s.Outputs
		}
		if s.FailureMessage != "" {
			row["FailureMessage"] = s.FailureMessage
			row["FailureDetails"] = map[string]any{"FailureStage": s.FailureStage, "FailureType": s.FailureType}
		}
		if s.TimeoutSeconds > 0 {
			row["TimeoutSeconds"] = s.TimeoutSeconds
		}
		if s.MaxAttempts > 0 {
			row["MaxAttempts"] = s.MaxAttempts
		}
		if s.OnFailure != "" {
			row["OnFailure"] = s.OnFailure
		}
		if s.NextStep != "" {
			row["NextStep"] = s.NextStep
		}
		if len(s.ValidNextSteps) > 0 {
			row["ValidNextSteps"] = s.ValidNextSteps
		}
		out = append(out, row)
	}
	return out
}

func ssmProgressCounters(steps []SSMAutomationStep) map[string]int {
	counters := map[string]int{"TotalSteps": len(steps), "SuccessSteps": 0, "FailedSteps": 0, "CancelledSteps": 0, "TimedOutSteps": 0}
	for _, s := range steps {
		switch s.StepStatus {
		case "Success":
			counters["SuccessSteps"]++
		case "Failed":
			counters["FailedSteps"]++
		case "Cancelled":
			counters["CancelledSteps"]++
		case "TimedOut":
			counters["TimedOutSteps"]++
		}
	}
	return counters
}

// ssmAutomationCommonWire holds the members AutomationExecution and
// AutomationExecutionMetadata share.
func ssmAutomationCommonWire(exec SSMAutomationExecution) map[string]any {
	out := map[string]any{
		"AutomationExecutionId":     exec.AutomationExecutionId,
		"DocumentName":              exec.DocumentName,
		"AutomationExecutionStatus": exec.Status,
		"ExecutionStartTime":        exec.StartTime,
		"Mode":                      exec.Mode,
		"ExecutedBy":                exec.ExecutedBy,
		"Outputs":                   exec.Outputs,
	}
	if exec.EndTime > 0 {
		out["ExecutionEndTime"] = exec.EndTime
	}
	optional := map[string]string{
		"DocumentVersion":             exec.DocumentVersion,
		"FailureMessage":              exec.FailureMessage,
		"CurrentStepName":             exec.CurrentStepName,
		"CurrentAction":               exec.CurrentAction,
		"ParentAutomationExecutionId": exec.ParentId,
		"TargetParameterName":         exec.TargetParameterName,
		"MaxConcurrency":              exec.MaxConcurrency,
		"MaxErrors":                   exec.MaxErrors,
		"Target":                      exec.Target,
		"ChangeRequestName":           exec.ChangeRequestName,
		"AutomationSubtype":           exec.Subtype,
	}
	for key, value := range optional {
		if value != "" {
			out[key] = value
		}
	}
	if len(exec.Targets) > 0 {
		out["Targets"] = ssmTargetsWire(exec.Targets)
	}
	if len(exec.TargetMaps) > 0 {
		out["TargetMaps"] = exec.TargetMaps
	}
	if len(exec.ResolvedTargets) > 0 {
		out["ResolvedTargets"] = map[string]any{"ParameterValues": exec.ResolvedTargets, "Truncated": false}
	}
	if exec.ScheduledTime > 0 {
		out["ScheduledTime"] = exec.ScheduledTime
	}
	if len(exec.Runbooks) > 0 {
		out["Runbooks"] = exec.Runbooks
	}
	return out
}

func ssmAutomationWire(exec SSMAutomationExecution) map[string]any {
	out := ssmAutomationCommonWire(exec)
	out["StepExecutions"] = ssmAutomationStepsWire(exec.Steps)
	out["StepExecutionsTruncated"] = false
	out["Parameters"] = exec.Parameters
	out["ProgressCounters"] = ssmProgressCounters(exec.Steps)
	return out
}

func handleSSMGetAutomationExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutomationExecutionId string `json:"AutomationExecutionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	exec, ok := ssmAutomations.Get(req.AutomationExecutionId)
	if !ok {
		ssmAutomationNotFound(w, req.AutomationExecutionId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AutomationExecution": ssmAutomationWire(exec)})
}

type ssmAutomationFilter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

func ssmFilterTime(raw string) (float64, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return float64(t.UnixMilli()) / 1000, true
		}
	}
	return 0, false
}

func ssmAutomationMatches(exec SSMAutomationExecution, filters []ssmAutomationFilter) bool {
	for _, f := range filters {
		matched := false
		for _, v := range f.Values {
			switch f.Key {
			case "DocumentNamePrefix":
				matched = strings.HasPrefix(exec.DocumentName, v)
			case "ExecutionStatus":
				matched = exec.Status == v
			case "ExecutionId":
				matched = exec.AutomationExecutionId == v
			case "ParentExecutionId":
				matched = exec.ParentId == v
			case "CurrentAction":
				matched = exec.CurrentAction == v
			case "StartTimeBefore":
				t, ok := ssmFilterTime(v)
				matched = ok && exec.StartTime < t
			case "StartTimeAfter":
				t, ok := ssmFilterTime(v)
				matched = ok && exec.StartTime > t
			case "AutomationType":
				matched = v == "Local"
			case "AutomationSubtype":
				matched = exec.Subtype == v
			case "TagKey":
				tags, _ := ssmResourceTags.Get(ssmTagKey("Automation", exec.AutomationExecutionId))
				for _, tag := range tags {
					matched = matched || tag.Key == v
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func handleSSMDescribeAutomationExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters    []ssmAutomationFilter `json:"Filters"`
		MaxResults int                   `json:"MaxResults"`
		NextToken  string                `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmAutomations.Filter(func(e SSMAutomationExecution) bool { return ssmAutomationMatches(e, req.Filters) })
	if all == nil {
		all = []SSMAutomationExecution{}
	}
	page, next, pageOK := awsKeyPage(w, ssmBadToken, all, func(it SSMAutomationExecution) string { return it.AutomationExecutionId }, req.NextToken, req.MaxResults, 50)
	if !pageOK {
		return
	}
	out := make([]map[string]any, 0, len(page))
	for _, e := range page {
		entry := ssmAutomationCommonWire(e)
		entry["AutomationType"] = "Local"
		out = append(out, entry)
	}
	resp := map[string]any{"AutomationExecutionMetadataList": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func ssmStepMatches(step SSMAutomationStep, filters []ssmAutomationFilter) bool {
	for _, f := range filters {
		matched := false
		for _, v := range f.Values {
			switch f.Key {
			case "StepExecutionStatus":
				matched = strings.EqualFold(step.StepStatus, v)
			case "StepExecutionId":
				matched = step.StepExecID == v
			case "StepName":
				matched = step.StepName == v
			case "Action":
				matched = step.Action == v
			case "StartTimeBefore":
				t, ok := ssmFilterTime(v)
				matched = ok && step.ExecStartTime > 0 && step.ExecStartTime < t
			case "StartTimeAfter":
				t, ok := ssmFilterTime(v)
				matched = ok && step.ExecStartTime > t
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func handleSSMDescribeAutomationStepExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutomationExecutionId string                `json:"AutomationExecutionId"`
		Filters               []ssmAutomationFilter `json:"Filters"`
		ReverseOrder          bool                  `json:"ReverseOrder"`
		MaxResults            int                   `json:"MaxResults"`
		NextToken             string                `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	exec, ok := ssmAutomations.Get(req.AutomationExecutionId)
	if !ok {
		ssmAutomationNotFound(w, req.AutomationExecutionId)
		return
	}
	steps := make([]SSMAutomationStep, 0, len(exec.Steps))
	for _, s := range exec.Steps {
		if ssmStepMatches(s, req.Filters) {
			steps = append(steps, s)
		}
	}
	page, next, pageOK := awsSeqPage(w, ssmBadToken, steps, req.ReverseOrder, req.NextToken, req.MaxResults, 50)
	if !pageOK {
		return
	}
	resp := map[string]any{"StepExecutions": ssmAutomationStepsWire(page)}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ssmValueStrings renders a typed runbook value as an AutomationParameterMap
// value list: a StringList item by item, a map or a list of maps as JSON.
func ssmValueStrings(value any) []string {
	switch v := value.(type) {
	case nil:
		return []string{}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, ssmValueString(item))
		}
		return out
	}
	return []string{ssmValueString(value)}
}

func ssmValueString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func ssmSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
