package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// AWS Glue — ML Transforms, Data Quality (rulesets, evaluation/recommendation
// runs, results, models) and Column-Statistics-Task families. AWS JSON 1.1
// protocol (X-Amz-Target: AWSGlue.<Op>), sharing glue.go's mutex, write
// helpers, and pagination. Runs settle to a terminal state synchronously since
// the simulator has no async backend; results over no real data are
// empty/zero-but-shaped (no fabricated metrics).

// GlueMLTransform models a machine learning transform keyed by TransformId.
type GlueMLTransform struct {
	TransformId       string            `json:"TransformId"`
	Name              string            `json:"Name"`
	Description       string            `json:"Description,omitempty"`
	Status            string            `json:"Status"`
	CreatedOn         float64           `json:"CreatedOn"`
	LastModifiedOn    float64           `json:"LastModifiedOn"`
	InputRecordTables []map[string]any  `json:"InputRecordTables,omitempty"`
	Parameters        map[string]any    `json:"Parameters,omitempty"`
	LabelCount        int               `json:"LabelCount"`
	Role              string            `json:"Role,omitempty"`
	GlueVersion       string            `json:"GlueVersion,omitempty"`
	MaxCapacity       *float64          `json:"MaxCapacity,omitempty"`
	WorkerType        string            `json:"WorkerType,omitempty"`
	NumberOfWorkers   *int              `json:"NumberOfWorkers,omitempty"`
	Timeout           *int              `json:"Timeout,omitempty"`
	MaxRetries        *int              `json:"MaxRetries,omitempty"`
	Tags              map[string]string `json:"Tags,omitempty"`
}

// glueMLTransformWire strips Tags from the response — the real MLTransform
// shape has no Tags member (tags ride GetTags / ListMLTransforms input).
type glueMLTransformWire struct {
	GlueMLTransform
}

func (t glueMLTransformWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(t.GlueMLTransform)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "Tags")
	return json.Marshal(m)
}

// GlueDataQualityRuleset models a Data Quality ruleset keyed by name.
type GlueDataQualityRuleset struct {
	Name                             string            `json:"Name"`
	Description                      string            `json:"Description,omitempty"`
	Ruleset                          string            `json:"Ruleset,omitempty"`
	TargetTable                      map[string]any    `json:"TargetTable,omitempty"`
	CreatedOn                        float64           `json:"CreatedOn"`
	LastModifiedOn                   float64           `json:"LastModifiedOn"`
	RecommendationRunId              string            `json:"RecommendationRunId,omitempty"`
	DataQualitySecurityConfiguration string            `json:"DataQualitySecurityConfiguration,omitempty"`
	Tags                             map[string]string `json:"Tags,omitempty"`
}

// glueRuleCount counts the rules in a DQDL ruleset string (the number of
// commas at the top level of the "Rules = [ ... ]" block, +1). The simulator
// keeps this faithful to the stored ruleset text rather than fabricating one.
func glueRuleCount(ruleset string) *int {
	open := strings.Index(ruleset, "[")
	closeI := strings.LastIndex(ruleset, "]")
	if open < 0 || closeI <= open {
		return nil
	}
	inner := strings.TrimSpace(ruleset[open+1 : closeI])
	if inner == "" {
		zero := 0
		return &zero
	}
	count := strings.Count(inner, ",") + 1
	return &count
}

// GlueDQRulesetEvaluationRun models a Data Quality ruleset evaluation run keyed
// by RunId. It settles SUCCEEDED synchronously, producing a result row.
type GlueDQRulesetEvaluationRun struct {
	RunId                string         `json:"RunId"`
	DataSource           map[string]any `json:"DataSource,omitempty"`
	Role                 string         `json:"Role,omitempty"`
	NumberOfWorkers      *int           `json:"NumberOfWorkers,omitempty"`
	Timeout              *int           `json:"Timeout,omitempty"`
	AdditionalRunOptions map[string]any `json:"AdditionalRunOptions,omitempty"`
	Status               string         `json:"Status"`
	StartedOn            float64        `json:"StartedOn"`
	LastModifiedOn       float64        `json:"LastModifiedOn"`
	CompletedOn          float64        `json:"CompletedOn"`
	ExecutionTime        int            `json:"ExecutionTime"`
	RulesetNames         []string       `json:"RulesetNames,omitempty"`
	ResultIds            []string       `json:"ResultIds,omitempty"`
}

// GlueDQRuleRecommendationRun models a Data Quality rule-recommendation run
// keyed by RunId. It settles SUCCEEDED synchronously and recommends a ruleset.
type GlueDQRuleRecommendationRun struct {
	RunId                            string         `json:"RunId"`
	DataSource                       map[string]any `json:"DataSource,omitempty"`
	Role                             string         `json:"Role,omitempty"`
	NumberOfWorkers                  *int           `json:"NumberOfWorkers,omitempty"`
	Timeout                          *int           `json:"Timeout,omitempty"`
	Status                           string         `json:"Status"`
	StartedOn                        float64        `json:"StartedOn"`
	LastModifiedOn                   float64        `json:"LastModifiedOn"`
	CompletedOn                      float64        `json:"CompletedOn"`
	ExecutionTime                    int            `json:"ExecutionTime"`
	RecommendedRuleset               string         `json:"RecommendedRuleset,omitempty"`
	CreatedRulesetName               string         `json:"CreatedRulesetName,omitempty"`
	DataQualitySecurityConfiguration string         `json:"DataQualitySecurityConfiguration,omitempty"`
}

// GlueDataQualityResult models the result row produced by a ruleset evaluation
// run, keyed by ResultId. Over no real data it carries an empty (but shaped)
// rule-result set — no fabricated metrics.
type GlueDataQualityResult struct {
	ResultId               string           `json:"ResultId"`
	DataSource             map[string]any   `json:"DataSource,omitempty"`
	RulesetName            string           `json:"RulesetName,omitempty"`
	RulesetEvaluationRunId string           `json:"RulesetEvaluationRunId,omitempty"`
	StartedOn              float64          `json:"StartedOn"`
	CompletedOn            float64          `json:"CompletedOn"`
	RuleResults            []map[string]any `json:"RuleResults,omitempty"`
}

// GlueColumnStatsTaskSettings models per-(database,table) column-statistics
// task settings.
type GlueColumnStatsTaskSettings struct {
	DatabaseName          string        `json:"DatabaseName"`
	TableName             string        `json:"TableName"`
	Schedule              *GlueSchedule `json:"Schedule,omitempty"`
	ColumnNameList        []string      `json:"ColumnNameList,omitempty"`
	CatalogID             string        `json:"CatalogID,omitempty"`
	Role                  string        `json:"Role,omitempty"`
	SampleSize            *float64      `json:"SampleSize,omitempty"`
	SecurityConfiguration string        `json:"SecurityConfiguration,omitempty"`
	ScheduleType          string        `json:"ScheduleType,omitempty"`
	SettingSource         string        `json:"SettingSource,omitempty"`
}

// GlueColumnStatsTaskRun models one column-statistics task run keyed by its
// ColumnStatisticsTaskRunId. It settles SUCCEEDED synchronously.
type GlueColumnStatsTaskRun struct {
	ColumnStatisticsTaskRunId string   `json:"ColumnStatisticsTaskRunId"`
	DatabaseName              string   `json:"DatabaseName"`
	TableName                 string   `json:"TableName"`
	ColumnNameList            []string `json:"ColumnNameList,omitempty"`
	CatalogID                 string   `json:"CatalogID,omitempty"`
	Role                      string   `json:"Role,omitempty"`
	SampleSize                *float64 `json:"SampleSize,omitempty"`
	SecurityConfiguration     string   `json:"SecurityConfiguration,omitempty"`
	ComputationType           string   `json:"ComputationType,omitempty"`
	Status                    string   `json:"Status"`
	CreationTime              float64  `json:"CreationTime"`
	LastUpdated               float64  `json:"LastUpdated"`
	StartTime                 float64  `json:"StartTime"`
	EndTime                   float64  `json:"EndTime"`
}

var (
	glueMLTransforms sim.Store[GlueMLTransform]
	glueDQRulesets   sim.Store[GlueDataQualityRuleset]
	glueDQEvalRuns   sim.Store[GlueDQRulesetEvaluationRun]
	glueDQRecRuns    sim.Store[GlueDQRuleRecommendationRun]
	glueDQResults    sim.Store[GlueDataQualityResult]
	glueCSTaskSettgs sim.Store[GlueColumnStatsTaskSettings]
	glueCSTaskRuns   sim.Store[GlueColumnStatsTaskRun]
)

// glueCSTaskKey is the store key for column-statistics task settings, scoped to
// a (database, table) pair.
func glueCSTaskKey(database, table string) string {
	return database + "/" + table
}

// glueHashID returns a 32-char hex id (no dashes) shaped like the real Glue
// HashString ids.
func glueHashID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

func registerGlueMLDataQuality(r *sim.AWSRouter, srv *sim.Server) {
	glueMLTransforms = sim.MakeStore[GlueMLTransform](srv.DB(), "glue_ml_transforms")
	glueDQRulesets = sim.MakeStore[GlueDataQualityRuleset](srv.DB(), "glue_dq_rulesets")
	glueDQEvalRuns = sim.MakeStore[GlueDQRulesetEvaluationRun](srv.DB(), "glue_dq_eval_runs")
	glueDQRecRuns = sim.MakeStore[GlueDQRuleRecommendationRun](srv.DB(), "glue_dq_rec_runs")
	glueDQResults = sim.MakeStore[GlueDataQualityResult](srv.DB(), "glue_dq_results")
	glueCSTaskSettgs = sim.MakeStore[GlueColumnStatsTaskSettings](srv.DB(), "glue_cs_task_settings")
	glueCSTaskRuns = sim.MakeStore[GlueColumnStatsTaskRun](srv.DB(), "glue_cs_task_runs")

	// ML Transforms.
	r.Register("AWSGlue.CreateMLTransform", handleGlueCreateMLTransform)
	r.Register("AWSGlue.GetMLTransform", handleGlueGetMLTransform)
	r.Register("AWSGlue.GetMLTransforms", handleGlueGetMLTransforms)
	r.Register("AWSGlue.ListMLTransforms", handleGlueListMLTransforms)
	r.Register("AWSGlue.UpdateMLTransform", handleGlueUpdateMLTransform)
	r.Register("AWSGlue.DeleteMLTransform", handleGlueDeleteMLTransform)

	// Data Quality rulesets.
	r.Register("AWSGlue.CreateDataQualityRuleset", handleGlueCreateDataQualityRuleset)
	r.Register("AWSGlue.GetDataQualityRuleset", handleGlueGetDataQualityRuleset)
	r.Register("AWSGlue.ListDataQualityRulesets", handleGlueListDataQualityRulesets)
	r.Register("AWSGlue.UpdateDataQualityRuleset", handleGlueUpdateDataQualityRuleset)
	r.Register("AWSGlue.DeleteDataQualityRuleset", handleGlueDeleteDataQualityRuleset)

	// Data Quality ruleset evaluation runs.
	r.Register("AWSGlue.StartDataQualityRulesetEvaluationRun", handleGlueStartDataQualityRulesetEvaluationRun)
	r.Register("AWSGlue.GetDataQualityRulesetEvaluationRun", handleGlueGetDataQualityRulesetEvaluationRun)
	r.Register("AWSGlue.CancelDataQualityRulesetEvaluationRun", handleGlueCancelDataQualityRulesetEvaluationRun)
	r.Register("AWSGlue.ListDataQualityRulesetEvaluationRuns", handleGlueListDataQualityRulesetEvaluationRuns)

	// Data Quality rule recommendation runs.
	r.Register("AWSGlue.StartDataQualityRuleRecommendationRun", handleGlueStartDataQualityRuleRecommendationRun)
	r.Register("AWSGlue.GetDataQualityRuleRecommendationRun", handleGlueGetDataQualityRuleRecommendationRun)
	r.Register("AWSGlue.CancelDataQualityRuleRecommendationRun", handleGlueCancelDataQualityRuleRecommendationRun)
	r.Register("AWSGlue.ListDataQualityRuleRecommendationRuns", handleGlueListDataQualityRuleRecommendationRuns)

	// Data Quality results + models.
	r.Register("AWSGlue.GetDataQualityResult", handleGlueGetDataQualityResult)
	r.Register("AWSGlue.BatchGetDataQualityResult", handleGlueBatchGetDataQualityResult)
	r.Register("AWSGlue.ListDataQualityResults", handleGlueListDataQualityResults)
	r.Register("AWSGlue.ListDataQualityStatistics", handleGlueListDataQualityStatistics)
	r.Register("AWSGlue.GetDataQualityModel", handleGlueGetDataQualityModel)
	r.Register("AWSGlue.GetDataQualityModelResult", handleGlueGetDataQualityModelResult)

	// Column-statistics task settings.
	r.Register("AWSGlue.CreateColumnStatisticsTaskSettings", handleGlueCreateColumnStatisticsTaskSettings)
	r.Register("AWSGlue.GetColumnStatisticsTaskSettings", handleGlueGetColumnStatisticsTaskSettings)
	r.Register("AWSGlue.UpdateColumnStatisticsTaskSettings", handleGlueUpdateColumnStatisticsTaskSettings)
	r.Register("AWSGlue.DeleteColumnStatisticsTaskSettings", handleGlueDeleteColumnStatisticsTaskSettings)

	// Column-statistics task runs.
	r.Register("AWSGlue.StartColumnStatisticsTaskRun", handleGlueStartColumnStatisticsTaskRun)
	r.Register("AWSGlue.GetColumnStatisticsTaskRun", handleGlueGetColumnStatisticsTaskRun)
	r.Register("AWSGlue.GetColumnStatisticsTaskRuns", handleGlueGetColumnStatisticsTaskRuns)
	r.Register("AWSGlue.ListColumnStatisticsTaskRuns", handleGlueListColumnStatisticsTaskRuns)
	r.Register("AWSGlue.StopColumnStatisticsTaskRun", handleGlueStopColumnStatisticsTaskRun)
}

func handleGlueCreateMLTransform(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string            `json:"Name"`
		Description       string            `json:"Description"`
		InputRecordTables []map[string]any  `json:"InputRecordTables"`
		Parameters        map[string]any    `json:"Parameters"`
		Role              string            `json:"Role"`
		GlueVersion       string            `json:"GlueVersion"`
		MaxCapacity       *float64          `json:"MaxCapacity"`
		WorkerType        string            `json:"WorkerType"`
		NumberOfWorkers   *int              `json:"NumberOfWorkers"`
		Timeout           *int              `json:"Timeout"`
		MaxRetries        *int              `json:"MaxRetries"`
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

	glueMu.Lock()
	defer glueMu.Unlock()

	id := glueHashID()
	now := glueEpochNow()
	t := GlueMLTransform{
		TransformId:       id,
		Name:              req.Name,
		Description:       req.Description,
		Status:            "READY",
		CreatedOn:         now,
		LastModifiedOn:    now,
		InputRecordTables: req.InputRecordTables,
		Parameters:        req.Parameters,
		LabelCount:        0,
		Role:              req.Role,
		GlueVersion:       req.GlueVersion,
		MaxCapacity:       req.MaxCapacity,
		WorkerType:        req.WorkerType,
		NumberOfWorkers:   req.NumberOfWorkers,
		Timeout:           req.Timeout,
		MaxRetries:        req.MaxRetries,
		Tags:              req.Tags,
	}
	glueMLTransforms.Put(id, t)
	glueWriteJSON(w, http.StatusOK, map[string]any{"TransformId": id})
}

func handleGlueGetMLTransform(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransformId string `json:"TransformId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	t, ok := glueMLTransforms.Get(req.TransformId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Transform not found: "+req.TransformId)
		return
	}
	// GetMLTransform returns the transform's members at the top level (not
	// nested), per the response shape — emit via the Tags-stripping wire form.
	b, _ := json.Marshal(glueMLTransformWire{t})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	glueWriteJSON(w, http.StatusOK, m)
}

func handleGlueGetMLTransforms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueMLTransforms.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	transforms := make([]glueMLTransformWire, 0, len(page))
	for _, t := range page {
		transforms = append(transforms, glueMLTransformWire{t})
	}
	resp := map[string]any{"Transforms": transforms}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListMLTransforms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueMLTransforms.List()
	ids := make([]string, 0, len(all))
	for _, t := range all {
		ids = append(ids, t.TransformId)
	}
	page, nextTok := awsPage(ids, req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	resp := map[string]any{"TransformIds": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateMLTransform(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransformId     string         `json:"TransformId"`
		Name            string         `json:"Name"`
		Description     string         `json:"Description"`
		Parameters      map[string]any `json:"Parameters"`
		Role            string         `json:"Role"`
		GlueVersion     string         `json:"GlueVersion"`
		MaxCapacity     *float64       `json:"MaxCapacity"`
		WorkerType      string         `json:"WorkerType"`
		NumberOfWorkers *int           `json:"NumberOfWorkers"`
		Timeout         *int           `json:"Timeout"`
		MaxRetries      *int           `json:"MaxRetries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	t, ok := glueMLTransforms.Get(req.TransformId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Transform not found: "+req.TransformId)
		return
	}
	if req.Name != "" {
		t.Name = req.Name
	}
	if req.Description != "" {
		t.Description = req.Description
	}
	if req.Parameters != nil {
		t.Parameters = req.Parameters
	}
	if req.Role != "" {
		t.Role = req.Role
	}
	if req.GlueVersion != "" {
		t.GlueVersion = req.GlueVersion
	}
	if req.MaxCapacity != nil {
		t.MaxCapacity = req.MaxCapacity
	}
	if req.WorkerType != "" {
		t.WorkerType = req.WorkerType
	}
	if req.NumberOfWorkers != nil {
		t.NumberOfWorkers = req.NumberOfWorkers
	}
	if req.Timeout != nil {
		t.Timeout = req.Timeout
	}
	if req.MaxRetries != nil {
		t.MaxRetries = req.MaxRetries
	}
	t.LastModifiedOn = glueEpochNow()
	glueMLTransforms.Put(req.TransformId, t)
	glueWriteJSON(w, http.StatusOK, map[string]any{"TransformId": req.TransformId})
}

func handleGlueDeleteMLTransform(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransformId string `json:"TransformId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueMLTransforms.Get(req.TransformId); !ok {
		glueWriteError(w, "EntityNotFoundException", "Transform not found: "+req.TransformId)
		return
	}
	glueMLTransforms.Delete(req.TransformId)
	glueWriteJSON(w, http.StatusOK, map[string]any{"TransformId": req.TransformId})
}

func handleGlueCreateDataQualityRuleset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                             string            `json:"Name"`
		Description                      string            `json:"Description"`
		Ruleset                          string            `json:"Ruleset"`
		Tags                             map[string]string `json:"Tags"`
		TargetTable                      map[string]any    `json:"TargetTable"`
		DataQualitySecurityConfiguration string            `json:"DataQualitySecurityConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}
	if req.Ruleset == "" {
		glueWriteError(w, "InvalidInputException", "Ruleset is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDQRulesets.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "DataQualityRuleset already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	rs := GlueDataQualityRuleset{
		Name:                             req.Name,
		Description:                      req.Description,
		Ruleset:                          req.Ruleset,
		TargetTable:                      req.TargetTable,
		CreatedOn:                        now,
		LastModifiedOn:                   now,
		DataQualitySecurityConfiguration: req.DataQualitySecurityConfiguration,
		Tags:                             req.Tags,
	}
	glueDQRulesets.Put(req.Name, rs)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetDataQualityRuleset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	rs, ok := glueDQRulesets.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "DataQualityRuleset not found: "+req.Name)
		return
	}
	resp := map[string]any{
		"Name":           rs.Name,
		"Ruleset":        rs.Ruleset,
		"CreatedOn":      rs.CreatedOn,
		"LastModifiedOn": rs.LastModifiedOn,
	}
	if rs.Description != "" {
		resp["Description"] = rs.Description
	}
	if rs.TargetTable != nil {
		resp["TargetTable"] = rs.TargetTable
	}
	if rs.RecommendationRunId != "" {
		resp["RecommendationRunId"] = rs.RecommendationRunId
	}
	if rs.DataQualitySecurityConfiguration != "" {
		resp["DataQualitySecurityConfiguration"] = rs.DataQualitySecurityConfiguration
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListDataQualityRulesets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueDQRulesets.List()
	details := make([]map[string]any, 0, len(all))
	for _, rs := range all {
		d := map[string]any{
			"Name":           rs.Name,
			"CreatedOn":      rs.CreatedOn,
			"LastModifiedOn": rs.LastModifiedOn,
		}
		if rs.Description != "" {
			d["Description"] = rs.Description
		}
		if rs.TargetTable != nil {
			d["TargetTable"] = rs.TargetTable
		}
		if rs.RecommendationRunId != "" {
			d["RecommendationRunId"] = rs.RecommendationRunId
		}
		if rc := glueRuleCount(rs.Ruleset); rc != nil {
			d["RuleCount"] = *rc
		}
		details = append(details, d)
	}
	page, nextTok := awsPage(details, req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	resp := map[string]any{"Rulesets": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateDataQualityRuleset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"Name"`
		Description string `json:"Description"`
		Ruleset     string `json:"Ruleset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	rs, ok := glueDQRulesets.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "DataQualityRuleset not found: "+req.Name)
		return
	}
	if req.Description != "" {
		rs.Description = req.Description
	}
	if req.Ruleset != "" {
		rs.Ruleset = req.Ruleset
	}
	rs.LastModifiedOn = glueEpochNow()
	glueDQRulesets.Put(req.Name, rs)
	resp := map[string]any{"Name": rs.Name}
	if rs.Description != "" {
		resp["Description"] = rs.Description
	}
	if rs.Ruleset != "" {
		resp["Ruleset"] = rs.Ruleset
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteDataQualityRuleset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDQRulesets.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "DataQualityRuleset not found: "+req.Name)
		return
	}
	glueDQRulesets.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStartDataQualityRulesetEvaluationRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DataSource           map[string]any `json:"DataSource"`
		Role                 string         `json:"Role"`
		NumberOfWorkers      *int           `json:"NumberOfWorkers"`
		Timeout              *int           `json:"Timeout"`
		AdditionalRunOptions map[string]any `json:"AdditionalRunOptions"`
		RulesetNames         []string       `json:"RulesetNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if len(req.RulesetNames) == 0 {
		glueWriteError(w, "InvalidInputException", "RulesetNames is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	runID := glueHashID()
	now := glueEpochNow()

	// Settle synchronously: one result row per ruleset evaluated. Over no real
	// data the row carries an empty (but shaped) rule-result set.
	resultIDs := make([]string, 0, len(req.RulesetNames))
	for _, name := range req.RulesetNames {
		resID := "dqresult-" + glueHashID()[:16]
		glueDQResults.Put(resID, GlueDataQualityResult{
			ResultId:               resID,
			DataSource:             req.DataSource,
			RulesetName:            name,
			RulesetEvaluationRunId: runID,
			StartedOn:              now,
			CompletedOn:            now,
			RuleResults:            []map[string]any{},
		})
		resultIDs = append(resultIDs, resID)
	}

	run := GlueDQRulesetEvaluationRun{
		RunId:                runID,
		DataSource:           req.DataSource,
		Role:                 req.Role,
		NumberOfWorkers:      req.NumberOfWorkers,
		Timeout:              req.Timeout,
		AdditionalRunOptions: req.AdditionalRunOptions,
		Status:               "SUCCEEDED",
		StartedOn:            now,
		LastModifiedOn:       now,
		CompletedOn:          now,
		ExecutionTime:        0,
		RulesetNames:         req.RulesetNames,
		ResultIds:            resultIDs,
	}
	glueDQEvalRuns.Put(runID, run)
	glueWriteJSON(w, http.StatusOK, map[string]any{"RunId": runID})
}

func handleGlueGetDataQualityRulesetEvaluationRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunId string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueDQEvalRuns.Get(req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "EvaluationRun not found: "+req.RunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, run)
}

func handleGlueCancelDataQualityRulesetEvaluationRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunId string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	run, ok := glueDQEvalRuns.Get(req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "EvaluationRun not found: "+req.RunId)
		return
	}
	// The run already settled SUCCEEDED synchronously; a cancel of a terminal
	// run leaves it terminal, matching the real service which only cancels
	// in-flight runs. Record the cancel attempt by stamping LastModifiedOn.
	run.LastModifiedOn = glueEpochNow()
	glueDQEvalRuns.Put(req.RunId, run)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListDataQualityRulesetEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueDQEvalRuns.List()
	runs := make([]map[string]any, 0, len(all))
	for _, run := range all {
		d := map[string]any{
			"RunId":     run.RunId,
			"Status":    run.Status,
			"StartedOn": run.StartedOn,
		}
		if run.DataSource != nil {
			d["DataSource"] = run.DataSource
		}
		runs = append(runs, d)
	}
	page, nextTok := awsPage(runs, req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	resp := map[string]any{"Runs": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueStartDataQualityRuleRecommendationRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DataSource                       map[string]any `json:"DataSource"`
		Role                             string         `json:"Role"`
		NumberOfWorkers                  *int           `json:"NumberOfWorkers"`
		Timeout                          *int           `json:"Timeout"`
		CreatedRulesetName               string         `json:"CreatedRulesetName"`
		DataQualitySecurityConfiguration string         `json:"DataQualitySecurityConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	runID := glueHashID()
	now := glueEpochNow()
	// Over no real data the recommendation is an empty (but shaped) ruleset —
	// no fabricated rules.
	recommended := "Rules = [ ]"
	run := GlueDQRuleRecommendationRun{
		RunId:                            runID,
		DataSource:                       req.DataSource,
		Role:                             req.Role,
		NumberOfWorkers:                  req.NumberOfWorkers,
		Timeout:                          req.Timeout,
		Status:                           "SUCCEEDED",
		StartedOn:                        now,
		LastModifiedOn:                   now,
		CompletedOn:                      now,
		ExecutionTime:                    0,
		RecommendedRuleset:               recommended,
		CreatedRulesetName:               req.CreatedRulesetName,
		DataQualitySecurityConfiguration: req.DataQualitySecurityConfiguration,
	}
	glueDQRecRuns.Put(runID, run)

	// If a CreatedRulesetName was requested, materialize the recommended
	// ruleset as a real ruleset row that links back to this run.
	if req.CreatedRulesetName != "" {
		if _, exists := glueDQRulesets.Get(req.CreatedRulesetName); !exists {
			glueDQRulesets.Put(req.CreatedRulesetName, GlueDataQualityRuleset{
				Name:                req.CreatedRulesetName,
				Ruleset:             recommended,
				CreatedOn:           now,
				LastModifiedOn:      now,
				RecommendationRunId: runID,
			})
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"RunId": runID})
}

func handleGlueGetDataQualityRuleRecommendationRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunId string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueDQRecRuns.Get(req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "RecommendationRun not found: "+req.RunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, run)
}

func handleGlueCancelDataQualityRuleRecommendationRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunId string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	run, ok := glueDQRecRuns.Get(req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "RecommendationRun not found: "+req.RunId)
		return
	}
	run.LastModifiedOn = glueEpochNow()
	glueDQRecRuns.Put(req.RunId, run)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListDataQualityRuleRecommendationRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueDQRecRuns.List()
	runs := make([]map[string]any, 0, len(all))
	for _, run := range all {
		d := map[string]any{
			"RunId":     run.RunId,
			"Status":    run.Status,
			"StartedOn": run.StartedOn,
		}
		if run.DataSource != nil {
			d["DataSource"] = run.DataSource
		}
		runs = append(runs, d)
	}
	page, nextTok := awsPage(runs, req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	resp := map[string]any{"Runs": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueDQResultPayload renders a stored result row as a GetDataQualityResult /
// DataQualityResult response map.
func glueDQResultPayload(res GlueDataQualityResult) map[string]any {
	m := map[string]any{
		"ResultId":    res.ResultId,
		"StartedOn":   res.StartedOn,
		"CompletedOn": res.CompletedOn,
		"RuleResults": res.RuleResults,
	}
	if res.DataSource != nil {
		m["DataSource"] = res.DataSource
	}
	if res.RulesetName != "" {
		m["RulesetName"] = res.RulesetName
	}
	if res.RulesetEvaluationRunId != "" {
		m["RulesetEvaluationRunId"] = res.RulesetEvaluationRunId
	}
	return m
}

func handleGlueGetDataQualityResult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResultId string `json:"ResultId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	res, ok := glueDQResults.Get(req.ResultId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "DataQualityResult not found: "+req.ResultId)
		return
	}
	glueWriteJSON(w, http.StatusOK, glueDQResultPayload(res))
}

func handleGlueBatchGetDataQualityResult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResultIds []string `json:"ResultIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var results []map[string]any
	var notFound []string
	for _, id := range req.ResultIds {
		if res, ok := glueDQResults.Get(id); ok {
			results = append(results, glueDQResultPayload(res))
		} else {
			notFound = append(notFound, id)
		}
	}
	resp := map[string]any{"Results": results}
	if len(notFound) > 0 {
		resp["ResultsNotFound"] = notFound
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListDataQualityResults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueDQResults.List()
	descs := make([]map[string]any, 0, len(all))
	for _, res := range all {
		d := map[string]any{
			"ResultId":  res.ResultId,
			"StartedOn": res.StartedOn,
		}
		if res.DataSource != nil {
			d["DataSource"] = res.DataSource
		}
		descs = append(descs, d)
	}
	page, nextTok := awsPage(descs, req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	resp := map[string]any{"Results": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListDataQualityStatistics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	// The simulator computes no statistics over its empty result rows, so the
	// statistic list is empty (but shaped) — no fabricated statistics.
	glueWriteJSON(w, http.StatusOK, map[string]any{"Statistics": []any{}})
}

func handleGlueGetDataQualityModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StatisticId string `json:"StatisticId"`
		ProfileId   string `json:"ProfileId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	// No model is trained without real statistical history; report a terminal
	// SUCCEEDED status with no failure reason — shaped, not fabricated.
	now := glueEpochNow()
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Status":      "SUCCEEDED",
		"StartedOn":   now,
		"CompletedOn": now,
	})
}

func handleGlueGetDataQualityModelResult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StatisticId string `json:"StatisticId"`
		ProfileId   string `json:"ProfileId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	// No model results over empty statistical history.
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"CompletedOn": glueEpochNow(),
		"Model":       []any{},
	})
}

func handleGlueCreateColumnStatisticsTaskSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName          string   `json:"DatabaseName"`
		TableName             string   `json:"TableName"`
		Role                  string   `json:"Role"`
		Schedule              string   `json:"Schedule"`
		ColumnNameList        []string `json:"ColumnNameList"`
		SampleSize            *float64 `json:"SampleSize"`
		CatalogID             string   `json:"CatalogID"`
		SecurityConfiguration string   `json:"SecurityConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	key := glueCSTaskKey(req.DatabaseName, req.TableName)
	if _, ok := glueCSTaskSettgs.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "ColumnStatisticsTaskSettings already exist for "+key)
		return
	}
	settings := glueCSSettingsFromInput(req.DatabaseName, req.TableName, req.Role, req.Schedule, req.ColumnNameList, req.SampleSize, req.CatalogID, req.SecurityConfiguration)
	glueCSTaskSettgs.Put(key, settings)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func glueCSSettingsFromInput(db, table, role, schedule string, cols []string, sampleSize *float64, catalogID, secConfig string) GlueColumnStatsTaskSettings {
	settings := GlueColumnStatsTaskSettings{
		DatabaseName:          db,
		TableName:             table,
		Role:                  role,
		ColumnNameList:        cols,
		SampleSize:            sampleSize,
		CatalogID:             catalogID,
		SecurityConfiguration: secConfig,
		SettingSource:         "TABLE",
	}
	if schedule != "" {
		settings.Schedule = &GlueSchedule{ScheduleExpression: schedule, State: "SCHEDULED"}
		settings.ScheduleType = "CRON"
	}
	return settings
}

func handleGlueGetColumnStatisticsTaskSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	settings, ok := glueCSTaskSettgs.Get(glueCSTaskKey(req.DatabaseName, req.TableName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "ColumnStatisticsTaskSettings not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ColumnStatisticsTaskSettings": settings})
}

func handleGlueUpdateColumnStatisticsTaskSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName          string   `json:"DatabaseName"`
		TableName             string   `json:"TableName"`
		Role                  string   `json:"Role"`
		Schedule              string   `json:"Schedule"`
		ColumnNameList        []string `json:"ColumnNameList"`
		SampleSize            *float64 `json:"SampleSize"`
		CatalogID             string   `json:"CatalogID"`
		SecurityConfiguration string   `json:"SecurityConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueCSTaskKey(req.DatabaseName, req.TableName)
	existing, ok := glueCSTaskSettgs.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "ColumnStatisticsTaskSettings not found")
		return
	}
	if req.Role != "" {
		existing.Role = req.Role
	}
	if req.ColumnNameList != nil {
		existing.ColumnNameList = req.ColumnNameList
	}
	if req.SampleSize != nil {
		existing.SampleSize = req.SampleSize
	}
	if req.CatalogID != "" {
		existing.CatalogID = req.CatalogID
	}
	if req.SecurityConfiguration != "" {
		existing.SecurityConfiguration = req.SecurityConfiguration
	}
	if req.Schedule != "" {
		existing.Schedule = &GlueSchedule{ScheduleExpression: req.Schedule, State: "SCHEDULED"}
		existing.ScheduleType = "CRON"
	}
	glueCSTaskSettgs.Put(key, existing)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteColumnStatisticsTaskSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueCSTaskKey(req.DatabaseName, req.TableName)
	if _, ok := glueCSTaskSettgs.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "ColumnStatisticsTaskSettings not found")
		return
	}
	glueCSTaskSettgs.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStartColumnStatisticsTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName          string   `json:"DatabaseName"`
		TableName             string   `json:"TableName"`
		ColumnNameList        []string `json:"ColumnNameList"`
		Role                  string   `json:"Role"`
		SampleSize            *float64 `json:"SampleSize"`
		CatalogID             string   `json:"CatalogID"`
		SecurityConfiguration string   `json:"SecurityConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	runID := glueHashID()
	now := glueEpochNow()
	run := GlueColumnStatsTaskRun{
		ColumnStatisticsTaskRunId: runID,
		DatabaseName:              req.DatabaseName,
		TableName:                 req.TableName,
		ColumnNameList:            req.ColumnNameList,
		CatalogID:                 req.CatalogID,
		Role:                      req.Role,
		SampleSize:                req.SampleSize,
		SecurityConfiguration:     req.SecurityConfiguration,
		ComputationType:           "FULL",
		Status:                    "SUCCEEDED",
		CreationTime:              now,
		LastUpdated:               now,
		StartTime:                 now,
		EndTime:                   now,
	}
	glueCSTaskRuns.Put(runID, run)
	glueWriteJSON(w, http.StatusOK, map[string]any{"ColumnStatisticsTaskRunId": runID})
}

func handleGlueGetColumnStatisticsTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColumnStatisticsTaskRunId string `json:"ColumnStatisticsTaskRunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueCSTaskRuns.Get(req.ColumnStatisticsTaskRunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "ColumnStatisticsTaskRun not found: "+req.ColumnStatisticsTaskRunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ColumnStatisticsTaskRun": run})
}

func handleGlueGetColumnStatisticsTaskRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var filtered []GlueColumnStatsTaskRun
	for _, run := range glueCSTaskRuns.List() {
		if run.DatabaseName == req.DatabaseName && run.TableName == req.TableName {
			filtered = append(filtered, run)
		}
	}
	page, nextTok := awsPage(filtered, req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	resp := map[string]any{"ColumnStatisticsTaskRuns": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListColumnStatisticsTaskRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueCSTaskRuns.List()
	ids := make([]string, 0, len(all))
	for _, run := range all {
		ids = append(ids, run.ColumnStatisticsTaskRunId)
	}
	page, nextTok := awsPage(ids, req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	resp := map[string]any{"ColumnStatisticsTaskRunIds": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueStopColumnStatisticsTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	// Find the most recent run for this (database, table). A terminal run can't
	// be stopped — the real service raises ColumnStatisticsTaskNotRunningException.
	glueMu.Lock()
	defer glueMu.Unlock()

	var found *GlueColumnStatsTaskRun
	for _, run := range glueCSTaskRuns.List() {
		run := run
		if run.DatabaseName == req.DatabaseName && run.TableName == req.TableName {
			if found == nil || run.CreationTime >= found.CreationTime {
				found = &run
			}
		}
	}
	if found == nil {
		glueWriteError(w, "EntityNotFoundException", "no column-statistics task run for "+glueCSTaskKey(req.DatabaseName, req.TableName))
		return
	}
	if found.Status == "SUCCEEDED" || found.Status == "FAILED" || found.Status == "STOPPED" {
		glueWriteError(w, "ColumnStatisticsTaskNotRunningException", "task run is not running")
		return
	}
	found.Status = "STOPPED"
	found.LastUpdated = glueEpochNow()
	glueCSTaskRuns.Put(found.ColumnStatisticsTaskRunId, *found)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func derefIntDefault(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}
