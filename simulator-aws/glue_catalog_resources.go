package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

func handleGlueCreateConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionInput struct {
			Name                           string            `json:"Name"`
			Description                    string            `json:"Description"`
			ConnectionType                 string            `json:"ConnectionType"`
			MatchCriteria                  []string          `json:"MatchCriteria"`
			ConnectionProperties           map[string]string `json:"ConnectionProperties"`
			PhysicalConnectionRequirements map[string]any    `json:"PhysicalConnectionRequirements"`
		} `json:"ConnectionInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ConnectionInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueConnections.Get(req.ConnectionInput.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Connection already exists: "+req.ConnectionInput.Name)
		return
	}
	now := glueEpochNow()
	in := req.ConnectionInput
	conn := GlueConnection{
		Name:                           in.Name,
		Description:                    in.Description,
		ConnectionType:                 in.ConnectionType,
		MatchCriteria:                  in.MatchCriteria,
		ConnectionProperties:           in.ConnectionProperties,
		PhysicalConnectionRequirements: in.PhysicalConnectionRequirements,
		CreationTime:                   now,
		LastUpdatedTime:                now,
	}
	glueConnections.Put(in.Name, conn)
	glueWriteJSON(w, http.StatusOK, map[string]any{"CreateConnectionStatus": "READY"})
}

func handleGlueGetConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	conn, ok := glueConnections.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Connection": conn})
}

func handleGlueGetConnections(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueConnections.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, all, func(it GlueConnection) string { return it.Name }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	resp := map[string]any{"ConnectionList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		ConnectionInput struct {
			Name                           string            `json:"Name"`
			Description                    string            `json:"Description"`
			ConnectionType                 string            `json:"ConnectionType"`
			MatchCriteria                  []string          `json:"MatchCriteria"`
			ConnectionProperties           map[string]string `json:"ConnectionProperties"`
			PhysicalConnectionRequirements map[string]any    `json:"PhysicalConnectionRequirements"`
		} `json:"ConnectionInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	conn, ok := glueConnections.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection not found: "+req.Name)
		return
	}
	in := req.ConnectionInput
	conn.Description = in.Description
	conn.ConnectionType = in.ConnectionType
	conn.MatchCriteria = in.MatchCriteria
	conn.ConnectionProperties = in.ConnectionProperties
	conn.PhysicalConnectionRequirements = in.PhysicalConnectionRequirements
	conn.LastUpdatedTime = glueEpochNow()
	glueConnections.Put(req.Name, conn)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionName string `json:"ConnectionName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	glueConnections.Delete(req.ConnectionName)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueCreateSecurityConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                    string         `json:"Name"`
		EncryptionConfiguration map[string]any `json:"EncryptionConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueSecConfigs.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Security configuration already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	glueSecConfigs.Put(req.Name, GlueSecurityConfiguration{
		Name:                    req.Name,
		CreatedTimeStamp:        now,
		EncryptionConfiguration: req.EncryptionConfiguration,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Name":             req.Name,
		"CreatedTimestamp": now,
	})
}

func handleGlueGetSecurityConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	sc, ok := glueSecConfigs.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Security configuration not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"SecurityConfiguration": sc})
}

func handleGlueGetSecurityConfigurations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueSecConfigs.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, all, func(it GlueSecurityConfiguration) string { return it.Name }, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"SecurityConfigurations": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteSecurityConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if _, ok := glueSecConfigs.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Security configuration not found: "+req.Name)
		return
	}
	glueSecConfigs.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                 string            `json:"Name"`
		Description          string            `json:"Description"`
		DefaultRunProperties map[string]string `json:"DefaultRunProperties"`
		Tags                 map[string]string `json:"Tags"`
		MaxConcurrentRuns    *int              `json:"MaxConcurrentRuns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueWorkflows.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Workflow already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	glueWorkflows.Put(req.Name, GlueWorkflow{
		Name:                 req.Name,
		Description:          req.Description,
		DefaultRunProperties: req.DefaultRunProperties,
		MaxConcurrentRuns:    req.MaxConcurrentRuns,
		Tags:                 req.Tags,
		CreatedOn:            now,
		LastModifiedOn:       now,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	wf, ok := glueWorkflows.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Workflow": wf})
}

func handleGlueListWorkflows(w http.ResponseWriter, r *http.Request) {
	glueListNames(w, r, glueWorkflows, func(it GlueWorkflow) string { return it.Name }, "Workflows", 25)
}

func handleGlueDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if _, ok := glueWorkflows.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	glueWorkflows.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueStartWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string            `json:"Name"`
		RunProperties map[string]string `json:"RunProperties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueWorkflows.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	runID := "wr_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := glueEpochNow()
	// Settle the run synchronously: the simulator runs no workflow graph.
	glueWfRuns.Put(req.Name+"\x00"+runID, GlueWorkflowRun{
		Name:                  req.Name,
		WorkflowRunId:         runID,
		WorkflowRunProperties: req.RunProperties,
		StartedOn:             now,
		CompletedOn:           now,
		Status:                "COMPLETED",
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"RunId": runID})
}

func handleGlueGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"Name"`
		RunId string `json:"RunId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueWfRuns.Get(req.Name + "\x00" + req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow run not found: "+req.RunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Run": run})
}

func glueClassifierName(c GlueClassifier) string {
	for _, m := range []map[string]any{c.GrokClassifier, c.XMLClassifier, c.JsonClassifier, c.CsvClassifier} {
		if m != nil {
			if n, ok := m["Name"].(string); ok {
				return n
			}
		}
	}
	return ""
}

// glueClassifierFromCreate maps a Create/UpdateClassifier request's
// Create*ClassifierRequest sub-object into the stored Classifier shape and stamps
// the server-managed timestamps and version.
func glueClassifierFromCreate(req map[string]json.RawMessage, prior *GlueClassifier) (GlueClassifier, string, bool) {
	var out GlueClassifier
	now := glueEpochNow()
	set := func(raw json.RawMessage, priorObj map[string]any) (map[string]any, bool) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, false
		}
		if priorObj != nil {
			m["CreationTime"] = priorObj["CreationTime"]
			ver := 1.0
			if v, ok := priorObj["Version"].(float64); ok {
				ver = v + 1
			}
			m["Version"] = ver
		} else {
			m["CreationTime"] = now
			m["Version"] = float64(1)
		}
		m["LastUpdated"] = now
		return m, true
	}
	for key, raw := range req {
		if raw == nil {
			continue
		}
		var priorObj map[string]any
		switch key {
		case "GrokClassifier":
			if prior != nil {
				priorObj = prior.GrokClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.GrokClassifier = m
		case "XMLClassifier":
			if prior != nil {
				priorObj = prior.XMLClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.XMLClassifier = m
		case "JsonClassifier":
			if prior != nil {
				priorObj = prior.JsonClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.JsonClassifier = m
		case "CsvClassifier":
			if prior != nil {
				priorObj = prior.CsvClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.CsvClassifier = m
		}
	}
	return out, glueClassifierName(out), true
}

func handleGlueCreateClassifier(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cls, name, ok := glueClassifierFromCreate(req, nil)
	if !ok || name == "" {
		glueWriteError(w, "InvalidInputException", "classifier Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, exists := glueClassifiers.Get(name); exists {
		glueWriteError(w, "AlreadyExistsException", "Classifier already exists: "+name)
		return
	}
	glueClassifiers.Put(name, cls)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetClassifier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cls, ok := glueClassifiers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Classifier not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Classifier": cls})
}

func handleGlueGetClassifiers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueClassifiers.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, all, func(it GlueClassifier) string { return glueClassifierName(it) }, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"Classifiers": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateClassifier(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var name string
	for _, raw := range req {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			if n, ok := m["Name"].(string); ok {
				name = n
				break
			}
		}
	}
	if name == "" {
		glueWriteError(w, "InvalidInputException", "classifier Name is required")
		return
	}
	prior, ok := glueClassifiers.Get(name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Classifier not found: "+name)
		return
	}
	cls, _, ok := glueClassifierFromCreate(req, &prior)
	if !ok {
		glueWriteError(w, "InvalidInputException", "invalid classifier input")
		return
	}
	glueClassifiers.Put(name, cls)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteClassifier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if _, ok := glueClassifiers.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Classifier not found: "+req.Name)
		return
	}
	glueClassifiers.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func glueUDFKey(db, fn string) string { return db + "\x00" + fn }

func handleGlueCreateUserDefinedFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId     string `json:"CatalogId"`
		DatabaseName  string `json:"DatabaseName"`
		FunctionInput struct {
			FunctionName string           `json:"FunctionName"`
			ClassName    string           `json:"ClassName"`
			OwnerName    string           `json:"OwnerName"`
			OwnerType    string           `json:"OwnerType"`
			FunctionType string           `json:"FunctionType"`
			ResourceUris []map[string]any `json:"ResourceUris"`
		} `json:"FunctionInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.FunctionInput.FunctionName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and FunctionInput.FunctionName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.DatabaseName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.DatabaseName)
		return
	}
	key := glueUDFKey(req.DatabaseName, req.FunctionInput.FunctionName)
	if _, ok := glueUDFs.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Function already exists: "+req.FunctionInput.FunctionName)
		return
	}
	glueUDFs.Put(key, GlueUserDefinedFunction{
		FunctionName: req.FunctionInput.FunctionName,
		DatabaseName: req.DatabaseName,
		ClassName:    req.FunctionInput.ClassName,
		OwnerName:    req.FunctionInput.OwnerName,
		OwnerType:    req.FunctionInput.OwnerType,
		FunctionType: req.FunctionInput.FunctionType,
		ResourceUris: req.FunctionInput.ResourceUris,
		CatalogId:    req.CatalogId,
		CreateTime:   glueEpochNow(),
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetUserDefinedFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		FunctionName string `json:"FunctionName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	udf, ok := glueUDFs.Get(glueUDFKey(req.DatabaseName, req.FunctionName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Function not found: "+req.FunctionName)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"UserDefinedFunction": udf})
}

func handleGlueGetUserDefinedFunctions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		Pattern      string `json:"Pattern"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueUDFs.List()
	filtered := make([]GlueUserDefinedFunction, 0, len(all))
	for _, udf := range all {
		if req.DatabaseName != "" && udf.DatabaseName != req.DatabaseName {
			continue
		}
		filtered = append(filtered, udf)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, filtered, func(it GlueUserDefinedFunction) string { return it.DatabaseName + "\x00" + it.FunctionName }, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"UserDefinedFunctions": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteUserDefinedFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		FunctionName string `json:"FunctionName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	key := glueUDFKey(req.DatabaseName, req.FunctionName)
	if _, ok := glueUDFs.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Function not found: "+req.FunctionName)
		return
	}
	glueUDFs.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}
