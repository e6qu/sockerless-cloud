package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// AWS Glue — machine-learning task runs, crawler/column-statistics schedules,
// materialized-view refresh runs, workflow-run properties, trigger listing/update,
// job bookmarks, and the connection-type registry. AWS JSON 1.1.
//
// There is no Spark/crawler compute backend in the simulator, so task runs and
// refresh runs settle to a terminal SUCCEEDED state synchronously with honestly
// empty/zero result shapes — never fabricated metrics. Schedules toggle a stored
// state flag on the existing crawler; column-statistics schedules and bookmarks
// are tracked in dedicated stores keyed by the resources they belong to.

// GlueMLTaskRun models one machine-learning transform task run, keyed by
// TransformId + TaskRunId.
type GlueMLTaskRun struct {
	TransformId    string         `json:"TransformId"`
	TaskRunId      string         `json:"TaskRunId"`
	Status         string         `json:"Status"`
	LogGroupName   string         `json:"LogGroupName,omitempty"`
	Properties     map[string]any `json:"Properties,omitempty"`
	ErrorString    string         `json:"ErrorString,omitempty"`
	StartedOn      float64        `json:"StartedOn"`
	LastModifiedOn float64        `json:"LastModifiedOn"`
	CompletedOn    float64        `json:"CompletedOn"`
	ExecutionTime  int            `json:"ExecutionTime"`
}

// GlueMVRefreshTaskRun models one materialized-view refresh task run, keyed by
// CatalogId + MaterializedViewRefreshTaskRunId.
type GlueMVRefreshTaskRun struct {
	CustomerId                       string  `json:"CustomerId,omitempty"`
	MaterializedViewRefreshTaskRunId string  `json:"MaterializedViewRefreshTaskRunId"`
	DatabaseName                     string  `json:"DatabaseName,omitempty"`
	TableName                        string  `json:"TableName,omitempty"`
	CatalogId                        string  `json:"CatalogId,omitempty"`
	Status                           string  `json:"Status"`
	CreationTime                     float64 `json:"CreationTime"`
	LastUpdated                      float64 `json:"LastUpdated"`
	StartTime                        float64 `json:"StartTime"`
	EndTime                          float64 `json:"EndTime"`
	RefreshType                      string  `json:"RefreshType,omitempty"`
}

// GlueColumnStatsSchedule tracks a column-statistics task run schedule for a
// table, keyed by database/table.
type GlueColumnStatsSchedule struct {
	DatabaseName string `json:"DatabaseName"`
	TableName    string `json:"TableName"`
	State        string `json:"State"`
}

// GlueJobBookmark holds the job bookmark entry keyed by job name. The simulator
// has no incremental ETL backend, so the bookmark is the catalog of resume
// metadata clients write/read; Reset clears the bookmark payload.
type GlueJobBookmark struct {
	JobName     string `json:"JobName"`
	Version     int    `json:"Version"`
	Run         int    `json:"Run"`
	Attempt     int    `json:"Attempt"`
	RunId       string `json:"RunId,omitempty"`
	JobBookmark string `json:"JobBookmark,omitempty"`
}

// GlueConnectionType models a registered connection type keyed by ConnectionType.
type GlueConnectionType struct {
	ConnectionType    string            `json:"ConnectionType"`
	ConnectionTypeArn string            `json:"ConnectionTypeArn"`
	IntegrationType   string            `json:"IntegrationType,omitempty"`
	Description       string            `json:"Description,omitempty"`
	Tags              map[string]string `json:"Tags,omitempty"`
}

var (
	glueMLTaskRuns      sim.Store[GlueMLTaskRun]
	glueMVRefreshRuns   sim.Store[GlueMVRefreshTaskRun]
	glueColStatsScheds  sim.Store[GlueColumnStatsSchedule]
	glueJobBookmarks    sim.Store[GlueJobBookmark]
	glueConnectionTypes sim.Store[GlueConnectionType]
)

// registerGlueMLTasksSchedules registers this Glue sub-service's awsJson1.1 operations.
func registerGlueMLTasksSchedules(r *AWSRouter, srv *sim.Server) {
	glueMLTaskRuns = sim.MakeStore[GlueMLTaskRun](srv.DB(), "glue_ml_task_runs")
	glueMVRefreshRuns = sim.MakeStore[GlueMVRefreshTaskRun](srv.DB(), "glue_mv_refresh_runs")
	glueColStatsScheds = sim.MakeStore[GlueColumnStatsSchedule](srv.DB(), "glue_column_stats_schedules")
	glueJobBookmarks = sim.MakeStore[GlueJobBookmark](srv.DB(), "glue_job_bookmarks")
	glueConnectionTypes = sim.MakeStore[GlueConnectionType](srv.DB(), "glue_connection_types")

	r.Register("AWSGlue.StartMLEvaluationTaskRun", glueStartMLTaskRunHandler("EVALUATION"))
	r.Register("AWSGlue.StartMLLabelingSetGenerationTaskRun", glueStartMLTaskRunHandler("LABELING_SET_GENERATION"))
	r.Register("AWSGlue.StartExportLabelsTaskRun", glueStartMLTaskRunHandler("EXPORT_LABELS"))
	r.Register("AWSGlue.StartImportLabelsTaskRun", glueStartMLTaskRunHandler("IMPORT_LABELS"))
	r.Register("AWSGlue.GetMLTaskRun", handleGlueGetMLTaskRun)
	r.Register("AWSGlue.GetMLTaskRuns", handleGlueGetMLTaskRuns)
	r.Register("AWSGlue.CancelMLTaskRun", handleGlueCancelMLTaskRun)

	r.Register("AWSGlue.StartColumnStatisticsTaskRunSchedule", handleGlueStartColumnStatisticsTaskRunSchedule)
	r.Register("AWSGlue.StopColumnStatisticsTaskRunSchedule", handleGlueStopColumnStatisticsTaskRunSchedule)
	r.Register("AWSGlue.StartCrawlerSchedule", handleGlueStartCrawlerSchedule)
	r.Register("AWSGlue.StopCrawlerSchedule", handleGlueStopCrawlerSchedule)
	r.Register("AWSGlue.UpdateCrawlerSchedule", handleGlueUpdateCrawlerSchedule)
	r.Register("AWSGlue.GetCrawlerMetrics", handleGlueGetCrawlerMetrics)
	r.Register("AWSGlue.ListCrawls", handleGlueListCrawls)

	r.Register("AWSGlue.StartMaterializedViewRefreshTaskRun", handleGlueStartMaterializedViewRefreshTaskRun)
	r.Register("AWSGlue.GetMaterializedViewRefreshTaskRun", handleGlueGetMaterializedViewRefreshTaskRun)
	r.Register("AWSGlue.StopMaterializedViewRefreshTaskRun", handleGlueStopMaterializedViewRefreshTaskRun)
	r.Register("AWSGlue.ListMaterializedViewRefreshTaskRuns", handleGlueListMaterializedViewRefreshTaskRuns)

	r.Register("AWSGlue.GetWorkflowRuns", handleGlueGetWorkflowRuns)
	r.Register("AWSGlue.GetWorkflowRunProperties", handleGlueGetWorkflowRunProperties)
	r.Register("AWSGlue.PutWorkflowRunProperties", handleGluePutWorkflowRunProperties)
	r.Register("AWSGlue.StopWorkflowRun", handleGlueStopWorkflowRun)
	r.Register("AWSGlue.ResumeWorkflowRun", handleGlueResumeWorkflowRun)
	r.Register("AWSGlue.UpdateWorkflow", handleGlueUpdateWorkflow)

	r.Register("AWSGlue.ListTriggers", handleGlueListTriggers)
	r.Register("AWSGlue.UpdateTrigger", handleGlueUpdateTrigger)

	r.Register("AWSGlue.GetJobBookmark", handleGlueGetJobBookmark)
	r.Register("AWSGlue.ResetJobBookmark", handleGlueResetJobBookmark)

	r.Register("AWSGlue.RegisterConnectionType", handleGlueRegisterConnectionType)
	r.Register("AWSGlue.DescribeConnectionType", handleGlueDescribeConnectionType)
	r.Register("AWSGlue.ListConnectionTypes", handleGlueListConnectionTypes)
	r.Register("AWSGlue.DeleteConnectionType", handleGlueDeleteConnectionType)
	r.Register("AWSGlue.TestConnection", handleGlueTestConnection)
}

// glueMLTaskRunKey scopes a task run to its transform.
func glueMLTaskRunKey(transformID, taskRunID string) string {
	return transformID + "\x00" + taskRunID
}

// glueStartMLTaskRunHandler returns a handler that starts a task run of the given
// TaskType against an existing ML transform and settles it to SUCCEEDED.
func glueStartMLTaskRunHandler(taskType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TransformId      string `json:"TransformId"`
			OutputS3Path     string `json:"OutputS3Path"`
			InputS3Path      string `json:"InputS3Path"`
			ReplaceAllLabels *bool  `json:"ReplaceAllLabels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			glueWriteError(w, "InvalidInputException", "invalid JSON")
			return
		}
		if req.TransformId == "" {
			glueWriteError(w, "InvalidInputException", "TransformId is required")
			return
		}

		glueMu.Lock()
		defer glueMu.Unlock()

		if _, ok := glueMLTransforms.Get(req.TransformId); !ok {
			glueWriteError(w, "EntityNotFoundException", "Transform not found: "+req.TransformId)
			return
		}
		taskRunID := strings.ReplaceAll(uuid.NewString(), "-", "")
		now := glueEpochNow()
		props := map[string]any{"TaskType": taskType}
		switch taskType {
		case "LABELING_SET_GENERATION":
			props["LabelingSetGenerationTaskRunProperties"] = map[string]any{"OutputS3Path": req.OutputS3Path}
		case "EXPORT_LABELS":
			props["ExportLabelsTaskRunProperties"] = map[string]any{"OutputS3Path": req.OutputS3Path}
		case "IMPORT_LABELS":
			imp := map[string]any{"InputS3Path": req.InputS3Path}
			if req.ReplaceAllLabels != nil {
				imp["Replace"] = *req.ReplaceAllLabels
			}
			props["ImportLabelsTaskRunProperties"] = imp
		}
		run := GlueMLTaskRun{
			TransformId:    req.TransformId,
			TaskRunId:      taskRunID,
			Status:         "SUCCEEDED",
			Properties:     props,
			StartedOn:      now,
			LastModifiedOn: now,
			CompletedOn:    now,
			ExecutionTime:  0,
		}
		glueMLTaskRuns.Put(glueMLTaskRunKey(req.TransformId, taskRunID), run)
		glueWriteJSON(w, http.StatusOK, map[string]any{"TaskRunId": taskRunID})
	}
}

func handleGlueGetMLTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransformId string `json:"TransformId"`
		TaskRunId   string `json:"TaskRunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueMLTaskRuns.Get(glueMLTaskRunKey(req.TransformId, req.TaskRunId))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Task run not found: "+req.TaskRunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, run)
}

func handleGlueGetMLTaskRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransformId string `json:"TransformId"`
		NextToken   string `json:"NextToken"`
		MaxResults  *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.TransformId == "" {
		glueWriteError(w, "InvalidInputException", "TransformId is required")
		return
	}
	var runs []GlueMLTaskRun
	for _, run := range glueMLTaskRuns.List() {
		if run.TransformId == req.TransformId {
			runs = append(runs, run)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(runs, req.NextToken, maxR, 20)
	resp := map[string]any{"TaskRuns": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueCancelMLTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransformId string `json:"TransformId"`
		TaskRunId   string `json:"TaskRunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueMLTaskRunKey(req.TransformId, req.TaskRunId)
	run, ok := glueMLTaskRuns.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Task run not found: "+req.TaskRunId)
		return
	}
	run.Status = "STOPPED"
	run.LastModifiedOn = glueEpochNow()
	glueMLTaskRuns.Put(key, run)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"TransformId": req.TransformId,
		"TaskRunId":   req.TaskRunId,
		"Status":      "STOPPED",
	})
}

func glueColStatsScheduleKey(database, table string) string {
	return database + "\x00" + table
}

func handleGlueStartColumnStatisticsTaskRunSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
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
	glueColStatsScheds.Put(glueColStatsScheduleKey(req.DatabaseName, req.TableName), GlueColumnStatsSchedule{
		DatabaseName: req.DatabaseName,
		TableName:    req.TableName,
		State:        "SCHEDULED",
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStopColumnStatisticsTaskRunSchedule(w http.ResponseWriter, r *http.Request) {
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

	key := glueColStatsScheduleKey(req.DatabaseName, req.TableName)
	sched, ok := glueColStatsScheds.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "No column statistics schedule for table: "+req.TableName)
		return
	}
	sched.State = "NOT_SCHEDULED"
	glueColStatsScheds.Put(key, sched)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStartCrawlerSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CrawlerName string `json:"CrawlerName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.CrawlerName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.CrawlerName)
		return
	}
	if crawler.Schedule == nil || crawler.Schedule.ScheduleExpression == "" {
		glueWriteError(w, "NoScheduleException", "There is no schedule to start for crawler: "+req.CrawlerName)
		return
	}
	crawler.Schedule.State = "SCHEDULED"
	crawler.LastUpdated = glueEpochNow()
	glueCrawlers.Put(req.CrawlerName, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStopCrawlerSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CrawlerName string `json:"CrawlerName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.CrawlerName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.CrawlerName)
		return
	}
	if crawler.Schedule != nil {
		crawler.Schedule.State = "NOT_SCHEDULED"
		crawler.LastUpdated = glueEpochNow()
		glueCrawlers.Put(req.CrawlerName, crawler)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueUpdateCrawlerSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CrawlerName string `json:"CrawlerName"`
		Schedule    string `json:"Schedule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.CrawlerName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.CrawlerName)
		return
	}
	if req.Schedule == "" {
		// An empty schedule removes the schedule from the crawler.
		crawler.Schedule = nil
	} else {
		crawler.Schedule = &GlueSchedule{ScheduleExpression: req.Schedule, State: "SCHEDULED"}
	}
	crawler.LastUpdated = glueEpochNow()
	glueCrawlers.Put(req.CrawlerName, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetCrawlerMetrics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CrawlerNameList []string `json:"CrawlerNameList"`
		NextToken       string   `json:"NextToken"`
		MaxResults      *int     `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var names []string
	if len(req.CrawlerNameList) > 0 {
		names = req.CrawlerNameList
	} else {
		for _, c := range glueCrawlers.List() {
			names = append(names, c.Name)
		}
	}
	// Metrics for stored crawlers only; the simulator runs no crawl, so the
	// runtime/table counters are honestly zero.
	var metrics []map[string]any
	for _, name := range names {
		if _, ok := glueCrawlers.Get(name); !ok {
			continue
		}
		metrics = append(metrics, map[string]any{
			"CrawlerName":          name,
			"TimeLeftSeconds":      0,
			"StillEstimating":      false,
			"LastRuntimeSeconds":   0,
			"MedianRuntimeSeconds": 0,
			"TablesCreated":        0,
			"TablesUpdated":        0,
			"TablesDeleted":        0,
		})
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(metrics, req.NextToken, maxR, 100)
	resp := map[string]any{"CrawlerMetricsList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListCrawls(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CrawlerName string `json:"CrawlerName"`
		NextToken   string `json:"NextToken"`
		MaxResults  *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.CrawlerName == "" {
		glueWriteError(w, "InvalidInputException", "CrawlerName is required")
		return
	}
	if _, ok := glueCrawlers.Get(req.CrawlerName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.CrawlerName)
		return
	}
	// The simulator runs no crawls; StartCrawler does not produce a history row,
	// so ListCrawls honestly returns an empty crawl history for the crawler.
	var crawls []map[string]any
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(crawls, req.NextToken, maxR, 20)
	resp := map[string]any{"Crawls": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func glueMVRefreshKey(catalogID, runID string) string {
	return catalogID + "\x00" + runID
}

func handleGlueStartMaterializedViewRefreshTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		FullRefresh  *bool  `json:"FullRefresh"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.CatalogId == "" || req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "CatalogId, DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	runID := uuid.NewString()
	now := glueEpochNow()
	refreshType := "INCREMENTAL"
	if req.FullRefresh != nil && *req.FullRefresh {
		refreshType = "FULL"
	}
	// No materialized-view compute backend; the refresh settles synchronously.
	glueMVRefreshRuns.Put(glueMVRefreshKey(req.CatalogId, runID), GlueMVRefreshTaskRun{
		CustomerId:                       req.CatalogId,
		MaterializedViewRefreshTaskRunId: runID,
		DatabaseName:                     req.DatabaseName,
		TableName:                        req.TableName,
		CatalogId:                        req.CatalogId,
		Status:                           "SUCCEEDED",
		CreationTime:                     now,
		LastUpdated:                      now,
		StartTime:                        now,
		EndTime:                          now,
		RefreshType:                      refreshType,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"MaterializedViewRefreshTaskRunId": runID})
}

func handleGlueGetMaterializedViewRefreshTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId                        string `json:"CatalogId"`
		MaterializedViewRefreshTaskRunId string `json:"MaterializedViewRefreshTaskRunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueMVRefreshRuns.Get(glueMVRefreshKey(req.CatalogId, req.MaterializedViewRefreshTaskRunId))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Refresh task run not found: "+req.MaterializedViewRefreshTaskRunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"MaterializedViewRefreshTaskRun": run})
}

func handleGlueStopMaterializedViewRefreshTaskRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	// Mark the most recent non-terminal run for the (catalog, db, table) STOPPED.
	stopped := false
	for key, run := range glueMVRefreshRunsByCatalog(req.CatalogId) {
		if run.DatabaseName == req.DatabaseName && run.TableName == req.TableName {
			run.Status = "STOPPED"
			run.LastUpdated = glueEpochNow()
			run.EndTime = run.LastUpdated
			glueMVRefreshRuns.Put(key, run)
			stopped = true
		}
	}
	if !stopped {
		glueWriteError(w, "EntityNotFoundException", "No refresh task run for table: "+req.TableName)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueMVRefreshRunsByCatalog returns key→run for all stored refresh runs in a
// catalog. Caller holds glueMu.
func glueMVRefreshRunsByCatalog(catalogID string) map[string]GlueMVRefreshTaskRun {
	out := map[string]GlueMVRefreshTaskRun{}
	for _, run := range glueMVRefreshRuns.List() {
		if run.CatalogId == catalogID {
			out[glueMVRefreshKey(catalogID, run.MaterializedViewRefreshTaskRunId)] = run
		}
	}
	return out
}

func handleGlueListMaterializedViewRefreshTaskRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.CatalogId == "" {
		glueWriteError(w, "InvalidInputException", "CatalogId is required")
		return
	}
	var runs []GlueMVRefreshTaskRun
	for _, run := range glueMVRefreshRuns.List() {
		if run.CatalogId != req.CatalogId {
			continue
		}
		if req.DatabaseName != "" && run.DatabaseName != req.DatabaseName {
			continue
		}
		if req.TableName != "" && run.TableName != req.TableName {
			continue
		}
		runs = append(runs, run)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(runs, req.NextToken, maxR, 20)
	resp := map[string]any{"MaterializedViewRefreshTaskRuns": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueWfRunKey mirrors the key scheme used by StartWorkflowRun in glue.go.
func glueWfRunKey(name, runID string) string {
	return name + "\x00" + runID
}

func handleGlueGetWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"Name"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}
	if _, ok := glueWorkflows.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	var runs []GlueWorkflowRun
	for _, run := range glueWfRuns.List() {
		if run.Name == req.Name {
			runs = append(runs, run)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(runs, req.NextToken, maxR, 25)
	resp := map[string]any{"Runs": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueGetWorkflowRunProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"Name"`
		RunId string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueWfRuns.Get(glueWfRunKey(req.Name, req.RunId))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow run not found: "+req.RunId)
		return
	}
	props := run.WorkflowRunProperties
	if props == nil {
		props = map[string]string{}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"RunProperties": props})
}

func handleGluePutWorkflowRunProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string            `json:"Name"`
		RunId         string            `json:"RunId"`
		RunProperties map[string]string `json:"RunProperties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueWfRunKey(req.Name, req.RunId)
	run, ok := glueWfRuns.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow run not found: "+req.RunId)
		return
	}
	run.WorkflowRunProperties = req.RunProperties
	glueWfRuns.Put(key, run)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStopWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"Name"`
		RunId string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueWfRunKey(req.Name, req.RunId)
	run, ok := glueWfRuns.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow run not found: "+req.RunId)
		return
	}
	run.Status = "STOPPED"
	run.CompletedOn = glueEpochNow()
	glueWfRuns.Put(key, run)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueResumeWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"Name"`
		RunId   string   `json:"RunId"`
		NodeIds []string `json:"NodeIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	prior, ok := glueWfRuns.Get(glueWfRunKey(req.Name, req.RunId))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow run not found: "+req.RunId)
		return
	}
	// Each resume creates a new run with a fresh ID; it settles synchronously.
	newRunID := "wr_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := glueEpochNow()
	glueWfRuns.Put(glueWfRunKey(req.Name, newRunID), GlueWorkflowRun{
		Name:                  req.Name,
		WorkflowRunId:         newRunID,
		WorkflowRunProperties: prior.WorkflowRunProperties,
		StartedOn:             now,
		CompletedOn:           now,
		Status:                "COMPLETED",
	})
	resp := map[string]any{"RunId": newRunID}
	if len(req.NodeIds) > 0 {
		resp["NodeIds"] = req.NodeIds
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                 string            `json:"Name"`
		Description          string            `json:"Description"`
		DefaultRunProperties map[string]string `json:"DefaultRunProperties"`
		MaxConcurrentRuns    *int              `json:"MaxConcurrentRuns"`
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

	wf, ok := glueWorkflows.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	wf.Description = req.Description
	if req.DefaultRunProperties != nil {
		wf.DefaultRunProperties = req.DefaultRunProperties
	}
	if req.MaxConcurrentRuns != nil {
		wf.MaxConcurrentRuns = req.MaxConcurrentRuns
	}
	wf.LastModifiedOn = glueEpochNow()
	glueWorkflows.Put(req.Name, wf)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueListTriggers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken        string `json:"NextToken"`
		DependentJobName string `json:"DependentJobName"`
		MaxResults       *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var names []string
	for _, t := range glueTriggers.List() {
		names = append(names, t.Name)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(names, req.NextToken, maxR, 25)
	resp := map[string]any{"TriggerNames": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"Name"`
		TriggerUpdate struct {
			Name        string           `json:"Name"`
			Description string           `json:"Description"`
			Schedule    string           `json:"Schedule"`
			Actions     []map[string]any `json:"Actions"`
			Predicate   map[string]any   `json:"Predicate"`
		} `json:"TriggerUpdate"`
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

	trigger, ok := glueTriggers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Trigger not found: "+req.Name)
		return
	}
	upd := req.TriggerUpdate
	trigger.Description = upd.Description
	if upd.Schedule != "" {
		trigger.Schedule = upd.Schedule
	}
	if upd.Actions != nil {
		trigger.Actions = upd.Actions
	}
	if upd.Predicate != nil {
		trigger.Predicate = upd.Predicate
	}
	glueTriggers.Put(req.Name, trigger)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Trigger": glueTriggerWire{trigger}})
}

func handleGlueGetJobBookmark(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
		RunId   string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.JobName == "" {
		glueWriteError(w, "InvalidInputException", "JobName is required")
		return
	}
	if _, ok := glueJobs.Get(req.JobName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	bm, ok := glueJobBookmarks.Get(req.JobName)
	if !ok {
		// No bookmark recorded yet; AWS returns a zeroed entry for the job.
		bm = GlueJobBookmark{JobName: req.JobName}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobBookmarkEntry": bm})
}

func handleGlueResetJobBookmark(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
		RunId   string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.JobName == "" {
		glueWriteError(w, "InvalidInputException", "JobName is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueJobs.Get(req.JobName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	// Reset clears the bookmark payload back to the initial state for the job.
	bm := GlueJobBookmark{JobName: req.JobName}
	glueJobBookmarks.Put(req.JobName, bm)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobBookmarkEntry": bm})
}

func handleGlueRegisterConnectionType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionType  string            `json:"ConnectionType"`
		IntegrationType string            `json:"IntegrationType"`
		Description     string            `json:"Description"`
		Tags            map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ConnectionType == "" {
		glueWriteError(w, "InvalidInputException", "ConnectionType is required")
		return
	}
	if req.IntegrationType == "" {
		glueWriteError(w, "InvalidInputException", "IntegrationType is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueConnectionTypes.Get(req.ConnectionType); ok {
		glueWriteError(w, "AlreadyExistsException", "Connection type already exists: "+req.ConnectionType)
		return
	}
	arn := "arn:aws:glue:us-east-1:000000000000:connectionType/" + req.ConnectionType
	glueConnectionTypes.Put(req.ConnectionType, GlueConnectionType{
		ConnectionType:    req.ConnectionType,
		ConnectionTypeArn: arn,
		IntegrationType:   req.IntegrationType,
		Description:       req.Description,
		Tags:              req.Tags,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"ConnectionTypeArn": arn})
}

func handleGlueDescribeConnectionType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionType string `json:"ConnectionType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	ct, ok := glueConnectionTypes.Get(req.ConnectionType)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection type not found: "+req.ConnectionType)
		return
	}
	resp := map[string]any{
		"ConnectionType": ct.ConnectionType,
		"Capabilities": map[string]any{
			"SupportedAuthenticationTypes": []string{"BASIC"},
			"SupportedDataOperations":      []string{"READ"},
			"SupportedComputeEnvironments": []string{"SPARK"},
		},
	}
	if ct.Description != "" {
		resp["Description"] = ct.Description
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueSupportedConnectionTypes is the connection-type catalogue AWS Glue
// publishes, transcribed from the ConnectionType enum the vendored model
// declares — the same kind of transcription as the AWS Lambda runtime images
// and the ElastiCache engine versions. It is not invented: the model is where
// the list comes from, and the list is what ListConnectionTypes returns.
var glueSupportedConnectionTypes = []string{
	"ADOBEANALYTICS", "ASANA", "AZURECOSMOS", "AZURESQL",
	"BIGQUERY", "BLACKBAUD", "BLACKBAUDRAISEREDGENXT", "CIRCLECI",
	"CLOUDERAHIVE", "CLOUDERAIMPALA", "CLOUDWATCH", "CLOUDWATCHMETRICS",
	"CMDB", "CUSTOM", "DATADOG", "DATALAKEGEN2",
	"DB2", "DB2AS400", "DOCUMENTDB", "DOCUSIGNMONITOR",
	"DOMO", "DYNAMODB", "DYNATRACE", "FACEBOOKADS",
	"FACEBOOKPAGEINSIGHTS", "FRESHDESK", "FRESHSALES", "GITLAB",
	"GOOGLEADS", "GOOGLEANALYTICS4", "GOOGLECLOUDSTORAGE", "GOOGLESEARCHCONSOLE",
	"GOOGLESHEETS", "HBASE", "HUBSPOT", "INSTAGRAMADS",
	"INTERCOM", "JDBC", "JIRACLOUD", "KAFKA",
	"KUSTOMER", "LINKEDIN", "MAILCHIMP", "MARKETO",
	"MARKETPLACE", "MICROSOFTDYNAMIC365FINANCEANDOPS", "MICROSOFTDYNAMICS365CRM", "MICROSOFTTEAMS",
	"MIXPANEL", "MONDAY", "MONGODB", "MYSQL",
	"NETSUITEERP", "NETWORK", "OKTA", "OPENSEARCH",
	"ORACLE", "PAYPAL", "PENDO", "PIPEDIVE",
	"PIPEDRIVE", "POSTGRESQL", "PRODUCTBOARD", "QUICKBOOKS",
	"SALESFORCE", "SALESFORCECOMMERCECLOUD", "SALESFORCEMARKETINGCLOUD", "SALESFORCEPARDOT",
	"SAPCONCUR", "SAPHANA", "SAPODATA", "SENDGRID",
	"SERVICENOW", "SFTP", "SLACK", "SMARTSHEET",
	"SNAPCHATADS", "SQLSERVER", "STRIPE", "SYNAPSE",
	"TERADATA", "TERADATANOS", "TIMESTREAM", "TPCDS",
	"TWILIO", "VERTICA", "VIEW_VALIDATION_ATHENA", "VIEW_VALIDATION_REDSHIFT",
	"WOOCOMMERCE", "ZENDESK", "ZOHOCRM", "ZOOM",
}

func handleGlueListConnectionTypes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	// The connection types AWS Glue supports, which is what this operation
	// lists: ConnectionTypeBrief.ConnectionType is the model's ConnectionType
	// enum, so a name outside it cannot appear here. Registering a custom
	// connector takes a free-form NameString and describes back as one, and
	// those are the caller's own — they are not this catalogue, and listing
	// them here put a value in a field whose type cannot hold it.
	//
	// Only the names are answered. The rest of a brief — the vendor, the
	// display name, the logo, the categories and capabilities — is AWS's own
	// catalogue copy, which this simulator does not have and will not invent;
	// every one of those members is optional.
	briefs := make([]map[string]any, 0, len(glueSupportedConnectionTypes))
	for _, name := range glueSupportedConnectionTypes {
		briefs = append(briefs, map[string]any{"ConnectionType": name})
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(briefs, req.NextToken, maxR, 50)
	resp := map[string]any{"ConnectionTypes": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteConnectionType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionType string `json:"ConnectionType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueConnectionTypes.Get(req.ConnectionType); !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection type not found: "+req.ConnectionType)
		return
	}
	glueConnectionTypes.Delete(req.ConnectionType)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueTestConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionName string `json:"ConnectionName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ConnectionName == "" {
		glueWriteError(w, "InvalidInputException", "ConnectionName is required")
		return
	}
	// A named connection must exist for the test to validate; the simulator has
	// no real network reachability backend, so a stored connection tests OK.
	if _, ok := glueConnections.Get(req.ConnectionName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection not found: "+req.ConnectionName)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}
