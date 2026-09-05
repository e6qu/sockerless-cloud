package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// SSM State Manager associations + Automation + Run Command.
//
// State Manager binds an SSM document to a set of targets (instance IDs or
// tag/resource-group Targets) on a schedule; each binding is a versioned
// Association (AssociationId is a UUID) whose status settles Pending→Success.
// Automation runs a document as a multi-step workflow (AutomationExecutionId is
// a UUID) with real-shaped StepExecutions, and exposes an execution-preview
// surface. Run Command dispatches a document to instances (CommandId is a UUID),
// producing a per-instance CommandInvocation. All async state settles
// deterministically on read — there are no synthetic timers — so a control
// plane reads back a terminal Success the first time it polls.

// SSMAssociationVersion is one immutable revision of an association's binding.
type SSMAssociationVersion struct {
	AssociationVersion    string              `json:"AssociationVersion"`
	CreatedDate           float64             `json:"CreatedDate"`
	Name                  string              `json:"Name"`
	DocumentVersion       string              `json:"DocumentVersion,omitempty"`
	Parameters            map[string][]string `json:"Parameters,omitempty"`
	Targets               []SSMTarget         `json:"Targets,omitempty"`
	ScheduleExpression    string              `json:"ScheduleExpression,omitempty"`
	AssociationName       string              `json:"AssociationName,omitempty"`
	MaxErrors             string              `json:"MaxErrors,omitempty"`
	MaxConcurrency        string              `json:"MaxConcurrency,omitempty"`
	ComplianceSeverity    string              `json:"ComplianceSeverity,omitempty"`
	SyncCompliance        string              `json:"SyncCompliance,omitempty"`
	ApplyOnlyAtCron       bool                `json:"ApplyOnlyAtCronInterval,omitempty"`
	ScheduleOffset        int                 `json:"ScheduleOffset,omitempty"`
	Duration              int                 `json:"Duration,omitempty"`
	AutomationTargetParam string              `json:"AutomationTargetParameterName,omitempty"`
}

// SSMAssociation is a State Manager binding plus its version history.
type SSMAssociation struct {
	AssociationId         string                  `json:"AssociationId"`
	Name                  string                  `json:"Name"`
	InstanceId            string                  `json:"InstanceId,omitempty"`
	AssociationVersion    string                  `json:"AssociationVersion"`
	DocumentVersion       string                  `json:"DocumentVersion,omitempty"`
	Parameters            map[string][]string     `json:"Parameters,omitempty"`
	Targets               []SSMTarget             `json:"Targets,omitempty"`
	ScheduleExpression    string                  `json:"ScheduleExpression,omitempty"`
	AssociationName       string                  `json:"AssociationName,omitempty"`
	MaxErrors             string                  `json:"MaxErrors,omitempty"`
	MaxConcurrency        string                  `json:"MaxConcurrency,omitempty"`
	ComplianceSeverity    string                  `json:"ComplianceSeverity,omitempty"`
	SyncCompliance        string                  `json:"SyncCompliance,omitempty"`
	ApplyOnlyAtCron       bool                    `json:"ApplyOnlyAtCronInterval,omitempty"`
	ScheduleOffset        int                     `json:"ScheduleOffset,omitempty"`
	Duration              int                     `json:"Duration,omitempty"`
	AutomationTargetParam string                  `json:"AutomationTargetParameterName,omitempty"`
	Date                  float64                 `json:"Date"`
	LastUpdateDate        float64                 `json:"LastUpdateAssociationDate"`
	LastExecutionDate     float64                 `json:"LastExecutionDate"`
	StatusName            string                  `json:"StatusName"`
	StatusMessage         string                  `json:"StatusMessage,omitempty"`
	Versions              []SSMAssociationVersion `json:"Versions"`
	Executions            []SSMAssociationExec    `json:"Executions"`
}

// SSMAssociationExec is one execution of an association (created on demand by
// StartAssociationsOnce / on first read), already settled to Success.
type SSMAssociationExec struct {
	ExecutionId        string  `json:"ExecutionId"`
	AssociationVersion string  `json:"AssociationVersion"`
	Status             string  `json:"Status"`
	CreatedTime        float64 `json:"CreatedTime"`
	LastExecutionDate  float64 `json:"LastExecutionDate"`
	ResourceId         string  `json:"ResourceId"`
}

// SSMAutomationStep is one StepExecution of an automation workflow.
type SSMAutomationStep struct {
	StepName      string  `json:"StepName"`
	Action        string  `json:"Action"`
	StepStatus    string  `json:"StepStatus"`
	StepExecID    string  `json:"StepExecutionId"`
	ExecStartTime float64 `json:"ExecutionStartTime"`
	ExecEndTime   float64 `json:"ExecutionEndTime"`
	ResponseCode  string  `json:"ResponseCode"`
	IsEnd         bool    `json:"IsEnd"`
}

// SSMAutomationExecution is an Automation workflow run.
type SSMAutomationExecution struct {
	AutomationExecutionId string              `json:"AutomationExecutionId"`
	DocumentName          string              `json:"DocumentName"`
	DocumentVersion       string              `json:"DocumentVersion,omitempty"`
	Status                string              `json:"AutomationExecutionStatus"`
	Mode                  string              `json:"Mode"`
	Parameters            map[string][]string `json:"Parameters,omitempty"`
	Outputs               map[string][]string `json:"Outputs,omitempty"`
	StartTime             float64             `json:"ExecutionStartTime"`
	EndTime               float64             `json:"ExecutionEndTime"`
	ExecutedBy            string              `json:"ExecutedBy"`
	Steps                 []SSMAutomationStep `json:"StepExecutions"`
	ChangeRequestName     string              `json:"ChangeRequestName,omitempty"`
	Targets               []SSMTarget         `json:"Targets,omitempty"`
}

// SSMExecutionPreview is a StartExecutionPreview/GetExecutionPreview record.
type SSMExecutionPreview struct {
	ExecutionPreviewId string  `json:"ExecutionPreviewId"`
	DocumentName       string  `json:"DocumentName"`
	Status             string  `json:"Status"`
	EndedAt            float64 `json:"EndedAt"`
}

// SSMCommandInvocation is the per-instance result of a SendCommand dispatch.
type SSMCommandInvocation struct {
	InstanceId   string `json:"InstanceId"`
	Status       string `json:"Status"`
	StatusDetail string `json:"StatusDetails"`
	StdoutURL    string `json:"StandardOutputUrl,omitempty"`
	StderrURL    string `json:"StandardErrorUrl,omitempty"`
}

// SSMCommand is a Run Command dispatch plus its per-instance invocations.
type SSMCommand struct {
	CommandId       string                 `json:"CommandId"`
	DocumentName    string                 `json:"DocumentName"`
	DocumentVersion string                 `json:"DocumentVersion,omitempty"`
	Comment         string                 `json:"Comment,omitempty"`
	Parameters      map[string][]string    `json:"Parameters,omitempty"`
	InstanceIds     []string               `json:"InstanceIds"`
	Targets         []SSMTarget            `json:"Targets,omitempty"`
	RequestedTime   float64                `json:"RequestedDateTime"`
	ExpiresAfter    float64                `json:"ExpiresAfter"`
	Status          string                 `json:"Status"`
	StatusDetail    string                 `json:"StatusDetails"`
	MaxConcurrency  string                 `json:"MaxConcurrency,omitempty"`
	MaxErrors       string                 `json:"MaxErrors,omitempty"`
	TimeoutSeconds  int                    `json:"TimeoutSeconds"`
	Invocations     []SSMCommandInvocation `json:"Invocations"`
}

var (
	ssmAssociations sim.Store[SSMAssociation]
	ssmAutomations  sim.Store[SSMAutomationExecution]
	ssmPreviews     sim.Store[SSMExecutionPreview]
	ssmCommands     sim.Store[SSMCommand]
)

func registerSSMAssociations(r *AWSRouter, srv *sim.Server) {
	ssmAssociations = sim.MakeStore[SSMAssociation](srv.DB(), "ssm_associations")
	ssmAutomations = sim.MakeStore[SSMAutomationExecution](srv.DB(), "ssm_automations")
	ssmPreviews = sim.MakeStore[SSMExecutionPreview](srv.DB(), "ssm_execution_previews")
	ssmCommands = sim.MakeStore[SSMCommand](srv.DB(), "ssm_commands")

	for target, h := range map[string]http.HandlerFunc{
		"AmazonSSM.CreateAssociation":                     handleSSMCreateAssociation,
		"AmazonSSM.CreateAssociationBatch":                handleSSMCreateAssociationBatch,
		"AmazonSSM.DeleteAssociation":                     handleSSMDeleteAssociation,
		"AmazonSSM.DescribeAssociation":                   handleSSMDescribeAssociation,
		"AmazonSSM.UpdateAssociation":                     handleSSMUpdateAssociation,
		"AmazonSSM.UpdateAssociationStatus":               handleSSMUpdateAssociationStatus,
		"AmazonSSM.ListAssociations":                      handleSSMListAssociations,
		"AmazonSSM.ListAssociationVersions":               handleSSMListAssociationVersions,
		"AmazonSSM.DescribeAssociationExecutions":         handleSSMDescribeAssociationExecutions,
		"AmazonSSM.DescribeAssociationExecutionTargets":   handleSSMDescribeAssociationExecutionTargets,
		"AmazonSSM.DescribeEffectiveInstanceAssociations": handleSSMDescribeEffectiveInstanceAssociations,
		"AmazonSSM.DescribeInstanceAssociationsStatus":    handleSSMDescribeInstanceAssociationsStatus,
		"AmazonSSM.StartAssociationsOnce":                 handleSSMStartAssociationsOnce,
		"AmazonSSM.StartAutomationExecution":              handleSSMStartAutomationExecution,
		"AmazonSSM.StopAutomationExecution":               handleSSMStopAutomationExecution,
		"AmazonSSM.GetAutomationExecution":                handleSSMGetAutomationExecution,
		"AmazonSSM.DescribeAutomationExecutions":          handleSSMDescribeAutomationExecutions,
		"AmazonSSM.DescribeAutomationStepExecutions":      handleSSMDescribeAutomationStepExecutions,
		"AmazonSSM.SendAutomationSignal":                  handleSSMSendAutomationSignal,
		"AmazonSSM.StartChangeRequestExecution":           handleSSMStartChangeRequestExecution,
		"AmazonSSM.StartExecutionPreview":                 handleSSMStartExecutionPreview,
		"AmazonSSM.GetExecutionPreview":                   handleSSMGetExecutionPreview,
		"AmazonSSM.SendCommand":                           handleSSMSendCommand,
		"AmazonSSM.CancelCommand":                         handleSSMCancelCommand,
		"AmazonSSM.ListCommands":                          handleSSMListCommands,
		"AmazonSSM.ListCommandInvocations":                handleSSMListCommandInvocations,
		"AmazonSSM.GetCommandInvocation":                  handleSSMGetCommandInvocation,
	} {
		r.Register(target, h)
	}
}

// ssmDocExists validates a referenced SSM document name. Real SSM accepts both
// a bare document name and the AWS-managed documents (e.g. AWS-RunShellScript);
// the sim only knows documents in its own store, so a name that isn't present
// is rejected exactly as real SSM rejects an unknown user document.
func ssmDocExists(name string) bool {
	if name == "" {
		return false
	}
	_, ok := ssmDocuments.Get(name)
	return ok
}

// ssmAssociationDescriptionWire projects AssociationDescription.
func ssmAssociationDescriptionWire(a SSMAssociation) map[string]any {
	out := map[string]any{
		"Name":                      a.Name,
		"AssociationId":             a.AssociationId,
		"AssociationVersion":        a.AssociationVersion,
		"Date":                      a.Date,
		"LastUpdateAssociationDate": a.LastUpdateDate,
		"LastExecutionDate":         a.LastExecutionDate,
		"Status": map[string]any{
			"Date":    a.LastExecutionDate,
			"Name":    a.StatusName,
			"Message": a.StatusMessage,
		},
		"Overview": map[string]any{
			"Status":         a.StatusName,
			"DetailedStatus": a.StatusName,
		},
		"Targets":    ssmTargetsWire(a.Targets),
		"Parameters": a.Parameters,
	}
	if a.InstanceId != "" {
		out["InstanceId"] = a.InstanceId
	}
	if a.DocumentVersion != "" {
		out["DocumentVersion"] = a.DocumentVersion
	}
	if a.ScheduleExpression != "" {
		out["ScheduleExpression"] = a.ScheduleExpression
	}
	if a.AssociationName != "" {
		out["AssociationName"] = a.AssociationName
	}
	if a.MaxErrors != "" {
		out["MaxErrors"] = a.MaxErrors
	}
	if a.MaxConcurrency != "" {
		out["MaxConcurrency"] = a.MaxConcurrency
	}
	if a.ComplianceSeverity != "" {
		out["ComplianceSeverity"] = a.ComplianceSeverity
	}
	if a.SyncCompliance != "" {
		out["SyncCompliance"] = a.SyncCompliance
	}
	if a.AutomationTargetParam != "" {
		out["AutomationTargetParameterName"] = a.AutomationTargetParam
	}
	return out
}

// ssmAssociationResources returns the resource IDs an association targets — the
// explicit InstanceId or the values of an InstanceIds Target — used to shape
// per-target execution and effective-instance views.
func ssmAssociationResources(a SSMAssociation) []string {
	var ids []string
	if a.InstanceId != "" {
		ids = append(ids, a.InstanceId)
	}
	for _, t := range a.Targets {
		if t.Key == "InstanceIds" {
			ids = append(ids, t.Values...)
		}
	}
	if len(ids) == 0 {
		ids = append(ids, "i-0000000000000000")
	}
	return ids
}

type ssmCreateAssociationReq struct {
	Name                          string              `json:"Name"`
	DocumentVersion               string              `json:"DocumentVersion"`
	InstanceId                    string              `json:"InstanceId"`
	Parameters                    map[string][]string `json:"Parameters"`
	Targets                       []SSMTarget         `json:"Targets"`
	ScheduleExpression            string              `json:"ScheduleExpression"`
	AssociationName               string              `json:"AssociationName"`
	AutomationTargetParameterName string              `json:"AutomationTargetParameterName"`
	MaxErrors                     string              `json:"MaxErrors"`
	MaxConcurrency                string              `json:"MaxConcurrency"`
	ComplianceSeverity            string              `json:"ComplianceSeverity"`
	SyncCompliance                string              `json:"SyncCompliance"`
	ApplyOnlyAtCronInterval       bool                `json:"ApplyOnlyAtCronInterval"`
	ScheduleOffset                int                 `json:"ScheduleOffset"`
	Duration                      int                 `json:"Duration"`
}

// ssmBuildAssociation creates a fresh association from a request entry.
func ssmBuildAssociation(req ssmCreateAssociationReq) SSMAssociation {
	now := float64(time.Now().Unix())
	a := SSMAssociation{
		AssociationId:         uuid.New().String(),
		Name:                  req.Name,
		InstanceId:            req.InstanceId,
		AssociationVersion:    "1",
		DocumentVersion:       req.DocumentVersion,
		Parameters:            req.Parameters,
		Targets:               req.Targets,
		ScheduleExpression:    req.ScheduleExpression,
		AssociationName:       req.AssociationName,
		MaxErrors:             req.MaxErrors,
		MaxConcurrency:        req.MaxConcurrency,
		ComplianceSeverity:    req.ComplianceSeverity,
		SyncCompliance:        req.SyncCompliance,
		ApplyOnlyAtCron:       req.ApplyOnlyAtCronInterval,
		ScheduleOffset:        req.ScheduleOffset,
		Duration:              req.Duration,
		AutomationTargetParam: req.AutomationTargetParameterName,
		Date:                  now,
		LastUpdateDate:        now,
		LastExecutionDate:     now,
		// On-demand associations (no schedule) settle to Success immediately;
		// scheduled ones start Pending until their first execution. Either way
		// the sim has no synthetic timer — the next read of a scheduled
		// association settles it.
		StatusName: "Pending",
	}
	a.Versions = []SSMAssociationVersion{ssmAssociationVersionOf(a)}
	return a
}

func ssmAssociationVersionOf(a SSMAssociation) SSMAssociationVersion {
	return SSMAssociationVersion{
		AssociationVersion:    a.AssociationVersion,
		CreatedDate:           a.LastUpdateDate,
		Name:                  a.Name,
		DocumentVersion:       a.DocumentVersion,
		Parameters:            a.Parameters,
		Targets:               a.Targets,
		ScheduleExpression:    a.ScheduleExpression,
		AssociationName:       a.AssociationName,
		MaxErrors:             a.MaxErrors,
		MaxConcurrency:        a.MaxConcurrency,
		ComplianceSeverity:    a.ComplianceSeverity,
		SyncCompliance:        a.SyncCompliance,
		ApplyOnlyAtCron:       a.ApplyOnlyAtCron,
		ScheduleOffset:        a.ScheduleOffset,
		Duration:              a.Duration,
		AutomationTargetParam: a.AutomationTargetParam,
	}
}

// ssmSettleAssociation moves a Pending association to its terminal Success on
// read and records an execution row, mirroring State Manager applying the
// document. Deterministic — no timer.
func ssmSettleAssociation(a SSMAssociation) SSMAssociation {
	if a.StatusName == "Pending" {
		a.StatusName = "Success"
		a.LastExecutionDate = float64(time.Now().Unix())
		if len(a.Executions) == 0 {
			a.Executions = append(a.Executions, ssmNewAssociationExec(a))
		}
		ssmAssociations.Put(a.AssociationId, a)
	}
	return a
}

func ssmNewAssociationExec(a SSMAssociation) SSMAssociationExec {
	now := float64(time.Now().Unix())
	return SSMAssociationExec{
		ExecutionId:        uuid.New().String(),
		AssociationVersion: a.AssociationVersion,
		Status:             "Success",
		CreatedTime:        now,
		LastExecutionDate:  now,
		ResourceId:         ssmAssociationResources(a)[0],
	}
}

func handleSSMCreateAssociation(w http.ResponseWriter, r *http.Request) {
	var req ssmCreateAssociationReq
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ssmDocExists(req.Name) {
		AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	a := ssmBuildAssociation(req)
	ssmAssociations.Put(a.AssociationId, a)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AssociationDescription": ssmAssociationDescriptionWire(a),
	})
}

func handleSSMCreateAssociationBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []ssmCreateAssociationReq `json:"Entries"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	successful := []map[string]any{}
	failed := []map[string]any{}
	for _, e := range req.Entries {
		if !ssmDocExists(e.Name) {
			failed = append(failed, map[string]any{
				"Entry":   ssmBatchEntryWire(e),
				"Message": "The specified SSM document doesn't exist.",
				"Fault":   "Client",
			})
			continue
		}
		a := ssmBuildAssociation(e)
		ssmAssociations.Put(a.AssociationId, a)
		successful = append(successful, ssmAssociationDescriptionWire(a))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Successful": successful,
		"Failed":     failed,
	})
}

func ssmBatchEntryWire(e ssmCreateAssociationReq) map[string]any {
	out := map[string]any{"Name": e.Name}
	if e.InstanceId != "" {
		out["InstanceId"] = e.InstanceId
	}
	if len(e.Targets) > 0 {
		out["Targets"] = ssmTargetsWire(e.Targets)
	}
	if e.AssociationName != "" {
		out["AssociationName"] = e.AssociationName
	}
	return out
}

func handleSSMDeleteAssociation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationId string `json:"AssociationId"`
		Name          string `json:"Name"`
		InstanceId    string `json:"InstanceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := ssmResolveAssociation(req.AssociationId, req.Name, req.InstanceId)
	if !ok {
		AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
			"The specified association doesn't exist.")
		return
	}
	ssmAssociations.Delete(a.AssociationId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ssmResolveAssociation finds an association by AssociationId, or by the
// (Name, InstanceId) pair real SSM also accepts.
func ssmResolveAssociation(id, name, instanceID string) (SSMAssociation, bool) {
	if id != "" {
		return ssmAssociations.Get(id)
	}
	for _, a := range ssmAssociations.List() {
		if a.Name != name {
			continue
		}
		if instanceID == "" {
			return a, true
		}
		for _, rid := range ssmAssociationResources(a) {
			if rid == instanceID {
				return a, true
			}
		}
	}
	return SSMAssociation{}, false
}

func handleSSMDescribeAssociation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationId      string `json:"AssociationId"`
		Name               string `json:"Name"`
		InstanceId         string `json:"InstanceId"`
		AssociationVersion string `json:"AssociationVersion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := ssmResolveAssociation(req.AssociationId, req.Name, req.InstanceId)
	if !ok {
		AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
			"The specified association doesn't exist.")
		return
	}
	a = ssmSettleAssociation(a)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AssociationDescription": ssmAssociationDescriptionWire(a),
	})
}

func handleSSMUpdateAssociation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationId      string              `json:"AssociationId"`
		Parameters         map[string][]string `json:"Parameters"`
		DocumentVersion    string              `json:"DocumentVersion"`
		ScheduleExpression string              `json:"ScheduleExpression"`
		Name               string              `json:"Name"`
		Targets            []SSMTarget         `json:"Targets"`
		AssociationName    string              `json:"AssociationName"`
		MaxErrors          string              `json:"MaxErrors"`
		MaxConcurrency     string              `json:"MaxConcurrency"`
		ComplianceSeverity string              `json:"ComplianceSeverity"`
		SyncCompliance     string              `json:"SyncCompliance"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := ssmAssociations.Get(req.AssociationId)
	if !ok {
		AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
			"The specified association doesn't exist.")
		return
	}
	if req.Parameters != nil {
		a.Parameters = req.Parameters
	}
	if req.DocumentVersion != "" {
		a.DocumentVersion = req.DocumentVersion
	}
	if req.ScheduleExpression != "" {
		a.ScheduleExpression = req.ScheduleExpression
	}
	if req.Name != "" {
		a.Name = req.Name
	}
	if req.Targets != nil {
		a.Targets = req.Targets
	}
	if req.AssociationName != "" {
		a.AssociationName = req.AssociationName
	}
	if req.MaxErrors != "" {
		a.MaxErrors = req.MaxErrors
	}
	if req.MaxConcurrency != "" {
		a.MaxConcurrency = req.MaxConcurrency
	}
	if req.ComplianceSeverity != "" {
		a.ComplianceSeverity = req.ComplianceSeverity
	}
	if req.SyncCompliance != "" {
		a.SyncCompliance = req.SyncCompliance
	}
	a.AssociationVersion = ssmNextAssociationVersion(a)
	a.LastUpdateDate = float64(time.Now().Unix())
	a.Versions = append(a.Versions, ssmAssociationVersionOf(a))
	ssmAssociations.Put(a.AssociationId, a)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AssociationDescription": ssmAssociationDescriptionWire(a),
	})
}

func ssmNextAssociationVersion(a SSMAssociation) string {
	max := 0
	for _, v := range a.Versions {
		n := atoiSSM(v.AssociationVersion)
		if n > max {
			max = n
		}
	}
	return itoaSSM(max + 1)
}

func atoiSSM(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func handleSSMUpdateAssociationStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string `json:"Name"`
		InstanceId        string `json:"InstanceId"`
		AssociationStatus struct {
			Date    float64 `json:"Date"`
			Name    string  `json:"Name"`
			Message string  `json:"Message"`
		} `json:"AssociationStatus"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := ssmResolveAssociation("", req.Name, req.InstanceId)
	if !ok {
		AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
			"The specified association doesn't exist.")
		return
	}
	if req.AssociationStatus.Name != "" {
		a.StatusName = req.AssociationStatus.Name
	}
	a.StatusMessage = req.AssociationStatus.Message
	a.LastExecutionDate = float64(time.Now().Unix())
	ssmAssociations.Put(a.AssociationId, a)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AssociationDescription": ssmAssociationDescriptionWire(a),
	})
}

func handleSSMListAssociations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationFilterList []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"AssociationFilterList"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmAssociations.Filter(func(a SSMAssociation) bool {
		for _, f := range req.AssociationFilterList {
			switch f.Key {
			case "Name":
				if a.Name != f.Value {
					return false
				}
			case "AssociationId":
				if a.AssociationId != f.Value {
					return false
				}
			case "InstanceId":
				if a.InstanceId != f.Value {
					return false
				}
			case "AssociationName":
				if a.AssociationName != f.Value {
					return false
				}
			}
		}
		return true
	})
	sortBy(all, func(a SSMAssociation) string { return a.AssociationId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, a := range page {
		a = ssmSettleAssociation(a)
		entry := map[string]any{
			"Name":               a.Name,
			"AssociationId":      a.AssociationId,
			"AssociationVersion": a.AssociationVersion,
			"LastExecutionDate":  a.LastExecutionDate,
			"Targets":            ssmTargetsWire(a.Targets),
			"Overview": map[string]any{
				"Status":         a.StatusName,
				"DetailedStatus": a.StatusName,
			},
		}
		if a.InstanceId != "" {
			entry["InstanceId"] = a.InstanceId
		}
		if a.DocumentVersion != "" {
			entry["DocumentVersion"] = a.DocumentVersion
		}
		if a.ScheduleExpression != "" {
			entry["ScheduleExpression"] = a.ScheduleExpression
		}
		if a.AssociationName != "" {
			entry["AssociationName"] = a.AssociationName
		}
		out = append(out, entry)
	}
	resp := map[string]any{"Associations": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMListAssociationVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationId string `json:"AssociationId"`
		MaxResults    int    `json:"MaxResults"`
		NextToken     string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := ssmAssociations.Get(req.AssociationId)
	if !ok {
		AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
			"The specified association doesn't exist.")
		return
	}
	all := make([]SSMAssociationVersion, len(a.Versions))
	copy(all, a.Versions)
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, v := range page {
		entry := map[string]any{
			"AssociationId":      a.AssociationId,
			"AssociationVersion": v.AssociationVersion,
			"CreatedDate":        v.CreatedDate,
			"Name":               v.Name,
			"Targets":            ssmTargetsWire(v.Targets),
			"Parameters":         v.Parameters,
		}
		if v.DocumentVersion != "" {
			entry["DocumentVersion"] = v.DocumentVersion
		}
		if v.ScheduleExpression != "" {
			entry["ScheduleExpression"] = v.ScheduleExpression
		}
		if v.AssociationName != "" {
			entry["AssociationName"] = v.AssociationName
		}
		out = append(out, entry)
	}
	resp := map[string]any{"AssociationVersions": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeAssociationExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationId string `json:"AssociationId"`
		MaxResults    int    `json:"MaxResults"`
		NextToken     string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := ssmAssociations.Get(req.AssociationId)
	if !ok {
		AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
			"The specified association doesn't exist.")
		return
	}
	a = ssmSettleAssociation(a)
	all := make([]SSMAssociationExec, len(a.Executions))
	copy(all, a.Executions)
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, e := range page {
		out = append(out, map[string]any{
			"AssociationId":      a.AssociationId,
			"AssociationVersion": e.AssociationVersion,
			"ExecutionId":        e.ExecutionId,
			"Status":             e.Status,
			"DetailedStatus":     e.Status,
			"CreatedTime":        e.CreatedTime,
			"LastExecutionDate":  e.LastExecutionDate,
			"ResourceCountByStatus": "{Success=" +
				itoaSSM(len(ssmAssociationResources(a))) + "}",
		})
	}
	resp := map[string]any{"AssociationExecutions": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeAssociationExecutionTargets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationId string `json:"AssociationId"`
		ExecutionId   string `json:"ExecutionId"`
		MaxResults    int    `json:"MaxResults"`
		NextToken     string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := ssmAssociations.Get(req.AssociationId)
	if !ok {
		AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
			"The specified association doesn't exist.")
		return
	}
	a = ssmSettleAssociation(a)
	if len(a.Executions) == 0 {
		AWSErrorf(w, "AssociationExecutionDoesNotExist", http.StatusBadRequest,
			"The specified execution ID doesn't exist.")
		return
	}
	execID := req.ExecutionId
	if execID == "" {
		execID = a.Executions[0].ExecutionId
	}
	resources := ssmAssociationResources(a)
	all := make([]map[string]any, 0, len(resources))
	now := float64(time.Now().Unix())
	for _, rid := range resources {
		all = append(all, map[string]any{
			"AssociationId":      a.AssociationId,
			"AssociationVersion": a.AssociationVersion,
			"ExecutionId":        execID,
			"ResourceId":         rid,
			"ResourceType":       "ManagedInstance",
			"Status":             "Success",
			"DetailedStatus":     "Success",
			"LastExecutionDate":  now,
			"OutputSource": map[string]any{
				"OutputSourceId":   uuid.New().String(),
				"OutputSourceType": "Amazon S3",
			},
		})
	}
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"AssociationExecutionTargets": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeEffectiveInstanceAssociations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.InstanceId == "" {
		AWSError(w, "ValidationException", "InstanceId is required", http.StatusBadRequest)
		return
	}
	matched := ssmAssociationsForInstance(req.InstanceId)
	all := make([]map[string]any, 0, len(matched))
	for _, a := range matched {
		content := ""
		if doc, ok := ssmDocuments.Get(a.Name); ok {
			if v, ok := ssmDocVersion(doc, a.DocumentVersion); ok {
				content = v.Content
			}
		}
		all = append(all, map[string]any{
			"AssociationId":      a.AssociationId,
			"InstanceId":         req.InstanceId,
			"Content":            content,
			"AssociationVersion": a.AssociationVersion,
		})
	}
	page, next := awsPage(all, req.NextToken, req.MaxResults, 5)
	resp := map[string]any{"Associations": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeInstanceAssociationsStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.InstanceId == "" {
		AWSError(w, "ValidationException", "InstanceId is required", http.StatusBadRequest)
		return
	}
	matched := ssmAssociationsForInstance(req.InstanceId)
	all := make([]map[string]any, 0, len(matched))
	for _, a := range matched {
		a = ssmSettleAssociation(a)
		entry := map[string]any{
			"AssociationId":      a.AssociationId,
			"Name":               a.Name,
			"AssociationVersion": a.AssociationVersion,
			"InstanceId":         req.InstanceId,
			"ExecutionDate":      a.LastExecutionDate,
			"Status":             a.StatusName,
			"DetailedStatus":     a.StatusName,
		}
		if a.DocumentVersion != "" {
			entry["DocumentVersion"] = a.DocumentVersion
		}
		if a.AssociationName != "" {
			entry["AssociationName"] = a.AssociationName
		}
		all = append(all, entry)
	}
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"InstanceAssociationStatusInfos": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ssmAssociationsForInstance returns associations bound to an instance, either
// by explicit InstanceId or by an InstanceIds Target.
func ssmAssociationsForInstance(instanceID string) []SSMAssociation {
	var out []SSMAssociation
	for _, a := range ssmAssociations.List() {
		for _, rid := range ssmAssociationResources(a) {
			if rid == instanceID {
				out = append(out, a)
				break
			}
		}
	}
	sortBy(out, func(a SSMAssociation) string { return a.AssociationId })
	return out
}

func handleSSMStartAssociationsOnce(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssociationIds []string `json:"AssociationIds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	for _, id := range req.AssociationIds {
		a, ok := ssmAssociations.Get(id)
		if !ok {
			AWSErrorf(w, "AssociationDoesNotExist", http.StatusBadRequest,
				"The specified association doesn't exist.")
			return
		}
		a.Executions = append(a.Executions, ssmNewAssociationExec(a))
		a.StatusName = "Success"
		a.LastExecutionDate = float64(time.Now().Unix())
		ssmAssociations.Put(a.AssociationId, a)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func ssmBuildAutomation(docName, docVersion, mode, changeReq string, params map[string][]string, targets []SSMTarget) SSMAutomationExecution {
	now := float64(time.Now().Unix())
	id := uuid.New().String()
	// One real-shaped, already-settled step per execution — the sim runs the
	// document synchronously, so GetAutomationExecution reads back Success.
	step := SSMAutomationStep{
		StepName:      "runStep",
		Action:        "aws:runShellScript",
		StepStatus:    "Success",
		StepExecID:    uuid.New().String(),
		ExecStartTime: now,
		ExecEndTime:   now,
		ResponseCode:  "0",
		IsEnd:         true,
	}
	return SSMAutomationExecution{
		AutomationExecutionId: id,
		DocumentName:          docName,
		DocumentVersion:       docVersion,
		Status:                "Success",
		Mode:                  mode,
		Parameters:            params,
		Outputs:               map[string][]string{},
		StartTime:             now,
		EndTime:               now,
		ExecutedBy:            "arn:aws:iam::" + awsAccountID() + ":user/sockerless",
		Steps:                 []SSMAutomationStep{step},
		ChangeRequestName:     changeReq,
		Targets:               targets,
	}
}

func handleSSMStartAutomationExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DocumentName    string              `json:"DocumentName"`
		DocumentVersion string              `json:"DocumentVersion"`
		Parameters      map[string][]string `json:"Parameters"`
		Mode            string              `json:"Mode"`
		Targets         []SSMTarget         `json:"Targets"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ssmDocExists(req.DocumentName) {
		AWSErrorf(w, "AutomationDefinitionNotFoundException", http.StatusBadRequest,
			"An Automation runbook with the specified name couldn't be found.")
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "Auto"
	}
	exec := ssmBuildAutomation(req.DocumentName, req.DocumentVersion, mode, "", req.Parameters, req.Targets)
	ssmAutomations.Put(exec.AutomationExecutionId, exec)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AutomationExecutionId": exec.AutomationExecutionId,
	})
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
	exec, ok := ssmAutomations.Get(req.AutomationExecutionId)
	if !ok {
		AWSErrorf(w, "AutomationExecutionNotFoundException", http.StatusBadRequest,
			"Automation execution %s couldn't be found.", req.AutomationExecutionId)
		return
	}
	exec.Status = "Cancelled"
	exec.EndTime = float64(time.Now().Unix())
	ssmAutomations.Put(exec.AutomationExecutionId, exec)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func ssmAutomationStepsWire(steps []SSMAutomationStep) []map[string]any {
	out := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		out = append(out, map[string]any{
			"StepName":           s.StepName,
			"Action":             s.Action,
			"StepStatus":         s.StepStatus,
			"StepExecutionId":    s.StepExecID,
			"ExecutionStartTime": s.ExecStartTime,
			"ExecutionEndTime":   s.ExecEndTime,
			"ResponseCode":       s.ResponseCode,
			"IsEnd":              s.IsEnd,
		})
	}
	return out
}

func ssmAutomationWire(exec SSMAutomationExecution) map[string]any {
	out := map[string]any{
		"AutomationExecutionId":     exec.AutomationExecutionId,
		"DocumentName":              exec.DocumentName,
		"AutomationExecutionStatus": exec.Status,
		"ExecutionStartTime":        exec.StartTime,
		"ExecutionEndTime":          exec.EndTime,
		"Mode":                      exec.Mode,
		"ExecutedBy":                exec.ExecutedBy,
		"StepExecutions":            ssmAutomationStepsWire(exec.Steps),
		"StepExecutionsTruncated":   false,
		"Parameters":                exec.Parameters,
		"Outputs":                   exec.Outputs,
		"Targets":                   ssmTargetsWire(exec.Targets),
	}
	if exec.DocumentVersion != "" {
		out["DocumentVersion"] = exec.DocumentVersion
	}
	if exec.ChangeRequestName != "" {
		out["ChangeRequestName"] = exec.ChangeRequestName
	}
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
		AWSErrorf(w, "AutomationExecutionNotFoundException", http.StatusBadRequest,
			"Automation execution %s couldn't be found.", req.AutomationExecutionId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AutomationExecution": ssmAutomationWire(exec),
	})
}

func handleSSMDescribeAutomationExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmAutomations.List()
	if all == nil {
		all = []SSMAutomationExecution{}
	}
	sortBy(all, func(e SSMAutomationExecution) string { return e.AutomationExecutionId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, e := range page {
		entry := map[string]any{
			"AutomationExecutionId":     e.AutomationExecutionId,
			"DocumentName":              e.DocumentName,
			"AutomationExecutionStatus": e.Status,
			"ExecutionStartTime":        e.StartTime,
			"ExecutionEndTime":          e.EndTime,
			"ExecutedBy":                e.ExecutedBy,
			"Mode":                      e.Mode,
			"AutomationType":            "Local",
		}
		if e.DocumentVersion != "" {
			entry["DocumentVersion"] = e.DocumentVersion
		}
		if e.ChangeRequestName != "" {
			entry["ChangeRequestName"] = e.ChangeRequestName
		}
		out = append(out, entry)
	}
	resp := map[string]any{"AutomationExecutionMetadataList": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeAutomationStepExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutomationExecutionId string `json:"AutomationExecutionId"`
		ReverseOrder          bool   `json:"ReverseOrder"`
		MaxResults            int    `json:"MaxResults"`
		NextToken             string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	exec, ok := ssmAutomations.Get(req.AutomationExecutionId)
	if !ok {
		AWSErrorf(w, "AutomationExecutionNotFoundException", http.StatusBadRequest,
			"Automation execution %s couldn't be found.", req.AutomationExecutionId)
		return
	}
	steps := make([]SSMAutomationStep, len(exec.Steps))
	copy(steps, exec.Steps)
	if req.ReverseOrder {
		for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
			steps[i], steps[j] = steps[j], steps[i]
		}
	}
	page, next := awsPage(steps, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"StepExecutions": ssmAutomationStepsWire(page)}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMSendAutomationSignal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutomationExecutionId string `json:"AutomationExecutionId"`
		SignalType            string `json:"SignalType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmAutomations.Get(req.AutomationExecutionId); !ok {
		AWSErrorf(w, "AutomationExecutionNotFoundException", http.StatusBadRequest,
			"Automation execution %s couldn't be found.", req.AutomationExecutionId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMStartChangeRequestExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DocumentName      string              `json:"DocumentName"`
		DocumentVersion   string              `json:"DocumentVersion"`
		Parameters        map[string][]string `json:"Parameters"`
		ChangeRequestName string              `json:"ChangeRequestName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ssmDocExists(req.DocumentName) {
		AWSErrorf(w, "AutomationDefinitionNotFoundException", http.StatusBadRequest,
			"An Automation runbook with the specified name couldn't be found.")
		return
	}
	exec := ssmBuildAutomation(req.DocumentName, req.DocumentVersion, "Auto", req.ChangeRequestName, req.Parameters, nil)
	ssmAutomations.Put(exec.AutomationExecutionId, exec)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AutomationExecutionId": exec.AutomationExecutionId,
	})
}

func handleSSMStartExecutionPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DocumentName    string `json:"DocumentName"`
		DocumentVersion string `json:"DocumentVersion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ssmDocExists(req.DocumentName) {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	p := SSMExecutionPreview{
		ExecutionPreviewId: uuid.New().String(),
		DocumentName:       req.DocumentName,
		Status:             "Success",
		EndedAt:            float64(time.Now().Unix()),
	}
	ssmPreviews.Put(p.ExecutionPreviewId, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ExecutionPreviewId": p.ExecutionPreviewId,
	})
}

func handleSSMGetExecutionPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionPreviewId string `json:"ExecutionPreviewId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	p, ok := ssmPreviews.Get(req.ExecutionPreviewId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified execution preview doesn't exist.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ExecutionPreviewId": p.ExecutionPreviewId,
		"Status":             p.Status,
		"EndedAt":            p.EndedAt,
		"ExecutionPreview": map[string]any{
			"Automation": map[string]any{
				"StepPreviews":  map[string]int{"Mutating": 1},
				"Regions":       []string{awsRegion()},
				"TotalAccounts": 1,
				"TargetPreviews": []map[string]any{
					{"Count": 1, "TargetType": "AWS::EC2::Instance"},
				},
			},
		},
	})
}

func handleSSMSendCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceIds     []string            `json:"InstanceIds"`
		Targets         []SSMTarget         `json:"Targets"`
		DocumentName    string              `json:"DocumentName"`
		DocumentVersion string              `json:"DocumentVersion"`
		Comment         string              `json:"Comment"`
		Parameters      map[string][]string `json:"Parameters"`
		MaxConcurrency  string              `json:"MaxConcurrency"`
		MaxErrors       string              `json:"MaxErrors"`
		TimeoutSeconds  int                 `json:"TimeoutSeconds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ssmDocExists(req.DocumentName) {
		AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	targets := req.InstanceIds
	for _, t := range req.Targets {
		if t.Key == "InstanceIds" {
			targets = append(targets, t.Values...)
		}
	}
	now := float64(time.Now().Unix())
	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = 3600
	}
	invocations := make([]SSMCommandInvocation, 0, len(targets))
	cmdID := uuid.New().String()
	for _, id := range targets {
		invocations = append(invocations, SSMCommandInvocation{
			InstanceId:   id,
			Status:       "Success",
			StatusDetail: "Success",
			StdoutURL:    "",
			StderrURL:    "",
		})
	}
	cmd := SSMCommand{
		CommandId:       cmdID,
		DocumentName:    req.DocumentName,
		DocumentVersion: req.DocumentVersion,
		Comment:         req.Comment,
		Parameters:      req.Parameters,
		InstanceIds:     targets,
		Targets:         req.Targets,
		RequestedTime:   now,
		ExpiresAfter:    now + float64(timeout),
		Status:          "Success",
		StatusDetail:    "Success",
		// A command always reports a concurrency and an error threshold: AWS
		// applies its documented defaults when the request names none, so an
		// unset member is not empty on the wire.
		MaxConcurrency: ssmOrDefault(req.MaxConcurrency, "50"),
		MaxErrors:      ssmOrDefault(req.MaxErrors, "0"),
		TimeoutSeconds: timeout,
		Invocations:    invocations,
	}
	ssmCommands.Put(cmd.CommandId, cmd)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Command": ssmCommandWire(cmd),
	})
}

func ssmOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func ssmCommandWire(cmd SSMCommand) map[string]any {
	out := map[string]any{
		"CommandId":             cmd.CommandId,
		"DocumentName":          cmd.DocumentName,
		"Comment":               cmd.Comment,
		"Parameters":            cmd.Parameters,
		"InstanceIds":           cmd.InstanceIds,
		"Targets":               ssmTargetsWire(cmd.Targets),
		"RequestedDateTime":     cmd.RequestedTime,
		"ExpiresAfter":          cmd.ExpiresAfter,
		"Status":                cmd.Status,
		"StatusDetails":         cmd.StatusDetail,
		"MaxConcurrency":        cmd.MaxConcurrency,
		"MaxErrors":             cmd.MaxErrors,
		"TargetCount":           len(cmd.InstanceIds),
		"CompletedCount":        len(cmd.Invocations),
		"ErrorCount":            0,
		"DeliveryTimedOutCount": 0,
		"TimeoutSeconds":        cmd.TimeoutSeconds,
	}
	if cmd.DocumentVersion != "" {
		out["DocumentVersion"] = cmd.DocumentVersion
	}
	return out
}

func handleSSMCancelCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CommandId   string   `json:"CommandId"`
		InstanceIds []string `json:"InstanceIds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cmd, ok := ssmCommands.Get(req.CommandId)
	if !ok {
		AWSErrorf(w, "InvalidCommandId", http.StatusBadRequest,
			"The specified command ID isn't valid.")
		return
	}
	if cmd.Status == "Success" {
		// A finished command can't be cancelled; real SSM no-ops.
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	cmd.Status = "Cancelled"
	cmd.StatusDetail = "Cancelled"
	ssmCommands.Put(cmd.CommandId, cmd)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMListCommands(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CommandId  string `json:"CommandId"`
		InstanceId string `json:"InstanceId"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmCommands.Filter(func(c SSMCommand) bool {
		if req.CommandId != "" && c.CommandId != req.CommandId {
			return false
		}
		if req.InstanceId != "" {
			found := false
			for _, id := range c.InstanceIds {
				if id == req.InstanceId {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	})
	sortBy(all, func(c SSMCommand) string { return c.CommandId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, c := range page {
		out = append(out, ssmCommandWire(c))
	}
	resp := map[string]any{"Commands": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMListCommandInvocations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CommandId  string `json:"CommandId"`
		InstanceId string `json:"InstanceId"`
		Details    bool   `json:"Details"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	type inv struct {
		cmd SSMCommand
		i   SSMCommandInvocation
	}
	var all []inv
	for _, c := range ssmCommands.List() {
		if req.CommandId != "" && c.CommandId != req.CommandId {
			continue
		}
		for _, i := range c.Invocations {
			if req.InstanceId != "" && i.InstanceId != req.InstanceId {
				continue
			}
			all = append(all, inv{cmd: c, i: i})
		}
	}
	sortBy(all, func(v inv) string { return v.cmd.CommandId + "/" + v.i.InstanceId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, v := range page {
		entry := map[string]any{
			"CommandId":         v.cmd.CommandId,
			"InstanceId":        v.i.InstanceId,
			"Comment":           v.cmd.Comment,
			"DocumentName":      v.cmd.DocumentName,
			"RequestedDateTime": v.cmd.RequestedTime,
			"Status":            v.i.Status,
			"StatusDetails":     v.i.StatusDetail,
		}
		if v.cmd.DocumentVersion != "" {
			entry["DocumentVersion"] = v.cmd.DocumentVersion
		}
		if req.Details {
			entry["CommandPlugins"] = []map[string]any{
				{
					"Name":          "aws:runShellScript",
					"Status":        "Success",
					"StatusDetails": "Success",
					"ResponseCode":  0,
					"Output":        "",
				},
			}
		}
		out = append(out, entry)
	}
	resp := map[string]any{"CommandInvocations": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMGetCommandInvocation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CommandId  string `json:"CommandId"`
		InstanceId string `json:"InstanceId"`
		PluginName string `json:"PluginName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cmd, ok := ssmCommands.Get(req.CommandId)
	if !ok {
		AWSErrorf(w, "InvalidCommandId", http.StatusBadRequest,
			"The specified command ID isn't valid.")
		return
	}
	var found *SSMCommandInvocation
	for i := range cmd.Invocations {
		if cmd.Invocations[i].InstanceId == req.InstanceId {
			found = &cmd.Invocations[i]
			break
		}
	}
	if found == nil {
		AWSErrorf(w, "InvocationDoesNotExist", http.StatusBadRequest,
			"The command ID and managed node ID you specified didn't match any invocations.")
		return
	}
	// ExecutionStartDateTime / ExecutionElapsedTime / ExecutionEndDateTime are
	// StringDateTime members on this op (ISO-8601 / duration strings), unlike
	// the epoch-number DateTime used elsewhere.
	start := time.Unix(int64(cmd.RequestedTime), 0).UTC()
	out := map[string]any{
		"CommandId":              cmd.CommandId,
		"InstanceId":             req.InstanceId,
		"Comment":                cmd.Comment,
		"DocumentName":           cmd.DocumentName,
		"PluginName":             ssmDefault(req.PluginName, "aws:runShellScript"),
		"ResponseCode":           0,
		"ExecutionStartDateTime": start.Format(time.RFC3339),
		"ExecutionElapsedTime":   "PT0S",
		"ExecutionEndDateTime":   start.Format(time.RFC3339),
		"Status":                 found.Status,
		"StatusDetails":          found.StatusDetail,
		"StandardOutputContent":  "",
		"StandardErrorContent":   "",
	}
	if cmd.DocumentVersion != "" {
		out["DocumentVersion"] = cmd.DocumentVersion
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func ssmDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
