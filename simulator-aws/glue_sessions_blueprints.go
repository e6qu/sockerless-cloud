package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// AWS Glue — Interactive Sessions, Statements, Dev Endpoints, and Blueprints
// families. AWS JSON 1.1 protocol (X-Amz-Target: AWSGlue.<Op>), sharing
// glue.go's mutex, write helpers, and pagination.
//
// The simulator has no Spark/Livy backend, so statements settle to AVAILABLE
// synchronously with a real-shaped but empty StatementOutput (never fabricated
// cell output). Blueprint runs settle SUCCEEDED synchronously. Sessions and
// dev endpoints report READY once created — matching the lifecycle a faithful
// client reads back.

// GlueSession models an Interactive Sessions session keyed by Id.
type GlueSession struct {
	Id                    string            `json:"Id"`
	CreatedOn             float64           `json:"CreatedOn"`
	Status                string            `json:"Status"`
	ErrorMessage          string            `json:"ErrorMessage,omitempty"`
	Description           string            `json:"Description,omitempty"`
	Role                  string            `json:"Role,omitempty"`
	Command               *GlueSessionCmd   `json:"Command,omitempty"`
	DefaultArguments      map[string]string `json:"DefaultArguments,omitempty"`
	Connections           *GlueConnectionsL `json:"Connections,omitempty"`
	Progress              float64           `json:"Progress"`
	MaxCapacity           *float64          `json:"MaxCapacity,omitempty"`
	SecurityConfiguration string            `json:"SecurityConfiguration,omitempty"`
	GlueVersion           string            `json:"GlueVersion,omitempty"`
	NumberOfWorkers       *int              `json:"NumberOfWorkers,omitempty"`
	WorkerType            string            `json:"WorkerType,omitempty"`
	CompletedOn           *float64          `json:"CompletedOn,omitempty"`
	ExecutionTime         *float64          `json:"ExecutionTime,omitempty"`
	DPUSeconds            *float64          `json:"DPUSeconds,omitempty"`
	IdleTimeout           *int              `json:"IdleTimeout,omitempty"`
	ProfileName           string            `json:"ProfileName,omitempty"`
	SessionType           string            `json:"SessionType,omitempty"`

	// Tags ride GetTags, not the Session shape; kept for persistence only.
	Tags map[string]string `json:"-"`
}

// GlueSessionCmd mirrors the SessionCommand structure.
type GlueSessionCmd struct {
	Name          string `json:"Name,omitempty"`
	PythonVersion string `json:"PythonVersion,omitempty"`
}

// GlueConnectionsL mirrors the ConnectionsList structure.
type GlueConnectionsL struct {
	Connections []string `json:"Connections,omitempty"`
}

// GlueStatement models a statement run within a session. Keyed in the store by
// "<sessionId>/<id>"; Id is a per-session monotonically increasing integer.
type GlueStatement struct {
	SessionId   string               `json:"-"`
	Id          int                  `json:"Id"`
	Code        string               `json:"Code,omitempty"`
	State       string               `json:"State"`
	Output      *GlueStatementOutput `json:"Output,omitempty"`
	Progress    float64              `json:"Progress"`
	StartedOn   int64                `json:"StartedOn"`
	CompletedOn int64                `json:"CompletedOn"`
}

// GlueStatementOutput mirrors the StatementOutput structure.
type GlueStatementOutput struct {
	Data           *GlueStatementData `json:"Data,omitempty"`
	ExecutionCount int                `json:"ExecutionCount"`
	Status         string             `json:"Status"`
	ErrorName      string             `json:"ErrorName,omitempty"`
	ErrorValue     string             `json:"ErrorValue,omitempty"`
	Traceback      []string           `json:"Traceback,omitempty"`
}

// GlueStatementData mirrors the StatementOutputData structure.
type GlueStatementData struct {
	TextPlain string `json:"TextPlain,omitempty"`
}

// GlueDevEndpoint models a development endpoint keyed by EndpointName.
type GlueDevEndpoint struct {
	EndpointName                       string            `json:"EndpointName"`
	RoleArn                            string            `json:"RoleArn,omitempty"`
	SecurityGroupIds                   []string          `json:"SecurityGroupIds,omitempty"`
	SubnetId                           string            `json:"SubnetId,omitempty"`
	YarnEndpointAddress                string            `json:"YarnEndpointAddress,omitempty"`
	PrivateAddress                     string            `json:"PrivateAddress,omitempty"`
	ZeppelinRemoteSparkInterpreterPort int               `json:"ZeppelinRemoteSparkInterpreterPort,omitempty"`
	PublicAddress                      string            `json:"PublicAddress,omitempty"`
	Status                             string            `json:"Status"`
	WorkerType                         string            `json:"WorkerType,omitempty"`
	GlueVersion                        string            `json:"GlueVersion,omitempty"`
	NumberOfWorkers                    *int              `json:"NumberOfWorkers,omitempty"`
	NumberOfNodes                      int               `json:"NumberOfNodes,omitempty"`
	AvailabilityZone                   string            `json:"AvailabilityZone,omitempty"`
	VpcId                              string            `json:"VpcId,omitempty"`
	ExtraPythonLibsS3Path              string            `json:"ExtraPythonLibsS3Path,omitempty"`
	ExtraJarsS3Path                    string            `json:"ExtraJarsS3Path,omitempty"`
	FailureReason                      string            `json:"FailureReason,omitempty"`
	LastUpdateStatus                   string            `json:"LastUpdateStatus,omitempty"`
	CreatedTimestamp                   float64           `json:"CreatedTimestamp"`
	LastModifiedTimestamp              float64           `json:"LastModifiedTimestamp"`
	PublicKey                          string            `json:"PublicKey,omitempty"`
	PublicKeys                         []string          `json:"PublicKeys,omitempty"`
	SecurityConfiguration              string            `json:"SecurityConfiguration,omitempty"`
	Arguments                          map[string]string `json:"Arguments,omitempty"`

	// Tags ride GetTags, not the DevEndpoint shape; kept for persistence only.
	Tags map[string]string `json:"-"`
}

// GlueBlueprint models a blueprint keyed by Name.
type GlueBlueprint struct {
	Name                     string  `json:"Name"`
	Description              string  `json:"Description,omitempty"`
	CreatedOn                float64 `json:"CreatedOn"`
	LastModifiedOn           float64 `json:"LastModifiedOn"`
	ParameterSpec            string  `json:"ParameterSpec,omitempty"`
	BlueprintLocation        string  `json:"BlueprintLocation,omitempty"`
	BlueprintServiceLocation string  `json:"BlueprintServiceLocation,omitempty"`
	Status                   string  `json:"Status"`
	ErrorMessage             string  `json:"ErrorMessage,omitempty"`

	// Tags ride GetTags, not the Blueprint shape; kept for persistence only.
	Tags map[string]string `json:"-"`
}

// GlueBlueprintRun models one execution of a blueprint, keyed in the store by
// "<blueprintName>/<runId>".
type GlueBlueprintRun struct {
	BlueprintName        string   `json:"BlueprintName"`
	RunId                string   `json:"RunId"`
	WorkflowName         string   `json:"WorkflowName,omitempty"`
	State                string   `json:"State"`
	StartedOn            float64  `json:"StartedOn"`
	CompletedOn          *float64 `json:"CompletedOn,omitempty"`
	ErrorMessage         string   `json:"ErrorMessage,omitempty"`
	RollbackErrorMessage string   `json:"RollbackErrorMessage,omitempty"`
	Parameters           string   `json:"Parameters,omitempty"`
	RoleArn              string   `json:"RoleArn,omitempty"`
}

var (
	glueSessions      sim.Store[GlueSession]
	glueStatements    sim.Store[GlueStatement]
	glueDevEndpoints  sim.Store[GlueDevEndpoint]
	glueBlueprints    sim.Store[GlueBlueprint]
	glueBlueprintRuns sim.Store[GlueBlueprintRun]
)

func registerGlueSessionsBlueprints(r *sim.AWSRouter, srv *sim.Server) {
	glueSessions = sim.MakeStore[GlueSession](srv.DB(), "glue_sessions")
	glueStatements = sim.MakeStore[GlueStatement](srv.DB(), "glue_statements")
	glueDevEndpoints = sim.MakeStore[GlueDevEndpoint](srv.DB(), "glue_dev_endpoints")
	glueBlueprints = sim.MakeStore[GlueBlueprint](srv.DB(), "glue_blueprints")
	glueBlueprintRuns = sim.MakeStore[GlueBlueprintRun](srv.DB(), "glue_blueprint_runs")

	// Interactive Sessions
	r.Register("AWSGlue.CreateSession", handleGlueCreateSession)
	r.Register("AWSGlue.GetSession", handleGlueGetSession)
	r.Register("AWSGlue.ListSessions", handleGlueListSessions)
	r.Register("AWSGlue.StopSession", handleGlueStopSession)
	r.Register("AWSGlue.DeleteSession", handleGlueDeleteSession)
	r.Register("AWSGlue.GetSessionEndpoint", handleGlueGetSessionEndpoint)

	// Statements
	r.Register("AWSGlue.RunStatement", handleGlueRunStatement)
	r.Register("AWSGlue.GetStatement", handleGlueGetStatement)
	r.Register("AWSGlue.ListStatements", handleGlueListStatements)
	r.Register("AWSGlue.CancelStatement", handleGlueCancelStatement)

	// Dev Endpoints
	r.Register("AWSGlue.CreateDevEndpoint", handleGlueCreateDevEndpoint)
	r.Register("AWSGlue.GetDevEndpoint", handleGlueGetDevEndpoint)
	r.Register("AWSGlue.GetDevEndpoints", handleGlueGetDevEndpoints)
	r.Register("AWSGlue.BatchGetDevEndpoints", handleGlueBatchGetDevEndpoints)
	r.Register("AWSGlue.ListDevEndpoints", handleGlueListDevEndpoints)
	r.Register("AWSGlue.UpdateDevEndpoint", handleGlueUpdateDevEndpoint)
	r.Register("AWSGlue.DeleteDevEndpoint", handleGlueDeleteDevEndpoint)

	// Blueprints
	r.Register("AWSGlue.CreateBlueprint", handleGlueCreateBlueprint)
	r.Register("AWSGlue.GetBlueprint", handleGlueGetBlueprint)
	r.Register("AWSGlue.BatchGetBlueprints", handleGlueBatchGetBlueprints)
	r.Register("AWSGlue.ListBlueprints", handleGlueListBlueprints)
	r.Register("AWSGlue.UpdateBlueprint", handleGlueUpdateBlueprint)
	r.Register("AWSGlue.DeleteBlueprint", handleGlueDeleteBlueprint)
	r.Register("AWSGlue.StartBlueprintRun", handleGlueStartBlueprintRun)
	r.Register("AWSGlue.GetBlueprintRun", handleGlueGetBlueprintRun)
	r.Register("AWSGlue.GetBlueprintRuns", handleGlueGetBlueprintRuns)
}

func handleGlueCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id                    string            `json:"Id"`
		Description           string            `json:"Description"`
		Role                  string            `json:"Role"`
		Command               *GlueSessionCmd   `json:"Command"`
		DefaultArguments      map[string]string `json:"DefaultArguments"`
		Connections           *GlueConnectionsL `json:"Connections"`
		MaxCapacity           *float64          `json:"MaxCapacity"`
		NumberOfWorkers       *int              `json:"NumberOfWorkers"`
		WorkerType            string            `json:"WorkerType"`
		SecurityConfiguration string            `json:"SecurityConfiguration"`
		GlueVersion           string            `json:"GlueVersion"`
		IdleTimeout           *int              `json:"IdleTimeout"`
		SessionType           string            `json:"SessionType"`
		Tags                  map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Id == "" {
		glueWriteError(w, "InvalidInputException", "Id is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueSessions.Get(req.Id); ok {
		glueWriteError(w, "AlreadyExistsException", "Session already exists: "+req.Id)
		return
	}
	s := GlueSession{
		Id:                    req.Id,
		CreatedOn:             glueEpochNow(),
		Status:                "READY",
		Description:           req.Description,
		Role:                  req.Role,
		Command:               req.Command,
		DefaultArguments:      req.DefaultArguments,
		Connections:           req.Connections,
		Progress:              0,
		MaxCapacity:           req.MaxCapacity,
		SecurityConfiguration: req.SecurityConfiguration,
		GlueVersion:           req.GlueVersion,
		NumberOfWorkers:       req.NumberOfWorkers,
		WorkerType:            req.WorkerType,
		IdleTimeout:           req.IdleTimeout,
		SessionType:           req.SessionType,
		Tags:                  req.Tags,
	}
	glueSessions.Put(req.Id, s)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Session": s})
}

func handleGlueGetSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	s, ok := glueSessions.Get(req.Id)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Session not found: "+req.Id)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Session": s})
}

func handleGlueListSessions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueSessions.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Id < all[j].Id })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	ids := make([]string, 0, len(page))
	for _, s := range page {
		ids = append(ids, s.Id)
	}
	resp := map[string]any{"Ids": ids, "Sessions": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueStopSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	s, ok := glueSessions.Get(req.Id)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Session not found: "+req.Id)
		return
	}
	s.Status = "STOPPED"
	now := glueEpochNow()
	s.CompletedOn = &now
	glueSessions.Put(req.Id, s)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Id": req.Id})
}

func handleGlueDeleteSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueSessions.Get(req.Id); !ok {
		glueWriteError(w, "EntityNotFoundException", "Session not found: "+req.Id)
		return
	}
	glueSessions.Delete(req.Id)
	// Cascade-delete the session's statements.
	for _, st := range glueStatements.List() {
		if st.SessionId == req.Id {
			glueStatements.Delete(glueStatementKey(req.Id, st.Id))
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Id": req.Id})
}

func handleGlueGetSessionEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionId string `json:"SessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if _, ok := glueSessions.Get(req.SessionId); !ok {
		glueWriteError(w, "EntityNotFoundException", "Session not found: "+req.SessionId)
		return
	}
	// The live Glue API, botocore, and aws-sdk-go-v2 all read this member as
	// "SparkConnect" on the wire (the vendored smithy model's jsonName
	// "SPARK_CONNECT" is not honored by the real service or any real client).
	resp := map[string]any{
		"SparkConnect": map[string]any{
			"Url":                     "sc://" + req.SessionId + ".glue.amazonaws.com:443",
			"AuthTokenExpirationTime": glueEpochNow() + 3600,
		},
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func glueStatementKey(sessionID string, id int) string {
	return sessionID + "/" + strconv.Itoa(id)
}

// glueNextStatementID returns the next per-session statement id (1-based).
// Caller holds glueMu.
func glueNextStatementID(sessionID string) int {
	maxID := 0
	for _, st := range glueStatements.List() {
		if st.SessionId == sessionID && st.Id > maxID {
			maxID = st.Id
		}
	}
	return maxID + 1
}

func handleGlueRunStatement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionId string `json:"SessionId"`
		Code      string `json:"Code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.SessionId == "" {
		glueWriteError(w, "InvalidInputException", "SessionId is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueSessions.Get(req.SessionId); !ok {
		glueWriteError(w, "EntityNotFoundException", "Session not found: "+req.SessionId)
		return
	}
	id := glueNextStatementID(req.SessionId)
	now := time.Now().UTC().UnixMilli()
	// No Spark backend: the statement settles AVAILABLE with a real-shaped but
	// empty output (TextPlain empty), never fabricated cell results.
	st := GlueStatement{
		SessionId: req.SessionId,
		Id:        id,
		Code:      req.Code,
		State:     "AVAILABLE",
		Progress:  1,
		Output: &GlueStatementOutput{
			Data:           &GlueStatementData{TextPlain: ""},
			ExecutionCount: id,
			Status:         "AVAILABLE",
		},
		StartedOn:   now,
		CompletedOn: now,
	}
	glueStatements.Put(glueStatementKey(req.SessionId, id), st)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Id": id})
}

func handleGlueGetStatement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionId string `json:"SessionId"`
		Id        int    `json:"Id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	st, ok := glueStatements.Get(glueStatementKey(req.SessionId, req.Id))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Statement not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Statement": st})
}

func handleGlueListStatements(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionId string `json:"SessionId"`
		NextToken string `json:"NextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if _, ok := glueSessions.Get(req.SessionId); !ok {
		glueWriteError(w, "EntityNotFoundException", "Session not found: "+req.SessionId)
		return
	}
	var filtered []GlueStatement
	for _, st := range glueStatements.List() {
		if st.SessionId == req.SessionId {
			filtered = append(filtered, st)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Id < filtered[j].Id })
	page, nextTok := awsPage(filtered, req.NextToken, 0, 100)
	resp := map[string]any{"Statements": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueCancelStatement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionId string `json:"SessionId"`
		Id        int    `json:"Id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueStatementKey(req.SessionId, req.Id)
	st, ok := glueStatements.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Statement not found")
		return
	}
	st.State = "CANCELLED"
	if st.Output != nil {
		st.Output.Status = "CANCELLED"
	}
	glueStatements.Put(key, st)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueCreateDevEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EndpointName          string            `json:"EndpointName"`
		RoleArn               string            `json:"RoleArn"`
		SecurityGroupIds      []string          `json:"SecurityGroupIds"`
		SubnetId              string            `json:"SubnetId"`
		PublicKey             string            `json:"PublicKey"`
		PublicKeys            []string          `json:"PublicKeys"`
		NumberOfNodes         int               `json:"NumberOfNodes"`
		WorkerType            string            `json:"WorkerType"`
		GlueVersion           string            `json:"GlueVersion"`
		NumberOfWorkers       *int              `json:"NumberOfWorkers"`
		ExtraPythonLibsS3Path string            `json:"ExtraPythonLibsS3Path"`
		ExtraJarsS3Path       string            `json:"ExtraJarsS3Path"`
		SecurityConfiguration string            `json:"SecurityConfiguration"`
		Arguments             map[string]string `json:"Arguments"`
		Tags                  map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.EndpointName == "" {
		glueWriteError(w, "InvalidInputException", "EndpointName is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDevEndpoints.Get(req.EndpointName); ok {
		glueWriteError(w, "AlreadyExistsException", "DevEndpoint already exists: "+req.EndpointName)
		return
	}
	now := glueEpochNow()
	de := GlueDevEndpoint{
		EndpointName:          req.EndpointName,
		RoleArn:               req.RoleArn,
		SecurityGroupIds:      req.SecurityGroupIds,
		SubnetId:              req.SubnetId,
		Status:                "READY",
		WorkerType:            req.WorkerType,
		GlueVersion:           req.GlueVersion,
		NumberOfWorkers:       req.NumberOfWorkers,
		NumberOfNodes:         req.NumberOfNodes,
		ExtraPythonLibsS3Path: req.ExtraPythonLibsS3Path,
		ExtraJarsS3Path:       req.ExtraJarsS3Path,
		SecurityConfiguration: req.SecurityConfiguration,
		Arguments:             req.Arguments,
		PublicKey:             req.PublicKey,
		PublicKeys:            req.PublicKeys,
		CreatedTimestamp:      now,
		LastModifiedTimestamp: now,
		Tags:                  req.Tags,
	}
	glueDevEndpoints.Put(req.EndpointName, de)

	resp := map[string]any{
		"EndpointName":          de.EndpointName,
		"Status":                de.Status,
		"SecurityGroupIds":      de.SecurityGroupIds,
		"SubnetId":              de.SubnetId,
		"RoleArn":               de.RoleArn,
		"NumberOfNodes":         de.NumberOfNodes,
		"WorkerType":            de.WorkerType,
		"GlueVersion":           de.GlueVersion,
		"ExtraPythonLibsS3Path": de.ExtraPythonLibsS3Path,
		"ExtraJarsS3Path":       de.ExtraJarsS3Path,
		"SecurityConfiguration": de.SecurityConfiguration,
		"CreatedTimestamp":      de.CreatedTimestamp,
		"Arguments":             de.Arguments,
	}
	if de.NumberOfWorkers != nil {
		resp["NumberOfWorkers"] = *de.NumberOfWorkers
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueGetDevEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EndpointName string `json:"EndpointName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	de, ok := glueDevEndpoints.Get(req.EndpointName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "DevEndpoint not found: "+req.EndpointName)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"DevEndpoint": de})
}

func handleGlueGetDevEndpoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueDevEndpoints.List()
	sort.Slice(all, func(i, j int) bool { return all[i].EndpointName < all[j].EndpointName })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	resp := map[string]any{"DevEndpoints": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchGetDevEndpoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DevEndpointNames []string `json:"DevEndpointNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var found []GlueDevEndpoint
	var notFound []string
	for _, name := range req.DevEndpointNames {
		de, ok := glueDevEndpoints.Get(name)
		if ok {
			found = append(found, de)
		} else {
			notFound = append(notFound, name)
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["DevEndpoints"] = found
	}
	if len(notFound) > 0 {
		resp["DevEndpointsNotFound"] = notFound
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListDevEndpoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueDevEndpoints.List()
	sort.Slice(all, func(i, j int) bool { return all[i].EndpointName < all[j].EndpointName })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	names := make([]string, 0, len(page))
	for _, de := range page {
		names = append(names, de.EndpointName)
	}
	resp := map[string]any{"DevEndpointNames": names}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateDevEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EndpointName     string            `json:"EndpointName"`
		PublicKey        string            `json:"PublicKey"`
		AddPublicKeys    []string          `json:"AddPublicKeys"`
		DeletePublicKeys []string          `json:"DeletePublicKeys"`
		DeleteArguments  []string          `json:"DeleteArguments"`
		AddArguments     map[string]string `json:"AddArguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	de, ok := glueDevEndpoints.Get(req.EndpointName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "DevEndpoint not found: "+req.EndpointName)
		return
	}
	if req.PublicKey != "" {
		de.PublicKey = req.PublicKey
	}
	de.PublicKeys = append(de.PublicKeys, req.AddPublicKeys...)
	if len(req.DeletePublicKeys) > 0 {
		del := map[string]bool{}
		for _, k := range req.DeletePublicKeys {
			del[k] = true
		}
		var kept []string
		for _, k := range de.PublicKeys {
			if !del[k] {
				kept = append(kept, k)
			}
		}
		de.PublicKeys = kept
	}
	if len(req.AddArguments) > 0 {
		if de.Arguments == nil {
			de.Arguments = map[string]string{}
		}
		for k, v := range req.AddArguments {
			de.Arguments[k] = v
		}
	}
	for _, k := range req.DeleteArguments {
		delete(de.Arguments, k)
	}
	de.LastModifiedTimestamp = glueEpochNow()
	glueDevEndpoints.Put(req.EndpointName, de)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteDevEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EndpointName string `json:"EndpointName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDevEndpoints.Get(req.EndpointName); !ok {
		glueWriteError(w, "EntityNotFoundException", "DevEndpoint not found: "+req.EndpointName)
		return
	}
	glueDevEndpoints.Delete(req.EndpointName)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueCreateBlueprint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string            `json:"Name"`
		Description       string            `json:"Description"`
		BlueprintLocation string            `json:"BlueprintLocation"`
		Tags              map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}
	if req.BlueprintLocation == "" {
		glueWriteError(w, "InvalidInputException", "BlueprintLocation is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueBlueprints.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Blueprint already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	bp := GlueBlueprint{
		Name:              req.Name,
		Description:       req.Description,
		CreatedOn:         now,
		LastModifiedOn:    now,
		BlueprintLocation: req.BlueprintLocation,
		Status:            "ACTIVE",
		Tags:              req.Tags,
	}
	glueBlueprints.Put(req.Name, bp)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetBlueprint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	bp, ok := glueBlueprints.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Blueprint not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Blueprint": bp})
}

func handleGlueBatchGetBlueprints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"Names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var found []GlueBlueprint
	var missing []string
	for _, name := range req.Names {
		bp, ok := glueBlueprints.Get(name)
		if ok {
			found = append(found, bp)
		} else {
			missing = append(missing, name)
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["Blueprints"] = found
	}
	if len(missing) > 0 {
		resp["MissingBlueprints"] = missing
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListBlueprints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueBlueprints.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 25)
	names := make([]string, 0, len(page))
	for _, bp := range page {
		names = append(names, bp.Name)
	}
	resp := map[string]any{"Blueprints": names}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateBlueprint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string `json:"Name"`
		Description       string `json:"Description"`
		BlueprintLocation string `json:"BlueprintLocation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	bp, ok := glueBlueprints.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Blueprint not found: "+req.Name)
		return
	}
	if req.Description != "" {
		bp.Description = req.Description
	}
	if req.BlueprintLocation != "" {
		bp.BlueprintLocation = req.BlueprintLocation
	}
	bp.LastModifiedOn = glueEpochNow()
	bp.Status = "ACTIVE"
	glueBlueprints.Put(req.Name, bp)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueDeleteBlueprint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueBlueprints.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Blueprint not found: "+req.Name)
		return
	}
	glueBlueprints.Delete(req.Name)
	for _, run := range glueBlueprintRuns.List() {
		if run.BlueprintName == req.Name {
			glueBlueprintRuns.Delete(glueBlueprintRunKey(req.Name, run.RunId))
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func glueBlueprintRunKey(name, runID string) string {
	return name + "/" + runID
}

func handleGlueStartBlueprintRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BlueprintName string `json:"BlueprintName"`
		Parameters    string `json:"Parameters"`
		RoleArn       string `json:"RoleArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueBlueprints.Get(req.BlueprintName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Blueprint not found: "+req.BlueprintName)
		return
	}
	runID := "blueprint-run-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
	now := glueEpochNow()
	completed := now
	// No orchestration backend: the blueprint run settles SUCCEEDED.
	run := GlueBlueprintRun{
		BlueprintName: req.BlueprintName,
		RunId:         runID,
		State:         "SUCCEEDED",
		StartedOn:     now,
		CompletedOn:   &completed,
		Parameters:    req.Parameters,
		RoleArn:       req.RoleArn,
	}
	glueBlueprintRuns.Put(glueBlueprintRunKey(req.BlueprintName, runID), run)
	glueWriteJSON(w, http.StatusOK, map[string]any{"RunId": runID})
}

func handleGlueGetBlueprintRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BlueprintName string `json:"BlueprintName"`
		RunId         string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueBlueprintRuns.Get(glueBlueprintRunKey(req.BlueprintName, req.RunId))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Blueprint run not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"BlueprintRun": run})
}

func handleGlueGetBlueprintRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BlueprintName string `json:"BlueprintName"`
		NextToken     string `json:"NextToken"`
		MaxResults    *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var filtered []GlueBlueprintRun
	for _, run := range glueBlueprintRuns.List() {
		if run.BlueprintName == req.BlueprintName {
			filtered = append(filtered, run)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].RunId < filtered[j].RunId })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(filtered, req.NextToken, maxR, 100)
	resp := map[string]any{"BlueprintRuns": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}
