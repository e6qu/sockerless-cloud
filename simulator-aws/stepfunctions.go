package main

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// AWS Step Functions — AWS JSON 1.0 protocol (X-Amz-Target: AWSStepFunctions.<Op>).
// Executions run a small ASL interpreter for the state types exercised by sockerless.

type SFNStateMachine struct {
	StateMachineArn         string         `json:"stateMachineArn"`
	Name                    string         `json:"name"`
	Definition              string         `json:"definition"`
	RoleArn                 string         `json:"roleArn"`
	Type                    string         `json:"type"`
	Status                  string         `json:"status"`
	CreationDate            float64        `json:"creationDate"`
	UpdateDate              float64        `json:"updateDate"`
	RevisionId              string         `json:"revisionId"`
	LoggingConfiguration    map[string]any `json:"loggingConfiguration,omitempty"`
	TracingConfiguration    map[string]any `json:"tracingConfiguration,omitempty"`
	EncryptionConfiguration map[string]any `json:"encryptionConfiguration,omitempty"`
	Tags                    []SFNTag       `json:"tags,omitempty"`
}

type SFNExecution struct {
	ExecutionArn           string             `json:"executionArn"`
	StateMachineArn        string             `json:"stateMachineArn"`
	Name                   string             `json:"name"`
	Status                 string             `json:"status"`
	StartDate              float64            `json:"startDate"`
	StopDate               *float64           `json:"stopDate,omitempty"`
	Input                  string             `json:"input,omitempty"`
	Output                 string             `json:"output,omitempty"`
	RedriveCount           int                `json:"redriveCount"`
	RedriveDate            *float64           `json:"redriveDate,omitempty"`
	Error                  string             `json:"error,omitempty"`
	Cause                  string             `json:"cause,omitempty"`
	StateMachineAliasArn   string             `json:"stateMachineAliasArn,omitempty"`
	StateMachineVersionArn string             `json:"stateMachineVersionArn,omitempty"`
	MapRunArn              string             `json:"mapRunArn,omitempty"`
	TraceHeader            string             `json:"traceHeader,omitempty"`
	DefinitionSnapshot     string             `json:"definitionSnapshot"`
	RoleArnSnapshot        string             `json:"roleArnSnapshot"`
	TypeSnapshot           string             `json:"typeSnapshot"`
	RevisionIdSnapshot     string             `json:"revisionIdSnapshot"`
	UpdateDateSnapshot     float64            `json:"updateDateSnapshot"`
	LoggingSnapshot        map[string]any     `json:"loggingSnapshot,omitempty"`
	TracingSnapshot        map[string]any     `json:"tracingSnapshot,omitempty"`
	EncryptionSnapshot     map[string]any     `json:"encryptionSnapshot,omitempty"`
	RedriveState           string             `json:"redriveState,omitempty"`
	RedriveInput           string             `json:"redriveInput,omitempty"`
	CheckpointEnteredDate  float64            `json:"checkpointEnteredDate,omitempty"`
	TaskCheckpoint         *SFNTaskCheckpoint `json:"taskCheckpoint,omitempty"`
	RedriveClientTokens    map[string]float64 `json:"redriveClientTokens,omitempty"`
}

type SFNTaskCheckpoint struct {
	StateName   string   `json:"stateName"`
	Resource    string   `json:"resource"`
	ResourceIDs []string `json:"resourceIds"`
}

type SFNTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

var (
	sfnStateMachines sim.Store[SFNStateMachine]
	sfnExecutions    sim.Store[SFNExecution]
	sfnCancels       sync.Map
	sfnMu            sync.Mutex
	sfnAWSRouter     *sim.AWSRouter
)

func registerStepFunctions(r *sim.AWSRouter, srv *sim.Server) {
	sfnAWSRouter = r
	sfnAWSServer = srv
	sfnStateMachines = sim.MakeStore[SFNStateMachine](srv.DB(), "sfn_state_machines")
	sfnExecutions = sim.MakeStore[SFNExecution](srv.DB(), "sfn_executions")
	sfnHistories = sim.MakeStore[[]sfnHistoryEvent](srv.DB(), "sfn_execution_histories")

	r.Register("AWSStepFunctions.CreateStateMachine", handleSFNCreateStateMachine)
	r.Register("AWSStepFunctions.DescribeStateMachine", handleSFNDescribeStateMachine)
	r.Register("AWSStepFunctions.ListStateMachines", handleSFNListStateMachines)
	r.Register("AWSStepFunctions.DeleteStateMachine", handleSFNDeleteStateMachine)
	r.Register("AWSStepFunctions.UpdateStateMachine", handleSFNUpdateStateMachine)
	r.Register("AWSStepFunctions.TagResource", handleSFNTagResource)
	r.Register("AWSStepFunctions.UntagResource", handleSFNUntagResource)
	r.Register("AWSStepFunctions.ListTagsForResource", handleSFNListTagsForResource)
	r.Register("AWSStepFunctions.ValidateStateMachineDefinition", handleSFNValidateStateMachineDefinition)
	r.Register("AWSStepFunctions.ListStateMachineVersions", handleSFNListStateMachineVersions)
	r.Register("AWSStepFunctions.StartExecution", handleSFNStartExecution)
	r.Register("AWSStepFunctions.DescribeExecution", handleSFNDescribeExecution)
	r.Register("AWSStepFunctions.GetExecutionHistory", handleSFNGetExecutionHistory)
	r.Register("AWSStepFunctions.ListExecutions", handleSFNListExecutions)
	r.Register("AWSStepFunctions.StopExecution", handleSFNStopExecution)

	// Activities — named task-poll resources with an ARN, plus the
	// task-token lifecycle GetActivityTask/SendTask* manage.
	sfnActivities = sim.MakeStore[SFNActivity](srv.DB(), "sfn_activities")
	sfnActivityTasks = sim.MakeStore[SFNActivityTask](srv.DB(), "sfn_activity_tasks")
	r.Register("AWSStepFunctions.CreateActivity", handleSFNCreateActivity)
	r.Register("AWSStepFunctions.DeleteActivity", handleSFNDeleteActivity)
	r.Register("AWSStepFunctions.DescribeActivity", handleSFNDescribeActivity)
	r.Register("AWSStepFunctions.ListActivities", handleSFNListActivities)
	r.Register("AWSStepFunctions.GetActivityTask", handleSFNGetActivityTask)
	r.Register("AWSStepFunctions.SendTaskSuccess", handleSFNSendTaskSuccess)
	r.Register("AWSStepFunctions.SendTaskFailure", handleSFNSendTaskFailure)
	r.Register("AWSStepFunctions.SendTaskHeartbeat", handleSFNSendTaskHeartbeat)

	// State-machine versions (numbered immutable snapshots) + aliases
	// (named pointers with a routingConfiguration to versions).
	sfnVersions = sim.MakeStore[SFNStateMachineVersion](srv.DB(), "sfn_sm_versions")
	sfnAliases = sim.MakeStore[SFNStateMachineAlias](srv.DB(), "sfn_sm_aliases")
	r.Register("AWSStepFunctions.PublishStateMachineVersion", handleSFNPublishStateMachineVersion)
	r.Register("AWSStepFunctions.DeleteStateMachineVersion", handleSFNDeleteStateMachineVersion)
	r.Register("AWSStepFunctions.CreateStateMachineAlias", handleSFNCreateStateMachineAlias)
	r.Register("AWSStepFunctions.DeleteStateMachineAlias", handleSFNDeleteStateMachineAlias)
	r.Register("AWSStepFunctions.DescribeStateMachineAlias", handleSFNDescribeStateMachineAlias)
	r.Register("AWSStepFunctions.ListStateMachineAliases", handleSFNListStateMachineAliases)
	r.Register("AWSStepFunctions.UpdateStateMachineAlias", handleSFNUpdateStateMachineAlias)

	// Execution-scoped read/redrive + Map Run aggregation.
	r.Register("AWSStepFunctions.DescribeStateMachineForExecution", handleSFNDescribeStateMachineForExecution)
	r.Register("AWSStepFunctions.RedriveExecution", handleSFNRedriveExecution)
	sfnMapRuns = sim.MakeStore[SFNMapRun](srv.DB(), "sfn_map_runs")
	r.Register("AWSStepFunctions.DescribeMapRun", handleSFNDescribeMapRun)
	r.Register("AWSStepFunctions.ListMapRuns", handleSFNListMapRuns)
	r.Register("AWSStepFunctions.UpdateMapRun", handleSFNUpdateMapRun)

	// Synchronous state / state-machine evaluation.
	r.Register("AWSStepFunctions.TestState", handleSFNTestState)
	r.Register("AWSStepFunctions.StartSyncExecution", handleSFNStartSyncExecution)
}

func sfnARN(resource string) string {
	return fmt.Sprintf("arn:aws:states:%s:%s:%s", awsRegion(), awsAccountID(), resource)
}

func sfnEpochNow() float64 {
	return float64(time.Now().UTC().UnixNano()) / float64(time.Second)
}

func sfnWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sfnWriteError(w http.ResponseWriter, code string, msg string) {
	sfnWriteErrorStatus(w, http.StatusBadRequest, code, msg)
}

func sfnWriteErrorStatus(w http.ResponseWriter, status int, code string, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"__type":  code,
		"message": msg,
	})
}

func handleSFNCreateStateMachine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                    string         `json:"name"`
		Definition              string         `json:"definition"`
		RoleArn                 string         `json:"roleArn"`
		Type                    string         `json:"type"`
		Tags                    []SFNTag       `json:"tags"`
		Publish                 bool           `json:"publish"`
		VersionDescription      string         `json:"versionDescription"`
		LoggingConfiguration    map[string]any `json:"loggingConfiguration"`
		TracingConfiguration    map[string]any `json:"tracingConfiguration"`
		EncryptionConfiguration map[string]any `json:"encryptionConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if req.Name == "" {
		sfnWriteError(w, "InvalidName", "name is required")
		return
	}
	if req.RoleArn == "" {
		sfnWriteError(w, "InvalidArn", "roleArn is required")
		return
	}
	if req.VersionDescription != "" && !req.Publish {
		sfnWriteError(w, "ValidationException", "versionDescription requires publish to be true")
		return
	}
	// Validate the ASL definition at create time (the validator AWS runs);
	// a malformed/empty definition is an InvalidDefinition error.
	if err := sfnValidateDefinition(req.Definition); err != nil {
		sfnWriteError(w, "InvalidDefinition", err.Error())
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	if _, ok := sfnStateMachines.Get(req.Name); ok {
		sfnWriteError(w, "StateMachineAlreadyExists", "State machine already exists: "+req.Name)
		return
	}

	smType := req.Type
	if smType == "" {
		smType = "STANDARD"
	}
	if smType != "STANDARD" && smType != "EXPRESS" {
		sfnWriteError(w, "ValidationException", "type must be STANDARD or EXPRESS")
		return
	}
	if req.LoggingConfiguration == nil {
		req.LoggingConfiguration = map[string]any{
			"destinations":         []any{},
			"includeExecutionData": false,
			"level":                "OFF",
		}
	}
	if req.TracingConfiguration == nil {
		req.TracingConfiguration = map[string]any{"enabled": false}
	}
	if req.EncryptionConfiguration == nil {
		req.EncryptionConfiguration = map[string]any{"type": "AWS_OWNED_KEY"}
	}
	arn := sfnARN("stateMachine:" + req.Name)
	now := sfnEpochNow()
	sm := SFNStateMachine{
		StateMachineArn:         arn,
		Name:                    req.Name,
		Definition:              req.Definition,
		RoleArn:                 req.RoleArn,
		Type:                    smType,
		Status:                  "ACTIVE",
		CreationDate:            now,
		UpdateDate:              now,
		RevisionId:              uuid.NewString(),
		LoggingConfiguration:    req.LoggingConfiguration,
		TracingConfiguration:    req.TracingConfiguration,
		EncryptionConfiguration: req.EncryptionConfiguration,
		Tags:                    req.Tags,
	}
	sfnStateMachines.Put(req.Name, sm)
	response := map[string]any{
		"stateMachineArn": arn,
		"creationDate":    sm.CreationDate,
	}
	if req.Publish {
		version := sfnPublishVersion(sm, req.VersionDescription)
		response["stateMachineVersionArn"] = version.StateMachineVersionArn
	}
	sfnWriteJSON(w, http.StatusOK, response)
}

func handleSFNDescribeStateMachine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		IncludedData    string `json:"includedData"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	if version, ok := sfnVersions.Get(req.StateMachineArn); ok {
		definition := version.Definition
		if req.IncludedData == "METADATA_ONLY" {
			definition = "{}"
		}
		response := map[string]any{
			"stateMachineArn": version.StateMachineVersionArn,
			"name":            version.StateMachineName,
			"status":          "ACTIVE",
			"definition":      definition,
			"roleArn":         version.RoleArn,
			"type":            version.Type,
			"creationDate":    version.CreationDate,
			"description":     version.Description,
			"revisionId":      version.RevisionId,
		}
		sfnAddStateMachineConfiguration(response, version.LoggingConfiguration, version.TracingConfiguration, version.EncryptionConfiguration)
		sfnWriteJSON(w, http.StatusOK, response)
		return
	}
	name := sfnNameFromARN(req.StateMachineArn)
	sm, ok := sfnStateMachines.Get(name)
	if !ok {
		sfnWriteError(w, "StateMachineDoesNotExist", "State machine does not exist: "+req.StateMachineArn)
		return
	}
	definition := sm.Definition
	if req.IncludedData == "METADATA_ONLY" {
		definition = "{}"
	}
	response := map[string]any{
		"stateMachineArn": sm.StateMachineArn,
		"name":            sm.Name,
		"status":          sm.Status,
		"definition":      definition,
		"roleArn":         sm.RoleArn,
		"type":            sm.Type,
		"creationDate":    sm.CreationDate,
		"revisionId":      sm.RevisionId,
	}
	sfnAddStateMachineConfiguration(response, sm.LoggingConfiguration, sm.TracingConfiguration, sm.EncryptionConfiguration)
	sfnWriteJSON(w, http.StatusOK, response)
}

func sfnAddStateMachineConfiguration(
	response map[string]any,
	loggingConfiguration, tracingConfiguration, encryptionConfiguration map[string]any,
) {
	if loggingConfiguration != nil {
		response["loggingConfiguration"] = loggingConfiguration
	}
	if tracingConfiguration != nil {
		response["tracingConfiguration"] = tracingConfiguration
	}
	if encryptionConfiguration != nil {
		response["encryptionConfiguration"] = encryptionConfiguration
	}
}

func handleSFNListStateMachines(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults *int   `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := sfnStateMachines.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)

	items := make([]map[string]any, 0, len(page))
	for _, sm := range page {
		items = append(items, map[string]any{
			"stateMachineArn": sm.StateMachineArn,
			"name":            sm.Name,
			"type":            sm.Type,
			"creationDate":    sm.CreationDate,
		})
	}
	resp := map[string]any{"stateMachines": items}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func handleSFNDeleteStateMachine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	name := sfnNameFromARN(req.StateMachineArn)
	sfnStateMachines.Delete(name)
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSFNUpdateStateMachine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn         string         `json:"stateMachineArn"`
		Definition              string         `json:"definition"`
		RoleArn                 string         `json:"roleArn"`
		Publish                 bool           `json:"publish"`
		VersionDescription      string         `json:"versionDescription"`
		LoggingConfiguration    map[string]any `json:"loggingConfiguration"`
		TracingConfiguration    map[string]any `json:"tracingConfiguration"`
		EncryptionConfiguration map[string]any `json:"encryptionConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if req.Definition != "" {
		if err := sfnValidateDefinition(req.Definition); err != nil {
			sfnWriteError(w, "InvalidDefinition", err.Error())
			return
		}
	}
	if req.VersionDescription != "" && !req.Publish {
		sfnWriteError(w, "ValidationException", "versionDescription requires publish to be true")
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	name := sfnNameFromARN(req.StateMachineArn)
	sm, ok := sfnStateMachines.Get(name)
	if !ok {
		sfnWriteError(w, "StateMachineDoesNotExist", "State machine does not exist: "+req.StateMachineArn)
		return
	}
	if req.Definition != "" {
		sm.Definition = req.Definition
	}
	if req.RoleArn != "" {
		sm.RoleArn = req.RoleArn
	}
	if req.LoggingConfiguration != nil {
		sm.LoggingConfiguration = req.LoggingConfiguration
	}
	if req.TracingConfiguration != nil {
		sm.TracingConfiguration = req.TracingConfiguration
	}
	if req.EncryptionConfiguration != nil {
		sm.EncryptionConfiguration = req.EncryptionConfiguration
	}
	sm.UpdateDate = sfnEpochNow()
	sm.RevisionId = uuid.NewString()
	sfnStateMachines.Put(name, sm)
	response := map[string]any{"updateDate": sm.UpdateDate, "revisionId": sm.RevisionId}
	if req.Publish {
		version := sfnPublishVersion(sm, req.VersionDescription)
		response["stateMachineVersionArn"] = version.StateMachineVersionArn
	}
	sfnWriteJSON(w, http.StatusOK, response)
}

func handleSFNTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		Tags        []SFNTag `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	name := sfnNameFromARN(req.ResourceArn)
	if strings.Contains(req.ResourceArn, ":activity:") {
		activity, ok := sfnActivities.Get(name)
		if !ok {
			sfnWriteError(w, "ResourceNotFound", "Resource not found: "+req.ResourceArn)
			return
		}
		activity.Tags = sfnMergeTags(activity.Tags, req.Tags)
		sfnActivities.Put(name, activity)
	} else {
		sm, ok := sfnStateMachines.Get(name)
		if !ok {
			sfnWriteError(w, "ResourceNotFound", "Resource not found: "+req.ResourceArn)
			return
		}
		sm.Tags = sfnMergeTags(sm.Tags, req.Tags)
		sfnStateMachines.Put(name, sm)
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSFNUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	name := sfnNameFromARN(req.ResourceArn)
	if strings.Contains(req.ResourceArn, ":activity:") {
		activity, ok := sfnActivities.Get(name)
		if !ok {
			sfnWriteError(w, "ResourceNotFound", "Resource not found: "+req.ResourceArn)
			return
		}
		activity.Tags = sfnRemoveTags(activity.Tags, req.TagKeys)
		sfnActivities.Put(name, activity)
	} else {
		sm, ok := sfnStateMachines.Get(name)
		if !ok {
			sfnWriteError(w, "ResourceNotFound", "Resource not found: "+req.ResourceArn)
			return
		}
		sm.Tags = sfnRemoveTags(sm.Tags, req.TagKeys)
		sfnStateMachines.Put(name, sm)
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSFNListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	name := sfnNameFromARN(req.ResourceArn)
	var tags []SFNTag
	if strings.Contains(req.ResourceArn, ":activity:") {
		activity, ok := sfnActivities.Get(name)
		if !ok {
			sfnWriteError(w, "ResourceNotFound", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = activity.Tags
	} else {
		sm, ok := sfnStateMachines.Get(name)
		if !ok {
			sfnWriteError(w, "ResourceNotFound", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = sm.Tags
	}
	if tags == nil {
		tags = []SFNTag{}
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func handleSFNListStateMachineVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		MaxResults      *int   `json:"maxResults"`
		NextToken       string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	smName := sfnNameFromARN(req.StateMachineArn)
	if _, ok := sfnStateMachines.Get(smName); !ok {
		sfnWriteError(w, "StateMachineDoesNotExist", "State machine does not exist: "+req.StateMachineArn)
		return
	}
	versions := make([]SFNStateMachineVersion, 0)
	for _, version := range sfnVersions.List() {
		if version.StateMachineName == smName {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	maxResults := 0
	if req.MaxResults != nil {
		maxResults = *req.MaxResults
	}
	page, nextToken := awsPage(versions, req.NextToken, maxResults, 100)
	items := make([]map[string]any, 0, len(page))
	for _, version := range page {
		items = append(items, map[string]any{
			"stateMachineVersionArn": version.StateMachineVersionArn,
			"creationDate":           version.CreationDate,
		})
	}
	response := map[string]any{"stateMachineVersions": items}
	if nextToken != "" {
		response["nextToken"] = nextToken
	}
	sfnWriteJSON(w, http.StatusOK, response)
}

func handleSFNValidateStateMachineDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Definition string `json:"definition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if err := sfnValidateDefinition(req.Definition); err != nil {
		sfnWriteJSON(w, http.StatusOK, map[string]any{
			"result":      "FAIL",
			"diagnostics": []map[string]string{{"message": err.Error()}},
		})
		return
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{"result": "OK"})
}

func handleSFNStartExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		Name            string `json:"name"`
		Input           string `json:"input"`
		TraceHeader     string `json:"traceHeader"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	sm, versionArn, aliasArn, ok := sfnResolveExecutionTarget(req.StateMachineArn)
	if !ok {
		sfnWriteError(w, "StateMachineDoesNotExist", "State machine does not exist: "+req.StateMachineArn)
		return
	}
	name := sm.Name
	if err := sfnValidateDefinition(sm.Definition); err != nil {
		sfnWriteError(w, "InvalidDefinition", err.Error())
		return
	}

	execName := req.Name
	if execName == "" {
		execName = uuid.New().String()
	}
	execARN := sfnARN("execution:" + name + ":" + execName)

	sfnMu.Lock()
	defer sfnMu.Unlock()

	if _, ok := sfnExecutions.Get(execARN); ok {
		sfnWriteError(w, "ExecutionAlreadyExists", "Execution already exists: "+execARN)
		return
	}

	now := sfnEpochNow()
	input := req.Input
	if input == "" {
		input = "{}"
	}
	exec := SFNExecution{
		ExecutionArn:           execARN,
		StateMachineArn:        sm.StateMachineArn,
		StateMachineAliasArn:   aliasArn,
		StateMachineVersionArn: versionArn,
		Name:                   execName,
		Status:                 "RUNNING",
		StartDate:              now,
		Input:                  input,
		TraceHeader:            req.TraceHeader,
		DefinitionSnapshot:     sm.Definition,
		RoleArnSnapshot:        sm.RoleArn,
		TypeSnapshot:           sm.Type,
		RevisionIdSnapshot:     sm.RevisionId,
		UpdateDateSnapshot:     sm.UpdateDate,
		LoggingSnapshot:        sm.LoggingConfiguration,
		TracingSnapshot:        sm.TracingConfiguration,
		EncryptionSnapshot:     sm.EncryptionConfiguration,
	}
	sfnExecutions.Put(execARN, exec)
	sfnAppendHistory(execARN, "ExecutionStarted", map[string]any{
		"input":        input,
		"inputDetails": map[string]any{"truncated": false},
		"roleArn":      sm.RoleArn,
	})
	cancel := make(chan struct{})
	sfnCancels.Store(execARN, cancel)
	go sfnRunExecution(execARN, sm.Definition, input, cancel)
	sfnWriteJSON(w, http.StatusOK, map[string]any{
		"executionArn": execARN,
		"startDate":    now,
	})
}

func sfnResolveExecutionTarget(arn string) (SFNStateMachine, string, string, bool) {
	if version, ok := sfnVersions.Get(arn); ok {
		sm, exists := sfnStateMachines.Get(version.StateMachineName)
		if !exists {
			return SFNStateMachine{}, "", "", false
		}
		sm.Definition = version.Definition
		sm.RoleArn = version.RoleArn
		sm.Type = version.Type
		sm.RevisionId = version.RevisionId
		sm.UpdateDate = version.UpdateDate
		sm.LoggingConfiguration = version.LoggingConfiguration
		sm.TracingConfiguration = version.TracingConfiguration
		sm.EncryptionConfiguration = version.EncryptionConfiguration
		return sm, version.StateMachineVersionArn, "", true
	}
	if alias, ok := sfnAliasByArn(arn); ok {
		if len(alias.RoutingConfiguration) == 0 {
			return SFNStateMachine{}, "", "", false
		}
		selection, err := cryptorand.Int(cryptorand.Reader, big.NewInt(100))
		if err != nil {
			return SFNStateMachine{}, "", "", false
		}
		point := int(selection.Int64())
		selected := alias.RoutingConfiguration[len(alias.RoutingConfiguration)-1].StateMachineVersionArn
		total := 0
		for _, route := range alias.RoutingConfiguration {
			total += route.Weight
			if point < total {
				selected = route.StateMachineVersionArn
				break
			}
		}
		sm, versionArn, _, exists := sfnResolveExecutionTarget(selected)
		return sm, versionArn, alias.StateMachineAliasArn, exists
	}
	sm, ok := sfnStateMachines.Get(sfnNameFromARN(arn))
	return sm, "", "", ok
}

type sfnDefinition struct {
	Comment         string              `json:"Comment"`
	QueryLanguage   string              `json:"QueryLanguage"`
	StartAt         string              `json:"StartAt"`
	TimeoutSeconds  *int                `json:"TimeoutSeconds"`
	States          map[string]sfnState `json:"States"`
	ProcessorConfig *sfnProcessorConfig `json:"ProcessorConfig"`
}

type sfnProcessorConfig struct {
	Mode          string `json:"Mode"`
	ExecutionType string `json:"ExecutionType"`
}

type sfnState struct {
	Type           string           `json:"Type"`
	Comment        string           `json:"Comment"`
	QueryLanguage  string           `json:"QueryLanguage"`
	Next           string           `json:"Next"`
	End            bool             `json:"End"`
	InputPath      json.RawMessage  `json:"InputPath"`
	Parameters     map[string]any   `json:"Parameters"`
	Arguments      any              `json:"Arguments"`
	Result         *json.RawMessage `json:"Result"`
	ResultSelector map[string]any   `json:"ResultSelector"`
	ResultPath     json.RawMessage  `json:"ResultPath"`
	OutputPath     json.RawMessage  `json:"OutputPath"`
	Assign         map[string]any   `json:"Assign"`
	Output         json.RawMessage  `json:"Output"`
	Seconds        *int             `json:"Seconds"`
	SecondsPath    string           `json:"SecondsPath"`
	Timestamp      string           `json:"Timestamp"`
	TimestampPath  string           `json:"TimestampPath"`
	Error          string           `json:"Error"`
	ErrorPath      string           `json:"ErrorPath"`
	Cause          string           `json:"Cause"`
	CausePath      string           `json:"CausePath"`
	// Task
	Resource             string         `json:"Resource"`
	TimeoutSeconds       *int           `json:"TimeoutSeconds"`
	TimeoutSecondsPath   string         `json:"TimeoutSecondsPath"`
	HeartbeatSeconds     *int           `json:"HeartbeatSeconds"`
	HeartbeatSecondsPath string         `json:"HeartbeatSecondsPath"`
	Retry                []sfnRetrier   `json:"Retry"`
	Catch                []sfnCatcher   `json:"Catch"`
	Credentials          map[string]any `json:"Credentials"`
	// Choice
	Choices []sfnChoiceRule `json:"Choices"`
	Default string          `json:"Default"`
	// Parallel
	Branches []sfnDefinition `json:"Branches"`
	// Map
	Iterator                   *sfnDefinition  `json:"Iterator"`
	ItemProcessor              *sfnDefinition  `json:"ItemProcessor"`
	ItemsPath                  string          `json:"ItemsPath"`
	Items                      json.RawMessage `json:"Items"`
	ItemSelector               map[string]any  `json:"ItemSelector"`
	MaxConcurrency             int             `json:"MaxConcurrency"`
	Label                      string          `json:"Label"`
	ToleratedFailurePercentage *float64        `json:"ToleratedFailurePercentage"`
	ToleratedFailureCount      *int            `json:"ToleratedFailureCount"`
}

type sfnRetrier struct {
	ErrorEquals     []string `json:"ErrorEquals"`
	IntervalSeconds int      `json:"IntervalSeconds"`
	MaxAttempts     *int     `json:"MaxAttempts"`
	BackoffRate     float64  `json:"BackoffRate"`
	MaxDelaySeconds int      `json:"MaxDelaySeconds"`
	JitterStrategy  string   `json:"JitterStrategy"`
}

type sfnCatcher struct {
	ErrorEquals []string        `json:"ErrorEquals"`
	Next        string          `json:"Next"`
	ResultPath  json.RawMessage `json:"ResultPath"`
	Assign      map[string]any  `json:"Assign"`
	Output      json.RawMessage `json:"Output"`
}

// sfnChoiceRule is one Choice rule: a data-test (optionally nested via
// And/Or/Not) plus the Next state to transition to when it matches.
type sfnChoiceRule struct {
	Condition                      json.RawMessage `json:"Condition"`
	Assign                         map[string]any  `json:"Assign"`
	Output                         json.RawMessage `json:"Output"`
	Variable                       string          `json:"Variable"`
	StringEquals                   *string         `json:"StringEquals"`
	StringEqualsPath               string          `json:"StringEqualsPath"`
	StringLessThan                 *string         `json:"StringLessThan"`
	StringLessThanEquals           *string         `json:"StringLessThanEquals"`
	StringGreaterThan              *string         `json:"StringGreaterThan"`
	StringGreaterThanEquals        *string         `json:"StringGreaterThanEquals"`
	StringMatches                  *string         `json:"StringMatches"`
	NumericEquals                  *float64        `json:"NumericEquals"`
	NumericEqualsPath              string          `json:"NumericEqualsPath"`
	NumericGreaterThan             *float64        `json:"NumericGreaterThan"`
	NumericGreaterThanPath         string          `json:"NumericGreaterThanPath"`
	NumericGreaterThanEquals       *float64        `json:"NumericGreaterThanEquals"`
	NumericGreaterThanEqualsPath   string          `json:"NumericGreaterThanEqualsPath"`
	NumericLessThan                *float64        `json:"NumericLessThan"`
	NumericLessThanPath            string          `json:"NumericLessThanPath"`
	NumericLessThanEquals          *float64        `json:"NumericLessThanEquals"`
	NumericLessThanEqualsPath      string          `json:"NumericLessThanEqualsPath"`
	BooleanEquals                  *bool           `json:"BooleanEquals"`
	BooleanEqualsPath              string          `json:"BooleanEqualsPath"`
	TimestampEquals                *string         `json:"TimestampEquals"`
	TimestampEqualsPath            string          `json:"TimestampEqualsPath"`
	TimestampLessThan              *string         `json:"TimestampLessThan"`
	TimestampLessThanPath          string          `json:"TimestampLessThanPath"`
	TimestampLessThanEquals        *string         `json:"TimestampLessThanEquals"`
	TimestampLessThanEqualsPath    string          `json:"TimestampLessThanEqualsPath"`
	TimestampGreaterThan           *string         `json:"TimestampGreaterThan"`
	TimestampGreaterThanPath       string          `json:"TimestampGreaterThanPath"`
	TimestampGreaterThanEquals     *string         `json:"TimestampGreaterThanEquals"`
	TimestampGreaterThanEqualsPath string          `json:"TimestampGreaterThanEqualsPath"`
	IsPresent                      *bool           `json:"IsPresent"`
	IsNull                         *bool           `json:"IsNull"`
	IsString                       *bool           `json:"IsString"`
	IsNumeric                      *bool           `json:"IsNumeric"`
	IsBoolean                      *bool           `json:"IsBoolean"`
	IsTimestamp                    *bool           `json:"IsTimestamp"`
	And                            []sfnChoiceRule `json:"And"`
	Or                             []sfnChoiceRule `json:"Or"`
	Not                            *sfnChoiceRule  `json:"Not"`
	Next                           string          `json:"Next"`
}

var errSFNAborted = errors.New("execution aborted")

type sfnExecutionError struct {
	Name  string
	Cause string
}

func (e *sfnExecutionError) Error() string {
	if e.Cause == "" {
		return e.Name
	}
	return e.Name + ": " + e.Cause
}

func sfnValidateDefinition(definition string) error {
	var def sfnDefinition
	if err := json.Unmarshal([]byte(definition), &def); err != nil {
		return fmt.Errorf("invalid ASL JSON: %w", err)
	}
	return sfnValidateDefinitionObject(def, "$", "")
}

func sfnValidateDefinitionObject(def sfnDefinition, location, inheritedQueryLanguage string) error {
	if def.StartAt == "" {
		return fmt.Errorf("%s.StartAt is required", location)
	}
	if len(def.States) == 0 {
		return fmt.Errorf("%s.States is required", location)
	}
	if _, ok := def.States[def.StartAt]; !ok {
		return fmt.Errorf("%s.StartAt state %q does not exist", location, def.StartAt)
	}
	queryLanguage := def.QueryLanguage
	if queryLanguage == "" {
		queryLanguage = inheritedQueryLanguage
	}
	if queryLanguage == "" {
		queryLanguage = "JSONPath"
	}
	if queryLanguage != "JSONPath" && queryLanguage != "JSONata" {
		return fmt.Errorf("%s.QueryLanguage must be JSONPath or JSONata", location)
	}
	if def.TimeoutSeconds != nil && *def.TimeoutSeconds <= 0 {
		return fmt.Errorf("%s.TimeoutSeconds must be positive", location)
	}
	for name, state := range def.States {
		stateLocation := fmt.Sprintf("%s.States[%q]", location, name)
		if err := sfnValidateState(def, state, stateLocation, queryLanguage); err != nil {
			return err
		}
	}
	reachable := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if reachable[name] {
			return
		}
		state, ok := def.States[name]
		if !ok {
			return
		}
		reachable[name] = true
		visit(state.Next)
		visit(state.Default)
		for _, choice := range state.Choices {
			visit(choice.Next)
		}
		for _, catcher := range state.Catch {
			visit(catcher.Next)
		}
	}
	visit(def.StartAt)
	for name := range def.States {
		if !reachable[name] {
			return fmt.Errorf("%s.States[%q] is not reachable", location, name)
		}
	}
	return nil
}

func sfnRunExecution(execARN, definition, input string, cancel <-chan struct{}) {
	defer sfnCancels.Delete(execARN)

	output, status, err := sfnExecuteRecorded(execARN, definition, input, cancel)
	if errors.Is(err, errSFNAborted) {
		return
	}
	if err != nil {
		status = "FAILED"
		output = ""
	}
	sfnCompleteExecution(execARN, status, output, err)
}

func recoverStepFunctionsExecutions() {
	for _, execution := range sfnExecutions.List() {
		if execution.Status != "RUNNING" {
			continue
		}
		definition := execution.DefinitionSnapshot
		input := execution.Input
		if execution.RedriveInput != "" {
			input = execution.RedriveInput
		}
		if execution.RedriveState != "" {
			var snapshot sfnDefinition
			if err := json.Unmarshal([]byte(definition), &snapshot); err != nil {
				sfnCompleteExecution(execution.ExecutionArn, "FAILED", "", &sfnExecutionError{
					Name:  "States.Runtime",
					Cause: "persisted state machine definition is invalid: " + err.Error(),
				})
				continue
			}
			snapshot.StartAt = execution.RedriveState
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				sfnCompleteExecution(execution.ExecutionArn, "FAILED", "", &sfnExecutionError{
					Name:  "States.Runtime",
					Cause: "persisted state machine definition could not be restored: " + err.Error(),
				})
				continue
			}
			definition = string(encoded)
		}
		cancel := make(chan struct{})
		if _, alreadyRunning := sfnCancels.LoadOrStore(execution.ExecutionArn, cancel); alreadyRunning {
			continue
		}
		go sfnRunExecution(execution.ExecutionArn, definition, input, cancel)
	}
}

func sfnExecute(definition, input string, cancel <-chan struct{}) (string, string, error) {
	return sfnExecuteWithVariables(definition, input, cancel, nil)
}

func sfnExecuteWithVariables(definition, input string, cancel <-chan struct{}, variables map[string]any) (string, string, error) {
	var def sfnDefinition
	if err := json.Unmarshal([]byte(definition), &def); err != nil {
		return "", "FAILED", err
	}
	return sfnRunTopLevelDefinition(def, input, cancel, variables, "")
}

func sfnExecuteRecorded(executionARN, definition, input string, cancel <-chan struct{}) (string, string, error) {
	var def sfnDefinition
	if err := json.Unmarshal([]byte(definition), &def); err != nil {
		return "", "FAILED", err
	}
	return sfnRunTopLevelDefinition(def, input, cancel, nil, executionARN)
}

func sfnRunTopLevelDefinition(def sfnDefinition, input string, cancel <-chan struct{}, variables map[string]any, executionARN string) (string, string, error) {
	if def.TimeoutSeconds == nil {
		return sfnRunDefDepthRuntime(def, input, cancel, 0, variables, executionARN)
	}
	timer := time.NewTimer(time.Duration(*def.TimeoutSeconds) * time.Second)
	defer timer.Stop()
	combined := make(chan struct{})
	timeoutFired := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	simGo(func() {
		select {
		case <-cancel:
			close(combined)
		case <-timer.C:
			close(timeoutFired)
			close(combined)
		case <-done:
		}
	})
	output, status, err := sfnRunDefDepthRuntime(def, input, combined, 0, variables, executionARN)
	select {
	case <-timeoutFired:
		return "", "TIMED_OUT", &sfnExecutionError{Name: "States.Timeout", Cause: "Execution timed out"}
	default:
		return output, status, err
	}
}

// sfnMaxNestingDepth bounds Parallel/Map branch recursion so a pathologically
// nested definition can't overflow the goroutine stack and crash the process.
// AWS's own ASL nesting limit is far below this.
const sfnMaxNestingDepth = 200

func sfnRunDefDepthWithVariables(def sfnDefinition, input string, cancel <-chan struct{}, depth int, inheritedVariables map[string]any) (string, string, error) {
	return sfnRunDefDepthRuntime(def, input, cancel, depth, inheritedVariables, "")
}

func sfnRunDefDepthRuntime(def sfnDefinition, input string, cancel <-chan struct{}, depth int, inheritedVariables map[string]any, executionARN string) (string, string, error) {
	if depth > sfnMaxNestingDepth {
		return "", "FAILED", fmt.Errorf("state machine nesting depth exceeded %d", sfnMaxNestingDepth)
	}
	data, err := sfnDecodeJSON(input)
	if err != nil {
		return "", "FAILED", &sfnExecutionError{Name: "States.Runtime", Cause: "execution input is not valid JSON"}
	}
	executionInput, err := sfnCloneJSON(data)
	if err != nil {
		return "", "FAILED", err
	}
	variables := sfnCloneVariables(inheritedVariables)
	executionStart := time.Now().UTC().Format(time.RFC3339Nano)
	current := def.StartAt
	transitionCount := 0
	for {
		transitionCount++
		if transitionCount > 25_000 {
			return "", "FAILED", &sfnExecutionError{Name: "States.Runtime", Cause: "execution exceeded the 25,000-event history limit"}
		}
		select {
		case <-cancel:
			return "", "ABORTED", errSFNAborted
		default:
		}
		state, ok := def.States[current]
		if !ok {
			return "", "FAILED", &sfnExecutionError{Name: "States.Runtime", Cause: fmt.Sprintf("state %q does not exist", current)}
		}
		sfnAppendHistory(executionARN, state.Type+"StateEntered", sfnStateHistoryDetails(current, data, "input"))
		context := map[string]any{
			"Execution": map[string]any{
				"Id":        executionARN,
				"Input":     executionInput,
				"StartTime": executionStart,
			},
			"State": map[string]any{
				"Name":        current,
				"EnteredTime": time.Now().UTC().Format(time.RFC3339Nano),
				"RetryCount":  0,
			},
		}
		var taskToken string
		if state.Type == "Task" && strings.HasSuffix(state.Resource, ".waitForTaskToken") {
			taskToken = uuid.NewString()
			context["Task"] = map[string]any{"Token": taskToken}
			sfnActivityTasks.Put(taskToken, SFNActivityTask{
				TaskToken: taskToken,
				Status:    "RUNNING",
			})
			defer sfnActivityTasks.Delete(taskToken)
		}
		queryLanguage := state.QueryLanguage
		if queryLanguage == "" {
			queryLanguage = def.QueryLanguage
		}
		if queryLanguage == "" {
			queryLanguage = "JSONPath"
		}
		effectiveInput := data
		if queryLanguage == "JSONata" {
			if state.Arguments != nil {
				effectiveInput, err = sfnResolveJSONataValue(state.Arguments, data, nil, nil, context, variables)
			}
		} else {
			effectiveInput, err = sfnApplyInputPath(data, context, state.InputPath)
			if err == nil && state.Parameters != nil {
				effectiveInput, err = sfnResolvePayload(state.Parameters, effectiveInput, context)
			}
		}
		if err != nil {
			return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
		}
		if depth == 0 && executionARN != "" {
			if encodedInput, encodeErr := sfnEncodeJSON(data); encodeErr == nil {
				sfnExecutions.Update(executionARN, func(execution *SFNExecution) {
					if execution.RedriveState != current || execution.RedriveInput != encodedInput || execution.CheckpointEnteredDate == 0 {
						execution.CheckpointEnteredDate = sfnEpochNow()
					}
					if execution.RedriveState != current {
						execution.TaskCheckpoint = nil
					}
					execution.RedriveState = current
					execution.RedriveInput = encodedInput
				})
			}
		}

		var (
			result       any
			executionErr *sfnExecutionError
			transition   string
			terminal     bool
		)
		switch state.Type {
		case "Pass":
			if queryLanguage == "JSONata" {
				result = data
			} else if state.Result != nil {
				result, err = sfnDecodeJSON(string(*state.Result))
				if err != nil {
					executionErr = &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
				}
			} else {
				result = effectiveInput
			}
			transition, terminal = state.Next, state.End
		case "Succeed":
			outputValue := data
			if queryLanguage == "JSONata" && len(state.Output) > 0 {
				var rawOutput any
				if err := json.Unmarshal(state.Output, &rawOutput); err != nil {
					return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
				}
				outputValue, err = sfnResolveJSONataValue(rawOutput, data, nil, nil, context, variables)
				if err != nil {
					return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
				}
			}
			sfnAppendHistory(executionARN, "SucceedStateExited", sfnStateHistoryDetails(current, outputValue, "output"))
			output, encodeErr := sfnEncodeJSON(outputValue)
			return output, "SUCCEEDED", encodeErr
		case "Fail":
			errorName := state.Error
			if queryLanguage == "JSONata" {
				errorName, err = sfnResolveJSONataString(state.Error, data, nil, nil, context, variables)
			} else if state.ErrorPath != "" {
				if selected, exists := sfnPathValue(data, context, state.ErrorPath); exists {
					errorName, _ = selected.(string)
				}
			}
			if errorName == "" {
				errorName = "States.Failed"
			}
			cause := state.Cause
			if queryLanguage == "JSONata" {
				cause, err = sfnResolveJSONataString(state.Cause, data, nil, nil, context, variables)
			} else if state.CausePath != "" {
				if selected, exists := sfnPathValue(data, context, state.CausePath); exists {
					cause, _ = selected.(string)
				}
			}
			if err != nil {
				return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
			}
			return "", "FAILED", &sfnExecutionError{Name: errorName, Cause: cause}
		case "Wait":
			wait, waitErr := sfnWaitDuration(state, effectiveInput, context)
			if waitErr != nil {
				executionErr = waitErr
				break
			}
			if depth == 0 && executionARN != "" {
				if execution, exists := sfnExecutions.Get(executionARN); exists &&
					execution.RedriveState == current && execution.CheckpointEnteredDate > 0 {
					enteredAt := time.Unix(0, int64(execution.CheckpointEnteredDate*float64(time.Second)))
					if elapsed := time.Since(enteredAt); elapsed >= wait {
						wait = 0
					} else if elapsed > 0 {
						wait -= elapsed
					}
				}
			}
			timer := time.NewTimer(wait)
			select {
			case <-cancel:
				timer.Stop()
				return "", "ABORTED", errSFNAborted
			case <-timer.C:
			}
			result = effectiveInput
			transition, terminal = state.Next, state.End
		case "Task":
			result, executionErr = sfnRunTaskWithRetry(state, effectiveInput, context, cancel, executionARN)
			transition, terminal = state.Next, state.End
		case "Choice":
			transition = state.Default
			var matchedRule *sfnChoiceRule
			for index := range state.Choices {
				rule := &state.Choices[index]
				var matched bool
				if queryLanguage == "JSONata" {
					matched, err = sfnEvalJSONataCondition(rule.Condition, data, context, variables)
					if err != nil {
						executionErr = &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
						break
					}
				} else {
					matched = sfnEvalChoiceValue(*rule, effectiveInput, context)
				}
				if matched {
					transition = rule.Next
					matchedRule = rule
					break
				}
			}
			if executionErr != nil {
				break
			}
			if transition == "" {
				executionErr = &sfnExecutionError{
					Name:  "States.NoChoiceMatched",
					Cause: fmt.Sprintf("Choice state %q matched no rule and has no Default", current),
				}
				break
			}
			if queryLanguage == "JSONata" {
				if matchedRule != nil {
					if err := sfnApplyJSONataAssignments(matchedRule.Assign, data, nil, nil, context, variables); err != nil {
						return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
					}
					if len(matchedRule.Output) > 0 {
						var rawOutput any
						if err := json.Unmarshal(matchedRule.Output, &rawOutput); err != nil {
							return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
						}
						data, err = sfnResolveJSONataValue(rawOutput, data, nil, nil, context, variables)
					}
				}
				if err == nil {
					err = sfnApplyJSONataAssignments(state.Assign, data, nil, nil, context, variables)
				}
			} else {
				data, err = sfnApplyOutputPath(effectiveInput, context, state.OutputPath)
			}
			if err != nil {
				return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
			}
			sfnAppendHistory(executionARN, "ChoiceStateExited", sfnStateHistoryDetails(current, data, "output"))
			current = transition
			continue
		case "Parallel":
			result, executionErr = sfnRunParallel(state, effectiveInput, cancel, depth, variables, executionARN)
			transition, terminal = state.Next, state.End
		case "Map":
			result, executionErr = sfnRunMap(state, current, effectiveInput, context, cancel, depth, variables, executionARN)
			transition, terminal = state.Next, state.End
		default:
			executionErr = &sfnExecutionError{Name: "States.Runtime", Cause: fmt.Sprintf("unsupported state type %q", state.Type)}
		}

		if executionErr != nil {
			caught := false
			for _, catcher := range state.Catch {
				if !sfnErrorMatches(catcher.ErrorEquals, executionErr.Name) {
					continue
				}
				errorOutput := map[string]any{"Error": executionErr.Name, "Cause": executionErr.Cause}
				if queryLanguage == "JSONata" {
					err = sfnApplyJSONataAssignments(catcher.Assign, data, nil, errorOutput, context, variables)
					if err == nil && len(catcher.Output) > 0 {
						var rawOutput any
						if err = json.Unmarshal(catcher.Output, &rawOutput); err == nil {
							data, err = sfnResolveJSONataValue(rawOutput, data, nil, errorOutput, context, variables)
						}
					}
				} else {
					data, err = sfnSetResultPath(data, errorOutput, catcher.ResultPath)
				}
				if err != nil {
					return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
				}
				current = catcher.Next
				caught = true
				break
			}
			if caught {
				continue
			}
			return "", "FAILED", executionErr
		}

		if queryLanguage == "JSONata" {
			outputValue := result
			if len(state.Output) > 0 {
				var rawOutput any
				if err := json.Unmarshal(state.Output, &rawOutput); err != nil {
					return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
				}
				outputValue, err = sfnResolveJSONataValue(rawOutput, data, result, nil, context, variables)
			}
			if err == nil {
				err = sfnApplyJSONataAssignments(state.Assign, data, result, nil, context, variables)
			}
			if err != nil {
				return "", "FAILED", &sfnExecutionError{Name: "States.QueryEvaluationError", Cause: err.Error()}
			}
			data = outputValue
		} else {
			if state.ResultSelector != nil {
				result, err = sfnResolvePayload(state.ResultSelector, result, context)
				if err != nil {
					return "", "FAILED", &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
				}
			}
			data, err = sfnSetResultPath(data, result, state.ResultPath)
			if err != nil {
				return "", "FAILED", &sfnExecutionError{Name: "States.ResultPathMatchFailure", Cause: err.Error()}
			}
			data, err = sfnApplyOutputPath(data, context, state.OutputPath)
			if err != nil {
				return "", "FAILED", &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
			}
		}
		sfnAppendHistory(executionARN, state.Type+"StateExited", sfnStateHistoryDetails(current, data, "output"))
		if terminal {
			output, encodeErr := sfnEncodeJSON(data)
			return output, "SUCCEEDED", encodeErr
		}
		if transition == "" {
			return "", "FAILED", &sfnExecutionError{Name: "States.Runtime", Cause: fmt.Sprintf("%s state %q must declare End or Next", state.Type, current)}
		}
		current = transition
	}
}

func sfnLambdaNameFromResource(resource string) (string, bool) {
	if i := strings.Index(resource, ":function:"); i >= 0 {
		name := resource[i+len(":function:"):]
		if j := strings.IndexByte(name, ':'); j >= 0 { // strip a :version/:alias suffix
			name = name[:j]
		}
		return name, name != ""
	}
	return "", false
}

func sfnAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func sfnCompleteExecution(execARN, status, output string, runErr error) {
	sfnMu.Lock()
	defer sfnMu.Unlock()

	exec, ok := sfnExecutions.Get(execARN)
	if !ok || exec.Status != "RUNNING" {
		return
	}
	now := sfnEpochNow()
	exec.Status = status
	exec.StopDate = &now
	exec.Output = output
	if runErr != nil {
		executionErr := sfnAsExecutionError(runErr)
		exec.Error = executionErr.Name
		exec.Cause = executionErr.Cause
	}
	sfnExecutions.Put(execARN, exec)
	switch status {
	case "SUCCEEDED":
		sfnAppendHistory(execARN, "ExecutionSucceeded", map[string]any{
			"output":        output,
			"outputDetails": map[string]any{"truncated": false},
		})
	case "FAILED":
		sfnAppendHistory(execARN, "ExecutionFailed", map[string]any{
			"error": exec.Error,
			"cause": exec.Cause,
		})
	}
}

func handleSFNDescribeExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionArn string `json:"executionArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	exec, ok := sfnExecutions.Get(req.ExecutionArn)
	if !ok {
		sfnWriteError(w, "ExecutionDoesNotExist", "Execution does not exist: "+req.ExecutionArn)
		return
	}
	if exec.TypeSnapshot == "EXPRESS" && exec.MapRunArn == "" {
		sfnWriteError(w, "ExecutionDoesNotExist", "DescribeExecution is not supported for EXPRESS executions")
		return
	}
	response := map[string]any{
		"executionArn":    exec.ExecutionArn,
		"stateMachineArn": exec.StateMachineArn,
		"name":            exec.Name,
		"status":          exec.Status,
		"startDate":       exec.StartDate,
		"input":           exec.Input,
		"inputDetails":    map[string]any{"included": true},
		"redriveCount":    exec.RedriveCount,
	}
	if exec.StopDate != nil {
		response["stopDate"] = *exec.StopDate
	}
	if exec.Status == "SUCCEEDED" {
		response["output"] = exec.Output
		response["outputDetails"] = map[string]any{"included": true}
	}
	if exec.Error != "" {
		response["error"] = exec.Error
	}
	if exec.Cause != "" {
		response["cause"] = exec.Cause
	}
	if exec.RedriveDate != nil {
		response["redriveDate"] = *exec.RedriveDate
	}
	if exec.StateMachineAliasArn != "" {
		response["stateMachineAliasArn"] = exec.StateMachineAliasArn
	}
	if exec.StateMachineVersionArn != "" {
		response["stateMachineVersionArn"] = exec.StateMachineVersionArn
	}
	if exec.MapRunArn != "" {
		response["mapRunArn"] = exec.MapRunArn
	}
	if exec.TraceHeader != "" {
		response["traceHeader"] = exec.TraceHeader
	}
	switch exec.Status {
	case "FAILED", "TIMED_OUT", "ABORTED":
		response["redriveStatus"] = "REDRIVABLE"
	case "RUNNING":
		response["redriveStatus"] = "NOT_REDRIVABLE"
		response["redriveStatusReason"] = "Execution is RUNNING and cannot be redriven"
	case "SUCCEEDED":
		response["redriveStatus"] = "NOT_REDRIVABLE"
		response["redriveStatusReason"] = "Execution is SUCCEEDED and cannot be redriven"
	}
	sfnWriteJSON(w, http.StatusOK, response)
}

func handleSFNGetExecutionHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionArn         string `json:"executionArn"`
		IncludeExecutionData *bool  `json:"includeExecutionData"`
		MaxResults           *int   `json:"maxResults"`
		NextToken            string `json:"nextToken"`
		ReverseOrder         bool   `json:"reverseOrder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	_, ok := sfnExecutions.Get(req.ExecutionArn)
	if !ok {
		sfnWriteError(w, "ExecutionDoesNotExist", "Execution does not exist: "+req.ExecutionArn)
		return
	}
	execution, _ := sfnExecutions.Get(req.ExecutionArn)
	if execution.TypeSnapshot == "EXPRESS" {
		sfnWriteError(w, "ExecutionDoesNotExist", "GetExecutionHistory is not supported for EXPRESS executions")
		return
	}
	events, _ := sfnHistories.Get(req.ExecutionArn)
	if req.ReverseOrder {
		reversed := make([]sfnHistoryEvent, len(events))
		for index := range events {
			reversed[index] = events[len(events)-1-index]
		}
		events = reversed
	}
	if req.IncludeExecutionData != nil && !*req.IncludeExecutionData {
		events = sfnHistoryWithoutExecutionData(events)
	}
	maxResults := 0
	if req.MaxResults != nil {
		maxResults = *req.MaxResults
	}
	page, nextToken := awsPage(events, req.NextToken, maxResults, 100)
	response := map[string]any{"events": page}
	if nextToken != "" {
		response["nextToken"] = nextToken
	}
	sfnWriteJSON(w, http.StatusOK, response)
}

func handleSFNListExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		MapRunArn       string `json:"mapRunArn"`
		StatusFilter    string `json:"statusFilter"`
		RedriveFilter   string `json:"redriveFilter"`
		MaxResults      *int   `json:"maxResults"`
		NextToken       string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if (req.StateMachineArn == "") == (req.MapRunArn == "") {
		sfnWriteError(w, "ValidationException", "exactly one of stateMachineArn or mapRunArn is required")
		return
	}

	all := sfnExecutions.List()
	var filtered []SFNExecution
	for _, e := range all {
		if req.StateMachineArn != "" {
			switch {
			case e.StateMachineAliasArn != "" && req.StateMachineArn == e.StateMachineAliasArn:
			case e.StateMachineVersionArn != "" && req.StateMachineArn == e.StateMachineVersionArn:
			case e.StateMachineArn == req.StateMachineArn:
			default:
				continue
			}
		}
		if req.MapRunArn != "" && e.MapRunArn != req.MapRunArn {
			continue
		}
		if req.StatusFilter != "" && e.Status != req.StatusFilter {
			continue
		}
		if req.RedriveFilter == "REDRIVEN" && e.RedriveCount == 0 {
			continue
		}
		if req.RedriveFilter == "NOT_REDRIVEN" && e.RedriveCount != 0 {
			continue
		}
		filtered = append(filtered, e)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].StartDate > filtered[j].StartDate })

	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(filtered, req.NextToken, maxR, 100)

	items := make([]map[string]any, 0, len(page))
	for _, e := range page {
		item := map[string]any{
			"executionArn":    e.ExecutionArn,
			"stateMachineArn": e.StateMachineArn,
			"name":            e.Name,
			"status":          e.Status,
			"startDate":       e.StartDate,
		}
		if e.StopDate != nil {
			item["stopDate"] = *e.StopDate
		}
		if e.MapRunArn != "" {
			item["mapRunArn"] = e.MapRunArn
		}
		if e.StateMachineAliasArn != "" {
			item["stateMachineAliasArn"] = e.StateMachineAliasArn
		}
		if e.StateMachineVersionArn != "" {
			item["stateMachineVersionArn"] = e.StateMachineVersionArn
		}
		item["redriveCount"] = e.RedriveCount
		if e.RedriveDate != nil {
			item["redriveDate"] = *e.RedriveDate
		}
		items = append(items, item)
	}
	resp := map[string]any{"executions": items}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func handleSFNStopExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionArn string `json:"executionArn"`
		Error        string `json:"error"`
		Cause        string `json:"cause"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	exec, ok := sfnExecutions.Get(req.ExecutionArn)
	if !ok {
		sfnWriteError(w, "ExecutionDoesNotExist", "Execution does not exist: "+req.ExecutionArn)
		return
	}
	if exec.Status != "RUNNING" {
		sfnWriteError(w, "ExecutionNotRunning", "Execution is not running: "+req.ExecutionArn)
		return
	}
	now := sfnEpochNow()
	exec.Status = "ABORTED"
	exec.StopDate = &now
	exec.Error = req.Error
	exec.Cause = req.Cause
	sfnExecutions.Put(req.ExecutionArn, exec)
	sfnAppendHistory(req.ExecutionArn, "ExecutionAborted", map[string]any{
		"error": req.Error,
		"cause": req.Cause,
	})
	if cancelAny, ok := sfnCancels.LoadAndDelete(req.ExecutionArn); ok {
		if cancel, ok := cancelAny.(chan struct{}); ok {
			close(cancel)
		}
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{"stopDate": now})
}

func sfnNameFromARN(arn string) string {
	// arn:aws:states:us-east-1:123456789012:stateMachine:name
	// arn:aws:states:us-east-1:123456789012:execution:name:execName
	parts := strings.Split(arn, ":")
	if len(parts) >= 7 {
		return parts[6]
	}
	return arn
}

func sfnTagsToMap(tags []SFNTag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

func sfnMapToTags(m map[string]string) []SFNTag {
	tags := make([]SFNTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, SFNTag{Key: k, Value: v})
	}
	return tags
}

func sfnMergeTags(current, additions []SFNTag) []SFNTag {
	tagMap := sfnTagsToMap(current)
	for _, tag := range additions {
		tagMap[tag.Key] = tag.Value
	}
	return sfnMapToTags(tagMap)
}

func sfnRemoveTags(current []SFNTag, keys []string) []SFNTag {
	tagMap := sfnTagsToMap(current)
	for _, key := range keys {
		delete(tagMap, key)
	}
	return sfnMapToTags(tagMap)
}

// ── Activities ────────────────────────────────────────────────────────────
//
// An activity is a named task-poll resource: a worker polls GetActivityTask
// for work and reports back via SendTaskSuccess/SendTaskFailure/
// SendTaskHeartbeat. A Task state with `Resource: arn:...:activity:NAME`
// schedules a task token onto the activity's queue; the worker drains it.

type SFNActivity struct {
	ActivityArn  string   `json:"activityArn"`
	Name         string   `json:"name"`
	CreationDate float64  `json:"creationDate"`
	Tags         []SFNTag `json:"tags,omitempty"`
}

// SFNActivityTask is one scheduled-but-not-yet-completed activity task,
// keyed by its opaque task token.
type SFNActivityTask struct {
	TaskToken   string  `json:"taskToken"`
	ActivityArn string  `json:"activityArn"`
	Input       string  `json:"input"`
	Status      string  `json:"status"` // SCHEDULED, RUNNING, SUCCEEDED, FAILED
	Output      string  `json:"output"`
	Error       string  `json:"error"`
	Cause       string  `json:"cause"`
	LastHB      float64 `json:"lastHeartbeat"`
}

var (
	sfnActivities    sim.Store[SFNActivity]
	sfnActivityTasks sim.Store[SFNActivityTask]
)

func handleSFNCreateActivity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string   `json:"name"`
		Tags []SFNTag `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if req.Name == "" {
		sfnWriteError(w, "InvalidName", "name is required")
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	arn := sfnARN("activity:" + req.Name)
	// CreateActivity is idempotent on (name) — re-creating returns the
	// existing ARN and original creation date.
	if existing, ok := sfnActivities.Get(req.Name); ok {
		sfnWriteJSON(w, http.StatusOK, map[string]any{
			"activityArn":  existing.ActivityArn,
			"creationDate": existing.CreationDate,
		})
		return
	}
	act := SFNActivity{
		ActivityArn:  arn,
		Name:         req.Name,
		CreationDate: sfnEpochNow(),
		Tags:         req.Tags,
	}
	sfnActivities.Put(req.Name, act)
	sfnWriteJSON(w, http.StatusOK, map[string]any{
		"activityArn":  arn,
		"creationDate": act.CreationDate,
	})
}

func handleSFNDeleteActivity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ActivityArn string `json:"activityArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()
	sfnActivities.Delete(sfnNameFromARN(req.ActivityArn))
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSFNDescribeActivity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ActivityArn string `json:"activityArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	act, ok := sfnActivities.Get(sfnNameFromARN(req.ActivityArn))
	if !ok {
		sfnWriteError(w, "ActivityDoesNotExist", "Activity does not exist: "+req.ActivityArn)
		return
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{
		"activityArn":  act.ActivityArn,
		"name":         act.Name,
		"creationDate": act.CreationDate,
	})
}

func handleSFNListActivities(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults *int   `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := sfnActivities.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	items := make([]map[string]any, 0, len(page))
	for _, a := range page {
		items = append(items, map[string]any{
			"activityArn":  a.ActivityArn,
			"name":         a.Name,
			"creationDate": a.CreationDate,
		})
	}
	resp := map[string]any{"activities": items}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func handleSFNGetActivityTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ActivityArn string `json:"activityArn"`
		WorkerName  string `json:"workerName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if _, ok := sfnActivities.Get(sfnNameFromARN(req.ActivityArn)); !ok {
		sfnWriteError(w, "ActivityDoesNotExist", "Activity does not exist: "+req.ActivityArn)
		return
	}

	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		sfnMu.Lock()
		for _, task := range sfnActivityTasks.List() {
			if task.ActivityArn == req.ActivityArn && task.Status == "SCHEDULED" {
				task.Status = "RUNNING"
				task.LastHB = sfnEpochNow()
				sfnActivityTasks.Put(task.TaskToken, task)
				sfnMu.Unlock()
				sfnWriteJSON(w, http.StatusOK, map[string]any{
					"taskToken": task.TaskToken,
					"input":     task.Input,
				})
				return
			}
		}
		sfnMu.Unlock()
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			sfnWriteJSON(w, http.StatusOK, map[string]any{})
			return
		case <-ticker.C:
		}
	}
}

func handleSFNSendTaskSuccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskToken string `json:"taskToken"`
		Output    string `json:"output"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()
	task, ok := sfnActivityTasks.Get(req.TaskToken)
	if !ok {
		sfnWriteError(w, "TaskDoesNotExist", "Task Token does not exist")
		return
	}
	if task.Status != "RUNNING" {
		sfnWriteError(w, "TaskTimedOut", "Task Timed Out")
		return
	}
	task.Status = "SUCCEEDED"
	task.Output = req.Output
	sfnActivityTasks.Put(req.TaskToken, task)
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSFNSendTaskFailure(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskToken string `json:"taskToken"`
		Error     string `json:"error"`
		Cause     string `json:"cause"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()
	task, ok := sfnActivityTasks.Get(req.TaskToken)
	if !ok {
		sfnWriteError(w, "TaskDoesNotExist", "Task Token does not exist")
		return
	}
	task.Status = "FAILED"
	task.Error = req.Error
	task.Cause = req.Cause
	sfnActivityTasks.Put(req.TaskToken, task)
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSFNSendTaskHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskToken string `json:"taskToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()
	task, ok := sfnActivityTasks.Get(req.TaskToken)
	if !ok {
		sfnWriteError(w, "TaskDoesNotExist", "Task Token does not exist")
		return
	}
	if task.Status != "RUNNING" {
		sfnWriteError(w, "TaskTimedOut", "Task Timed Out")
		return
	}
	task.LastHB = sfnEpochNow()
	sfnActivityTasks.Put(req.TaskToken, task)
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

// ── State machine versions + aliases ──────────────────────────────────────
//
// PublishStateMachineVersion snapshots the current definition+role into a
// numbered, immutable version (arn:...:stateMachine:Name:N). An alias is a
// named pointer (arn:...:stateMachine:Name:aliasName) whose
// routingConfiguration weights traffic across one or two versions.

type SFNStateMachineVersion struct {
	StateMachineVersionArn  string         `json:"stateMachineVersionArn"`
	StateMachineName        string         `json:"stateMachineName"`
	Version                 int            `json:"version"`
	Definition              string         `json:"definition"`
	RoleArn                 string         `json:"roleArn"`
	Type                    string         `json:"type"`
	Description             string         `json:"description"`
	CreationDate            float64        `json:"creationDate"`
	RevisionId              string         `json:"revisionId"`
	UpdateDate              float64        `json:"updateDate"`
	LoggingConfiguration    map[string]any `json:"loggingConfiguration,omitempty"`
	TracingConfiguration    map[string]any `json:"tracingConfiguration,omitempty"`
	EncryptionConfiguration map[string]any `json:"encryptionConfiguration,omitempty"`
}

type SFNRoutingConfig struct {
	StateMachineVersionArn string `json:"stateMachineVersionArn"`
	Weight                 int    `json:"weight"`
}

type SFNStateMachineAlias struct {
	StateMachineAliasArn string             `json:"stateMachineAliasArn"`
	Name                 string             `json:"name"`
	Description          string             `json:"description"`
	RoutingConfiguration []SFNRoutingConfig `json:"routingConfiguration"`
	CreationDate         float64            `json:"creationDate"`
	UpdateDate           float64            `json:"updateDate"`
}

var (
	sfnVersions         sim.Store[SFNStateMachineVersion]
	sfnAliases          sim.Store[SFNStateMachineAlias]
	sfnAliasNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	sfnAliasNonNumeric  = regexp.MustCompile(`[A-Za-z_.-]`)
)

// sfnNextVersionNumber returns 1 + the highest existing version number for a
// state machine.
func sfnNextVersionNumber(smName string) int {
	max := 0
	for _, v := range sfnVersions.List() {
		if v.StateMachineName == smName && v.Version > max {
			max = v.Version
		}
	}
	return max + 1
}

func sfnPublishVersion(sm SFNStateMachine, description string) SFNStateMachineVersion {
	number := sfnNextVersionNumber(sm.Name)
	version := SFNStateMachineVersion{
		StateMachineVersionArn:  fmt.Sprintf("%s:%d", sm.StateMachineArn, number),
		StateMachineName:        sm.Name,
		Version:                 number,
		Definition:              sm.Definition,
		RoleArn:                 sm.RoleArn,
		Type:                    sm.Type,
		Description:             description,
		CreationDate:            sfnEpochNow(),
		RevisionId:              sm.RevisionId,
		UpdateDate:              sm.UpdateDate,
		LoggingConfiguration:    sm.LoggingConfiguration,
		TracingConfiguration:    sm.TracingConfiguration,
		EncryptionConfiguration: sm.EncryptionConfiguration,
	}
	sfnVersions.Put(version.StateMachineVersionArn, version)
	return version
}

func handleSFNPublishStateMachineVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		Description     string `json:"description"`
		RevisionId      string `json:"revisionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()

	smName := sfnNameFromARN(req.StateMachineArn)
	sm, ok := sfnStateMachines.Get(smName)
	if !ok {
		sfnWriteError(w, "StateMachineDoesNotExist", "State machine does not exist: "+req.StateMachineArn)
		return
	}
	ver := sfnPublishVersion(sm, req.Description)
	sfnWriteJSON(w, http.StatusOK, map[string]any{
		"stateMachineVersionArn": ver.StateMachineVersionArn,
		"creationDate":           ver.CreationDate,
	})
}

func handleSFNDeleteStateMachineVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineVersionArn string `json:"stateMachineVersionArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()
	if _, ok := sfnVersions.Get(req.StateMachineVersionArn); !ok {
		sfnWriteError(w, "ValidationException", "State machine version does not exist: "+req.StateMachineVersionArn)
		return
	}
	for _, alias := range sfnAliases.List() {
		for _, route := range alias.RoutingConfiguration {
			if route.StateMachineVersionArn == req.StateMachineVersionArn {
				sfnWriteErrorStatus(
					w,
					http.StatusConflict,
					"ConflictException",
					"State machine version is referenced by alias: "+alias.StateMachineAliasArn,
				)
				return
			}
		}
	}
	sfnVersions.Delete(req.StateMachineVersionArn)
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

// sfnAliasKey is the store key for an alias: the state-machine name joined
// with the alias name, so aliases are unique per state machine.
func sfnAliasKey(smName, aliasName string) string {
	return smName + "/" + aliasName
}

// sfnSMNameFromVersionArn pulls the state-machine name out of a version ARN
// (arn:...:stateMachine:Name:N).
func sfnSMNameFromVersionArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 7 {
		return parts[6]
	}
	return arn
}

func handleSFNCreateStateMachineAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                 string             `json:"name"`
		Description          string             `json:"description"`
		RoutingConfiguration []SFNRoutingConfig `json:"routingConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if !sfnAliasNamePattern.MatchString(req.Name) || !sfnAliasNonNumeric.MatchString(req.Name) || len(req.Name) > 80 {
		sfnWriteError(w, "InvalidName", "name is required")
		return
	}

	sfnMu.Lock()
	defer sfnMu.Unlock()

	smName, validationErr := sfnValidateAliasRouting(req.RoutingConfiguration)
	if validationErr != "" {
		sfnWriteError(w, "ValidationException", validationErr)
		return
	}
	if existing, ok := sfnAliases.Get(sfnAliasKey(smName, req.Name)); ok {
		if existing.Description == req.Description && sfnRoutingEqual(existing.RoutingConfiguration, req.RoutingConfiguration) {
			sfnWriteJSON(w, http.StatusOK, map[string]any{
				"stateMachineAliasArn": existing.StateMachineAliasArn,
				"creationDate":         existing.CreationDate,
			})
			return
		}
		sfnWriteErrorStatus(w, http.StatusConflict, "ConflictException", "State machine alias already exists: "+existing.StateMachineAliasArn)
		return
	}
	aliasArn := fmt.Sprintf("%s:%s", sfnARN("stateMachine:"+smName), req.Name)
	now := sfnEpochNow()
	alias := SFNStateMachineAlias{
		StateMachineAliasArn: aliasArn,
		Name:                 req.Name,
		Description:          req.Description,
		RoutingConfiguration: req.RoutingConfiguration,
		CreationDate:         now,
		UpdateDate:           now,
	}
	sfnAliases.Put(sfnAliasKey(smName, req.Name), alias)
	sfnWriteJSON(w, http.StatusOK, map[string]any{
		"stateMachineAliasArn": aliasArn,
		"creationDate":         now,
	})
}

// sfnAliasByArn resolves an alias by its full ARN
// (arn:...:stateMachine:Name:aliasName).
func sfnAliasByArn(arn string) (SFNStateMachineAlias, bool) {
	for _, a := range sfnAliases.List() {
		if a.StateMachineAliasArn == arn {
			return a, true
		}
	}
	return SFNStateMachineAlias{}, false
}

func handleSFNDescribeStateMachineAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineAliasArn string `json:"stateMachineAliasArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	alias, ok := sfnAliasByArn(req.StateMachineAliasArn)
	if !ok {
		sfnWriteError(w, "ResourceNotFound", "State machine alias does not exist: "+req.StateMachineAliasArn)
		return
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{
		"stateMachineAliasArn": alias.StateMachineAliasArn,
		"name":                 alias.Name,
		"description":          alias.Description,
		"routingConfiguration": alias.RoutingConfiguration,
		"creationDate":         alias.CreationDate,
		"updateDate":           alias.UpdateDate,
	})
}

func handleSFNListStateMachineAliases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		MaxResults      *int   `json:"maxResults"`
		NextToken       string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	smName := sfnNameFromARN(req.StateMachineArn)
	if _, ok := sfnStateMachines.Get(smName); !ok {
		sfnWriteError(w, "StateMachineDoesNotExist", "State machine does not exist: "+req.StateMachineArn)
		return
	}
	var owned []SFNStateMachineAlias
	smArn := sfnARN("stateMachine:" + smName)
	for _, a := range sfnAliases.List() {
		if strings.HasPrefix(a.StateMachineAliasArn, smArn+":") {
			if _, isVersion := sfnVersions.Get(req.StateMachineArn); isVersion {
				referencesVersion := false
				for _, route := range a.RoutingConfiguration {
					if route.StateMachineVersionArn == req.StateMachineArn {
						referencesVersion = true
						break
					}
				}
				if !referencesVersion {
					continue
				}
			}
			owned = append(owned, a)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].CreationDate > owned[j].CreationDate })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(owned, req.NextToken, maxR, 100)
	items := make([]map[string]any, 0, len(page))
	for _, a := range page {
		items = append(items, map[string]any{
			"stateMachineAliasArn": a.StateMachineAliasArn,
			"creationDate":         a.CreationDate,
		})
	}
	resp := map[string]any{"stateMachineAliases": items}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func handleSFNUpdateStateMachineAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineAliasArn string             `json:"stateMachineAliasArn"`
		Description          *string            `json:"description"`
		RoutingConfiguration []SFNRoutingConfig `json:"routingConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()

	alias, ok := sfnAliasByArn(req.StateMachineAliasArn)
	if !ok {
		sfnWriteError(w, "ResourceNotFound", "State machine alias does not exist: "+req.StateMachineAliasArn)
		return
	}
	if req.Description != nil {
		alias.Description = *req.Description
	}
	if len(req.RoutingConfiguration) > 0 {
		smName, validationErr := sfnValidateAliasRouting(req.RoutingConfiguration)
		if validationErr != "" {
			sfnWriteError(w, "ValidationException", validationErr)
			return
		}
		if smName != sfnSMNameFromVersionArn(alias.RoutingConfiguration[0].StateMachineVersionArn) {
			sfnWriteError(w, "ValidationException", "routingConfiguration must reference versions of the alias state machine")
			return
		}
		alias.RoutingConfiguration = req.RoutingConfiguration
	} else if req.Description == nil {
		sfnWriteError(w, "ValidationException", "description or routingConfiguration is required")
		return
	}
	alias.UpdateDate = sfnEpochNow()
	smName := sfnSMNameFromVersionArn(alias.RoutingConfiguration[0].StateMachineVersionArn)
	sfnAliases.Put(sfnAliasKey(smName, alias.Name), alias)
	sfnWriteJSON(w, http.StatusOK, map[string]any{"updateDate": alias.UpdateDate})
}

func sfnValidateAliasRouting(routes []SFNRoutingConfig) (string, string) {
	if len(routes) < 1 || len(routes) > 2 {
		return "", "routingConfiguration must contain one or two versions"
	}
	totalWeight := 0
	stateMachineName := ""
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if route.Weight < 0 || route.Weight > 100 {
			return "", "routingConfiguration weight must be between 0 and 100"
		}
		version, ok := sfnVersions.Get(route.StateMachineVersionArn)
		if !ok {
			return "", "state machine version does not exist: " + route.StateMachineVersionArn
		}
		if stateMachineName == "" {
			stateMachineName = version.StateMachineName
		} else if version.StateMachineName != stateMachineName {
			return "", "routingConfiguration versions must belong to the same state machine"
		}
		if _, duplicate := seen[route.StateMachineVersionArn]; duplicate {
			return "", "routingConfiguration cannot reference the same version twice"
		}
		seen[route.StateMachineVersionArn] = struct{}{}
		totalWeight += route.Weight
	}
	if totalWeight != 100 {
		return "", "routingConfiguration weights must total 100"
	}
	return stateMachineName, ""
}

func sfnRoutingEqual(left, right []SFNRoutingConfig) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func handleSFNDeleteStateMachineAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineAliasArn string `json:"stateMachineAliasArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()
	if alias, ok := sfnAliasByArn(req.StateMachineAliasArn); ok {
		smName := sfnSMNameFromVersionArn(alias.RoutingConfiguration[0].StateMachineVersionArn)
		sfnAliases.Delete(sfnAliasKey(smName, alias.Name))
	}
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

// ── DescribeStateMachineForExecution + RedriveExecution ───────────────────

func handleSFNDescribeStateMachineForExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionArn string `json:"executionArn"`
		IncludedData string `json:"includedData"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	exec, ok := sfnExecutions.Get(req.ExecutionArn)
	if !ok {
		sfnWriteError(w, "ExecutionDoesNotExist", "Execution does not exist: "+req.ExecutionArn)
		return
	}
	definition := exec.DefinitionSnapshot
	if req.IncludedData == "METADATA_ONLY" {
		definition = "{}"
	}
	response := map[string]any{
		"stateMachineArn": exec.StateMachineArn,
		"name":            sfnNameFromARN(exec.StateMachineArn),
		"definition":      definition,
		"roleArn":         exec.RoleArnSnapshot,
		"revisionId":      exec.RevisionIdSnapshot,
		"updateDate":      exec.UpdateDateSnapshot,
	}
	sfnAddStateMachineConfiguration(response, exec.LoggingSnapshot, exec.TracingSnapshot, exec.EncryptionSnapshot)
	if exec.MapRunArn != "" {
		response["mapRunArn"] = exec.MapRunArn
	}
	sfnWriteJSON(w, http.StatusOK, response)
}

func handleSFNRedriveExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionArn string `json:"executionArn"`
		ClientToken  string `json:"clientToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}

	sfnMu.Lock()
	exec, ok := sfnExecutions.Get(req.ExecutionArn)
	if !ok {
		sfnMu.Unlock()
		sfnWriteError(w, "ExecutionDoesNotExist", "Execution does not exist: "+req.ExecutionArn)
		return
	}
	if req.ClientToken != "" && exec.RedriveClientTokens != nil {
		if priorDate, exists := exec.RedriveClientTokens[req.ClientToken]; exists {
			sfnMu.Unlock()
			sfnWriteJSON(w, http.StatusOK, map[string]any{"redriveDate": priorDate})
			return
		}
	}
	// Redrive only applies to a terminal, non-successful execution
	// (FAILED/ABORTED/TIMED_OUT). A RUNNING or SUCCEEDED execution is not
	// redrivable.
	if exec.Status == "RUNNING" || exec.Status == "SUCCEEDED" {
		sfnMu.Unlock()
		sfnWriteError(w, "ExecutionNotRedrivable", "Execution is not redrivable: "+req.ExecutionArn)
		return
	}
	now := sfnEpochNow()
	exec.Status = "RUNNING"
	exec.StopDate = nil
	exec.Output = ""
	exec.RedriveCount++
	exec.RedriveDate = &now
	if req.ClientToken != "" {
		if exec.RedriveClientTokens == nil {
			exec.RedriveClientTokens = map[string]float64{}
		}
		exec.RedriveClientTokens[req.ClientToken] = now
	}
	sfnExecutions.Put(req.ExecutionArn, exec)
	input := exec.RedriveInput
	if input == "" {
		input = exec.Input
	}
	definition := exec.DefinitionSnapshot
	if exec.RedriveState != "" {
		var snapshot sfnDefinition
		if err := json.Unmarshal([]byte(definition), &snapshot); err != nil {
			sfnMu.Unlock()
			sfnWriteError(w, "InvalidDefinition", err.Error())
			return
		}
		snapshot.StartAt = exec.RedriveState
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			sfnMu.Unlock()
			sfnWriteError(w, "InvalidDefinition", err.Error())
			return
		}
		definition = string(encoded)
	}
	cancel := make(chan struct{})
	sfnCancels.Store(req.ExecutionArn, cancel)
	sfnMu.Unlock()

	go sfnRunExecution(req.ExecutionArn, definition, input, cancel)
	sfnWriteJSON(w, http.StatusOK, map[string]any{"redriveDate": now})
}

// ── Map Runs ──────────────────────────────────────────────────────────────
//
// A Map Run aggregates the child workflow executions a Distributed Map state
// launches. It is keyed by its mapRunArn and tied to the parent execution.

type SFNMapRun struct {
	MapRunArn                  string   `json:"mapRunArn"`
	ExecutionArn               string   `json:"executionArn"`
	StateMachineArn            string   `json:"stateMachineArn"`
	Status                     string   `json:"status"`
	StartDate                  float64  `json:"startDate"`
	StopDate                   *float64 `json:"stopDate"`
	MaxConcurrency             int      `json:"maxConcurrency"`
	ToleratedFailurePercentage float64  `json:"toleratedFailurePercentage"`
	ToleratedFailureCount      int      `json:"toleratedFailureCount"`
	Total                      int      `json:"total"`
	Pending                    int      `json:"pending"`
	Running                    int      `json:"running"`
	Succeeded                  int      `json:"succeeded"`
	Failed                     int      `json:"failed"`
	TimedOut                   int      `json:"timedOut"`
	Aborted                    int      `json:"aborted"`
	ResultsWritten             int      `json:"resultsWritten"`
	FailuresNotRedrivable      int      `json:"failuresNotRedrivable"`
	PendingRedrive             int      `json:"pendingRedrive"`
	RedriveCount               int      `json:"redriveCount"`
}

var sfnMapRuns sim.Store[SFNMapRun]

func handleSFNDescribeMapRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MapRunArn string `json:"mapRunArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	mr, ok := sfnMapRuns.Get(req.MapRunArn)
	if !ok {
		sfnWriteError(w, "ResourceNotFound", "Map Run does not exist: "+req.MapRunArn)
		return
	}
	resp := map[string]any{
		"mapRunArn":                  mr.MapRunArn,
		"executionArn":               mr.ExecutionArn,
		"status":                     mr.Status,
		"startDate":                  mr.StartDate,
		"maxConcurrency":             mr.MaxConcurrency,
		"toleratedFailurePercentage": mr.ToleratedFailurePercentage,
		"toleratedFailureCount":      mr.ToleratedFailureCount,
		"itemCounts": map[string]any{
			"pending":               mr.Pending,
			"running":               mr.Running,
			"succeeded":             mr.Succeeded,
			"failed":                mr.Failed,
			"timedOut":              mr.TimedOut,
			"aborted":               mr.Aborted,
			"total":                 mr.Total,
			"resultsWritten":        mr.ResultsWritten,
			"failuresNotRedrivable": mr.FailuresNotRedrivable,
			"pendingRedrive":        mr.PendingRedrive,
		},
		"executionCounts": map[string]any{
			"pending":               mr.Pending,
			"running":               mr.Running,
			"succeeded":             mr.Succeeded,
			"failed":                mr.Failed,
			"timedOut":              mr.TimedOut,
			"aborted":               mr.Aborted,
			"total":                 mr.Total,
			"resultsWritten":        mr.ResultsWritten,
			"failuresNotRedrivable": mr.FailuresNotRedrivable,
			"pendingRedrive":        mr.PendingRedrive,
		},
		"redriveCount": mr.RedriveCount,
	}
	if mr.StopDate != nil {
		resp["stopDate"] = *mr.StopDate
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func handleSFNListMapRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionArn string `json:"executionArn"`
		MaxResults   *int   `json:"maxResults"`
		NextToken    string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	var owned []SFNMapRun
	for _, mr := range sfnMapRuns.List() {
		if mr.ExecutionArn == req.ExecutionArn {
			owned = append(owned, mr)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(owned, req.NextToken, maxR, 100)
	items := make([]map[string]any, 0, len(page))
	for _, mr := range page {
		item := map[string]any{
			"executionArn":    mr.ExecutionArn,
			"mapRunArn":       mr.MapRunArn,
			"stateMachineArn": mr.StateMachineArn,
			"startDate":       mr.StartDate,
		}
		if mr.StopDate != nil {
			item["stopDate"] = *mr.StopDate
		}
		items = append(items, item)
	}
	resp := map[string]any{"mapRuns": items}
	if nextTok != "" {
		resp["nextToken"] = nextTok
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func handleSFNUpdateMapRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MapRunArn                  string   `json:"mapRunArn"`
		MaxConcurrency             *int     `json:"maxConcurrency"`
		ToleratedFailurePercentage *float64 `json:"toleratedFailurePercentage"`
		ToleratedFailureCount      *int     `json:"toleratedFailureCount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sfnMu.Lock()
	defer sfnMu.Unlock()
	mr, ok := sfnMapRuns.Get(req.MapRunArn)
	if !ok {
		sfnWriteError(w, "ResourceNotFound", "Map Run does not exist: "+req.MapRunArn)
		return
	}
	if req.MaxConcurrency != nil {
		mr.MaxConcurrency = *req.MaxConcurrency
	}
	if req.ToleratedFailurePercentage != nil {
		mr.ToleratedFailurePercentage = *req.ToleratedFailurePercentage
	}
	if req.ToleratedFailureCount != nil {
		mr.ToleratedFailureCount = *req.ToleratedFailureCount
	}
	sfnMapRuns.Put(req.MapRunArn, mr)
	sfnWriteJSON(w, http.StatusOK, map[string]any{})
}

// ── TestState + StartSyncExecution ────────────────────────────────────────
//
// TestState runs a single state synchronously; StartSyncExecution runs the
// whole state machine synchronously and returns the terminal result. Both
// reuse the same ASL interpreter that backs StartExecution.

func handleSFNTestState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Definition string          `json:"definition"`
		Input      string          `json:"input"`
		StateName  string          `json:"stateName"`
		Variables  string          `json:"variables"`
		Context    string          `json:"context"`
		Mock       json.RawMessage `json:"mock"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	if req.Definition == "" {
		sfnWriteError(w, "ValidationException", "definition is required")
		return
	}
	var state sfnState
	var fullDefinition sfnDefinition
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(req.Definition), &raw); err != nil {
		sfnWriteError(w, "InvalidDefinition", "invalid state definition: "+err.Error())
		return
	}
	stateName := req.StateName
	queryLanguage := ""
	if _, full := raw["States"]; full {
		if err := json.Unmarshal([]byte(req.Definition), &fullDefinition); err != nil {
			sfnWriteError(w, "InvalidDefinition", "invalid state machine definition: "+err.Error())
			return
		}
		if err := sfnValidateDefinitionObject(fullDefinition, "$", ""); err != nil {
			sfnWriteError(w, "InvalidDefinition", err.Error())
			return
		}
		if stateName == "" {
			stateName = fullDefinition.StartAt
		}
		var exists bool
		state, exists = fullDefinition.States[stateName]
		if !exists {
			sfnWriteError(w, "ValidationException", "stateName does not identify a state in the definition")
			return
		}
		queryLanguage = fullDefinition.QueryLanguage
	} else if err := json.Unmarshal([]byte(req.Definition), &state); err != nil {
		sfnWriteError(w, "InvalidDefinition", "invalid state definition: "+err.Error())
		return
	}
	if stateName == "" {
		stateName = "TestState"
	}
	if req.Context != "" && len(req.Mock) == 0 {
		sfnWriteError(w, "ValidationException", "context can only be specified together with mock")
		return
	}
	if len(req.Mock) > 0 {
		var mock struct {
			Result      string `json:"result"`
			ErrorOutput *struct {
				Error string `json:"error"`
				Cause string `json:"cause"`
			} `json:"errorOutput"`
		}
		if err := json.Unmarshal(req.Mock, &mock); err != nil {
			sfnWriteError(w, "ValidationException", "mock must be valid JSON")
			return
		}
		if mock.ErrorOutput != nil {
			sfnWriteJSON(w, http.StatusOK, map[string]any{
				"status": "FAILED",
				"error":  mock.ErrorOutput.Error,
				"cause":  mock.ErrorOutput.Cause,
			})
			return
		}
		if mock.Result != "" {
			result := json.RawMessage(mock.Result)
			if !json.Valid(result) {
				sfnWriteError(w, "ValidationException", "mock.result must contain valid JSON")
				return
			}
			state.Type = "Pass"
			state.Resource = ""
			state.Result = &result
		}
	}
	// Force the single state to be terminal so the interpreter returns its
	// result rather than chasing a Next that isn't in the wrapper.
	nextState := state.Next
	state.Next = ""
	state.End = true
	wrapped := sfnDefinition{
		StartAt:       stateName,
		QueryLanguage: queryLanguage,
		States:        map[string]sfnState{stateName: state},
	}
	input := req.Input
	if input == "" {
		input = "{}"
	}
	variables := map[string]any{}
	if req.Variables != "" {
		if err := json.Unmarshal([]byte(req.Variables), &variables); err != nil {
			sfnWriteError(w, "ValidationException", "variables must be valid JSON")
			return
		}
	}
	output, status, err := sfnRunDefDepthWithVariables(wrapped, input, nil, 0, variables)

	resp := map[string]any{
		"inspectionData": map[string]any{"input": input},
	}
	if err != nil || status == "FAILED" {
		resp["status"] = "FAILED"
		resp["error"] = "States.Runtime"
		if err != nil {
			resp["cause"] = err.Error()
		}
	} else {
		resp["status"] = "SUCCEEDED"
		resp["output"] = output
		if nextState != "" {
			resp["nextState"] = nextState
		}
		if id, ok := resp["inspectionData"].(map[string]any); ok {
			id["result"] = output
		}
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func handleSFNStartSyncExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StateMachineArn string `json:"stateMachineArn"`
		Name            string `json:"name"`
		Input           string `json:"input"`
		TraceHeader     string `json:"traceHeader"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sfnWriteError(w, "InvalidRequest", "invalid JSON")
		return
	}
	sm, _, _, ok := sfnResolveExecutionTarget(req.StateMachineArn)
	if !ok {
		sfnWriteError(w, "StateMachineDoesNotExist", "State machine does not exist: "+req.StateMachineArn)
		return
	}
	smName := sm.Name
	if sm.Type != "EXPRESS" {
		sfnWriteError(w, "StateMachineTypeNotSupported", "StartSyncExecution is not supported for STANDARD workflows")
		return
	}
	if err := sfnValidateDefinition(sm.Definition); err != nil {
		sfnWriteError(w, "InvalidDefinition", err.Error())
		return
	}

	execName := req.Name
	if execName == "" {
		execName = uuid.New().String()
	}
	execARN := sfnARN("express:" + smName + ":" + execName + ":" + uuid.New().String())
	input := req.Input
	if input == "" {
		input = "{}"
	}
	startDate := sfnEpochNow()
	output, status, err := sfnExecute(sm.Definition, input, nil)
	stopDate := sfnEpochNow()

	resp := map[string]any{
		"executionArn":    execARN,
		"stateMachineArn": req.StateMachineArn,
		"name":            execName,
		"startDate":       startDate,
		"stopDate":        stopDate,
		"input":           input,
		"inputDetails":    map[string]any{"included": true},
		"outputDetails":   map[string]any{"included": true},
	}
	if req.TraceHeader != "" {
		resp["traceHeader"] = req.TraceHeader
	}
	if err != nil || status == "FAILED" {
		resp["status"] = "FAILED"
		if err != nil {
			executionErr := sfnAsExecutionError(err)
			resp["error"] = executionErr.Name
			resp["cause"] = executionErr.Cause
		}
	} else {
		resp["status"] = "SUCCEEDED"
		resp["output"] = output
		resp["billingDetails"] = sfnExpressBillingDetails(sm.Definition, input, startDate, stopDate)
	}
	sfnWriteJSON(w, http.StatusOK, resp)
}

func sfnExpressBillingDetails(definition, input string, startDate, stopDate float64) map[string]any {
	durationMilliseconds := int64(math.Ceil((stopDate-startDate)*1000/100) * 100)
	if durationMilliseconds < 100 {
		durationMilliseconds = 100
	}
	var parsed sfnDefinition
	_ = json.Unmarshal([]byte(definition), &parsed)
	parallelMapSteps := sfnParallelMapStateCount(parsed)
	memoryBytes := int64(50*1024*1024 + len(definition))
	if parallelMapSteps > 0 {
		memoryBytes += int64(len(input) * parallelMapSteps)
	}
	const billingChunk = int64(64 * 1024 * 1024)
	billedMemory := ((memoryBytes + billingChunk - 1) / billingChunk) * 64
	return map[string]any{
		"billedMemoryUsedInMB":         billedMemory,
		"billedDurationInMilliseconds": durationMilliseconds,
	}
}

func sfnParallelMapStateCount(definition sfnDefinition) int {
	count := 0
	for _, state := range definition.States {
		switch state.Type {
		case "Parallel":
			count++
			for _, branch := range state.Branches {
				count += sfnParallelMapStateCount(branch)
			}
		case "Map":
			count++
			if state.ItemProcessor != nil {
				count += sfnParallelMapStateCount(*state.ItemProcessor)
			} else if state.Iterator != nil {
				count += sfnParallelMapStateCount(*state.Iterator)
			}
		}
	}
	return count
}
